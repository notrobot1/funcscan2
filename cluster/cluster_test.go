package cluster

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// cliques builds k groups of size nodes, each fully connected inside, with one
// edge from each group to the next.
func cliques(k, size int, shuffle int64) *Graph {
	type pair struct{ u, v string }
	var edges []pair
	name := func(c, i int) string { return fmt.Sprintf("c%02d_n%02d", c, i) }
	for c := 0; c < k; c++ {
		for i := 0; i < size; i++ {
			for j := i + 1; j < size; j++ {
				edges = append(edges, pair{name(c, i), name(c, j)})
			}
		}
		edges = append(edges, pair{name(c, 0), name((c+1)%k, 0)})
	}
	rand.New(rand.NewSource(shuffle)).Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
	g := NewGraph()
	for i, e := range edges {
		if i%2 == 0 {
			e.u, e.v = e.v, e.u
		}
		g.AddEdge(e.u, e.v, 1, "EXTRACTED")
	}
	return g
}

func TestFindsCliques(t *testing.T) {
	communities := Cluster(cliques(6, 8, 1), Options{})
	if len(communities) != 6 {
		t.Fatalf("got %d communities, want 6", len(communities))
	}
	for cid, nodes := range communities {
		if len(nodes) != 8 {
			t.Errorf("community %d has %d nodes, want 8", cid, len(nodes))
		}
		for _, id := range nodes {
			if id[:3] != nodes[0][:3] {
				t.Errorf("community %d mixes cliques: %v", cid, nodes)
				break
			}
		}
	}
}

