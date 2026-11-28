package viz

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"github.com/notrobot1/funcscan2/graph"
)

// DefaultNodeLimit is the largest graph Network draws node by node.
const DefaultNodeLimit = 5000

// ErrNothingToDraw is returned for a graph that is too large to draw node by
// node and has fewer than two communities to draw instead.
var ErrNothingToDraw = errors.New("viz: the graph is over the node limit and has fewer than two communities to show in its place")

// NetworkOptions tune Network. The zero value is ready to use.
type NetworkOptions struct {
	// Title names the page, normally after the project.
	Title string
	// Labels names the communities in the legend and in the node panel. Only
	// communities named here are listed in the legend. Nil means "Community
	// N" for every community.
	Labels map[int]string
	// NodeLimit is the largest graph drawn node by node; a larger one is
	// drawn as its communities. Zero means DefaultNodeLimit; a negative value
	// lifts the limit.
	NodeLimit int
}

// Network renders the graph as an interactive page.
//
// Nodes are coloured by community and sized by their number of edges; only
// the best-connected ones carry a label until hovered. Solid edges were
// stated in the source, dashed ones were inferred.
//
// A graph of more than NodeLimit nodes is drawn as its communities instead
// (see the package documentation); Aggregated reports whether that happened.
func Network(g *graph.Graph, communities map[int][]string, opts NetworkOptions) (page string, aggregated bool, err error) {
	limit := opts.NodeLimit
	if limit == 0 {
		limit = DefaultNodeLimit
	}
	labels := opts.Labels
	if labels == nil {
		labels = make(map[int]string, len(communities))
		for cid := range communities {
			labels[cid] = "Community " + strconv.Itoa(cid)
		}
	}
	var memberCounts map[int]int
	if limit > 0 && g.NumNodes() > limit {
		g, communities, memberCounts = aggregate(g, communities, labels)
		if g.NumNodes() <= 1 {
			return "", true, ErrNothingToDraw
		}
		aggregated = true
	}
	return renderNetwork(g, communities, labels, memberCounts, opts.Title), aggregated, nil
}

// WriteNetwork renders the graph with Network and writes the page to a file.
func WriteNetwork(path string, g *graph.Graph, communities map[int][]string, opts NetworkOptions) (aggregated bool, err error) {
	page, aggregated, err := Network(g, communities, opts)
	if err != nil {
		return aggregated, err
	}
	return aggregated, writeFile(path, page)
}

// aggregate folds the graph into one node per community, with an edge
// between two communities for the edges that cross from one to the other.
func aggregate(g *graph.Graph, communities map[int][]string, labels map[int]string) (*graph.Graph, map[int][]string, map[int]int) {
	ids := make([]int, 0, len(communities))
	communityOf := map[string]int{}
	for cid, members := range communities {
		ids = append(ids, cid)
		for _, id := range members {
			communityOf[id] = cid
		}
	}
	sort.Ints(ids)

	meta := graph.New()
	metaCommunities := make(map[int][]string, len(ids))
	memberCounts := make(map[int]int, len(ids))
	for _, cid := range ids {
		label, ok := labels[cid]
		if !ok {
			label = "Community " + strconv.Itoa(cid)
		}
		meta.AddNode(strconv.Itoa(cid), label)
		metaCommunities[cid] = []string{strconv.Itoa(cid)}
		memberCounts[cid] = len(communities[cid])
	}
	type pair struct{ a, b int }
	counts := map[pair]int{}
	var order []pair
	for _, e := range g.Edges() {
		cu, okU := communityOf[e.U]
		cv, okV := communityOf[e.V]
		if !okU || !okV || cu == cv {
			continue
		}
		p := pair{min(cu, cv), max(cu, cv)}
		if counts[p] == 0 {
			order = append(order, p)
		}
		counts[p]++
	}
	for _, p := range order {
		meta.SetEdge(&graph.Edge{
			Source: strconv.Itoa(p.a), Target: strconv.Itoa(p.b),
			Relation:   fmt.Sprintf("%d cross-community edges", counts[p]),
			Confidence: "AGGREGATED",
			Weight:     float64(counts[p]),
		})
	}
	return meta, metaCommunities, memberCounts
}

