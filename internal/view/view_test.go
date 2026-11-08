package view

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Fanatic-zer0/kq/internal/k8s"
	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFormatDurationBuckets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{72 * time.Hour, "3d"},
		{400 * 24 * time.Hour, "1y"},
	}
	for _, testCase := range cases {
		if got := FormatDuration(testCase.d); got != testCase.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", testCase.d, got, testCase.want)
		}
	}
}

func TestStatusClassifiesWarnings(t *testing.T) {
	ref := model.ResourceRef{Kind: "ConfigMap", Namespace: "payments", Name: "app-config"}
	warnings := []model.QueryWarning{{Resource: ref, Operation: "get", Message: "forbidden: User cannot get"}}
	if got := Status(warnings, ref); got != "NO ACCESS" {
		t.Fatalf("got %q, want NO ACCESS", got)
	}
	warnings[0].Message = "configmaps \"app-config\" not found"
	if got := Status(warnings, ref); got != "NOT FOUND" {
		t.Fatalf("got %q, want NOT FOUND", got)
	}
	if got := Status(nil, ref); got != "OK" {
		t.Fatalf("got %q, want OK", got)
	}
}

func TestPodPhasePrefersWaitingReasonOverPhase(t *testing.T) {
	details := &model.PodDetails{
		Phase:      "Running",
		Containers: []model.ContainerStatus{{Name: "api", State: "Waiting(CrashLoopBackOff)"}},
	}
	if got := PodPhase(details); got != "CrashLoopBackOff" {
		t.Fatalf("got %q, want CrashLoopBackOff", got)
	}
	details = &model.PodDetails{Phase: "Succeeded"}
	if got := PodPhase(details); got != "Completed" {
		t.Fatalf("got %q, want Completed", got)
	}
}

func TestOwnerChainOrdersFromRootToTarget(t *testing.T) {
	pod := model.ResourceNode{Ref: model.ResourceRef{Kind: "Pod", Name: "api-0", UID: "pod-uid"}}
	replicaSet := model.ResourceNode{Ref: model.ResourceRef{Kind: "ReplicaSet", Name: "api-rs", UID: "rs-uid"}}
	deployment := model.ResourceNode{Ref: model.ResourceRef{Kind: "Deployment", Name: "api", UID: "deployment-uid"}}
	graph := model.Graph{
		Nodes: []model.ResourceNode{pod, replicaSet, deployment},
		Edges: []model.Edge{
			{FromUID: "pod-uid", ToUID: "rs-uid", Type: model.RelationOwner},
			{FromUID: "rs-uid", ToUID: "deployment-uid", Type: model.RelationOwner},
		},
	}
	chain := ownerChain(graph, pod)
	if len(chain) != 3 || chain[0].Ref.Kind != "Deployment" || chain[2].Ref.Kind != "Pod" {
		t.Fatalf("unexpected owner chain: %+v", chain)
	}
}

func TestRenderListTableAndJSON(t *testing.T) {
	result := &ListResult{
		Headers: []string{"NAMESPACE", "NAME"},
		Rows:    []Row{{Ref: model.ResourceRef{Kind: "Pod", Namespace: "payments", Name: "api-0"}, Columns: []string{"payments", "api-0"}}},
	}
	var table bytes.Buffer
	if err := RenderList(&table, result, "table", false); err != nil {
		t.Fatalf("RenderList table: %v", err)
	}
	if !strings.Contains(table.String(), "api-0") {
		t.Fatalf("table output missing row: %s", table.String())
	}
	var jsonOut bytes.Buffer
	if err := RenderList(&jsonOut, result, "json", false); err != nil {
		t.Fatalf("RenderList json: %v", err)
	}
	if !strings.Contains(jsonOut.String(), "\"NAME\": \"api-0\"") {
		t.Fatalf("json output missing row: %s", jsonOut.String())
	}
}

func TestRenderListEmptyProducesNoResourcesMessage(t *testing.T) {
	var out bytes.Buffer
	if err := RenderList(&out, &ListResult{Headers: []string{"NAME"}}, "table", false); err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	if !strings.Contains(out.String(), "No resources found") {
		t.Fatalf("expected empty-list message, got %q", out.String())
	}
}

