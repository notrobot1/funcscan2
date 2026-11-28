package viz

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/notrobot1/funcscan2/graph"
)

// CallflowOptions tune Callflow. The zero value is ready to use.
type CallflowOptions struct {
	// Project names the project in the title.
	Project string
	// Commit is the revision the graph was built from.
	Commit string
	// Labels names the communities. A community without a name is named
	// after the words that recur in its symbols.
	Labels map[int]string
	// Sections lays the page out by hand. Nil means sections are worked out
	// from the communities (see Callflow).
	Sections []Section
	// Layers groups communities into sections by the words in their names
	// (see Layer). Nil means no grouping: each of the largest communities
	// gets a section of its own.
	Layers []Layer
	// MaxSections caps the sections worked out from the communities, not
	// counting the overview. Zero means 15.
	MaxSections int
	// MaxDiagramNodes and MaxDiagramEdges bound each section's diagram. Zero
	// means 18 nodes and 24 edges.
	MaxDiagramNodes, MaxDiagramEdges int
	// DiagramScale enlarges or shrinks the diagrams, from 0.65 to 1.8. Zero
	// means 1.
	DiagramScale float64
	// ReportMarkdown, when set to the output of report.Report.Markdown, puts
	// the report's summary and most connected nodes on the overview.
	ReportMarkdown string
	// Date dates the page. Zero means now.
	Date time.Time
}

// ErrEmptyGraph is returned for a graph with no nodes.
var ErrEmptyGraph = errors.New("viz: the graph has no nodes")

// ErrNoSections is returned when there is nothing to divide the page into:
// no communities, and no sections given.
var ErrNoSections = errors.New("viz: no sections to lay the page out in")