// The shapes of the data the page's script reads.
type (
	visColor struct {
		Background string       `json:"background"`
		Border     string       `json:"border"`
		Highlight  visHighlight `json:"highlight"`
	}
	visHighlight struct {
		Background string `json:"background"`
		Border     string `json:"border"`
	}
	visFont struct {
		Size  int    `json:"size"`
		Color string `json:"color"`
	}
	visNode struct {
		ID            string   `json:"id"`
		Label         string   `json:"label"`
		Color         visColor `json:"color"`
		Size          float64  `json:"size"`
		Font          visFont  `json:"font"`
		Title         string   `json:"title"`
		Community     int      `json:"community"`
		CommunityName string   `json:"community_name"`
		SourceFile    string   `json:"source_file"`
		FileType      string   `json:"file_type"`
		Degree        int      `json:"degree"`
		MemberCount   *int     `json:"member_count,omitempty"`
	}
	visEdge struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Label  string `json:"label"`
		Title  string `json:"title"`
		Dashes bool   `json:"dashes"`
		Width  int    `json:"width"`
		Color  struct {
			Opacity float64 `json:"opacity"`
		} `json:"color"`
		Confidence string `json:"confidence"`
	}
	legendEntry struct {
		CID   int    `json:"cid"`
		Color string `json:"color"`
		// Label is HTML-escaped: the script puts it into the page as markup.
		Label string `json:"label"`
		Count int    `json:"count"`
	}
)

func networkData(g *graph.Graph, communities map[int][]string, labels map[int]string, memberCounts map[int]int) ([]visNode, []visEdge, []legendEntry) {
	communityOf := map[string]int{}
	for cid, members := range communities {
		for _, id := range members {
			communityOf[id] = cid
		}
	}
	communityName := func(cid int) string {
		if label, ok := labels[cid]; ok {
			return label
		}
		return "Community " + strconv.Itoa(cid)
	}
	members := func(cid int) int {
		if n, ok := memberCounts[cid]; ok {
			return n
		}
		return len(communities[cid])
	}
	maxDegree, maxMembers := 1, 1
	for _, id := range g.Nodes() {
		maxDegree = max(maxDegree, g.Degree(id))
	}
	for _, n := range memberCounts {
		maxMembers = max(maxMembers, n)
	}

	nodes := make([]visNode, 0, g.NumNodes())
	for _, id := range g.Nodes() {
		n := g.Node(id)
		cid := communityOf[id]
		color := communityColor(cid)
		label := n.Label
		if label == "" {
			label = id
		}
		label = sanitize(label)
		degree := g.Degree(id)
		node := visNode{
			ID:    id,
			Label: label,
			Color: visColor{color, color, visHighlight{"#ffffff", color}},
			Font:  visFont{0, "#ffffff"},
			// The tooltip is given as a string, which the drawing library
			// shows as text. It must stay a string: an element here would be
			// inserted as markup.
			Title:         label,
			Community:     cid,
			CommunityName: sanitize(communityName(cid)),
			SourceFile:    sanitize(n.SourceFile),
			FileType:      n.FileType,
			Degree:        degree,
		}
		share := float64(degree) / float64(maxDegree)
		if memberCounts != nil {
			count := members(cid)
			node.MemberCount = &count
			share = float64(max(count, 1)) / float64(maxMembers)
			node.Font.Size = 12
		} else if float64(degree) >= float64(maxDegree)*0.15 {
			// Only the well-connected nodes are labelled; the rest show their
			// name on hover, or the picture would be all text.
			node.Font.Size = 12
		}
		node.Size = roundTenth(10 + 30*share)
		nodes = append(nodes, node)
	}

	edges := make([]visEdge, 0, g.NumEdges())
	for _, e := range g.Edges() {
		confidence := e.Confidence
		if confidence == "" {
			confidence = "EXTRACTED"
		}
		extracted := confidence == "EXTRACTED"
		edge := visEdge{
			From: e.Source, To: e.Target, // the direction of the relation, for the arrow
			Label:      e.Relation,
			Title:      sanitize(fmt.Sprintf("%s [%s]", e.Relation, confidence)),
			Dashes:     !extracted,
			Width:      1,
			Confidence: confidence,
		}
		edge.Color.Opacity = 0.35
		if extracted {
			edge.Width, edge.Color.Opacity = 2, 0.7
		}
		edges = append(edges, edge)
	}

	ids := make([]int, 0, len(labels))
	for cid := range labels {
		ids = append(ids, cid)
	}
	sort.Ints(ids)
	legend := make([]legendEntry, 0, len(ids))
	for _, cid := range ids {
		legend = append(legend, legendEntry{cid, communityColor(cid), html.EscapeString(sanitize(labels[cid])), members(cid)})
	}
	return nodes, edges, legend
}

