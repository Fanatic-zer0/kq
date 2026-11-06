package query

import (
	"context"
	"testing"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestServiceAccountResolvesNamespaceAndClusterRBAC(t *testing.T) {
	serviceAccount := resourceObject("v1", "ServiceAccount", "payments", "api-sa", "sa-uid")
	roleBinding := resourceObject("rbac.authorization.k8s.io/v1", "RoleBinding", "payments", "reader-binding", "rb-uid")
	roleBinding.Object["subjects"] = []any{map[string]any{"kind": "ServiceAccount", "name": "api-sa", "namespace": "payments"}}
	roleBinding.Object["roleRef"] = map[string]any{"kind": "Role", "name": "reader"}
	clusterRoleBinding := resourceObject("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "", "view-binding", "crb-uid")
	clusterRoleBinding.Object["subjects"] = []any{map[string]any{"kind": "ServiceAccount", "name": "api-sa", "namespace": "payments"}}
	clusterRoleBinding.Object["roleRef"] = map[string]any{"kind": "ClusterRole", "name": "view"}
	wrongNamespaceBinding := resourceObject("rbac.authorization.k8s.io/v1", "RoleBinding", "payments", "wrong-binding", "wrong-uid")
	wrongNamespaceBinding.Object["subjects"] = []any{map[string]any{"kind": "ServiceAccount", "name": "api-sa", "namespace": "other"}}
	wrongNamespaceBinding.Object["roleRef"] = map[string]any{"kind": "Role", "name": "reader"}
	role := resourceObject("rbac.authorization.k8s.io/v1", "Role", "payments", "reader", "role-uid")
	clusterRole := resourceObject("rbac.authorization.k8s.io/v1", "ClusterRole", "", "view", "clusterrole-uid")
	reader := readerWithObjects(serviceAccount, roleBinding, clusterRoleBinding, wrongNamespaceBinding, role, clusterRole)
	reader.lists[schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}.String()] = []unstructured.Unstructured{*roleBinding, *wrongNamespaceBinding}
	reader.lists[schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}.String()] = []unstructured.Unstructured{*clusterRoleBinding}
	builder := graph.New(model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0", UID: "pod-uid"})
	builder.AddNode(project(serviceAccount, "ServiceAccount", "v1", false))

	warnings := New(reader).resolveServiceAccountRBAC(context.Background(), model.ResourceRef{Kind: "Pod", Namespace: "payments", Name: "api-0", UID: "pod-uid"}, "api-sa", builder, Request{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	kinds := make(map[string]int)
	for _, node := range builder.Value().Nodes {
		kinds[node.Ref.Kind]++
	}
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding", "Role", "ClusterRole"} {
		if kinds[kind] != 1 {
			t.Errorf("got %d %s nodes, want exactly one", kinds[kind], kind)
		}
	}
	if kinds["RoleBinding"] == 2 {
		t.Fatal("cross-namespace ServiceAccount subject was incorrectly matched")
	}
	if got := len(builder.Value().Edges); got != 4 {
		t.Fatalf("got %d RBAC edges, want 4", got)
	}
}
