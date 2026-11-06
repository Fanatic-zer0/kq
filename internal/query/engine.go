package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/k8s"
	"github.com/Fanatic-zer0/kq/internal/model"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// traversalConcurrency bounds how many sibling resources (e.g. Pods owned by
// the same ReplicaSet) are resolved in parallel per BFS level, so a workload
// with many replicas doesn't pay its association-resolution latency serially.
const traversalConcurrency = 10

type Request struct {
	Resource       schema.GroupVersionResource
	Kind           string
	Namespace      string
	Name           string
	Depth          int
	Since          time.Duration
	IncludeEvents  bool
	MaxEvents      int
	ShowSecretKeys bool
	IncludeRBAC    bool
	ExcludedKinds  map[string]bool
}

type Engine struct {
	client          k8s.Reader
	listCacheMu     sync.Mutex
	listCache       map[string][]unstructured.Unstructured
	listFlights     map[string]*listCall
	metadataCache   map[string][]metav1.PartialObjectMetadata
	metadataFlights map[string]*metadataListCall
}

func New(client k8s.Reader) *Engine {
	return &Engine{
		client: client, listCache: make(map[string][]unstructured.Unstructured), listFlights: make(map[string]*listCall),
		metadataCache: make(map[string][]metav1.PartialObjectMetadata), metadataFlights: make(map[string]*metadataListCall),
	}
}

type listCall struct {
	done  chan struct{}
	items []unstructured.Unstructured
	err   error
}

type metadataListCall struct {
	done  chan struct{}
	items []metav1.PartialObjectMetadata
	err   error
}

func (e *Engine) Run(ctx context.Context, request Request) (*model.QueryResult, error) {
	if request.Name == "" || request.Kind == "" || request.Resource.Resource == "" {
		return nil, fmt.Errorf("target resource, kind, and name are required")
	}
	if request.Depth < 0 {
		return nil, fmt.Errorf("depth must be non-negative")
	}
	if request.MaxEvents < 0 {
		return nil, fmt.Errorf("max-events must be non-negative")
	}
	if strings.EqualFold(request.Kind, "Secret") && !request.ShowSecretKeys {
		return nil, fmt.Errorf("querying a Secret requires --show-secret-keys because Kubernetes does not support metadata-only GET for Secret objects")
	}

	targetObject, err := e.client.Get(ctx, request.Resource, request.Namespace, request.Name)
	if err != nil {
		return nil, fmt.Errorf("get target %s/%s: %w", request.Kind, request.Name, err)
	}
	targetNode := project(targetObject, request.Kind, request.Resource.GroupVersion().String(), request.ShowSecretKeys)
	builder := graph.New(targetNode.Ref)
	builder.AddNode(targetNode)
	result := &model.QueryResult{GeneratedAt: time.Now().UTC(), Target: targetNode.Ref}

	var depthsMu sync.Mutex
	depths := map[string]int{targetNode.Ref.UID: 0}

	var apiResources []*metav1.APIResourceList
	var discoveryErr error
	discoveryAttempted := false

	currentLevel := []visit{{object: targetObject, depth: 0}}
	for len(currentLevel) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("query %s/%s: %w", request.Kind, request.Name, err)
		}
		needsDiscovery := false
		for _, current := range currentLevel {
			if current.depth >= request.Depth || isExcluded(request.ExcludedKinds, current.object.GetKind()) {
				continue
			}
			for _, owner := range current.object.GetOwnerReferences() {
				if _, _, ok := knownOwnerResource(owner.APIVersion, owner.Kind); !ok {
					needsDiscovery = true
					break
				}
			}
		}
		if needsDiscovery && !discoveryAttempted {
			discoveryAttempted = true
			apiResources, discoveryErr = e.client.APIResources(ctx)
			if discoveryErr != nil {
				result.Warnings = append(result.Warnings, warningFor(targetObject, "discovery", discoveryErr))
			}
		}
		var nextLevel []visit
		var levelWarnings []model.QueryWarning
		var levelMu sync.Mutex
		var wg sync.WaitGroup
		workers := min(traversalConcurrency, len(currentLevel))
		jobs := make(chan visit)
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for current := range jobs {
					if ctx.Err() != nil {
						return
					}
					expandChildren := targetNode.Ref.Kind != "Pod" || current.object.GetUID() == targetObject.GetUID()
					localNext, localWarnings := e.processVisit(ctx, current, builder, request, apiResources, discoveryErr, depths, &depthsMu, expandChildren)
					levelMu.Lock()
					levelWarnings = append(levelWarnings, localWarnings...)
					nextLevel = append(nextLevel, localNext...)
					levelMu.Unlock()
				}
			}()
		}
		for _, current := range currentLevel {
			if current.depth >= request.Depth || isExcluded(request.ExcludedKinds, current.object.GetKind()) {
				continue
			}
			select {
			case jobs <- current:
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				break
			}
		}
		close(jobs)
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("query %s/%s: %w", request.Kind, request.Name, err)
		}
		result.Warnings = append(result.Warnings, levelWarnings...)
		currentLevel = nextLevel
	}
	result.Warnings = append(result.Warnings, e.resolveWorkloadHPAs(ctx, builder, request, depths)...)

	var events []model.EventSummary
	if request.IncludeEvents && request.MaxEvents > 0 {
		var warnings []model.QueryWarning
		events, warnings = e.collectEvents(ctx, builder.Value().Nodes, request)
		result.Warnings = append(result.Warnings, warnings...)
	}
	result.Graph = builder.Value()
	result.Graph.Events = events
	return result, nil
}

