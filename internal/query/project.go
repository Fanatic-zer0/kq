package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Fanatic-zer0/kq/internal/model"
	cron "github.com/robfig/cron/v3"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func decode[T any](object *unstructured.Unstructured, out *T) bool {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, out) == nil
}

// Project is the exported form of project, used by the view package to build
// list rows for arbitrary resource kinds without duplicating typed decoding.
func Project(object *unstructured.Unstructured, fallbackKind, fallbackVersion string, showSecretKeys bool) model.ResourceNode {
	return project(object, fallbackKind, fallbackVersion, showSecretKeys)
}

// project builds a safe, display-ready node: a stable reference plus typed, allowlisted details.
// Secret contents are never captured; Secret key names are populated only when showSecretKeys is true.
func project(object *unstructured.Unstructured, fallbackKind, fallbackVersion string, showSecretKeys bool) model.ResourceNode {
	kind := object.GetKind()
	if kind == "" {
		kind = fallbackKind
	}
	apiVersion := object.GetAPIVersion()
	if apiVersion == "" {
		apiVersion = fallbackVersion
	}
	ref := model.ResourceRef{
		APIVersion: apiVersion,
		Kind:       kind,
		Namespace:  object.GetNamespace(),
		Name:       object.GetName(),
		UID:        string(object.GetUID()),
	}
	metadata := map[string]any{"name": ref.Name}
	if ref.Namespace != "" {
		metadata["namespace"] = ref.Namespace
	}
	if ref.UID != "" {
		metadata["uid"] = ref.UID
	}
	return model.ResourceNode{
		Ref:     ref,
		Object:  map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": metadata},
		Details: buildDetails(kind, object, showSecretKeys),
	}
}

