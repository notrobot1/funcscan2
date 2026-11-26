package cluster

import (
	"path/filepath"
	"slices"
	"sort"
)

const (
	// A community holding more than this share of the graph is split.
	maxCommunityFraction = 0.25
	// ...but never one of fewer nodes than this.
	minSplitSize = 10
	// A community this loosely knit is split again...
	cohesionSplitThreshold = 0.05
	// ...when it has at least this many nodes.
	cohesionSplitMinSize = 50
)

// Options tune Cluster. The zero value gives the defaults.
type Options struct {
	// Resolution above 1 gives more, smaller communities; below 1, fewer and
	// larger. Zero means 1.
	Resolution float64

	// ExcludeHubsPercentile, when above zero (up to 100), keeps the nodes
	// whose degree is above that percentile out of the partitioning, and
	// afterwards puts each into the community most of its neighbours ended up
	// in. A utility that everything touches otherwise drags unrelated
	// subsystems into one community. A node whose only neighbours are such
	// hubs follows them, by the same vote.
	ExcludeHubsPercentile float64

	// AmbiguousScale multiplies the weight of AMBIGUOUS edges before
	// partitioning, so that low-confidence links do not glue independent
	// clusters together. Nil means 0.5; 1 treats them like any other edge; 0
	// makes them count for nothing. Only the partitioning is affected: the
	// graph itself is not changed. See Scale.
	AmbiguousScale *float64

	// CacheDir, when set, is a directory in which the result is kept, keyed by
	// a fingerprint of the graph and of the options. A later call with the
	// same graph and options returns the stored result without partitioning
	// again. A cache that is missing, damaged or cannot be written is simply
	// not used.
	CacheDir string

	// Partition replaces the built-in community detection (Louvain). It gets
	// a graph with no isolated nodes and returns a community number for each
	// of its nodes; a node it leaves out becomes a community of its own. It
	// must give the same answer for the same graph, or neither the result nor
	// the cache will be stable. The cache fingerprint cannot see which
	// function is set here: use a separate CacheDir per partitioner.
	Partition func(g *Graph, resolution float64) map[string]int
}

// Scale returns a pointer to v, for Options.AmbiguousScale.
func Scale(v float64) *float64 { return &v }

// Cluster divides the graph into communities and returns the node ids of
// each, sorted, keyed by community number. Community 0 is the largest; equal
// sizes are ordered by their members, so the same grouping always gets the
// same numbers.
//
// A node with no edges is a community of its own. A community holding more
// than a quarter of the graph is partitioned again on its own, and so is a
// large community whose nodes are barely connected to each other: both are
// usually several subsystems held together by a few nodes that link to
// everything.
func Cluster(g *Graph, opts Options) map[int][]string {
	if g.NumNodes() == 0 {
		return map[int][]string{}
	}
	if g.NumEdges() == 0 {
		out := make(map[int][]string, g.NumNodes())
		for i, id := range g.Nodes() {
			out[i] = []string{id}
		}
		return out
	}
	resolution := opts.Resolution
	if resolution == 0 {
		resolution = 1
	}
	ambiguousScale := 0.5
	if opts.AmbiguousScale != nil {
		ambiguousScale = *opts.AmbiguousScale
	}
	partition := opts.Partition
	if partition == nil {
		partition = Louvain
	}

	cachePath, fingerprint := "", ""
	if opts.CacheDir != "" {
		cachePath = filepath.Join(opts.CacheDir, partitionCacheName)
		fingerprint = topologyHash(g, resolution, opts.ExcludeHubsPercentile, ambiguousScale)
		if cached := loadCachedPartition(cachePath, fingerprint); cached != nil {
			return cached
		}
	}

	if ambiguousScale != 1 {
		g = g.scaled("AMBIGUOUS", ambiguousScale)
	}

	// Hubs are chosen by their degree in the whole graph, before anything is
	// taken out of it.
	hubs := map[string]bool{}
	if opts.ExcludeHubsPercentile > 0 {
		degrees := make([]int, 0, g.NumNodes())
		for i := range g.ids {
			degrees = append(degrees, g.degree(i))
		}
		sort.Ints(degrees)
		at := max(0, int(float64(len(degrees))*opts.ExcludeHubsPercentile/100)-1)
		for i, id := range g.ids {
			if g.degree(i) > degrees[at] {
				hubs[id] = true
			}
		}
	}

	// Nodes without edges are set aside: they are communities of their own.
	var isolates, connected, stranded []string
	for _, id := range g.Nodes() {
		switch {
		case hubs[id]:
		case g.Degree(id) == 0:
			isolates = append(isolates, id)
		case len(hubs) > 0 && onlyHubNeighbours(g, id, hubs):
			// With its hubs gone this node would be alone in the partitioned
			// graph, cut off from the only nodes it connects to. It is placed
			// with them afterwards instead.
			stranded = append(stranded, id)
		default:
			connected = append(connected, id)
		}
	}

	raw := map[int][]string{}
	next := 0
	if len(connected) > 0 {
		sub := g.Subgraph(connected)
		assigned := partition(sub, resolution)
		for _, id := range connected {
			if cid, ok := assigned[id]; ok {
				raw[cid] = append(raw[cid], id)
				next = max(next, cid+1)
			}
		}
		for _, id := range connected {
			if _, ok := assigned[id]; !ok {
				raw[next] = []string{id}
				next++
			}
		}
	}
	for _, id := range isolates {
		raw[next] = []string{id}
		next++
	}

	if len(hubs) > 0 {
		communityOf := map[string]int{}
		for cid, nodes := range raw {
			for _, id := range nodes {
				communityOf[id] = cid
			}
		}
		// vote returns the community most of the node's placed neighbours are
		// in; the lowest number wins a tie.
		vote := func(id string) (int, bool) {
			votes := map[int]int{}
			for _, neighbour := range g.Neighbors(id) {
				if cid, placed := communityOf[neighbour]; placed && neighbour != id {
					votes[cid]++
				}
			}
			best, found := 0, false
			for cid, n := range votes {
				if !found || n > votes[best] || (n == votes[best] && cid < best) {
					best, found = cid, true
				}
			}
			return best, found
		}
		place := func(id string) {
			cid, ok := vote(id)
			if !ok {
				cid, next = next, next+1
			}
			raw[cid] = append(raw[cid], id)
			communityOf[id] = cid
		}
		for _, id := range sortedSet(hubs) {
			place(id)
		}
		// Every neighbour of a stranded node is a hub, and every hub has a
		// community by now.
		for _, id := range stranded {
			place(id)
		}
	}

	split := func(nodes []string) [][]string { return splitCommunity(g, nodes, partition) }

	maxSize := max(minSplitSize, int(float64(g.NumNodes())*maxCommunityFraction))
	var communities [][]string
	for _, cid := range sortedInts(raw) {
		if nodes := raw[cid]; len(nodes) > maxSize {
			communities = append(communities, split(nodes)...)
		} else {
			communities = append(communities, nodes)
		}
	}

	// A large community with almost no edges inside it is what a node linked
	// to everything (a README, a registry) leaves behind.
	var final [][]string
	for _, nodes := range communities {
		if len(nodes) >= cohesionSplitMinSize && CohesionScore(g, nodes) < cohesionSplitThreshold {
			if parts := split(nodes); len(parts) > 1 {
				final = append(final, parts...)
				continue
			}
		}
		final = append(final, nodes)
	}

	// Largest first, equal sizes by their members: a total order, so the same
	// grouping always gets the same numbers.
	for _, nodes := range final {
		sort.Strings(nodes)
	}
	sort.Slice(final, func(i, j int) bool {
		if len(final[i]) != len(final[j]) {
			return len(final[i]) > len(final[j])
		}
		return slices.Compare(final[i], final[j]) < 0
	})
	result := make(map[int][]string, len(final))
	for i, nodes := range final {
		result[i] = nodes
	}

	if cachePath != "" {
		storeCachedPartition(cachePath, fingerprint, result)
	}
	return result
}

