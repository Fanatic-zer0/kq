package view

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Fanatic-zer0/kq/internal/k8s"
	"github.com/Fanatic-zer0/kq/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ListRequest struct {
	Resource      schema.GroupVersionResource
	Kind          string
	Namespace     string
	AllNamespaces bool
	Wide          bool
	IncludeEvents bool
	Since         time.Duration
}

// Row is a kind-agnostic list row; Columns holds the ordered default values and
// WideColumns holds the additional values shown only in wide mode.
type Row struct {
	Ref         model.ResourceRef
	Columns     []string
	WideColumns []string
}

type ListResult struct {
	Headers     []string
	WideHeaders []string
	Rows        []Row
	Warnings    []model.QueryWarning
}

func ListPods(ctx context.Context, client k8s.Reader, request ListRequest) (*ListResult, error) {
	namespace := request.Namespace
	if request.AllNamespaces {
		namespace = ""
	}
	items, err := listAll(ctx, client, request.Resource, namespace, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("list Pods: %w", err)
	}
	owners, warnings := bulkOwnerChains(ctx, client, items, namespace)
	eventCounts := map[string]int{}
	if request.IncludeEvents {
		counts, eventWarnings := bulkWarningEventCounts(ctx, client, namespace, request.Since)
		eventCounts = counts
		warnings = append(warnings, eventWarnings...)
	}

	result := &ListResult{
		Headers:     []string{"NAMESPACE", "POD", "STATUS", "CONTAINERS", "OWNED BY", "RESTARTS", "NODE IP", "AGE"},
		WideHeaders: []string{"NODE", "POD IP", "QOS", "CONFIG", "SECRET", "STORAGE", "SA", "EVENTS"},
	}
	for i := range items {
		pod := &items[i]
		var typed podLike
		if !decodePod(pod, &typed) {
			continue
		}
		owner := owners[string(pod.GetUID())]
		row := Row{
			Ref: model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: pod.GetNamespace(), Name: pod.GetName(), UID: string(pod.GetUID())},
			Columns: []string{
				pod.GetNamespace(), pod.GetName(), podListStatus(typed), fmt.Sprintf("%d/%d", typed.readyContainers, typed.totalContainers),
				orDash(owner), fmt.Sprintf("%d", typed.restarts), orDash(typed.hostIP), Age(pod.GetCreationTimestamp().Time),
			},
		}
		if request.Wide {
			configCount, secretCount, storageCount := countPodReferences(typed)
			row.WideColumns = []string{
				orDash(typed.nodeName), orDash(typed.podIP), orDash(typed.qosClass),
				fmt.Sprintf("%d", configCount), fmt.Sprintf("%d", secretCount), fmt.Sprintf("%d", storageCount),
				orDash(typed.serviceAccountName), fmt.Sprintf("%d", eventCounts[string(pod.GetUID())]),
			}
		}
		result.Rows = append(result.Rows, row)
	}
	sort.Slice(result.Rows, func(i, j int) bool { return result.Rows[i].Ref.Name < result.Rows[j].Ref.Name })
	result.Warnings = warnings
	return result, nil
}

func listAll(ctx context.Context, client k8s.Reader, resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	for {
		list, err := client.List(ctx, resource, namespace, options)
		if err != nil {
			return nil, err
		}
		items = append(items, list.Items...)
		options.Continue = list.GetContinue()
		if options.Continue == "" {
			return items, nil
		}
	}
}

