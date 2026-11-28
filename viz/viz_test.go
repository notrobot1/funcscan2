package viz

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/notrobot1/funcscan2/extractors/base"
	"github.com/notrobot1/funcscan2/graph"
)

// sample builds k communities of size nodes each: a hub function calling the
// others, and one call from each hub to the next.
func sample(k, size int) (*graph.Graph, map[int][]string, map[int]string) {
	var nodes []*base.Node
	var edges []*base.Edge
	communities := map[int][]string{}
	labels := map[int]string{}
	for c := 0; c < k; c++ {
		file := fmt.Sprintf("pkg%d/file.go", c)
		hub := fmt.Sprintf("c%d_hub", c)
		labels[c] = fmt.Sprintf("Layer <%d>", c)
		for i := 0; i < size; i++ {
			id := fmt.Sprintf("c%d_n%d", c, i)
			label := fmt.Sprintf("fn%d_%d()", c, i)
			if i == 0 {
				id, label = hub, fmt.Sprintf("Hub%d", c)
			} else {
				edges = append(edges, &base.Edge{Source: hub, Target: id, Relation: "calls", Confidence: "EXTRACTED", SourceFile: file, Weight: 1})
			}
			nodes = append(nodes, &base.Node{ID: id, Label: label, FileType: "code", SourceFile: file})
			communities[c] = append(communities[c], id)
		}
		edges = append(edges, &base.Edge{
			Source: hub, Target: fmt.Sprintf("c%d_hub", (c+1)%k), Relation: "calls",
			Confidence: "INFERRED", ConfidenceScore: 0.85, SourceFile: file, Weight: 1,
		})
	}
	return graph.FromExtract(nodes, edges), communities, labels
}

