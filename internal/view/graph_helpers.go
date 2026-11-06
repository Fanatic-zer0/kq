package view

import "github.com/Fanatic-zer0/kq/internal/model"

// Status classifies a lookup failure the way kq_project.md requires: NOT FOUND, NO ACCESS, or a short error.
func Status(warnings []model.QueryWarning, ref model.ResourceRef) string {
	for _, warning := range warnings {
		if warning.Resource.Kind == ref.Kind && warning.Resource.Namespace == ref.Namespace && warning.Resource.Name == ref.Name {
			return classifyWarning(warning.Message)
		}
	}
	return "OK"
}

func classifyWarning(message string) string {
	switch {
	case containsFold(message, "forbidden"):
		return "NO ACCESS"
	case containsFold(message, "not found"):
		return "NOT FOUND"
	default:
		return "ERROR"
	}
}

func containsFold(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	lowerHaystack, lowerNeedle := toLower(haystack), toLower(needle)
	for i := 0; i+len(lowerNeedle) <= len(lowerHaystack); i++ {
		if lowerHaystack[i:i+len(lowerNeedle)] == lowerNeedle {
			return i
		}
	}
	return -1
}

func toLower(value string) string {
	result := make([]byte, len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		result[i] = c
	}
	return string(result)
}

func nodeByUID(graph model.Graph, uid string) (model.ResourceNode, bool) {
	for _, node := range graph.Nodes {
		if node.Ref.UID == uid {
			return node, true
		}
	}
	return model.ResourceNode{}, false
}

// edgeTargets returns nodes reached by edges of relationType starting at fromUID.
func edgeTargets(graph model.Graph, fromUID string, relationType model.RelationType) []model.ResourceNode {
	var matches []model.ResourceNode
	for _, edge := range graph.Edges {
		if edge.Type == relationType && edge.FromUID == fromUID {
			if node, ok := nodeByUID(graph, edge.ToUID); ok {
				matches = append(matches, node)
			}
		}
	}
	return matches
}

// edgeSources returns nodes that point to toUID via relationType (the reverse direction of edgeTargets).
func edgeSources(graph model.Graph, toUID string, relationType model.RelationType) []model.ResourceNode {
	var matches []model.ResourceNode
	for _, edge := range graph.Edges {
		if edge.Type == relationType && edge.ToUID == toUID {
			if node, ok := nodeByUID(graph, edge.FromUID); ok {
				matches = append(matches, node)
			}
		}
	}
	return matches
}

// ownerChain walks RelationOwner edges (child->parent) from the target upward,
// returning nodes ordered from the topmost ancestor down to the target itself.
func ownerChain(graph model.Graph, target model.ResourceNode) []model.ResourceNode {
	chain := []model.ResourceNode{target}
	current := target
	seen := map[string]bool{target.Ref.UID: true}
	for {
		parents := edgeTargets(graph, current.Ref.UID, model.RelationOwner)
		if len(parents) == 0 || seen[parents[0].Ref.UID] {
			break
		}
		current = parents[0]
		seen[current.Ref.UID] = true
		chain = append([]model.ResourceNode{current}, chain...)
	}
	return chain
}

func eventsForUID(graph model.Graph, uid string) []model.EventSummary {
	var matches []model.EventSummary
	for _, event := range graph.Events {
		if event.NodeUID == uid {
			matches = append(matches, event)
		}
	}
	return matches
}

func detailsOf[T any](node model.ResourceNode) (T, bool) {
	value, ok := node.Details.(T)
	return value, ok
}
