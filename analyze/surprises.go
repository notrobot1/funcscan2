package analyze

import (
	"fmt"
	"sort"
	"strings"

	"github.com/notrobot1/funcscan2/graph"
)

// Connection is an edge worth a second look.
type Connection struct {
	// Source and Target are the labels of the two nodes, in the direction of
	// the relation.
	Source      string    `json:"source"`
	Target      string    `json:"target"`
	SourceFiles [2]string `json:"source_files"`
	Confidence  string    `json:"confidence"`
	Relation    string    `json:"relation"`
	// Why says what makes the connection non-obvious; Note is set instead
	// when the connection was picked for where it sits in the graph.
	Why  string `json:"why,omitempty"`
	Note string `json:"note,omitempty"`
}

// structural relations say how the code is laid out, not what it does.
var structural = map[string]bool{"imports": true, "imports_from": true, "contains": true, "method": true}

// SurprisingConnections returns up to topN edges that are not obvious from
// how the files are laid out.
//
// When the graph spans several files these are edges between nodes of
// different files, scored for how unexpected they are: a guessed edge more
// than a certain one, and more again when it crosses file types, top-level
// directories or communities, or runs from an obscure node to a central one.
// For a graph of a single file, or when no such edge exists, they are the
// edges that cross from one community to another — one per pair of
// communities, least certain first. communities may be nil; edges are then
// picked by how many shortest paths run along them.
func SurprisingConnections(g *graph.Graph, communities map[int][]string, topN int) []Connection {
	files := map[string]bool{}
	for _, id := range g.Nodes() {
		if file := g.Node(id).SourceFile; file != "" {
			files[file] = true
		}
	}
	if len(files) > 1 {
		if found := crossFileSurprises(g, communities, topN); len(found) > 0 {
			return found
		}
	}
	return crossCommunitySurprises(g, communities, topN)
}

func connection(g *graph.Graph, e graph.EdgeRef) Connection {
	source, target := e.Source, e.Target
	if !g.Has(source) {
		source = e.U
	}
	if !g.Has(target) {
		target = e.V
	}
	return Connection{
		Source:      name(g, source),
		Target:      name(g, target),
		SourceFiles: [2]string{g.Node(source).SourceFile, g.Node(target).SourceFile},
		Confidence:  confidence(e),
		Relation:    e.Relation,
	}
}

func confidence(e graph.EdgeRef) string {
	if e.Confidence == "" {
		return "EXTRACTED"
	}
	return e.Confidence
}