func TestCRDListAndDetailShowCatalogAndReadiness(t *testing.T) {
	crd := model.ResourceNode{
		Ref: model.ResourceRef{APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition", Name: "widgets.example.io", UID: "crd-uid"},
		Details: &model.CRDDetails{
			Group: "example.io", Kind: "Widget", Plural: "widgets", Singular: "widget", Scope: "Namespaced",
			Versions:          []model.CRDVersion{{Name: "v1", Served: true, Storage: true}},
			Conditions:        []model.CRDCondition{{Type: "Established", Status: "True", Reason: "InitialNamesAccepted", Message: "the initial names have been accepted"}},
			CreationTimestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	}

	headers, columns := genericColumns("CustomResourceDefinition")
	row := columns(crd)
	if strings.Join(headers, ",") != "NAME,GROUP,KIND,SCOPE,VERSIONS,ESTABLISHED,AGE" || len(row) != len(headers) {
		t.Fatalf("unexpected CRD list columns: headers=%v row=%v", headers, row)
	}
	for _, value := range []string{"widgets.example.io", "example.io", "Widget", "Namespaced", "v1 (served, storage)", "True"} {
		if !strings.Contains(strings.Join(row, " "), value) {
			t.Errorf("CRD list row missing %q: %v", value, row)
		}
	}

	result := &model.QueryResult{Target: crd.Ref, Graph: model.Graph{Target: crd.Ref, Nodes: []model.ResourceNode{crd}}}
	var detail bytes.Buffer
	if err := RenderDetailText(&detail, result, DetailOptions{}); err != nil {
		t.Fatalf("RenderDetailText: %v", err)
	}
	for _, value := range []string{"CUSTOMRESOURCEDEFINITION: widgets.example.io", "Group: example.io", "Kind: Widget", "Plural: widgets", "Scope: Namespaced", "Established: True", "VERSIONS", "v1", "CONDITIONS", "InitialNamesAccepted", "the initial names have been accepted"} {
		if !strings.Contains(detail.String(), value) {
			t.Errorf("CRD detail missing %q:\n%s", value, detail.String())
		}
	}
}

func TestCustomResourceListAndDetailShowSpecAndStatus(t *testing.T) {
	resource := model.ResourceNode{
		Ref: model.ResourceRef{APIVersion: "example.io/v1", Kind: "Widget", Namespace: "payments", Name: "sample", UID: "widget-uid"},
		Details: &model.CustomResourceDetails{
			SchemaFields: []model.CustomSchemaField{{Path: "spec.replicas", Type: "integer", Description: "Desired replica count"}},
			APIVersion:   "example.io/v1", CreationTimestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Labels: map[string]string{"app": "sample"}, Annotations: map[string]string{"example.io/team": "payments"},
			Config: map[string]any{"endpoint": "https://api.example.test"},
			Spec:   map[string]any{"replicas": int64(3), "image": "example/widget:v1"},
			Status: map[string]any{"ready": true},
			Fields: []model.CustomResourceField{
				{Path: "config.endpoint", Value: "https://api.example.test"},
				{Path: "spec.replicas", Value: "3"},
				{Path: "spec.image", Value: "example/widget:v1"},
				{Path: "status.ready", Value: "true"},
			},
		},
	}

	headers, columns := genericColumns("Widget")
	row := columns(resource)
	if strings.Join(headers, ",") != "NAMESPACE,NAME,API VERSION,AGE" || len(row) != len(headers) {
		t.Fatalf("unexpected custom-resource list columns: headers=%v row=%v", headers, row)
	}
	if row[0] != "payments" || row[1] != "sample" || row[2] != "example.io/v1" || row[3] == "-" {
		t.Fatalf("custom-resource list row missing metadata: %v", row)
	}

	result := &model.QueryResult{Target: resource.Ref, Graph: model.Graph{Target: resource.Ref, Nodes: []model.ResourceNode{resource}}}
	var detail bytes.Buffer
	if err := RenderDetailText(&detail, result, DetailOptions{}); err != nil {
		t.Fatalf("RenderDetailText: %v", err)
	}
	for _, expected := range []string{"SCHEMA", "spec.replicas", "integer", "Desired replica count"} {
		if !strings.Contains(detail.String(), expected) {
			t.Errorf("schema missing %q: %s", expected, detail.String())
		}
	}
	for _, expected := range []string{"WIDGET: sample", "Namespace: payments", "API Version: example.io/v1", "LABELS", "app", "sample", "ANNOTATIONS", "example.io/team", "CONFIG / SPEC / STATUS", "config:", "endpoint: https://api.example.test", "spec:", "replicas: 3", "image: example/widget:v1", "status:", "ready: true"} {
		if !strings.Contains(detail.String(), expected) {
			t.Errorf("custom-resource detail missing %q:\n%s", expected, detail.String())
		}
	}
}

func TestPersistentVolumeAndClaimViewsShowStorageDetails(t *testing.T) {
	claim := model.ResourceNode{
		Ref: model.ResourceRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "data", UID: "claim-uid"},
		Details: &model.PVCDetails{
			Phase: "Bound", RequestedCapacity: "5Gi", Capacity: "5Gi", StorageClassName: "fast",
			AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", VolumeName: "pv-data",
			Selector: "app=database", DataSource: "snapshot.storage.k8s.io/VolumeSnapshot/data-snapshot",
			Conditions: []model.PVCCondition{{Type: "Resizing", Status: "False", Message: "no resize in progress"}},
		},
	}
	volume := model.ResourceNode{
		Ref: model.ResourceRef{APIVersion: "v1", Kind: "PersistentVolume", Name: "pv-data", UID: "pv-uid"},
		Details: &model.PVDetails{
			Phase: "Bound", Capacity: "5Gi", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem",
			StorageClassName: "fast", ReclaimPolicy: "Retain", ClaimNamespace: "payments", ClaimName: "data",
			MountOptions: []string{"discard"},
		},
	}
	pod := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "consumer-0", UID: "pod-uid"},
		Details: &model.PodDetails{Phase: "Running", Ready: "1/1"},
	}

	claimHeaders, claimColumns := genericColumns("PersistentVolumeClaim")
	claimRow := claimColumns(claim)
	if len(claimHeaders) != len(claimRow) || !strings.Contains(strings.Join(claimRow, " "), "5Gi") {
		t.Fatalf("PVC list row missing requested/provisioned capacity: headers=%v row=%v", claimHeaders, claimRow)
	}
	volumeHeaders, volumeColumns := genericColumns("PersistentVolume")
	volumeRow := volumeColumns(volume)
	if len(volumeHeaders) != len(volumeRow) || !strings.Contains(strings.Join(volumeRow, " "), "payments/data") {
		t.Fatalf("PV list row missing bound claim: headers=%v row=%v", volumeHeaders, volumeRow)
	}

	claimResult := &model.QueryResult{
		Target: claim.Ref,
		Graph: model.Graph{Target: claim.Ref, Nodes: []model.ResourceNode{claim, volume, pod}, Edges: []model.Edge{
			{FromUID: claim.Ref.UID, ToUID: volume.Ref.UID, Type: model.RelationVolume},
			{FromUID: pod.Ref.UID, ToUID: claim.Ref.UID, Type: model.RelationVolume, Detail: "spec.volumes[0](data).persistentVolumeClaim"},
		}},
	}
	var claimDetail bytes.Buffer
	if err := RenderDetailText(&claimDetail, claimResult, DetailOptions{}); err != nil {
		t.Fatalf("Render PVC detail: %v", err)
	}
	for _, expected := range []string{"PVC: data", "Namespace: payments", "Requested: 5Gi", "Capacity: 5Gi", "Volume Mode: Filesystem", "Selector: app=database", "VolumeSnapshot/data-snapshot", "BOUND PV", "Reclaim Policy: Retain", "CONDITIONS", "no resize in progress", "USED BY", "consumer-0", "Running"} {
		if !strings.Contains(claimDetail.String(), expected) {
			t.Errorf("PVC detail missing %q:\n%s", expected, claimDetail.String())
		}
	}

	volumeResult := &model.QueryResult{Target: volume.Ref, Graph: model.Graph{Target: volume.Ref, Nodes: []model.ResourceNode{volume, claim, pod}, Edges: []model.Edge{
		{FromUID: claim.Ref.UID, ToUID: volume.Ref.UID, Type: model.RelationVolume},
		{FromUID: pod.Ref.UID, ToUID: claim.Ref.UID, Type: model.RelationVolume},
	}}}
	var volumeDetail bytes.Buffer
	if err := RenderDetailText(&volumeDetail, volumeResult, DetailOptions{}); err != nil {
		t.Fatalf("Render PV detail: %v", err)
	}
	for _, expected := range []string{"PV: pv-data", "Status: Bound", "Capacity: 5Gi", "Volume Mode: Filesystem", "StorageClass: fast", "Reclaim Policy: Retain", "Claim: payments/data", "Mount Options: discard", "BOUND PVC", "USED BY", "consumer-0", "Running"} {
		if !strings.Contains(volumeDetail.String(), expected) {
			t.Errorf("PV detail missing %q:\n%s", expected, volumeDetail.String())
		}
	}
}

