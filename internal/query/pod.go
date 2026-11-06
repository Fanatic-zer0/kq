package query

import (
	"context"
	"fmt"
	"sort"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (e *Engine) resolvePodAssociations(ctx context.Context, pod *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	podRef := reference(pod)
	podSpec, _, _ := unstructured.NestedMap(pod.Object, "spec")
	serviceAccount := stringValue(podSpec["serviceAccountName"])
	if serviceAccount == "" {
		serviceAccount = "default"
	}

	var tasks []func() []model.QueryWarning

	if nodeName := nestedString(pod.Object, "spec", "nodeName"); nodeName != "" && !isExcluded(request.ExcludedKinds, "Node") {
		tasks = append(tasks, func() []model.QueryWarning {
			node, err := e.client.Get(ctx, schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "", nodeName)
			if err != nil {
				return []model.QueryWarning{warningForRef(model.ResourceRef{APIVersion: "v1", Kind: "Node", Name: nodeName}, "get node", err)}
			}
			nodeModel := project(node, "Node", "v1", request.ShowSecretKeys)
			builder.AddNode(nodeModel)
			builder.AddEdge(model.Edge{FromUID: podRef.UID, ToUID: nodeModel.Ref.UID, From: refPointer(podRef), To: refPointer(nodeModel.Ref), Type: model.RelationNode})
			return nil
		})
	}

	tasks = append(tasks, func() []model.QueryWarning {
		var warnings []model.QueryWarning
		if !isExcluded(request.ExcludedKinds, "ServiceAccount") {
			e.addNamedReference(ctx, builder, request, podRef, schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, "ServiceAccount", podRef.Namespace, serviceAccount, model.RelationServiceAccount, "spec.serviceAccountName", true, &warnings)
		}
		if request.IncludeRBAC {
			warnings = append(warnings, e.resolveServiceAccountRBAC(ctx, podRef, serviceAccount, builder, request)...)
		}
		return warnings
	})

	volumes, _, _ := unstructured.NestedSlice(pod.Object, "spec", "volumes")
	for volumeIndex, value := range volumes {
		volume, ok := value.(map[string]any)
		if !ok {
			continue
		}
		tasks = append(tasks, func() []model.QueryWarning {
			return e.resolveVolume(ctx, builder, request, podRef, volumeIndex, volume)
		})
	}

	for _, containerField := range []string{"containers", "initContainers", "ephemeralContainers"} {
		containers, _, _ := unstructured.NestedSlice(pod.Object, "spec", containerField)
		for containerIndex, value := range containers {
			container, ok := value.(map[string]any)
			if !ok {
				continue
			}
			containerField, containerIndex := containerField, containerIndex
			tasks = append(tasks, func() []model.QueryWarning {
				return e.resolveContainerRefs(ctx, builder, request, podRef, containerField, containerIndex, container)
			})
		}
	}

	tasks = append(tasks, func() []model.QueryWarning {
		return e.resolvePodServices(ctx, pod, builder, request)
	})
	tasks = append(tasks, func() []model.QueryWarning {
		return e.resolvePodPolicies(ctx, pod, builder, request)
	})

	imagePullSecrets, _, _ := unstructured.NestedSlice(pod.Object, "spec", "imagePullSecrets")
	if len(imagePullSecrets) > 0 {
		tasks = append(tasks, func() []model.QueryWarning {
			var warnings []model.QueryWarning
			for pullSecretIndex, value := range imagePullSecrets {
				entry, ok := value.(map[string]any)
				if !ok {
					continue
				}
				if name := stringValue(entry["name"]); name != "" {
					e.addSecretReference(ctx, builder, request, podRef, name, nil, fmt.Sprintf("spec.imagePullSecrets[%d].name", pullSecretIndex), &warnings)
				}
			}
			return warnings
		})
	}

	return e.runConcurrently(ctx, tasks)
}

// resolveVolume resolves one spec.volumes[] entry's PVC/ConfigMap/Secret/projected
// references; split out so sibling volumes resolve concurrently.
func (e *Engine) resolveVolume(ctx context.Context, builder *graph.Builder, request Request, podRef model.ResourceRef, volumeIndex int, volume map[string]any) []model.QueryWarning {
	var warnings []model.QueryWarning
	prefix := fmt.Sprintf("spec.volumes[%d]", volumeIndex)
	if claim := nestedString(volume, "persistentVolumeClaim", "claimName"); claim != "" {
		e.resolvePVC(ctx, builder, request, podRef, claim, prefix+".persistentVolumeClaim.claimName", &warnings)
	}
	if name := nestedString(volume, "configMap", "name"); name != "" {
		e.addNamedReference(ctx, builder, request, podRef, schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "ConfigMap", podRef.Namespace, name, model.RelationConfig, prefix+".configMap.name", true, &warnings)
	}
	if name := nestedString(volume, "secret", "secretName"); name != "" {
		e.addSecretReference(ctx, builder, request, podRef, name, nil, prefix+".secret.secretName", &warnings)
	}
	projected, _, _ := unstructured.NestedSlice(volume, "projected", "sources")
	for sourceIndex, sourceValue := range projected {
		source, ok := sourceValue.(map[string]any)
		if !ok {
			continue
		}
		sourcePrefix := fmt.Sprintf("%s.projected.sources[%d]", prefix, sourceIndex)
		if name := nestedString(source, "configMap", "name"); name != "" {
			e.addNamedReference(ctx, builder, request, podRef, schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "ConfigMap", podRef.Namespace, name, model.RelationConfig, sourcePrefix+".configMap.name", true, &warnings)
		}
		if name := nestedString(source, "secret", "name"); name != "" {
			keys := projectedKeyNames(source, "secret")
			e.addSecretReference(ctx, builder, request, podRef, name, keys, sourcePrefix+".secret.name", &warnings)
		}
	}
	return warnings
}

// resolveContainerRefs resolves one container's env/envFrom ConfigMap and
// Secret references; split out so sibling containers resolve concurrently.
func (e *Engine) resolveContainerRefs(ctx context.Context, builder *graph.Builder, request Request, podRef model.ResourceRef, containerField string, containerIndex int, container map[string]any) []model.QueryWarning {
	var warnings []model.QueryWarning
	containerName := stringValue(container["name"])
	containerPrefix := fmt.Sprintf("spec.%s[%d](%s)", containerField, containerIndex, containerName)
	env, _, _ := unstructured.NestedSlice(container, "env")
	for envIndex, envValue := range env {
		entry, ok := envValue.(map[string]any)
		if !ok {
			continue
		}
		valueFrom, _, _ := unstructured.NestedMap(entry, "valueFrom")
		location := fmt.Sprintf("%s.env[%d](%s).valueFrom", containerPrefix, envIndex, stringValue(entry["name"]))
		if keyRef, ok := valueFrom["configMapKeyRef"].(map[string]any); ok {
			name, key := stringValue(keyRef["name"]), stringValue(keyRef["key"])
			if name != "" {
				detail := location + ".configMapKeyRef"
				if key != "" {
					detail += " key=" + key
				}
				e.addNamedReference(ctx, builder, request, podRef, schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "ConfigMap", podRef.Namespace, name, model.RelationConfig, detail, true, &warnings)
			}
		}
		if keyRef, ok := valueFrom["secretKeyRef"].(map[string]any); ok {
			name, key := stringValue(keyRef["name"]), stringValue(keyRef["key"])
			if name != "" {
				e.addSecretReference(ctx, builder, request, podRef, name, []string{key}, location+".secretKeyRef", &warnings)
			}
		}
	}
	envFrom, _, _ := unstructured.NestedSlice(container, "envFrom")
	for envIndex, envValue := range envFrom {
		source, ok := envValue.(map[string]any)
		if !ok {
			continue
		}
		location := fmt.Sprintf("%s.envFrom[%d]", containerPrefix, envIndex)
		if name := nestedString(source, "configMapRef", "name"); name != "" {
			e.addNamedReference(ctx, builder, request, podRef, schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "ConfigMap", podRef.Namespace, name, model.RelationConfig, location+".configMapRef", true, &warnings)
		}
		if name := nestedString(source, "secretRef", "name"); name != "" {
			e.addSecretReference(ctx, builder, request, podRef, name, nil, location+".secretRef", &warnings)
		}
	}
	return warnings
}

func (e *Engine) resolvePVC(ctx context.Context, builder *graph.Builder, request Request, pod model.ResourceRef, name, location string, warnings *[]model.QueryWarning) {
	if isExcluded(request.ExcludedKinds, "PersistentVolumeClaim") {
		return
	}
	claim, err := e.client.Get(ctx, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, pod.Namespace, name)
	if err != nil {
		*warnings = append(*warnings, warningForRef(model.ResourceRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: pod.Namespace, Name: name}, "get PVC", err))
		return
	}
	claimNode := project(claim, "PersistentVolumeClaim", "v1", request.ShowSecretKeys)
	builder.AddNode(claimNode)
	builder.AddEdge(model.Edge{FromUID: pod.UID, ToUID: claimNode.Ref.UID, From: refPointer(pod), To: refPointer(claimNode.Ref), Type: model.RelationVolume, Detail: location})
	*warnings = append(*warnings, e.resolvePVCStorage(ctx, claim, claimNode, builder, request)...)
}

