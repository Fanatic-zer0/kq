package view

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// podLike holds just the Pod fields needed for list rendering, decoded once per row.
type podLike struct {
	phase              string
	readyContainers    int
	totalContainers    int
	restarts           int32
	nodeName           string
	podIP              string
	hostIP             string
	qosClass           string
	serviceAccountName string
	containerStates    []string
	configMapRefs      int
	secretRefs         int
	pvcRefs            int
}

func decodePod(object *unstructured.Unstructured, out *podLike) bool {
	var typed corev1.Pod
	if runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &typed) != nil {
		return false
	}
	ready := 0
	states := make([]string, 0, len(typed.Status.ContainerStatuses))
	var restarts int32
	for _, status := range typed.Status.ContainerStatuses {
		if status.Ready {
			ready++
		}
		restarts += status.RestartCount
		states = append(states, containerStateReason(status.State))
	}
	configMapRefs, secretRefs, pvcRefs := countReferences(typed)
	*out = podLike{
		phase: string(typed.Status.Phase), readyContainers: ready, totalContainers: len(typed.Status.ContainerStatuses),
		restarts: restarts, nodeName: typed.Spec.NodeName, podIP: typed.Status.PodIP, hostIP: typed.Status.HostIP,
		qosClass: string(typed.Status.QOSClass), serviceAccountName: typed.Spec.ServiceAccountName, containerStates: states,
		configMapRefs: configMapRefs, secretRefs: secretRefs, pvcRefs: pvcRefs,
	}
	return true
}

func containerStateReason(state corev1.ContainerState) string {
	switch {
	case state.Waiting != nil:
		return state.Waiting.Reason
	case state.Terminated != nil:
		return state.Terminated.Reason
	default:
		return ""
	}
}

// countReferences derives ConfigMap/Secret/PVC counts directly from the Pod spec
// (no extra API calls), matching the -o wide CONFIG/SECRET/STORAGE columns.
func countReferences(pod corev1.Pod) (configMaps, secrets, pvcs int) {
	configSeen, secretSeen, pvcSeen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, volume := range pod.Spec.Volumes {
		switch {
		case volume.ConfigMap != nil:
			configSeen[volume.ConfigMap.Name] = true
		case volume.Secret != nil:
			secretSeen[volume.Secret.SecretName] = true
		case volume.PersistentVolumeClaim != nil:
			pvcSeen[volume.PersistentVolumeClaim.ClaimName] = true
		case volume.Projected != nil:
			for _, source := range volume.Projected.Sources {
				if source.ConfigMap != nil {
					configSeen[source.ConfigMap.Name] = true
				}
				if source.Secret != nil {
					secretSeen[source.Secret.Name] = true
				}
			}
		}
	}
	for _, secret := range pod.Spec.ImagePullSecrets {
		secretSeen[secret.Name] = true
	}
	for _, container := range append(append([]corev1.Container{}, pod.Spec.Containers...), pod.Spec.InitContainers...) {
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if env.ValueFrom.ConfigMapKeyRef != nil {
				configSeen[env.ValueFrom.ConfigMapKeyRef.Name] = true
			}
			if env.ValueFrom.SecretKeyRef != nil {
				secretSeen[env.ValueFrom.SecretKeyRef.Name] = true
			}
		}
		for _, envFrom := range container.EnvFrom {
			if envFrom.ConfigMapRef != nil {
				configSeen[envFrom.ConfigMapRef.Name] = true
			}
			if envFrom.SecretRef != nil {
				secretSeen[envFrom.SecretRef.Name] = true
			}
		}
	}
	return len(configSeen), len(secretSeen), len(pvcSeen)
}

func countPodReferences(pod podLike) (configMaps, secrets, storage int) {
	return pod.configMapRefs, pod.secretRefs, pod.pvcRefs
}

func podListStatus(pod podLike) string {
	for _, reason := range pod.containerStates {
		if reason != "" && reason != "Completed" {
			return reason
		}
	}
	if pod.phase == "Succeeded" {
		return "Completed"
	}
	return pod.phase
}
