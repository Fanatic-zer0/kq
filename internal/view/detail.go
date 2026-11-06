package view

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Fanatic-zer0/kq/internal/model"
)

type DetailOptions struct {
	Since        time.Duration
	ShowWarnings bool
}

func RenderDetailText(w io.Writer, result *model.QueryResult, opts DetailOptions) error {
	target, ok := nodeByUID(result.Graph, result.Target.UID)
	if !ok {
		target = model.ResourceNode{Ref: result.Target}
	}
	switch result.Target.Kind {
	case "Pod":
		renderPodDetail(w, result, target)
	case "Deployment":
		renderDeploymentDetail(w, result, target)
	case "StatefulSet":
		renderStatefulSetDetail(w, result, target)
	case "Job":
		renderJobDetail(w, result, target)
	case "CronJob":
		renderCronJobDetail(w, result, target)
	case "Service":
		renderServiceDetail(w, result, target)
	case "Ingress":
		renderIngressDetail(w, result, target)
	case "CustomResourceDefinition":
		renderCRDDetail(w, target)
	case "PersistentVolumeClaim":
		renderPVCDetail(w, result, target)
	case "PersistentVolume":
		renderPVDetail(w, result, target)
	case "ConfigMap":
		renderConfigMapDetail(w, result, target)
	case "Secret":
		renderSecretDetail(w, result, target)
	case "ServiceAccount":
		renderServiceAccountDetail(w, result, target)
	case "Node":
		renderNodeDetail(w, result, target)
	default:
		if err := renderGenericDetail(w, result, target); err != nil {
			return err
		}
	}
	renderEventsSection(w, result, target, opts.Since)
	if opts.ShowWarnings {
		return renderWarningsSection(w, result.Warnings)
	}
	return nil
}

func header(w io.Writer, kind, name string) {
	fmt.Fprintf(w, "%s: %s\n", bold(strings.ToUpper(kind)), name)
}

func field(w io.Writer, label, value string) {
	fmt.Fprintf(w, "%s: %s\n", label, value)
}

func statusField(w io.Writer, label, value string) {
	fmt.Fprintf(w, "%s: %s\n", label, statusColor(value))
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n", bold(title))
}

func renderPodDetail(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	details, _ := detailsOf[*model.PodDetails](target)
	header(w, "Pod", target.Ref.Name)
	field(w, "Namespace", target.Ref.Namespace)
	if details == nil {
		return
	}
	statusField(w, "Status", PodPhase(details))
	field(w, "Ready", details.Ready)
	field(w, "Restarts", fmt.Sprintf("%d", details.Restarts))
	field(w, "Age", Age(details.CreationTimestamp))
	field(w, "QoS", orDash(details.QoSClass))
	field(w, "Node", orDash(details.NodeName)+" ("+orDash(details.HostIP)+")")
	field(w, "Pod IP", orDash(details.PodIP))
	field(w, "ServiceAccount", orDash(details.ServiceAccountName))

	chain := ownerChain(result.Graph, target)
	if len(chain) > 1 {
		section(w, "OWNED BY")
		writeOwnerTree(w, chain)
	}

	if len(details.Containers) > 0 {
		section(w, "CONTAINERS")
		table := NewTable("NAME", "IMAGE", "READY", "RESTARTS", "STATE")
		for _, container := range details.Containers {
			table.Row(container.Name, container.Image, boolRatio(container.Ready), fmt.Sprintf("%d", container.RestartCount), container.State)
		}
		table.Flush(w)
	}

	if configMaps := edgeTargets(result.Graph, target.Ref.UID, model.RelationConfig); len(configMaps) > 0 {
		section(w, "CONFIGMAPS")
		table := NewTable("NAME", "KEYS", "AGE", "STATUS")
		for _, cm := range configMaps {
			cmDetails, _ := detailsOf[*model.ConfigMapDetails](cm)
			keys, age := "-", "-"
			if cmDetails != nil {
				keys = fmt.Sprintf("%d", len(cmDetails.Keys))
				age = Age(cmDetails.CreationTimestamp)
			}
			table.Row(cm.Ref.Name, keys, age, Status(result.Warnings, cm.Ref))
		}
		table.Flush(w)
	}

	if secrets := edgeTargets(result.Graph, target.Ref.UID, model.RelationSecret); len(secrets) > 0 {
		section(w, "SECRETS")
		table := NewTable("NAME", "KEYS", "AGE", "STATUS")
		for _, secret := range secrets {
			secretDetails, _ := detailsOf[*model.SecretDetails](secret)
			keys, age := "-", "-"
			if secretDetails != nil {
				if len(secretDetails.Keys) > 0 {
					keys = fmt.Sprintf("%d", len(secretDetails.Keys))
				}
				age = Age(secretDetails.CreationTimestamp)
			}
			table.Row(secret.Ref.Name, keys, age, Status(result.Warnings, secret.Ref))
		}
		table.Flush(w)
	}

	if len(details.Volumes) > 0 {
		section(w, "VOLUMES / STORAGE")
		table := NewTable("VOLUME", "TYPE", "CLAIM/BACKING", "STATUS", "CAPACITY", "ACCESS MODES")
		for _, volume := range details.Volumes {
			status, capacity, modes := "OK", "-", "-"
			if volume.Type == "PVC" {
				if pvc := findByName(edgeTargets(result.Graph, target.Ref.UID, model.RelationVolume), volume.Backing); pvc != nil {
					status = Status(result.Warnings, pvc.Ref)
					if pvcDetails, ok := detailsOf[*model.PVCDetails](*pvc); ok && pvcDetails != nil {
						status = pvcDetails.Phase
						capacity = orDash(pvcDetails.Capacity)
						modes = joinOrDash(pvcDetails.AccessModes)
					}
				}
			}
			table.Row(volume.Name, volume.Type, orDash(volume.Backing), status, capacity, modes)
		}
		table.Flush(w)
	}

	renderServiceNetworkSection(w, result, target)

	hpas := []model.ResourceNode{}
	pdbs := edgeTargets(result.Graph, target.Ref.UID, model.RelationDisruptionBudget)
	for _, ancestor := range chain {
		hpas = append(hpas, edgeSources(result.Graph, ancestor.Ref.UID, model.RelationAutoscaler)...)
	}
	if len(hpas) > 0 || len(pdbs) > 0 {
		section(w, "ASSOCIATED WORKLOAD SETTINGS")
		for _, hpa := range hpas {
			if hpaDetails, ok := detailsOf[*model.HPADetails](hpa); ok && hpaDetails != nil {
				fmt.Fprintf(w, "HPA/%s: %d/%d replicas\n", hpa.Ref.Name, hpaDetails.CurrentReplicas, hpaDetails.MaxReplicas)
			}
		}
		for _, pdb := range pdbs {
			if pdbDetails, ok := detailsOf[*model.PDBDetails](pdb); ok && pdbDetails != nil {
				fmt.Fprintf(w, "PDB/%s: disruptionsAllowed=%d\n", pdb.Ref.Name, pdbDetails.AllowedDisruptions)
			}
		}
	}
}

