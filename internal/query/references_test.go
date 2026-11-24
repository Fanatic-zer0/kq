package query

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestPodReferenceLocationsFindsConfigMapAndSecretSources(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	pod.Object["spec"] = map[string]any{
		"volumes": []any{
			map[string]any{"name": "config-volume", "configMap": map[string]any{"name": "app-config"}},
			map[string]any{"name": "secret-volume", "secret": map[string]any{"secretName": "app-secret"}},
			map[string]any{"name": "projected", "projected": map[string]any{"sources": []any{
				map[string]any{"configMap": map[string]any{"name": "app-config"}},
				map[string]any{"secret": map[string]any{"name": "app-secret", "items": []any{map[string]any{"key": "TOKEN", "path": "token"}}}},
			}}},
		},
		"imagePullSecrets": []any{map[string]any{"name": "pull-credentials"}},
		"containers": []any{map[string]any{
			"name": "api",
			"env": []any{
				map[string]any{"name": "MODE", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "app-config", "key": "MODE"}}},
				map[string]any{"name": "TOKEN", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "app-secret", "key": "PASSWORD"}}},
			},
			"envFrom": []any{
				map[string]any{"configMapRef": map[string]any{"name": "app-config"}},
				map[string]any{"secretRef": map[string]any{"name": "app-secret"}},
			},
		}},
	}

	configLocations := podReferenceLocations(pod, "ConfigMap", "app-config", false)
	if len(configLocations) != 4 {
		t.Fatalf("got %d ConfigMap references, want 4: %v", len(configLocations), configLocations)
	}
	secretLocations := podReferenceLocations(pod, "Secret", "app-secret", false)
	if len(secretLocations) != 4 {
		t.Fatalf("got %d Secret references, want 4: %v", len(secretLocations), secretLocations)
	}
	for _, location := range secretLocations {
		if strings.Contains(location, "key=PASSWORD") || strings.Contains(location, "key=TOKEN") {
			t.Fatalf("Secret key name leaked without opt-in: %s", location)
		}
	}
	secretLocations = podReferenceLocations(pod, "Secret", "app-secret", true)
	if !containsLocation(secretLocations, "key=PASSWORD") || !containsLocation(secretLocations, "key=TOKEN") {
		t.Fatalf("explicit Secret key opt-in missing keys: %v", secretLocations)
	}
	pullSecretLocations := podReferenceLocations(pod, "Secret", "pull-credentials", false)
	if len(pullSecretLocations) != 1 || !strings.Contains(pullSecretLocations[0], "imagePullSecrets") {
		t.Fatalf("imagePullSecret reference missing: %v", pullSecretLocations)
	}
}

func TestConfigMapDetailFindsReversePodReferencesAndOwner(t *testing.T) {
	configMap := resourceObject("v1", "ConfigMap", "payments", "app-config", "config-uid")
	configMap.Object["data"] = map[string]any{"MODE": "production"}
	replicaSet := resourceObject("apps/v1", "ReplicaSet", "payments", "api-rs", "rs-uid", metav1Owner("apps/v1", "Deployment", "api", "deployment-uid"))
	deployment := resourceObject("apps/v1", "Deployment", "payments", "api", "deployment-uid")
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid", metav1Owner("apps/v1", "ReplicaSet", "api-rs", "rs-uid"))
	pod.Object["spec"] = map[string]any{"containers": []any{map[string]any{
		"name": "api", "env": []any{map[string]any{"name": "MODE", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "app-config", "key": "MODE"}}}},
	}}}
	reader := readerWithObjects(configMap, replicaSet, deployment, pod)
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*pod}
	reader.lists[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}.String()] = []unstructured.Unstructured{*replicaSet}
	reader.lists[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}.String()] = []unstructured.Unstructured{*deployment}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"},
		Kind:     "ConfigMap", Namespace: "payments", Name: "app-config", Depth: 3,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	kinds := make(map[string]bool)
	for _, node := range result.Graph.Nodes {
		kinds[node.Ref.Kind] = true
	}
	if !kinds["Pod"] || !kinds["ReplicaSet"] || !kinds["Deployment"] {
		t.Fatalf("reverse reference owner chain is incomplete: %v", kinds)
	}
	foundLocation := false
	for _, edge := range result.Graph.Edges {
		if edge.Type == model.RelationConfig && edge.FromUID == "pod-uid" && edge.ToUID == "config-uid" && strings.Contains(edge.Detail, "configMapKeyRef") {
			foundLocation = true
		}
	}
	if !foundLocation {
		t.Fatalf("ConfigMap reference location edge missing: %+v", result.Graph.Edges)
	}
}

