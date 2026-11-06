package view

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Fanatic-zer0/kq/internal/k8s"
	"github.com/Fanatic-zer0/kq/internal/model"
	"github.com/Fanatic-zer0/kq/internal/query"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ListGeneric lists any supported resource kind and renders kind-appropriate columns,
// reusing the same typed Details used by detail views.
func ListGeneric(ctx context.Context, client k8s.Reader, request ListRequest) (*ListResult, error) {
	namespace := request.Namespace
	if request.AllNamespaces {
		namespace = ""
	}
	items, err := listAll(ctx, client, request.Resource, namespace, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("list %ss: %w", request.Kind, err)
	}
	headers, columnFunc := genericColumns(request.Kind)
	result := &ListResult{Headers: headers}
	var podUsers map[string][]string
	var podUsersErr error
	if request.Kind == "PersistentVolumeClaim" || request.Kind == "PersistentVolume" {
		podNamespace := namespace
		if request.Kind == "PersistentVolume" {
			podNamespace = ""
		}
		podUsers, podUsersErr = listPVCUsers(ctx, client, podNamespace, request.AllNamespaces || request.Kind == "PersistentVolume")
		if podUsersErr != nil {
			result.Warnings = append(result.Warnings, model.QueryWarning{Operation: "list Pods using PVCs", Message: podUsersErr.Error()})
		}
	}
	for i := range items {
		node := query.Project(&items[i], request.Kind, request.Resource.GroupVersion().String(), false)
		columns := columnFunc(node)
		if request.Kind == "PersistentVolumeClaim" || request.Kind == "PersistentVolume" {
			usedBy := "unavailable"
			if podUsersErr == nil {
				usedBy = "-"
				claimNamespace, claimName := node.Ref.Namespace, node.Ref.Name
				if details, ok := detailsOf[*model.PVDetails](node); ok && details != nil {
					claimNamespace, claimName = details.ClaimNamespace, details.ClaimName
				}
				usedBy = joinOrDash(podUsers[claimNamespace+"/"+claimName])
			}
			columns[len(columns)-1] = usedBy
		}
		result.Rows = append(result.Rows, Row{Ref: node.Ref, Columns: columns})
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].Ref.Namespace != result.Rows[j].Ref.Namespace {
			return result.Rows[i].Ref.Namespace < result.Rows[j].Ref.Namespace
		}
		return result.Rows[i].Ref.Name < result.Rows[j].Ref.Name
	})
	return result, nil
}

func listPVCUsers(ctx context.Context, client k8s.Reader, namespace string, qualifyPodNames bool) (map[string][]string, error) {
	pods, err := listAll(ctx, client, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, namespace, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("list Pods using PVCs: %w", err)
	}
	users := make(map[string]map[string]struct{})
	for i := range pods {
		pod := &pods[i]
		volumes, _, _ := unstructured.NestedSlice(pod.Object, "spec", "volumes")
		for _, value := range volumes {
			volume, ok := value.(map[string]any)
			if !ok {
				continue
			}
			claimName, _, _ := unstructured.NestedString(volume, "persistentVolumeClaim", "claimName")
			if claimName == "" {
				continue
			}
			claimKey := pod.GetNamespace() + "/" + claimName
			if users[claimKey] == nil {
				users[claimKey] = make(map[string]struct{})
			}
			podName := pod.GetName()
			if qualifyPodNames {
				podName = pod.GetNamespace() + "/" + podName
			}
			users[claimKey][podName] = struct{}{}
		}
	}
	result := make(map[string][]string, len(users))
	for claim, names := range users {
		for name := range names {
			result[claim] = append(result[claim], name)
		}
		sort.Strings(result[claim])
	}
	return result, nil
}

type columnFunc func(model.ResourceNode) []string