func (e *Engine) resolvePVCStorage(ctx context.Context, claim *unstructured.Unstructured, claimNode model.ResourceNode, builder *graph.Builder, request Request) []model.QueryWarning {
	var warnings []model.QueryWarning
	volumeName := nestedString(claim.Object, "spec", "volumeName")
	if volumeName == "" || isExcluded(request.ExcludedKinds, "PersistentVolume") {
		return nil
	}
	volume, err := e.client.Get(ctx, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, "", volumeName)
	if err != nil {
		warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: "v1", Kind: "PersistentVolume", Name: volumeName}, "get PV", err))
		return warnings
	}
	volumeNode := project(volume, "PersistentVolume", "v1", request.ShowSecretKeys)
	builder.AddNode(volumeNode)
	builder.AddEdge(model.Edge{FromUID: claimNode.Ref.UID, ToUID: volumeNode.Ref.UID, From: refPointer(claimNode.Ref), To: refPointer(volumeNode.Ref), Type: model.RelationVolume})
	storageClass := nestedString(volume.Object, "spec", "storageClassName")
	if storageClass == "" || isExcluded(request.ExcludedKinds, "StorageClass") {
		return warnings
	}
	class, err := e.client.Get(ctx, schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, "", storageClass)
	if err != nil {
		warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass", Name: storageClass}, "get StorageClass", err))
		return warnings
	}
	classNode := project(class, "StorageClass", "storage.k8s.io/v1", request.ShowSecretKeys)
	builder.AddNode(classNode)
	builder.AddEdge(model.Edge{FromUID: volumeNode.Ref.UID, ToUID: classNode.Ref.UID, From: refPointer(volumeNode.Ref), To: refPointer(classNode.Ref), Type: model.RelationVolume})
	return warnings
}

