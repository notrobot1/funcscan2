package report

import (
	"strings"
	"testing"
	"time"

	"github.com/notrobot1/funcscan2/extractors/base"
	"github.com/notrobot1/funcscan2/graph"
)

func sample() (*graph.Graph, map[int][]string) {
	node := func(id, label, file string) *base.Node {
		return &base.Node{ID: id, Label: label, FileType: "code", SourceFile: file, Origin: "ast"}
	}
	edge := func(source, target, relation, confidence string) *base.Edge {
		return &base.Edge{Source: source, Target: target, Relation: relation, Confidence: confidence, SourceFile: "svc/a.go", Weight: 1}
	}
	nodes := []*base.Node{
		node("fa", "a.go", "svc/a.go"), node("fb", "b.go", "util/b.go"),
		node("A", "Alpha", "svc/a.go"), node("B", "Beta", "svc/a.go"), node("C", "<Gamma & co>", "svc/a.go"),
		node("D", "Delta", "util/b.go"),
	}
	edges := []*base.Edge{
		edge("fa", "A", "contains", "EXTRACTED"), edge("fa", "B", "contains", "EXTRACTED"),
		edge("fa", "C", "contains", "EXTRACTED"), edge("fb", "D", "contains", "EXTRACTED"),
		edge("A", "B", "calls", "EXTRACTED"), edge("B", "C", "calls", "INFERRED"),
		edge("A", "D", "calls", "AMBIGUOUS"), edge("fa", "fb", "imports_from", "EXTRACTED"),
	}
	return graph.FromExtract(nodes, edges), map[int][]string{0: {"fa", "A", "B", "C"}, 1: {"fb", "D"}}
}

func TestReportFigures(t *testing.T) {
	g, communities := sample()
	r := New(g, communities, Options{Root: "/home/me/project", Date: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)})
	if r.Title != "project" || r.Date != "2026-11-27" {
		t.Errorf("title %q, date %q", r.Title, r.Date)
	}
	if r.Nodes != 6 || r.Edges != 8 || r.Communities != 2 || r.Shown != 1 || r.Thin != 1 {
		t.Errorf("counts: %+v", r)
	}
	// 6 of 8 extracted, 1 inferred, 1 ambiguous; halves round to the even number.
	if r.ExtractedPct != 75 || r.InferredPct != 12 || r.AmbiguousPct != 12 {
		t.Errorf("shares: %d %d %d", r.ExtractedPct, r.InferredPct, r.AmbiguousPct)
	}
	if len(r.Listed) != 1 || len(r.Listed[0].Nodes) != 3 {
		t.Errorf("listed communities: %+v", r.Listed)
	}
	if len(r.Ambiguous) != 1 || r.Ambiguous[0].Target != "Delta" {
		t.Errorf("ambiguous edges: %+v", r.Ambiguous)
	}
	if !r.HasCode {
		t.Error("a graph of code should report import cycles")
	}
}

func TestMarkdownAndHTMLCarryTheSameContent(t *testing.T) {
	g, communities := sample()
	r := New(g, communities, Options{Root: "project", Commit: "0123456789abcdef"})
	md, page := r.Markdown(), r.HTML()
	for _, want := range []string{"# Graph Report - project", "## God Nodes", "`Alpha`", "Built from commit: `01234567`", "## Import Cycles", "## Ambiguous Edges"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	for _, want := range []string{"<h1>project</h1>", "Most connected", "Alpha", "01234567", "Ambiguous edges"} {
		if !strings.Contains(page, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	if strings.Contains(page, "<Gamma") || !strings.Contains(page, "&lt;Gamma &amp; co&gt;") {
		t.Error("a label with markup in it was not escaped")
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("the page refers to something outside itself")
	}
}

func TestEmptyGraph(t *testing.T) {
	r := New(graph.New(), nil, Options{Root: "empty"})
	if md := r.Markdown(); !strings.Contains(md, "0 nodes") {
		t.Errorf("markdown: %s", md)
	}
	if page := r.HTML(); !strings.Contains(page, "<h1>empty</h1>") {
		t.Error("html did not render")
	}
}
