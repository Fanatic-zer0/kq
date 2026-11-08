package query

import (
	"context"
	"fmt"
	"sort"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (e *Engine) resolveCustomTarget(ctx context.Context, object *unstructured.Unstructured, details *model.CustomResourceDetails, builder *graph.Builder, request Request) []model.QueryWarning {
	var warnings []model.QueryWarning
	target := reference(object)
	if !isExcluded(request.ExcludedKinds, "CustomResourceDefinition") {
		resource := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
		name := request.Resource.Resource + "." + request.Resource.Group
		crd, err := e.client.Get(ctx, resource, "", name)
		if err != nil {
			warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: resource.GroupVersion().String(), Kind: "CustomResourceDefinition", Name: name}, "custom resource definition lookup", err))
		} else {
			node := project(crd, "CustomResourceDefinition", resource.GroupVersion().String(), false)
			builder.AddNode(node)
			builder.AddEdge(model.Edge{FromUID: target.UID, ToUID: node.Ref.UID, From: refPointer(target), To: refPointer(node.Ref), Type: model.RelationDefinition})
			for _, value := range nestedSlice(crd.Object, "spec", "versions") {
				version, ok := value.(map[string]any)
				if !ok || stringValue(version["name"]) != request.Resource.Version {
					continue
				}
				root, _, _ := unstructured.NestedMap(version, "schema", "openAPIV3Schema")
				budget := 512
				collectCustomSchema(root, "", 0, &budget, &details.SchemaFields)
				break
			}
		}
	}
	refs := make(map[string]model.ResourceRef)
	budget := 512
	for _, section := range []string{"config", "spec", "status"} {
		collectCustomReferences(object.Object[section], target.Namespace, 0, &budget, refs)
	}
	if len(refs) >= 8 || budget <= 0 {
		warnings = append(warnings, model.QueryWarning{Resource: target, Operation: "custom resource reference limit", Message: "Reference scanning is bounded to eight unique references and 512 entries; additional references may be omitted."})
	}
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var tasks []func() []model.QueryWarning
	var discovered bool
	var discoveryErr error
	var resourcesAvailable []*metav1.APIResourceList
	for _, key := range keys {
		ref := refs[key]
		if isExcluded(request.ExcludedKinds, ref.Kind) {
			continue
		}
		resource, namespaced, known := knownOwnerResource(ref.APIVersion, ref.Kind)
		if !known {
			if !discovered {
				discovered = true
				resourcesAvailable, discoveryErr = e.client.APIResources(ctx)
			}
			if discoveryErr == nil {
				resource, namespaced, known = findResource(resourcesAvailable, ref.APIVersion, ref.Kind)
			}
		}
		if !known {
			warnings = append(warnings, warningForRef(ref, "custom resource reference discovery", fmt.Errorf("cannot resolve API resource for %s/%s: %v", ref.APIVersion, ref.Kind, discoveryErr)))
			continue
		}
		if !namespaced {
			ref.Namespace = ""
		}
		tasks = append(tasks, func() []model.QueryWarning {
			if ref.Kind == "Secret" && !request.ShowSecretKeys {
				builder.AddNode(model.ResourceNode{Ref: ref})
				builder.AddEdge(model.Edge{FromUID: target.UID, From: refPointer(target), To: refPointer(ref), Type: model.RelationSecret})
				return nil
			}
			linked, err := e.client.Get(ctx, resource, ref.Namespace, ref.Name)
			if err != nil {
				return []model.QueryWarning{warningForRef(ref, "custom resource reference lookup", err)}
			}
			node := project(linked, ref.Kind, ref.APIVersion, request.ShowSecretKeys)
			builder.AddNode(node)
			builder.AddEdge(model.Edge{FromUID: target.UID, ToUID: node.Ref.UID, From: refPointer(target), To: refPointer(node.Ref), Type: model.RelationReference})
			return nil
		})
	}
	return append(warnings, e.runConcurrently(ctx, tasks)...)
}

func collectCustomReferences(value any, namespace string, depth int, budget *int, refs map[string]model.ResourceRef) {
	if depth > 5 || *budget <= 0 || len(refs) >= 8 {
		return
	}
	*budget--
	switch typed := value.(type) {
	case map[string]any:
		ref := model.ResourceRef{APIVersion: stringValue(typed["apiVersion"]), Kind: stringValue(typed["kind"]), Name: stringValue(typed["name"]), Namespace: stringValue(typed["namespace"])}
		if ref.APIVersion != "" && ref.Kind != "" && ref.Name != "" {
			if ref.Namespace == "" {
				ref.Namespace = namespace
			}
			refs[ref.APIVersion+"/"+ref.Kind+"/"+ref.Namespace+"/"+ref.Name] = ref
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if *budget <= 0 || len(refs) >= 8 {
				break
			}
			if !isSensitiveArgument(key) {
				collectCustomReferences(typed[key], namespace, depth+1, budget, refs)
			}
		}
	case []any:
		for _, item := range typed {
			if *budget <= 0 || len(refs) >= 8 {
				break
			}
			collectCustomReferences(item, namespace, depth+1, budget, refs)
		}
	}
}

func collectCustomSchema(value map[string]any, path string, depth int, budget *int, fields *[]model.CustomSchemaField) {
	if depth > 5 || *budget <= 0 || len(*fields) >= 100 {
		return
	}
	*budget--
	if path != "" {
		description := []rune(stringValue(value["description"]))
		if len(description) > 256 {
			description = description[:256]
		}
		*fields = append(*fields, model.CustomSchemaField{Path: path, Type: stringValue(value["type"]), Description: string(description)})
	}
	properties, _, _ := unstructured.NestedMap(value, "properties")
	keys := make([]string, 0, len(properties))
	for key := range properties {
		if path != "" || key == "config" || key == "spec" || key == "status" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if *budget <= 0 || len(*fields) >= 100 {
			break
		}
		child, ok := properties[key].(map[string]any)
		if !ok {
			continue
		}
		childPath := key
		if path != "" {
			childPath = path + "." + key
		}
		collectCustomSchema(child, childPath, depth+1, budget, fields)
	}
	if items, ok := value["items"].(map[string]any); ok {
		collectCustomSchema(items, path+"[]", depth+1, budget, fields)
	}
}
