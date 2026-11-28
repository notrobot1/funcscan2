package viz

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/notrobot1/funcscan2/graph"
)

// cfNode and cfEdge are the graph as the call-flow page reads it.
type cfNode struct {
	id, label, community string
	sourceFile, fileType string
}

type cfEdge struct {
	source, target string
	relation       string // lower case
	confidence     string // upper case
	score          float64
}

// Section is a part of the call-flow page: a named group of communities that
// make up one layer of the architecture.
type Section struct {
	// ID is used for the section's anchor; it is made unique and URL-safe.
	ID   string
	Name string
	// Communities are the numbers of the communities the section covers.
	Communities []int
}

type cfSection struct {
	id, name    string
	communities []string
}

func callflowGraph(g *graph.Graph, communities map[int][]string) ([]*cfNode, []*cfEdge) {
	communityOf := map[string]string{}
	for cid, members := range communities {
		for _, id := range members {
			communityOf[id] = strconv.Itoa(cid)
		}
	}
	nodes := make([]*cfNode, 0, g.NumNodes())
	for _, id := range g.Nodes() {
		n := g.Node(id)
		node := &cfNode{id: id, label: n.Label, community: communityOf[id], sourceFile: n.SourceFile, fileType: n.FileType}
		if node.label == "" {
			node.label = id
		}
		if node.community == "" {
			node.community = "unknown"
		}
		if node.fileType == "" {
			node.fileType = "code"
			switch strings.ToLower(path.Ext(node.sourceFile)) {
			case ".md", ".mdx", ".rst", ".txt":
				node.fileType = "document"
			}
		}
		nodes = append(nodes, node)
	}
	edges := make([]*cfEdge, 0, g.NumEdges())
	for _, e := range g.Edges() {
		edge := &cfEdge{
			source: e.Source, target: e.Target,
			relation:   strings.ToLower(e.Relation),
			confidence: strings.ToUpper(e.Confidence),
			score:      e.ConfidenceScore,
		}
		if edge.relation == "" {
			edge.relation = "relates"
		}
		if edge.confidence == "" {
			edge.confidence = "EXTRACTED"
		}
		if edge.score == 0 {
			edge.score = e.Weight
		}
		if edge.score == 0 {
			edge.score = 1
		}
		edges = append(edges, edge)
	}
	return nodes, edges
}

// tally counts strings and remembers the order they first came in, which
// breaks ties when the commonest are asked for.
type tally struct {
	keys  []string
	count map[string]int
}

func (t *tally) add(key string, n int) {
	if t.count == nil {
		t.count = map[string]int{}
	}
	if _, seen := t.count[key]; !seen {
		t.keys = append(t.keys, key)
	}
	t.count[key] += n
}

func (t *tally) mostCommon(n int) []string {
	keys := append([]string(nil), t.keys...)
	sort.SliceStable(keys, func(i, j int) bool { return t.count[keys[i]] > t.count[keys[j]] })
	if n >= 0 && len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// ---- text helpers ----

func escapeText(s string) string { // &, < and > only: quotes stay as they are
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// mermaidText makes text safe inside a Mermaid node label: the characters
// Mermaid reads as syntax are removed or replaced.
func mermaidText(text string) string {
	text = strings.NewReplacer(`"`, "'", "`", "", "#", "", "|", " ", "{", "", "}", "").Replace(text)
	text = strings.NewReplacer("->>", " to ", "-->", " to ", "->", " to ").Replace(text)
	return escapeText(strings.Join(strings.Fields(text), " "))
}

var (
	nonIDChars     = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	underscores    = regexp.MustCompile(`_+`)
	nonAnchorChars = regexp.MustCompile(`[^a-z0-9]+`)
)

func shortHash(raw string, n int) string {
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])[:n]
}

// mermaidID builds an identifier Mermaid accepts — letters, digits and
// underscores, not starting with a digit — with a hash of the original at the
// end so that two different names never give the same identifier.
func mermaidID(raw, prefix string) string {
	slug := strings.Trim(underscores.ReplaceAllString(nonIDChars.ReplaceAllString(raw, "_"), "_"), "_")
	if slug == "" {
		slug = prefix
	}
	if slug[0] >= '0' && slug[0] <= '9' {
		slug = prefix + "_" + slug
	}
	if len(slug) > 48 {
		slug = slug[:48]
	}
	return strings.TrimRight(slug, "_") + "_" + shortHash(raw, 8)
}

