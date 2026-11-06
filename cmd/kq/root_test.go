package main

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestResolveDiscoveredResourceByKind(t *testing.T) {
	lists := []*metav1.APIResourceList{{
		GroupVersion: "example.io/v1",
		APIResources: []metav1.APIResource{
			{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}},
			{Name: "widgets/status", Kind: "Widget", Namespaced: true},
		},
	}}

	for _, query := range []string{"Widget", "widget"} {
		resource, kind, clusterScoped, err := resolveDiscoveredResource(lists, query)
		if err != nil {
			t.Fatalf("resolve Kind %q: %v", query, err)
		}
		if resource != (schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}) || kind != "Widget" || clusterScoped {
			t.Fatalf("unexpected resolved resource: %s kind=%q clusterScoped=%t", resource, kind, clusterScoped)
		}
	}
}

func TestRootRoutesUnknownCommandToResourceLookup(t *testing.T) {
	root := newRootCommand()
	command, args, err := root.Find([]string{"widget", "sample"})
	if err != nil {
		t.Fatalf("Find custom Kind command: %v", err)
	}
	if command != root || len(args) != 2 || args[0] != "widget" || args[1] != "sample" || root.RunE == nil {
		t.Fatalf("custom Kind was not routed through root: command=%q args=%v", command.CommandPath(), args)
	}
}

func TestResolveDiscoveredResourceByQualifiedNameAndKindGroup(t *testing.T) {
	lists := []*metav1.APIResourceList{{
		GroupVersion: "example.io/v1",
		APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true}},
	}}

	for _, query := range []string{"widgets.example.io", "Widget.example.io"} {
		resource, kind, clusterScoped, err := resolveDiscoveredResource(lists, query)
		if err != nil {
			t.Fatalf("resolve %q: %v", query, err)
		}
		if resource.Resource != "widgets" || resource.Group != "example.io" || kind != "Widget" || clusterScoped {
			t.Errorf("unexpected result for %q: %s kind=%q clusterScoped=%t", query, resource, kind, clusterScoped)
		}
	}
}

func TestResolveDiscoveredResourceRejectsAmbiguousKind(t *testing.T) {
	lists := []*metav1.APIResourceList{
		{GroupVersion: "alpha.example.io/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget"}}},
		{GroupVersion: "beta.example.io/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget"}}},
	}

	_, _, _, err := resolveDiscoveredResource(lists, "Widget")
	if err == nil || !strings.Contains(err.Error(), "widgets.alpha.example.io") || !strings.Contains(err.Error(), "widgets.beta.example.io") {
		t.Fatalf("expected actionable ambiguity error, got %v", err)
	}
}