func TestPersistentVolumeAndClaimViewsExplainEmptyUsedBy(t *testing.T) {
	claim := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "data", UID: "claim-uid"},
		Details: &model.PVCDetails{Phase: "Bound", VolumeName: "pv-data"},
	}
	volume := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "v1", Kind: "PersistentVolume", Name: "pv-data", UID: "pv-uid"},
		Details: &model.PVDetails{Phase: "Bound", ClaimNamespace: "payments", ClaimName: "data"},
	}
	for _, target := range []model.ResourceNode{claim, volume} {
		graph := model.Graph{Target: target.Ref, Nodes: []model.ResourceNode{claim, volume}}
		if target.Ref.Kind == "PersistentVolumeClaim" {
			graph.Edges = []model.Edge{{FromUID: claim.Ref.UID, ToUID: volume.Ref.UID, Type: model.RelationVolume}}
		} else {
			graph.Edges = []model.Edge{{FromUID: claim.Ref.UID, ToUID: volume.Ref.UID, Type: model.RelationVolume}}
		}
		result := &model.QueryResult{Target: target.Ref, Graph: graph}
		var out bytes.Buffer
		if err := RenderDetailText(&out, result, DetailOptions{}); err != nil {
			t.Fatalf("Render %s detail: %v", target.Ref.Kind, err)
		}
		if !strings.Contains(out.String(), "USED BY") || !strings.Contains(out.String(), "No Pods currently use this claim.") {
			t.Errorf("%s detail should explain empty Pod usage:\n%s", target.Ref.Kind, out.String())
		}
	}
}

