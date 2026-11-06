package graph

import (
	"testing"

	"github.com/Fanatic-zer0/kq/internal/model"
)

func TestAddNodeDeduplicatesByUID(t *testing.T) {
	builder := New(model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "pod", UID: "pod-uid"})
	first := model.ResourceNode{Ref: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "pod", UID: "pod-uid"}}
	second := model.ResourceNode{Ref: model.ResourceRef{Kind: "Pod", Namespace: "other", Name: "renamed", UID: "pod-uid"}}

	if !builder.AddNode(first) {
		t.Fatal("first node should be inserted")
	}
	if builder.AddNode(second) {
		t.Fatal("node with an existing UID should be deduplicated")
	}
	if got := len(builder.Value().Nodes); got != 1 {
		t.Fatalf("got %d nodes, want 1", got)
	}
}
