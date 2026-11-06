package view

import (
	"fmt"
	"io"
	"strings"

	"github.com/Fanatic-zer0/kq/internal/model"
)

func RenderDetail(w io.Writer, result *model.QueryResult, format string, opts DetailOptions) error {
	if !opts.ShowWarnings && len(result.Warnings) > 0 {
		copy := *result
		copy.Warnings = nil
		result = &copy
	}
	switch format {
	case "json":
		return encodeJSON(w, result)
	case "yaml":
		return encodeYAML(w, result)
	case "tree":
		return renderDetailTree(w, result, opts)
	default:
		return RenderDetailText(w, result, opts)
	}
}

func renderDetailTree(w io.Writer, result *model.QueryResult, opts DetailOptions) error {
	target, ok := nodeByUID(result.Graph, result.Target.UID)
	if !ok {
		target = model.ResourceNode{Ref: result.Target}
	}
	chain := ownerChain(result.Graph, target)
	writeTreeNode(w, chain, 0, result)
	if opts.ShowWarnings {
		return renderWarningsSection(w, result.Warnings)
	}
	return nil
}

func writeTreeNode(w io.Writer, chain []model.ResourceNode, depth int, result *model.QueryResult) {
	node := chain[depth]
	prefix := ""
	if depth > 0 {
		prefix = strings.Repeat("    ", depth-1) + "└── "
	}
	fmt.Fprintf(w, "%s%s/%s\n", prefix, cyan(node.Ref.Kind), node.Ref.Name)
	if depth+1 < len(chain) {
		writeTreeNode(w, chain, depth+1, result)
		return
	}
	childPrefix := strings.Repeat("    ", depth+1)
	for _, related := range result.Graph.Nodes {
		if related.Ref.UID == node.Ref.UID || isAncestor(chain, related.Ref.UID) {
			continue
		}
		fmt.Fprintf(w, "%s├── %s/%s\n", childPrefix, cyan(related.Ref.Kind), related.Ref.Name)
	}
}

func isAncestor(chain []model.ResourceNode, uid string) bool {
	for _, node := range chain {
		if node.Ref.UID == uid {
			return true
		}
	}
	return false
}