// onlyHubNeighbours reports whether the node has neighbours other than itself
// and all of them are hubs.
func onlyHubNeighbours(g *Graph, id string, hubs map[string]bool) bool {
	others := 0
	for _, neighbour := range g.Neighbors(id) {
		if neighbour == id {
			continue
		}
		if !hubs[neighbour] {
			return false
		}
		others++
	}
	return others > 0
}

// scaled returns the graph with the weight of every edge of the given
// confidence multiplied by factor; the graph itself when it has none.
func (g *Graph) scaled(confidence string, factor float64) *Graph {
	found := false
	for i := range g.adj {
		for _, e := range g.adj[i] {
			if e.confidence == confidence {
				found = true
			}
		}
	}
	if !found {
		return g
	}
	out := g.Subgraph(g.ids)
	for i := range out.adj {
		for j, e := range out.adj[i] {
			if j >= i && e.confidence == confidence {
				e.weight *= factor
			}
		}
	}
	return out
}

// splitCommunity partitions a community on its own. It returns the community
// whole when it does not divide, or when the partitioner fails.
func splitCommunity(g *Graph, nodes []string, partition func(*Graph, float64) map[string]int) (parts [][]string) {
	whole := append([]string(nil), nodes...)
	sort.Strings(whole)
	sub := g.Subgraph(nodes)
	if sub.NumEdges() == 0 {
		for _, id := range whole {
			parts = append(parts, []string{id})
		}
		return parts
	}
	defer func() {
		if recover() != nil {
			parts = [][]string{whole}
		}
	}()
	assigned := partition(sub, 1)
	byCommunity := map[int][]string{}
	next := 0
	for _, id := range whole {
		if cid, ok := assigned[id]; ok {
			byCommunity[cid] = append(byCommunity[cid], id)
			next = max(next, cid+1)
		}
	}
	for _, id := range whole {
		if _, ok := assigned[id]; !ok {
			byCommunity[next] = []string{id}
			next++
		}
	}
	if len(byCommunity) <= 1 {
		return [][]string{whole}
	}
	for _, cid := range sortedInts(byCommunity) {
		parts = append(parts, byCommunity[cid])
	}
	return parts
}

// CohesionScore is how densely the nodes are connected to each other: the
// edges among them as a share of the edges there could be, from 0 to 1. An
// edge from a node to itself does not count. One node, or none, scores 1.
func CohesionScore(g *Graph, nodes []string) float64 {
	n := len(nodes)
	if n <= 1 {
		return 1
	}
	possible := float64(n) * float64(n-1) / 2
	return min(1, float64(g.internalEdges(nodes))/possible)
}

// ScoreAll is the CohesionScore of every community.
func ScoreAll(g *Graph, communities map[int][]string) map[int]float64 {
	scores := make(map[int]float64, len(communities))
	for cid, nodes := range communities {
		scores[cid] = CohesionScore(g, nodes)
	}
	return scores
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func sortedInts[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