func genericColumns(kind string) ([]string, columnFunc) {
	switch kind {
	case "Deployment":
		return []string{"NAMESPACE", "NAME", "READY", "UP-TO-DATE", "AVAILABLE", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.DeploymentDetails](node)
			if d == nil {
				return emptyRow(node, 4)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, fmt.Sprintf("%d/%d", d.ReadyReplicas, d.Replicas), fmt.Sprintf("%d", d.UpdatedReplicas), fmt.Sprintf("%d", d.AvailableReplicas), Age(d.CreationTimestamp)}
		}
	case "StatefulSet":
		return []string{"NAMESPACE", "NAME", "READY", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.StatefulSetDetails](node)
			if d == nil {
				return emptyRow(node, 2)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, fmt.Sprintf("%d/%d", d.ReadyReplicas, d.Replicas), Age(d.CreationTimestamp)}
		}
	case "DaemonSet":
		return []string{"NAMESPACE", "NAME", "DESIRED", "READY", "AVAILABLE", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.DaemonSetDetails](node)
			if d == nil {
				return emptyRow(node, 4)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, fmt.Sprintf("%d", d.DesiredNumberScheduled), fmt.Sprintf("%d", d.NumberReady), fmt.Sprintf("%d", d.NumberAvailable), Age(d.CreationTimestamp)}
		}
	case "ReplicaSet":
		return []string{"NAMESPACE", "NAME", "DESIRED", "READY", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.ReplicaSetDetails](node)
			if d == nil {
				return emptyRow(node, 3)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, fmt.Sprintf("%d", d.Desired), fmt.Sprintf("%d", d.Ready), Age(d.CreationTimestamp)}
		}
	case "Job":
		return []string{"NAMESPACE", "NAME", "ACTIVE", "SUCCEEDED", "FAILED", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.JobDetails](node)
			if d == nil {
				return emptyRow(node, 4)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, fmt.Sprintf("%d", d.Active), fmt.Sprintf("%d", d.Succeeded), fmt.Sprintf("%d", d.Failed), Age(d.CreationTimestamp)}
		}
	case "CronJob":
		return []string{"NAMESPACE", "NAME", "SCHEDULE", "SUSPEND", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.CronJobDetails](node)
			if d == nil {
				return emptyRow(node, 3)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, orDash(d.Schedule), fmt.Sprintf("%t", d.Suspend), Age(d.CreationTimestamp)}
		}
	case "Service":
		return []string{"NAMESPACE", "NAME", "TYPE", "CLUSTER-IP", "PORTS", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.ServiceDetails](node)
			if d == nil {
				return emptyRow(node, 4)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, orDash(d.Type), orDash(d.ClusterIP), joinOrDash(d.Ports), Age(d.CreationTimestamp)}
		}
	case "Ingress":
		return []string{"NAMESPACE", "NAME", "HOSTS", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.IngressDetails](node)
			if d == nil {
				return emptyRow(node, 2)
			}
			hosts := make([]string, 0, len(d.Rules))
			for _, rule := range d.Rules {
				hosts = append(hosts, orDash(rule.Host))
			}
			return []string{node.Ref.Namespace, node.Ref.Name, joinOrDash(hosts), Age(d.CreationTimestamp)}
		}
	case "PersistentVolumeClaim":
		return []string{"NAMESPACE", "NAME", "STATUS", "REQUESTED", "CAPACITY", "STORAGECLASS", "AGE", "USED BY"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.PVCDetails](node)
			if d == nil {
				return emptyRow(node, 6)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, orDash(d.Phase), orDash(d.RequestedCapacity), orDash(d.Capacity), orDash(d.StorageClassName), Age(d.CreationTimestamp), "-"}
		}
	case "PersistentVolume":
		return []string{"NAME", "STATUS", "CAPACITY", "ACCESS MODES", "VOLUME MODE", "RECLAIM POLICY", "CLAIM", "STORAGECLASS", "AGE", "USED BY"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.PVDetails](node)
			if d == nil {
				return []string{node.Ref.Name, "-", "-", "-", "-", "-", "-", "-", "-", "-"}
			}
			claim := d.ClaimName
			if d.ClaimNamespace != "" {
				claim = d.ClaimNamespace + "/" + claim
			}
			return []string{node.Ref.Name, orDash(d.Phase), orDash(d.Capacity), joinOrDash(d.AccessModes), orDash(d.VolumeMode), orDash(d.ReclaimPolicy), orDash(claim), orDash(d.StorageClassName), Age(d.CreationTimestamp), "-"}
		}
	case "ConfigMap":
		return []string{"NAMESPACE", "NAME", "KEYS", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.ConfigMapDetails](node)
			if d == nil {
				return emptyRow(node, 2)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, fmt.Sprintf("%d", len(d.Keys)), Age(d.CreationTimestamp)}
		}
	case "Secret":
		return []string{"NAMESPACE", "NAME", "TYPE", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.SecretDetails](node)
			if d == nil {
				return emptyRow(node, 2)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, orDash(d.Type), Age(d.CreationTimestamp)}
		}
	case "ServiceAccount":
		return []string{"NAMESPACE", "NAME", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.ServiceAccountDetails](node)
			if d == nil {
				return emptyRow(node, 1)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, Age(d.CreationTimestamp)}
		}
	case "Node":
		return []string{"NAME", "INTERNAL-IP", "KUBELET VERSION", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.NodeDetails](node)
			if d == nil {
				return emptyRow(node, 3)
			}
			return []string{node.Ref.Name, orDash(d.InternalIP), orDash(d.KubeletVersion), Age(d.CreationTimestamp)}
		}
	case "CustomResourceDefinition":
		return []string{"NAME", "GROUP", "KIND", "SCOPE", "VERSIONS", "ESTABLISHED", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.CRDDetails](node)
			if d == nil {
				return []string{node.Ref.Name, "-", "-", "-", "-", "Unknown", "-"}
			}
			return []string{node.Ref.Name, orDash(d.Group), orDash(d.Kind), orDash(d.Scope), crdVersionSummary(d.Versions), crdEstablishedStatus(d), Age(d.CreationTimestamp)}
		}
	default:
		return []string{"NAMESPACE", "NAME", "API VERSION", "AGE"}, func(node model.ResourceNode) []string {
			d, _ := detailsOf[*model.CustomResourceDetails](node)
			age := "-"
			if d != nil {
				age = Age(d.CreationTimestamp)
			}
			return []string{node.Ref.Namespace, node.Ref.Name, node.Ref.APIVersion, age}
		}
	}
}