// Callflow renders the graph as an architecture document.
//
// The page opens with an overview: the sections as boxes and the calls
// between them as arrows. Each section then has a diagram of its
// representative nodes and edges, a table of its nodes with what calls them
// and what they call, and its main files.
//
// Unless Sections are given, the largest communities get a section each, up
// to MaxSections, and the remainder is gathered under "Other". With Layers,
// communities are first grouped into the layers their names speak of.
//
// Diagrams show only edges that are certain or inferred with high
// confidence, and no more than MaxDiagramNodes nodes each; the tables carry
// the rest. Each diagram is rendered when it is scrolled into view.
func Callflow(g *graph.Graph, communities map[int][]string, opts CallflowOptions) (string, error) {
	if g.NumNodes() == 0 {
		return "", ErrEmptyGraph
	}
	maxSections, maxNodes, maxEdges := opts.MaxSections, opts.MaxDiagramNodes, opts.MaxDiagramEdges
	if maxSections <= 0 {
		maxSections = 15
	}
	if maxNodes <= 0 {
		maxNodes = 18
	}
	if maxEdges <= 0 {
		maxEdges = 24
	}
	date := opts.Date
	if date.IsZero() {
		date = time.Now()
	}
	project := opts.Project
	if project == "" {
		project = "Project"
	}

	nodes, edges := callflowGraph(g, communities)
	labels := make(map[string]string, len(opts.Labels))
	for cid, label := range opts.Labels {
		labels[strconv.Itoa(cid)] = label
	}
	var sections []cfSection
	if opts.Sections != nil {
		for _, s := range opts.Sections {
			section := cfSection{id: s.ID, name: s.Name}
			for _, cid := range s.Communities {
				section.communities = append(section.communities, strconv.Itoa(cid))
			}
			sections = append(sections, section)
		}
	} else {
		sections = deriveSections(nodes, labels, maxSections, opts.Layers)
	}
	sections = normalizeSections(sections)
	if len(sections) <= 1 {
		return "", ErrNoSections
	}

	index, _ := communityIndex(nodes)
	sectionNodes := map[string][]*cfNode{}
	for _, s := range sections {
		for _, cid := range s.communities {
			sectionNodes[s.id] = append(sectionNodes[s.id], index[cid]...)
		}
	}
	classes := classifyEdges(edges, sections, sectionNodes)
	nodeByID := make(map[string]*cfNode, len(nodes))
	for _, n := range nodes {
		nodeByID[n.id] = n
	}
	// Who calls a node is a fact about the whole graph, not about its
	// section: most callers of a node sit in other sections, or in none.
	callers, callees := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, e := range edges {
		switch e.relation {
		case "calls", "imports", "imports_from", "uses", "method", "indirect_call":
			if callers[e.target] == nil {
				callers[e.target] = map[string]bool{}
			}
			if callees[e.source] == nil {
				callees[e.source] = map[string]bool{}
			}
			callers[e.target][e.source] = true
			callees[e.source][e.target] = true
		}
	}

	esc := html.EscapeString
	title := project + " — Complete Call Flow & Architecture Documentation"
	commit := opts.Commit
	if commit == "" {
		commit = "unknown"
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	p("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"UTF-8\">\n")
	p("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n<title>%s</title>\n", esc(title))
	p("<script src=\"https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js\"></script>\n<style>\n%s</style>\n</head>\n<body>\n<div class=\"container\">\n", asset("callflow.css"))

	p("<h1>%s</h1>\n", esc(title))
	p("<p class=\"subtitle\">%s</p>\n\n", esc(fmt.Sprintf(
		"Generated from the funcscan code graph: %d nodes, %d edges, %d communities. Commit: %s",
		len(nodes), len(edges), len(index), commit)))
	b.WriteString("<div class=\"nav\">\n")
	for _, s := range sections {
		p("    <a href=\"#%s\">%s</a>\n", esc(s.id), esc(s.name))
	}
	b.WriteString("</div>\n\n")

	// Overview.
	p("<h2 id=\"overview\">1. %s</h2>\n\n<div class=\"mermaid-pending\"><template>\n%s\n</template></div>\n", esc(sections[0].name),
		overviewDiagram(sections, sectionNodes, classes, opts.DiagramScale))
	b.WriteString("<div class=\"grid\">\n  <div class=\"card\">\n    <h4>Architecture Layers</h4>\n    <table style=\"width:100%;font-size:0.85rem;\">\n      <tr><th>Layer</th><th>Nodes</th><th>Communities</th></tr>\n")
	for _, s := range sections[1:] {
		p("      <tr><td>%s</td><td>%d</td><td><code>%s</code></td></tr>\n", esc(s.name), len(sectionNodes[s.id]), esc(strings.Join(s.communities, ", ")))
	}
	p("    </table>\n  </div>\n  <div class=\"card\">\n    <h4>Core Flow</h4>\n    <div class=\"arrow-chain\">%s</div>\n  </div>\n</div>\n", esc(flowChain(sections, classes)))
	if highlights := reportHighlights(opts.ReportMarkdown); len(highlights) > 0 {
		b.WriteString("<div class=\"grid\">\n  <div class=\"card\">\n    <h4>Graph Report Highlights</h4>\n    <ul>\n")
		for _, item := range highlights {
			p("      <li>%s</li>\n", esc(item))
		}
		b.WriteString("    </ul>\n  </div>\n</div>\n")
	}
	b.WriteString("<hr>\n")

	// Sections.
	for i, s := range sections[1:] {
		members, inner := sectionNodes[s.id], classes.intra[s.id]
		p("\n<h2 id=\"%s\">%d. %s</h2>\n<p>%s</p>\n\n", esc(s.id), i+2, esc(s.name), esc(sectionIntro(s, members, len(inner))))
		p("<div class=\"mermaid-pending\"><template>\n%s\n</template></div>\n\n", sectionDiagram(s, members, inner, opts.DiagramScale, maxNodes, maxEdges))
		b.WriteString(`<h3>Call Details</h3>
<table class="call-table">
<tr>
  <th style="width:5%">#</th>
  <th style="width:28%">Node</th>
  <th style="width:10%">Type</th>
  <th style="width:17%">Caller</th>
  <th style="width:20%">Callees</th>
  <th style="width:20%">Description</th>
</tr>
`)
		for row, n := range members {
			if row >= 30 { // the table is a sample too; the graph page has every node
				break
			}
			file := shortPath(n.sourceFile)
			tag := kindTags[n.kind()]
			p("<tr>\n  <td>%d</td>\n  <td><code>%s</code><br><small style=\"color:var(--muted)\">%s</small></td>\n  <td><span class=\"tag %s\">%s</span></td>\n  <td>%s</td>\n  <td>%s</td>\n  <td>%s</td>\n</tr>\n",
				row+1, esc(n.label), esc(file), tag.class, tag.name,
				nodeRefs(callers[n.id], nodeByID, "External entry / no inbound edge"),
				nodeRefs(callees[n.id], nodeByID, "No direct outbound edge"),
				esc(describeNode(n.label, file, n.fileType)))
		}
		b.WriteString("</table>\n\n")
		b.WriteString(sectionCards(members, inner))
		b.WriteString("<hr>\n")
	}

	// Statistics.
	byConfidence := map[string]int{}
	for _, e := range edges {
		byConfidence[e.confidence]++
	}
	p(`
<h2 id="stats">Project Statistics</h2>

<div class="grid">
  <div class="card">
    <h4>Graph</h4>
    <table style="width:100%%;font-size:0.85rem;">
      <tr><td>Nodes</td><td>%d</td></tr>
      <tr><td>Edges</td><td>%d</td></tr>
      <tr><td>Communities</td><td>%d</td></tr>
      <tr><td>Documented Sections</td><td>%d</td></tr>
    </table>
  </div>
  <div class="card">
    <h4>Edge Confidence</h4>
    <table style="width:100%%;font-size:0.85rem;">
      <tr><td>EXTRACTED</td><td>%d</td></tr>
      <tr><td>INFERRED</td><td>%d</td></tr>
      <tr><td>AMBIGUOUS</td><td>%d</td></tr>
    </table>
  </div>
</div>
`, len(nodes), len(edges), len(index), len(sections)-1, byConfidence["EXTRACTED"], byConfidence["INFERRED"], byConfidence["AMBIGUOUS"])

	p("<div style=\"text-align:center; padding:40px 0; color: var(--muted); font-size:0.9rem;\">\n  <p>%s — Architecture Documentation</p>\n  <p>Generated: %s · funcscan callflow</p>\n</div>\n",
		esc(project), date.UTC().Format("2006-01-02 15:04 UTC"))
	p("</div>\n\n<script>\n%s</script>\n\n</body>\n</html>\n", asset("callflow.js"))
	return b.String(), nil
}

// WriteCallflow renders the graph with Callflow and writes the page to a file.
func WriteCallflow(path string, g *graph.Graph, communities map[int][]string, opts CallflowOptions) error {
	page, err := Callflow(g, communities, opts)
	if err != nil {
		return err
	}
	return writeFile(path, page)
}

// flowChain reads a path through the sections off the calls between them:
// from the section least called into, each time along the busiest link to a
// section not yet visited.
func flowChain(sections []cfSection, c classified) string {
	names := map[string]string{}
	var order []string
	for _, s := range sections[1:] {
		names[s.id] = s.name
		order = append(order, s.id)
	}
	outgoing := map[string]map[string]int{}
	incoming := map[string]int{}
	for _, link := range sectionLinks(c) {
		if outgoing[link.from] == nil {
			outgoing[link.from] = map[string]int{}
		}
		outgoing[link.from][link.to] += link.count
		incoming[link.to] += link.count
	}
	current := order[0]
	for _, id := range order {
		if incoming[id] < incoming[current] {
			current = id
		}
	}
	chain, seen := []string{current}, map[string]bool{current: true}
	for len(chain) < min(7, len(order)) {
		next, best := "", 0
		for to, count := range outgoing[current] {
			if !seen[to] && (count > best || (count == best && to > next)) {
				next, best = to, count
			}
		}
		if next == "" {
			for _, id := range order {
				if !seen[id] {
					next = id
					break
				}
			}
		}
		if next == "" {
			break
		}
		chain = append(chain, next)
		seen[next] = true
		current = next
	}
	for i, id := range chain {
		chain[i] = names[id]
	}
	return strings.Join(chain, " -> ")
}

func sectionIntro(s cfSection, nodes []*cfNode, edgeCount int) string {
	var fileCounts tally
	for _, n := range nodes {
		if n.sourceFile != "" {
			fileCounts.add(n.sourceFile, 1)
		}
	}
	files := fileCounts.mostCommon(3)
	for i, file := range files {
		files[i] = shortPath(file)
	}
	where, about := "unmapped files", s.name
	if len(files) > 0 {
		where = strings.Join(files, ", ")
	}
	if keywords := sectionKeywords(nodes, 4); len(keywords) > 0 {
		about = strings.Join(keywords, ", ")
	}
	return fmt.Sprintf("%s groups implementation around %s, mostly in %s. This section covers %d nodes and %d internal edges; "+
		"the diagram shows only representative relationships to stay readable.", s.name, about, where, len(nodes), edgeCount)
}

func sectionCards(nodes []*cfNode, edges []*cfEdge) string {
	fileCounts := map[string]int{}
	for _, n := range nodes {
		if n.sourceFile != "" {
			fileCounts[n.sourceFile]++
		}
	}
	files := make([]string, 0, len(fileCounts))
	for file := range fileCounts {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		if fileCounts[files[i]] != fileCounts[files[j]] {
			return fileCounts[files[i]] > fileCounts[files[j]]
		}
		return files[i] < files[j]
	})
	var rows strings.Builder
	for i, file := range files {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&rows, "      <tr><td><code>%s</code></td><td>%d nodes</td></tr>\n", html.EscapeString(shortPath(file)), fileCounts[file])
	}
	if len(files) == 0 {
		rows.WriteString("      <tr><td colspan=\"2\">No source file mapping</td></tr>\n")
	}
	var relations tally
	for _, e := range edges {
		if e.drawable() {
			relations.add(e.relation, 1)
		}
	}
	var parts []string
	for _, relation := range relations.mostCommon(4) {
		parts = append(parts, fmt.Sprintf("%s x%d", relationLabel(relation), relations.count[relation]))
	}
	summary := "No high-confidence call edges detected"
	if len(parts) > 0 {
		summary = strings.Join(parts, ", ")
	}
	note := "This section comes from community clustering of the code graph. Relationship summary: " + summary +
		". The diagram prioritizes high-confidence calls or usage relationships; the table keeps the broader node inventory."
	return fmt.Sprintf(`<div class="grid">
  <div class="card">
    <h4>Key Files</h4>
    <table style="width:100%%;font-size:0.85rem;">
      <tr><th>File</th><th>Coverage</th></tr>
%s    </table>
  </div>
  <div class="card">
    <h4>Design Notes</h4>
    <p>%s</p>
  </div>
</div>
`, rows.String(), html.EscapeString(note))
}

