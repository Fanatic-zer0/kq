package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fanatic-zer0/kq/internal/k8s"
	"github.com/Fanatic-zer0/kq/internal/model"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type fakeReader struct {
	mu                sync.Mutex
	objects           map[string]*unstructured.Unstructured
	lists             map[string][]unstructured.Unstructured
	resources         []*metav1.APIResourceList
	listErr           error
	getCalls          map[string]int
	listCalls         map[string]int
	discoveryCalls    int
	rejectUIDSelector bool
	listDelay         time.Duration
}

var _ k8s.Reader = (*fakeReader)(nil)

func (f *fakeReader) Get(_ context.Context, resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getCalls == nil {
		f.getCalls = make(map[string]int)
	}
	f.getCalls[objectKey(resource, namespace, name)]++
	object, ok := f.objects[objectKey(resource, namespace, name)]
	if !ok {
		return nil, apierrors.NewNotFound(resource.GroupResource(), name)
	}
	return object.DeepCopy(), nil
}

func (f *fakeReader) List(_ context.Context, resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	f.mu.Lock()
	if f.listCalls == nil {
		f.listCalls = make(map[string]int)
	}
	f.listCalls[resource.String()+"|"+namespace+"|"+options.FieldSelector+"|"+options.LabelSelector]++
	delay := f.listDelay
	f.mu.Unlock()
	if delay > 0 {
		<-time.After(delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.rejectUIDSelector && strings.Contains(options.FieldSelector, "involvedObject.uid") {
		return nil, fmt.Errorf("unsupported involvedObject.uid selector")
	}
	selector, err := fields.ParseSelector(options.FieldSelector)
	if err != nil {
		return nil, err
	}
	list := &unstructured.UnstructuredList{}
	for _, object := range f.lists[resource.String()] {
		if namespace != "" && object.GetNamespace() != namespace {
			continue
		}
		fieldSet := fields.Set{
			"involvedObject.uid":       nestedString(object.Object, "involvedObject", "uid"),
			"involvedObject.kind":      nestedString(object.Object, "involvedObject", "kind"),
			"involvedObject.name":      nestedString(object.Object, "involvedObject", "name"),
			"involvedObject.namespace": nestedString(object.Object, "involvedObject", "namespace"),
			"spec.nodeName":            nestedString(object.Object, "spec", "nodeName"),
		}
		if selector.Matches(fieldSet) {
			list.Items = append(list.Items, *object.DeepCopy())
		}
	}
	return list, nil
}

func (f *fakeReader) APIResources(context.Context) ([]*metav1.APIResourceList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discoveryCalls++
	return f.resources, nil
}

func (f *fakeReader) ListMetadata(ctx context.Context, resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	objects, err := f.List(ctx, resource, namespace, options)
	if err != nil {
		return nil, err
	}
	list := &metav1.PartialObjectMetadataList{}
	for i := range objects.Items {
		object := &objects.Items[i]
		list.Items = append(list.Items, metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
			Name: object.GetName(), Namespace: object.GetNamespace(), UID: object.GetUID(), OwnerReferences: object.GetOwnerReferences(),
		}})
	}
	return list, nil
}

func TestRunTraversesOwnersOnlyWhenUIDMatches(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-rs", UID: "rs-uid"})
	replicaSet := resourceObject("apps/v1", "ReplicaSet", "payments", "api-rs", "rs-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", UID: "deployment-uid"})
	deployment := resourceObject("apps/v1", "Deployment", "payments", "api", "deployment-uid")
	siblingPod := resourceObject("v1", "Pod", "payments", "api-1", "sibling-pod-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-rs", UID: "rs-uid"})
	reader := readerWithObjects(pod, replicaSet, deployment)
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*pod, *siblingPod}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 3,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := len(result.Graph.Nodes); got != 4 {
		t.Fatalf("got %d nodes, want target, two owners, and default ServiceAccount", got)
	}
	if got := len(result.Graph.Edges); got != 3 {
		t.Fatalf("got %d edges, want two owner edges and one ServiceAccount edge", got)
	}
	if result.Graph.Edges[0].FromUID != "pod-uid" || result.Graph.Edges[0].ToUID != "rs-uid" {
		t.Fatalf("unexpected first edge: %+v", result.Graph.Edges[0])
	}
	for _, node := range result.Graph.Nodes {
		if node.Ref.UID == "sibling-pod-uid" {
			t.Fatal("Pod detail expanded to a sibling Pod through its owner")
		}
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.discoveryCalls != 0 {
		t.Fatalf("built-in ownership unexpectedly performed %d discovery calls", reader.discoveryCalls)
	}
}

