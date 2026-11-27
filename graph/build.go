package graph

import (
	"sort"
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
)

var (
	// importRelations are the edges whose target may lie outside the graph: a
	// standard-library or third-party module.
	importRelations = map[string]bool{"imports": true, "imports_from": true, "re_exports": true}
	// membershipRelations tie a member to what it belongs to...
	membershipRelations = map[string]bool{"method": true, "contains": true}
	// ...and usageRelations are a member being used by it.
	usageRelations = map[string]bool{
		"calls": true, "indirect_call": true, "instantiates": true,
		"references_constant": true, "uses_static_prop": true,
	}
	// genericRelations say only that two nodes have to do with each other.
	genericRelations = map[string]bool{"references": true, "mentions": true, "uses": true}
	confidenceRank   = map[string]int{"EXTRACTED": 3, "INFERRED": 2, "AMBIGUOUS": 1}
)

// FromExtract builds the graph from the nodes and edges that
// extractors.Extract returns. The nodes are copied; the result is not
// changed.
//
// An edge to an id no node carries is left out — except an import: its target
// is an external module, which becomes a node of its own (FileType "concept",
// Type "external"), so that the files importing the same module are tied
// together through it. An import of a file by itself is left out too.
//
// Only one edge fits between two nodes. When the extraction has several, the
// one that says the most stands:
//
//   - the same relation both ways (a calls b, b calls a) keeps the first
//     direction; for imports the edge is marked ReverseImport;
//   - a member's own use of what it belongs to does not replace the
//     membership edge (method, contains);
//   - a generic relation (references, uses) does not replace a specific one
//     (calls);
//   - of two edges of one relation, the more confident stands, then the one
//     with a source location, then the higher confidence score;
//   - otherwise the later edge stands, edges being taken in order of source,
//     target and relation.
//
// File nodes that share a base name are relabelled with as much of their
// directory as tells them apart ("svc/doc.go", "util/doc.go").
func FromExtract(nodes []*base.Node, edges []*base.Edge) *Graph {
	g := New()
	for _, n := range nodes {
		if n != nil && n.ID != "" {
			c := *n
			g.SetNode(&c)
		}
	}

	sorted := append([]*base.Edge(nil), edges...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Relation < b.Relation
	})
	for _, in := range sorted {
		if in.Source == "" || in.Target == "" || in.Relation == "" || !g.Has(in.Source) {
			continue
		}
		isImport := importRelations[in.Relation]
		if !g.Has(in.Target) {
			if !isImport {
				continue
			}
			g.SetNode(&Node{ID: in.Target, Label: in.Target, FileType: "concept", Type: "external"})
		}
		if in.Source == in.Target && isImport {
			continue
		}
		e := &Edge{
			Source: in.Source, Target: in.Target,
			Relation: in.Relation, Context: in.Context,
			Confidence: in.Confidence, ConfidenceScore: in.ConfidenceScore, Weight: in.Weight,
			SourceFile: strings.ReplaceAll(in.SourceFile, `\`, "/"), SourceLocation: in.SourceLocation,
			Origin: in.Origin,
		}
		if e.Weight <= 0 {
			e.Weight = 1
		}
		if e.SourceFile == "" {
			if e.SourceFile = g.Node(e.Source).SourceFile; e.SourceFile == "" {
				e.SourceFile = g.Node(e.Target).SourceFile
			}
		}
		if existing := g.Edge(e.Source, e.Target); existing != nil {
			if !replaces(e, existing) {
				continue
			}
			// What only the earlier edge knew stays known.
			e.ReverseImport = e.ReverseImport || existing.ReverseImport
			if e.Context == "" {
				e.Context = existing.Context
			}
			if e.Relation != existing.Relation {
				if e.ConfidenceScore == 0 {
					e.ConfidenceScore = existing.ConfidenceScore
				}
				if e.SourceLocation == "" {
					e.SourceLocation = existing.SourceLocation
				}
			}
		}
		g.SetEdge(e)
	}
	relabelFileNodes(g)
	return g
}

// replaces decides whether the incoming edge takes the place of the one
// already between its two nodes. It may mark the existing edge instead.
func replaces(incoming, existing *Edge) bool {
	reversed := existing.Source == incoming.Target && existing.Target == incoming.Source
	if reversed && existing.Relation == incoming.Relation {
		// The other half of a two-file import cycle must stay visible.
		if incoming.Relation == "imports_from" || incoming.Relation == "re_exports" {
			existing.ReverseImport = true
		}
		return false
	}
	if reversed && membershipRelations[existing.Relation] && usageRelations[incoming.Relation] {
		return false
	}
	if genericRelations[incoming.Relation] && !genericRelations[existing.Relation] {
		return false
	}
	if existing.Relation != incoming.Relation {
		return true
	}
	have, got := confidenceRank[existing.Confidence], confidenceRank[incoming.Confidence]
	if have != got {
		return got > have
	}
	if (existing.Origin == "ast") != (incoming.Origin == "ast") {
		return incoming.Origin == "ast"
	}
	if existing.SourceLocation != "" && incoming.SourceLocation == "" {
		return false
	}
	if existing.ConfidenceScore != 0 && incoming.ConfidenceScore != 0 &&
		existing.ConfidenceScore > incoming.ConfidenceScore {
		return false
	}
	return true
}

// IsFileLabel reports whether label is the label of the file node of
// sourceFile: the file's base name, or the directory-qualified form
// FromExtract gives to files that share a base name.
func IsFileLabel(label, sourceFile string) bool {
	if label == "" || sourceFile == "" {
		return false
	}
	sourceFile = strings.ReplaceAll(sourceFile, `\`, "/")
	if label == sourceFile[strings.LastIndex(sourceFile, "/")+1:] {
		return true
	}
	return strings.Contains(label, "/") && (sourceFile == label || strings.HasSuffix(sourceFile, "/"+label))
}

// relabelFileNodes gives file nodes that share a base name the shortest
// directory-qualified label that is theirs alone.
func relabelFileNodes(g *Graph) {
	type member struct {
		node *Node
		file string
	}
	groups := map[string][]member{}
	for _, n := range g.nodes {
		if IsFileLabel(n.Label, n.SourceFile) {
			file := strings.ReplaceAll(n.SourceFile, `\`, "/")
			name := file[strings.LastIndex(file, "/")+1:]
			groups[name] = append(groups[name], member{n, file})
		}
	}
	for _, members := range groups {
		parts := map[string][]string{}
		for _, m := range members {
			if _, seen := parts[m.file]; !seen {
				parts[m.file] = strings.FieldsFunc(m.file, func(r rune) bool { return r == '/' })
			}
		}
		if len(parts) < 2 {
			continue
		}
		suffixCount := map[string]int{}
		for _, p := range parts {
			for k := 1; k <= len(p); k++ {
				suffixCount[strings.Join(p[len(p)-k:], "/")]++
			}
		}
		for _, m := range members {
			p := parts[m.file]
			label := strings.Join(p, "/")
			for k := 1; k <= len(p); k++ {
				if suffix := strings.Join(p[len(p)-k:], "/"); suffixCount[suffix] == 1 {
					label = suffix
					break
				}
			}
			m.node.Label = label
		}
	}
}
