package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func TestCRDDetailOrGroup(t *testing.T) {
	resource := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	crd := func(name, group, uid string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": name, "uid": uid},
			"spec":     map[string]any{"group": group, "scope": "Namespaced", "names": map[string]any{"kind": "Kafka", "plural": "kafkas"}},
		}}
	}
	kafka := crd("kafkas.kafka.strimzi.io", "kafka.strimzi.io", "kafka-uid")
	topic := crd("kafkatopics.kafka.strimzi.io", "kafka.strimzi.io", "topic-uid")
	other := crd("widgets.example.io", "example.io", "widget-uid")
	tests := []struct {
		name      string
		input     string
		getError  error
		want      []string
		absent    []string
		wantError string
		wantLists int
	}{
		{name: "exact name", input: "kafkas.kafka.strimzi.io", want: []string{"CUSTOMRESOURCEDEFINITION: kafkas.kafka.strimzi.io"}},
		{name: "group", input: "kafka.strimzi.io", want: []string{"kafkas.kafka.strimzi.io", "kafkatopics.kafka.strimzi.io"}, absent: []string{"widgets.example.io"}, wantLists: 1},
		{name: "missing group", input: "missing.example.io", wantError: "not found", wantLists: 1},
		{name: "forbidden", input: "kafka.strimzi.io", getError: apierrors.NewForbidden(resource.GroupResource(), "kafka.strimzi.io", fmt.Errorf("denied")), wantError: "forbidden"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			reader := &crdReader{objects: []unstructured.Unstructured{kafka, topic, other}, getError: testCase.getError}
			var output bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&output)
			err := executeCRDDetailOrGroup(command, &flags{output: "table"}, reader, context.Background(), resourceKind{kind: "CustomResourceDefinition", resource: resource}, "", testCase.input)
			if testCase.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("error = %v, want %q", err, testCase.wantError)
				}
			} else if err != nil {
				t.Fatalf("query: %v", err)
			}
			if reader.listCalls != testCase.wantLists {
				t.Errorf("list calls = %d, want %d", reader.listCalls, testCase.wantLists)
			}
			for _, expected := range testCase.want {
				if !strings.Contains(output.String(), expected) {
					t.Errorf("output missing %q: %s", expected, output.String())
				}
			}
			for _, unexpected := range testCase.absent {
				if strings.Contains(output.String(), unexpected) {
					t.Errorf("output contains %q: %s", unexpected, output.String())
				}
			}
		})
	}
}

type crdReader struct {
	objects   []unstructured.Unstructured
	getError  error
	listCalls int
}

func TestCustomDefinitionFailureIsVisibleWithoutEvents(t *testing.T) {
	object := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "sample", "namespace": "payments", "uid": "widget-uid"},
		"spec":     map[string]any{"replicas": int64(3)},
	}}
	reader := &crdReader{objects: []unstructured.Unstructured{object}}
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	err := executeDetail(command, &flags{output: "table", depth: 1}, reader, context.Background(), schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}, "Widget", "payments", "sample")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "custom resource definition lookup") || !strings.Contains(output.String(), "not found") {
		t.Fatalf("missing default incomplete-result warning: %s", output.String())
	}
}

func (reader *crdReader) Get(_ context.Context, resource schema.GroupVersionResource, _ string, name string) (*unstructured.Unstructured, error) {
	if reader.getError != nil {
		return nil, reader.getError
	}
	for index := range reader.objects {
		if reader.objects[index].GetName() == name {
			return reader.objects[index].DeepCopy(), nil
		}
	}
	return nil, apierrors.NewNotFound(resource.GroupResource(), name)
}

func (reader *crdReader) List(_ context.Context, _ schema.GroupVersionResource, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	reader.listCalls++
	return &unstructured.UnstructuredList{Items: reader.objects}, nil
}

func (reader *crdReader) ListMetadata(context.Context, schema.GroupVersionResource, string, metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	return nil, fmt.Errorf("unexpected metadata list")
}

func (reader *crdReader) APIResources(context.Context) ([]*metav1.APIResourceList, error) {
	return nil, fmt.Errorf("unexpected API discovery")
}
