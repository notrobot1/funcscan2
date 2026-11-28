package viz

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
)

// mermaidHeader opens a flowchart, scaled through Mermaid's own settings so
// that text and spacing grow together.
func mermaidHeader(scale float64) string {
	if scale == 0 {
		scale = 1
	}
	scale = math.Max(0.65, math.Min(scale, 1.8))
	round := func(v float64) int { return int(math.RoundToEven(v * scale)) }
	return fmt.Sprintf(`%%%%{init: {"theme": "dark", "themeVariables": {"fontSize": "%spx", "fontFamily": "Segoe UI, system-ui, sans-serif", `+
		`"primaryColor": "#1e293b", "primaryTextColor": "#e2e8f0", "primaryBorderColor": "#38bdf8", "secondaryColor": "#0f172a", `+
		`"tertiaryColor": "#334155", "lineColor": "#64748b", "textColor": "#e2e8f0"}, "flowchart": {"htmlLabels": true, "curve": "basis", `+
		`"nodeSpacing": %d, "rankSpacing": %d, "padding": %d, "diagramPadding": %d, "useMaxWidth": true}}}%%%%`+"\nflowchart LR",
		strconv.FormatFloat(math.Round(15*scale*10)/10, 'f', 1, 64), round(48), round(64), round(14), round(10))
}

// One colour scheme per kind of node.
var mermaidClasses = []string{
	"    classDef entry fill:#422006,stroke:#fbbf24,color:#fde68a,stroke-width:1px;",
	"    classDef api fill:#450a0a,stroke:#f87171,color:#fee2e2,stroke-width:1px;",
	"    classDef async fill:#2e1065,stroke:#a78bfa,color:#ede9fe,stroke-width:1px;",
	"    classDef klass fill:#064e3b,stroke:#34d399,color:#d1fae5,stroke-width:1px;",
	"    classDef ui fill:#831843,stroke:#f472b6,color:#fce7f3,stroke-width:1px;",
	"    classDef module fill:#172554,stroke:#60a5fa,color:#dbeafe,stroke-width:1px;",
	"    classDef test fill:#3f3f46,stroke:#a1a1aa,color:#f4f4f5,stroke-width:1px;",
	"    classDef concept fill:#292524,stroke:#a8a29e,color:#fafaf9,stroke-dasharray:4 3;",
	"    classDef function fill:#0f172a,stroke:#38bdf8,color:#e0f2fe,stroke-width:1px;",
}

// overviewDiagram draws the sections as boxes and the calls between them as
// arrows: the dozen busiest links, each with its commonest relation.
func overviewDiagram(sections []cfSection, sectionNodes map[string][]*cfNode, c classified, scale float64) string {
	lines := []string{mermaidHeader(scale)}
	var shown []cfSection
	for _, s := range sections {
		if s.id == "overview" {
			continue
		}
		shown = append(shown, s)
		id := sectionMermaidID(s.id)
		lines = append(lines,
			fmt.Sprintf(`    %s("%s<br/><small>%d nodes</small>")`, id, mermaidText(s.name), len(sectionNodes[s.id])),
			fmt.Sprintf("    class %s module;", id))
	}
	links := sectionLinks(c)
	sort.SliceStable(links, func(i, j int) bool { return links[i].count > links[j].count })
	for i, link := range links {
		if i >= 12 {
			break
		}
		label := relationLabel(link.relations.mostCommon(1)[0])
		if link.count > 1 {
			label += fmt.Sprintf(" x%d", link.count)
		}
		lines = append(lines, fmt.Sprintf("    %s -->|%s| %s", sectionMermaidID(link.from), label, sectionMermaidID(link.to)))
	}
	if len(links) == 0 {
		for i := 1; i < len(shown); i++ {
			lines = append(lines, fmt.Sprintf("    %s -.-> %s", sectionMermaidID(shown[i-1].id), sectionMermaidID(shown[i].id)))
		}
	}
	return strings.Join(append(lines, mermaidClasses...), "\n")
}

func diagramNodeLabel(n *cfNode) string {
	label := humanize(n.label, n.sourceFile)
	file := shortPath(n.sourceFile)
	if file != "" && !strings.HasSuffix(label, path.Base(file)) {
		return mermaidText(label) + "<br/><small>" + mermaidText(file) + "</small>"
	}
	return mermaidText(label)
}

// sectionDiagram draws one section: a bounded number of representative nodes,
// grouped by file, and the most telling edges among them. What is left out is
// counted in a comment, and listed in the table below the diagram.
func sectionDiagram(s cfSection, nodes []*cfNode, edges []*cfEdge, scale float64, maxNodes, maxEdges int) string {
	lines := []string{mermaidHeader(scale),
		fmt.Sprintf("    %%%% Section: %s (%d nodes, %d edges)", mermaidText(s.name), len(nodes), len(edges))}
	if len(nodes) == 0 {
		lines = append(lines, fmt.Sprintf(`    empty("%s")`, mermaidText(s.name+" - no nodes")))
		return strings.Join(append(lines, mermaidClasses...), "\n")
	}

	selected := selectDiagramNodes(nodes, edges, maxNodes)
	isSelected := make(map[string]bool, len(selected))
	for _, n := range selected {
		isSelected[n.id] = true
	}
	among := func(candidates []*cfEdge) []*cfEdge {
		var out []*cfEdge
		for _, e := range candidates {
			if isSelected[e.source] && isSelected[e.target] {
				out = append(out, e)
			}
		}
		return out
	}
	visible := among(preferredEdges(edges, false))
	if len(visible) == 0 {
		visible = among(preferredEdges(edges, true))
	}

	byFile := map[string][]*cfNode{}
	var files []string
	for _, n := range selected {
		file := shortPath(n.sourceFile)
		if file == "" {
			file = "External / generated"
		}
		if _, seen := byFile[file]; !seen {
			files = append(files, file)
		}
		byFile[file] = append(byFile[file], n)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if a, b := len(byFile[files[i]]), len(byFile[files[j]]); a != b {
			return a > b
		}
		return files[i] < files[j]
	})
	var classLines []string
	for _, file := range files {
		group := byFile[file]
		boxed := len(files) > 1 && len(group) > 1
		indent := "    "
		if boxed {
			lines = append(lines, fmt.Sprintf(`    subgraph %s["%s"]`, nodeMermaidID(s.id+"_"+file), mermaidText(file)))
			indent = "        "
		}
		for _, n := range group {
			id := nodeMermaidID(n.id)
			lines = append(lines, fmt.Sprintf(`%s%s("%s")`, indent, id, diagramNodeLabel(n)))
			classLines = append(classLines, fmt.Sprintf("    class %s %s;", id, n.kind()))
		}
		if boxed {
			lines = append(lines, "    end")
		}
	}

	drawn := 0
	for _, e := range byRank(visible) {
		if drawn >= maxEdges {
			break
		}
		lines = append(lines, fmt.Sprintf("    %s -->|%s| %s", nodeMermaidID(e.source), relationLabel(e.relation), nodeMermaidID(e.target)))
		drawn++
	}
	if omittedNodes, omittedEdges := len(nodes)-len(selected), len(visible)-drawn; omittedNodes > 0 || omittedEdges > 0 {
		lines = append(lines, fmt.Sprintf("    %%%% Omitted for readability: %d nodes, %d edges", max(0, omittedNodes), max(0, omittedEdges)))
	}
	lines = append(lines, classLines...)
	return strings.Join(append(lines, mermaidClasses...), "\n")
}