func TestUnknownOwnerTriggersDiscoveryLazily(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid", metav1.OwnerReference{APIVersion: "example.io/v1", Kind: "Widget", Name: "api", UID: "widget-uid"})
	widget := resourceObject("example.io/v1", "Widget", "payments", "api", "widget-uid")
	reader := readerWithObjects(pod, widget)
	reader.resources = append(reader.resources, &metav1.APIResourceList{
		GroupVersion: "example.io/v1",
		APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true}},
	})

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	found := false
	for _, node := range result.Graph.Nodes {
		found = found || node.Ref.Kind == "Widget" && node.Ref.Name == "api"
	}
	if !found {
		t.Fatal("custom owner was not resolved using API discovery")
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.discoveryCalls != 1 {
		t.Fatalf("unknown owner used %d discovery calls, want 1", reader.discoveryCalls)
	}
}

func TestPodRBACBindingListsAreOptIn(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	reader := readerWithObjects(pod)
	request := Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 1,
	}
	if _, err := New(reader).Run(context.Background(), request); err != nil {
		t.Fatalf("default Pod query: %v", err)
	}
	roleBindingsKey := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}.String() + "|payments||"
	clusterRoleBindingsKey := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}.String() + "|||"
	reader.mu.Lock()
	defaultRoleBindingCalls := reader.listCalls[roleBindingsKey] + reader.listCalls[clusterRoleBindingsKey]
	reader.mu.Unlock()
	if defaultRoleBindingCalls != 0 {
		t.Fatalf("default Pod query issued %d RBAC binding Lists, want 0", defaultRoleBindingCalls)
	}

	request.IncludeRBAC = true
	if _, err := New(reader).Run(context.Background(), request); err != nil {
		t.Fatalf("Pod query with RBAC: %v", err)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if got := reader.listCalls[roleBindingsKey] + reader.listCalls[clusterRoleBindingsKey]; got != 2 {
		t.Fatalf("Pod query with RBAC issued %d binding Lists, want 2", got)
	}
}

func TestRunSkipsOwnerWhenUIDDiffers(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-rs", UID: "old-rs-uid"})
	replicaSet := resourceObject("apps/v1", "ReplicaSet", "payments", "api-rs", "current-rs-uid")
	reader := readerWithObjects(pod, replicaSet)

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 3,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := len(result.Graph.Nodes); got != 2 {
		t.Fatalf("got %d nodes, want target and default ServiceAccount", got)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0].Message, "UID does not match") {
		t.Fatalf("expected UID mismatch warning, got %+v", result.Warnings)
	}
}

func TestRunTraversesCronJobJobsAndPodsWithinDepth(t *testing.T) {
	cronjob := resourceObject("batch/v1", "CronJob", "payments", "nightly", "cron-uid")
	job := resourceObject("batch/v1", "Job", "payments", "nightly-1", "job-uid", metav1.OwnerReference{APIVersion: "batch/v1", Kind: "CronJob", Name: "nightly", UID: "cron-uid"})
	pod := resourceObject("v1", "Pod", "payments", "nightly-1-pod", "pod-uid", metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: "nightly-1", UID: "job-uid"})
	reader := readerWithObjects(cronjob, job, pod)
	reader.lists[schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}.String()] = []unstructured.Unstructured{*job}
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*pod}

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"},
		Kind:     "CronJob", Namespace: "payments", Name: "nightly", Depth: 2,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := len(result.Graph.Nodes); got != 3 {
		t.Fatalf("got %d nodes, want CronJob, Job, and Pod", got)
	}
	if got := len(result.Graph.Edges); got != 2 {
		t.Fatalf("got %d edges, want 2", got)
	}

	limited, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"},
		Kind:     "CronJob", Namespace: "payments", Name: "nightly", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run with depth limit returned error: %v", err)
	}
	if got := len(limited.Graph.Nodes); got != 2 {
		t.Fatalf("depth 1 produced %d nodes, want CronJob and Job", got)
	}
}

