package query

import (
	"context"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (e *Engine) resolvePodPolicies(ctx context.Context, pod *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	var tasks []func() []model.QueryWarning
	if !isExcluded(request.ExcludedKinds, "NetworkPolicy") {
		tasks = append(tasks, func() []model.QueryWarning {
			return e.resolvePodNetworkPolicies(ctx, pod, builder, request)
		})
	}
	if !isExcluded(request.ExcludedKinds, "PodDisruptionBudget") {
		tasks = append(tasks, func() []model.QueryWarning {
			return e.resolvePodDisruptionBudgets(ctx, pod, builder, request)
		})
	}
	return e.runConcurrently(ctx, tasks)
}

func (e *Engine) resolvePodNetworkPolicies(ctx context.Context, pod *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	podRef := reference(pod)
	resource := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	items, err := e.listAll(ctx, resource, pod.GetNamespace(), metav1.ListOptions{Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningFor(pod, "list NetworkPolicies", err)}
	}
	var warnings []model.QueryWarning
	for i := range items {
		policy := &items[i]
		var typed networkingv1.NetworkPolicy
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(policy.Object, &typed); err != nil {
			warnings = append(warnings, warningFor(policy, "decode NetworkPolicy selector", err))
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&typed.Spec.PodSelector)
		if err != nil || !selector.Matches(labels.Set(pod.GetLabels())) {
			continue
		}
		policyNode := project(policy, "NetworkPolicy", "networking.k8s.io/v1", request.ShowSecretKeys)
		builder.AddNode(policyNode)
		builder.AddEdge(model.Edge{FromUID: policyNode.Ref.UID, ToUID: podRef.UID, From: refPointer(policyNode.Ref), To: refPointer(podRef), Type: model.RelationNetworkPolicy})
	}
	return warnings
}

func (e *Engine) resolvePodDisruptionBudgets(ctx context.Context, pod *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	podRef := reference(pod)
	resource := schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}
	items, err := e.listAll(ctx, resource, pod.GetNamespace(), metav1.ListOptions{Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningFor(pod, "list PodDisruptionBudgets", err)}
	}
	var warnings []model.QueryWarning
	for i := range items {
		budget := &items[i]
		var typed policyv1.PodDisruptionBudget
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(budget.Object, &typed); err != nil {
			warnings = append(warnings, warningFor(budget, "decode PDB selector", err))
			continue
		}
		if typed.Spec.Selector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(typed.Spec.Selector)
		if err != nil || !selector.Matches(labels.Set(pod.GetLabels())) {
			continue
		}
		budgetNode := project(budget, "PodDisruptionBudget", "policy/v1", request.ShowSecretKeys)
		builder.AddNode(budgetNode)
		builder.AddEdge(model.Edge{FromUID: podRef.UID, ToUID: budgetNode.Ref.UID, From: refPointer(podRef), To: refPointer(budgetNode.Ref), Type: model.RelationDisruptionBudget})
	}
	return warnings
}

func (e *Engine) resolveWorkloadHPAs(ctx context.Context, builder *graph.Builder, request Request, depths map[string]int) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "HorizontalPodAutoscaler") {
		return nil
	}
	resource := schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	var warnings []model.QueryWarning
	for _, node := range builder.Value().Nodes {
		if node.Ref.Kind != "Deployment" && node.Ref.Kind != "StatefulSet" {
			continue
		}
		if depths[node.Ref.UID]+1 > request.Depth {
			continue
		}
		items, err := e.listAll(ctx, resource, node.Ref.Namespace, metav1.ListOptions{Limit: 500})
		if err != nil {
			warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler", Namespace: node.Ref.Namespace}, "list HPAs", err))
			continue
		}
		for i := range items {
			hpa := &items[i]
			var typed autoscalingv2.HorizontalPodAutoscaler
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(hpa.Object, &typed); err != nil {
				warnings = append(warnings, warningFor(hpa, "decode HPA target", err))
				continue
			}
			if typed.Spec.ScaleTargetRef.Kind != node.Ref.Kind || typed.Spec.ScaleTargetRef.Name != node.Ref.Name {
				continue
			}
			hpaNode := project(hpa, "HorizontalPodAutoscaler", "autoscaling/v2", request.ShowSecretKeys)
			builder.AddNode(hpaNode)
			builder.AddEdge(model.Edge{FromUID: hpaNode.Ref.UID, ToUID: node.Ref.UID, From: refPointer(hpaNode.Ref), To: refPointer(node.Ref), Type: model.RelationAutoscaler})
		}
	}
	return warnings
}

func ingressReferencesService(ingress *unstructured.Unstructured, serviceName string) bool {
	if nestedString(ingress.Object, "spec", "defaultBackend", "service", "name") == serviceName {
		return true
	}
	rules, _, _ := unstructured.NestedSlice(ingress.Object, "spec", "rules")
	for _, ruleValue := range rules {
		rule, ok := ruleValue.(map[string]any)
		if !ok {
			continue
		}
		paths, _, _ := unstructured.NestedSlice(rule, "http", "paths")
		for _, pathValue := range paths {
			path, ok := pathValue.(map[string]any)
			if ok && nestedString(path, "backend", "service", "name") == serviceName {
				return true
			}
		}
	}
	return false
}

func (e *Engine) resolveServiceIngresses(ctx context.Context, service *unstructured.Unstructured, serviceRef model.ResourceRef, builder *graph.Builder, request Request) []model.QueryWarning {
	if isExcluded(request.ExcludedKinds, "Ingress") {
		return nil
	}
	resource := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	items, err := e.listAll(ctx, resource, service.GetNamespace(), metav1.ListOptions{Limit: 500})
	if err != nil {
		return []model.QueryWarning{warningForRef(model.ResourceRef{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: service.GetNamespace()}, "list Ingresses", err)}
	}
	for i := range items {
		ingress := &items[i]
		if !ingressReferencesService(ingress, service.GetName()) {
			continue
		}
		ingressNode := project(ingress, "Ingress", "networking.k8s.io/v1", request.ShowSecretKeys)
		builder.AddNode(ingressNode)
		builder.AddEdge(model.Edge{FromUID: ingressNode.Ref.UID, ToUID: serviceRef.UID, From: refPointer(ingressNode.Ref), To: refPointer(serviceRef), Type: model.RelationIngress})
	}
	return nil
}
