package analyze

import (
	"slices"
	"sort"

	"github.com/notrobot1/funcscan2/graph"
)

// Cycle is a circular dependency between files: each file imports the next,
// and the last imports the first.
type Cycle struct {
	// Files starts at the file whose path sorts first.
	Files  []string `json:"cycle"`
	Length int      `json:"length"`
	Why    string   `json:"why"`
}

// FindImportCycles finds circular imports between files.
//
// Every import edge is taken from the file it was written in to the file of
// the node it reaches, so imports between symbols count for their files. Up
// to topN cycles of at most maxLength files are returned, shortest first —
// two files importing each other are the tightest coupling there is — and
// cycles of the same length in the order of their file paths.
func FindImportCycles(g *graph.Graph, maxLength, topN int) []Cycle {
	// Which files each file imports.
	imports := map[string]map[string]bool{}
	link := func(from, to string) {
		if imports[from] == nil {
			imports[from] = map[string]bool{}
		}
		imports[from][to] = true
	}
	for _, e := range g.Edges() {
		if (e.Relation != "imports_from" && e.Relation != "re_exports") || e.SourceFile == "" {
			continue
		}
		// The importing file is the one the edge was found in; the imported
		// file is that of the node at the other end.
		fileU, fileV := g.Node(e.U).SourceFile, g.Node(e.V).SourceFile
		var imported string
		switch {
		case fileU == e.SourceFile:
			imported = fileV
		case fileV == e.SourceFile:
			imported = fileU
		case fileV != "":
			imported = fileV
		default:
			imported = fileU
		}
		if imported == "" {
			continue
		}
		link(e.SourceFile, imported)
		if e.ReverseImport {
			link(imported, e.SourceFile)
		}
	}
	if len(imports) == 0 {
		return []Cycle{}
	}
	sortedImports := make(map[string][]string, len(imports))
	for file, targets := range imports {
		for target := range targets {
			sortedImports[file] = append(sortedImports[file], target)
		}
		sort.Strings(sortedImports[file])
	}
	files := make([]string, 0, len(sortedImports))
	for file := range sortedImports {
		files = append(files, file)
	}
	sort.Strings(files)

	// Cycles are looked for one length at a time, shortest first, each from
	// its first file through files that sort after it — so every cycle is
	// found exactly once, already in its final form, and the search can stop
	// as soon as there are enough.
	var found [][]string
	onPath := map[string]bool{}
	var walk func(start string, path []string, length int)
	walk = func(start string, path []string, length int) {
		if len(found) >= topN {
			return
		}
		last := path[len(path)-1]
		if len(path) == length {
			if imports[last][start] {
				found = append(found, slices.Clone(path))
			}
			return
		}
		for _, next := range sortedImports[last] {
			if next > start && !onPath[next] {
				onPath[next] = true
				walk(start, append(path, next), length)
				onPath[next] = false
			}
		}
	}
	for length := 1; length <= maxLength && len(found) < topN; length++ {
		for _, start := range files {
			walk(start, []string{start}, length)
		}
	}

	out := make([]Cycle, len(found))
	for i, cycle := range found {
		out[i] = Cycle{Files: cycle, Length: len(cycle), Why: "circular dependency"}
	}
	return out
}