// roundTenth rounds to one decimal, an exact half going to the even digit.
func roundTenth(v float64) float64 {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 1, 64), 64)
	return rounded
}

// The drawing library, pinned to a version and checked against its hash by
// the browser. The tooltip handling in networkData relies on how this version
// renders a string title; recheck it before moving the pin.
const visNetworkScript = `<script src="https://unpkg.com/vis-network@9.1.6/standalone/umd/vis-network.min.js"
        integrity="sha384-Ux6phic9PEHJ38YtrijhkzyJ8yQlH8i/+buBR8s3mAZOJrP1gwyvAcIYl3GWtpX1"
        crossorigin="anonymous"></script>`

// Applied before the first paint, so the page does not flash the other theme.
const themeInitScript = `<script>
(function () {
  var pref = null;
  try { pref = localStorage.getItem('funcscan-theme'); } catch (e) {}
  var t = pref === 'light' ? 'light'
    : pref === 'system' && window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light'
    : 'dark';
  document.documentElement.setAttribute('data-theme', t);
})();
</script>`

func renderNetwork(g *graph.Graph, communities map[int][]string, labels map[int]string, memberCounts map[int]int, title string) string {
	nodes, edges, legend := networkData(g, communities, labels, memberCounts)
	pageTitle := "funcscan"
	if title = sanitize(title); title != "" {
		pageTitle += " - " + title
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"UTF-8\">\n")
	b.WriteString("<title>" + html.EscapeString(pageTitle) + "</title>\n")
	b.WriteString(visNetworkScript + "\n<style>\n" + asset("network.css") + "</style>\n" + themeInitScript + "\n</head>\n<body>\n")
	b.WriteString(`<div id="graph"></div>
<div id="sidebar">
  <div id="theme-wrap" role="group" aria-label="Theme">
    <button type="button" data-theme-choice="light">Light</button>
    <button type="button" data-theme-choice="dark">Dark</button>
    <button type="button" data-theme-choice="system">System</button>
  </div>
  <div id="search-wrap">
    <input id="search" type="text" placeholder="Search nodes..." autocomplete="off">
    <div id="search-results"></div>
  </div>
  <div id="info-panel">
    <h3>Node Info</h3>
    <div id="info-content"><span class="empty">Click a node to inspect it</span></div>
  </div>
  <div id="legend-wrap">
    <h3>Communities</h3>
    <div id="legend-controls">
      <label><input type="checkbox" id="select-all-cb" checked>Select All</label>
    </div>
    <div id="legend"></div>
  </div>
`)
	fmt.Fprintf(&b, "  <div id=\"stats\">%d nodes &middot; %d edges &middot; %d communities</div>\n</div>\n",
		g.NumNodes(), g.NumEdges(), len(communities))
	b.WriteString("<script>\nconst RAW_NODES = " + scriptJSON(nodes) + ";\nconst RAW_EDGES = " + scriptJSON(edges) +
		";\nconst LEGEND = " + scriptJSON(legend) + ";\n</script>\n")
	b.WriteString("<script>\n" + asset("network.js") + "</script>\n</body>\n</html>\n")
	return b.String()
}

// scriptJSON encodes a value for use inside a <script> element. The encoder
// writes every <, > and & as a \u escape, so no label can close the element
// or open a comment in it.
func scriptJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic("viz: encoding page data: " + err.Error())
	}
	return string(data)
}
