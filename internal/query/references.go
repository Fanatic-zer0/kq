package query

import (
	"context"
	"fmt"
	"sync"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (e *Engine) resolveConfigOrSecretTarget(ctx context.Context, target *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "Pod") {
		return nil
	}
	resource := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	pods, err := e.listAll(ctx, resource, target.GetNamespace(), metav1.ListOptions{Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningFor(target, "list referencing Pods", err)}
	}
	targetRef := reference(target)
	var warnings []model.QueryWarning
	var referencingPods []*unstructured.Unstructured
	for i := range pods {
		pod := &pods[i]
		locations := podReferenceLocations(pod, target.GetKind(), target.GetName(), request.ShowSecretKeys)
		if len(locations) == 0 {
			continue
		}
		podNode := project(pod, "Pod", "v1", request.ShowSecretKeys)
		builder.AddNode(podNode)
		for _, location := range locations {
			relation := model.RelationConfig
			if target.GetKind() == "Secret" {
				relation = model.RelationSecret
			}
			builder.AddEdge(model.Edge{
				FromUID: podNode.Ref.UID, ToUID: targetRef.UID,
				From: refPointer(podNode.Ref), To: refPointer(targetRef), Type: relation, Detail: location,
			})
		}
		referencingPods = append(referencingPods, pod)
	}
	warnings = append(warnings, e.addOwnerChainsForPods(ctx, referencingPods, builder, request)...)
	return warnings
}

func (e *Engine) resolvePVCUsers(ctx context.Context, claim *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "Pod") {
		return nil
	}
	pods, err := e.listAll(ctx, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, claim.GetNamespace(), metav1.ListOptions{Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningFor(claim, "list Pods using PVC", err)}
	}
	claimRef := reference(claim)
	var referencingPods []*unstructured.Unstructured
	for i := range pods {
		pod := &pods[i]
		volumes := nestedSlice(pod.Object, "spec", "volumes")
		for volumeIndex, value := range volumes {
			volume, ok := value.(map[string]any)
			if !ok || nestedString(volume, "persistentVolumeClaim", "claimName") != claim.GetName() {
				continue
			}
			podNode := project(pod, "Pod", "v1", request.ShowSecretKeys)
			builder.AddNode(podNode)
			builder.AddEdge(model.Edge{
				FromUID: podNode.Ref.UID, ToUID: claimRef.UID,
				From: refPointer(podNode.Ref), To: refPointer(claimRef), Type: model.RelationVolume,
				Detail: fmt.Sprintf("spec.volumes[%d](%s).persistentVolumeClaim", volumeIndex, stringValue(volume["name"])),
			})
			referencingPods = append(referencingPods, pod)
		}
	}
	return e.addOwnerChainsForPods(ctx, referencingPods, builder, request)
}

func (e *Engine) resolvePVTarget(ctx context.Context, volume *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "PersistentVolumeClaim") {
		return nil
	}
	claimName := nestedString(volume.Object, "spec", "claimRef", "name")
	claimNamespace := nestedString(volume.Object, "spec", "claimRef", "namespace")
	if claimName == "" || claimNamespace == "" {
		return nil
	}
	resource := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	claim, err := e.client.Get(ctx, resource, claimNamespace, claimName)
	if err != nil {
		return []model.QueryWarning{warningForRef(model.ResourceRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: claimNamespace, Name: claimName}, "get bound PVC", err)}
	}
	claimNode := project(claim, "PersistentVolumeClaim", "v1", request.ShowSecretKeys)
	volumeNode := project(volume, "PersistentVolume", "v1", request.ShowSecretKeys)
	builder.AddNode(claimNode)
	builder.AddEdge(model.Edge{
		FromUID: claimNode.Ref.UID, ToUID: volumeNode.Ref.UID,
		From: refPointer(claimNode.Ref), To: refPointer(volumeNode.Ref), Type: model.RelationVolume,
	})
	return e.resolvePVCUsers(ctx, claim, builder, request)
}

