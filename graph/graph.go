// Package graph is the code graph the analysis packages work on: the nodes
// and edges that extractors.Extract returns, joined into one undirected graph
// with at most one edge between any two nodes.
//
//	result, _ := extractors.Extract(files, extractors.Options{Root: root})
//	g := graph.FromExtract(result.Nodes, result.Edges)
//
// Nodes and edges keep the order they were added in, so everything computed
// from a graph is reproducible.
package graph

import (
	"sort"

	"github.com/notrobot1/funcscan2/extractors/base"
)

// Node is a node of the graph, with the attributes the extractor gave it.
type Node = base.Node

// Edge is the one link between two nodes. The graph is undirected, but an
// edge remembers which way the relation it records points.
type Edge struct {
	// Source and Target give the direction of the relation: Source calls,
	// imports or contains Target.
	Source, Target string

	Relation   string
	Context    string
	Confidence string // "EXTRACTED", "INFERRED" or "AMBIGUOUS"
	// ConfidenceScore is 0 when the extractor gave none.
	ConfidenceScore float64
	Weight          float64

	SourceFile     string
	SourceLocation string
	Origin         string

	// ReverseImport marks an import edge that stands for two: Target imports
	// Source as well. Only one edge fits between two files, so the second
	// import survives as this mark.
	ReverseImport bool
}

// EdgeRef is an edge seen from one of its ends: U is the node it was reached
// from, V the node at the other end. U and V say nothing about the direction
// of the relation; Edge.Source and Edge.Target do.
type EdgeRef struct {
	U, V string
	*Edge
}

// Graph is an undirected graph with at most one edge per pair of nodes. A
// node may have an edge to itself.
type Graph struct {
	ids    []string
	nodes  []*Node
	index  map[string]int
	order  [][]int         // neighbours of each node, in the order their edges were added
	edgeTo []map[int]*Edge // the same neighbours, by index; both ends share the *Edge
	edges  int
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{index: map[string]int{}}
}

// AddNode adds a node with just an id and a label, or relabels the node that
// already has the id.
func (g *Graph) AddNode(id, label string) {
	if i, ok := g.index[id]; ok {
		g.nodes[i].Label = label
		return
	}
	g.SetNode(&Node{ID: id, Label: label})
}

// SetNode adds the node, or replaces the attributes of the node that already
// has its id. The graph keeps the pointer.
func (g *Graph) SetNode(n *Node) {
	if i, ok := g.index[n.ID]; ok {
		g.nodes[i] = n
		return
	}
	g.index[n.ID] = len(g.ids)
	g.ids = append(g.ids, n.ID)
	g.nodes = append(g.nodes, n)
	g.order = append(g.order, nil)
	g.edgeTo = append(g.edgeTo, map[int]*Edge{})
}

// AddEdge links u and v with an edge that carries only a weight and a
// confidence, pointing from u to v. See SetEdge.
func (g *Graph) AddEdge(u, v string, weight float64, confidence string) {
	g.SetEdge(&Edge{Source: u, Target: v, Weight: weight, Confidence: confidence})
}

