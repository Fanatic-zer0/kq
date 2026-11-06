package query

import (
	"context"
	"testing"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestPodResolverFindsMatchingServiceAndIngress(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	pod.SetLabels(map[string]string{"app": "api", "tier": "backend"})
	matching := resourceObject("v1", "Service", "payments", "api", "service-uid")
	matching.Object["spec"] = map[string]any{"selector": map[string]any{"app": "api"}}
	emptySelector := resourceObject("v1", "Service", "payments", "headless", "empty-uid")
	emptySelector.Object["spec"] = map[string]any{"clusterIP": "None"}
	mismatch := resourceObject("v1", "Service", "payments", "other", "other-uid")
	mismatch.Object["spec"] = map[string]any{"selector": map[string]any{"app": "other"}}
	ingress := resourceObject("networking.k8s.io/v1", "Ingress", "payments", "api-ingress", "ingress-uid")
	ingress.Object["spec"] = map[string]any{"rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{
		"backend": map[string]any{"service": map[string]any{"name": "api"}},
	}}}}}}

	reader := readerWithObjects(pod, matching, emptySelector, mismatch, ingress)
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "services"}.String()] = []unstructured.Unstructured{*matching, *emptySelector, *mismatch}
	reader.lists[schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}.String()] = []unstructured.Unstructured{*ingress}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	kinds := make(map[string]bool)
	for _, node := range result.Graph.Nodes {
		if node.Ref.Name == "headless" || node.Ref.Name == "other" {
			t.Fatalf("non-matching Service %q was included", node.Ref.Name)
		}
		kinds[node.Ref.Kind] = true
	}
	if !kinds["Service"] || !kinds["Ingress"] {
		t.Fatalf("expected matching Service and Ingress; kinds=%v", kinds)
	}
}

func TestPodResolverFindsMatchingNetworkPolicyAndPDB(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	pod.SetLabels(map[string]string{"app": "api"})
	networkPolicy := resourceObject("networking.k8s.io/v1", "NetworkPolicy", "payments", "api-policy", "np-uid")
	networkPolicy.Object["spec"] = map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}}}
	otherPolicy := resourceObject("networking.k8s.io/v1", "NetworkPolicy", "payments", "other-policy", "other-np-uid")
	otherPolicy.Object["spec"] = map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "other"}}}
	budget := resourceObject("policy/v1", "PodDisruptionBudget", "payments", "api-budget", "pdb-uid")
	budget.Object["spec"] = map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "api"}}}

	reader := readerWithObjects(pod, networkPolicy, otherPolicy, budget)
	reader.lists[schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}.String()] = []unstructured.Unstructured{*networkPolicy, *otherPolicy}
	reader.lists[schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}.String()] = []unstructured.Unstructured{*budget}
	builder := graph.New(reference(pod))
	builder.AddNode(project(pod, "Pod", "v1", false))

	warnings := New(reader).resolvePodPolicies(context.Background(), pod, builder, Request{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if got := len(builder.Value().Nodes); got != 3 {
		t.Fatalf("got %d nodes, want Pod, matching NetworkPolicy, and PDB", got)
	}
	if got := len(builder.Value().Edges); got != 2 {
		t.Fatalf("got %d edges, want 2", got)
	}
}

func TestRunFindsHPAForOwnedWorkload(t *testing.T) {
	deployment := resourceObject("apps/v1", "Deployment", "payments", "api", "deployment-uid")
	hpa := resourceObject("autoscaling/v2", "HorizontalPodAutoscaler", "payments", "api-hpa", "hpa-uid")
	hpa.Object["spec"] = map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"}}
	reader := readerWithObjects(deployment, hpa)
	reader.lists[schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}.String()] = []unstructured.Unstructured{*hpa}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Kind:     "Deployment", Namespace: "payments", Name: "api", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Graph.Nodes) != 2 || result.Graph.Nodes[1].Ref.Kind != "HorizontalPodAutoscaler" {
		t.Fatalf("HPA was not added to graph: %+v", result.Graph.Nodes)
	}
}

func TestPodResolverLinksPVCToPVAndStorageClass(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	pod.Object["spec"] = map[string]any{"volumes": []any{map[string]any{
		"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "api-data"},
	}}}
	claim := resourceObject("v1", "PersistentVolumeClaim", "payments", "api-data", "pvc-uid")
	claim.Object["spec"] = map[string]any{"volumeName": "pvc-volume"}
	volume := resourceObject("v1", "PersistentVolume", "", "pvc-volume", "pv-uid")
	volume.Object["spec"] = map[string]any{"storageClassName": "fast"}
	storageClass := resourceObject("storage.k8s.io/v1", "StorageClass", "", "fast", "sc-uid")
	reader := readerWithObjects(pod, claim, volume, storageClass)

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	kinds := make(map[string]bool)
	for _, node := range result.Graph.Nodes {
		kinds[node.Ref.Kind] = true
	}
	for _, kind := range []string{"PersistentVolumeClaim", "PersistentVolume", "StorageClass"} {
		if !kinds[kind] {
			t.Errorf("graph is missing %s", kind)
		}
	}
}