func writeOwnerTree(w io.Writer, chain []model.ResourceNode) {
	for i, node := range chain {
		prefix := ""
		if i > 0 {
			prefix = strings.Repeat("    ", i-1) + "└── "
		}
		fmt.Fprintf(w, "%s%s/%s\n", prefix, cyan(node.Ref.Kind), node.Ref.Name)
	}
}

func boolRatio(ready bool) string {
	if ready {
		return "1/1"
	}
	return "0/1"
}

func findByName(nodes []model.ResourceNode, name string) *model.ResourceNode {
	for i := range nodes {
		if nodes[i].Ref.Name == name {
			return &nodes[i]
		}
	}
	return nil
}

func renderServiceNetworkSection(w io.Writer, result *model.QueryResult, target model.ResourceNode) {
	services := edgeSources(result.Graph, target.Ref.UID, model.RelationSelector)
	ingresses := []model.ResourceNode{}
	hasNetwork := len(services) > 0
	for _, service := range services {
		ingresses = append(ingresses, edgeSources(result.Graph, service.Ref.UID, model.RelationIngress)...)
	}
	if !hasNetwork {
		return
	}
	section(w, "SERVICE / NETWORK")
	for _, service := range services {
		fmt.Fprintf(w, "Service/%s\n", service.Ref.Name)
		for _, endpoint := range edgeTargets(result.Graph, service.Ref.UID, model.RelationServiceBackend) {
			if endpointDetails, ok := detailsOf[*model.EndpointsDetails](endpoint); ok && endpointDetails != nil {
				fmt.Fprintf(w, "└── %s/%s: %d ready\n", endpoint.Ref.Kind, endpoint.Ref.Name, endpointDetails.ReadyAddresses)
			} else if sliceDetails, ok := detailsOf[*model.EndpointSliceDetails](endpoint); ok && sliceDetails != nil {
				fmt.Fprintf(w, "└── %s/%s: %d ready\n", endpoint.Ref.Kind, endpoint.Ref.Name, sliceDetails.Ready)
			}
		}
	}
	for _, ingress := range ingresses {
		for _, service := range services {
			fmt.Fprintf(w, "Ingress/%s → Service/%s\n", ingress.Ref.Name, service.Ref.Name)
		}
	}
}

func renderEventsSection(w io.Writer, result *model.QueryResult, target model.ResourceNode, since time.Duration) {
	events := eventsForUID(result.Graph, target.Ref.UID)
	if len(events) == 0 {
		return
	}
	section(w, fmt.Sprintf("EVENTS (LAST %s)", FormatDuration(since)))
	for _, event := range events {
		// Pad the plain Type text to a fixed width before colorizing, since ANSI
		// escape codes are zero-width on a terminal but would otherwise be counted
		// by fmt's %-Ns padding and misalign the following column.
		paddedType := eventTypeColor(event.Type) + strings.Repeat(" ", max(0, 7-len(event.Type)))
		fmt.Fprintf(w, "%s  %s  %-10s %s\n", event.LastTimestamp.Format("15:04:05"), paddedType, event.Reason, event.Message)
	}
}

func renderWarningsSection(w io.Writer, warnings []model.QueryWarning) error {
	if len(warnings) == 0 {
		return nil
	}
	section(w, "WARNINGS")
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(w, "- %s/%s/%s %s: %s\n", warning.Resource.Kind, warning.Resource.Namespace, warning.Resource.Name, warning.Operation, yellow(warning.Message)); err != nil {
			return err
		}
	}
	return nil
}

func PodPhase(details *model.PodDetails) string {
	for _, container := range details.Containers {
		if strings.HasPrefix(container.State, "Waiting(") {
			return strings.TrimSuffix(strings.TrimPrefix(container.State, "Waiting("), ")")
		}
		if strings.HasPrefix(container.State, "Terminated(") {
			reason := strings.TrimSuffix(strings.TrimPrefix(container.State, "Terminated("), ")")
			if reason != "Completed" {
				return reason
			}
		}
	}
	if details.Phase == "Succeeded" {
		return "Completed"
	}
	return details.Phase
}
