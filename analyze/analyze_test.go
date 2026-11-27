package analyze

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/notrobot1/funcscan2/extractors/base"
	"github.com/notrobot1/funcscan2/graph"
)

func TestSamplingMatchesPython(t *testing.T) {
	// random.Random(seed).sample(range(n), k) in CPython.
	for _, c := range []struct {
		seed uint32
		n, k int
		want []int
	}{
		{42, 2000, 100, []int{1309, 228, 51, 1518, 563, 501}},
		{42, 1040, 100, []int{228, 51, 563, 501, 457, 285}},
		{7, 30, 4, []int{10, 4, 12, 20}},
	} {
		if got := newMT(c.seed).sample(c.n, c.k)[:len(c.want)]; !reflect.DeepEqual(got, c.want) {
			t.Errorf("seed %d, %d of %d: got %v, want %v", c.seed, c.k, c.n, got, c.want)
		}
	}
}

func chain(ids ...string) *graph.Graph {
	g := graph.New()
	for i := 1; i < len(ids); i++ {
		g.AddEdge(ids[i-1], ids[i], 1, "EXTRACTED")
	}
	return g
}

func TestBetweennessOfAPath(t *testing.T) {
	// On a-b-c-d-e the middle node lies on 4 of the 6 pairs among the others.
	scores := Betweenness(chain("a", "b", "c", "d", "e"), 0, 0)
	want := map[string]float64{"a": 0, "b": 0.5, "c": 4.0 / 6, "d": 0.5, "e": 0}
	for id, w := range want {
		if math.Abs(scores[id]-w) > 1e-12 {
			t.Errorf("%s: got %v, want %v", id, scores[id], w)
		}
	}
	edges := EdgeBetweenness(chain("a", "b", "c"))
	if len(edges) != 2 || math.Abs(edges[0].Score-2.0/3) > 1e-12 {
		t.Errorf("edge betweenness: %v", edges)
	}
}

func node(id, label, file string) *base.Node {
	return &base.Node{ID: id, Label: label, FileType: "code", SourceFile: file, Origin: "ast"}
}

func edge(source, target, relation, confidence, file string) *base.Edge {
	return &base.Edge{Source: source, Target: target, Relation: relation, Confidence: confidence, SourceFile: file, Weight: 1}
}

func TestGodNodesLeaveOutFilesStubsAndBuiltins(t *testing.T) {
	nodes := []*base.Node{
		node("f", "a.go", "a.go"), node("core", "Core", "a.go"), node("m", ".Run()", "a.go"),
		node("s", "String", "a.go"), node("x", "X", "a.go"), node("y", "Y", "a.go"), node("z", "Z", "a.go"),
	}
	var edges []*base.Edge
	for _, to := range []string{"core", "m", "s", "x", "y", "z"} {
		edges = append(edges, edge("f", to, "contains", "EXTRACTED", "a.go"))
	}
	for _, to := range []string{"x", "y", "z", "m", "s"} {
		edges = append(edges, edge("core", to, "calls", "EXTRACTED", "a.go"))
		edges = append(edges, edge("s", to, "references", "EXTRACTED", "a.go"))
		edges = append(edges, edge("m", to, "calls", "EXTRACTED", "a.go"))
	}
	got := GodNodes(graph.FromExtract(nodes, edges), 2, 0)
	if len(got) != 2 || got[0].ID != "core" || got[0].Degree != 6 {
		t.Errorf("got %+v, want Core first", got)
	}
	for _, n := range got {
		if n.ID == "f" || n.ID == "m" || n.ID == "s" {
			t.Errorf("%s should be left out", n.ID)
		}
	}
}

