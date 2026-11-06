package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fanatic-zer0/kq/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

func TestDynamicGetUsesReadOnlyGET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("unexpected Kubernetes method %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != "/api/v1/namespaces/test/pods/sample" {
			t.Errorf("unexpected request path %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name": "sample", "namespace": "test", "uid": "pod-uid",
			},
		})
	}))
	defer server.Close()

	client, err := k8s.New(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	pod, err := client.Get(context.Background(), schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "test", "sample")
	if err != nil {
		t.Fatalf("GET Pod: %v", err)
	}
	if pod.GetUID() != "pod-uid" {
		t.Fatalf("got UID %q, want pod-uid", pod.GetUID())
	}
}

func TestMetadataListRequestsPartialObjectMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("unexpected Kubernetes method %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != "/apis/apps/v1/namespaces/test/replicasets" {
			t.Errorf("unexpected request path %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.Contains(request.Header.Get("Accept"), "PartialObjectMetadataList") {
			t.Errorf("request did not negotiate PartialObjectMetadataList: %q", request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(&metav1.PartialObjectMetadataList{
			TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadataList"},
			Items: []metav1.PartialObjectMetadata{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sample-rs", Namespace: "test", UID: types.UID("rs-uid"),
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "sample", UID: types.UID("deployment-uid")}},
				},
			}},
		})
	}))
	defer server.Close()

	client, err := k8s.New(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	list, err := client.ListMetadata(context.Background(), schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "test", metav1.ListOptions{Limit: 500})
	if err != nil {
		t.Fatalf("ListMetadata ReplicaSets: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].GetOwnerReferences()[0].UID != "deployment-uid" {
		t.Fatalf("unexpected metadata list result: %+v", list.Items)
	}
}