func TestDeploymentChildPodListIsCoalescedAcrossReplicaSets(t *testing.T) {
	deployment := resourceObject("apps/v1", "Deployment", "payments", "api", "deployment-uid")
	firstReplicaSet := resourceObject("apps/v1", "ReplicaSet", "payments", "api-a", "rs-a-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", UID: "deployment-uid"})
	secondReplicaSet := resourceObject("apps/v1", "ReplicaSet", "payments", "api-b", "rs-b-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", UID: "deployment-uid"})
	firstPod := resourceObject("v1", "Pod", "payments", "api-a-0", "pod-a-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-a", UID: "rs-a-uid"})
	secondPod := resourceObject("v1", "Pod", "payments", "api-b-0", "pod-b-uid", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-b", UID: "rs-b-uid"})
	reader := readerWithObjects(deployment, firstReplicaSet, secondReplicaSet, firstPod, secondPod)
	reader.lists[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}.String()] = []unstructured.Unstructured{*firstReplicaSet, *secondReplicaSet}
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String()] = []unstructured.Unstructured{*firstPod, *secondPod}

	_, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Kind:     "Deployment", Namespace: "payments", Name: "api", Depth: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	podsKey := schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String() + "|payments||"
	if got := reader.listCalls[podsKey]; got != 1 {
		t.Fatalf("underlying Pod List called %d times for two ReplicaSets, want 1", got)
	}
}

func TestRunAttachesAndDeduplicatesEvents(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	first := eventObject("event-1", "pod-uid", time.Now().Add(-time.Minute), 2)
	second := eventObject("event-2", "pod-uid", time.Now(), 4)
	reader := readerWithObjects(pod)
	reader.lists[schema.GroupVersionResource{Version: "v1", Resource: "events"}.String()] = []unstructured.Unstructured{*first, *second}
	reader.rejectUIDSelector = true

	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 0,
		Since: time.Hour, IncludeEvents: true, MaxEvents: 10,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := len(result.Graph.Events); got != 1 {
		t.Fatalf("got %d events, want one deduplicated event", got)
	}
	event := result.Graph.Events[0]
	if event.NodeUID != "pod-uid" || event.Count != 4 {
		t.Fatalf("unexpected event attachment/count: %+v", event)
	}
	if event.LastTimestamp.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("event timestamp was not updated from duplicate: %+v", event)
	}
}