func TestPersistentVolumeAndClaimListsShowUsedByPods(t *testing.T) {
	claim := listObject("PersistentVolumeClaim", "payments", "data", map[string]any{
		"spec":   map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": "5Gi"}}},
		"status": map[string]any{"phase": "Bound"},
	})
	volume := listObject("PersistentVolume", "", "pv-data", map[string]any{
		"spec":   map[string]any{"claimRef": map[string]any{"namespace": "payments", "name": "data"}, "capacity": map[string]any{"storage": "5Gi"}},
		"status": map[string]any{"phase": "Bound"},
	})
	consumer := listObject("Pod", "payments", "consumer-0", map[string]any{"spec": map[string]any{"volumes": []any{
		map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "data"}},
	}}})
	sameNameOtherNamespace := listObject("Pod", "payments-dev", "other-0", map[string]any{"spec": map[string]any{"volumes": []any{
		map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "data"}},
	}}})
	reader := &storageListReader{lists: map[string][]unstructured.Unstructured{
		schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}.String(): {*claim},
		schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}.String():      {*volume},
		schema.GroupVersionResource{Version: "v1", Resource: "pods"}.String():                   {*consumer, *sameNameOtherNamespace},
	}}

	pvcResult, err := ListGeneric(context.Background(), reader, ListRequest{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"},
		Kind:     "PersistentVolumeClaim", Namespace: "payments",
	})
	if err != nil {
		t.Fatalf("ListGeneric PVC: %v", err)
	}
	if got := pvcResult.Rows[0].Columns[len(pvcResult.Rows[0].Columns)-1]; got != "consumer-0" {
		t.Fatalf("PVC USED BY = %q, want consumer-0", got)
	}

	pvResult, err := ListGeneric(context.Background(), reader, ListRequest{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, Kind: "PersistentVolume",
	})
	if err != nil {
		t.Fatalf("ListGeneric PV: %v", err)
	}
	if got := pvResult.Rows[0].Columns[len(pvResult.Rows[0].Columns)-1]; got != "payments/consumer-0" {
		t.Fatalf("PV USED BY = %q, want payments/consumer-0", got)
	}
}

type storageListReader struct {
	lists map[string][]unstructured.Unstructured
}

var _ k8s.Reader = (*storageListReader)(nil)

func (r *storageListReader) Get(context.Context, schema.GroupVersionResource, string, string) (*unstructured.Unstructured, error) {
	return nil, nil
}

func (r *storageListReader) List(_ context.Context, resource schema.GroupVersionResource, namespace string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	result := &unstructured.UnstructuredList{}
	for _, item := range r.lists[resource.String()] {
		if namespace == "" || item.GetNamespace() == namespace {
			result.Items = append(result.Items, *item.DeepCopy())
		}
	}
	return result, nil
}

func (r *storageListReader) ListMetadata(context.Context, schema.GroupVersionResource, string, metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	return nil, nil
}

func (r *storageListReader) APIResources(context.Context) ([]*metav1.APIResourceList, error) {
	return nil, nil
}

func listObject(kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": kind,
		"metadata": map[string]any{"namespace": namespace, "name": name},
	}}
	for key, value := range fields {
		object.Object[key] = value
	}
	return object
}

