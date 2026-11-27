package analyze

import (
	"math"

	"github.com/notrobot1/funcscan2/graph"
)

// Degrees is the number of edges at each node (see graph.Graph.Degree).
func Degrees(g *graph.Graph) map[string]int {
	out := make(map[string]int, g.NumNodes())
	for _, id := range g.Nodes() {
		out[id] = g.Degree(id)
	}
	return out
}

// Betweenness is the betweenness centrality of each node: the share of
// shortest paths between other nodes that pass through it, from 0 to 1. A
// node with a high score is a bridge — much of the graph is connected only
// through it. Edges are taken as unweighted.
//
// The exact score follows the shortest paths from every node, which on a
// large graph is slow. With samples of 2 or more (and fewer than there are
// nodes) only that many starting nodes are used, picked at random from seed,
// and the scores are scaled up to estimate the exact ones. The same seed
// always picks the same nodes. Pass 0 for the exact score.
func Betweenness(g *graph.Graph, samples int, seed uint32) map[string]float64 {
	ids, neighbours := g.Index()
	n := len(ids)
	if samples < 2 || samples >= n {
		samples = 0
	}
	sources := make([]int, 0, n)
	if samples == 0 {
		for i := 0; i < n; i++ {
			sources = append(sources, i)
		}
	} else {
		sources = newMT(seed).sample(n, samples)
	}

	scores := make([]float64, n)
	search := newSearch(n)
	for _, s := range sources {
		search.from(s, neighbours)
		for i := len(search.order) - 1; i >= 0; i-- {
			w := search.order[i]
			coeff := (1 + search.delta[w]) / search.sigma[w]
			for _, v := range search.preds[w] {
				search.delta[v] += search.sigma[v] * coeff
			}
			if w != s {
				scores[w] += search.delta[w]
			}
		}
		search.reset()
	}

	out := make(map[string]float64, n)
	targets := float64(n - 1) // a path through a node cannot end at that node
	sampled := map[int]bool{}
	for _, s := range sources {
		sampled[s] = true
	}
	for i, id := range ids {
		score := scores[i]
		switch {
		case n < 3:
		case samples == 0:
			score /= targets * (targets - 1)
		case sampled[i]:
			// A sampled node cannot lie on a path from itself, so it was
			// counted from one source fewer.
			score /= float64(samples-1) * (targets - 1)
		default:
			score /= float64(samples) * (targets - 1)
		}
		out[id] = score
	}
	return out
}

// EdgeScore is an edge with a score.
type EdgeScore struct {
	U, V  string
	Score float64
}

// EdgeBetweenness is the betweenness centrality of each edge: the share of
// shortest paths between nodes that run along it. The edges come in the order
// of graph.Graph.Edges.
func EdgeBetweenness(g *graph.Graph) []EdgeScore {
	ids, neighbours := g.Index()
	n := len(ids)
	scores := map[[2]int]float64{}
	search := newSearch(n)
	for s := 0; s < n; s++ {
		search.from(s, neighbours)
		for i := len(search.order) - 1; i >= 0; i-- {
			w := search.order[i]
			coeff := (1 + search.delta[w]) / search.sigma[w]
			for _, v := range search.preds[w] {
				c := search.sigma[v] * coeff
				scores[pair(v, w)] += c
				search.delta[v] += c
			}
		}
		search.reset()
	}
	position := make(map[string]int, n)
	for i, id := range ids {
		position[id] = i
	}
	scale := 1.0
	if n > 1 {
		scale = 1 / (float64(n) * float64(n-1))
	}
	edges := g.Edges()
	out := make([]EdgeScore, len(edges))
	for i, e := range edges {
		out[i] = EdgeScore{e.U, e.V, scores[pair(position[e.U], position[e.V])] * scale}
	}
	return out
}

func pair(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}