func (e *Engine) processVisit(ctx context.Context, current visit, builder *graph.Builder, request Request, apiResources []*metav1.APIResourceList, discoveryErr error, depths map[string]int, depthsMu *sync.Mutex, expandChildren bool) ([]visit, []model.QueryWarning) {
	var next []visit
	var warnings []model.QueryWarning
	if discoveryErr == nil {
		parents, parentWarnings := e.parents(ctx, current.object, apiResources, request)
		warnings = append(warnings, parentWarnings...)
		for _, parent := range parents {
			if builder.AddNode(parent.node) {
				depthsMu.Lock()
				depths[parent.node.Ref.UID] = current.depth + 1
				depthsMu.Unlock()
				next = append(next, visit{object: parent.object, depth: current.depth + 1})
			}
			builder.AddEdge(model.Edge{FromUID: string(current.object.GetUID()), ToUID: parent.node.Ref.UID, Type: model.RelationOwner})
		}
	}
	if expandChildren {
		children, childWarnings := e.children(ctx, current.object, request)
		warnings = append(warnings, childWarnings...)
		for _, child := range children {
			if builder.AddNode(child.node) {
				depthsMu.Lock()
				depths[child.node.Ref.UID] = current.depth + 1
				depthsMu.Unlock()
				next = append(next, visit{object: child.object, depth: current.depth + 1})
			}
			builder.AddEdge(model.Edge{FromUID: child.node.Ref.UID, ToUID: string(current.object.GetUID()), Type: model.RelationOwner})
		}
	}
	if current.object.GetKind() == "Pod" {
		warnings = append(warnings, e.resolvePodAssociations(ctx, current.object, builder, request)...)
	} else {
		switch current.object.GetKind() {
		case "Service":
			warnings = append(warnings, e.resolveServiceTarget(ctx, current.object, builder, request)...)
		case "Ingress":
			warnings = append(warnings, e.resolveIngressTarget(ctx, current.object, builder, request)...)
		case "Node":
			warnings = append(warnings, e.resolveNodePods(ctx, current.object, builder, request)...)
		case "PersistentVolumeClaim":
			claimNode := project(current.object, "PersistentVolumeClaim", current.object.GetAPIVersion(), request.ShowSecretKeys)
			warnings = append(warnings, e.resolvePVCStorage(ctx, current.object, claimNode, builder, request)...)
			warnings = append(warnings, e.resolvePVCUsers(ctx, current.object, builder, request)...)
		case "PersistentVolume":
			warnings = append(warnings, e.resolvePVTarget(ctx, current.object, builder, request)...)
		case "ServiceAccount":
			warnings = append(warnings, e.resolveServiceAccountTarget(ctx, current.object, builder, request)...)
		case "ConfigMap", "Secret":
			warnings = append(warnings, e.resolveConfigOrSecretTarget(ctx, current.object, builder, request)...)
		}
	}
	return next, warnings
}