func TestListAllCoalescesConcurrentIdenticalLists(t *testing.T) {
	reader := readerWithObjects()
	reader.listDelay = 20 * time.Millisecond
	engine := New(reader)
	resource := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	const callers = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := engine.listAll(context.Background(), resource, "payments", metav1.ListOptions{Limit: 500}); err != nil {
				t.Errorf("listAll: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	reader.mu.Lock()
	defer reader.mu.Unlock()
	key := resource.String() + "|payments||"
	if got := reader.listCalls[key]; got != 1 {
		t.Fatalf("underlying List called %d times for %d identical callers, want 1", got, callers)
	}
}

func TestPodSecretReferenceDoesNotFetchOrExposeKeyNamesByDefault(t *testing.T) {
	pod := resourceObject("v1", "Pod", "payments", "api-0", "pod-uid")
	pod.Object["spec"] = map[string]any{
		"containers": []any{map[string]any{
			"name": "api",
			"env": []any{map[string]any{
				"name":      "TOKEN",
				"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "credentials", "key": "private-key-name"}},
			}},
		}},
	}
	reader := readerWithObjects(pod)
	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     "Pod", Namespace: "payments", Name: "api-0", Depth: 1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	secretKey := objectKey(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "payments", "credentials")
	if reader.getCalls[secretKey] != 0 {
		t.Fatalf("Secret GET count is %d, want 0", reader.getCalls[secretKey])
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(encoded), "private-key-name") {
		t.Fatal("Secret key name leaked into graph output")
	}
	foundReference := false
	for _, node := range result.Graph.Nodes {
		if node.Ref.Kind == "Secret" && node.Ref.Name == "credentials" {
			foundReference = true
		}
	}
	if !foundReference {
		t.Fatal("Secret reference was not included in the graph")
	}
}

func TestServiceSelectorRequiresNonEmptyFullMatch(t *testing.T) {
	labels := map[string]string{"app": "api", "tier": "backend"}
	if selectorMatches(nil, labels) || selectorMatches(map[string]string{}, labels) {
		t.Fatal("empty selector must not match")
	}
	if selectorMatches(map[string]string{"app": "other"}, labels) {
		t.Fatal("mismatched selector must not match")
	}
	if !selectorMatches(map[string]string{"app": "api"}, labels) {
		t.Fatal("matching selector was rejected")
	}
}

func TestProjectSecretRetainsOnlyOptedInKeyNames(t *testing.T) {
	secret := resourceObject("v1", "Secret", "payments", "credentials", "secret-uid")
	secret.Object["data"] = map[string]any{"password": "c2VjcmV0LXZhbHVl"}
	withKeys := project(secret, "Secret", "v1", true)
	withoutKeys := project(secret, "Secret", "v1", false)
	encoded, err := json.Marshal(withKeys)
	if err != nil {
		t.Fatalf("marshal Secret projection: %v", err)
	}
	if strings.Contains(string(encoded), "c2VjcmV0LXZhbHVl") || strings.Contains(string(encoded), "secret-value") {
		t.Fatal("Secret data leaked into projection")
	}
	withKeysDetails, _ := withKeys.Details.(*model.SecretDetails)
	if withKeysDetails == nil || len(withKeysDetails.Keys) != 1 || withKeysDetails.Keys[0] != "password" {
		t.Fatalf("unexpected Secret keys: %+v", withKeysDetails)
	}
	withoutKeysDetails, _ := withoutKeys.Details.(*model.SecretDetails)
	if withoutKeysDetails == nil || len(withoutKeysDetails.Keys) != 0 {
		t.Fatalf("Secret key names should be hidden by default: %+v", withoutKeysDetails)
	}
}

func TestProjectCustomResourceDefinitionAndCronJobMetadata(t *testing.T) {
	crd := resourceObject("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "widgets.example.io", "crd-uid")
	crd.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
	crd.Object["spec"] = map[string]any{
		"group": "example.io", "scope": "Namespaced",
		"names":    map[string]any{"plural": "widgets", "singular": "widget", "kind": "Widget"},
		"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}},
	}
	crd.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "NamesAccepted", "status": "True", "reason": "NoConflicts"},
		map[string]any{"type": "Established", "status": "True", "reason": "InitialNamesAccepted", "message": "the initial names have been accepted"},
	}}
	crdProjection := project(crd, "CustomResourceDefinition", "apiextensions.k8s.io/v1", false)
	crdDetails, _ := crdProjection.Details.(*model.CRDDetails)
	if crdDetails == nil || crdDetails.Group != "example.io" || crdDetails.Scope != "Namespaced" || crdDetails.CreationTimestamp.IsZero() {
		t.Fatalf("CRD projection is missing group or scope: %+v", crdDetails)
	}
	if len(crdDetails.Versions) != 1 || !crdDetails.Versions[0].Served || !crdDetails.Versions[0].Storage {
		t.Fatalf("CRD projection is missing version state: %+v", crdDetails.Versions)
	}
	if len(crdDetails.Conditions) != 2 || crdDetails.Conditions[1].Type != "Established" || crdDetails.Conditions[1].Status != "True" || crdDetails.Conditions[1].Reason != "InitialNamesAccepted" {
		t.Fatalf("CRD projection is missing readiness conditions: %+v", crdDetails.Conditions)
	}

	cronjob := resourceObject("batch/v1", "CronJob", "payments", "nightly", "cron-uid")
	cronjob.Object["spec"] = map[string]any{"schedule": "0 2 * * *", "suspend": true}
	cronProjection := project(cronjob, "CronJob", "batch/v1", false)
	cronDetails, _ := cronProjection.Details.(*model.CronJobDetails)
	if cronDetails == nil || cronDetails.Schedule != "0 2 * * *" || !cronDetails.Suspend {
		t.Fatalf("CronJob projection is missing schedule metadata: %+v", cronDetails)
	}
}