// SetEdge links e.Source and e.Target, adding either node if it is missing.
// An edge already between the two, in either direction, is replaced. The
// graph keeps the pointer.
func (g *Graph) SetEdge(e *Edge) {
	for _, id := range [2]string{e.Source, e.Target} {
		if _, ok := g.index[id]; !ok {
			g.AddNode(id, "")
		}
	}
	i, j := g.index[e.Source], g.index[e.Target]
	if _, exists := g.edgeTo[i][j]; !exists {
		g.edges++
		g.order[i] = append(g.order[i], j)
		if i != j {
			g.order[j] = append(g.order[j], i)
		}
	}
	g.edgeTo[i][j], g.edgeTo[j][i] = e, e
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

// Node returns the node, nil if the graph does not hold it.
func (g *Graph) Node(id string) *Node {
	if i, ok := g.index[id]; ok {
		return g.nodes[i]
	}
	return nil
}

// Label is the node's label, "" if it has none or is not in the graph.
func (g *Graph) Label(id string) string {
	if n := g.Node(id); n != nil {
		return n.Label
	}
	return ""
}

// Nodes lists the node ids in the order the nodes were added.
func (g *Graph) Nodes() []string {
	return append([]string(nil), g.ids...)
}

// SortedNodes lists the node ids in alphabetical order.
func (g *Graph) SortedNodes() []string {
	ids := g.Nodes()
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
	d := len(g.order[i])
	if _, loop := g.edgeTo[i][i]; loop {
		d++
	}
	return d
}

// Neighbors lists the nodes the node has an edge to, in the order the edges
// were added; the node itself is among them when it has an edge to itself.
func (g *Graph) Neighbors(id string) []string {
	i, ok := g.index[id]
	if !ok {
		return nil
	}
	out := make([]string, len(g.order[i]))
	for k, j := range g.order[i] {
		out[k] = g.ids[j]
	}
	return out
}

// Edge returns the edge between u and v, nil if there is none.
func (g *Graph) Edge(u, v string) *Edge {
	i, ok := g.index[u]
	j, ok2 := g.index[v]
	if !ok || !ok2 {
		return nil
	}
	return g.edgeTo[i][j]
}

// Edges lists every edge once: node by node in the order the nodes were
// added, and for each node its edges to the nodes not yet gone through, in
// the order those edges were added.
func (g *Graph) Edges() []EdgeRef {
	out := make([]EdgeRef, 0, g.edges)
	done := make([]bool, len(g.ids))
	for i, id := range g.ids {
		for _, j := range g.order[i] {
			if !done[j] {
				out = append(out, EdgeRef{id, g.ids[j], g.edgeTo[i][j]})
			}
		}
		done[i] = true
	}
	return out
}

// EdgesOf lists the edges at the node, each with U set to the node, in the
// order they were added.
func (g *Graph) EdgesOf(id string) []EdgeRef {
	i, ok := g.index[id]
	if !ok {
		return nil
	}
	out := make([]EdgeRef, len(g.order[i]))
	for k, j := range g.order[i] {
		out[k] = EdgeRef{id, g.ids[j], g.edgeTo[i][j]}
	}
	return out
}

// Subgraph returns a new graph of the given nodes that are in g and the edges
// of g between them, both in the order they have in g. The nodes are shared
// with g; the edges are copies.
func (g *Graph) Subgraph(nodes []string) *Graph {
	keep := make([]bool, len(g.ids))
	for _, id := range nodes {
		if i, ok := g.index[id]; ok {
			keep[i] = true
		}
	}
	sub := New()
	for i, n := range g.nodes {
		if keep[i] {
			sub.SetNode(n)
		}
	}
	for _, ref := range g.Edges() {
		if keep[g.index[ref.U]] && keep[g.index[ref.V]] {
			e := *ref.Edge
			// Keep the adjacency order of g rather than that of Source/Target.
			sub.link(ref.U, ref.V, &e)
		}
	}
	return sub
}

// link adds e between u and v, recording u's end first.
func (g *Graph) link(u, v string, e *Edge) {
	i, j := g.index[u], g.index[v]
	g.edges++
	g.order[i] = append(g.order[i], j)
	if i != j {
		g.order[j] = append(g.order[j], i)
	}
	g.edgeTo[i][j], g.edgeTo[j][i] = e, e
}

// EdgesWithin counts the edges between two different nodes of the set.
func (g *Graph) EdgesWithin(nodes []string) int {
	keep := make(map[int]bool, len(nodes))
	for _, id := range nodes {
		if i, ok := g.index[id]; ok {
			keep[i] = true
		}
	}
	count := 0
	for i := range keep {
		for _, j := range g.order[i] {
			if j > i && keep[j] {
				count++
			}
		}
	}
	return count
}

// Index numbers the nodes 0..n-1 in the order they were added and returns,
// for each, the numbers of its neighbours in the order their edges were
// added. Algorithms that run over the whole graph many times work on this
// form.
func (g *Graph) Index() (ids []string, neighbours [][]int) {
	return g.ids, g.order
}