func podReferenceLocations(pod *unstructured.Unstructured, kind, name string, showSecretKeys bool) []string {
	var locations []string
	isSecret := kind == "Secret"
	for volumeIndex, value := range nestedSlice(pod.Object, "spec", "volumes") {
		volume, ok := value.(map[string]any)
		if !ok {
			continue
		}
		volumeName := stringValue(volume["name"])
		prefix := fmt.Sprintf("spec.volumes[%d](%s)", volumeIndex, volumeName)
		if isSecret && nestedString(volume, "secret", "secretName") == name {
			locations = append(locations, prefix+".secret")
		}
		if !isSecret && nestedString(volume, "configMap", "name") == name {
			locations = append(locations, prefix+".configMap")
		}
		for sourceIndex, sourceValue := range nestedSlice(volume, "projected", "sources") {
			source, ok := sourceValue.(map[string]any)
			if !ok {
				continue
			}
			sourcePrefix := fmt.Sprintf("%s.projected.sources[%d]", prefix, sourceIndex)
			if isSecret && nestedString(source, "secret", "name") == name {
				location := sourcePrefix + ".secret"
				if showSecretKeys {
					for _, itemValue := range nestedSlice(source, "secret", "items") {
						if item, ok := itemValue.(map[string]any); ok {
							if key := stringValue(item["key"]); key != "" {
								location += " key=" + key
							}
						}
					}
				}
				locations = append(locations, location)
			}
			if !isSecret && nestedString(source, "configMap", "name") == name {
				locations = append(locations, sourcePrefix+".configMap")
			}
		}
	}

	containerGroups := []struct {
		field string
		items []any
	}{}
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		containerGroups = append(containerGroups, struct {
			field string
			items []any
		}{field: field, items: nestedSlice(pod.Object, "spec", field)})
	}
	for _, group := range containerGroups {
		for containerIndex, value := range group.items {
			container, ok := value.(map[string]any)
			if !ok {
				continue
			}
			containerPrefix := fmt.Sprintf("spec.%s[%d](%s)", group.field, containerIndex, stringValue(container["name"]))
			for envIndex, envValue := range nestedSlice(container, "env") {
				entry, ok := envValue.(map[string]any)
				if !ok {
					continue
				}
				valueFrom, _, _ := unstructured.NestedMap(entry, "valueFrom")
				location := fmt.Sprintf("%s.env[%d](%s)", containerPrefix, envIndex, stringValue(entry["name"]))
				if isSecret {
					if keyRef, ok := valueFrom["secretKeyRef"].(map[string]any); ok && stringValue(keyRef["name"]) == name {
						if showSecretKeys && stringValue(keyRef["key"]) != "" {
							location += ".secretKeyRef key=" + stringValue(keyRef["key"])
						} else {
							location += ".secretKeyRef"
						}
						locations = append(locations, location)
					}
				} else if keyRef, ok := valueFrom["configMapKeyRef"].(map[string]any); ok && stringValue(keyRef["name"]) == name {
					locations = append(locations, location+".configMapKeyRef key="+stringValue(keyRef["key"]))
				}
			}
			for envIndex, envValue := range nestedSlice(container, "envFrom") {
				source, ok := envValue.(map[string]any)
				if !ok {
					continue
				}
				location := fmt.Sprintf("%s.envFrom[%d]", containerPrefix, envIndex)
				if isSecret && nestedString(source, "secretRef", "name") == name {
					locations = append(locations, location+".secretRef")
				}
				if !isSecret && nestedString(source, "configMapRef", "name") == name {
					locations = append(locations, location+".configMapRef")
				}
			}
		}
	}
	if isSecret {
		for index, value := range nestedSlice(pod.Object, "spec", "imagePullSecrets") {
			secretRef, ok := value.(map[string]any)
			if ok && stringValue(secretRef["name"]) == name {
				locations = append(locations, fmt.Sprintf("spec.imagePullSecrets[%d]", index))
			}
		}
	}
	return locations
}

func nestedSlice(object map[string]any, fields ...string) []any {
	values, _, _ := unstructured.NestedSlice(object, fields...)
	return values
}

type ownerMetadata struct {
	object     *metav1.PartialObjectMetadata
	apiVersion string
	kind       string
}

type ownerLookup struct {
	ref       metav1.OwnerReference
	namespace string
}