// bulkOwnerChains resolves each Pod's display owner (Deployment/StatefulSet/DaemonSet/Job/CronJob)
// using at most two bulk List calls (ReplicaSets, Jobs) regardless of Pod count.
func bulkOwnerChains(ctx context.Context, client k8s.Reader, pods []unstructured.Unstructured, namespace string) (map[string]string, []model.QueryWarning) {
	owners := make(map[string]string, len(pods))
	var warnings []model.QueryWarning

	var replicaSets, jobs []metav1.PartialObjectMetadata
	var replicaSetErr, jobErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		replicaSets, replicaSetErr = listAllMetadata(ctx, client, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, namespace, metav1.ListOptions{Limit: 500})
	}()
	go func() {
		defer wg.Done()
		jobs, jobErr = listAllMetadata(ctx, client, schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, namespace, metav1.ListOptions{Limit: 500})
	}()
	wg.Wait()
	if replicaSetErr != nil {
		warnings = append(warnings, model.QueryWarning{Operation: "list ReplicaSets", Message: replicaSetErr.Error()})
	}
	if jobErr != nil {
		warnings = append(warnings, model.QueryWarning{Operation: "list Jobs", Message: jobErr.Error()})
	}
	replicaSetByUID := indexMetadataByUID(replicaSets)
	jobByUID := indexMetadataByUID(jobs)

	for i := range pods {
		pod := &pods[i]
		owner, ok := firstOwnerRef(pod)
		if !ok {
			continue
		}
		switch owner.Kind {
		case "ReplicaSet":
			if rs, found := replicaSetByUID[string(owner.UID)]; found {
				if grandOwner, ok := firstMetadataOwnerRef(rs); ok && grandOwner.Kind == "Deployment" {
					owners[string(pod.GetUID())] = "Deployment/" + grandOwner.Name
					continue
				}
			}
			owners[string(pod.GetUID())] = "ReplicaSet/" + owner.Name
		case "Job":
			if job, found := jobByUID[string(owner.UID)]; found {
				if grandOwner, ok := firstMetadataOwnerRef(job); ok && grandOwner.Kind == "CronJob" {
					owners[string(pod.GetUID())] = "CronJob/" + grandOwner.Name
					continue
				}
			}
			owners[string(pod.GetUID())] = "Job/" + owner.Name
		default:
			owners[string(pod.GetUID())] = owner.Kind + "/" + owner.Name
		}
	}
	return owners, warnings
}

func indexMetadataByUID(items []metav1.PartialObjectMetadata) map[string]*metav1.PartialObjectMetadata {
	index := make(map[string]*metav1.PartialObjectMetadata, len(items))
	for i := range items {
		index[string(items[i].GetUID())] = &items[i]
	}
	return index
}

func listAllMetadata(ctx context.Context, client k8s.Reader, resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) ([]metav1.PartialObjectMetadata, error) {
	var items []metav1.PartialObjectMetadata
	for {
		list, err := client.ListMetadata(ctx, resource, namespace, options)
		if err != nil {
			return nil, err
		}
		items = append(items, list.Items...)
		options.Continue = list.GetContinue()
		if options.Continue == "" {
			return items, nil
		}
	}
}

type ownerRef struct {
	Kind, Name string
	UID        string
}

func firstOwnerRef(object *unstructured.Unstructured) (ownerRef, bool) {
	refs := object.GetOwnerReferences()
	if len(refs) == 0 {
		return ownerRef{}, false
	}
	return ownerRef{Kind: refs[0].Kind, Name: refs[0].Name, UID: string(refs[0].UID)}, true
}

func firstMetadataOwnerRef(object *metav1.PartialObjectMetadata) (ownerRef, bool) {
	refs := object.GetOwnerReferences()
	if len(refs) == 0 {
		return ownerRef{}, false
	}
	return ownerRef{Kind: refs[0].Kind, Name: refs[0].Name, UID: string(refs[0].UID)}, true
}

// bulkWarningEventCounts counts Warning events within the since window per involved-object UID,
// using a single namespace-scoped List regardless of how many Pods are listed.
func bulkWarningEventCounts(ctx context.Context, client k8s.Reader, namespace string, since time.Duration) (map[string]int, []model.QueryWarning) {
	counts := make(map[string]int)
	selector := fields.OneTermEqualSelector("type", "Warning").String()
	events, err := listAll(ctx, client, schema.GroupVersionResource{Version: "v1", Resource: "events"}, namespace, metav1.ListOptions{FieldSelector: selector, Limit: 500})
	if err != nil {
		return counts, []model.QueryWarning{{Operation: "list Events", Message: err.Error()}}
	}
	cutoff := time.Now().Add(-since)
	for i := range events {
		if since > 0 && eventLastSeen(&events[i]).Before(cutoff) {
			continue
		}
		uid, _, _ := unstructured.NestedString(events[i].Object, "involvedObject", "uid")
		if uid != "" {
			counts[uid]++
		}
	}
	return counts, nil
}

func eventLastSeen(event *unstructured.Unstructured) time.Time {
	for _, field := range []string{"lastTimestamp", "eventTime"} {
		if value, found, _ := unstructured.NestedString(event.Object, field); found && value != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}