func crossFileSurprises(g *graph.Graph, communities map[int][]string, topN int) []Connection {
	community := communityOf(communities)
	type scored struct {
		Connection
		score int
	}
	var candidates []scored
	for _, e := range g.Edges() {
		if structural[e.Relation] || IsConceptNode(g, e.U) || IsConceptNode(g, e.V) ||
			IsFileNode(g, e.U) || IsFileNode(g, e.V) {
			continue
		}
		if g.Node(e.U).SourceFile == g.Node(e.V).SourceFile {
			continue
		}
		score, reasons := surpriseScore(g, e, community)
		c := connection(g, e)
		c.Why = "cross-file semantic connection"
		if len(reasons) > 0 {
			c.Why = strings.Join(reasons, "; ")
		}
		candidates = append(candidates, scored{c, score})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	out := make([]Connection, 0, topN)
	for i := 0; i < len(candidates) && i < topN; i++ {
		out = append(out, candidates[i].Connection)
	}
	return out
}

// surpriseScore says how unexpected an edge between two files is, and why.
func surpriseScore(g *graph.Graph, e graph.EdgeRef, community map[string]int) (int, []string) {
	fileU, fileV := g.Node(e.U).SourceFile, g.Node(e.V).SourceFile
	conf := confidence(e)
	categoryU, categoryV := fileCategory(fileU), fileCategory(fileV)

	// A guessed call across languages, or from code into a document, is the
	// name matching going wrong rather than a finding: it earns nothing.
	codeToDoc := (categoryU == "code" && categoryV == "doc") || (categoryU == "doc" && categoryV == "code")
	suppressed := conf == "INFERRED" && (e.Relation == "calls" || e.Relation == "uses") &&
		(crossLanguage(fileU, fileV) || codeToDoc)

	score := 0
	var reasons []string
	if !suppressed {
		score = map[string]int{"AMBIGUOUS": 3, "INFERRED": 2}[conf]
		if score == 0 {
			score = 1
		}
	}
	if conf == "AMBIGUOUS" || conf == "INFERRED" {
		reasons = append(reasons, strings.ToLower(conf)+" connection - not explicitly stated in source")
	}
	if !suppressed {
		if categoryU != categoryV {
			score += 2
			reasons = append(reasons, fmt.Sprintf("crosses file types (%s ↔ %s)", categoryU, categoryV))
		}
		if topLevelDir(fileU) != topLevelDir(fileV) {
			score += 2
			reasons = append(reasons, "connects across different repos/directories")
		}
		cu, okU := community[e.U]
		cv, okV := community[e.V]
		if okU && okV && cu != cv {
			score++
			reasons = append(reasons, "bridges separate communities")
		}
	}
	if e.Relation == "semantically_similar_to" {
		score = score * 3 / 2
		reasons = append(reasons, "semantically similar concepts with no structural link")
	}
	degreeU, degreeV := g.Degree(e.U), g.Degree(e.V)
	if min(degreeU, degreeV) <= 2 && max(degreeU, degreeV) >= 5 {
		score++
		peripheral, hub := name(g, e.V), name(g, e.U)
		if degreeU <= 2 {
			peripheral, hub = hub, peripheral
		}
		reasons = append(reasons, fmt.Sprintf("peripheral node `%s` unexpectedly reaches hub `%s`", peripheral, hub))
	}
	return score, reasons
}

func crossCommunitySurprises(g *graph.Graph, communities map[int][]string, topN int) []Connection {
	out := []Connection{}
	if len(communities) == 0 {
		// Exact edge betweenness is quadratic; past this size it is not worth
		// the wait for a fallback.
		if g.NumEdges() == 0 || g.NumNodes() > 5000 {
			return out
		}
		scores := EdgeBetweenness(g)
		sort.SliceStable(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })
		for i := 0; i < len(scores) && i < topN; i++ {
			u, v := scores[i].U, scores[i].V
			e := g.Edge(u, v)
			out = append(out, Connection{
				Source:      name(g, u),
				Target:      name(g, v),
				SourceFiles: [2]string{g.Node(u).SourceFile, g.Node(v).SourceFile},
				Confidence:  confidence(graph.EdgeRef{Edge: e}),
				Relation:    e.Relation,
				Note:        fmt.Sprintf("Bridges graph structure (betweenness=%.3f)", scores[i].Score),
			})
		}
		return out
	}

	community := communityOf(communities)
	type crossing struct {
		Connection
		pair [2]int
	}
	var crossings []crossing
	for _, e := range g.Edges() {
		cu, okU := community[e.U]
		cv, okV := community[e.V]
		if !okU || !okV || cu == cv || IsFileNode(g, e.U) || IsFileNode(g, e.V) || structural[e.Relation] {
			continue
		}
		c := connection(g, e)
		c.Note = fmt.Sprintf("Bridges community %d → community %d", cu, cv)
		crossings = append(crossings, crossing{c, [2]int{min(cu, cv), max(cu, cv)}})
	}
	order := map[string]int{"AMBIGUOUS": 0, "INFERRED": 1, "EXTRACTED": 2}
	rank := func(c crossing) int {
		if r, ok := order[c.Confidence]; ok {
			return r
		}
		return 3
	}
	sort.SliceStable(crossings, func(i, j int) bool { return rank(crossings[i]) < rank(crossings[j]) })
	// One edge per pair of communities; otherwise a single central node would
	// supply every result.
	seen := map[[2]int]bool{}
	for _, c := range crossings {
		if len(out) >= topN {
			break
		}
		if !seen[c.pair] {
			seen[c.pair] = true
			out = append(out, c.Connection)
		}
	}
	return out
}