func TestProjectCustomResourceDetailsAreUsefulAndRedacted(t *testing.T) {
	resource := resourceObject("example.io/v1", "Widget", "payments", "sample", "widget-uid")
	resource.SetLabels(map[string]string{"app": "sample"})
	resource.SetAnnotations(map[string]string{"example.io/api-token": "annotation-token"})
	resource.Object["config"] = map[string]any{"endpoint": "https://api.example.test", "region": "west"}
	resource.Object["spec"] = map[string]any{
		"replicas":    int64(3),
		"credentials": map[string]any{"password": "spec-password"},
		"routes":      []any{map[string]any{"host": "shop.example.test", "port": int64(8443)}},
	}
	resource.Object["status"] = map[string]any{"ready": true, "message": "running"}

	node := project(resource, "Widget", "example.io/v1", false)
	details, ok := node.Details.(*model.CustomResourceDetails)
	if !ok || details == nil {
		t.Fatalf("custom-resource details missing or wrong type: %#v", node.Details)
	}
	fields := make(map[string]string, len(details.Fields))
	for _, item := range details.Fields {
		fields[item.Path] = item.Value
	}
	for path, expected := range map[string]string{
		"config.endpoint":     "https://api.example.test",
		"config.region":       "west",
		"spec.replicas":       "3",
		"spec.credentials":    "REDACTED",
		"spec.routes[0].host": "shop.example.test",
		"status.ready":        "true",
		"status.message":      "running",
	} {
		if fields[path] != expected {
			t.Errorf("field %q = %q, want %q; all fields: %+v", path, fields[path], expected, details.Fields)
		}
	}
	if details.Labels["app"] != "sample" || details.Annotations["example.io/api-token"] != "REDACTED" {
		t.Fatalf("custom-resource metadata missing or unredacted: labels=%v annotations=%v", details.Labels, details.Annotations)
	}
	config, ok := details.Config.(map[string]any)
	if !ok || config["endpoint"] != "https://api.example.test" {
		t.Fatalf("nested custom-resource config missing: %#v", details.Config)
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal custom-resource projection: %v", err)
	}
	if strings.Contains(string(encoded), "spec-password") || strings.Contains(string(encoded), "annotation-token") {
		t.Fatalf("sensitive custom-resource value leaked: %s", encoded)
	}
}