// runConcurrently runs each task bounded by traversalConcurrency and merges
// their warnings; used to fan out a single resource's independent association
// lookups (Node, ServiceAccount/RBAC, volumes, env refs, Services, policies)
// instead of making their Kubernetes API calls one at a time.
func (e *Engine) runConcurrently(ctx context.Context, tasks []func() []model.QueryWarning) []model.QueryWarning {
	var mu sync.Mutex
	var warnings []model.QueryWarning
	var wg sync.WaitGroup
	workers := min(traversalConcurrency, len(tasks))
	jobs := make(chan func() []model.QueryWarning)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if ctx.Err() != nil {
					return
				}
				result := task()
				mu.Lock()
				warnings = append(warnings, result...)
				mu.Unlock()
			}
		}()
	}
	for _, task := range tasks {
		select {
		case jobs <- task:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	return warnings
}

// listAll fetches every page of a List and memoizes the full result for the
// lifetime of this command. Resolvers repeatedly list the same namespace-scoped
// resource (Services, RoleBindings, NetworkPolicies, PDBs, HPAs) once per
// sibling Pod/node during traversal; without this cache a Deployment with N
// Pods triggers the same cluster-wide List N times instead of once.
func (e *Engine) listAll(ctx context.Context, resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) ([]unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := resource.String() + "\x00" + namespace + "\x00" + options.FieldSelector + "\x00" + options.LabelSelector
	e.listCacheMu.Lock()
	cached, ok := e.listCache[key]
	if ok {
		e.listCacheMu.Unlock()
		return cached, nil
	}
	if call, ok := e.listFlights[key]; ok {
		e.listCacheMu.Unlock()
		select {
		case <-call.done:
			return call.items, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &listCall{done: make(chan struct{})}
	e.listFlights[key] = call
	e.listCacheMu.Unlock()

	var items []unstructured.Unstructured
	var fetchErr error
	for {
		if err := ctx.Err(); err != nil {
			fetchErr = err
			break
		}
		list, err := e.client.List(ctx, resource, namespace, options)
		if err != nil {
			fetchErr = err
			break
		}
		items = append(items, list.Items...)
		options.Continue = list.GetContinue()
		if options.Continue == "" {
			break
		}
	}

	e.listCacheMu.Lock()
	call.items, call.err = items, fetchErr
	if fetchErr == nil {
		e.listCache[key] = items
	}
	delete(e.listFlights, key)
	close(call.done)
	e.listCacheMu.Unlock()
	return items, fetchErr
}

func (e *Engine) listMetadataAll(ctx context.Context, resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) ([]metav1.PartialObjectMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := resource.String() + "\x00" + namespace + "\x00" + options.FieldSelector + "\x00" + options.LabelSelector
	e.listCacheMu.Lock()
	if cached, ok := e.metadataCache[key]; ok {
		e.listCacheMu.Unlock()
		return cached, nil
	}
	if call, ok := e.metadataFlights[key]; ok {
		e.listCacheMu.Unlock()
		select {
		case <-call.done:
			return call.items, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &metadataListCall{done: make(chan struct{})}
	e.metadataFlights[key] = call
	e.listCacheMu.Unlock()

	var items []metav1.PartialObjectMetadata
	var fetchErr error
	for {
		if err := ctx.Err(); err != nil {
			fetchErr = err
			break
		}
		list, err := e.client.ListMetadata(ctx, resource, namespace, options)
		if err != nil {
			fetchErr = err
			break
		}
		items = append(items, list.Items...)
		options.Continue = list.GetContinue()
		if options.Continue == "" {
			break
		}
	}
	e.listCacheMu.Lock()
	call.items, call.err = items, fetchErr
	if fetchErr == nil {
		e.metadataCache[key] = items
	}
	delete(e.metadataFlights, key)
	close(call.done)
	e.listCacheMu.Unlock()
	return items, fetchErr
}

type visit struct {
	object *unstructured.Unstructured
	depth  int
}

type related struct {
	object *unstructured.Unstructured
	node   model.ResourceNode
}

func (e *Engine) parents(ctx context.Context, object *unstructured.Unstructured, lists []*metav1.APIResourceList, request Request) ([]related, []model.QueryWarning) {
	var found []related
	var warnings []model.QueryWarning
	for _, owner := range object.GetOwnerReferences() {
		if owner.UID == "" {
			warnings = append(warnings, warningFor(object, "resolve owner", fmt.Errorf("owner reference %s/%s has no UID; skipped to avoid name-only matching", owner.Kind, owner.Name)))
			continue
		}
		resource, namespaced, ok := knownOwnerResource(owner.APIVersion, owner.Kind)
		if !ok && lists != nil {
			resource, namespaced, ok = findResource(lists, owner.APIVersion, owner.Kind)
		}
		if !ok {
			warnings = append(warnings, warningFor(object, "resolve owner", fmt.Errorf("API resource for %s %s was not discovered", owner.APIVersion, owner.Kind)))
			continue
		}
		if isExcluded(request.ExcludedKinds, owner.Kind) {
			continue
		}
		namespace := ""
		if namespaced {
			namespace = object.GetNamespace()
		}
		parent, err := e.client.Get(ctx, resource, namespace, owner.Name)
		if err != nil {
			warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: owner.APIVersion, Kind: owner.Kind, Namespace: namespace, Name: owner.Name, UID: string(owner.UID)}, "get owner", err))
			continue
		}
		if string(parent.GetUID()) != string(owner.UID) {
			warnings = append(warnings, warningFor(parent, "verify owner", fmt.Errorf("UID does not match owner reference; skipped")))
			continue
		}
		found = append(found, related{object: parent, node: project(parent, owner.Kind, owner.APIVersion, request.ShowSecretKeys)})
	}
	return found, warnings
}

func (e *Engine) children(ctx context.Context, parent *unstructured.Unstructured, request Request) ([]related, []model.QueryWarning) {
	if parent.GetNamespace() == "" || parent.GetUID() == "" {
		return nil, nil
	}
	if parent.GetKind() == "ReplicaSet" {
		if replicas, found, _ := unstructured.NestedInt64(parent.Object, "spec", "replicas"); found && replicas == 0 {
			return nil, nil
		}
	}
	childKinds := map[string][]schema.GroupVersionResource{
		"Deployment":  {{Group: "apps", Version: "v1", Resource: "replicasets"}},
		"ReplicaSet":  {{Version: "v1", Resource: "pods"}},
		"StatefulSet": {{Version: "v1", Resource: "pods"}},
		"DaemonSet":   {{Version: "v1", Resource: "pods"}},
		"Job":         {{Version: "v1", Resource: "pods"}},
		"CronJob":     {{Group: "batch", Version: "v1", Resource: "jobs"}},
	}
	resources, ok := childKinds[parent.GetKind()]
	if !ok {
		return nil, nil
	}
	var warnings []model.QueryWarning
	var found []related
	type candidate struct {
		resource   schema.GroupVersionResource
		kind       string
		apiVersion string
		metadata   metav1.PartialObjectMetadata
	}
	candidates := make([]candidate, 0)
	for _, resource := range resources {
		if isExcluded(request.ExcludedKinds, resourceKind(resource)) {
			continue
		}
		labelSelector := childLabelSelector(parent)
		if labelSelector != "" {
			items, err := e.listAll(ctx, resource, parent.GetNamespace(), metav1.ListOptions{Limit: 500, LabelSelector: labelSelector})
			if err != nil {
				warnings = append(warnings, warningFor(parent, "list owned resources", err))
				continue
			}
			for i := range items {
				child := &items[i]
				if !ownedBy(child, parent) {
					continue
				}
				kind := child.GetKind()
				if kind == "" {
					kind = resourceKind(resource)
				}
				found = append(found, related{object: child, node: project(child, kind, resource.GroupVersion().String(), request.ShowSecretKeys)})
			}
			continue
		}
		items, err := e.listMetadataAll(ctx, resource, parent.GetNamespace(), metav1.ListOptions{Limit: 500})
		if err != nil {
			warnings = append(warnings, warningFor(parent, "list owned resources", err))
			continue
		}
		for i := range items {
			child := &items[i]
			if !ownedByMetadata(child, parent) {
				continue
			}
			kind := resourceKind(resource)
			if kind == "" {
				kind = child.Kind
			}
			candidates = append(candidates, candidate{resource: resource, kind: kind, apiVersion: resource.GroupVersion().String(), metadata: *child})
		}
	}

	fetched := make([]related, len(candidates))
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	workers := min(traversalConcurrency, len(candidates))
	jobs := make(chan int)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				candidate := candidates[index]
				child, err := e.client.Get(ctx, candidate.resource, parent.GetNamespace(), candidate.metadata.GetName())
				if err != nil {
					resultMu.Lock()
					warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: candidate.apiVersion, Kind: candidate.kind, Namespace: parent.GetNamespace(), Name: candidate.metadata.GetName(), UID: string(candidate.metadata.GetUID())}, "get owned resource", err))
					resultMu.Unlock()
					continue
				}
				if child.GetUID() != candidate.metadata.GetUID() {
					resultMu.Lock()
					warnings = append(warnings, warningFor(child, "verify owned resource", fmt.Errorf("UID changed after metadata lookup; skipped")))
					resultMu.Unlock()
					continue
				}
				fetched[index] = related{object: child, node: project(child, candidate.kind, candidate.apiVersion, request.ShowSecretKeys)}
			}
		}()
	}
	for index := range candidates {
		select {
		case jobs <- index:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		warnings = append(warnings, warningFor(parent, "list owned resources", ctx.Err()))
	}
	for _, child := range fetched {
		if child.object != nil {
			found = append(found, child)
		}
	}
	return found, warnings
}