func TestConfigMapDetailShowsUsageLocationAndOwner(t *testing.T) {
	configMap := model.ResourceNode{Ref: model.ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "app-config", UID: "cm-uid"}, Details: &model.ConfigMapDetails{Keys: []string{"MODE"}}}
	pod := model.ResourceNode{Ref: model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0", UID: "pod-uid"}}
	replicaSet := model.ResourceNode{Ref: model.ResourceRef{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: "payments", Name: "api-rs", UID: "rs-uid"}}
	deployment := model.ResourceNode{Ref: model.ResourceRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "api", UID: "deployment-uid"}}
	result := &model.QueryResult{
		Target: configMap.Ref,
		Graph: model.Graph{
			Target: configMap.Ref,
			Nodes:  []model.ResourceNode{configMap, pod, replicaSet, deployment},
			Edges: []model.Edge{
				{FromUID: "pod-uid", ToUID: "cm-uid", Type: model.RelationConfig, Detail: "spec.containers[0](api).env[0](MODE).configMapKeyRef key=MODE"},
				{FromUID: "pod-uid", ToUID: "rs-uid", Type: model.RelationOwner},
				{FromUID: "rs-uid", ToUID: "deployment-uid", Type: model.RelationOwner},
			},
		},
	}
	var out bytes.Buffer
	if err := RenderDetail(&out, result, "table", DetailOptions{}); err != nil {
		t.Fatalf("RenderDetail: %v", err)
	}
	for _, expected := range []string{"USED BY", "Pod/payments/api-0", "configMapKeyRef key=MODE", "Deployment/api"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("ConfigMap detail missing %q:\n%s", expected, out.String())
		}
	}
}

func TestTableFlushAlignsColumnsWhenColorized(t *testing.T) {
	colorEnabled = true
	defer func() { colorEnabled = false }()

	table := NewTable("NAME", "STATUS", "AGE")
	table.Row("a", "Running", "1d")
	table.Row("longer-name", "CrashLoopBackOff", "2d")
	var out bytes.Buffer
	if err := table.Flush(&out); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	// Every data row's AGE column must start at the same visible column, even
	// though the STATUS cells are wrapped in ANSI codes of different lengths.
	firstAgeOffset := strings.Index(stripANSI(lines[1]), "1d")
	secondAgeOffset := strings.Index(stripANSI(lines[2]), "2d")
	if firstAgeOffset != secondAgeOffset {
		t.Fatalf("AGE column misaligned: row1 offset %d, row2 offset %d", firstAgeOffset, secondAgeOffset)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestStatusColorClassification(t *testing.T) {
	colorEnabled = true
	defer func() { colorEnabled = false }()
	if !strings.Contains(statusColor("Running"), "32m") {
		t.Fatalf("Running should be green: %q", statusColor("Running"))
	}
	if !strings.Contains(statusColor("CrashLoopBackOff"), "31m") {
		t.Fatalf("CrashLoopBackOff should be red: %q", statusColor("CrashLoopBackOff"))
	}
	if !strings.Contains(statusColor("Pending"), "33m") {
		t.Fatalf("Pending should be yellow: %q", statusColor("Pending"))
	}
	if colorEnabled == false {
		t.Fatal("test setup invalid")
	}
	colorEnabled = false
	if statusColor("Running") != "Running" {
		t.Fatalf("color disabled must return plain text, got %q", statusColor("Running"))
	}
}

func TestDetailWarningsAreOptInForAllFormats(t *testing.T) {
	result := &model.QueryResult{
		Target:   model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "ns", Name: "pod", UID: "pod-uid"},
		Graph:    model.Graph{Target: model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "ns", Name: "pod", UID: "pod-uid"}},
		Warnings: []model.QueryWarning{{Resource: model.ResourceRef{Kind: "Service", Namespace: "ns", Name: "svc"}, Operation: "list", Message: "forbidden"}},
	}
	for _, format := range []string{"table", "tree", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			if err := RenderDetail(&out, result, format, DetailOptions{}); err != nil {
				t.Fatalf("RenderDetail default: %v", err)
			}
			if strings.Contains(out.String(), "WARNINGS") || strings.Contains(out.String(), "forbidden") {
				t.Fatalf("warnings leaked without opt-in: %s", out.String())
			}
			out.Reset()
			if err := RenderDetail(&out, result, format, DetailOptions{ShowWarnings: true}); err != nil {
				t.Fatalf("RenderDetail with events: %v", err)
			}
			if !strings.Contains(out.String(), "forbidden") {
				t.Fatalf("--events output omitted warning: %s", out.String())
			}
		})
	}
}