func TestSameResultWhateverTheInsertionOrder(t *testing.T) {
	want := Cluster(cliques(9, 7, 1), Options{})
	for seed := int64(2); seed < 8; seed++ {
		if got := Cluster(cliques(9, 7, seed), Options{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d gave a different result", seed)
		}
	}
}

func TestIsolatesAndEmptyGraphs(t *testing.T) {
	if got := Cluster(NewGraph(), Options{}); len(got) != 0 {
		t.Errorf("empty graph: %v", got)
	}
	g := NewGraph()
	g.AddNode("b", "")
	g.AddNode("a", "")
	if got, want := Cluster(g, Options{}), (map[int][]string{0: {"a"}, 1: {"b"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("no edges: got %v, want %v", got, want)
	}
	g.AddEdge("x", "y", 1, "")
	got := Cluster(g, Options{})
	if !reflect.DeepEqual(got[0], []string{"x", "y"}) || len(got) != 3 {
		t.Errorf("isolates beside a pair: %v", got)
	}
}

func TestHubIsPlacedWithMostOfItsNeighbours(t *testing.T) {
	g := cliques(3, 6, 1)
	for c, links := range []int{4, 2, 1} {
		for i := 0; i < links; i++ {
			g.AddEdge("hub", fmt.Sprintf("c%02d_n%02d", c, i+1), 1, "")
		}
	}
	g.AddEdge("leaf", "hub", 1, "") // connected to nothing but the hub
	communities := Cluster(g, Options{ExcludeHubsPercentile: 95})
	find := func(id string) int {
		for cid, nodes := range communities {
			for _, n := range nodes {
				if n == id {
					return cid
				}
			}
		}
		return -1
	}
	if find("hub") != find("c00_n01") {
		t.Errorf("hub is not with the clique it has most edges to: %v", communities)
	}
	if find("leaf") != find("hub") {
		t.Errorf("a node whose only neighbour is the hub did not follow it: %v", communities)
	}
}

func TestCohesionIgnoresSelfLoops(t *testing.T) {
	g := NewGraph()
	g.AddEdge("a", "b", 1, "")
	g.AddEdge("a", "a", 1, "")
	if got := CohesionScore(g, []string{"a", "b"}); got != 1 {
		t.Errorf("got %v, want 1", got)
	}
	if got := CohesionScore(g, []string{"a", "b", "c", "d"}); got != 1.0/6 {
		t.Errorf("got %v, want 1/6", got)
	}
}

func TestCacheIsUsedOnlyForTheSameGraphAndOptions(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	opts := Options{CacheDir: dir, Partition: func(g *Graph, r float64) map[string]int {
		calls++
		return Louvain(g, r)
	}}
	g := cliques(4, 6, 1)
	first := Cluster(g, opts)
	if calls == 0 {
		t.Fatal("partitioner was not called")
	}
	before := calls
	if again := Cluster(cliques(4, 6, 5), opts); !reflect.DeepEqual(again, first) || calls != before {
		t.Errorf("same graph, other insertion order: cache was not used (calls %d -> %d)", before, calls)
	}
	g.AddEdge("c00_n01", "c02_n03", 1, "")
	Cluster(g, opts)
	if calls == before {
		t.Error("a changed graph was answered from the cache")
	}
	before = calls
	opts.Resolution = 2
	Cluster(g, opts)
	if calls == before {
		t.Error("changed options were answered from the cache")
	}
	// A damaged cache is ignored, not fatal.
	os.WriteFile(filepath.Join(dir, partitionCacheName), []byte("{broken"), 0o644)
	if got := Cluster(cliques(4, 6, 1), Options{CacheDir: dir}); len(got) != 4 {
		t.Errorf("with a damaged cache: %d communities, want 4", len(got))
	}
}

func TestAmbiguousEdgesWeighLess(t *testing.T) {
	// Two triangles joined by three AMBIGUOUS edges: at full weight the joins
	// are as strong as the triangles; at zero they do not count.
	build := func() *Graph {
		g := NewGraph()
		for _, t := range []string{"a", "b"} {
			g.AddEdge(t+"1", t+"2", 1, "EXTRACTED")
			g.AddEdge(t+"2", t+"3", 1, "EXTRACTED")
			g.AddEdge(t+"1", t+"3", 1, "EXTRACTED")
		}
		for _, i := range []string{"1", "2", "3"} {
			g.AddEdge("a"+i, "b"+i, 1, "AMBIGUOUS")
		}
		return g
	}
	g := build()
	if got := Cluster(g, Options{AmbiguousScale: Scale(0)}); len(got) != 2 {
		t.Errorf("scale 0: %d communities, want 2: %v", len(got), got)
	}
	if !reflect.DeepEqual(g, build()) {
		t.Error("Cluster changed the graph it was given")
	}
}

func TestRemapAndLabels(t *testing.T) {
	communities := map[int][]string{0: {"a", "b", "c"}, 1: {"d", "e"}, 2: {"f"}}
	previous := map[string]int{"a": 5, "b": 5, "d": 0, "e": 0}
	got := RemapToPrevious(communities, previous)
	want := map[int][]string{5: {"a", "b", "c"}, 0: {"d", "e"}, 1: {"f"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("remap: got %v, want %v", got, want)
	}
	sigs := MemberSigs(communities)
	if !LabelStillApplies(0, []string{"c", "b", "a"}, nil, sigs[0], MemberSigs(map[int][]string{0: {"c", "b", "a"}})[0]) {
		t.Error("same members, same fingerprint: the label should stay")
	}
	big := map[string]int{"a": 5, "b": 5, "c": 5, "d": 5}
	if LabelStillApplies(5, []string{"a"}, big, "x", "y") {
		t.Error("one node left of four: the label should go")
	}
	if !LabelStillApplies(5, []string{"a", "b", "c"}, big, "x", "y") {
		t.Error("three of four kept: the label should stay")
	}

	g := NewGraph()
	g.AddNode("f1", "Run()")
	g.AddEdge("f1", "x", 1, "")
	g.AddEdge("f1", "y", 1, "")
	labels := LabelByHub(g, map[int][]string{0: {"x", "f1", "y"}, 1: {"gone"}})
	if labels[0] != "Run" || labels[1] != "Community 1" {
		t.Errorf("labels: %v", labels)
	}
}