func TestProjectPersistentVolumeAndClaimDetails(t *testing.T) {
	claim := resourceObject("v1", "PersistentVolumeClaim", "payments", "data", "claim-uid")
	claim.Object["spec"] = map[string]any{
		"accessModes":      []any{"ReadWriteOnce"},
		"resources":        map[string]any{"requests": map[string]any{"storage": "5Gi"}},
		"storageClassName": "fast", "volumeName": "pv-data", "volumeMode": "Filesystem",
		"selector":   map[string]any{"matchLabels": map[string]any{"app": "database"}},
		"dataSource": map[string]any{"apiGroup": "snapshot.storage.k8s.io", "kind": "VolumeSnapshot", "name": "data-snapshot"},
	}
	claim.Object["status"] = map[string]any{
		"phase": "Bound", "capacity": map[string]any{"storage": "5Gi"},
		"conditions": []any{map[string]any{"type": "Resizing", "status": "False", "message": "no resize in progress"}},
	}
	claimDetails, ok := project(claim, "PersistentVolumeClaim", "v1", false).Details.(*model.PVCDetails)
	if !ok || claimDetails == nil {
		t.Fatalf("PVC details missing: %#v", claimDetails)
	}
	if claimDetails.RequestedCapacity != "5Gi" || claimDetails.Capacity != "5Gi" || claimDetails.Phase != "Bound" || claimDetails.VolumeMode != "Filesystem" || claimDetails.VolumeName != "pv-data" {
		t.Fatalf("PVC storage details missing: %+v", claimDetails)
	}
	if claimDetails.Selector != "app=database" || claimDetails.DataSource != "snapshot.storage.k8s.io/VolumeSnapshot/data-snapshot" {
		t.Fatalf("PVC selector/data source missing: %+v", claimDetails)
	}
	if len(claimDetails.Conditions) != 1 || claimDetails.Conditions[0].Type != "Resizing" || claimDetails.Conditions[0].Message != "no resize in progress" {
		t.Fatalf("PVC conditions missing: %+v", claimDetails.Conditions)
	}

	volume := resourceObject("v1", "PersistentVolume", "", "pv-data", "pv-uid")
	volume.Object["spec"] = map[string]any{
		"capacity": map[string]any{"storage": "5Gi"}, "accessModes": []any{"ReadWriteOnce"},
		"persistentVolumeReclaimPolicy": "Retain", "storageClassName": "fast", "volumeMode": "Filesystem",
		"claimRef": map[string]any{"namespace": "payments", "name": "data"}, "mountOptions": []any{"discard"},
	}
	volume.Object["status"] = map[string]any{"phase": "Bound"}
	volumeDetails, ok := project(volume, "PersistentVolume", "v1", false).Details.(*model.PVDetails)
	if !ok || volumeDetails == nil {
		t.Fatalf("PV details missing: %#v", volumeDetails)
	}
	if volumeDetails.Capacity != "5Gi" || volumeDetails.Phase != "Bound" || volumeDetails.ReclaimPolicy != "Retain" || volumeDetails.VolumeMode != "Filesystem" || volumeDetails.ClaimNamespace != "payments" || volumeDetails.ClaimName != "data" || len(volumeDetails.MountOptions) != 1 || volumeDetails.MountOptions[0] != "discard" {
		t.Fatalf("PV storage details missing: %+v", volumeDetails)
	}
}

