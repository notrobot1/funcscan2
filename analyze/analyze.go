// Package analyze reads a code graph for what is worth knowing about it: the
// nodes everything hangs on, the connections nobody would guess from the
// file layout, the import cycles, and the questions the graph raises.
//
//	g := graph.FromExtract(result.Nodes, result.Edges)
//	communities := cluster.Cluster(g, cluster.Options{})
//	labels := cluster.LabelByHub(g, communities)
//
//	gods := analyze.GodNodes(g, 10, 0)
//	surprises := analyze.SurprisingConnections(g, communities, 5)
//	questions := analyze.SuggestQuestions(g, communities, labels, 7)
//	cycles := analyze.FindImportCycles(g, 5, 20)
//
// The underlying metrics are available on their own: Degrees, Betweenness
// and EdgeBetweenness here, cohesion in package cluster.
package analyze

import (
	"path"
	"sort"
	"strings"

	"github.com/notrobot1/funcscan2/graph"
)

// builtinNoise are names of language built-ins and standard-library types
// that can turn up as nodes. They are no part of the project and are kept out
// of the ranking of its central nodes.
var builtinNoise = set(
	"str", "int", "float", "bool", "bytes", "bytearray", "complex", "object",
	"True", "False",
	"MagicMock", "Mock", "AsyncMock", "NonCallableMock",
	"NonCallableMagicMock", "PropertyMock", "patch", "sentinel",
	"Path", "Any", "Optional", "List", "Dict", "Set", "Tuple", "Union",
	"Callable", "Type", "ClassVar", "Final", "Literal", "Protocol",
	"Counter", "defaultdict", "OrderedDict", "datetime", "Enum",
	"os", "sys", "re", "json", "io", "abc", "typing",
	"Foundation", "SwiftUI", "UIKit", "AppKit", "Combine",
	"String", "Int", "Double", "Float", "Bool", "Data", "URL", "Date", "UUID",
	"Sendable", "Codable", "Decodable", "Encodable", "Equatable", "Hashable",
	"Identifiable", "Comparable", "AnyObject", "Error", "LocalizedError",
	"NSObject", "NSString", "NSError", "NSLock",
	"View", "Color", "Font", "DispatchQueue",
)

// jsonNoise are keys so common in JSON files that a node named after one
// says nothing.
var jsonNoise = set(
	"start", "end", "name", "id", "type", "properties",
	"value", "key", "data", "items", "title", "description", "version",
	"dependencies", "devdependencies", "peerdependencies",
	"optionaldependencies", "bundleddependencies", "bundledependencies",
)

// langFamily groups file extensions by runtime: languages of one family can
// call each other, languages of different families cannot.
var langFamily = families(map[string][]string{
	"python":   {".py", ".pyw"},
	"js":       {".js", ".jsx", ".mjs", ".cjs", ".ejs", ".ts", ".tsx", ".mts", ".cts", ".vue", ".svelte"},
	"go":       {".go"},
	"rust":     {".rs"},
	"jvm":      {".java", ".kt", ".kts", ".scala"},
	"c":        {".c", ".h", ".cpp", ".cc", ".cxx", ".hpp"},
	"ruby":     {".rb", ".rake"},
	"swift":    {".swift"},
	"dotnet":   {".cs", ".vb"},
	"php":      {".php"},
	"r":        {".r"},
	"cobol":    {".cbl", ".cob", ".cobol", ".cpy"},
	"solidity": {".sol"},
	"erlang":   {".erl", ".hrl", ".escript"},
})

var (
	codeExtensions = set(
		".asd", ".astro", ".bash", ".c", ".cbl", ".cc", ".cjs", ".cl", ".cls", ".cob", ".cobol",
		".cpp", ".cpy", ".cs", ".cshtml", ".csproj", ".cts", ".cu", ".cuh", ".cxx", ".dart", ".dfm",
		".dm", ".dme", ".dmf", ".dmi", ".dmm", ".dpk", ".dpr", ".ejs", ".erl", ".escript", ".ets",
		".ex", ".exs", ".f", ".f03", ".f08", ".f90", ".f95", ".fsproj", ".go", ".gradle", ".groovy",
		".h", ".hcl", ".hpp", ".hrl", ".inc", ".java", ".jl", ".js", ".json", ".jsx", ".kt", ".kts",
		".lfm", ".lisp", ".lpk", ".lpr", ".lsp", ".lua", ".luau", ".m", ".metal", ".mjs", ".ml",
		".mli", ".mm", ".mts", ".pas", ".php", ".pp", ".ps1", ".psd1", ".psm1", ".py", ".r", ".rake",
		".razor", ".rb", ".resource", ".robot", ".rs", ".scala", ".sh", ".sln", ".slnx", ".sol",
		".sql", ".sv", ".svelte", ".svh", ".swift", ".tf", ".tfvars", ".toc", ".trigger", ".ts",
		".tsx", ".v", ".vb", ".vbproj", ".vh", ".vue", ".xaml", ".yaml", ".yml", ".zig",
	)
	paperExtensions = set(".pdf")
	imageExtensions = set(".gif", ".jpeg", ".jpg", ".png", ".svg", ".webp")
)

