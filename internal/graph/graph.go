package graph

import (
	"fmt"
	"sync"

	"github.com/Fanatic-zer0/kq/internal/model"
)

// Builder is safe for concurrent use: the query engine resolves sibling
// resources (e.g. many Pods owned by the same ReplicaSet) concurrently.
type Builder struct {
	mu      sync.Mutex
	value   model.Graph
	indices map[string]int
	edges   map[string]struct{}
	refs    map[string]model.ResourceRef
}

func New(target model.ResourceRef) *Builder {
	return &Builder{
		value:   model.Graph{Target: target, Nodes: []model.ResourceNode{}, Edges: []model.Edge{}},
		indices: make(map[string]int),
		edges:   make(map[string]struct{}),
		refs:    make(map[string]model.ResourceRef),
	}
}

func (b *Builder) AddNode(node model.ResourceNode) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := nodeKey(node.Ref)
	if _, ok := b.indices[key]; ok {
		return false
	}
	b.indices[key] = len(b.value.Nodes)
	b.value.Nodes = append(b.value.Nodes, node)
	if node.Ref.UID != "" {
		b.refs[node.Ref.UID] = node.Ref
	}
	return true
}

func (b *Builder) Has(ref model.ResourceRef) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.indices[nodeKey(ref)]
	return ok
}

func (b *Builder) AddEdge(edge model.Edge) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if edge.From == nil && edge.FromUID != "" {
		if ref, ok := b.refs[edge.FromUID]; ok {
			copy := ref
			edge.From = &copy
		}
	}
	if edge.To == nil && edge.ToUID != "" {
		if ref, ok := b.refs[edge.ToUID]; ok {
			copy := ref
			edge.To = &copy
		}
	}
	fromKey, toKey := edge.FromUID, edge.ToUID
	if edge.From != nil {
		fromKey = nodeKey(*edge.From)
	}
	if edge.To != nil {
		toKey = nodeKey(*edge.To)
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s", fromKey, toKey, edge.Type, edge.Detail)
	if _, ok := b.edges[key]; ok {
		return
	}
	b.edges[key] = struct{}{}
	b.value.Edges = append(b.value.Edges, edge)
}

func (b *Builder) Value() model.Graph {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.value
}

func nodeKey(ref model.ResourceRef) string {
	if ref.UID != "" {
		return "uid:" + ref.UID
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s", ref.APIVersion, ref.Kind, ref.Namespace, ref.Name)
}
