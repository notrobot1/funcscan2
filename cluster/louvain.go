package cluster

import "sort"

const (
	// A level that improves modularity by no more than this ends the search.
	louvainThreshold = 1e-4
	// Levels of aggregation at most; bounds the work on large sparse graphs.
	louvainMaxLevels = 10
	// A move must gain more than this, so rounding noise cannot keep two nodes
	// swapping places forever.
	louvainEpsilon = 1e-12
)

type halfEdge struct {
	to     int
	weight float64
}

// weighted is the graph Louvain works on: nodes are 0..n-1, adj holds each
// edge at both its ends (never a node's edge to itself — that weight is in
// self).
type weighted struct {
	adj  [][]halfEdge
	self []float64
}

func (w *weighted) degree(i int) float64 {
	d := 2 * w.self[i]
	for _, e := range w.adj[i] {
		d += e.weight
	}
	return d
}

// Louvain partitions the graph by the Louvain method: every node starts
// alone, nodes are moved to the neighbouring community that raises modularity
// the most until no move helps, the communities found are merged into single
// nodes, and the same is done again on the smaller graph.
//
// resolution above 1 favours more, smaller communities; below 1, fewer and
// larger. The result maps each node id to a community number. Nodes are
// visited in the order of their ids, with no randomness, so the same graph
// always gives the same partition.
func Louvain(g *Graph, resolution float64) map[string]int {
	ids := g.Nodes()
	position := make(map[int]int, len(ids)) // index in g -> index in ids
	for pos, id := range ids {
		position[g.index[id]] = pos
	}
	w := &weighted{adj: make([][]halfEdge, len(ids)), self: make([]float64, len(ids))}
	total := 0.0
	for pos, id := range ids {
		for j, e := range g.adj[g.index[id]] {
			other := position[j]
			switch {
			case other == pos:
				w.self[pos] = e.weight
				total += e.weight
			default:
				w.adj[pos] = append(w.adj[pos], halfEdge{other, e.weight})
				if other > pos {
					total += e.weight
				}
			}
		}
		sort.Slice(w.adj[pos], func(a, b int) bool { return w.adj[pos][a].to < w.adj[pos][b].to })
	}

	membership := make([]int, len(ids)) // node -> its community so far
	for i := range membership {
		membership[i] = i
	}
	if total > 0 {
		quality := modularity(w, membership, len(ids), total, resolution)
		for level := 0; level < louvainMaxLevels; level++ {
			community, count, moved := moveNodes(w, total, resolution)
			if !moved {
				break
			}
			for i, c := range membership {
				membership[i] = community[c]
			}
			improved := modularity(w, community, count, total, resolution)
			if improved-quality <= louvainThreshold {
				break
			}
			quality = improved
			w = aggregate(w, community, count)
		}
	}

	// Number the communities by their first node, so the numbering does not
	// depend on how the search got there.
	renumber := map[int]int{}
	out := make(map[string]int, len(ids))
	for pos, id := range ids {
		c, ok := renumber[membership[pos]]
		if !ok {
			c = len(renumber)
			renumber[membership[pos]] = c
		}
		out[id] = c
	}
	return out
}