func TestImportCycles(t *testing.T) {
	var nodes []*base.Node
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		nodes = append(nodes, node(f, f, f))
	}
	imports := func(from, to string) *base.Edge { return edge(from, to, "imports_from", "EXTRACTED", from) }
	g := graph.FromExtract(nodes, []*base.Edge{
		imports("a.go", "b.go"), imports("b.go", "a.go"), // two files
		imports("b.go", "c.go"), imports("c.go", "d.go"), imports("d.go", "b.go"), // three
	})
	got := FindImportCycles(g, 5, 20)
	want := [][]string{{"a.go", "b.go"}, {"b.go", "c.go", "d.go"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, c := range got {
		if !reflect.DeepEqual(c.Files, want[i]) || c.Length != len(want[i]) {
			t.Errorf("cycle %d: got %v, want %v", i, c.Files, want[i])
		}
	}
	if short := FindImportCycles(g, 2, 20); len(short) != 1 {
		t.Errorf("at most two files: got %+v", short)
	}
}

func TestSurprisesAndQuestions(t *testing.T) {
	nodes := []*base.Node{
		node("fa", "a.go", "svc/a.go"), node("fb", "b.go", "util/b.go"),
		node("A", "Alpha", "svc/a.go"), node("B", "Beta", "util/b.go"), node("C", "Gamma", "util/b.go"),
	}
	g := graph.FromExtract(nodes, []*base.Edge{
		edge("fa", "A", "contains", "EXTRACTED", "svc/a.go"),
		edge("fb", "B", "contains", "EXTRACTED", "util/b.go"),
		edge("fb", "C", "contains", "EXTRACTED", "util/b.go"),
		edge("A", "B", "calls", "INFERRED", "svc/a.go"),
		edge("B", "C", "calls", "AMBIGUOUS", "util/b.go"),
	})
	communities := map[int][]string{0: {"fa", "A"}, 1: {"fb", "B", "C"}}
	got := SurprisingConnections(g, communities, 5)
	if len(got) != 1 || got[0].Source != "Alpha" || got[0].Target != "Beta" || got[0].Confidence != "INFERRED" {
		t.Fatalf("surprises: %+v", got)
	}
	for _, reason := range []string{"inferred connection", "different repos/directories", "separate communities"} {
		if !strings.Contains(got[0].Why, reason) {
			t.Errorf("why %q lacks %q", got[0].Why, reason)
		}
	}
	questions := SuggestQuestions(g, communities, map[int]string{0: "svc", 1: "util"}, 7)
	if questions[0].Type != AmbiguousEdge || !questions[0].LowConfidence {
		t.Errorf("first question: %+v", questions[0])
	}
	if none := SuggestQuestions(graph.New(), nil, nil, 7); len(none) != 1 || none[0].Type != NoSignal {
		t.Errorf("empty graph: %+v", none)
	}
}

func TestGraphDiff(t *testing.T) {
	before := graph.FromExtract(
		[]*base.Node{node("a", "A", "x.go"), node("b", "B", "x.go"), node("c", "C", "x.go")},
		[]*base.Edge{edge("a", "b", "calls", "EXTRACTED", "x.go"), edge("b", "c", "calls", "EXTRACTED", "x.go")},
	)
	after := graph.FromExtract(
		[]*base.Node{node("a", "A", "x.go"), node("b", "B", "x.go"), node("d", "D", "x.go")},
		[]*base.Edge{edge("b", "a", "calls", "EXTRACTED", "x.go"), edge("a", "d", "calls", "INFERRED", "x.go")},
	)
	diff := GraphDiff(before, after)
	if diff.Summary != "1 new node, 2 new edges, 1 node removed, 2 edges removed" {
		t.Errorf("summary: %q", diff.Summary)
	}
	if diff.NewNodes[0].ID != "d" || diff.RemovedNodes[0].ID != "c" {
		t.Errorf("nodes: %+v %+v", diff.NewNodes, diff.RemovedNodes)
	}
	// a->b turned into b->a: one edge removed, one added.
	if diff.NewEdges[1] != (EdgeChange{"b", "a", "calls", "EXTRACTED"}) || diff.RemovedEdges[0] != (EdgeChange{"a", "b", "calls", "EXTRACTED"}) {
		t.Errorf("edges: %+v %+v", diff.NewEdges, diff.RemovedEdges)
	}
	if same := GraphDiff(before, before); same.Summary != "no changes" {
		t.Errorf("same graph: %q", same.Summary)
	}
}
