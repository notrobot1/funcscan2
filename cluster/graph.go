// Package cluster groups the nodes of a code graph into communities: sets of
// nodes that are tied to each other more tightly than to the rest of the
// graph.
//
//	result, _ := extractors.Extract(files, extractors.Options{Root: root})
//	g := graph.FromExtract(result.Nodes, result.Edges)
//	communities := cluster.Cluster(g, cluster.Options{})
//	labels := cluster.LabelByHub(g, communities)
//
// Community 0 is the largest. For a given graph and options the result is
// always the same, whatever order the nodes and edges were added in.
package cluster

import (
	"github.com/notrobot1/funcscan2/extractors/base"
	"github.com/notrobot1/funcscan2/graph"
)

// Graph is the graph being clustered; it lives in package graph.
type Graph = graph.Graph

// NewGraph returns an empty graph. Same as graph.New.
func NewGraph() *Graph { return graph.New() }

// FromExtract builds the graph from what extractors.Extract returns. Same as
// graph.FromExtract.
func FromExtract(nodes []*base.Node, edges []*base.Edge) *Graph {
	return graph.FromExtract(nodes, edges)
}