func buildDetails(kind string, object *unstructured.Unstructured, showSecretKeys bool) any {
	switch kind {
	case "Pod":
		var typed corev1.Pod
		if !decode(object, &typed) {
			return nil
		}
		return podDetails(&typed)
	case "Node":
		var typed corev1.Node
		if !decode(object, &typed) {
			return nil
		}
		return nodeDetails(&typed)
	case "Deployment":
		var typed appsv1.Deployment
		if !decode(object, &typed) {
			return nil
		}
		selector := ""
		if typed.Spec.Selector != nil {
			if parsed, err := metav1.LabelSelectorAsSelector(typed.Spec.Selector); err == nil {
				selector = parsed.String()
			}
		}
		conditions := make([]model.NodeCondition, 0, len(typed.Status.Conditions))
		for _, condition := range typed.Status.Conditions {
			conditions = append(conditions, model.NodeCondition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason})
		}
		return &model.DeploymentDetails{
			Replicas: typed.Status.Replicas, ReadyReplicas: typed.Status.ReadyReplicas,
			UpdatedReplicas: typed.Status.UpdatedReplicas, AvailableReplicas: typed.Status.AvailableReplicas,
			Generation: typed.Generation, ObservedGeneration: typed.Status.ObservedGeneration, Selector: selector,
			Strategy: string(typed.Spec.Strategy.Type), Conditions: conditions, CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "ReplicaSet":
		var typed appsv1.ReplicaSet
		if !decode(object, &typed) {
			return nil
		}
		desired := int32(0)
		if typed.Spec.Replicas != nil {
			desired = *typed.Spec.Replicas
		}
		return &model.ReplicaSetDetails{Desired: desired, Ready: typed.Status.ReadyReplicas, CreationTimestamp: typed.CreationTimestamp.Time}
	case "StatefulSet":
		var typed appsv1.StatefulSet
		if !decode(object, &typed) {
			return nil
		}
		return &model.StatefulSetDetails{
			Replicas: typed.Status.Replicas, ReadyReplicas: typed.Status.ReadyReplicas,
			ServiceName: typed.Spec.ServiceName, UpdateStrategy: string(typed.Spec.UpdateStrategy.Type),
			CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "DaemonSet":
		var typed appsv1.DaemonSet
		if !decode(object, &typed) {
			return nil
		}
		return &model.DaemonSetDetails{
			DesiredNumberScheduled: typed.Status.DesiredNumberScheduled, NumberReady: typed.Status.NumberReady,
			NumberAvailable: typed.Status.NumberAvailable, CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "Job":
		var typed batchv1.Job
		if !decode(object, &typed) {
			return nil
		}
		var completion *time.Time
		if typed.Status.CompletionTime != nil {
			completion = &typed.Status.CompletionTime.Time
		}
		var startTime *time.Time
		if typed.Status.StartTime != nil {
			startTime = &typed.Status.StartTime.Time
		}
		completions, parallelism, backoffLimit := int32(1), int32(1), int32(6)
		if typed.Spec.Completions != nil {
			completions = *typed.Spec.Completions
		}
		if typed.Spec.Parallelism != nil {
			parallelism = *typed.Spec.Parallelism
		}
		if typed.Spec.BackoffLimit != nil {
			backoffLimit = *typed.Spec.BackoffLimit
		}
		return &model.JobDetails{
			Active: typed.Status.Active, Succeeded: typed.Status.Succeeded, Failed: typed.Status.Failed,
			Completions: completions, Parallelism: parallelism, BackoffLimit: backoffLimit,
			Containers: workloadContainers(typed.Spec.Template.Spec),
			StartTime:  startTime, CompletionTime: completion, CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "CronJob":
		var typed batchv1.CronJob
		if !decode(object, &typed) {
			return nil
		}
		var lastSchedule *time.Time
		if typed.Status.LastScheduleTime != nil {
			lastSchedule = &typed.Status.LastScheduleTime.Time
		}
		nextRun, scheduleErr := nextCronJobRun(typed.Spec.Schedule, typed.Spec.TimeZone, typed.Spec.Suspend != nil && *typed.Spec.Suspend, time.Now())
		return &model.CronJobDetails{
			Schedule: typed.Spec.Schedule, Suspend: typed.Spec.Suspend != nil && *typed.Spec.Suspend,
			ConcurrencyPolicy: string(typed.Spec.ConcurrencyPolicy),
			NextRunTime:       nextRun, ScheduleError: scheduleErr,
			Containers:       workloadContainers(typed.Spec.JobTemplate.Spec.Template.Spec),
			LastScheduleTime: lastSchedule, CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "Service":
		var typed corev1.Service
		if !decode(object, &typed) {
			return nil
		}
		ports := make([]string, 0, len(typed.Spec.Ports))
		for _, port := range typed.Spec.Ports {
			ports = append(ports, formatServicePort(port))
		}
		return &model.ServiceDetails{
			Type: string(typed.Spec.Type), ClusterIP: typed.Spec.ClusterIP, Selector: typed.Spec.Selector,
			Ports: ports, CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "Endpoints":
		var typed corev1.Endpoints
		if !decode(object, &typed) {
			return nil
		}
		ready, notReady := 0, 0
		for _, subset := range typed.Subsets {
			ready += len(subset.Addresses)
			notReady += len(subset.NotReadyAddresses)
		}
		return &model.EndpointsDetails{ReadyAddresses: ready, NotReadyAddresses: notReady}
	case "EndpointSlice":
		var typed discoveryv1.EndpointSlice
		if !decode(object, &typed) {
			return nil
		}
		ready, notReady := 0, 0
		for _, endpoint := range typed.Endpoints {
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				ready++
			} else {
				notReady++
			}
		}
		return &model.EndpointSliceDetails{Ready: ready, NotReady: notReady}
	case "Ingress":
		var typed networkingv1.Ingress
		if !decode(object, &typed) {
			return nil
		}
		return ingressDetails(&typed)
	case "PersistentVolumeClaim":
		var typed corev1.PersistentVolumeClaim
		if !decode(object, &typed) {
			return nil
		}
		modes := make([]string, 0, len(typed.Spec.AccessModes))
		for _, mode := range typed.Spec.AccessModes {
			modes = append(modes, string(mode))
		}
		volumeMode := ""
		if typed.Spec.VolumeMode != nil {
			volumeMode = string(*typed.Spec.VolumeMode)
		}
		selector := ""
		if typed.Spec.Selector != nil {
			if parsed, err := metav1.LabelSelectorAsSelector(typed.Spec.Selector); err == nil {
				selector = parsed.String()
			}
		}
		conditions := make([]model.PVCCondition, 0, len(typed.Status.Conditions))
		for _, condition := range typed.Status.Conditions {
			conditions = append(conditions, model.PVCCondition{Type: string(condition.Type), Status: string(condition.Status), Message: condition.Message})
		}
		return &model.PVCDetails{
			Phase: string(typed.Status.Phase), StorageClassName: derefString(typed.Spec.StorageClassName),
			RequestedCapacity: capacityString(typed.Spec.Resources.Requests, corev1.ResourceStorage),
			Capacity:          capacityString(typed.Status.Capacity, corev1.ResourceStorage), AccessModes: modes,
			VolumeMode: volumeMode, VolumeName: typed.Spec.VolumeName, Selector: selector,
			DataSource: pvcDataSource(typed.Spec.DataSource, typed.Spec.DataSourceRef),
			Conditions: conditions, CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "PersistentVolume":
		var typed corev1.PersistentVolume
		if !decode(object, &typed) {
			return nil
		}
		modes := make([]string, 0, len(typed.Spec.AccessModes))
		for _, mode := range typed.Spec.AccessModes {
			modes = append(modes, string(mode))
		}
		volumeMode := ""
		if typed.Spec.VolumeMode != nil {
			volumeMode = string(*typed.Spec.VolumeMode)
		}
		claimNamespace, claimName := "", ""
		if typed.Spec.ClaimRef != nil {
			claimNamespace, claimName = typed.Spec.ClaimRef.Namespace, typed.Spec.ClaimRef.Name
		}
		return &model.PVDetails{
			Capacity: capacityString(typed.Spec.Capacity, corev1.ResourceStorage), AccessModes: modes,
			ReclaimPolicy: string(typed.Spec.PersistentVolumeReclaimPolicy), Phase: string(typed.Status.Phase),
			StorageClassName: typed.Spec.StorageClassName, VolumeMode: volumeMode,
			ClaimNamespace: claimNamespace, ClaimName: claimName,
			MountOptions: append([]string(nil), typed.Spec.MountOptions...), CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "StorageClass":
		var typed storagev1.StorageClass
		if !decode(object, &typed) {
			return nil
		}
		policy := ""
		if typed.ReclaimPolicy != nil {
			policy = string(*typed.ReclaimPolicy)
		}
		return &model.StorageClassDetails{Provisioner: typed.Provisioner, ReclaimPolicy: policy}
	case "ConfigMap":
		var typed corev1.ConfigMap
		if !decode(object, &typed) {
			return nil
		}
		keys := make([]string, 0, len(typed.Data)+len(typed.BinaryData))
		for key := range typed.Data {
			keys = append(keys, key)
		}
		for key := range typed.BinaryData {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return &model.ConfigMapDetails{Keys: keys, CreationTimestamp: typed.CreationTimestamp.Time, LastUpdated: latestManagedUpdate(object)}
	case "Secret":
		var typed corev1.Secret
		if !decode(object, &typed) {
			return nil
		}
		details := &model.SecretDetails{Type: string(typed.Type), CreationTimestamp: typed.CreationTimestamp.Time, LastUpdated: latestManagedUpdate(object)}
		if showSecretKeys {
			keys := make(map[string]struct{}, len(typed.Data)+len(typed.StringData))
			for key := range typed.Data {
				keys[key] = struct{}{}
			}
			for key := range typed.StringData {
				keys[key] = struct{}{}
			}
			for key := range keys {
				details.Keys = append(details.Keys, key)
			}
			sort.Strings(details.Keys)
		}
		return details
	case "ServiceAccount":
		var typed corev1.ServiceAccount
		if !decode(object, &typed) {
			return nil
		}
		secretRefs := make([]string, 0, len(typed.Secrets))
		for _, secret := range typed.Secrets {
			secretRefs = append(secretRefs, secret.Name)
		}
		imagePullSecrets := make([]string, 0, len(typed.ImagePullSecrets))
		for _, secret := range typed.ImagePullSecrets {
			imagePullSecrets = append(imagePullSecrets, secret.Name)
		}
		return &model.ServiceAccountDetails{
			AutomountToken: typed.AutomountServiceAccountToken, SecretRefs: secretRefs,
			ImagePullSecrets: imagePullSecrets, CreationTimestamp: typed.CreationTimestamp.Time,
		}
	case "HorizontalPodAutoscaler":
		var typed autoscalingv2.HorizontalPodAutoscaler
		if !decode(object, &typed) {
			return nil
		}
		min := int32(1)
		if typed.Spec.MinReplicas != nil {
			min = *typed.Spec.MinReplicas
		}
		return &model.HPADetails{
			MinReplicas: min, MaxReplicas: typed.Spec.MaxReplicas, CurrentReplicas: typed.Status.CurrentReplicas,
			TargetSummary: hpaTargetSummary(&typed),
		}
	case "PodDisruptionBudget":
		var typed policyv1.PodDisruptionBudget
		if !decode(object, &typed) {
			return nil
		}
		return &model.PDBDetails{
			AllowedDisruptions: typed.Status.DisruptionsAllowed, CurrentHealthy: typed.Status.CurrentHealthy,
			DesiredHealthy: typed.Status.DesiredHealthy,
		}
	case "RoleBinding", "ClusterRoleBinding":
		return roleBindingDetails(object, kind)
	case "Role":
		var typed rbacv1.Role
		if !decode(object, &typed) {
			return nil
		}
		return &model.RoleDetails{Rules: formatPolicyRules(typed.Rules)}
	case "ClusterRole":
		var typed rbacv1.ClusterRole
		if !decode(object, &typed) {
			return nil
		}
		return &model.RoleDetails{Rules: formatPolicyRules(typed.Rules)}
	case "CustomResourceDefinition":
		return crdDetails(object)
	default:
		return customResourceDetails(object)
	}
}

const (
	maxCustomResourceFields = 100
	maxCustomResourceDepth  = 5
	maxCustomResourceValue  = 256
)

func customResourceDetails(object *unstructured.Unstructured) *model.CustomResourceDetails {
	labels := make(map[string]string, len(object.GetLabels()))
	for key, value := range object.GetLabels() {
		labels[key] = value
	}
	details := &model.CustomResourceDetails{
		APIVersion: object.GetAPIVersion(), CreationTimestamp: object.GetCreationTimestamp().Time,
		Labels: labels, Annotations: safeAnnotations(object.GetAnnotations()),
	}
	for _, section := range []string{"config", "spec", "status"} {
		value, found := object.Object[section]
		if !found {
			continue
		}
		sanitized := appendCustomResourceFields(&details.Fields, &details.FieldsTruncated, section, section, value, 0)
		switch section {
		case "config":
			details.Config = sanitized
		case "spec":
			details.Spec = sanitized
		case "status":
			details.Status = sanitized
		}
	}
	return details
}

func appendCustomResourceFields(fields *[]model.CustomResourceField, truncated *bool, path, key string, value any, depth int) any {
	if len(*fields) >= maxCustomResourceFields {
		*truncated = true
		return "(additional fields omitted)"
	}
	if isSensitiveArgument(key) {
		*fields = append(*fields, model.CustomResourceField{Path: path, Value: "REDACTED"})
		return "REDACTED"
	}
	switch typed := value.(type) {
	case map[string]any:
		if depth >= maxCustomResourceDepth {
			*fields = append(*fields, model.CustomResourceField{Path: path, Value: "(nested fields omitted)"})
			return "(nested fields omitted)"
		}
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			*fields = append(*fields, model.CustomResourceField{Path: path, Value: "{}"})
			return map[string]any{}
		}
		result := make(map[string]any, len(keys))
		for _, childKey := range keys {
			if len(*fields) >= maxCustomResourceFields {
				*truncated = true
				result["..."] = "(additional fields omitted)"
				break
			}
			result[childKey] = appendCustomResourceFields(fields, truncated, path+"."+childKey, childKey, typed[childKey], depth+1)
		}
		return result
	case []any:
		if depth >= maxCustomResourceDepth {
			*fields = append(*fields, model.CustomResourceField{Path: path, Value: "(nested fields omitted)"})
			return "(nested fields omitted)"
		}
		if len(typed) == 0 {
			*fields = append(*fields, model.CustomResourceField{Path: path, Value: "[]"})
			return []any{}
		}
		result := make([]any, 0, len(typed))
		for index, item := range typed {
			if len(*fields) >= maxCustomResourceFields {
				*truncated = true
				result = append(result, "(additional fields omitted)")
				break
			}
			result = append(result, appendCustomResourceFields(fields, truncated, fmt.Sprintf("%s[%d]", path, index), key, item, depth+1))
		}
		return result
	default:
		text := fmt.Sprint(value)
		if len([]rune(text)) > maxCustomResourceValue {
			text = string([]rune(text)[:maxCustomResourceValue]) + "..."
		}
		*fields = append(*fields, model.CustomResourceField{Path: path, Value: text})
		if _, ok := value.(string); ok {
			return text
		}
		return value
	}
}

func podDetails(pod *corev1.Pod) *model.PodDetails {
	ready, total := 0, len(pod.Status.ContainerStatuses)
	var restarts int32
	containers := make([]model.ContainerStatus, 0, total)
	for _, status := range pod.Status.ContainerStatuses {
		if status.Ready {
			ready++
		}
		restarts += status.RestartCount
		containers = append(containers, model.ContainerStatus{
			Name: status.Name, Image: status.Image, Ready: status.Ready,
			RestartCount: status.RestartCount, State: containerStateString(status.State),
		})
	}
	volumes := make([]model.VolumeInfo, 0, len(pod.Spec.Volumes))
	for _, volume := range pod.Spec.Volumes {
		volumes = append(volumes, classifyVolume(volume))
	}
	imagePullSecrets := make([]string, 0, len(pod.Spec.ImagePullSecrets))
	for _, secret := range pod.Spec.ImagePullSecrets {
		imagePullSecrets = append(imagePullSecrets, secret.Name)
	}
	return &model.PodDetails{
		Phase: string(pod.Status.Phase), Ready: fmtRatio(ready, total), Restarts: restarts,
		QoSClass: string(pod.Status.QOSClass), NodeName: pod.Spec.NodeName, PodIP: pod.Status.PodIP,
		HostIP: pod.Status.HostIP, ServiceAccountName: pod.Spec.ServiceAccountName,
		CreationTimestamp: pod.CreationTimestamp.Time, Containers: containers, Volumes: volumes,
		ImagePullSecretRefs: imagePullSecrets,
	}
}

func workloadContainers(podSpec corev1.PodSpec) []model.WorkloadContainerDetails {
	containers := make([]model.WorkloadContainerDetails, 0, len(podSpec.InitContainers)+len(podSpec.Containers)+len(podSpec.EphemeralContainers))
	for _, container := range podSpec.InitContainers {
		containers = append(containers, model.WorkloadContainerDetails{Name: container.Name, Type: "init", Image: container.Image, Command: append([]string(nil), container.Command...), Args: redactSensitiveArgs(container.Args)})
	}
	for _, container := range podSpec.Containers {
		containers = append(containers, model.WorkloadContainerDetails{Name: container.Name, Type: "container", Image: container.Image, Command: append([]string(nil), container.Command...), Args: redactSensitiveArgs(container.Args)})
	}
	for _, container := range podSpec.EphemeralContainers {
		containers = append(containers, model.WorkloadContainerDetails{Name: container.Name, Type: "ephemeral", Image: container.Image, Command: append([]string(nil), container.Command...), Args: redactSensitiveArgs(container.Args)})
	}
	return containers
}

func redactSensitiveArgs(args []string) []string {
	redacted := make([]string, 0, len(args))
	redactNext := false
	for _, arg := range args {
		if redactNext {
			redacted = append(redacted, "REDACTED")
			redactNext = false
			continue
		}
		key, _, hasValue := strings.Cut(arg, "=")
		if isSensitiveArgument(key) {
			if hasValue {
				redacted = append(redacted, key+"=REDACTED")
			} else {
				redacted = append(redacted, arg)
				redactNext = true
			}
			continue
		}
		redacted = append(redacted, arg)
	}
	return redacted
}

func isSensitiveArgument(value string) bool {
	key := strings.ToLower(strings.TrimLeft(strings.TrimSpace(value), "-"))
	for _, marker := range []string{"password", "passwd", "token", "secret", "credential", "api-key", "apikey", "private-key"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func nextCronJobRun(schedule string, timeZone *string, suspended bool, now time.Time) (*time.Time, string) {
	if suspended || strings.TrimSpace(schedule) == "" {
		return nil, ""
	}
	expression := schedule
	if timeZone != nil && strings.TrimSpace(*timeZone) != "" {
		expression = "CRON_TZ=" + *timeZone + " " + expression
	}
	parsed, err := cron.ParseStandard(expression)
	if err != nil {
		return nil, err.Error()
	}
	next := parsed.Next(now)
	if next.IsZero() {
		return nil, "no future run"
	}
	return &next, ""
}

func classifyVolume(volume corev1.Volume) model.VolumeInfo {
	switch {
	case volume.PersistentVolumeClaim != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "PVC", Backing: volume.PersistentVolumeClaim.ClaimName}
	case volume.ConfigMap != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "ConfigMap", Backing: volume.ConfigMap.Name}
	case volume.Secret != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "Secret", Backing: volume.Secret.SecretName}
	case volume.Projected != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "Projected", Backing: projectedSourcesSummary(volume.Projected)}
	case volume.EmptyDir != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "EmptyDir"}
	case volume.HostPath != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "HostPath", Backing: volume.HostPath.Path}
	case volume.CSI != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "CSI", Backing: volume.CSI.Driver}
	case volume.Ephemeral != nil:
		return model.VolumeInfo{Name: volume.Name, Type: "Ephemeral"}
	default:
		return model.VolumeInfo{Name: volume.Name, Type: "Other"}
	}
}

func projectedSourcesSummary(projected *corev1.ProjectedVolumeSource) string {
	kinds := make([]string, 0, len(projected.Sources))
	for _, source := range projected.Sources {
		switch {
		case source.ConfigMap != nil:
			kinds = append(kinds, "ConfigMap:"+source.ConfigMap.Name)
		case source.Secret != nil:
			kinds = append(kinds, "Secret:"+source.Secret.Name)
		case source.ServiceAccountToken != nil:
			kinds = append(kinds, "ServiceAccountToken")
		case source.DownwardAPI != nil:
			kinds = append(kinds, "DownwardAPI")
		}
	}
	return joinNonEmpty(kinds)
}

func containerStateString(state corev1.ContainerState) string {
	switch {
	case state.Running != nil:
		return "Running"
	case state.Waiting != nil:
		if state.Waiting.Reason != "" {
			return "Waiting(" + state.Waiting.Reason + ")"
		}
		return "Waiting"
	case state.Terminated != nil:
		if state.Terminated.Reason != "" {
			return "Terminated(" + state.Terminated.Reason + ")"
		}
		return "Terminated"
	default:
		return ""
	}
}

func nodeDetails(node *corev1.Node) *model.NodeDetails {
	details := &model.NodeDetails{
		KubeletVersion: node.Status.NodeInfo.KubeletVersion, Unschedulable: node.Spec.Unschedulable,
		CreationTimestamp: node.CreationTimestamp.Time,
	}
	for _, address := range node.Status.Addresses {
		switch address.Type {
		case corev1.NodeInternalIP:
			details.InternalIP = address.Address
		case corev1.NodeExternalIP:
			details.ExternalIP = address.Address
		}
	}
	for _, taint := range node.Spec.Taints {
		details.Taints = append(details.Taints, taint.Key+"="+taint.Value+":"+string(taint.Effect))
	}
	for _, condition := range node.Status.Conditions {
		details.Conditions = append(details.Conditions, model.NodeCondition{
			Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason,
		})
	}
	return details
}

func ingressDetails(ingress *networkingv1.Ingress) *model.IngressDetails {
	details := &model.IngressDetails{
		IngressClassName:  derefString(ingress.Spec.IngressClassName),
		Annotations:       safeAnnotations(ingress.Annotations),
		CreationTimestamp: ingress.CreationTimestamp.Time,
	}
	for _, address := range ingress.Status.LoadBalancer.Ingress {
		if address.Hostname != "" {
			details.LoadBalancerAddresses = append(details.LoadBalancerAddresses, address.Hostname)
		} else if address.IP != "" {
			details.LoadBalancerAddresses = append(details.LoadBalancerAddresses, address.IP)
		}
	}
	if ingress.Spec.DefaultBackend != nil && ingress.Spec.DefaultBackend.Service != nil {
		backend := ingress.Spec.DefaultBackend.Service
		details.DefaultBackend = &model.IngressRule{Host: "*", Path: "default", ServiceName: backend.Name, ServicePort: ingressServicePort(backend.Port)}
	}
	for _, rule := range ingress.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			serviceName, servicePort := "", ""
			if path.Backend.Service != nil {
				serviceName = path.Backend.Service.Name
				servicePort = ingressServicePort(path.Backend.Service.Port)
			}
			pathType := ""
			if path.PathType != nil {
				pathType = string(*path.PathType)
			}
			details.Rules = append(details.Rules, model.IngressRule{Host: rule.Host, Path: path.Path, PathType: pathType, ServiceName: serviceName, ServicePort: servicePort})
		}
	}
	for _, tls := range ingress.Spec.TLS {
		details.TLS = append(details.TLS, model.IngressTLS{SecretName: tls.SecretName, Hosts: append([]string(nil), tls.Hosts...)})
	}
	return details
}

