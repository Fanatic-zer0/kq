package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

type Reader interface {
	Get(context.Context, schema.GroupVersionResource, string, string) (*unstructured.Unstructured, error)
	List(context.Context, schema.GroupVersionResource, string, v1.ListOptions) (*unstructured.UnstructuredList, error)
	ListMetadata(context.Context, schema.GroupVersionResource, string, v1.ListOptions) (*v1.PartialObjectMetadataList, error)
	APIResources(context.Context) ([]*v1.APIResourceList, error)
}

type Client struct {
	dynamic      dynamic.Interface
	discovery    discovery.DiscoveryInterface
	metadata     metadata.Interface
	rest         rest.Interface
	host         string
	requestSlots chan struct{}
	getMu        sync.Mutex
	getFlights   map[string]*getCall
	mu           sync.Mutex
	resources    []*v1.APIResourceList
	objectsMu    sync.RWMutex
	byLookup     map[string]*unstructured.Unstructured
	byUID        map[string]*unstructured.Unstructured
}

type getCall struct {
	done   chan struct{}
	object *unstructured.Unstructured
	err    error
}

func New(config *rest.Config) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("Kubernetes REST config is required")
	}
	readOnlyConfig := rest.CopyConfig(config)
	readOnlyConfig.UserAgent = "kq/0.1"
	if readOnlyConfig.QPS <= 0 {
		readOnlyConfig.QPS = defaultQPS
	}
	if readOnlyConfig.Burst <= 0 {
		readOnlyConfig.Burst = defaultBurst
	}
	if readOnlyConfig.RateLimiter == nil {
		readOnlyConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(readOnlyConfig.QPS, readOnlyConfig.Burst)
	}
	previousWrap := readOnlyConfig.WrapTransport
	readOnlyConfig.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		if previousWrap != nil {
			base = previousWrap(base)
		}
		return readOnlyTransport{next: base}
	}

	httpClient, err := rest.HTTPClientFor(readOnlyConfig)
	if err != nil {
		return nil, fmt.Errorf("create shared HTTP client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfigAndClient(readOnlyConfig, httpClient)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(readOnlyConfig, httpClient)
	if err != nil {
		return nil, fmt.Errorf("create discovery client: %w", err)
	}
	metadataClient, err := metadata.NewForConfigAndClient(readOnlyConfig, httpClient)
	if err != nil {
		return nil, fmt.Errorf("create metadata client: %w", err)
	}
	return &Client{
		dynamic: dynamicClient, discovery: discoveryClient, metadata: metadataClient, rest: discoveryClient.RESTClient(), host: config.Host,
		requestSlots: make(chan struct{}, maxInFlightRequests),
		getFlights:   make(map[string]*getCall),
		byLookup:     make(map[string]*unstructured.Unstructured), byUID: make(map[string]*unstructured.Unstructured),
	}, nil
}

func (c *Client) Get(ctx context.Context, resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if name == "" {
		return nil, fmt.Errorf("resource name must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := lookupKey(resource, namespace, name)
	if resource.Resource != "secrets" {
		c.objectsMu.RLock()
		cached := c.byLookup[key]
		c.objectsMu.RUnlock()
		if cached != nil {
			return cached.DeepCopy(), nil
		}
	}

	c.getMu.Lock()
	if call, ok := c.getFlights[key]; ok {
		c.getMu.Unlock()
		select {
		case <-call.done:
			if call.err != nil {
				return nil, call.err
			}
			return call.object.DeepCopy(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &getCall{done: make(chan struct{})}
	c.getFlights[key] = call
	c.getMu.Unlock()

	if err := c.acquireRequestSlot(ctx); err != nil {
		c.finishGet(key, call, nil, err)
		return nil, err
	}
	object, err := c.dynamic.Resource(resource).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	c.releaseRequestSlot()
	if resource.Resource != "secrets" {
		c.cacheObject(resource, namespace, object)
	}
	c.finishGet(key, call, object, err)
	if err != nil {
		return nil, err
	}
	return object, nil
}

func (c *Client) finishGet(key string, call *getCall, object *unstructured.Unstructured, err error) {
	c.getMu.Lock()
	call.object = object
	call.err = err
	delete(c.getFlights, key)
	close(call.done)
	c.getMu.Unlock()
}

func (c *Client) List(ctx context.Context, resource schema.GroupVersionResource, namespace string, options v1.ListOptions) (*unstructured.UnstructuredList, error) {
	if err := c.acquireRequestSlot(ctx); err != nil {
		return nil, err
	}
	defer c.releaseRequestSlot()
	list, err := c.dynamic.Resource(resource).Namespace(namespace).List(ctx, options)
	if err != nil {
		return nil, err
	}
	if resource.Resource != "secrets" {
		for i := range list.Items {
			c.cacheObject(resource, list.Items[i].GetNamespace(), &list.Items[i])
		}
	}
	return list, nil
}

func (c *Client) ListMetadata(ctx context.Context, resource schema.GroupVersionResource, namespace string, options v1.ListOptions) (*v1.PartialObjectMetadataList, error) {
	if err := c.acquireRequestSlot(ctx); err != nil {
		return nil, err
	}
	defer c.releaseRequestSlot()
	return c.metadata.Resource(resource).Namespace(namespace).List(ctx, options)
}

func (c *Client) acquireRequestSlot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.requestSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			c.releaseRequestSlot()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) releaseRequestSlot() {
	<-c.requestSlots
}

func (c *Client) cacheObject(resource schema.GroupVersionResource, namespace string, object *unstructured.Unstructured) {
	if object == nil || object.GetName() == "" || resource.Resource == "secrets" {
		return
	}
	copy := object.DeepCopy()
	c.objectsMu.Lock()
	defer c.objectsMu.Unlock()
	if uid := string(object.GetUID()); uid != "" {
		if cached := c.byUID[uid]; cached != nil {
			copy = cached
		}
		c.byUID[uid] = copy
	}
	c.byLookup[lookupKey(resource, namespace, object.GetName())] = copy
}

func lookupKey(resource schema.GroupVersionResource, namespace, name string) string {
	return resource.String() + "\x00" + namespace + "\x00" + name
}

type readOnlyTransport struct{ next http.RoundTripper }

func (t readOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet {
		return nil, fmt.Errorf("kq read-only transport rejected HTTP %s", request.Method)
	}
	return t.next.RoundTrip(request)
}

// discoveryCacheTTL matches kubectl's default on-disk discovery cache lifetime.
const discoveryCacheTTL = 10 * time.Minute

// discoveryConcurrency bounds parallel group-version discovery requests; a
// cluster with many CRDs/operators can have 100+ API groups, and discovering
// them sequentially (the previous behavior) dominated detail-view latency.
const discoveryConcurrency = 16

const maxInFlightRequests = 8

// defaultQPS/defaultBurst raise client-go's conservative default client-side
// rate limit (5 QPS / 10 burst), which otherwise throttles a single read-only
// kq invocation's concurrent association lookups regardless of how much the
// traversal itself is parallelized.
const (
	defaultQPS   float32 = 20
	defaultBurst int     = 40
)

type discoveryCacheEntry struct {
	CachedAt  time.Time             `json:"cachedAt"`
	Resources []*v1.APIResourceList `json:"resources"`
}

// APIResources uses contextual GET requests rather than the discovery client's
// non-contextual convenience methods, so cancellation applies to every request.
// Results are cached in-process for the command's lifetime and on disk for
// discoveryCacheTTL, since a full group-version walk is the dominant cost of
// every kq invocation against a cluster with many installed CRDs/operators.
func (c *Client) APIResources(ctx context.Context) ([]*v1.APIResourceList, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resources != nil {
		return c.resources, nil
	}
	if cached, ok := c.readDiscoveryCache(); ok {
		c.resources = cached
		return cached, nil
	}

	var coreVersions v1.APIVersions
	if err := c.rest.Get().AbsPath("/api").Do(ctx).Into(&coreVersions); err != nil {
		return nil, fmt.Errorf("discover core API versions: %w", err)
	}
	var groups v1.APIGroupList
	if err := c.rest.Get().AbsPath("/apis").Do(ctx).Into(&groups); err != nil {
		return nil, fmt.Errorf("discover API groups: %w", err)
	}

	paths := make([]string, 0, len(coreVersions.Versions)+len(groups.Groups))
	for _, version := range coreVersions.Versions {
		paths = append(paths, "/api/"+version)
	}
	groupVersions := make([]string, 0, len(groups.Groups))
	for _, group := range groups.Groups {
		for _, version := range group.Versions {
			groupVersions = append(groupVersions, version.GroupVersion)
		}
	}
	sort.Strings(groupVersions)
	for _, groupVersion := range groupVersions {
		paths = append(paths, "/apis/"+groupVersion)
	}

	resources, err := c.discoverPaths(ctx, paths)
	if err != nil {
		return nil, err
	}
	c.resources = resources
	c.writeDiscoveryCache(resources)
	return resources, nil
}

// discoverPaths fetches each discovery path concurrently, bounded by
// discoveryConcurrency, and returns results in a stable (input) order.
func (c *Client) discoverPaths(ctx context.Context, paths []string) ([]*v1.APIResourceList, error) {
	results := make([]*v1.APIResourceList, len(paths))
	errs := make([]error, len(paths))
	semaphore := make(chan struct{}, discoveryConcurrency)
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(index int, path string) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				errs[index] = ctx.Err()
				return
			}
			defer func() { <-semaphore }()
			if err := c.acquireRequestSlot(ctx); err != nil {
				errs[index] = err
				return
			}
			defer c.releaseRequestSlot()
			var list v1.APIResourceList
			if err := c.rest.Get().AbsPath(path).Do(ctx).Into(&list); err != nil {
				errs[index] = fmt.Errorf("discover API path %s: %w", path, err)
				return
			}
			results[index] = &list
		}(i, path)
	}
	wg.Wait()
	resources := make([]*v1.APIResourceList, 0, len(paths))
	for i, list := range results {
		if errs[i] != nil {
			return nil, errs[i]
		}
		resources = append(resources, list)
	}
	return resources, nil
}

func (c *Client) discoveryCachePath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(c.host))
	return filepath.Join(base, "kq", "discovery", hex.EncodeToString(digest[:])+".json"), nil
}

func (c *Client) readDiscoveryCache() ([]*v1.APIResourceList, bool) {
	path, err := c.discoveryCachePath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var entry discoveryCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	if time.Since(entry.CachedAt) > discoveryCacheTTL {
		return nil, false
	}
	return entry.Resources, true
}

func (c *Client) writeDiscoveryCache(resources []*v1.APIResourceList) {
	path, err := c.discoveryCachePath()
	if err != nil {
		return
	}
	data, err := json.Marshal(discoveryCacheEntry{CachedAt: time.Now(), Resources: resources})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}