func childLabelSelector(parent *unstructured.Unstructured) string {
	switch parent.GetKind() {
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job":
		selectorMap, found, _ := unstructured.NestedMap(parent.Object, "spec", "selector")
		if !found || len(selectorMap) == 0 {
			return ""
		}
		var selector metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, &selector); err != nil {
			return ""
		}
		parsed, err := metav1.LabelSelectorAsSelector(&selector)
		if err != nil || parsed.Empty() {
			return ""
		}
		return parsed.String()
	default:
		return ""
	}
}

func ownedByMetadata(child *metav1.PartialObjectMetadata, parent *unstructured.Unstructured) bool {
	for _, owner := range child.GetOwnerReferences() {
		if owner.UID == parent.GetUID() && owner.Kind == parent.GetKind() && owner.APIVersion == parent.GetAPIVersion() {
			return true
		}
	}
	return false
}

func ownedBy(child, parent *unstructured.Unstructured) bool {
	for _, owner := range child.GetOwnerReferences() {
		if owner.UID == parent.GetUID() && owner.Kind == parent.GetKind() && owner.APIVersion == parent.GetAPIVersion() {
			return true
		}
	}
	return false
}

func (e *Engine) collectEvents(ctx context.Context, nodes []model.ResourceNode, request Request) ([]model.EventSummary, []model.QueryWarning) {
	var events []model.EventSummary
	var warnings []model.QueryWarning
	seen := make(map[string]int)
	resource := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	cutoff := time.Now().Add(-request.Since)
	for _, node := range nodes {
		selectors := make([]string, 0, 2)
		if node.Ref.UID != "" {
			selectors = append(selectors, fields.OneTermEqualSelector("involvedObject.uid", node.Ref.UID).String())
		}
		identitySelectors := []fields.Selector{
			fields.OneTermEqualSelector("involvedObject.kind", node.Ref.Kind),
			fields.OneTermEqualSelector("involvedObject.name", node.Ref.Name),
		}
		if node.Ref.Namespace != "" {
			identitySelectors = append(identitySelectors, fields.OneTermEqualSelector("involvedObject.namespace", node.Ref.Namespace))
		}
		selectors = append(selectors, fields.AndSelectors(identitySelectors...).String())
		var listed []unstructured.Unstructured
		var listErr error
		for _, selector := range selectors {
			listed, listErr = e.listAll(ctx, resource, node.Ref.Namespace, metav1.ListOptions{FieldSelector: selector, Limit: 500})
			if listErr == nil {
				break
			}
		}
		if listErr != nil {
			warnings = append(warnings, warningForRef(node.Ref, "list events", listErr))
			continue
		}
		for i := range listed {
			event := &listed[i]
			summary, ok := eventSummary(event, node.Ref)
			if !ok || (!summary.LastTimestamp.IsZero() && summary.LastTimestamp.Before(cutoff)) {
				continue
			}
			identity := summary.InvolvedObject.UID
			if identity == "" {
				identity = summary.InvolvedObject.Kind + "\x00" + summary.InvolvedObject.Namespace + "\x00" + summary.InvolvedObject.Name
			}
			key := identity + "\x00" + summary.Reason + "\x00" + summary.Message
			if index, exists := seen[key]; exists {
				if summary.Count > events[index].Count {
					events[index].Count = summary.Count
				}
				if summary.LastTimestamp.After(events[index].LastTimestamp) {
					events[index].LastTimestamp = summary.LastTimestamp
				}
				if events[index].FirstTimestamp.IsZero() || (!summary.FirstTimestamp.IsZero() && summary.FirstTimestamp.Before(events[index].FirstTimestamp)) {
					events[index].FirstTimestamp = summary.FirstTimestamp
				}
				continue
			}
			seen[key] = len(events)
			events = append(events, summary)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].LastTimestamp.After(events[j].LastTimestamp) })
	if len(events) > request.MaxEvents {
		events = events[:request.MaxEvents]
	}
	return events, warnings
}

