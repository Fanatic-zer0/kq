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