func TestConfigMapDetailFindsWorkloadTemplateWithoutPods(t *testing.T) {
	configMap := resourceObject("v1", "ConfigMap", "payments", "app-config", "config-uid")
	deployment := resourceObject("apps/v1", "Deployment", "payments", "api", "deployment-uid")
	deployment.Object["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{
			"name": "api", "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "app-config"}}},
		}},
	}}}
	reader := readerWithObjects(configMap, deployment)
	reader.lists[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}.String()] = []unstructured.Unstructured{*deployment}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"},
		Kind:     "ConfigMap", Namespace: "payments", Name: "app-config", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, edge := range result.Graph.Edges {
		if edge.Type == model.RelationConfig && edge.FromUID == "deployment-uid" && edge.ToUID == "config-uid" && strings.Contains(edge.Detail, "spec.template.spec.containers") {
			return
		}
	}
	t.Fatalf("Deployment template reference missing: %+v", result.Graph.Edges)
}

func TestSecretReverseReferenceNeverExposesValues(t *testing.T) {
	secret := resourceObject("v1", "Secret", "payments", "app-secret", "secret-uid")
	secret.Object["data"] = map[string]any{"PASSWORD": "c2VjcmV0LXZhbHVl"}
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	pod.Object["spec"] = map[string]any{"containers": []any{map[string]any{
		"name": "api", "env": []any{map[string]any{"name": "PASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "app-secret", "key": "PASSWORD"}}}},
	}}}
	reader := readerWithObjects(secret, pod)
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*pod}
	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
		Kind:     "Secret", Namespace: "payments", Name: "app-secret", Depth: 1, ShowSecretKeys: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "c2VjcmV0LXZhbHVl") || strings.Contains(string(encoded), "secret-value") {
		t.Fatal("Secret value leaked into reverse-reference result")
	}
}

func TestSecretReverseReferencesFindIngressTLSAndServiceAccount(t *testing.T) {
	secret := resourceObject("v1", "Secret", "payments", "app-secret", "secret-uid")
	ingress := resourceObject("networking.k8s.io/v1", "Ingress", "payments", "web", "ingress-uid")
	ingress.Object["spec"] = map[string]any{"tls": []any{map[string]any{"secretName": "app-secret"}}}
	serviceAccount := resourceObject("v1", "ServiceAccount", "payments", "api", "sa-uid")
	serviceAccount.Object["imagePullSecrets"] = []any{map[string]any{"name": "app-secret"}}
	reader := readerWithObjects(secret, ingress, serviceAccount)
	reader.lists[schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}.String()] = []unstructured.Unstructured{*ingress}
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}.String()] = []unstructured.Unstructured{*serviceAccount}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
		Kind:     "Secret", Namespace: "payments", Name: "app-secret", Depth: 1, ShowSecretKeys: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	foundTLS, foundPullSecret := false, false
	for _, edge := range result.Graph.Edges {
		foundTLS = foundTLS || edge.FromUID == "ingress-uid" && strings.Contains(edge.Detail, "spec.tls[0].secretName")
		foundPullSecret = foundPullSecret || edge.FromUID == "sa-uid" && strings.Contains(edge.Detail, "imagePullSecrets[0].name")
	}
	if !foundTLS || !foundPullSecret {
		t.Fatalf("Ingress TLS or ServiceAccount Secret reference missing: %+v", result.Graph.Edges)
	}
}