func (e *Engine) addSecretReference(ctx context.Context, builder *graph.Builder, request Request, from model.ResourceRef, name string, keys []string, location string, warnings *[]model.QueryWarning) {
	if isExcluded(request.ExcludedKinds, "Secret") {
		return
	}
	ref := model.ResourceRef{APIVersion: "v1", Kind: "Secret", Namespace: from.Namespace, Name: name}
	node := model.ResourceNode{Ref: ref, Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "namespace": from.Namespace}}}
	if request.ShowSecretKeys {
		secret, err := e.client.Get(ctx, schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, from.Namespace, name)
		if err != nil {
			*warnings = append(*warnings, warningForRef(ref, "get Secret keys", err))
		} else {
			node = project(secret, "Secret", "v1", true)
		}
		details, _ := node.Details.(*model.SecretDetails)
		if details == nil {
			details = &model.SecretDetails{}
			node.Details = details
		}
		unique := make(map[string]struct{}, len(keys)+len(details.Keys))
		for _, key := range append(keys, details.Keys...) {
			if key != "" {
				unique[key] = struct{}{}
			}
		}
		details.Keys = details.Keys[:0]
		for key := range unique {
			details.Keys = append(details.Keys, key)
		}
		sort.Strings(details.Keys)
		// The key names are public only by explicit opt-in.
		for _, key := range details.Keys {
			location += " key=" + key
		}
	}
	builder.AddNode(node)
	builder.AddEdge(model.Edge{FromUID: from.UID, ToUID: node.Ref.UID, From: refPointer(from), To: refPointer(node.Ref), Type: model.RelationSecret, Detail: location})
}