func safeAnnotations(annotations map[string]string) map[string]string {
	if len(annotations) == 0 {
		return nil
	}
	result := make(map[string]string, len(annotations))
	for key, value := range annotations {
		if isSensitiveArgument(key) {
			value = "REDACTED"
		}
		result[key] = value
	}
	return result
}

func ingressServicePort(port networkingv1.ServiceBackendPort) string {
	if port.Name != "" {
		return port.Name
	}
	if port.Number != 0 {
		return fmtInt32(port.Number)
	}
	return ""
}

func pvcDataSource(local *corev1.TypedLocalObjectReference, reference *corev1.TypedObjectReference) string {
	if reference != nil {
		return formatPVCDataSource(reference.APIGroup, reference.Kind, reference.Namespace, reference.Name)
	}
	if local != nil {
		return formatPVCDataSource(local.APIGroup, local.Kind, nil, local.Name)
	}
	return ""
}

func formatPVCDataSource(apiGroup *string, kind string, namespace *string, name string) string {
	parts := make([]string, 0, 4)
	if apiGroup != nil && *apiGroup != "" {
		parts = append(parts, *apiGroup)
	}
	parts = append(parts, kind)
	if namespace != nil && *namespace != "" {
		parts = append(parts, *namespace)
	}
	parts = append(parts, name)
	return strings.Join(parts, "/")
}