func TestSecretReverseReferencesFindCrossNamespaceIngressAnnotation(t *testing.T) {
	secret := resourceObject("v1", "Secret", "prd-plm", "ca-secret", "secret-uid")
	ingress := resourceObject("networking.k8s.io/v1", "Ingress", "ingress", "plm-mtls-route-all", "ingress-uid")
	ingress.Object["metadata"].(map[string]any)["annotations"] = map[string]any{
		"nginx.ingress.kubernetes.io/auth-tls-secret": "prd-plm/ca-secret",
	}
	otherIngress := resourceObject("networking.k8s.io/v1", "Ingress", "other", "other-route", "other-ingress-uid")
	otherIngress.Object["metadata"].(map[string]any)["annotations"] = map[string]any{
		"nginx.ingress.kubernetes.io/auth-tls-secret": "ca-secret",
	}
	reader := readerWithObjects(secret, ingress, otherIngress)
	reader.lists[schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}.String()] = []unstructured.Unstructured{*ingress, *otherIngress}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
		Kind:     "Secret", Namespace: "prd-plm", Name: "ca-secret", Depth: 1, ShowSecretKeys: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, edge := range result.Graph.Edges {
		if edge.FromUID == "other-ingress-uid" {
			t.Fatalf("unqualified Secret name matched an Ingress in another namespace: %+v", edge)
		}
		if edge.FromUID == "ingress-uid" && edge.Type == model.RelationSecret && edge.Detail == "metadata.annotations[nginx.ingress.kubernetes.io/auth-tls-secret]" {
			return
		}
	}
	t.Fatalf("cross-namespace Ingress Secret annotation was not found: %+v", result.Graph.Edges)
}

func TestCustomResourceReverseReferencesFindFieldsAndAnnotations(t *testing.T) {
	customDefinition := resourceObject("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "applications.example.io", "crd-uid")
	customDefinition.Object["spec"] = map[string]any{
		"group": "example.io", "scope": "Namespaced",
		"names":    map[string]any{"plural": "applications", "kind": "Application"},
		"versions": []any{map[string]any{"name": "v1", "served": true}},
	}
	secret := resourceObject("v1", "Secret", "payments", "app-secret", "secret-uid")
	configMap := resourceObject("v1", "ConfigMap", "payments", "app-config", "config-uid")
	application := resourceObject("example.io/v1", "Application", "payments", "shop", "application-uid")
	application.Object["spec"] = map[string]any{
		"credentials": map[string]any{"secretRef": map[string]any{"name": "app-secret"}},
		"settings":    map[string]any{"configMapRef": map[string]any{"name": "app-config"}},
	}
	application.Object["metadata"].(map[string]any)["annotations"] = map[string]any{
		"operator.example.io/secret-name":    "payments/app-secret",
		"operator.example.io/configmap-name": "app-config",
	}
	reader := readerWithObjects(customDefinition, secret, configMap, application)
	reader.lists[schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}.String()] = []unstructured.Unstructured{*customDefinition}
	applicationResource := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "applications"}
	reader.lists[applicationResource.String()] = []unstructured.Unstructured{*application}

	tests := []struct {
		kind       string
		name       string
		resource   schema.GroupVersionResource
		showKeys   bool
		wantFields []string
	}{
		{kind: "Secret", name: "app-secret", resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, showKeys: true, wantFields: []string{"spec.credentials.secretRef", "metadata.annotations[operator.example.io/secret-name]"}},
		{kind: "ConfigMap", name: "app-config", resource: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, wantFields: []string{"spec.settings.configMapRef", "metadata.annotations[operator.example.io/configmap-name]"}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			result, err := New(reader).Run(context.Background(), Request{
				Resource: test.resource, Kind: test.kind, Namespace: "payments", Name: test.name, Depth: 1, ShowSecretKeys: test.showKeys,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			for _, expected := range test.wantFields {
				found := false
				for _, edge := range result.Graph.Edges {
					if edge.FromUID == "application-uid" && edge.Detail == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("reference location %q missing from edges: %+v", expected, result.Graph.Edges)
				}
			}
		})
	}
}

func containsLocation(locations []string, text string) bool {
	for _, location := range locations {
		if strings.Contains(location, text) {
			return true
		}
	}
	return false
}

func metav1Owner(apiVersion, kind, name, uid string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(uid)}
}