func pageData(t *testing.T, page, name string, into any) {
	t.Helper()
	m := regexp.MustCompile(`const ` + name + ` = (.*);\n`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("page has no %s", name)
	}
	if err := json.Unmarshal([]byte(m[1]), into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func TestNetworkDrawsEveryNodeOfASmallGraph(t *testing.T) {
	g, communities, labels := sample(3, 10)
	page, aggregated, err := Network(g, communities, NetworkOptions{Title: "demo", Labels: labels})
	if err != nil || aggregated {
		t.Fatalf("aggregated %v, err %v", aggregated, err)
	}
	var nodes []visNode
	var edges []visEdge
	var legend []legendEntry
	pageData(t, page, "RAW_NODES", &nodes)
	pageData(t, page, "RAW_EDGES", &edges)
	pageData(t, page, "LEGEND", &legend)
	if len(nodes) != 30 || len(edges) != 30 || len(legend) != 3 {
		t.Fatalf("%d nodes, %d edges, %d legend entries", len(nodes), len(edges), len(legend))
	}
	// The hub has the most edges: largest, and labelled. A leaf is neither.
	if nodes[0].Size != 40 || nodes[0].Font.Size != 12 || nodes[1].Font.Size != 0 {
		t.Errorf("hub %+v, leaf %+v", nodes[0], nodes[1])
	}
	for _, e := range edges {
		if inferred := e.Confidence == "INFERRED"; e.Dashes != inferred {
			t.Errorf("edge %s->%s: confidence %s, dashed %v", e.From, e.To, e.Confidence, e.Dashes)
		}
	}
	if legend[0].Label != "Layer &lt;0&gt;" || legend[0].Count != 10 {
		t.Errorf("legend: %+v", legend[0])
	}
	if !strings.Contains(page, "<title>funcscan - demo</title>") {
		t.Error("title missing")
	}
}

func TestNetworkFoldsALargeGraphIntoItsCommunities(t *testing.T) {
	g, communities, labels := sample(4, 6)
	page, aggregated, err := Network(g, communities, NetworkOptions{Labels: labels, NodeLimit: 10})
	if err != nil || !aggregated {
		t.Fatalf("aggregated %v, err %v", aggregated, err)
	}
	var nodes []visNode
	var edges []visEdge
	pageData(t, page, "RAW_NODES", &nodes)
	pageData(t, page, "RAW_EDGES", &edges)
	if len(nodes) != 4 || len(edges) != 4 {
		t.Fatalf("%d nodes, %d edges; want one node per community and one edge per neighbouring pair", len(nodes), len(edges))
	}
	if nodes[0].Label != "Layer <0>" || nodes[0].MemberCount == nil || *nodes[0].MemberCount != 6 {
		t.Errorf("community node: %+v", nodes[0])
	}
	if edges[0].Label != "1 cross-community edges" || edges[0].Confidence != "AGGREGATED" {
		t.Errorf("community edge: %+v", edges[0])
	}
	if _, _, err := Network(g, communities, NetworkOptions{NodeLimit: -1}); err != nil {
		t.Errorf("no limit: %v", err)
	}
	one := map[int][]string{0: g.Nodes()}
	if _, _, err := Network(g, one, NetworkOptions{NodeLimit: 10}); err != ErrNothingToDraw {
		t.Errorf("one community over the limit: err %v", err)
	}
}

func TestPageDataCannotBreakOutOfTheScript(t *testing.T) {
	g := graph.New()
	g.AddNode("a", `</script><script>alert(1)</script>`)
	g.AddNode("b", "<!-- <script")
	g.AddEdge("a", "b", 1, "EXTRACTED")
	page, _, err := Network(g, map[int][]string{0: {"a", "b"}}, NetworkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page, "alert(1)</script>") || strings.Contains(page, "<!-- <script") {
		t.Error("a label reached the page unescaped")
	}
}

func TestCallflow(t *testing.T) {
	g, communities, labels := sample(3, 30)
	page, err := Callflow(g, communities, CallflowOptions{Project: "demo", Labels: labels, MaxDiagramNodes: 6, MaxDiagramEdges: 4})
	if err != nil {
		t.Fatal(err)
	}
	diagrams := regexp.MustCompile(`(?s)<template>\n(.*?)\n</template>`).FindAllStringSubmatch(page, -1)
	if len(diagrams) != 4 {
		t.Fatalf("%d diagrams, want the overview and one per section", len(diagrams))
	}
	if !strings.Contains(diagrams[0][1], "<small>30 nodes</small>") || strings.Count(diagrams[0][1], "-->|calls|") != 3 {
		t.Errorf("overview:\n%s", diagrams[0][1])
	}
	section := diagrams[1][1]
	if n := strings.Count(section, `("`); n != 6 {
		t.Errorf("section diagram draws %d nodes, want the 6 allowed", n)
	}
	if n := strings.Count(section, "-->|"); n != 4 {
		t.Errorf("section diagram draws %d edges, want the 4 allowed", n)
	}
	if !strings.Contains(section, "Omitted for readability: 24 nodes, 1 edges") {
		t.Errorf("section diagram does not say what it leaves out:\n%s", section)
	}
	if strings.Contains(page, "Layer <0>") || !strings.Contains(page, "Layer &lt;0&gt;") {
		t.Error("a section name reached the page unescaped")
	}
	if rows := strings.Count(page, "<tr>\n  <td>"); rows != 90 {
		t.Errorf("%d table rows, want 30 per section", rows)
	}

	if _, err := Callflow(graph.New(), nil, CallflowOptions{}); err != ErrEmptyGraph {
		t.Errorf("empty graph: err %v", err)
	}
	custom, err := Callflow(g, communities, CallflowOptions{Sections: []Section{{ID: "Core API", Name: "Core", Communities: []int{0, 1}}}})
	if err != nil || !strings.Contains(custom, `<h2 id="core-api">2. Core</h2>`) {
		t.Errorf("custom sections: err %v", err)
	}
}

func TestLayersGroupCommunitiesByKeyword(t *testing.T) {
	g, communities, _ := sample(3, 5)
	layers := []Layer{{ID: "first", Name: "First layer", Keywords: []string{"pkg0", "fn0"}}}
	page, err := Callflow(g, communities, CallflowOptions{Layers: layers})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, `<h2 id="first">2. First layer</h2>`) {
		t.Error("the community named with the layer's keywords did not get the layer's section")
	}
}

func TestMermaidText(t *testing.T) {
	got := mermaidText("a -> b {x} `y` \"z\" #1 | <T>")
	if want := "a to b x y 'z' 1 &lt;T&gt;"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if id := nodeMermaidID("9 lives/файл.go"); !regexp.MustCompile(`^node_9_lives_go_[0-9a-f]{8}$`).MatchString(id) {
		t.Errorf("id %q", id)
	}
}