func (e *Engine) addNamedReference(ctx context.Context, builder *graph.Builder, request Request, from model.ResourceRef, resource schema.GroupVersionResource, kind, namespace, name string, relation model.RelationType, detail string, fetch bool, warnings *[]model.QueryWarning) {
	if isExcluded(request.ExcludedKinds, kind) {
		return
	}
	ref := model.ResourceRef{APIVersion: resource.GroupVersion().String(), Kind: kind, Namespace: namespace, Name: name}
	var node model.ResourceNode
	if fetch {
		object, err := e.client.Get(ctx, resource, namespace, name)
		if err != nil {
			*warnings = append(*warnings, warningForRef(ref, "get related resource", err))
		} else {
			node = project(object, kind, resource.GroupVersion().String(), request.ShowSecretKeys)
		}
	}
	if node.Ref.Name == "" {
		node = model.ResourceNode{Ref: ref, Object: map[string]any{"apiVersion": ref.APIVersion, "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace}}}
	}
	builder.AddNode(node)
	builder.AddEdge(model.Edge{FromUID: from.UID, ToUID: node.Ref.UID, From: refPointer(from), To: refPointer(node.Ref), Type: relation, Detail: detail})
}

func (e *Engine) resolvePodServices(ctx context.Context, pod *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "Service") {
		return nil
	}
	resource := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	items, err := e.listAll(ctx, resource, pod.GetNamespace(), metav1.ListOptions{Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningFor(pod, "list Services", err)}
	}
	var warnings []model.QueryWarning
	var relatedTasks []func() []model.QueryWarning
	for i := range items {
		service := &items[i]
		selector, _, _ := unstructured.NestedStringMap(service.Object, "spec", "selector")
		if len(selector) == 0 || !selectorMatches(selector, pod.GetLabels()) {
			continue
		}
		serviceNode := project(service, "Service", "v1", request.ShowSecretKeys)
		builder.AddNode(serviceNode)
		podRef := reference(pod)
		builder.AddEdge(model.Edge{FromUID: serviceNode.Ref.UID, ToUID: podRef.UID, From: refPointer(serviceNode.Ref), To: refPointer(podRef), Type: model.RelationSelector})
		serviceCopy := service.DeepCopy()
		serviceRef := serviceNode.Ref
		relatedTasks = append(relatedTasks, func() []model.QueryWarning {
			return e.resolveServiceEndpoints(ctx, serviceCopy, serviceRef, builder, request)
		})
		relatedTasks = append(relatedTasks, func() []model.QueryWarning {
			return e.resolveServiceIngresses(ctx, serviceCopy, serviceRef, builder, request)
		})
	}
	warnings = append(warnings, e.runConcurrently(ctx, relatedTasks)...)
	return warnings
}

func (e *Engine) resolveServiceTarget(ctx context.Context, service *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	var warnings []model.QueryWarning
	serviceRef := reference(service)
	if !isExcluded(request.ExcludedKinds, "Pod") {
		selector, _, _ := unstructured.NestedStringMap(service.Object, "spec", "selector")
		if len(selector) > 0 {
			selectorText := labels.Set(selector).AsSelector().String()
			items, err := e.listAll(ctx, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, service.GetNamespace(), metav1.ListOptions{LabelSelector: selectorText, Limit: 500})
			if err != nil {
				warnings = append(warnings, warningFor(service, "list selected Pods", err))
			} else {
				for i := range items {
					pod := &items[i]
					if !selectorMatches(selector, pod.GetLabels()) {
						continue
					}
					podNode := project(pod, "Pod", "v1", request.ShowSecretKeys)
					builder.AddNode(podNode)
					builder.AddEdge(model.Edge{FromUID: serviceRef.UID, ToUID: podNode.Ref.UID, From: refPointer(serviceRef), To: refPointer(podNode.Ref), Type: model.RelationSelector})
				}
			}
		}
	}
	warnings = append(warnings, e.resolveServiceEndpoints(ctx, service, serviceRef, builder, request)...)
	warnings = append(warnings, e.resolveServiceIngresses(ctx, service, serviceRef, builder, request)...)
	return warnings
}

func (e *Engine) resolveNodePods(ctx context.Context, node *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "Pod") {
		return nil
	}
	selector := fields.OneTermEqualSelector("spec.nodeName", node.GetName()).String()
	items, err := e.listAll(ctx, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "", metav1.ListOptions{FieldSelector: selector, Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningFor(node, "list scheduled Pods", err)}
	}
	nodeRef := reference(node)
	for i := range items {
		pod := &items[i]
		if nestedString(pod.Object, "spec", "nodeName") != node.GetName() {
			continue
		}
		podNode := project(pod, "Pod", "v1", request.ShowSecretKeys)
		builder.AddNode(podNode)
		builder.AddEdge(model.Edge{FromUID: podNode.Ref.UID, ToUID: nodeRef.UID, From: refPointer(podNode.Ref), To: refPointer(nodeRef), Type: model.RelationNode})
	}
	return nil
}