func nodeMermaidID(id string) string { return mermaidID(id, "node") }

func sectionMermaidID(id string) string { return strings.ToUpper(mermaidID(id, "section")) }

// anchorID makes a unique HTML anchor from a section id.
func anchorID(raw, fallback string, used map[string]bool) string {
	clean := func(s string) string {
		return strings.Trim(nonAnchorChars.ReplaceAllString(strings.ToLower(s), "-"), "-")
	}
	base := clean(raw)
	if base == "" {
		base = clean(fallback)
	}
	if base == "" {
		base = "section"
	}
	if len(base) > 48 {
		base = base[:48]
	}
	if base = strings.Trim(base, "-"); base == "" {
		base = "section"
	}
	candidate := base
	if used[candidate] {
		candidate = base + "-" + shortHash(raw, 6)
	}
	for suffix := 2; used[candidate]; suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	used[candidate] = true
	return candidate
}

// shortPath is the last three parts of a path, enough to recognise a file.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) > 3 {
		return strings.Join(parts[len(parts)-3:], "/")
	}
	return p
}

func truncate(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	return strings.TrimRightFunc(string(r[:max(0, limit-3)]), unicode.IsSpace) + "..."
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

func containsAny(s string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(s, word) {
			return true
		}
	}
	return false
}

// humanize turns a graph label into something short enough to scan in a
// diagram.
func humanize(label, sourceFile string) string {
	label = strings.TrimSpace(label)
	switch {
	case label == "":
		if sourceFile != "" {
			return path.Base(sourceFile)
		}
		return "Unknown"
	case strings.HasPrefix(label, ".") && strings.HasSuffix(label, "()"):
		return label[1:]
	case hasAnySuffix(label, ".py", ".ts", ".tsx", ".js", ".jsx", ".go", ".rs", ".java", ".rb"):
		return path.Base(label)
	}
	if strings.Contains(label, "_") && !strings.Contains(label, " ") && len([]rune(label)) > 28 {
		var parts []string
		for _, part := range strings.Split(label, "_") {
			if part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) > 3 {
			parts = parts[len(parts)-3:]
		}
		if len(parts) > 0 {
			label = strings.Join(parts, " ")
		}
	}
	return truncate(label, 42)
}

// kind classifies a node for the colours of the diagrams and the tags of the
// tables.
func (n *cfNode) kind() string {
	label, file := strings.ToLower(n.label), strings.ToLower(n.sourceFile)
	raw := n.label
	hookLike := strings.HasPrefix(raw, "use") && len(raw) > 3 &&
		(unicode.IsUpper(rune(raw[3])) || raw[3] == '_' || raw[3] == '-')
	switch {
	case n.fileType == "rationale" || n.fileType == "document":
		return "concept"
	case strings.Contains(file, "test") || strings.HasPrefix(label, "test_") || strings.Contains(file, "spec"):
		return "test"
	case containsAny(label, "endpoint", "router", "api", "route"):
		return "api"
	case containsAny(label, "cli", "command", "click", "typer"):
		return "entry"
	case containsAny(label, "async", "await", "stream", "sse"):
		return "async"
	case containsAny(label, "component", "props", "hook", "store") || hookLike ||
		hasAnySuffix(file, ".tsx", ".jsx", ".vue", ".svelte"):
		return "ui"
	case raw != "" && unicode.IsUpper([]rune(raw)[0]) && !strings.HasSuffix(raw, "()"):
		return "klass"
	case hasAnySuffix(raw, ".py", ".ts", ".tsx", ".js", ".jsx", ".go", ".rs", ".java", ".kt", ".rb", ".php", ".cs", ".swift", ".vue", ".svelte"):
		return "module"
	}
	return "function"
}

var relationNames = map[string]string{
	"calls": "calls", "uses": "uses", "imports": "imports", "imports_from": "imports",
	"method": "method", "contains": "contains", "rationale_for": "explains",
	"conceptually_related_to": "relates", "participate_in": "joins", "form": "forms",
}

func relationLabel(relation string) string {
	relation = strings.TrimSpace(relation)
	if name, ok := relationNames[relation]; ok {
		return mermaidText(name)
	}
	return mermaidText(strings.ReplaceAll(relation, "_", " "))
}

// ---- which edges and nodes make a readable diagram ----

