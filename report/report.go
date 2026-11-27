// Package report turns a code graph and its analysis into a report a person
// can read: what the graph holds, the nodes everything hangs on, the
// connections worth a second look, the communities, and the gaps.
//
//	g := graph.FromExtract(result.Nodes, result.Edges)
//	communities := cluster.Cluster(g, cluster.Options{})
//	r := report.New(g, communities, report.Options{Root: root})
//	err := r.WriteHTML("graph-report.html")   // or r.HTML(), r.Markdown()
//
// New computes whatever analysis it is not handed. HTML gives one
// self-contained page with no external resources; Markdown gives the same
// content as plain text.
package report

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/notrobot1/funcscan2/analyze"
	"github.com/notrobot1/funcscan2/cluster"
	"github.com/notrobot1/funcscan2/graph"
)

// Corpus describes the files the graph was built from.
type Corpus struct {
	Files int
	Words int
	// Warning, when set, replaces the file and word counts: the corpus is too
	// small, say, for a graph to tell much.
	Warning string
	// Unclassified lists files that were found but are of no supported kind,
	// and so are not in the graph.
	Unclassified []string
	// UnclassifiedWarning says so in words when they are a large part of the
	// corpus.
	UnclassifiedWarning string
}

// TokenCost is what building the graph cost in language-model tokens.
type TokenCost struct{ Input, Output int }

// Options say what the report is about and may carry analysis that has
// already been done. Every field is optional.
type Options struct {
	// Root is the project directory; its base name titles the report.
	Root string
	// Date dates the report. Zero means today.
	Date time.Time
	// Commit is the revision the graph was built from.
	Commit string

	Corpus *Corpus
	Tokens *TokenCost

	// MinCommunitySize is the fewest real nodes a community needs to be
	// listed. Zero means 3.
	MinCommunitySize int

	// Labels names the communities; nil means hub names (cluster.LabelByHub).
	// Pass the result of naming.Communities for model-given names.
	Labels map[int]string
	// Cohesion, GodNodes, Surprises and Questions are computed when nil.
	Cohesion  map[int]float64
	GodNodes  []analyze.GodNode
	Surprises []analyze.Connection
	Questions []analyze.Question
}

// Community is one community as the report lists it.
type Community struct {
	ID       int
	Label    string
	Cohesion float64
	// Nodes are the labels of its real nodes: declarations, not files or
	// method stubs.
	Nodes []string
}

// AmbiguousEdge is an edge the extractor was unsure of.
type AmbiguousEdge struct {
	Source, Target string
	SourceFile     string
	Relation       string
}

// Report is everything the report says, ready to be rendered.
type Report struct {
	Title  string
	Date   string
	Commit string
	Corpus *Corpus
	Tokens *TokenCost

	Nodes, Edges int
	// Communities counts all communities; Shown those listed, Thin those left
	// out for having fewer than MinCommunitySize real nodes.
	Communities, Shown, Thin int
	MinCommunitySize         int

	// Shares of the edges by how they were established, in whole percent.
	ExtractedPct, InferredPct, AmbiguousPct int
	ExtractedEdges, InferredEdges           int
	// InferredAvg is the mean confidence score of the inferred edges, 0 when
	// there are none.
	InferredAvg float64

	// Hubs are the names of the communities that hold a real node.
	Hubs      []string
	GodNodes  []analyze.GodNode
	Surprises []analyze.Connection
	// HasCode says whether import cycles are meaningful for this graph.
	HasCode bool
	Cycles  []analyze.Cycle

	Listed    []Community
	Ambiguous []AmbiguousEdge

	// Isolated are the labels of the real nodes with at most one edge;
	// WeaklyConnected counts every node with at most one edge, files and
	// stubs included.
	Isolated        []string
	WeaklyConnected int
	// HasDocuments says whether the graph holds document, paper or image
	// nodes, which changes what an isolated node may mean.
	HasDocuments bool

	Questions []analyze.Question
}

