// Package viz draws a code graph as HTML pages to look at and click through.
//
// Network gives one interactive picture of the whole graph: every node a dot
// coloured by its community, laid out by a physical simulation, with search,
// a panel for the node under the cursor and a legend to switch communities on
// and off.
//
// Callflow gives a document instead: the communities grouped into the layers
// of the architecture, each with a small diagram of its most telling calls
// and a table of who calls whom.
//
//	g := graph.FromExtract(result.Nodes, result.Edges)
//	communities := cluster.Cluster(g, cluster.Options{})
//	labels, _ := naming.Communities(ctx, g, communities, naming.Options{})
//
//	err := viz.WriteNetwork("graph.html", g, communities, viz.NetworkOptions{Title: "myproject", Labels: labels})
//	err = viz.WriteCallflow("callflow.html", g, communities, viz.CallflowOptions{Project: "myproject", Labels: labels})
//
// Both pages load their drawing library from a CDN (vis-network and Mermaid),
// so they need a network connection when opened. Everything about the graph
// itself is inside the file.
//
// # Keeping large graphs usable
//
// A browser cannot lay out tens of thousands of nodes, so neither page tries:
//
//   - Network draws a graph of more than NodeLimit nodes as its communities:
//     one node per community, sized by its member count, and one edge per
//     pair of communities, counting the edges between them.
//   - The layout simulation runs for a fixed number of steps and is then
//     switched off, and edges are hidden while the view is dragged.
//   - Callflow never draws the whole graph: each section gets a diagram of a
//     bounded number of representative nodes and edges, and a diagram is only
//     rendered when it is scrolled into view.
package viz

import (
	"embed"
	"os"
	"regexp"
)

//go:embed assets/*
var assets embed.FS

func asset(name string) string {
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		panic("viz: missing embedded asset " + name)
	}
	return string(data)
}

// communityColors are cycled through by community number.
var communityColors = []string{
	"#4E79A7", "#F28E2B", "#E15759", "#76B7B2", "#59A14F",
	"#EDC948", "#B07AA1", "#FF9DA7", "#9C755F", "#BAB0AC",
}

func communityColor(cid int) string {
	n := len(communityColors)
	return communityColors[((cid%n)+n)%n]
}

const maxLabelLength = 256

var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// sanitize strips control characters from a label and caps its length. The
// result is safe inside JSON data; it still has to be escaped before it goes
// into HTML.
func sanitize(text string) string {
	text = controlChars.ReplaceAllString(text, "")
	if r := []rune(text); len(r) > maxLabelLength {
		text = string(r[:maxLabelLength])
	}
	return text
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