func hpaTargetSummary(hpa *autoscalingv2.HorizontalPodAutoscaler) string {
	for _, metric := range hpa.Spec.Metrics {
		if metric.Resource != nil && metric.Resource.Target.AverageUtilization != nil {
			return string(metric.Resource.Name) + " " + fmtInt32(*metric.Resource.Target.AverageUtilization) + "%"
		}
	}
	return ""
}

func roleBindingDetails(object *unstructured.Unstructured, kind string) *model.RoleBindingDetails {
	if kind == "ClusterRoleBinding" {
		var typed rbacv1.ClusterRoleBinding
		if !decode(object, &typed) {
			return nil
		}
		return &model.RoleBindingDetails{RoleKind: typed.RoleRef.Kind, RoleName: typed.RoleRef.Name, Subjects: formatSubjects(typed.Subjects)}
	}
	var typed rbacv1.RoleBinding
	if !decode(object, &typed) {
		return nil
	}
	return &model.RoleBindingDetails{RoleKind: typed.RoleRef.Kind, RoleName: typed.RoleRef.Name, Subjects: formatSubjects(typed.Subjects)}
}

func formatSubjects(subjects []rbacv1.Subject) []string {
	formatted := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Namespace != "" {
			formatted = append(formatted, subject.Kind+"/"+subject.Namespace+"/"+subject.Name)
		} else {
			formatted = append(formatted, subject.Kind+"/"+subject.Name)
		}
	}
	return formatted
}