func (e *Engine) addOwnerChainsForPods(ctx context.Context, pods []*unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	var warnings []model.QueryWarning
	if request.Depth == 0 || len(pods) == 0 {
		return nil
	}
	resolved := make(map[string]ownerMetadata)
	current := make([]ownerLookup, 0)
	for _, pod := range pods {
		for _, owner := range pod.GetOwnerReferences() {
			if owner.UID != "" {
				current = append(current, ownerLookup{ref: owner, namespace: pod.GetNamespace()})
			}
		}
	}

	var discovered []*metav1.APIResourceList
	var discoveryErr error
	discoveryAttempted := false
	var resolvedMu sync.Mutex
	for depth := 0; depth < request.Depth && len(current) > 0; depth++ {
		type listGroup struct {
			resource  schema.GroupVersionResource
			namespace string
			wanted    map[string]ownerLookup
		}
		groups := make(map[string]*listGroup)
		for _, lookup := range current {
			if isExcluded(request.ExcludedKinds, lookup.ref.Kind) {
				continue
			}
			resource, namespaced, ok := knownOwnerResource(lookup.ref.APIVersion, lookup.ref.Kind)
			if !ok {
				if !discoveryAttempted {
					discovered, discoveryErr = e.client.APIResources(ctx)
					discoveryAttempted = true
					if discoveryErr != nil {
						warnings = append(warnings, model.QueryWarning{Resource: model.ResourceRef{APIVersion: lookup.ref.APIVersion, Kind: lookup.ref.Kind, Namespace: lookup.namespace, Name: lookup.ref.Name, UID: string(lookup.ref.UID)}, Operation: "discover Pod owner", Message: discoveryErr.Error()})
					}
				}
				if discoveryErr != nil {
					continue
				}
				if discovered != nil {
					resource, namespaced, ok = findResource(discovered, lookup.ref.APIVersion, lookup.ref.Kind)
				}
			}
			if !ok {
				continue
			}
			namespace := ""
			if namespaced {
				namespace = lookup.namespace
			}
			key := resource.String() + "\x00" + namespace
			group := groups[key]
			if group == nil {
				group = &listGroup{resource: resource, namespace: namespace, wanted: make(map[string]ownerLookup)}
				groups[key] = group
			}
			group.wanted[string(lookup.ref.UID)] = lookup
		}
		next := make([]ownerLookup, 0)
		var nextMu sync.Mutex
		tasks := make([]func() []model.QueryWarning, 0, len(groups))
		for _, group := range groups {
			group := group
			tasks = append(tasks, func() []model.QueryWarning {
				items, err := e.listMetadataAll(ctx, group.resource, group.namespace, metav1.ListOptions{Limit: 500})
				if err != nil {
					return []model.QueryWarning{{Resource: model.ResourceRef{APIVersion: group.resource.GroupVersion().String(), Namespace: group.namespace}, Operation: "list owner metadata", Message: err.Error()}}
				}
				var localNext []ownerLookup
				for i := range items {
					item := &items[i]
					lookup, wanted := group.wanted[string(item.GetUID())]
					if !wanted || item.GetName() != lookup.ref.Name {
						continue
					}
					resolvedMu.Lock()
					resolved[string(item.GetUID())] = ownerMetadata{object: item, apiVersion: lookup.ref.APIVersion, kind: lookup.ref.Kind}
					resolvedMu.Unlock()
					for _, owner := range item.GetOwnerReferences() {
						if owner.UID != "" {
							localNext = append(localNext, ownerLookup{ref: owner, namespace: item.GetNamespace()})
						}
					}
				}
				nextMu.Lock()
				next = append(next, localNext...)
				nextMu.Unlock()
				return nil
			})
		}
		warnings = append(warnings, e.runConcurrently(ctx, tasks)...)
		current = next
	}

	for _, pod := range pods {
		currentRef := reference(pod)
		owners := pod.GetOwnerReferences()
		seen := map[string]bool{currentRef.UID: true}
		for depth := 0; depth < request.Depth && len(owners) > 0; depth++ {
			owner := owners[0]
			resolvedMu.Lock()
			parent, ok := resolved[string(owner.UID)]
			resolvedMu.Unlock()
			if !ok || parent.kind != owner.Kind || parent.apiVersion != owner.APIVersion || seen[string(owner.UID)] {
				break
			}
			parentRef := model.ResourceRef{
				APIVersion: parent.apiVersion, Kind: parent.kind, Namespace: parent.object.GetNamespace(),
				Name: parent.object.GetName(), UID: string(parent.object.GetUID()),
			}
			metadata := map[string]any{"name": parentRef.Name, "uid": parentRef.UID}
			if parentRef.Namespace != "" {
				metadata["namespace"] = parentRef.Namespace
			}
			builder.AddNode(model.ResourceNode{Ref: parentRef, Object: map[string]any{"apiVersion": parentRef.APIVersion, "kind": parentRef.Kind, "metadata": metadata}})
			builder.AddEdge(model.Edge{FromUID: currentRef.UID, ToUID: parentRef.UID, Type: model.RelationOwner})
			seen[string(owner.UID)] = true
			currentRef = parentRef
			owners = parent.object.GetOwnerReferences()
		}
	}
	return warnings
}