func TestProjectIngressDetails(t *testing.T) {
	ingress := resourceObject("networking.k8s.io/v1", "Ingress", "payments", "checkout", "ingress-uid")
	ingress.SetAnnotations(map[string]string{"nginx.ingress.kubernetes.io/proxy-body-size": "8m", "example.io/api-token": "sensitive-value"})
	ingress.Object["spec"] = map[string]any{
		"ingressClassName": "public",
		"defaultBackend":   map[string]any{"service": map[string]any{"name": "fallback", "port": map[string]any{"number": int64(8080)}}},
		"rules": []any{map[string]any{"host": "shop.example.test", "http": map[string]any{"paths": []any{map[string]any{
			"path": "/checkout", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": "checkout", "port": map[string]any{"name": "http"}}},
		}}}}},
		"tls": []any{map[string]any{"secretName": "shop-cert", "hosts": []any{"shop.example.test"}}},
	}
	ingress.Object["status"] = map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"hostname": "lb.example.test"}, map[string]any{"ip": "192.0.2.10"}}}}

	node := project(ingress, "Ingress", "networking.k8s.io/v1", false)
	details, ok := node.Details.(*model.IngressDetails)
	if !ok || details == nil {
		t.Fatalf("Ingress details missing or wrong type: %#v", node.Details)
	}
	if details.IngressClassName != "public" || details.DefaultBackend == nil || details.DefaultBackend.ServicePort != "8080" {
		t.Fatalf("unexpected Ingress class/default backend: %+v", details)
	}
	if details.Annotations["nginx.ingress.kubernetes.io/proxy-body-size"] != "8m" || details.Annotations["example.io/api-token"] != "REDACTED" {
		t.Fatalf("unexpected Ingress annotations: %+v", details.Annotations)
	}
	if len(details.Rules) != 1 || details.Rules[0].Host != "shop.example.test" || details.Rules[0].ServiceName != "checkout" || details.Rules[0].ServicePort != "http" || details.Rules[0].PathType != "Prefix" {
		t.Fatalf("unexpected Ingress rules: %+v", details.Rules)
	}
	if len(details.TLS) != 1 || details.TLS[0].SecretName != "shop-cert" || len(details.TLS[0].Hosts) != 1 {
		t.Fatalf("unexpected Ingress TLS: %+v", details.TLS)
	}
	if strings.Join(details.LoadBalancerAddresses, ",") != "lb.example.test,192.0.2.10" {
		t.Fatalf("unexpected load-balancer addresses: %+v", details.LoadBalancerAddresses)
	}
}

func TestProjectDeploymentRolloutDetails(t *testing.T) {
	deployment := resourceObject("apps/v1", "Deployment", "payments", "api", "deployment-uid")
	deployment.SetGeneration(7)
	deployment.Object["spec"] = map[string]any{
		"selector": map[string]any{"matchLabels": map[string]any{"app": "api"}},
		"strategy": map[string]any{"type": "RollingUpdate"},
	}
	deployment.Object["status"] = map[string]any{
		"observedGeneration": int64(6),
		"replicas":           int64(3), "readyReplicas": int64(2), "updatedReplicas": int64(1), "availableReplicas": int64(2),
		"conditions": []any{map[string]any{"type": "Progressing", "status": "True", "reason": "ReplicaSetUpdated"}},
	}

	node := project(deployment, "Deployment", "apps/v1", false)
	details, ok := node.Details.(*model.DeploymentDetails)
	if !ok || details == nil {
		t.Fatalf("Deployment details missing or wrong type: %#v", node.Details)
	}
	if details.Generation != 7 || details.ObservedGeneration != 6 || details.Selector != "app=api" {
		t.Fatalf("unexpected Deployment rollout metadata: %+v", details)
	}
	if len(details.Conditions) != 1 || details.Conditions[0].Reason != "ReplicaSetUpdated" {
		t.Fatalf("unexpected Deployment conditions: %+v", details.Conditions)
	}
}

func TestProjectJobTemplateContainersRedactsSensitiveArgs(t *testing.T) {
	job := resourceObject("batch/v1", "Job", "payments", "migration", "job-uid")
	job.Object["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{
			"name": "migrate", "image": "example/migrator:v2", "command": []any{"/bin/migrate"},
			"args": []any{"--password", "top-secret", "--mode=apply", "--api-token=token-value"},
		}},
	}}}
	node := project(job, "Job", "batch/v1", false)
	details, ok := node.Details.(*model.JobDetails)
	if !ok || details == nil || len(details.Containers) != 1 {
		t.Fatalf("Job template container details missing: %+v", node.Details)
	}
	container := details.Containers[0]
	if container.Image != "example/migrator:v2" || len(container.Command) != 1 || container.Command[0] != "/bin/migrate" {
		t.Fatalf("Job template image/command missing: %+v", container)
	}
	if strings.Contains(strings.Join(container.Args, " "), "top-secret") || strings.Contains(strings.Join(container.Args, " "), "token-value") {
		t.Fatalf("sensitive args were not redacted: %v", container.Args)
	}
}