func crdVersionSummary(versions []model.CRDVersion) string {
	formatted := make([]string, 0, len(versions))
	for _, version := range versions {
		states := make([]string, 0, 2)
		if version.Served {
			states = append(states, "served")
		}
		if version.Storage {
			states = append(states, "storage")
		}
		formatted = append(formatted, version.Name+" ("+strings.Join(states, ", ")+")")
	}
	return joinOrDash(formatted)
}

func crdEstablishedStatus(details *model.CRDDetails) string {
	for _, condition := range details.Conditions {
		if condition.Type == "Established" {
			return condition.Status
		}
	}
	return "Unknown"
}

func emptyRow(node model.ResourceNode, extraDashes int) []string {
	row := []string{node.Ref.Namespace, node.Ref.Name}
	for i := 0; i < extraDashes; i++ {
		row = append(row, "-")
	}
	return row
}

func ListEvents(ctx context.Context, client k8s.Reader, namespace string, allNamespaces bool) (*ListResult, error) {
	scope := namespace
	if allNamespaces {
		scope = ""
	}
	items, err := listAll(ctx, client, schema.GroupVersionResource{Version: "v1", Resource: "events"}, scope, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("list Events: %w", err)
	}
	result := &ListResult{Headers: []string{"NAMESPACE", "LAST SEEN", "TYPE", "REASON", "OBJECT", "MESSAGE"}}
	for i := range items {
		event := &items[i]
		involvedKind, _, _ := unstructured.NestedString(event.Object, "involvedObject", "kind")
		involvedName, _, _ := unstructured.NestedString(event.Object, "involvedObject", "name")
		eventType, _, _ := unstructured.NestedString(event.Object, "type")
		reason, _, _ := unstructured.NestedString(event.Object, "reason")
		message, _, _ := unstructured.NestedString(event.Object, "message")
		result.Rows = append(result.Rows, Row{
			Ref:     model.ResourceRef{APIVersion: "v1", Kind: "Event", Namespace: event.GetNamespace(), Name: event.GetName(), UID: string(event.GetUID())},
			Columns: []string{event.GetNamespace(), Age(eventLastSeen(event)), eventType, reason, involvedKind + "/" + involvedName, message},
		})
	}
	sort.Slice(result.Rows, func(i, j int) bool { return result.Rows[i].Columns[1] < result.Rows[j].Columns[1] })
	return result, nil
}
