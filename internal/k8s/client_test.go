package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestReadOnlyTransportAllowsGETAndRejectsWrites(t *testing.T) {
	transport := readOnlyTransport{next: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}

	request, _ := http.NewRequest(http.MethodGet, "https://cluster.example/api", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("GET was rejected: %v", err)
	}
	response.Body.Close()

	request, _ = http.NewRequest(http.MethodPost, "https://cluster.example/api", nil)
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("POST was allowed by read-only transport")
	}
}

func TestCacheDeduplicatesByUIDAndNeverCachesSecrets(t *testing.T) {
	client := &Client{byLookup: make(map[string]*unstructured.Unstructured), byUID: make(map[string]*unstructured.Unstructured)}
	resource := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	first := &unstructured.Unstructured{}
	first.SetName("pod-a")
	first.SetNamespace("ns")
	first.SetUID("shared-uid")
	second := &unstructured.Unstructured{}
	second.SetName("pod-b")
	second.SetNamespace("ns")
	second.SetUID("shared-uid")

	client.cacheObject(resource, "ns", first)
	client.cacheObject(resource, "ns", second)
	if client.byLookup[lookupKey(resource, "ns", "pod-a")] != client.byLookup[lookupKey(resource, "ns", "pod-b")] {
		t.Fatal("objects with the same UID were not deduplicated")
	}

	secretResource := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secret := &unstructured.Unstructured{}
	secret.SetName("credentials")
	secret.SetNamespace("ns")
	secret.SetUID("secret-uid")
	client.cacheObject(secretResource, "ns", secret)
	if len(client.byUID) != 1 {
		t.Fatalf("Secret was cached; UID cache contains %d objects", len(client.byUID))
	}
}

func TestCanceledGetDoesNotWaitForRequestSlot(t *testing.T) {
	client := &Client{requestSlots: make(chan struct{}, 1)}
	client.requestSlots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Get(ctx, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "ns", "pod")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v, want context.Canceled", err)
	}
}

func TestConcurrentGetsForSameObjectUseOneRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet {
			t.Errorf("unexpected method %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		time.Sleep(25 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "shared", "namespace": "ns", "uid": "shared-uid"},
		})
	}))
	defer server.Close()
	client, err := New(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const callers = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			object, err := client.Get(context.Background(), schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "ns", "shared")
			if err != nil {
				t.Errorf("Get: %v", err)
			} else if object.GetUID() != "shared-uid" {
				t.Errorf("got UID %q, want shared-uid", object.GetUID())
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := requests.Load(); got != 1 {
		t.Fatalf("underlying HTTP GET count = %d, want 1", got)
	}
}