// New builds the report for a graph and its communities.
func New(g *graph.Graph, communities map[int][]string, opts Options) *Report {
	date := opts.Date
	if date.IsZero() {
		date = time.Now()
	}
	minSize := opts.MinCommunitySize
	if minSize <= 0 {
		minSize = 3
	}
	labels := opts.Labels
	if labels == nil {
		labels = cluster.LabelByHub(g, communities)
	}
	cohesion := opts.Cohesion
	if cohesion == nil {
		cohesion = cluster.ScoreAll(g, communities)
	}
	r := &Report{
		Title:            rootLabel(opts.Root),
		Date:             date.Format("2006-01-02"),
		Commit:           opts.Commit,
		Corpus:           opts.Corpus,
		Tokens:           opts.Tokens,
		Nodes:            g.NumNodes(),
		Edges:            g.NumEdges(),
		Communities:      len(communities),
		MinCommunitySize: minSize,
		GodNodes:         opts.GodNodes,
		Surprises:        opts.Surprises,
		Questions:        opts.Questions,
	}
	if r.GodNodes == nil {
		r.GodNodes = analyze.GodNodes(g, 10, 0)
	}
	if r.Surprises == nil {
		r.Surprises = analyze.SurprisingConnections(g, communities, 5)
	}
	if r.Questions == nil {
		r.Questions = analyze.SuggestQuestions(g, communities, labels, 7)
	}

	edges := g.Edges()
	ambiguous, scoreSum := 0, 0.0
	for _, e := range edges {
		switch e.Confidence {
		case "INFERRED":
			r.InferredEdges++
			if e.ConfidenceScore > 0 {
				scoreSum += e.ConfidenceScore
			} else {
				scoreSum += 0.5
			}
		case "AMBIGUOUS":
			ambiguous++
			r.Ambiguous = append(r.Ambiguous, AmbiguousEdge{name(g, e.U), name(g, e.V), e.SourceFile, e.Relation})
		case "EXTRACTED", "":
			r.ExtractedEdges++
		}
		if e.Relation == "imports" || e.Relation == "imports_from" {
			r.HasCode = true
		}
	}
	percent := func(n int) int {
		return int(math.RoundToEven(float64(n) / float64(max(len(edges), 1)) * 100))
	}
	r.ExtractedPct, r.InferredPct, r.AmbiguousPct = percent(r.ExtractedEdges), percent(r.InferredEdges), percent(ambiguous)
	if r.InferredEdges > 0 {
		r.InferredAvg = math.Round(scoreSum/float64(r.InferredEdges)*100) / 100
	}

	// One definition of a "real" node for every figure in the report: a
	// declaration a reader would recognise, not a file, a method stub, a
	// node without a source, or a fragment of documentation.
	real := func(id string) bool {
		return g.Has(id) && !analyze.IsFileNode(g, id) && !analyze.IsConceptNode(g, id) &&
			g.Node(id).FileType != "rationale"
	}
	ids := make([]int, 0, len(communities))
	for cid := range communities {
		ids = append(ids, cid)
	}
	sort.Ints(ids)
	for _, cid := range ids {
		label, ok := labels[cid]
		if !ok {
			label = "Community " + itoa(cid)
		}
		var nodes []string
		for _, id := range communities[cid] {
			if real(id) {
				nodes = append(nodes, name(g, id))
			}
		}
		if len(nodes) > 0 {
			r.Hubs = append(r.Hubs, label)
		}
		if len(nodes) < minSize {
			r.Thin++
			continue
		}
		r.Shown++
		r.Listed = append(r.Listed, Community{cid, label, cohesion[cid], nodes})
	}

	for _, id := range g.Nodes() {
		n := g.Node(id)
		switch n.FileType {
		case "code":
			r.HasCode = true
		case "document", "paper", "image":
			r.HasDocuments = true
		}
		if g.Degree(id) > 1 {
			continue
		}
		r.WeaklyConnected++
		if real(id) && !analyze.IsContainedOnly(g, id) {
			r.Isolated = append(r.Isolated, name(g, id))
		}
	}
	if r.HasCode {
		r.Cycles = analyze.FindImportCycles(g, 5, 20)
	}
	return r
}

// WriteHTML writes the HTML report to a file.
func (r *Report) WriteHTML(path string) error {
	return os.WriteFile(path, []byte(r.HTML()), 0o644)
}

// WriteMarkdown writes the Markdown report to a file.
func (r *Report) WriteMarkdown(path string) error {
	return os.WriteFile(path, []byte(r.Markdown()), 0o644)
}

// hasGaps says whether the report has anything to list under its gaps.
func (r *Report) hasGaps() bool {
	return len(r.Isolated) > 0 || r.Thin > 0 || r.AmbiguousPct > 20
}

// rootLabel names the project by the base name of its directory, so that a
// report does not carry the path of the machine it was made on.
func rootLabel(root string) string {
	raw := strings.ReplaceAll(root, `\`, "/")
	label := raw[strings.LastIndex(raw, "/")+1:]
	if label == "" || label == "." || label == ".." {
		label = ""
		if abs, err := filepath.Abs(filepath.FromSlash(raw)); err == nil && filepath.Base(abs) != string(filepath.Separator) {
			label = filepath.Base(abs)
		}
	}
	if label == "" {
		return raw
	}
	return label
}

func name(g *graph.Graph, id string) string {
	if label := g.Label(id); label != "" {
		return label
	}
	return id
}