var kindTags = map[string]struct{ name, class string }{
	"concept": {"Concept", "tag-func"}, "entry": {"Entry", "tag-cmd"}, "api": {"API", "tag-endpoint"},
	"async": {"Async", "tag-async"}, "klass": {"Class", "tag-class"}, "ui": {"UI", "tag-hook"},
	"module": {"Module", "tag-class"}, "test": {"Test", "tag-func"}, "function": {"Function", "tag-func"},
}

// nodeRefs lists up to three nodes by name, with their files.
func nodeRefs(ids map[string]bool, nodeByID map[string]*cfNode, none string) string {
	if len(ids) == 0 {
		return html.EscapeString(none)
	}
	display := func(id string) string {
		if n := nodeByID[id]; n != nil {
			return humanize(n.label, n.sourceFile)
		}
		return id
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if a, b := strings.ToLower(display(sorted[i])), strings.ToLower(display(sorted[j])); a != b {
			return a < b
		}
		return sorted[i] < sorted[j]
	})
	var parts []string
	for i, id := range sorted {
		if i >= 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(sorted)-3))
			break
		}
		part := "<code>" + html.EscapeString(display(id)) + "</code>"
		if n := nodeByID[id]; n != nil && n.sourceFile != "" {
			part += "<br><small style=\"color:var(--muted)\">" + html.EscapeString(shortPath(n.sourceFile)) + "</small>"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "<br>")
}