func formatPolicyRules(rules []rbacv1.PolicyRule) []string {
	formatted := make([]string, 0, len(rules))
	for _, rule := range rules {
		formatted = append(formatted, joinNonEmpty(rule.Verbs)+" "+joinNonEmpty(rule.Resources)+" ("+joinNonEmpty(rule.APIGroups)+")")
	}
	return formatted
}

func crdDetails(object *unstructured.Unstructured) *model.CRDDetails {
	details := &model.CRDDetails{
		Group:             nestedString(object.Object, "spec", "group"),
		Scope:             nestedString(object.Object, "spec", "scope"),
		Kind:              nestedString(object.Object, "spec", "names", "kind"),
		Plural:            nestedString(object.Object, "spec", "names", "plural"),
		Singular:          nestedString(object.Object, "spec", "names", "singular"),
		CreationTimestamp: object.GetCreationTimestamp().Time,
	}
	versions, _, _ := unstructured.NestedSlice(object.Object, "spec", "versions")
	for _, value := range versions {
		version, ok := value.(map[string]any)
		if !ok {
			continue
		}
		served, _ := version["served"].(bool)
		storage, _ := version["storage"].(bool)
		details.Versions = append(details.Versions, model.CRDVersion{Name: stringValue(version["name"]), Served: served, Storage: storage})
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, value := range conditions {
		condition, ok := value.(map[string]any)
		if !ok {
			continue
		}
		details.Conditions = append(details.Conditions, model.CRDCondition{
			Type: stringValue(condition["type"]), Status: stringValue(condition["status"]),
			Reason: stringValue(condition["reason"]), Message: stringValue(condition["message"]),
		})
	}
	return details
}

func latestManagedUpdate(object *unstructured.Unstructured) *time.Time {
	managedFields, _, _ := unstructured.NestedSlice(object.Object, "metadata", "managedFields")
	var latest time.Time
	for _, value := range managedFields {
		managedField, ok := value.(map[string]any)
		if !ok {
			continue
		}
		updatedAt := stringValue(managedField["time"])
		if updatedAt == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
		if err == nil && parsed.After(latest) {
			latest = parsed
		}
	}
	if latest.IsZero() {
		return nil
	}
	return &latest
}

func capacityString(resources corev1.ResourceList, name corev1.ResourceName) string {
	if quantity, ok := resources[name]; ok {
		return quantity.String()
	}
	return ""
}

func formatServicePort(port corev1.ServicePort) string {
	target := ""
	if port.TargetPort.String() != "" && port.TargetPort.String() != fmtInt32(port.Port) {
		target = " -> " + port.TargetPort.String()
	}
	return fmtInt32(port.Port) + "/" + string(port.Protocol) + target
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func fmtRatio(ready, total int) string {
	return strconv.Itoa(ready) + "/" + strconv.Itoa(total)
}

func fmtInt32(value int32) string {
	return strconv.FormatInt(int64(value), 10)
}

func joinNonEmpty(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ","
		}
		result += value
	}
	return result
}
