package query

import (
	"context"
	"fmt"
	"testing"

	"github.com/Fanatic-zer0/kq/internal/model"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCustomTargetEnrichmentReadBudget(t *testing.T) {
	widget := resourceObject("example.io/v1", "Widget", "payments", "sample", "widget-uid")
	config := resourceObject("v1", "ConfigMap", "other", "settings", "config-uid")
	widget.Object["spec"] = map[string]any{
		"config":        map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "namespace": "other", "name": "settings"},
		"duplicate":     map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "namespace": "other", "name": "settings"},
		"tlsRef":        map[string]any{"apiVersion": "v1", "kind": "Secret", "namespace": "other", "name": "tls"},
		"notAReference": "settings",
	}
	crd := resourceObject("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "widgets.example.io", "crd-uid")
	crd.Object["spec"] = map[string]any{
		"group": "example.io", "names": map[string]any{"kind": "Widget", "plural": "widgets"},
		"versions": []any{map[string]any{"name": "v1", "schema": map[string]any{"openAPIV3Schema": map[string]any{
			"type": "object", "properties": map[string]any{"spec": map[string]any{
				"type": "object", "properties": map[string]any{"replicas": map[string]any{"type": "integer", "description": "Desired replica count"}},
			}},
		}}}},
	}
	reader := readerWithObjects(widget, config)
	crdResource := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	reader.objects[objectKey(crdResource, "", crd.GetName())] = crd
	result, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"},
		Kind:     "Widget", Namespace: "payments", Name: "sample", Depth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", result.Warnings)
	}
	if reader.discoveryCalls != 0 || len(reader.listCalls) != 0 {
		t.Fatalf("unexpected discovery/list calls: %d / %v", reader.discoveryCalls, reader.listCalls)
	}
	reads := 0
	for _, count := range reader.getCalls {
		reads += count
	}
	if reads != 3 {
		t.Fatalf("GET count = %d, want target + CRD + one deduplicated ConfigMap", reads)
	}
	var details *model.CustomResourceDetails
	for _, node := range result.Graph.Nodes {
		if node.Ref.UID == "widget-uid" {
			details, _ = node.Details.(*model.CustomResourceDetails)
		}
	}
	if details == nil || len(details.SchemaFields) != 2 || details.SchemaFields[1].Path != "spec.replicas" || details.SchemaFields[1].Type != "integer" {
		t.Fatalf("missing version-specific schema: %+v", details)
	}
	definition, reference, secret := false, false, false
	for _, edge := range result.Graph.Edges {
		definition = definition || edge.Type == model.RelationDefinition
		reference = reference || edge.Type == model.RelationReference && edge.To.Namespace == "other"
		secret = secret || edge.Type == model.RelationSecret && edge.To.Namespace == "other"
	}
	if !definition || !reference || !secret {
		t.Fatalf("missing relationships: %+v", result.Graph.Edges)
	}
}

func TestCustomReferenceCollectionIsBounded(t *testing.T) {
	items := make([]any, 1000)
	for index := range items {
		items[index] = map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": fmt.Sprintf("config-%d", index)}
	}
	refs := make(map[string]model.ResourceRef)
	budget := 512
	collectCustomReferences(items, "payments", 0, &budget, refs)
	if len(refs) != 8 || budget < 480 {
		t.Fatalf("reference collection exceeded budget: refs=%d remaining=%d", len(refs), budget)
	}
}

func TestBuiltinTargetSkipsCustomEnrichment(t *testing.T) {
	service := resourceObject("v1", "Service", "payments", "api", "service-uid")
	reader := readerWithObjects(service)
	_, err := New(reader).Run(context.Background(), Request{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "services"},
		Kind:     "Service", Namespace: "payments", Name: "api", Depth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.getCalls) != 1 || reader.discoveryCalls != 0 {
		t.Fatalf("builtin query added enrichment reads: %v", reader.getCalls)
	}
}