func set(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

func families(byFamily map[string][]string) map[string]string {
	out := map[string]string{}
	for family, extensions := range byFamily {
		for _, ext := range extensions {
			out[ext] = family
		}
	}
	return out
}

// crossLanguage reports whether the two files are of different language
// families. A file of no known language is of no family.
func crossLanguage(a, b string) bool {
	fa, fb := langFamily[strings.ToLower(path.Ext(a))], langFamily[strings.ToLower(path.Ext(b))]
	return fa != "" && fb != "" && fa != fb
}

// fileCategory sorts a file into "code", "paper", "image" or "doc".
func fileCategory(file string) string {
	ext := ""
	if dot := strings.LastIndex(file, "."); dot >= 0 {
		ext = strings.ToLower(file[dot:])
	}
	switch {
	case codeExtensions[ext]:
		return "code"
	case paperExtensions[ext]:
		return "paper"
	case imageExtensions[ext]:
		return "image"
	}
	return "doc"
}

// topLevelDir is the first directory of a path, "." for a file at the root.
func topLevelDir(file string) string {
	file = strings.ReplaceAll(file, `\`, "/")
	for strings.HasPrefix(file, "./") {
		file = file[2:]
	}
	if slash := strings.Index(file, "/"); slash >= 0 {
		return file[:slash]
	}
	return "."
}

// name is what a node is called in a result: its label, or its id when it has
// none.
func name(g *graph.Graph, id string) string {
	if label := g.Label(id); label != "" {
		return label
	}
	return id
}

// communityOf inverts communities: node id -> community number.
func communityOf(communities map[int][]string) map[string]int {
	out := map[string]int{}
	for cid, nodes := range communities {
		for _, id := range nodes {
			out[id] = cid
		}
	}
	return out
}

// IsFileNode reports a node that stands for the mechanics of the extraction
// rather than for an idea of the program: a file, a method stub
// (".Close()"), or a function that nothing but its file connects to. Such
// nodes gather edges by construction and are left out of every ranking.
func IsFileNode(g *graph.Graph, id string) bool {
	n := g.Node(id)
	if n.Label == "" {
		return false
	}
	if graph.IsFileLabel(n.Label, n.SourceFile) {
		return true
	}
	if strings.HasSuffix(n.Label, "()") {
		return strings.HasPrefix(n.Label, ".") || g.Degree(id) <= 1
	}
	return false
}

// IsConceptNode reports a node that was not found in a source file: it has
// no source file, or one without an extension.
func IsConceptNode(g *graph.Graph, id string) bool {
	file := g.Node(id).SourceFile
	return file == "" || !strings.Contains(file[strings.LastIndex(file, "/")+1:], ".")
}

func isJSONKeyNode(g *graph.Graph, id string) bool {
	n := g.Node(id)
	return strings.HasSuffix(strings.ToLower(n.SourceFile), ".json") &&
		jsonNoise[strings.ToLower(strings.TrimSpace(n.Label))]
}

// IsContainedOnly reports an extracted declaration whose only edge is the
// `contains` from its file: a type alias, a constant. That is how such a
// declaration is meant to look, not a gap in the graph.
func IsContainedOnly(g *graph.Graph, id string) bool {
	if g.Node(id).Origin != "ast" || g.Degree(id) != 1 {
		return false
	}
	edges := g.EdgesOf(id)
	return len(edges) == 1 && edges[0].Relation == "contains"
}

// hubThreshold is the degree above which a node counts as a hub for the
// given percentile, computed the way cluster.Cluster computes it.
func hubThreshold(g *graph.Graph, percentile float64) (int, bool) {
	if percentile <= 0 || g.NumNodes() == 0 {
		return 0, false
	}
	degrees := make([]int, 0, g.NumNodes())
	for _, id := range g.Nodes() {
		degrees = append(degrees, g.Degree(id))
	}
	sort.Ints(degrees)
	return degrees[max(0, int(float64(len(degrees))*percentile/100)-1)], true
}

// GodNode is one of the most connected nodes of the graph.
type GodNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Degree int    `json:"degree"`
}

// GodNodes returns the topN most connected nodes that stand for something in
// the program: its core abstractions, most connected first.
//
// Files, method stubs, names of built-ins and nodes without a source are left
// out: they collect edges mechanically. With excludeHubsPercentile above
// zero, nodes whose degree is above that percentile are left out as well, by
// the same rule as cluster.Options.ExcludeHubsPercentile — one setting then
// keeps utility hubs out of both the ranking and the communities.
func GodNodes(g *graph.Graph, topN int, excludeHubsPercentile float64) []GodNode {
	threshold, limited := hubThreshold(g, excludeHubsPercentile)
	out := []GodNode{}
	for _, id := range byDegree(g, g.Nodes()) {
		if len(out) >= topN {
			break
		}
		degree := g.Degree(id)
		if limited && degree > threshold {
			continue
		}
		if IsFileNode(g, id) || IsConceptNode(g, id) || isJSONKeyNode(g, id) || builtinNoise[g.Label(id)] {
			continue
		}
		out = append(out, GodNode{ID: id, Label: name(g, id), Degree: degree})
	}
	return out
}

// byDegree orders the nodes by degree, highest first; nodes of equal degree
// keep the order they came in.
func byDegree(g *graph.Graph, ids []string) []string {
	sort.SliceStable(ids, func(i, j int) bool { return g.Degree(ids[i]) > g.Degree(ids[j]) })
	return ids
}

func sortedCommunities[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for cid := range m {
		out = append(out, cid)
	}
	sort.Ints(out)
	return out
}