// drawable reports an edge sure enough to draw: stated in the source, or
// inferred with high confidence.
func (e *cfEdge) drawable() bool {
	return e.confidence == "EXTRACTED" || (e.confidence == "INFERRED" && e.score >= 0.85)
}

// rank orders edges by how much they tell: certain ones first, calls before
// imports, structure last.
func (e *cfEdge) rank() float64 {
	score := e.score
	if e.confidence == "EXTRACTED" {
		score += 2
	}
	switch e.relation {
	case "calls", "uses", "method":
		score += 1
	case "imports", "imports_from":
		score += 0.6
	case "contains":
		score -= 0.2
	case "rationale_for":
		score -= 0.6
	}
	return score
}

var (
	flowRelations      = map[string]bool{"calls": true, "uses": true, "method": true, "imports": true, "imports_from": true}
	structureRelations = map[string]bool{"contains": true, "rationale_for": true, "conceptually_related_to": true}
)

// preferredEdges keeps the edges that show how the code flows; with
// structure, also those that show how it is laid out. If none qualify, every
// drawable edge does.
func preferredEdges(edges []*cfEdge, structure bool) []*cfEdge {
	var selected, drawable []*cfEdge
	for _, e := range edges {
		if !e.drawable() {
			continue
		}
		drawable = append(drawable, e)
		if flowRelations[e.relation] || (structure && structureRelations[e.relation]) {
			selected = append(selected, e)
		}
	}
	if len(selected) > 0 {
		return selected
	}
	return drawable
}

func byRank(edges []*cfEdge) []*cfEdge {
	sorted := append([]*cfEdge(nil), edges...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].rank() > sorted[j].rank() })
	return sorted
}

// selectDiagramNodes picks up to limit nodes that give a connected, readable
// picture of a section: first the entry points — nodes that call out more
// than they are called — then the ends of the most telling edges, then
// whatever is best connected.
func selectDiagramNodes(nodes []*cfNode, edges []*cfEdge, limit int) []*cfNode {
	byID := make(map[string]*cfNode, len(nodes))
	var ids []string
	for _, n := range nodes {
		if _, seen := byID[n.id]; !seen {
			ids = append(ids, n.id)
		}
		byID[n.id] = n
	}
	usable := preferredEdges(edges, false)
	if len(usable) == 0 {
		usable = preferredEdges(edges, true)
	}
	scores := map[string]float64{}
	outgoing, incoming := map[string]int{}, map[string]int{}
	for _, e := range usable {
		scores[e.source] += e.rank()
		scores[e.target] += e.rank()
		outgoing[e.source]++
		incoming[e.target]++
	}

	var selected []*cfNode
	seen := map[string]bool{}
	// add reports whether the diagram is full.
	add := func(id string) bool {
		n := byID[id]
		if n == nil || seen[id] {
			return false
		}
		// Notes and documents may not crowd out the code.
		if n.kind() == "concept" && len(selected) >= max(4, limit/3) {
			return false
		}
		selected = append(selected, n)
		seen[id] = true
		return len(selected) >= limit
	}

	entries := append([]string(nil), ids...)
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if da, db := outgoing[a]-incoming[a], outgoing[b]-incoming[b]; da != db {
			return da > db
		}
		if outgoing[a] != outgoing[b] {
			return outgoing[a] > outgoing[b]
		}
		return a < b
	})
	for i := 0; i < len(entries) && i < max(3, limit/3); i++ {
		if outgoing[entries[i]] > 0 && add(entries[i]) {
			return selected
		}
	}
	for _, e := range byRank(usable) {
		if add(e.source) || add(e.target) {
			return selected
		}
	}

	rest := append([]*cfNode(nil), nodes...)
	penalty := func(n *cfNode) int {
		if n.kind() == "concept" {
			return 1
		}
		return 0
	}
	sort.SliceStable(rest, func(i, j int) bool {
		a, b := rest[i], rest[j]
		if pa, pb := penalty(a), penalty(b); pa != pb {
			return pa < pb
		}
		if scores[a.id] != scores[b.id] {
			return scores[a.id] > scores[b.id]
		}
		if fa, fb := shortPath(a.sourceFile), shortPath(b.sourceFile); fa != fb {
			return fa < fb
		}
		return humanize(a.label, "") < humanize(b.label, "")
	})
	for _, n := range rest {
		if len(selected) >= limit {
			break
		}
		if !seen[n.id] {
			selected = append(selected, n)
			seen[n.id] = true
		}
	}
	return selected
}
