package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (e *Engine) resolveConfigOrSecretTarget(ctx context.Context, target *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	targetRef := reference(target)
	var warnings []model.QueryWarning
	var referencingPods []*unstructured.Unstructured
	if !isExcluded(request.ExcludedKinds, "Pod") {
		resource := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
		pods, err := e.listAll(ctx, resource, target.GetNamespace(), metav1.ListOptions{Limit: 500})
		if err != nil {
			warnings = append(warnings, warningFor(target, "list referencing Pods", err))
		} else {
			for i := range pods {
				pod := &pods[i]
				locations := podReferenceLocations(pod, target.GetKind(), target.GetName(), request.ShowSecretKeys)
				locations = append(locations, annotationReferenceLocations(pod, target)...)
				if len(locations) == 0 {
					continue
				}
				podNode := project(pod, "Pod", "v1", request.ShowSecretKeys)
				builder.AddNode(podNode)
				for _, location := range locations {
					builder.AddEdge(model.Edge{
						FromUID: podNode.Ref.UID, ToUID: targetRef.UID,
						From: refPointer(podNode.Ref), To: refPointer(targetRef), Type: relationFor(target.GetKind()), Detail: location,
					})
				}
				referencingPods = append(referencingPods, pod)
			}
		}
	}
	for _, workloadType := range workloadTemplateTypes {
		if isExcluded(request.ExcludedKinds, workloadType.kind) {
			continue
		}
		workloads, err := e.listAll(ctx, workloadType.resource, target.GetNamespace(), metav1.ListOptions{Limit: 500})
		if err != nil {
			warnings = append(warnings, warningFor(target, "list referencing "+workloadType.kind+" resources", err))
			continue
		}
		for i := range workloads {
			workload := &workloads[i]
			spec, found, _ := unstructured.NestedMap(workload.Object, workloadType.templatePath...)
			if !found {
				continue
			}
			templatePath := workloadType.templatePath[:len(workloadType.templatePath)-1]
			templateMetadataPath := append(append([]string(nil), templatePath...), "metadata")
			templateMetadata, _, _ := unstructured.NestedMap(workload.Object, templateMetadataPath...)
			metadata := map[string]any{"name": workload.GetName(), "namespace": workload.GetNamespace()}
			if annotations, ok := templateMetadata["annotations"].(map[string]any); ok {
				metadata["annotations"] = annotations
			}
			pod := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   metadata,
				"spec":       spec,
			}}
			locations := podReferenceLocations(pod, target.GetKind(), target.GetName(), request.ShowSecretKeys)
			locations = append(locations, annotationReferenceLocations(pod, target)...)
			for _, location := range annotationReferenceLocations(workload, target) {
				locations = append(locations, "workload."+location)
			}
			if len(locations) == 0 {
				continue
			}
			workloadNode := project(workload, workloadType.kind, workload.GetAPIVersion(), request.ShowSecretKeys)
			builder.AddNode(workloadNode)
			for _, location := range locations {
				fullLocation := strings.Join(workloadType.templatePath, ".") + "." + strings.TrimPrefix(location, "spec.")
				if strings.HasPrefix(location, "metadata.") {
					fullLocation = strings.Join(templatePath, ".") + "." + location
				}
				if strings.HasPrefix(location, "workload.metadata.") {
					fullLocation = strings.TrimPrefix(location, "workload.")
				}
				builder.AddEdge(model.Edge{
					FromUID: workloadNode.Ref.UID, ToUID: targetRef.UID,
					From: refPointer(workloadNode.Ref), To: refPointer(targetRef), Type: relationFor(target.GetKind()), Detail: fullLocation,
				})
			}
		}
	}
	warnings = append(warnings, e.resolveNativeConfigOrSecretReferences(ctx, target, targetRef, builder, request)...)
	warnings = append(warnings, e.resolveCustomConfigOrSecretReferences(ctx, target, targetRef, builder, request)...)
	warnings = append(warnings, e.addOwnerChainsForPods(ctx, referencingPods, builder, request)...)
	return warnings
}