func (e *Engine) resolveServiceEndpoints(ctx context.Context, service *unstructured.Unstructured, serviceRef model.ResourceRef, builder *graph.Builder, request Request) []model.QueryWarning {
	var warnings []model.QueryWarning
	if !isExcluded(request.ExcludedKinds, "EndpointSlice") {
		resource := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
		selector := "kubernetes.io/service-name=" + service.GetName()
		items, err := e.listAll(ctx, resource, service.GetNamespace(), metav1.ListOptions{LabelSelector: selector, Limit: 500})
		if err != nil {
			warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice", Namespace: service.GetNamespace()}, "list EndpointSlices", err))
		} else {
			for i := range items {
				slice := &items[i]
				if slice.GetLabels()["kubernetes.io/service-name"] != service.GetName() {
					continue
				}
				sliceNode := project(slice, "EndpointSlice", "discovery.k8s.io/v1", request.ShowSecretKeys)
				builder.AddNode(sliceNode)
				builder.AddEdge(model.Edge{FromUID: serviceRef.UID, ToUID: sliceNode.Ref.UID, From: refPointer(serviceRef), To: refPointer(sliceNode.Ref), Type: model.RelationServiceBackend})
			}
		}
	}
	return warnings
}

func selectorMatches(selector, objectLabels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	return labels.Set(selector).AsSelector().Matches(labels.Set(objectLabels))
}

func projectedKeyNames(source map[string]any, sourceKind string) []string {
	items, _, _ := unstructured.NestedSlice(source, sourceKind, "items")
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if itemMap, ok := item.(map[string]any); ok {
			if key := stringValue(itemMap["key"]); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func reference(object *unstructured.Unstructured) model.ResourceRef {
	return model.ResourceRef{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: string(object.GetUID())}
}

func refPointer(ref model.ResourceRef) *model.ResourceRef { return &ref }

func nestedString(object map[string]any, fields ...string) string {
	value, found, _ := unstructured.NestedString(object, fields...)
	if !found {
		return ""
	}
	return value
}
