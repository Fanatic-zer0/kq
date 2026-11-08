package view

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/Fanatic-zer0/kq/internal/model"
)

func renderDeploymentDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "Deployment", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.DeploymentDetails](target)
	if details == nil {
		return
	}
	field(w, "Replicas", fmt.Sprintf("%d/%d available", details.AvailableReplicas, details.Replicas))
	field(w, "Ready", fmt.Sprintf("%d/%d", details.ReadyReplicas, details.Replicas))
	field(w, "Updated", fmt.Sprintf("%d", details.UpdatedReplicas))
	field(w, "Generation", fmt.Sprintf("%d/%d observed", details.ObservedGeneration, details.Generation))
	field(w, "Age", Age(details.CreationTimestamp))
	field(w, "Selector", orDash(details.Selector))
	field(w, "Strategy", orDash(details.Strategy))
	if len(details.Conditions) > 0 {
		section(w, "CONDITIONS")
		table := NewTable("TYPE", "STATUS", "REASON")
		for _, condition := range details.Conditions {
			table.Row(condition.Type, condition.Status, orDash(condition.Reason))
		}
		table.Flush(w)
	}

	replicaSets := reverseOwnerLookup(result.Graph, target.Ref.UID, "ReplicaSet")
	if len(replicaSets) > 0 {
		section(w, "REPLICASETS")
		table := NewTable("NAME", "DESIRED", "READY", "AGE", "STATUS")
		for _, rs := range replicaSets {
			rsDetails, _ := detailsOf[*model.ReplicaSetDetails](rs)
			status := "Old"
			if rsDetails != nil && rsDetails.Desired > 0 {
				status = "Active"
			}
			desired, ready, age := int32(0), int32(0), "-"
			if rsDetails != nil {
				desired, ready, age = rsDetails.Desired, rsDetails.Ready, Age(rsDetails.CreationTimestamp)
			}
			table.Row(rs.Ref.Name, fmt.Sprintf("%d", desired), fmt.Sprintf("%d", ready), age, status)
		}
		table.Flush(w)
		renderPodsForOwners(w, result, replicaSets)
	}

	renderServicesSummary(w, result, target)
	renderHPAPDBSummary(w, result, target)
}

func renderStatefulSetDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "StatefulSet", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.StatefulSetDetails](target)
	if details == nil {
		return
	}
	field(w, "Replicas", fmt.Sprintf("%d/%d", details.ReadyReplicas, details.Replicas))
	field(w, "Service", orDash(details.ServiceName))
	field(w, "Update Strategy", orDash(details.UpdateStrategy))
	field(w, "Age", Age(details.CreationTimestamp))

	pods := edgeSources(result.Graph, target.Ref.UID, model.RelationOwner)
	if len(pods) > 0 {
		section(w, "PODS / STORAGE")
		table := NewTable("POD", "STATUS", "PVC", "PV", "CAPACITY", "STATUS")
		for _, pod := range pods {
			podDetails, _ := detailsOf[*model.PodDetails](pod)
			status := "-"
			if podDetails != nil {
				status = PodPhase(podDetails)
			}
			claims := edgeTargets(result.Graph, pod.Ref.UID, model.RelationVolume)
			if len(claims) == 0 {
				table.Row(pod.Ref.Name, status, "-", "-", "-", "-")
				continue
			}
			for _, claim := range claims {
				pv := firstOrNil(edgeTargets(result.Graph, claim.Ref.UID, model.RelationVolume))
				pvName, capacity, pvStatus := "-", "-", "-"
				if pv != nil {
					pvName = pv.Ref.Name
					if pvDetails, ok := detailsOf[*model.PVDetails](*pv); ok && pvDetails != nil {
						capacity, pvStatus = orDash(pvDetails.Capacity), pvDetails.Phase
					}
				}
				table.Row(pod.Ref.Name, status, claim.Ref.Name, pvName, capacity, pvStatus)
			}
		}
		table.Flush(w)
	}
	renderServicesSummary(w, result, target)
	renderHPAPDBSummary(w, result, target)
}

func renderServiceDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "Service", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.ServiceDetails](target)
	if details == nil {
		return
	}
	field(w, "Type", orDash(details.Type))
	field(w, "Selector", formatSelector(details.Selector))
	field(w, "Ports", joinOrDash(details.Ports))

	pods := edgeTargets(result.Graph, target.Ref.UID, model.RelationSelector)
	if len(pods) > 0 {
		section(w, "BACKEND PODS")
		table := NewTable("NAME", "STATUS", "READY", "IP")
		for _, pod := range pods {
			podDetails, _ := detailsOf[*model.PodDetails](pod)
			status, ready, ip := "-", "-", "-"
			if podDetails != nil {
				status, ready, ip = PodPhase(podDetails), podDetails.Ready, orDash(podDetails.PodIP)
			}
			table.Row(pod.Ref.Name, status, ready, ip)
		}
		table.Flush(w)
	}

	slices := edgeTargets(result.Graph, target.Ref.UID, model.RelationServiceBackend)
	if len(slices) > 0 {
		section(w, "ENDPOINTSLICES")
		table := NewTable("NAME", "READY", "NOT READY")
		for _, slice := range slices {
			if sliceDetails, ok := detailsOf[*model.EndpointSliceDetails](slice); ok && sliceDetails != nil {
				table.Row(slice.Ref.Name, fmt.Sprintf("%d", sliceDetails.Ready), fmt.Sprintf("%d", sliceDetails.NotReady))
			}
		}
		table.Flush(w)
	}

	if ingresses := edgeSources(result.Graph, target.Ref.UID, model.RelationIngress); len(ingresses) > 0 {
		section(w, "INGRESS")
		for _, ingress := range ingresses {
			fmt.Fprintf(w, "%s → %s\n", ingress.Ref.Name, target.Ref.Name)
		}
	}
}

func renderIngressDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "Ingress", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.IngressDetails](target)
	if details == nil {
		return
	}
	field(w, "Ingress Class", orDash(details.IngressClassName))
	field(w, "Age", Age(details.CreationTimestamp))
	field(w, "Load Balancer", joinOrDash(details.LoadBalancerAddresses))
	if len(details.Annotations) > 0 {
		section(w, "ANNOTATIONS")
		keys := make([]string, 0, len(details.Annotations))
		for key := range details.Annotations {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		table := NewTable("KEY", "VALUE")
		for _, key := range keys {
			table.Row(key, details.Annotations[key])
		}
		table.Flush(w)
	}

	rules := make([]model.IngressRule, 0, len(details.Rules)+1)
	if details.DefaultBackend != nil {
		rules = append(rules, *details.DefaultBackend)
	}
	rules = append(rules, details.Rules...)
	if len(rules) > 0 {
		section(w, "RULES")
		table := NewTable("HOST", "PATH", "PATH TYPE", "SERVICE", "PORT")
		for _, rule := range rules {
			table.Row(orDash(rule.Host), orDash(rule.Path), orDash(rule.PathType), orDash(rule.ServiceName), orDash(rule.ServicePort))
		}
		table.Flush(w)
	}
	if len(details.TLS) > 0 {
		section(w, "TLS")
		table := NewTable("SECRET", "HOSTS")
		for _, tls := range details.TLS {
			table.Row(orDash(tls.SecretName), joinOrDash(tls.Hosts))
		}
		table.Flush(w)
	}

	services := edgeTargets(result.Graph, target.Ref.UID, model.RelationIngress)
	if len(services) == 0 {
		return
	}
	section(w, "RELATED RESOURCES")
	table := NewTable("SERVICE", "TYPE", "PORTS", "ENDPOINTS")
	for _, service := range services {
		serviceDetails, _ := detailsOf[*model.ServiceDetails](service)
		serviceType, ports := "-", "-"
		if serviceDetails != nil {
			serviceType, ports = orDash(serviceDetails.Type), joinOrDash(serviceDetails.Ports)
		}
		endpointCount := 0
		for _, endpoint := range edgeTargets(result.Graph, service.Ref.UID, model.RelationServiceBackend) {
			if details, ok := detailsOf[*model.EndpointSliceDetails](endpoint); ok && details != nil {
				endpointCount += details.Ready
			}
		}
		table.Row(service.Ref.Name, serviceType, ports, fmt.Sprintf("%d ready", endpointCount))
	}
	table.Flush(w)
	for _, service := range services {
		pods := edgeTargets(result.Graph, service.Ref.UID, model.RelationSelector)
		if len(pods) == 0 {
			continue
		}
		fmt.Fprintf(w, "\nPods selected by Service/%s\n", service.Ref.Name)
		podTable := NewTable("NAME", "STATUS", "READY", "IP")
		for _, pod := range pods {
			podDetails, _ := detailsOf[*model.PodDetails](pod)
			status, ready, ip := "-", "-", "-"
			if podDetails != nil {
				status, ready, ip = PodPhase(podDetails), podDetails.Ready, orDash(podDetails.PodIP)
			}
			podTable.Row(pod.Ref.Name, status, ready, ip)
		}
		podTable.Flush(w)
	}
}

