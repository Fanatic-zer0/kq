package model

import "time"

type ResourceRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"`
}

type RelationType string

const (
	RelationOwner            RelationType = "owner"
	RelationOwnedBy          RelationType = "owned-by"
	RelationSelector         RelationType = "selector"
	RelationServiceBackend   RelationType = "service-backend"
	RelationVolume           RelationType = "volume"
	RelationConfig           RelationType = "config"
	RelationSecret           RelationType = "secret"
	RelationServiceAccount   RelationType = "service-account"
	RelationRBAC             RelationType = "rbac"
	RelationNode             RelationType = "node"
	RelationAutoscaler       RelationType = "autoscaler"
	RelationDisruptionBudget RelationType = "disruption-budget"
	RelationIngress          RelationType = "ingress"
	RelationNetworkPolicy    RelationType = "network-policy"
)

type ResourceNode struct {
	Ref     ResourceRef    `json:"ref"`
	Object  map[string]any `json:"object,omitempty"`
	Details any            `json:"details,omitempty"`
}

type Edge struct {
	FromUID string       `json:"fromUid,omitempty"`
	ToUID   string       `json:"toUid,omitempty"`
	From    *ResourceRef `json:"from,omitempty"`
	To      *ResourceRef `json:"to,omitempty"`
	Type    RelationType `json:"type"`
	Detail  string       `json:"detail,omitempty"`
}

type EventSummary struct {
	UID            string      `json:"uid,omitempty"`
	Namespace      string      `json:"namespace,omitempty"`
	InvolvedObject ResourceRef `json:"involvedObject"`
	NodeUID        string      `json:"nodeUid"`
	Type           string      `json:"type,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	Message        string      `json:"message,omitempty"`
	Count          int32       `json:"count,omitempty"`
	FirstTimestamp time.Time   `json:"firstTimestamp,omitempty"`
	LastTimestamp  time.Time   `json:"lastTimestamp,omitempty"`
}

type Graph struct {
	Target ResourceRef    `json:"target"`
	Nodes  []ResourceNode `json:"nodes"`
	Edges  []Edge         `json:"edges"`
	Events []EventSummary `json:"events,omitempty"`
}

type QueryWarning struct {
	Resource  ResourceRef `json:"resource"`
	Operation string      `json:"operation"`
	Message   string      `json:"message"`
}

type QueryResult struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	Target      ResourceRef    `json:"target"`
	Graph       Graph          `json:"graph"`
	Warnings    []QueryWarning `json:"warnings,omitempty"`
}
