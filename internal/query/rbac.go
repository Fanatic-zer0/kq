package query

import (
	"context"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (e *Engine) resolveServiceAccountTarget(ctx context.Context, serviceAccount *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	saRef := reference(serviceAccount)
	var warnings []model.QueryWarning
	if !isExcluded(request.ExcludedKinds, "Pod") {
		items, err := e.listAll(ctx, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, saRef.Namespace, metav1.ListOptions{Limit: 500})
		if err != nil {
			warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: saRef.Namespace}, "list ServiceAccount Pods", err))
		} else {
			for i := range items {
				pod := &items[i]
				name := nestedString(pod.Object, "spec", "serviceAccountName")
				if name == "" {
					name = "default"
				}
				if name != saRef.Name {
					continue
				}
				podNode := model.ResourceNode{
					Ref:    model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: pod.GetNamespace(), Name: pod.GetName(), UID: string(pod.GetUID())},
					Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": pod.GetName(), "namespace": pod.GetNamespace(), "uid": string(pod.GetUID())}},
				}
				builder.AddNode(podNode)
				builder.AddEdge(model.Edge{FromUID: podNode.Ref.UID, ToUID: saRef.UID, From: refPointer(podNode.Ref), To: refPointer(saRef), Type: model.RelationServiceAccount})
			}
		}
	}
	warnings = append(warnings, e.resolveServiceAccountRBAC(ctx, saRef, saRef.Name, builder, request)...)
	return warnings
}

func (e *Engine) resolveServiceAccountRBAC(ctx context.Context, podRef model.ResourceRef, serviceAccount string, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "RoleBinding") && isExcluded(request.ExcludedKinds, "ClusterRoleBinding") {
		return nil
	}
	serviceAccountRef := model.ResourceRef{APIVersion: "v1", Kind: "ServiceAccount", Namespace: podRef.Namespace, Name: serviceAccount}
	for _, node := range builder.Value().Nodes {
		if node.Ref.Kind == "ServiceAccount" && node.Ref.Namespace == podRef.Namespace && node.Ref.Name == serviceAccount {
			serviceAccountRef = node.Ref
			break
		}
	}

	bindings := []struct {
		resource  schema.GroupVersionResource
		kind      string
		namespace string
	}{
		{resource: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, kind: "RoleBinding", namespace: podRef.Namespace},
		{resource: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, kind: "ClusterRoleBinding", namespace: ""},
	}
	var tasks []func() []model.QueryWarning
	for _, bindingType := range bindings {
		if isExcluded(request.ExcludedKinds, bindingType.kind) {
			continue
		}
		bindingType := bindingType
		tasks = append(tasks, func() []model.QueryWarning {
			items, err := e.listAll(ctx, bindingType.resource, bindingType.namespace, metav1.ListOptions{Limit: 500})
			if err != nil {
				return []model.QueryWarning{warningForRef(model.ResourceRef{APIVersion: "rbac.authorization.k8s.io/v1", Kind: bindingType.kind, Namespace: bindingType.namespace}, "list RBAC bindings", err)}
			}
			var warnings []model.QueryWarning
			for i := range items {
				binding := &items[i]
				if !bindingReferencesServiceAccount(binding, serviceAccount, podRef.Namespace) {
					continue
				}
				bindingNode := project(binding, bindingType.kind, "rbac.authorization.k8s.io/v1", request.ShowSecretKeys)
				builder.AddNode(bindingNode)
				builder.AddEdge(model.Edge{FromUID: bindingNode.Ref.UID, ToUID: serviceAccountRef.UID, From: refPointer(bindingNode.Ref), To: refPointer(serviceAccountRef), Type: model.RelationRBAC, Detail: "subject"})
				warnings = append(warnings, e.resolveBindingRole(ctx, binding, bindingNode.Ref, builder, request)...)
			}
			return warnings
		})
	}
	return e.runConcurrently(ctx, tasks)
}

func bindingReferencesServiceAccount(binding *unstructured.Unstructured, name, namespace string) bool {
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	bindingNamespace := binding.GetNamespace()
	if binding.GetKind() == "ClusterRoleBinding" {
		bindingNamespace = ""
	}
	for _, value := range subjects {
		subject, ok := value.(map[string]any)
		if !ok || stringValue(subject["kind"]) != "ServiceAccount" || stringValue(subject["name"]) != name {
			continue
		}
		subjectNamespace := stringValue(subject["namespace"])
		if subjectNamespace == "" {
			subjectNamespace = bindingNamespace
		}
		if subjectNamespace == namespace {
			return true
		}
	}
	return false
}

func (e *Engine) resolveBindingRole(ctx context.Context, binding *unstructured.Unstructured, bindingRef model.ResourceRef, builder *graph.Builder, request Request) []model.QueryWarning {
	roleKind := nestedString(binding.Object, "roleRef", "kind")
	roleName := nestedString(binding.Object, "roleRef", "name")
	if roleName == "" || (roleKind != "Role" && roleKind != "ClusterRole") || isExcluded(request.ExcludedKinds, roleKind) {
		return nil
	}
	resourceName := "roles"
	namespace := binding.GetNamespace()
	if roleKind == "ClusterRole" {
		resourceName = "clusterroles"
		namespace = ""
	}
	resource := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: resourceName}
	role, err := e.client.Get(ctx, resource, namespace, roleName)
	if err != nil {
		return []model.QueryWarning{warningForRef(model.ResourceRef{APIVersion: "rbac.authorization.k8s.io/v1", Kind: roleKind, Namespace: namespace, Name: roleName}, "get RBAC role", err)}
	}
	roleNode := project(role, roleKind, "rbac.authorization.k8s.io/v1", request.ShowSecretKeys)
	builder.AddNode(roleNode)
	builder.AddEdge(model.Edge{FromUID: bindingRef.UID, ToUID: roleNode.Ref.UID, From: refPointer(bindingRef), To: refPointer(roleNode.Ref), Type: model.RelationRBAC, Detail: "roleRef"})
	return nil
}