func eventSummary(object *unstructured.Unstructured, fallback model.ResourceRef) (model.EventSummary, bool) {
	// Extract fields directly rather than via a single typed Event decode: a
	// malformed/edge-case eventTime (metav1.MicroTime requires an exact 6-digit
	// fractional second, which not every producer emits) must not discard an
	// otherwise-valid event's reason/message/count.
	involved, _, _ := unstructured.NestedMap(object.Object, "involvedObject")
	ref := model.ResourceRef{
		APIVersion: stringValue(involved["apiVersion"]), Kind: stringValue(involved["kind"]),
		Namespace: stringValue(involved["namespace"]), Name: stringValue(involved["name"]), UID: stringValue(involved["uid"]),
	}
	if ref.UID == "" && (ref.Kind != fallback.Kind || ref.Namespace != fallback.Namespace || ref.Name != fallback.Name) {
		return model.EventSummary{}, false
	}
	if ref.UID != "" && fallback.UID != "" && ref.UID != fallback.UID {
		return model.EventSummary{}, false
	}

	firstTimestamp := parseEventTimestamp(object.Object, "firstTimestamp")
	if firstTimestamp.IsZero() {
		firstTimestamp = parseEventTimestamp(object.Object, "eventTime")
	}
	lastTimestamp := parseEventTimestamp(object.Object, "lastTimestamp")
	if lastTimestamp.IsZero() {
		if observed, _, _ := unstructured.NestedString(object.Object, "series", "lastObservedTime"); observed != "" {
			lastTimestamp = parseTimestamp(observed)
		}
	}
	if lastTimestamp.IsZero() {
		lastTimestamp = parseEventTimestamp(object.Object, "eventTime")
	}
	if lastTimestamp.IsZero() {
		if created, found, _ := unstructured.NestedString(object.Object, "metadata", "creationTimestamp"); found {
			lastTimestamp = parseTimestamp(created)
		}
	}

	count := int32(1)
	if value, found, _ := unstructured.NestedInt64(object.Object, "count"); found && value != 0 {
		count = int32(value)
	}
	return model.EventSummary{
		UID: string(object.GetUID()), Namespace: object.GetNamespace(), InvolvedObject: ref, NodeUID: fallback.UID,
		Type: stringValue(object.Object["type"]), Reason: stringValue(object.Object["reason"]), Message: stringValue(object.Object["message"]), Count: count,
		FirstTimestamp: firstTimestamp, LastTimestamp: lastTimestamp,
	}, true
}