func renderCRDDetail(w io.Writer, target model.ResourceNode) {
	header(w, "CustomResourceDefinition", target.Ref.Name)
	details, _ := detailsOf[*model.CRDDetails](target)
	if details == nil {
		return
	}
	field(w, "Group", orDash(details.Group))
	field(w, "Kind", orDash(details.Kind))
	field(w, "Plural", orDash(details.Plural))
	field(w, "Singular", orDash(details.Singular))
	field(w, "Scope", orDash(details.Scope))
	field(w, "Age", Age(details.CreationTimestamp))
	field(w, "Established", crdEstablishedStatus(details))

	if len(details.Versions) > 0 {
		section(w, "VERSIONS")
		table := NewTable("NAME", "SERVED", "STORAGE")
		for _, version := range details.Versions {
			table.Row(version.Name, fmt.Sprintf("%t", version.Served), fmt.Sprintf("%t", version.Storage))
		}
		table.Flush(w)
	}
	if len(details.Conditions) > 0 {
		section(w, "CONDITIONS")
		table := NewTable("TYPE", "STATUS", "REASON", "MESSAGE")
		for _, condition := range details.Conditions {
			table.Row(condition.Type, condition.Status, orDash(condition.Reason), orDash(condition.Message))
		}
		table.Flush(w)
	}
}

func renderPVCDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "PVC", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.PVCDetails](target)
	if details == nil {
		return
	}
	statusField(w, "Status", orDash(details.Phase))
	field(w, "Requested", orDash(details.RequestedCapacity))
	field(w, "Capacity", orDash(details.Capacity))
	field(w, "StorageClass", orDash(details.StorageClassName))
	field(w, "Access Modes", joinOrDash(details.AccessModes))
	field(w, "Volume Mode", orDash(details.VolumeMode))
	field(w, "Volume", orDash(details.VolumeName))
	field(w, "Selector", orDash(details.Selector))
	field(w, "Data Source", orDash(details.DataSource))
	field(w, "Age", Age(details.CreationTimestamp))
	if len(details.Conditions) > 0 {
		section(w, "CONDITIONS")
		table := NewTable("TYPE", "STATUS", "MESSAGE")
		for _, condition := range details.Conditions {
			table.Row(condition.Type, condition.Status, orDash(condition.Message))
		}
		table.Flush(w)
	}

	if pv := firstOrNil(edgeTargets(result.Graph, target.Ref.UID, model.RelationVolume)); pv != nil {
		section(w, "BOUND PV")
		fmt.Fprintf(w, "Name: %s\n", pv.Ref.Name)
		if pvDetails, ok := detailsOf[*model.PVDetails](*pv); ok && pvDetails != nil {
			fmt.Fprintf(w, "Capacity: %s\n", orDash(pvDetails.Capacity))
			fmt.Fprintf(w, "Storage Class: %s\n", orDash(pvDetails.StorageClassName))
			fmt.Fprintf(w, "Reclaim Policy: %s\n", orDash(pvDetails.ReclaimPolicy))
			fmt.Fprintf(w, "Status: %s\n", statusColor(orDash(pvDetails.Phase)))
		}
	}

	section(w, "USED BY")
	pods := edgeSources(result.Graph, target.Ref.UID, model.RelationVolume)
	if len(pods) == 0 {
		fmt.Fprintln(w, "No Pods currently use this claim.")
		return
	}
	table := NewTable("POD", "NAMESPACE", "STATUS")
	for _, pod := range pods {
		status := "-"
		if podDetails, ok := detailsOf[*model.PodDetails](pod); ok && podDetails != nil {
			status = PodPhase(podDetails)
		}
		table.Row(pod.Ref.Name, pod.Ref.Namespace, status)
	}
	table.Flush(w)
}

func renderPVDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "PV", target.Ref.Name)
	details, _ := detailsOf[*model.PVDetails](target)
	if details == nil {
		return
	}
	statusField(w, "Status", orDash(details.Phase))
	field(w, "Capacity", orDash(details.Capacity))
	field(w, "Access Modes", joinOrDash(details.AccessModes))
	field(w, "Volume Mode", orDash(details.VolumeMode))
	field(w, "StorageClass", orDash(details.StorageClassName))
	field(w, "Reclaim Policy", orDash(details.ReclaimPolicy))
	claim := ""
	if details.ClaimNamespace != "" {
		claim = details.ClaimNamespace + "/"
	}
	claim += details.ClaimName
	field(w, "Claim", orDash(claim))
	field(w, "Mount Options", joinOrDash(details.MountOptions))
	field(w, "Age", Age(details.CreationTimestamp))
	claims := edgeSources(result.Graph, target.Ref.UID, model.RelationVolume)
	section(w, "BOUND PVC")
	if len(claims) == 0 {
		fmt.Fprintln(w, "No bound PVC found.")
		section(w, "USED BY")
		fmt.Fprintln(w, "No Pods currently use this volume.")
		return
	}
	claimTable := NewTable("NAME", "NAMESPACE", "STATUS", "REQUESTED", "CAPACITY")
	usingPods := make(map[string]model.ResourceNode)
	for _, claim := range claims {
		claimStatus, requested, capacity := "-", "-", "-"
		if claimDetails, ok := detailsOf[*model.PVCDetails](claim); ok && claimDetails != nil {
			claimStatus = orDash(claimDetails.Phase)
			requested, capacity = orDash(claimDetails.RequestedCapacity), orDash(claimDetails.Capacity)
		}
		claimTable.Row(claim.Ref.Name, claim.Ref.Namespace, claimStatus, requested, capacity)
		pods := edgeSources(result.Graph, claim.Ref.UID, model.RelationVolume)
		for _, pod := range pods {
			usingPods[pod.Ref.UID] = pod
		}
	}
	claimTable.Flush(w)
	section(w, "USED BY")
	if len(usingPods) == 0 {
		fmt.Fprintln(w, "No Pods currently use this claim.")
		return
	}
	pods := make([]model.ResourceNode, 0, len(usingPods))
	for _, pod := range usingPods {
		pods = append(pods, pod)
	}
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Ref.Namespace != pods[j].Ref.Namespace {
			return pods[i].Ref.Namespace < pods[j].Ref.Namespace
		}
		return pods[i].Ref.Name < pods[j].Ref.Name
	})
	podTable := NewTable("POD", "NAMESPACE", "STATUS")
	for _, pod := range pods {
		status := "-"
		if podDetails, ok := detailsOf[*model.PodDetails](pod); ok && podDetails != nil {
			status = PodPhase(podDetails)
		}
		podTable.Row(pod.Ref.Name, pod.Ref.Namespace, status)
	}
	podTable.Flush(w)
}

func renderConfigMapDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "ConfigMap", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.ConfigMapDetails](target)
	if details != nil {
		field(w, "Age", Age(details.CreationTimestamp))
		field(w, "Last Updated", lastUpdated(details.LastUpdated))
		field(w, "Keys", joinOrDash(details.Keys))
	}
	renderUsedBySection(w, result, target, model.RelationConfig)
}

func renderSecretDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "Secret", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.SecretDetails](target)
	if details != nil {
		field(w, "Age", Age(details.CreationTimestamp))
		field(w, "Last Updated", lastUpdated(details.LastUpdated))
		if len(details.Keys) > 0 {
			field(w, "Keys", joinOrDash(details.Keys))
		} else {
			field(w, "Keys", "(use --show-secret-keys to list key names)")
		}
	}
	renderUsedBySection(w, result, target, model.RelationSecret)
}