func TestPVCAndPVTargetsFindPodsUsingTheClaim(t *testing.T) {
	claim := resourceObject("v1", "PersistentVolumeClaim", "payments", "data", "claim-uid")
	claim.Object["spec"] = map[string]any{"volumeName": "pv-data", "storageClassName": "fast"}
	volume := resourceObject("v1", "PersistentVolume", "", "pv-data", "pv-uid")
	volume.Object["spec"] = map[string]any{"storageClassName": "fast", "claimRef": map[string]any{"namespace": "payments", "name": "data"}}
	storageClass := resourceObject("storage.k8s.io/v1", "StorageClass", "", "fast", "sc-uid")
	usingPod := resourceObject("v1", "Pod", "payments", "consumer-0", "pod-uid")
	usingPod.Object["spec"] = map[string]any{"volumes": []any{map[string]any{
		"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "data"},
	}}}
	unrelatedPod := resourceObject("v1", "Pod", "payments", "other-0", "other-pod-uid")
	unrelatedPod.Object["spec"] = map[string]any{"volumes": []any{map[string]any{
		"name": "other", "persistentVolumeClaim": map[string]any{"claimName": "other-data"},
	}}}

	for _, target := range []struct {
		kind      string
		resource  schema.GroupVersionResource
		name      string
		namespace string
	}{
		{kind: "PersistentVolumeClaim", resource: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, name: "data", namespace: "payments"},
		{kind: "PersistentVolume", resource: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, name: "pv-data"},
	} {
		t.Run(target.kind, func(t *testing.T) {
			reader := readerWithObjects(claim, volume, storageClass, usingPod, unrelatedPod)
			reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*usingPod, *unrelatedPod}
			result, err := New(reader).Run(context.Background(), Request{
				Resource: target.resource, Kind: target.kind, Namespace: target.namespace, Name: target.name, Depth: 1,
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			foundPod := false
			for _, node := range result.Graph.Nodes {
				if node.Ref.Kind == "Pod" && node.Ref.Name == "consumer-0" {
					foundPod = true
				}
				if node.Ref.Name == "other-0" {
					t.Fatal("unrelated Pod was included")
				}
			}
			if !foundPod {
				t.Fatalf("query did not include Pod using claim: %+v", result.Graph.Nodes)
			}
			podToClaim, claimToPV := false, false
			for _, edge := range result.Graph.Edges {
				podToClaim = podToClaim || edge.FromUID == "pod-uid" && edge.ToUID == "claim-uid" && edge.Type == "volume"
				claimToPV = claimToPV || edge.FromUID == "claim-uid" && edge.ToUID == "pv-uid" && edge.Type == "volume"
			}
			if !podToClaim {
				t.Fatal("missing Pod-to-PVC volume edge")
			}
			if target.kind == "PersistentVolume" && !claimToPV {
				t.Fatal("missing PVC-to-PV volume edge")
			}
		})
	}
}

func TestServiceTargetFindsSelectedPods(t *testing.T) {
	service := resourceObject("v1", "Service", "payments", "api", "service-uid")
	service.Object["spec"] = map[string]any{"selector": map[string]any{"app": "api"}}
	matchingPod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	matchingPod.SetLabels(map[string]string{"app": "api"})
	otherPod := resourceObject("v1", "Pod", "payments", "worker-0", "worker-uid")
	otherPod.SetLabels(map[string]string{"app": "worker"})
	reader := readerWithObjects(service, matchingPod, otherPod)
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*matchingPod, *otherPod}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "services"},
		Kind:     "Service", Namespace: "payments", Name: "api", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Graph.Nodes) != 2 || result.Graph.Nodes[1].Ref.Name != "api-0" {
		t.Fatalf("Service query did not select only matching Pod: %+v", result.Graph.Nodes)
	}
}

func TestNodeTargetFindsScheduledPods(t *testing.T) {
	node := resourceObject("v1", "Node", "", "worker-1", "node-uid")
	first := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	first.Object["spec"] = map[string]any{"nodeName": "worker-1"}
	second := resourceObject("v1", "Pod", "payments", "worker-0", "worker-uid")
	second.Object["spec"] = map[string]any{"nodeName": "worker-2"}
	reader := readerWithObjects(node, first, second)
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*first, *second}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "nodes"},
		Kind:     "Node", Name: "worker-1", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Graph.Nodes) != 2 || result.Graph.Nodes[1].Ref.Name != "api-0" {
		t.Fatalf("Node query did not return only scheduled Pod: %+v", result.Graph.Nodes)
	}
}
