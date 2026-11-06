package view

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Fanatic-zer0/kq/internal/model"
)

func TestJobDetailShowsExecutionStatusPodsAndPodEvents(t *testing.T) {
	job := model.ResourceNode{
		Ref: model.ResourceRef{APIVersion: "batch/v1", Kind: "Job", Namespace: "payments", Name: "migration", UID: "job-uid"},
		Details: &model.JobDetails{
			Active: 0, Succeeded: 1, Failed: 0, Completions: 1, Parallelism: 1,
			Containers: []model.WorkloadContainerDetails{{Name: "migrate", Type: "container", Image: "example/migrator:v2", Command: []string{"/bin/migrate"}, Args: []string{"--mode=apply", "--message", "hello world", "--password=REDACTED"}}},
		},
	}
	pod := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "migration-abc", UID: "pod-uid"},
		Details: &model.PodDetails{Phase: "Succeeded", Ready: "1/1", Restarts: 0},
	}
	result := &model.QueryResult{
		Target: job.Ref,
		Graph: model.Graph{
			Target: job.Ref,
			Nodes:  []model.ResourceNode{job, pod},
			Edges:  []model.Edge{{FromUID: pod.Ref.UID, ToUID: job.Ref.UID, Type: model.RelationOwner}},
			Events: []model.EventSummary{{NodeUID: pod.Ref.UID, Type: "Normal", Reason: "Completed", Message: "Job completed"}},
		},
	}
	var out bytes.Buffer
	if err := RenderDetailText(&out, result, DetailOptions{}); err != nil {
		t.Fatalf("RenderDetailText: %v", err)
	}
	for _, expected := range []string{"JOB: migration", "Status: Complete", "Completions: 1/1", "POD TEMPLATE CONTAINERS", "example/migrator:v2", "  [2] --message", `  [3] "hello world"`, "  [4] --password=REDACTED", "PODS", "migration-abc", "EVENTS: Pod/migration-abc", "Job completed"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("Job detail missing %q:\n%s", expected, out.String())
		}
	}
}

func TestCronJobDetailShowsScheduleJobsAndPods(t *testing.T) {
	cronjob := model.ResourceNode{
		Ref: model.ResourceRef{APIVersion: "batch/v1", Kind: "CronJob", Namespace: "payments", Name: "nightly", UID: "cron-uid"},
		Details: &model.CronJobDetails{
			Schedule: "0 2 * * *", ConcurrencyPolicy: "Forbid",
			NextRunTime: ptrTime(time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)),
			Containers:  []model.WorkloadContainerDetails{{Name: "task", Image: "example/task:v1", Args: []string{"--mode=run"}}},
		},
	}
	job := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "batch/v1", Kind: "Job", Namespace: "payments", Name: "nightly-1", UID: "job-uid"},
		Details: &model.JobDetails{Succeeded: 1, Completions: 1},
	}
	pod := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "nightly-1-abc", UID: "pod-uid"},
		Details: &model.PodDetails{Phase: "Succeeded", Ready: "1/1"},
	}
	result := &model.QueryResult{
		Target: cronjob.Ref,
		Graph: model.Graph{
			Target: cronjob.Ref,
			Nodes:  []model.ResourceNode{cronjob, job, pod},
			Edges: []model.Edge{
				{FromUID: job.Ref.UID, ToUID: cronjob.Ref.UID, Type: model.RelationOwner},
				{FromUID: pod.Ref.UID, ToUID: job.Ref.UID, Type: model.RelationOwner},
			},
		},
	}
	var out bytes.Buffer
	if err := RenderDetailText(&out, result, DetailOptions{}); err != nil {
		t.Fatalf("RenderDetailText: %v", err)
	}
	for _, expected := range []string{"CRONJOB: nightly", "Schedule: 0 2 * * *", "Next Run:", "Next Run In:", "Concurrency Policy: Forbid", "POD TEMPLATE CONTAINERS", "example/task:v1", "RELATED RESOURCES", "JOBS", "nightly-1", "PODS", "nightly-1-abc"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("CronJob detail missing %q:\n%s", expected, out.String())
		}
	}
}

func TestIngressDetailShowsRoutesTLSAndBackends(t *testing.T) {
	ingress := model.ResourceNode{
		Ref: model.ResourceRef{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: "payments", Name: "checkout", UID: "ingress-uid"},
		Details: &model.IngressDetails{
			IngressClassName: "public", LoadBalancerAddresses: []string{"192.0.2.10"},
			Annotations:    map[string]string{"nginx.ingress.kubernetes.io/proxy-body-size": "8m"},
			DefaultBackend: &model.IngressRule{Host: "*", Path: "default", ServiceName: "fallback", ServicePort: "8080"},
			Rules:          []model.IngressRule{{Host: "shop.example.test", Path: "/checkout", PathType: "Prefix", ServiceName: "checkout-svc", ServicePort: "http"}},
			TLS:            []model.IngressTLS{{SecretName: "shop-cert", Hosts: []string{"shop.example.test"}}},
		},
	}
	service := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: "payments", Name: "checkout-svc", UID: "service-uid"},
		Details: &model.ServiceDetails{Type: "ClusterIP", Ports: []string{"80/TCP"}},
	}
	endpointSlice := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice", Namespace: "payments", Name: "checkout-svc-abc", UID: "slice-uid"},
		Details: &model.EndpointSliceDetails{Ready: 2},
	}
	pod := model.ResourceNode{
		Ref:     model.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "checkout-abc", UID: "pod-uid"},
		Details: &model.PodDetails{Phase: "Running", Ready: "1/1", PodIP: "10.0.0.5"},
	}
	result := &model.QueryResult{Target: ingress.Ref, Graph: model.Graph{
		Target: ingress.Ref,
		Nodes:  []model.ResourceNode{ingress, service, endpointSlice, pod},
		Edges: []model.Edge{
			{FromUID: ingress.Ref.UID, ToUID: service.Ref.UID, Type: model.RelationIngress},
			{FromUID: service.Ref.UID, ToUID: endpointSlice.Ref.UID, Type: model.RelationServiceBackend},
			{FromUID: service.Ref.UID, ToUID: pod.Ref.UID, Type: model.RelationSelector},
		},
	}}
	var out bytes.Buffer
	if err := RenderDetailText(&out, result, DetailOptions{}); err != nil {
		t.Fatalf("RenderDetailText: %v", err)
	}
	for _, expected := range []string{"INGRESS: checkout", "Ingress Class: public", "Load Balancer: 192.0.2.10", "ANNOTATIONS", "nginx.ingress.kubernetes.io/proxy-body-size", "8m", "shop.example.test", "/checkout", "Prefix", "shop-cert", "RELATED RESOURCES", "checkout-svc", "2 ready", "checkout-abc", "10.0.0.5"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("Ingress detail missing %q:\n%s", expected, out.String())
		}
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