func renderUsedBySection(w io.Writer, result *model.QueryResult, target model.ResourceNode, relation model.RelationType) {
	users := make(map[string]model.ResourceNode)
	locations := make(map[string]map[string]bool)
	for _, edge := range result.Graph.Edges {
		if edge.Type != relation || edge.ToUID != target.Ref.UID {
			continue
		}
		user, ok := nodeByUID(result.Graph, edge.FromUID)
		if !ok || user.Ref.Kind != "Pod" {
			continue
		}
		users[user.Ref.UID] = user
		if edge.Detail != "" {
			if locations[user.Ref.UID] == nil {
				locations[user.Ref.UID] = make(map[string]bool)
			}
			locations[user.Ref.UID][edge.Detail] = true
		}
	}
	if len(users) == 0 {
		return
	}
	section(w, "USED BY")
	ordered := make([]model.ResourceNode, 0, len(users))
	for _, user := range users {
		ordered = append(ordered, user)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Ref.Namespace != ordered[j].Ref.Namespace {
			return ordered[i].Ref.Namespace < ordered[j].Ref.Namespace
		}
		return ordered[i].Ref.Name < ordered[j].Ref.Name
	})
	for _, user := range ordered {
		fmt.Fprintf(w, "Pod/%s/%s\n", user.Ref.Namespace, user.Ref.Name)
		for _, location := range sortedKeys(locations[user.Ref.UID]) {
			fmt.Fprintf(w, "  ├── %s\n", location)
		}
		chain := ownerChain(result.Graph, user)
		if len(chain) > 1 && chain[0].Ref.Kind != "Pod" {
			fmt.Fprintf(w, "  └── %s/%s\n", chain[0].Ref.Kind, chain[0].Ref.Name)
		}
	}
}

func lastUpdated(updated *time.Time) string {
	if updated == nil || updated.IsZero() {
		return "unknown"
	}
	return updated.Format(time.RFC3339)
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func renderServiceAccountDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "ServiceAccount", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	details, _ := detailsOf[*model.ServiceAccountDetails](target)
	if details != nil {
		field(w, "Age", Age(details.CreationTimestamp))
	}

	if pods := edgeSources(result.Graph, target.Ref.UID, model.RelationServiceAccount); len(pods) > 0 {
		section(w, "USED BY")
		for _, pod := range pods {
			fmt.Fprintf(w, "Pod/%s\n", pod.Ref.Name)
		}
	}

	bindings := edgeTargets(result.Graph, target.Ref.UID, model.RelationRBAC)
	bindings = append(bindings, filterByKind(edgeSources(result.Graph, target.Ref.UID, model.RelationRBAC), "RoleBinding", "ClusterRoleBinding")...)
	if len(bindings) > 0 {
		section(w, "RBAC")
		for _, binding := range bindings {
			bindingDetails, _ := detailsOf[*model.RoleBindingDetails](binding)
			fmt.Fprintf(w, "%s/%s\n", binding.Ref.Kind, binding.Ref.Name)
			if bindingDetails != nil {
				fmt.Fprintf(w, "└── %s/%s\n", bindingDetails.RoleKind, bindingDetails.RoleName)
				for _, role := range edgeTargets(result.Graph, binding.Ref.UID, model.RelationRBAC) {
					if roleDetails, ok := detailsOf[*model.RoleDetails](role); ok && roleDetails != nil {
						for _, rule := range roleDetails.Rules {
							fmt.Fprintf(w, "    ├── %s\n", rule)
						}
					}
				}
			}
		}
	}
}

func renderNodeDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	header(w, "Node", target.Ref.Name)
	details, _ := detailsOf[*model.NodeDetails](target)
	if details == nil {
		return
	}
	field(w, "Internal IP", orDash(details.InternalIP))
	field(w, "External IP", orDash(details.ExternalIP))
	field(w, "Kubelet Version", orDash(details.KubeletVersion))
	field(w, "Unschedulable", fmt.Sprintf("%t", details.Unschedulable))
	field(w, "Age", Age(details.CreationTimestamp))
	field(w, "Taints", joinOrDash(details.Taints))

	if len(details.Conditions) > 0 {
		section(w, "CONDITIONS")
		table := NewTable("TYPE", "STATUS", "REASON")
		for _, condition := range details.Conditions {
			table.Row(condition.Type, condition.Status, orDash(condition.Reason))
		}
		table.Flush(w)
	}

	if pods := edgeSources(result.Graph, target.Ref.UID, model.RelationNode); len(pods) > 0 {
		section(w, "SCHEDULED PODS")
		table := NewTable("NAME", "NAMESPACE", "STATUS")
		for _, pod := range pods {
			podDetails, _ := detailsOf[*model.PodDetails](pod)
			status := "-"
			if podDetails != nil {
				status = PodPhase(podDetails)
			}
			table.Row(pod.Ref.Name, pod.Ref.Namespace, status)
		}
		table.Flush(w)
	}
}

func renderGenericDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) error {
	header(w, target.Ref.Kind, target.Ref.Name)
	if target.Ref.Namespace != "" {
		field(w, "Namespace", target.Ref.Namespace)
	}
	if details, ok := detailsOf[*model.CustomResourceDetails](target); ok && details != nil {
		field(w, "API Version", orDash(details.APIVersion))
		field(w, "Age", Age(details.CreationTimestamp))
		if len(details.Labels) > 0 {
			section(w, "LABELS")
			writeStringMapTable(w, details.Labels)
		}
		if len(details.Annotations) > 0 {
			section(w, "ANNOTATIONS")
			writeStringMapTable(w, details.Annotations)
		}
		sections := map[string]any{}
		if details.Config != nil {
			sections["config"] = details.Config
		}
		if details.Spec != nil {
			sections["spec"] = details.Spec
		}
		if details.Status != nil {
			sections["status"] = details.Status
		}
		if len(sections) > 0 {
			section(w, "CONFIG / SPEC / STATUS")
			if err := encodeYAML(w, sections); err != nil {
				return err
			}
		}
		if details.FieldsTruncated {
			field(w, "Fields", "truncated at the 100-field limit")
		}
		if len(details.SchemaFields) > 0 {
			section(w, "SCHEMA")
			table := NewTable("FIELD", "TYPE", "DESCRIPTION")
			for _, item := range details.SchemaFields {
				table.Row(item.Path, orDash(item.Type), orDash(item.Description))
			}
			table.Flush(w)
		}
	}

	chain := ownerChain(result.Graph, target)
	if len(chain) > 1 {
		section(w, "OWNED BY")
		writeOwnerTree(w, chain)
	}
	renderPodsForOwners(w, result, []model.ResourceNode{target})

	var related []model.ResourceNode
	for _, node := range result.Graph.Nodes {
		if node.Ref.UID != target.Ref.UID {
			related = append(related, node)
		}
	}
	if len(related) > 0 {
		section(w, "RELATED RESOURCES")
		for _, node := range related {
			name := node.Ref.Name
			if node.Ref.Namespace != "" {
				name = node.Ref.Namespace + "/" + name
			}
			fmt.Fprintf(w, "- %s/%s\n", node.Ref.Kind, name)
		}
	}
	return nil
}

func writeStringMapTable(w io.Writer, values map[string]string) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	table := NewTable("KEY", "VALUE")
	for _, key := range keys {
		table.Row(key, values[key])
	}
	table.Flush(w)
}

func renderPodsForOwners(w io.Writer, result *model.QueryResult, owners []model.ResourceNode) {
	var pods []model.ResourceNode
	for _, owner := range owners {
		pods = append(pods, nodesReachableAsPods(result.Graph, owner.Ref.UID)...)
	}
	if len(pods) == 0 {
		return
	}
	section(w, "PODS")
	table := NewTable("NAME", "STATUS", "READY", "RESTARTS", "NODE", "AGE")
	for _, pod := range pods {
		podDetails, _ := detailsOf[*model.PodDetails](pod)
		status, ready, restarts, node, age := "-", "-", int32(0), "-", "-"
		if podDetails != nil {
			status, ready, restarts, node, age = PodPhase(podDetails), podDetails.Ready, podDetails.Restarts, orDash(podDetails.NodeName), Age(podDetails.CreationTimestamp)
		}
		table.Row(pod.Ref.Name, status, ready, fmt.Sprintf("%d", restarts), node, age)
	}
	table.Flush(w)
}

func nodesReachableAsPods(graph model.Graph, fromUID string) []model.ResourceNode {
	var pods []model.ResourceNode
	for _, node := range edgeSources(graph, fromUID, model.RelationOwner) {
		if node.Ref.Kind == "Pod" {
			pods = append(pods, node)
		}
	}
	return pods
}

func reverseOwnerLookup(graph model.Graph, parentUID string, kind string) []model.ResourceNode {
	var matches []model.ResourceNode
	for _, node := range edgeSources(graph, parentUID, model.RelationOwner) {
		if node.Ref.Kind == kind {
			matches = append(matches, node)
		}
	}
	return matches
}