// moveNodes is one level of local moving. It returns each node's community,
// numbered from 0, how many there are, and whether any node moved at all.
func moveNodes(w *weighted, total, resolution float64) (community []int, count int, moved bool) {
	n := len(w.adj)
	community = make([]int, n)
	degree := make([]float64, n)
	communityDegree := make([]float64, n)
	for i := range community {
		community[i] = i
		degree[i] = w.degree(i)
		communityDegree[i] = degree[i]
	}
	weightTo := make([]float64, n) // weight from the current node into each community
	var touched []int
	for {
		moves := 0
		for u := 0; u < n; u++ {
			touched = touched[:0]
			for _, e := range w.adj[u] {
				c := community[e.to]
				if weightTo[c] == 0 {
					touched = append(touched, c)
				}
				weightTo[c] += e.weight
			}
			current := community[u]
			communityDegree[current] -= degree[u]
			scale := resolution * degree[u] / (2 * total * total)
			leaving := -weightTo[current]/total + scale*communityDegree[current]
			best, bestGain := current, 0.0
			for _, c := range touched {
				gain := leaving + weightTo[c]/total - scale*communityDegree[c]
				if gain > bestGain+louvainEpsilon {
					best, bestGain = c, gain
				}
			}
			communityDegree[best] += degree[u]
			if best != current {
				community[u] = best
				moves++
			}
			for _, c := range touched {
				weightTo[c] = 0
			}
		}
		if moves == 0 {
			break
		}
		moved = true
	}
	renumber := make([]int, n)
	for i := range renumber {
		renumber[i] = -1
	}
	for i, c := range community {
		if renumber[c] < 0 {
			renumber[c] = count
			count++
		}
		community[i] = renumber[c]
	}
	return community, count, moved
}

// aggregate merges each community into one node. The weight inside a
// community becomes that node's weight to itself; the weight between two
// communities becomes one edge.
func aggregate(w *weighted, community []int, count int) *weighted {
	merged := &weighted{adj: make([][]halfEdge, count), self: make([]float64, count)}
	between := make([]map[int]float64, count)
	for i := range between {
		between[i] = map[int]float64{}
	}
	for i, edges := range w.adj {
		c := community[i]
		merged.self[c] += w.self[i]
		for _, e := range edges {
			d := community[e.to]
			switch {
			case c != d:
				between[c][d] += e.weight
			case e.to > i:
				merged.self[c] += e.weight
			}
		}
	}
	for c, weights := range between {
		for d, weight := range weights {
			merged.adj[c] = append(merged.adj[c], halfEdge{d, weight})
		}
		sort.Slice(merged.adj[c], func(a, b int) bool { return merged.adj[c][a].to < merged.adj[c][b].to })
	}
	return merged
}

// modularity scores a partition: the share of edge weight that falls inside
// communities, less the share expected if the edges were placed at random
// between nodes of the same degrees.
func modularity(w *weighted, community []int, count int, total, resolution float64) float64 {
	inside := make([]float64, count)
	degree := make([]float64, count)
	for i, edges := range w.adj {
		c := community[i]
		inside[c] += w.self[i]
		degree[c] += w.degree(i)
		for _, e := range edges {
			if e.to > i && community[e.to] == c {
				inside[c] += e.weight
			}
		}
	}
	q := 0.0
	for c := range inside {
		share := degree[c] / (2 * total)
		q += inside[c]/total - resolution*share*share
	}
	return q
}

// Modularity scores how well the communities divide the graph, from about
// -0.5 to 1; higher means more of the edges fall inside communities than
// chance would put there. Nodes missing from every community count as
// communities of one.
func Modularity(g *Graph, communities map[int][]string) float64 {
	partition := map[string]int{}
	next := 0
	for cid, nodes := range communities {
		for _, id := range nodes {
			partition[id] = cid
		}
		if cid >= next {
			next = cid + 1
		}
	}
	inside := map[int]float64{}
	degree := map[int]float64{}
	total := 0.0
	for i, id := range g.ids {
		c, ok := partition[id]
		if !ok {
			c, next = next, next+1
			partition[id] = c
		}
		for j, e := range g.adj[i] {
			switch {
			case j == i:
				total += e.weight
				inside[c] += e.weight
				degree[c] += 2 * e.weight
			default:
				degree[c] += e.weight
				if j > i {
					total += e.weight
				}
			}
		}
	}
	if total <= 0 {
		return 0
	}
	for i, id := range g.ids {
		for j, e := range g.adj[i] {
			if j > i && partition[g.ids[j]] == partition[id] {
				inside[partition[id]] += e.weight
			}
		}
	}
	q := 0.0
	for c, d := range degree {
		share := d / (2 * total)
		q += inside[c]/total - share*share
	}
	return q
}
