package query

import (
	"context"
	"sort"

	"github.com/Fanatic-zer0/kq/internal/graph"
	"github.com/Fanatic-zer0/kq/internal/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (e *Engine) resolveIngressTarget(ctx context.Context, ingress *unstructured.Unstructured, builder *graph.Builder, request Request) []model.QueryWarning {
	ingressNode := project(ingress, "Ingress", "networking.k8s.io/v1", request.ShowSecretKeys)
	details, _ := ingressNode.Details.(*model.IngressDetails)
	if details == nil || isExcluded(request.ExcludedKinds, "Service") {
		return nil
	}
	backends := make(map[string]bool)
	if details.DefaultBackend != nil && details.DefaultBackend.ServiceName != "" {
		backends[details.DefaultBackend.ServiceName] = true
	}
	for _, rule := range details.Rules {
		if rule.ServiceName != "" {
			backends[rule.ServiceName] = true
		}
	}

	var warnings []model.QueryWarning
	serviceResource := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	serviceNames := make([]string, 0, len(backends))
	for serviceName := range backends {
		serviceNames = append(serviceNames, serviceName)
	}
	sort.Strings(serviceNames)
	for _, serviceName := range serviceNames {
		service, err := e.client.Get(ctx, serviceResource, ingress.GetNamespace(), serviceName)
		if err != nil {
			warnings = append(warnings, warningForRef(model.ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: ingress.GetNamespace(), Name: serviceName}, "get Ingress backend Service", err))
			continue
		}
		serviceNode := project(service, "Service", "v1", request.ShowSecretKeys)
		builder.AddNode(serviceNode)
		builder.AddEdge(model.Edge{
			FromUID: ingressNode.Ref.UID, ToUID: serviceNode.Ref.UID,
			From: refPointer(ingressNode.Ref), To: refPointer(serviceNode.Ref), Type: model.RelationIngress,
		})
		warnings = append(warnings, e.resolveServiceTarget(ctx, service, builder, request)...)
	}
	return warnings
}