func (e *Engine) resolveNativeConfigOrSecretReferences(ctx context.Context, target *unstructured.Unstructured, targetRef model.ResourceRef, builder *graph.Builder, request Request) []model.QueryWarning {
	var warnings []model.QueryWarning
	sources := []struct {
		kind     string
		resource schema.GroupVersionResource
	}{
		{kind: "Ingress", resource: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}},
		{kind: "ServiceAccount", resource: schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}},
	}
	for _, source := range sources {
		if isExcluded(request.ExcludedKinds, source.kind) {
			continue
		}
		namespace := target.GetNamespace()
		if source.kind == "Ingress" && target.GetKind() == "Secret" {
			namespace = ""
		}
		items, err := e.listAll(ctx, source.resource, namespace, metav1.ListOptions{Limit: 500})
		if err != nil {
			warnings = append(warnings, warningFor(target, "list referencing "+source.kind+" resources", err))
			continue
		}
		for i := range items {
			object := &items[i]
			var locations []string
			locations = append(locations, annotationReferenceLocations(object, target)...)
			if target.GetKind() == "Secret" && source.kind == "Ingress" {
				if object.GetNamespace() == target.GetNamespace() {
					for index, tls := range nestedSlice(object.Object, "spec", "tls") {
						entry, ok := tls.(map[string]any)
						if ok && stringValue(entry["secretName"]) == target.GetName() {
							locations = append(locations, fmt.Sprintf("spec.tls[%d].secretName", index))
						}
					}
				}
			}
			if target.GetKind() == "Secret" && source.kind == "ServiceAccount" {
				for index, secret := range nestedSlice(object.Object, "imagePullSecrets") {
					entry, ok := secret.(map[string]any)
					if ok && stringValue(entry["name"]) == target.GetName() {
						locations = append(locations, fmt.Sprintf("imagePullSecrets[%d].name", index))
					}
				}
				for index, secret := range nestedSlice(object.Object, "secrets") {
					entry, ok := secret.(map[string]any)
					if ok && stringValue(entry["name"]) == target.GetName() {
						locations = append(locations, fmt.Sprintf("secrets[%d].name", index))
					}
				}
			}
			if len(locations) == 0 {
				continue
			}
			node := project(object, source.kind, object.GetAPIVersion(), request.ShowSecretKeys)
			builder.AddNode(node)
			for _, location := range locations {
				builder.AddEdge(model.Edge{
					FromUID: node.Ref.UID, ToUID: targetRef.UID,
					From: refPointer(node.Ref), To: refPointer(targetRef), Type: relationFor(target.GetKind()), Detail: location,
				})
			}
		}
	}
	return warnings
}

func (e *Engine) resolveCustomConfigOrSecretReferences(ctx context.Context, target *unstructured.Unstructured, targetRef model.ResourceRef, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "CustomResourceDefinition") {
		return nil
	}
	crds, err := e.listAll(ctx, schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, "", metav1.ListOptions{Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningFor(target, "list custom resource definitions for reverse reference lookup", err)}
	}
	type customResourceType struct {
		kind      string
		resource  schema.GroupVersionResource
		namespace string
	}
	var resourceTypes []customResourceType
	for i := range crds {
		crd := &crds[i]
		scope := nestedString(crd.Object, "spec", "scope")
		if scope != "Namespaced" && scope != "Cluster" {
			continue
		}
		group := nestedString(crd.Object, "spec", "group")
		plural := nestedString(crd.Object, "spec", "names", "plural")
		kind := nestedString(crd.Object, "spec", "names", "kind")
		if group == "" || plural == "" || kind == "" || isExcluded(request.ExcludedKinds, kind) {
			continue
		}
		for _, value := range nestedSlice(crd.Object, "spec", "versions") {
			version, ok := value.(map[string]any)
			if !ok || version["served"] != true || stringValue(version["name"]) == "" {
				continue
			}
			resourceTypes = append(resourceTypes, customResourceType{
				kind: kind, namespace: target.GetNamespace(),
				resource: schema.GroupVersionResource{Group: group, Version: stringValue(version["name"]), Resource: plural},
			})
			if scope == "Cluster" {
				resourceTypes[len(resourceTypes)-1].namespace = ""
			}
			break
		}
	}
	sort.Slice(resourceTypes, func(i, j int) bool {
		return resourceTypes[i].resource.String() < resourceTypes[j].resource.String()
	})
	const maxCustomResourceTypes = 32
	var warnings []model.QueryWarning
	if len(resourceTypes) > maxCustomResourceTypes {
		warnings = append(warnings, model.QueryWarning{
			Resource: targetRef, Operation: "custom resource reverse reference limit",
			Message: fmt.Sprintf("Only the first %d namespaced custom resource types are scanned; %d types were omitted.", maxCustomResourceTypes, len(resourceTypes)-maxCustomResourceTypes),
		})
		resourceTypes = resourceTypes[:maxCustomResourceTypes]
	}
	for _, resourceType := range resourceTypes {
		list, err := e.client.List(ctx, resourceType.resource, resourceType.namespace, metav1.ListOptions{Limit: 500})
		if err != nil {
			warnings = append(warnings, warningFor(target, "list referencing custom resources of kind "+resourceType.kind, err))
			continue
		}
		if list.GetContinue() != "" {
			warnings = append(warnings, model.QueryWarning{
				Resource: targetRef, Operation: "custom resource reverse reference limit",
				Message: fmt.Sprintf("The scan of %s is limited to the first 500 objects.", resourceType.kind),
			})
		}
		for i := range list.Items {
			object := &list.Items[i]
			locations := customResourceReferenceLocations(object, target)
			if len(locations) == 0 {
				continue
			}
			node := project(object, resourceType.kind, object.GetAPIVersion(), request.ShowSecretKeys)
			builder.AddNode(node)
			for _, location := range locations {
				builder.AddEdge(model.Edge{
					FromUID: node.Ref.UID, ToUID: targetRef.UID,
					From: refPointer(node.Ref), To: refPointer(targetRef), Type: relationFor(target.GetKind()), Detail: location,
				})
			}
		}
	}
	return warnings
}