func renderServicesSummary(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	var services []model.ResourceNode
	seen := make(map[string]bool)
	for _, pod := range workloadPods(result.Graph, target) {
		for _, service := range edgeSources(result.Graph, pod.Ref.UID, model.RelationSelector) {
			if !seen[service.Ref.UID] {
				seen[service.Ref.UID] = true
				services = append(services, service)
			}
		}
	}
	if len(services) == 0 {
		return
	}
	section(w, "SERVICES")
	table := NewTable("NAME", "TYPE", "PORTS", "ENDPOINTS")
	for _, service := range services {
		serviceDetails, _ := detailsOf[*model.ServiceDetails](service)
		ports := "-"
		if serviceDetails != nil {
			ports = joinOrDash(serviceDetails.Ports)
		}
		readyCount := 0
		for _, endpoint := range edgeTargets(result.Graph, service.Ref.UID, model.RelationServiceBackend) {
			if endpointDetails, ok := detailsOf[*model.EndpointsDetails](endpoint); ok && endpointDetails != nil {
				readyCount += endpointDetails.ReadyAddresses
			}
			if sliceDetails, ok := detailsOf[*model.EndpointSliceDetails](endpoint); ok && sliceDetails != nil {
				readyCount += sliceDetails.Ready
			}
		}
		table.Row(service.Ref.Name, orDash(stringOrEmpty(serviceDetails)), ports, fmt.Sprintf("%d ready", readyCount))
	}
	table.Flush(w)
}

func stringOrEmpty(serviceDetails *model.ServiceDetails) string {
	if serviceDetails == nil {
		return ""
	}
	return serviceDetails.Type
}

func renderHPAPDBSummary(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	hpas := edgeSources(result.Graph, target.Ref.UID, model.RelationAutoscaler)
	pdbByUID := make(map[string]model.ResourceNode)
	for _, pod := range workloadPods(result.Graph, target) {
		for _, pdb := range edgeTargets(result.Graph, pod.Ref.UID, model.RelationDisruptionBudget) {
			pdbByUID[pdb.Ref.UID] = pdb
		}
	}
	if len(hpas) > 0 || len(pdbByUID) > 0 {
		section(w, "ASSOCIATED WORKLOAD SETTINGS")
	}
	if len(hpas) > 0 {
		fmt.Fprintln(w, "HPA")
		table := NewTable("NAME", "MIN", "MAX", "CURRENT", "TARGET")
		for _, hpa := range hpas {
			if hpaDetails, ok := detailsOf[*model.HPADetails](hpa); ok && hpaDetails != nil {
				table.Row(hpa.Ref.Name, fmt.Sprintf("%d", hpaDetails.MinReplicas), fmt.Sprintf("%d", hpaDetails.MaxReplicas), fmt.Sprintf("%d", hpaDetails.CurrentReplicas), orDash(hpaDetails.TargetSummary))
			}
		}
		table.Flush(w)
	}
	if len(pdbByUID) > 0 {
		fmt.Fprintln(w, "PDB")
		table := NewTable("NAME", "ALLOWED DISRUPTIONS", "CURRENT HEALTHY", "DESIRED HEALTHY")
		for _, pdb := range pdbByUID {
			if details, ok := detailsOf[*model.PDBDetails](pdb); ok && details != nil {
				table.Row(pdb.Ref.Name, fmt.Sprintf("%d", details.AllowedDisruptions), fmt.Sprintf("%d", details.CurrentHealthy), fmt.Sprintf("%d", details.DesiredHealthy))
			}
		}
		table.Flush(w)
	}
}

func workloadPods(graph model.Graph, target model.ResourceNode) []model.ResourceNode {
	var pods []model.ResourceNode
	queue := []model.ResourceNode{target}
	seen := map[string]bool{target.Ref.UID: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range edgeSources(graph, parent.Ref.UID, model.RelationOwner) {
			if seen[child.Ref.UID] {
				continue
			}
			seen[child.Ref.UID] = true
			if child.Ref.Kind == "Pod" {
				pods = append(pods, child)
			} else {
				queue = append(queue, child)
			}
		}
	}
	return pods
}

func formatSelector(selector map[string]string) string {
	if len(selector) == 0 {
		return "-"
	}
	result := ""
	first := true
	for key, value := range selector {
		if !first {
			result += ","
		}
		result += key + "=" + value
		first = false
	}
	return result
}

func filterByKind(nodes []model.ResourceNode, kinds ...string) []model.ResourceNode {
	allowed := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = true
	}
	var matches []model.ResourceNode
	for _, node := range nodes {
		if allowed[node.Ref.Kind] {
			matches = append(matches, node)
		}
	}
	return matches
}

func firstOrNil(nodes []model.ResourceNode) *model.ResourceNode {
	if len(nodes) == 0 {
		return nil
	}
	return &nodes[0]
}