// search is a breadth-first search that counts shortest paths, with its
// state kept between runs so that a run costs only what it reaches.
type search struct {
	dist  []int
	sigma []float64 // number of shortest paths from the source
	delta []float64
	preds [][]int
	order []int // the nodes reached, nearest first
}

func newSearch(n int) *search {
	s := &search{dist: make([]int, n), sigma: make([]float64, n), delta: make([]float64, n), preds: make([][]int, n)}
	for i := range s.dist {
		s.dist[i] = -1
	}
	return s
}

func (s *search) from(source int, neighbours [][]int) {
	s.dist[source] = 0
	s.sigma[source] = 1
	s.order = append(s.order[:0], source)
	for i := 0; i < len(s.order); i++ {
		v := s.order[i]
		next := s.dist[v] + 1
		for _, w := range neighbours[v] {
			if s.dist[w] < 0 {
				s.dist[w] = next
				s.order = append(s.order, w)
			} else if s.dist[w] != next {
				continue
			}
			s.sigma[w] += s.sigma[v]
			s.preds[w] = append(s.preds[w], v)
		}
	}
}

func (s *search) reset() {
	for _, v := range s.order {
		s.dist[v] = -1
		s.sigma[v], s.delta[v] = 0, 0
		s.preds[v] = s.preds[v][:0]
	}
}

// mt is the Mersenne Twister (MT19937), seeded and sampled the way Python's
// random module does it, so that a sampled Betweenness picks the very nodes
// the original implementation picks for the same seed.
type mt struct {
	state [624]uint32
	next  int
}

func newMT(seed uint32) *mt {
	m := &mt{next: 624}
	m.state[0] = 19650218
	for i := 1; i < 624; i++ {
		m.state[i] = 1812433253*(m.state[i-1]^(m.state[i-1]>>30)) + uint32(i)
	}
	i := 1
	for k := 624; k > 0; k-- {
		m.state[i] = (m.state[i] ^ ((m.state[i-1] ^ (m.state[i-1] >> 30)) * 1664525)) + seed
		if i++; i >= 624 {
			m.state[0], i = m.state[623], 1
		}
	}
	for k := 623; k > 0; k-- {
		m.state[i] = (m.state[i] ^ ((m.state[i-1] ^ (m.state[i-1] >> 30)) * 1566083941)) - uint32(i)
		if i++; i >= 624 {
			m.state[0], i = m.state[623], 1
		}
	}
	m.state[0] = 0x80000000
	return m
}

func (m *mt) uint32() uint32 {
	if m.next >= 624 {
		for i := 0; i < 624; i++ {
			y := (m.state[i] & 0x80000000) | (m.state[(i+1)%624] & 0x7fffffff)
			m.state[i] = m.state[(i+397)%624] ^ (y >> 1)
			if y&1 != 0 {
				m.state[i] ^= 0x9908b0df
			}
		}
		m.next = 0
	}
	y := m.state[m.next]
	m.next++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	return y ^ (y >> 18)
}

// below returns a uniform number in [0, n).
func (m *mt) below(n int) int {
	bits := 0
	for v := n; v > 0; v >>= 1 {
		bits++
	}
	for {
		if r := int(m.uint32() >> (32 - bits)); r < n {
			return r
		}
	}
}

// sample picks k different numbers out of 0..n-1.
func (m *mt) sample(n, k int) []int {
	out := make([]int, k)
	limit := 21
	if k > 5 {
		limit += int(math.Pow(4, math.Ceil(math.Log(float64(k*3))/math.Log(4))))
	}
	if n <= limit {
		pool := make([]int, n)
		for i := range pool {
			pool[i] = i
		}
		for i := 0; i < k; i++ {
			j := m.below(n - i)
			out[i] = pool[j]
			pool[j] = pool[n-i-1]
		}
		return out
	}
	taken := make(map[int]bool, k)
	for i := 0; i < k; i++ {
		j := m.below(n)
		for taken[j] {
			j = m.below(n)
		}
		taken[j] = true
		out[i] = j
	}
	return out
}
