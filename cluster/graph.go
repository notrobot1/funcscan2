// Package cluster groups the nodes of a code graph into communities: sets of
// nodes that are tied to each other more tightly than to the rest of the
// graph.
//
//	result, _ := extractors.Extract(files, extractors.Options{Root: root})
//	g := cluster.FromExtract(result.Nodes, result.Edges)
//	communities := cluster.Cluster(g, cluster.Options{})
//	labels := cluster.LabelByHub(g, communities)
//
// Community 0 is the largest. For a given graph and options the result is
// always the same, whatever order the nodes and edges were added in.
package cluster

import (
	"sort"

	"github.com/notrobot1/funcscan2/extractors/base"
)

// edge is what clustering needs to know about a link between two nodes.
type edge struct {
	weight     float64
	confidence string
}

// Graph is an undirected graph with at most one edge per pair of nodes. A
// node may have an edge to itself.
type Graph struct {
	ids    []string
	labels []string
	index  map[string]int
	adj    []map[int]*edge // both endpoints share the *edge
	edges  int
}

// NewGraph returns an empty graph.
func NewGraph() *Graph {
	return &Graph{index: map[string]int{}}
}

// AddNode adds a node, or replaces the label of one that is already there.
func (g *Graph) AddNode(id, label string) {
	if i, ok := g.index[id]; ok {
		g.labels[i] = label
		return
	}
	g.index[id] = len(g.ids)
	g.ids = append(g.ids, id)
	g.labels = append(g.labels, label)
	g.adj = append(g.adj, map[int]*edge{})
}

// AddEdge links two nodes, adding either one if it is missing. An edge
// between the same pair, in either direction, is replaced: the last one added
// stands. confidence is "EXTRACTED", "INFERRED" or "AMBIGUOUS"; only the last
// is treated specially (see Options.AmbiguousScale).
func (g *Graph) AddEdge(u, v string, weight float64, confidence string) {
	for _, id := range [2]string{u, v} {
		if _, ok := g.index[id]; !ok {
			g.AddNode(id, "")
		}
	}
	i, j := g.index[u], g.index[v]
	if _, exists := g.adj[i][j]; !exists {
		g.edges++
	}
	e := &edge{weight: weight, confidence: confidence}
	g.adj[i][j], g.adj[j][i] = e, e
}

// importRelations are the edges whose target may legitimately lie outside the
// graph: a standard-library or third-party module.
var importRelations = map[string]bool{"imports": true, "imports_from": true, "re_exports": true}

// FromExtract builds the graph to cluster from the nodes and edges that
// extractors.Extract returns.
//
// Direction is dropped, and several edges between one pair of nodes become
// one. An edge to an id no node carries is left out — except an import: its
// target is an external module, which becomes a node of its own, so that the
// files importing the same module are tied together through it. An import of
// a file by itself is left out too.
func FromExtract(nodes []*base.Node, edges []*base.Edge) *Graph {
	g := NewGraph()
	for _, n := range nodes {
		if n.ID != "" {
			g.AddNode(n.ID, n.Label)
		}
	}
	for _, e := range edges {
		if e.Source == "" || e.Target == "" || e.Relation == "" {
			continue
		}
		if !g.Has(e.Source) {
			continue
		}
		isImport := importRelations[e.Relation]
		if !g.Has(e.Target) {
			if !isImport {
				continue
			}
			g.AddNode(e.Target, e.Target)
		}
		if e.Source == e.Target && isImport {
			continue
		}
		weight := e.Weight
		if weight == 0 {
			weight = 1
		}
		g.AddEdge(e.Source, e.Target, weight, e.Confidence)
	}
	return g
}

// NumNodes is the number of nodes.
func (g *Graph) NumNodes() int { return len(g.ids) }

// NumEdges is the number of edges; an edge from a node to itself counts once.
func (g *Graph) NumEdges() int { return g.edges }

// Has reports whether the graph holds the node.
func (g *Graph) Has(id string) bool {
	_, ok := g.index[id]
	return ok
}

// Label is the node's label, "" if it has none or is not in the graph.
func (g *Graph) Label(id string) string {
	if i, ok := g.index[id]; ok {
		return g.labels[i]
	}
	return ""
}

// Nodes lists the node ids, sorted.
func (g *Graph) Nodes() []string {
	ids := append([]string(nil), g.ids...)
	sort.Strings(ids)
	return ids
}

// Degree is the number of edges at the node. An edge from the node to itself
// counts twice, having both its ends there.
func (g *Graph) Degree(id string) int {
	i, ok := g.index[id]
	if !ok {
		return 0
	}
	return g.degree(i)
}

func (g *Graph) degree(i int) int {
	d := len(g.adj[i])
	if _, loop := g.adj[i][i]; loop {
		d++
	}
	return d
}

// Neighbors lists the nodes the node has an edge to, sorted; the node itself
// is among them when it has an edge to itself.
func (g *Graph) Neighbors(id string) []string {
	i, ok := g.index[id]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(g.adj[i]))
	for j := range g.adj[i] {
		out = append(out, g.ids[j])
	}
	sort.Strings(out)
	return out
}

// Subgraph returns a new graph holding the given nodes that are in g, and the
// edges of g between them.
func (g *Graph) Subgraph(nodes []string) *Graph {
	sub := NewGraph()
	keep := make(map[int]bool, len(nodes))
	for _, id := range nodes {
		if i, ok := g.index[id]; ok && !keep[i] {
			keep[i] = true
			sub.AddNode(id, g.labels[i])
		}
	}
	for i := range keep {
		for j, e := range g.adj[i] {
			if j >= i && keep[j] {
				sub.AddEdge(g.ids[i], g.ids[j], e.weight, e.confidence)
			}
		}
	}
	return sub
}

// internalEdges counts the edges between two different nodes of the set.
func (g *Graph) internalEdges(nodes []string) int {
	keep := make(map[int]bool, len(nodes))
	for _, id := range nodes {
		if i, ok := g.index[id]; ok {
			keep[i] = true
		}
	}
	count := 0
	for i := range keep {
		for j := range g.adj[i] {
			if j > i && keep[j] {
				count++
			}
		}
	}
	return count
}