type workloadTemplateType struct {
	kind         string
	resource     schema.GroupVersionResource
	templatePath []string
}

var workloadTemplateTypes = []workloadTemplateType{
	{kind: "Deployment", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, templatePath: []string{"spec", "template", "spec"}},
	{kind: "ReplicaSet", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, templatePath: []string{"spec", "template", "spec"}},
	{kind: "StatefulSet", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, templatePath: []string{"spec", "template", "spec"}},
	{kind: "DaemonSet", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, templatePath: []string{"spec", "template", "spec"}},
	{kind: "Job", resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, templatePath: []string{"spec", "template", "spec"}},
	{kind: "CronJob", resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, templatePath: []string{"spec", "jobTemplate", "spec", "template", "spec"}},
}

func relationFor(kind string) model.RelationType {
	if kind == "Secret" {
		return model.RelationSecret
	}
	return model.RelationConfig
}

func annotationReferenceLocations(object *unstructured.Unstructured, target *unstructured.Unstructured) []string {
	annotations, _, _ := unstructured.NestedStringMap(object.Object, "metadata", "annotations")
	keys := make([]string, 0, len(annotations))
	for key := range annotations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var locations []string
	for _, key := range keys {
		value := strings.TrimSpace(annotations[key])
		matchesName := object.GetNamespace() == target.GetNamespace() && value == target.GetName()
		matchesQualifiedName := target.GetNamespace() != "" && value == target.GetNamespace()+"/"+target.GetName()
		if !referenceFieldMatchesKind(key, target.GetKind()) || (!matchesName && !matchesQualifiedName) {
			continue
		}
		locations = append(locations, fmt.Sprintf("metadata.annotations[%s]", key))
	}
	return locations
}

func customResourceReferenceLocations(object, target *unstructured.Unstructured) []string {
	var locations []string
	seen := make(map[string]bool)
	budget := 512
	for _, field := range []string{"config", "spec", "status"} {
		collectMatchingCustomReferences(object.Object[field], field, object.GetNamespace(), target, false, 0, &budget, seen, &locations)
	}
	for _, location := range annotationReferenceLocations(object, target) {
		appendUniqueLocation(location, seen, &locations)
	}
	sort.Strings(locations)
	return locations
}

func collectMatchingCustomReferences(value any, path, namespace string, target *unstructured.Unstructured, kindHint bool, depth int, budget *int, seen map[string]bool, locations *[]string) {
	if depth > 5 || *budget <= 0 || len(*locations) >= 8 {
		return
	}
	*budget--
	switch typed := value.(type) {
	case map[string]any:
		kind := stringValue(typed["kind"])
		name := stringValue(typed["name"])
		refNamespace := stringValue(typed["namespace"])
		apiVersion := stringValue(typed["apiVersion"])
		if refNamespace == "" {
			refNamespace = namespace
		}
		if name != "" && refNamespace == target.GetNamespace() && referenceValueMatches(name, target.GetNamespace(), target.GetName()) {
			kindMatches := strings.EqualFold(kind, target.GetKind())
			if apiVersion != "" && apiVersion != target.GetAPIVersion() {
				kindMatches = false
			}
			if kindMatches || (kind == "" && kindHint) {
				appendUniqueLocation(path, seen, locations)
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if *budget <= 0 || len(*locations) >= 8 {
				continue
			}
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			childHint := kindHint || referenceFieldMatchesKind(key, target.GetKind())
			if scalar, ok := typed[key].(string); ok && childHint && referenceValueMatches(scalar, target.GetNamespace(), target.GetName()) {
				appendUniqueLocation(childPath, seen, locations)
				continue
			}
			collectMatchingCustomReferences(typed[key], childPath, namespace, target, childHint, depth+1, budget, seen, locations)
		}
	case []any:
		for index, item := range typed {
			if *budget <= 0 || len(*locations) >= 8 {
				break
			}
			collectMatchingCustomReferences(item, fmt.Sprintf("%s[%d]", path, index), namespace, target, kindHint, depth+1, budget, seen, locations)
		}
	}
}

func referenceFieldMatchesKind(field, kind string) bool {
	normalizedField := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(field))
	switch strings.ToLower(kind) {
	case "secret":
		return strings.Contains(normalizedField, "secret")
	case "configmap":
		return strings.Contains(normalizedField, "configmap")
	default:
		return false
	}
}

func referenceValueMatches(value, namespace, name string) bool {
	value = strings.TrimSpace(value)
	return value == name || (namespace != "" && value == namespace+"/"+name)
}

func appendUniqueLocation(location string, seen map[string]bool, locations *[]string) {
	if location == "" || seen[location] {
		return
	}
	seen[location] = true
	*locations = append(*locations, location)
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