func TestCronJobNextRunCalculation(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 1, 0, 0, time.UTC)
	next, errText := nextCronJobRun("*/5 * * * *", nil, false, now)
	if errText != "" || next == nil || !next.Equal(time.Date(2026, 11, 6, 12, 5, 0, 0, time.UTC)) {
		t.Fatalf("nextCronJobRun returned (%v, %q), want 12:05 UTC", next, errText)
	}
	timeZone := "UTC"
	next, errText = nextCronJobRun("0 2 * * *", &timeZone, false, now)
	if errText != "" || next == nil || !next.Equal(time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("timezone nextCronJobRun returned (%v, %q), want next day 02:00 UTC", next, errText)
	}
	if next, errText = nextCronJobRun("* * * * *", nil, true, now); errText != "" || next != nil {
		t.Fatalf("suspended CronJob should have no next run, got (%v, %q)", next, errText)
	}
	if next, errText = nextCronJobRun("invalid", nil, false, now); errText == "" || next != nil {
		t.Fatalf("invalid schedule should return an error, got (%v, %q)", next, errText)
	}
}

func readerWithObjects(objects ...*unstructured.Unstructured) *fakeReader {
	reader := &fakeReader{
		objects: make(map[string]*unstructured.Unstructured),
		lists:   make(map[string][]unstructured.Unstructured),
		resources: []*metav1.APIResourceList{
			{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}, {Name: "events", Kind: "Event", Namespaced: true}}},
			{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true}, {Name: "replicasets", Kind: "ReplicaSet", Namespaced: true}}},
			{GroupVersion: "batch/v1", APIResources: []metav1.APIResource{{Name: "jobs", Kind: "Job", Namespaced: true}, {Name: "cronjobs", Kind: "CronJob", Namespaced: true}}},
		},
	}
	for _, object := range objects {
		groupVersion, _ := schema.ParseGroupVersion(object.GetAPIVersion())
		resource := map[string]string{
			"Pod": "pods", "ReplicaSet": "replicasets", "Deployment": "deployments", "Job": "jobs", "CronJob": "cronjobs", "Widget": "widgets",
			"Service": "services", "Ingress": "ingresses", "ConfigMap": "configmaps", "Secret": "secrets", "ServiceAccount": "serviceaccounts",
			"PersistentVolumeClaim": "persistentvolumeclaims", "PersistentVolume": "persistentvolumes", "StorageClass": "storageclasses",
			"Node": "nodes", "Endpoints": "endpoints", "EndpointSlice": "endpointslices", "NetworkPolicy": "networkpolicies",
			"PodDisruptionBudget": "poddisruptionbudgets", "HorizontalPodAutoscaler": "horizontalpodautoscalers",
			"RoleBinding": "rolebindings", "ClusterRoleBinding": "clusterrolebindings", "Role": "roles", "ClusterRole": "clusterroles",
		}[object.GetKind()]
		reader.objects[objectKey(groupVersion.WithResource(resource), object.GetNamespace(), object.GetName())] = object
	}
	return reader
}

func resourceObject(apiVersion, kind, namespace, name, uid string, owners ...metav1.OwnerReference) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind}}
	object.SetNamespace(namespace)
	object.SetName(name)
	object.SetUID(typesUID(uid))
	object.SetOwnerReferences(owners)
	return object
}

func eventObject(name, podUID string, observed time.Time, count int64) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion":     "v1",
		"kind":           "Event",
		"involvedObject": map[string]any{"apiVersion": "v1", "kind": "Pod", "namespace": "payments", "name": "api-0", "uid": podUID},
		"eventTime":      observed.UTC().Format(time.RFC3339Nano),
		"type":           "Warning",
		"reason":         "Unhealthy",
		"message":        "probe failed",
		"count":          count,
	}}
	object.SetNamespace("payments")
	object.SetName(name)
	object.SetUID(typesUID(name))
	return object
}

func objectKey(resource schema.GroupVersionResource, namespace, name string) string {
	return fmt.Sprintf("%s|%s|%s", resource.String(), namespace, name)
}

func typesUID(value string) types.UID { return types.UID(value) }
