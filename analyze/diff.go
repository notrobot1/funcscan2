package analyze

import (
	"fmt"
	"sort"
	"strings"

	"github.com/notrobot1/funcscan2/graph"
)

// NodeChange is a node that one of two graphs has and the other has not.
type NodeChange struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// EdgeChange is an edge that one of two graphs has and the other has not.
type EdgeChange struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Relation   string `json:"relation"`
	Confidence string `json:"confidence"`
}

// Diff is what changed between two graphs.
type Diff struct {
	NewNodes     []NodeChange `json:"new_nodes"`
	RemovedNodes []NodeChange `json:"removed_nodes"`
	NewEdges     []EdgeChange `json:"new_edges"`
	RemovedEdges []EdgeChange `json:"removed_edges"`
	// Summary reads like "3 new nodes, 5 new edges, 1 node removed", or "no
	// changes".
	Summary string `json:"summary"`
}

// GraphDiff compares two snapshots of a graph.
//
// A node is the same node when it has the same id. An edge is the same edge
// when it joins the same two nodes with the same relation; if its direction
// has turned around, it is reported as one edge removed and one added. All
// four lists are sorted.
func GraphDiff(before, after *graph.Graph) Diff {
	diff := Diff{NewNodes: []NodeChange{}, RemovedNodes: []NodeChange{}, NewEdges: []EdgeChange{}, RemovedEdges: []EdgeChange{}}
	for _, id := range after.SortedNodes() {
		if !before.Has(id) {
			diff.NewNodes = append(diff.NewNodes, NodeChange{id, name(after, id)})
		}
	}
	for _, id := range before.SortedNodes() {
		if !after.Has(id) {
			diff.RemovedNodes = append(diff.RemovedNodes, NodeChange{id, name(before, id)})
		}
	}

	type key struct{ a, b, relation string }
	keyOf := func(e graph.EdgeRef) key {
		return key{min(e.U, e.V), max(e.U, e.V), e.Relation}
	}
	index := func(g *graph.Graph) map[key]graph.EdgeRef {
		out := make(map[key]graph.EdgeRef, g.NumEdges())
		for _, e := range g.Edges() {
			out[keyOf(e)] = e
		}
		return out
	}
	old, new := index(before), index(after)
	changed := func(e graph.EdgeRef, other map[key]graph.EdgeRef) bool {
		twin, kept := other[keyOf(e)]
		return !kept || twin.Source != e.Source || twin.Target != e.Target
	}
	collect := func(g *graph.Graph, other map[key]graph.EdgeRef) []EdgeChange {
		out := []EdgeChange{}
		for _, e := range g.Edges() {
			if changed(e, other) {
				out = append(out, EdgeChange{e.Source, e.Target, e.Relation, e.Confidence})
			}
		}
		sort.Slice(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			if a.Target != b.Target {
				return a.Target < b.Target
			}
			return a.Relation < b.Relation
		})
		return out
	}
	diff.NewEdges = collect(after, old)
	diff.RemovedEdges = collect(before, new)

	var parts []string
	count := func(n int, one, many string) {
		if n == 1 {
			parts = append(parts, "1 "+one)
		} else if n > 1 {
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	count(len(diff.NewNodes), "new node", "new nodes")
	count(len(diff.NewEdges), "new edge", "new edges")
	count(len(diff.RemovedNodes), "node removed", "nodes removed")
	count(len(diff.RemovedEdges), "edge removed", "edges removed")
	diff.Summary = "no changes"
	if len(parts) > 0 {
		diff.Summary = strings.Join(parts, ", ")
	}
	return diff
}