func parseEventTimestamp(object map[string]any, field string) time.Time {
	value, found, _ := unstructured.NestedString(object, field)
	if !found || value == "" {
		return time.Time{}
	}
	return parseTimestamp(value)
}

// parseTimestamp tolerates the timestamp layouts Kubernetes Events actually use:
// metav1.Time (RFC3339) and metav1.MicroTime (RFC3339 with exactly 6 fractional
// digits), plus plain RFC3339Nano for test fixtures and other producers.
func parseTimestamp(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000000Z07:00", time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func findResource(lists []*metav1.APIResourceList, apiVersion, kind string) (schema.GroupVersionResource, bool, bool) {
	for _, list := range lists {
		if list.GroupVersion != apiVersion {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		for _, resource := range list.APIResources {
			if resource.Kind == kind && !strings.Contains(resource.Name, "/") {
				return gv.WithResource(resource.Name), resource.Namespaced, true
			}
		}
	}
	return schema.GroupVersionResource{}, false, false
}

func knownOwnerResource(apiVersion, kind string) (schema.GroupVersionResource, bool, bool) {
	resources := map[string]map[string]schema.GroupVersionResource{
		"v1": {
			"Pod": {Version: "v1", Resource: "pods"}, "Service": {Version: "v1", Resource: "services"},
			"Node": {Version: "v1", Resource: "nodes"}, "Namespace": {Version: "v1", Resource: "namespaces"},
			"PersistentVolumeClaim": {Version: "v1", Resource: "persistentvolumeclaims"},
			"PersistentVolume":      {Version: "v1", Resource: "persistentvolumes"},
			"ConfigMap":             {Version: "v1", Resource: "configmaps"}, "Secret": {Version: "v1", Resource: "secrets"},
			"ServiceAccount": {Version: "v1", Resource: "serviceaccounts"}, "Endpoints": {Version: "v1", Resource: "endpoints"},
		},
		"apps/v1": {
			"Deployment":  {Group: "apps", Version: "v1", Resource: "deployments"},
			"ReplicaSet":  {Group: "apps", Version: "v1", Resource: "replicasets"},
			"StatefulSet": {Group: "apps", Version: "v1", Resource: "statefulsets"},
			"DaemonSet":   {Group: "apps", Version: "v1", Resource: "daemonsets"},
		},
		"batch/v1": {
			"Job":     {Group: "batch", Version: "v1", Resource: "jobs"},
			"CronJob": {Group: "batch", Version: "v1", Resource: "cronjobs"},
		},
		"rbac.authorization.k8s.io/v1": {
			"Role":               {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
			"ClusterRole":        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
			"RoleBinding":        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
			"ClusterRoleBinding": {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
		},
	}
	resource, ok := resources[apiVersion][kind]
	if !ok {
		return schema.GroupVersionResource{}, false, false
	}
	clusterScoped := kind == "Node" || kind == "Namespace" || kind == "PersistentVolume" || kind == "ClusterRole"
	return resource, !clusterScoped, true
}

func resourceKind(resource schema.GroupVersionResource) string {
	known := map[string]string{"pods": "Pod", "replicasets": "ReplicaSet", "jobs": "Job"}
	return known[resource.Resource]
}

func isExcluded(excluded map[string]bool, kind string) bool {
	return excluded[kind] || excluded[strings.ToLower(kind)]
}

func warningFor(object *unstructured.Unstructured, operation string, err error) model.QueryWarning {
	ref := model.ResourceRef{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: string(object.GetUID())}
	return warningForRef(ref, operation, err)
}

func warningForRef(ref model.ResourceRef, operation string, err error) model.QueryWarning {
	message := err.Error()
	if apierrors.IsForbidden(err) {
		message = "forbidden: " + message
	}
	return model.QueryWarning{Resource: ref, Operation: operation, Message: message}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
