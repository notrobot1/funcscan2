package report

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/notrobot1/funcscan2/analyze"
)

// Markdown renders the report as Markdown.
func (r *Report) Markdown() string {
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	section := func(title string) { lines = append(lines, "", "## "+title) }

	add("# Graph Report - %s  (%s)", r.Title, r.Date)

	if c := r.Corpus; c != nil {
		section("Corpus Check")
		if c.Warning != "" {
			add("- %s", c.Warning)
		} else {
			add("- %d files · ~%s words", c.Files, commas(c.Words))
			add("- Verdict: corpus is large enough that graph structure adds value.")
			if len(c.Unclassified) > 0 {
				add("- Unclassified: %d file(s) not represented in the graph (top: %s)",
					len(c.Unclassified), topExtensions(c.Unclassified, 3))
			}
			if c.UnclassifiedWarning != "" {
				add("- WARNING: %s", c.UnclassifiedWarning)
			}
		}
	}

	section("Summary")
	summary := fmt.Sprintf("- %d nodes · %d edges · %d communities", r.Nodes, r.Edges, r.Communities)
	if r.Thin > 0 {
		summary += fmt.Sprintf(" (%d shown, %d thin omitted)", r.Shown, r.Thin)
	}
	lines = append(lines, summary)
	extraction := fmt.Sprintf("- Extraction: %d%% EXTRACTED · %d%% INFERRED · %d%% AMBIGUOUS", r.ExtractedPct, r.InferredPct, r.AmbiguousPct)
	if r.InferredEdges > 0 {
		extraction += fmt.Sprintf(" · INFERRED: %d edges (avg confidence: %s)", r.InferredEdges, decimal(r.InferredAvg))
	}
	lines = append(lines, extraction)
	if r.Tokens != nil {
		add("- Token cost: %s input · %s output", commas(r.Tokens.Input), commas(r.Tokens.Output))
	}

	if r.Commit != "" {
		section("Graph Freshness")
		add("- Built from commit: `%s`", short(r.Commit))
		add("- Run `git rev-parse HEAD` and compare to check if the graph is stale.")
	}

	if len(r.Hubs) > 0 {
		section("Community Hubs (Navigation)")
		for _, hub := range r.Hubs {
			add("- %s", hub)
		}
	}

	section("God Nodes (most connected - your core abstractions)")
	for i, n := range r.GodNodes {
		add("%d. `%s` - %d edges", i+1, n.Label, n.Degree)
	}

	section("Surprising Connections (you probably didn't know these)")
	if len(r.Surprises) == 0 {
		add("- None detected - all connections are within the same source files.")
	}
	for _, s := range r.Surprises {
		tag := ""
		if s.Relation == "semantically_similar_to" {
			tag = " [semantically similar]"
		}
		add("- `%s` --%s--> `%s`  [%s]%s", s.Source, s.Relation, s.Target, s.Confidence, tag)
		files := fmt.Sprintf("  %s → %s", s.SourceFiles[0], s.SourceFiles[1])
		if s.Note != "" {
			files += "  _" + s.Note + "_"
		}
		lines = append(lines, files)
	}

	if r.HasCode {
		section("Import Cycles")
		if len(r.Cycles) == 0 {
			add("- None detected.")
		}
		for _, c := range r.Cycles {
			add("- %d-file cycle: `%s`", c.Length, strings.Join(append(append([]string{}, c.Files...), c.Files[0]), " -> "))
		}
	}

	section(fmt.Sprintf("Communities (%d total, %d thin omitted)", r.Communities, r.Thin))
	for _, c := range r.Listed {
		shown, more := c.Nodes, ""
		if len(shown) > 8 {
			more = fmt.Sprintf(" (+%d more)", len(shown)-8)
			shown = shown[:8]
		}
		lines = append(lines, "",
			fmt.Sprintf("### Community %d - \"%s\"", c.ID, c.Label),
			fmt.Sprintf("Cohesion: %.2f", c.Cohesion),
			fmt.Sprintf("Nodes (%d): %s%s", len(c.Nodes), strings.Join(shown, ", "), more),
		)
	}

	if len(r.Ambiguous) > 0 {
		section("Ambiguous Edges - Review These")
		for _, e := range r.Ambiguous {
			relation := e.Relation
			if relation == "" {
				relation = "unknown"
			}
			add("- `%s` → `%s`  [AMBIGUOUS]", e.Source, e.Target)
			add("  %s · relation: %s", e.SourceFile, relation)
		}
	}

	if r.hasGaps() {
		section("Knowledge Gaps")
		if len(r.Isolated) > 0 {
			shown, more := r.Isolated, ""
			if len(shown) > 5 {
				more = fmt.Sprintf(" (+%d more)", len(shown)-5)
				shown = shown[:5]
			}
			add("- **%d isolated node(s):** %s%s", len(r.Isolated), quoted(shown), more)
			add("  These have ≤1 connection - %s. (Counts symbols only; %d node(s) total have ≤1 connection when file, concept and rationale nodes are included.)",
				r.isolationReason(), r.WeaklyConnected)
		}
		if r.Thin > 0 {
			add("- **%d thin communities (<%d nodes) omitted from report**", r.Thin, r.MinCommunitySize)
		}
		if r.AmbiguousPct > 20 {
			add("- **High ambiguity: %d%% of edges are AMBIGUOUS.** Review the Ambiguous Edges section above.", r.AmbiguousPct)
		}
	}

	if len(r.Questions) > 0 {
		section("Suggested Questions")
		main, hints := r.splitQuestions()
		if r.noSignal() {
			add("_%s_", r.Questions[0].Why)
		} else {
			lines = append(lines, "_Questions this graph is uniquely positioned to answer:_", "")
			ask := func(questions []analyze.Question) {
				for _, q := range questions {
					if q.Question != "" {
						add("- **%s**", q.Question)
						add("  _%s_", q.Why)
					}
				}
			}
			ask(main)
			if len(hints) > 0 {
				lines = append(lines, "", "### Low-confidence Hints",
					"_AMBIGUOUS edges — the extractor was unsure. Verify before acting on these._", "")
				ask(hints)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (r *Report) isolationReason() string {
	if r.HasDocuments {
		return "possible missing edges or undocumented components"
	}
	return "possible missing edges"
}

func (r *Report) noSignal() bool {
	return len(r.Questions) == 1 && r.Questions[0].Type == analyze.NoSignal
}

// splitQuestions separates the questions raised by doubtful edges from the
// rest, so that they do not crowd them.
func (r *Report) splitQuestions() (main, hints []analyze.Question) {
	for _, q := range r.Questions {
		if q.LowConfidence {
			hints = append(hints, q)
		} else {
			main = append(main, q)
		}
	}
	return main, hints
}

func quoted(labels []string) string {
	out := make([]string, len(labels))
	for i, label := range labels {
		out[i] = "`" + label + "`"
	}
	return strings.Join(out, ", ")
}

// topExtensions names the n commonest file extensions among the paths, with
// their counts.
func topExtensions(paths []string, n int) string {
	counts := map[string]int{}
	var order []string
	for _, p := range paths {
		ext := path.Ext(strings.ReplaceAll(p, `\`, "/"))
		if ext == "" {
			ext = "(none)"
		}
		if counts[ext] == 0 {
			order = append(order, ext)
		}
		counts[ext]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	var parts []string
	for i := 0; i < len(order) && i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s %d", order[i], counts[order[i]]))
	}
	return strings.Join(parts, ", ")
}

// commas writes a number with its thousands separated: 1,234,567.
func commas(n int) string {
	s := strconv.Itoa(n)
	sign := ""
	if n < 0 {
		sign, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}

// decimal writes a number with as many digits as it needs, and always with a
// decimal point: 0.85, 1.0.
func decimal(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func itoa(n int) string { return strconv.Itoa(n) }