// describeNode says in a sentence what a node probably is, going by its name.
func describeNode(label, file, fileType string) string {
	lower := strings.ToLower(label)
	where := file
	if where == "" {
		where = "project"
	}
	switch {
	case fileType == "rationale":
		return "Design note for " + label + "."
	case fileType == "document":
		return "Documentation node describing " + label + "."
	case hasAnySuffix(label, ".py", ".tsx", ".ts"):
		return "Module file in " + where + "."
	case strings.Contains(lower, "config"):
		return "Reads, resolves, or persists project configuration."
	case strings.Contains(lower, "scan"):
		return "Starts scanning or handles scan status."
	case containsAny(lower, "ingest", "clone", "git"):
		return "Turns a local path or remote repository into analysis context."
	case strings.Contains(lower, "prompt"):
		return "Builds structured prompts for model calls."
	case strings.Contains(lower, "analy"):
		return "Orchestrates analysis and returns structured documentation data."
	case containsAny(lower, "graph", "dependency"):
		return "Builds dependency relationships and graph data."
	case containsAny(lower, "export", "markdown", "html"):
		return "Exports documentation data to a target format."
	case containsAny(lower, "chat", "rag", "retrieve"):
		return "Supports retrieval-augmented Q&A or streaming chat."
	case containsAny(lower, "wiki", "page", "sidebar"):
		return "Organizes documentation pages, navigation, or content lookup."
	case containsAny(lower, "cache", "hash"):
		return "Caches analysis results or computes cache keys."
	case strings.Contains(lower, "test"):
		return "Verifies imports, entry points, or version behavior."
	}
	return label + " node in " + where + "."
}

var numbered = regexp.MustCompile(`^\d+\.`)

// reportHighlights takes the summary lines and the most connected nodes out
// of a Markdown report, six items at most.
func reportHighlights(markdown string) []string {
	var keep []string
	section := ""
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "## "):
			section = line
			continue
		case section == "## Summary" && strings.HasPrefix(line, "- "):
			keep = append(keep, line[2:])
		case strings.HasPrefix(section, "## God Nodes") && numbered.MatchString(line):
			keep = append(keep, line)
		}
		if len(keep) >= 6 {
			break
		}
	}
	return keep
}

func itoa(n int) string { return strconv.Itoa(n) }
