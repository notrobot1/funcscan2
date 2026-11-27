package report

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/notrobot1/funcscan2/analyze"
)

// maxNodesPerCommunity bounds how many node names of one community go into
// the page, so that a very large graph still gives a page a browser opens.
const maxNodesPerCommunity = 400

// HTML renders the report as one self-contained HTML page: no scripts,
// styles, fonts or images are loaded from anywhere else, so the file can be
// opened offline, attached to a ticket or kept next to the code. It follows
// the reader's light or dark setting.
func (r *Report) HTML() string {
	var out bytes.Buffer
	if err := page.Execute(&out, r.view()); err != nil {
		// The template is fixed and the data is plain values; a failure here
		// is a bug in this package, not something a caller can act on.
		panic("report: rendering HTML: " + err.Error())
	}
	return out.String()
}

// view is the report arranged for the page.
type view struct {
	*Report
	Main, Hints  []analyze.Question
	NoSignal     string
	MaxDegree    int
	ListedForWeb []webCommunity
	IsolatedTop  []string
	IsolatedMore int
	Reason       string
}

type webCommunity struct {
	Community
	Shown []string
	More  int
	// Search is what the filter box matches against.
	Search string
}

func (r *Report) view() view {
	v := view{Report: r, Reason: r.isolationReason()}
	if r.noSignal() {
		v.NoSignal = r.Questions[0].Why
	} else {
		v.Main, v.Hints = r.splitQuestions()
	}
	for _, n := range r.GodNodes {
		v.MaxDegree = max(v.MaxDegree, n.Degree)
	}
	for _, c := range r.Listed {
		w := webCommunity{Community: c, Shown: c.Nodes}
		if len(w.Shown) > maxNodesPerCommunity {
			w.More = len(w.Shown) - maxNodesPerCommunity
			w.Shown = w.Shown[:maxNodesPerCommunity]
		}
		w.Search = strings.ToLower(c.Label + " " + strings.Join(w.Shown, " "))
		v.ListedForWeb = append(v.ListedForWeb, w)
	}
	v.IsolatedTop = r.Isolated
	if len(v.IsolatedTop) > 40 {
		v.IsolatedMore = len(v.IsolatedTop) - 40
		v.IsolatedTop = v.IsolatedTop[:40]
	}
	return v
}

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"commas": commas,
	"share": func(part, whole int) int { // width of a bar, in percent
		if whole <= 0 {
			return 0
		}
		return max(1, part*100/whole)
	},
	"percent": func(v float64) int { return int(v*100 + 0.5) },
	"lower":   strings.ToLower,
	"inc":     func(i int) int { return i + 1 },
	"chain": func(files []string) []string { // a cycle, closed back on its first file
		return append(append([]string{}, files...), files[0])
	},
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}).Parse(pageSource))

const pageSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · code graph report</title>
<style>
:root {
  color-scheme: light dark;
  --paper: #eef1f5; --surface: #ffffff; --ink: #16202c; --muted: #566375; --rule: #d3dae3;
  --link: #1f4fa3; --code: #e6ebf2;
  --extracted: #0f766e; --inferred: #b45309; --ambiguous: #be185d;
}
@media (prefers-color-scheme: dark) {
  :root {
    --paper: #11161d; --surface: #19212b; --ink: #e5ebf2; --muted: #98a5b6; --rule: #2b3644;
    --link: #8fb3ff; --code: #232e3b;
    --extracted: #2dd4bf; --inferred: #fbbf24; --ambiguous: #f472b6;
  }
}
* { box-sizing: border-box; }
html { scroll-behavior: smooth; scroll-padding-top: 1rem; }
@media (prefers-reduced-motion: reduce) { html { scroll-behavior: auto; } }
body {
  margin: 0; background: var(--paper); color: var(--ink);
  font: 16px/1.55 system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", sans-serif;
}
code, .mono { font-family: ui-monospace, "SF Mono", "Cascadia Code", Menlo, Consolas, monospace; font-size: .9em; }
code { background: var(--code); padding: .08em .35em; border-radius: 4px; overflow-wrap: anywhere; }
a { color: var(--link); }
a:focus-visible, summary:focus-visible, input:focus-visible { outline: 2px solid var(--link); outline-offset: 2px; }

.layout { display: grid; grid-template-columns: 13rem minmax(0, 54rem); gap: 3rem; max-width: 72rem; margin: 0 auto; padding: 2.5rem 1.5rem 5rem; }
nav { position: sticky; top: 1.5rem; align-self: start; font-size: .92rem; }
nav ol { list-style: none; margin: 0; padding: 0; border-left: 2px solid var(--rule); }
nav a { display: flex; justify-content: space-between; gap: .75rem; padding: .28rem 0 .28rem .9rem; margin-left: -2px; border-left: 2px solid transparent; color: var(--muted); text-decoration: none; }
nav a:hover { color: var(--ink); border-left-color: var(--ink); }
nav .count { font-variant-numeric: tabular-nums; }
@media (max-width: 56rem) {
  .layout { grid-template-columns: minmax(0, 1fr); gap: 1.5rem; padding-top: 1.5rem; }
  nav { position: static; }
  nav ol { display: flex; flex-wrap: wrap; gap: .1rem 1.2rem; border: 0; }
  nav a { padding: .2rem 0; border: 0; margin: 0; }
}

header h1 { font-size: clamp(2rem, 5vw, 3.1rem); line-height: 1.05; letter-spacing: -.02em; margin: 0; overflow-wrap: anywhere; }
header p { margin: .5rem 0 0; color: var(--muted); }
h2 { font-size: 1.35rem; letter-spacing: -.01em; margin: 3.2rem 0 .3rem; }
h2 + .lede { margin: 0 0 1.1rem; color: var(--muted); max-width: 44rem; }
ul, ol { padding-left: 1.2rem; }
.plain { list-style: none; padding: 0; margin: 0; }

/* How the edges were established: the one figure that says how far to trust the rest. */
.trust { margin: 2rem 0 0; }
.trust .bar { display: flex; height: 1.1rem; border-radius: 999px; overflow: hidden; background: var(--rule); }
.trust .bar span { min-width: 2px; }
.extracted { background: var(--extracted); } .inferred { background: var(--inferred); } .ambiguous { background: var(--ambiguous); }
.trust dl { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); gap: .9rem 1.5rem; margin: .9rem 0 0; }
.trust dt { display: flex; align-items: center; gap: .45rem; font-weight: 600; }
.trust dt i { width: .7rem; height: .7rem; border-radius: 50%; flex: none; }
.trust dd { margin: .1rem 0 0 1.15rem; color: var(--muted); font-size: .92rem; }

.facts { margin: 1.6rem 0 0; padding: 0; list-style: none; display: flex; flex-wrap: wrap; gap: .4rem 2.2rem; }
.facts b { font-size: 1.5rem; font-variant-numeric: tabular-nums; letter-spacing: -.01em; margin-right: .35rem; }
.note { color: var(--muted); font-size: .92rem; }
.warning { border-left: 3px solid var(--inferred); padding: .5rem .9rem; background: var(--surface); margin: 1rem 0 0; }

.ranked { list-style: none; padding: 0; margin: 0; counter-reset: rank; }
.ranked li { counter-increment: rank; display: grid; grid-template-columns: 1.6rem minmax(0, 1fr) auto; gap: .2rem .8rem; align-items: baseline; padding: .5rem 0; border-top: 1px solid var(--rule); }
.ranked li::before { content: counter(rank); color: var(--muted); font-variant-numeric: tabular-nums; }
.ranked .meter { grid-column: 2 / 4; height: 4px; background: var(--rule); border-radius: 2px; }
.ranked .meter span { display: block; height: 100%; background: var(--ink); border-radius: 2px; }
.ranked code { justify-self: start; }
.ranked .degree { color: var(--muted); font-variant-numeric: tabular-nums; }

.links > li { padding: .75rem 0; border-top: 1px solid var(--rule); }
.links .rel { color: var(--muted); white-space: nowrap; }
.links .files, .links .why { margin: .25rem 0 0; color: var(--muted); font-size: .92rem; overflow-wrap: anywhere; }
.tag { font-size: .78rem; font-weight: 600; padding: .1em .55em; border-radius: 999px; border: 1px solid currentColor; white-space: nowrap; }
.tag.extracted { color: var(--extracted); background: none; } .tag.inferred { color: var(--inferred); background: none; } .tag.ambiguous { color: var(--ambiguous); background: none; }

.filter { display: block; width: 100%; max-width: 26rem; margin: 0 0 1rem; padding: .55rem .8rem; font: inherit; color: inherit; background: var(--surface); border: 1px solid var(--rule); border-radius: 8px; }
details.community { background: var(--surface); border: 1px solid var(--rule); border-radius: 10px; margin: 0 0 .5rem; }
details.community summary { display: grid; grid-template-columns: minmax(0, 1fr) auto 7rem; gap: 1rem; align-items: center; padding: .7rem 1rem; cursor: pointer; list-style: none; }
details.community summary::-webkit-details-marker { display: none; }
details.community summary strong { overflow-wrap: anywhere; }
details.community .size { color: var(--muted); font-size: .9rem; white-space: nowrap; }
.cohesion { display: flex; align-items: center; gap: .5rem; font-size: .85rem; color: var(--muted); font-variant-numeric: tabular-nums; }
.cohesion .track { flex: 1; height: 6px; background: var(--rule); border-radius: 3px; overflow: hidden; }
.cohesion .track span { display: block; height: 100%; background: var(--extracted); }
details.community .members { padding: 0 1rem 1rem; display: flex; flex-wrap: wrap; gap: .35rem; }
.empty { color: var(--muted); }
@media (max-width: 34rem) { details.community summary { grid-template-columns: minmax(0, 1fr) auto; } .cohesion { grid-column: 1 / 3; } }

.questions > li { padding: .7rem 0; border-top: 1px solid var(--rule); }
.questions p { margin: .2rem 0 0; color: var(--muted); font-size: .92rem; }
footer { margin-top: 4rem; padding-top: 1rem; border-top: 1px solid var(--rule); color: var(--muted); font-size: .88rem; }
</style>
</head>
<body>
<div class="layout">
<nav aria-label="Sections">
  <ol>
    <li><a href="#summary">Summary</a></li>
    <li><a href="#central">Most connected <span class="count">{{len .GodNodes}}</span></a></li>
    <li><a href="#surprises">Surprising connections <span class="count">{{len .Surprises}}</span></a></li>
    {{if .HasCode}}<li><a href="#cycles">Import cycles <span class="count">{{len .Cycles}}</span></a></li>{{end}}
    <li><a href="#communities">Communities <span class="count">{{.Shown}}</span></a></li>
    {{if .Ambiguous}}<li><a href="#ambiguous">Ambiguous edges <span class="count">{{len .Ambiguous}}</span></a></li>{{end}}
    {{if or .Isolated .Thin}}<li><a href="#gaps">Gaps</a></li>{{end}}
    {{if .Questions}}<li><a href="#questions">Questions</a></li>{{end}}
  </ol>
</nav>

<main>
<header id="summary">
  <h1>{{.Title}}</h1>
  <p>Code graph report, {{.Date}}{{if .Commit}}, built from commit <code>{{printf "%.8s" .Commit}}</code>{{end}}</p>
</header>

<ul class="facts">
  <li><b>{{commas .Nodes}}</b>{{plural .Nodes "node" "nodes"}}</li>
  <li><b>{{commas .Edges}}</b>{{plural .Edges "edge" "edges"}}</li>
  <li><b>{{commas .Communities}}</b>{{plural .Communities "community" "communities"}}</li>
  {{with .Corpus}}{{if not .Warning}}<li><b>{{commas .Files}}</b>{{plural .Files "file" "files"}}</li>{{end}}{{end}}
</ul>
{{if .Thin}}<p class="note">{{.Shown}} {{plural .Shown "community is" "communities are"}} listed below; {{.Thin}} with fewer than {{.MinCommunitySize}} declarations {{plural .Thin "is" "are"}} left out.</p>{{end}}
{{with .Corpus}}
  {{if .Warning}}<p class="warning">{{.Warning}}</p>{{end}}
  {{if .Unclassified}}<p class="note">{{len .Unclassified}} {{plural (len .Unclassified) "file is" "files are"}} of no supported kind and not in the graph.</p>{{end}}
  {{if .UnclassifiedWarning}}<p class="warning">{{.UnclassifiedWarning}}</p>{{end}}
{{end}}

<section class="trust" aria-label="How the edges were established">
  <div class="bar" role="img" aria-label="{{.ExtractedPct}}% extracted, {{.InferredPct}}% inferred, {{.AmbiguousPct}}% ambiguous">
    <span class="extracted" style="width:{{.ExtractedPct}}%"></span><span class="inferred" style="width:{{.InferredPct}}%"></span><span class="ambiguous" style="width:{{.AmbiguousPct}}%"></span>
  </div>
  <dl>
    <div><dt><i class="extracted"></i>{{.ExtractedPct}}% extracted</dt><dd>Stated in the source: a call, an import, a declaration.</dd></div>
    <div><dt><i class="inferred"></i>{{.InferredPct}}% inferred</dt><dd>Worked out from names and types{{if .InferredEdges}}; {{commas .InferredEdges}} {{plural .InferredEdges "edge" "edges"}}, average confidence {{printf "%.2f" .InferredAvg}}{{end}}.</dd></div>
    <div><dt><i class="ambiguous"></i>{{.AmbiguousPct}}% ambiguous</dt><dd>The extractor was unsure. Check before relying on these.</dd></div>
  </dl>
</section>
{{with .Tokens}}<p class="note">Language-model cost: {{commas .Input}} input and {{commas .Output}} output tokens.</p>{{end}}

<section id="central">
  <h2>Most connected</h2>
  <p class="lede">The declarations most of the code touches: the core abstractions. Files and method stubs are left out.</p>
  {{if .GodNodes}}<ol class="ranked">
    {{range .GodNodes}}<li><code>{{.Label}}</code><span class="degree">{{commas .Degree}} {{plural .Degree "edge" "edges"}}</span><div class="meter"><span style="width:{{share .Degree $.MaxDegree}}%"></span></div></li>
    {{end}}</ol>{{else}}<p class="empty">No declaration stands out: the graph holds only files or stubs.</p>{{end}}
</section>

<section id="surprises">
  <h2>Surprising connections</h2>
  <p class="lede">Links you would not guess from how the files are laid out.</p>
  {{if .Surprises}}<ul class="plain links">
    {{range .Surprises}}<li>
      <code>{{.Source}}</code> <span class="rel">{{.Relation}} →</span> <code>{{.Target}}</code>
      <span class="tag {{lower .Confidence}}">{{lower .Confidence}}</span>
      <p class="files">{{index .SourceFiles 0}} → {{index .SourceFiles 1}}</p>
      {{if .Why}}<p class="why">{{.Why}}</p>{{end}}{{if .Note}}<p class="why">{{.Note}}</p>{{end}}
    </li>{{end}}
  </ul>{{else}}<p class="empty">None found: every connection stays within one source file.</p>{{end}}
</section>

{{if .HasCode}}<section id="cycles">
  <h2>Import cycles</h2>
  <p class="lede">Files that import each other in a circle, shortest circles first.</p>
  {{if .Cycles}}<ul class="plain links">
    {{range .Cycles}}<li><span class="note">{{.Length}} files</span><p class="files mono">{{range $i, $f := chain .Files}}{{if $i}} → {{end}}{{$f}}{{end}}</p></li>{{end}}
  </ul>{{else}}<p class="empty">None found.</p>{{end}}
</section>{{end}}

<section id="communities">
  <h2>Communities</h2>
  <p class="lede">Groups of declarations tied to each other more than to the rest. Cohesion is the share of possible links inside a group that exist.</p>
  {{if .ListedForWeb}}
  <input class="filter" type="search" placeholder="Filter by community or declaration name" aria-label="Filter communities" hidden>
  {{range .ListedForWeb}}<details class="community" data-search="{{.Search}}">
    <summary>
      <strong>{{.Label}}</strong>
      <span class="size">{{len .Nodes}} declarations</span>
      <span class="cohesion"><span class="track"><span style="width:{{percent .Cohesion}}%"></span></span>{{printf "%.2f" .Cohesion}}</span>
    </summary>
    <div class="members">{{range .Shown}}<code>{{.}}</code>{{end}}{{if .More}}<span class="note">and {{.More}} more</span>{{end}}</div>
  </details>
  {{end}}
  <p class="empty" id="no-match" hidden>No community matches.</p>
  {{else}}<p class="empty">No community has {{.MinCommunitySize}} or more declarations.</p>{{end}}
</section>

{{if .Ambiguous}}<section id="ambiguous">
  <h2>Ambiguous edges</h2>
  <p class="lede">Connections the extractor could not settle. Each is worth a look.</p>
  <ul class="plain links">
    {{range .Ambiguous}}<li><code>{{.Source}}</code> <span class="rel">{{if .Relation}}{{.Relation}}{{else}}related{{end}} →</span> <code>{{.Target}}</code><p class="files">{{.SourceFile}}</p></li>{{end}}
  </ul>
</section>{{end}}

{{if or .Isolated .Thin}}<section id="gaps">
  <h2>Gaps</h2>
  {{if .Isolated}}
  <p class="lede">{{len .Isolated}} {{plural (len .Isolated) "declaration has" "declarations have"}} at most one connection: {{.Reason}}. Counting files and stubs too, {{.WeaklyConnected}} {{plural .WeaklyConnected "node has" "nodes have"}}.</p>
  <p>{{range .IsolatedTop}}<code>{{.}}</code> {{end}}{{if .IsolatedMore}}<span class="note">and {{.IsolatedMore}} more</span>{{end}}</p>
  {{end}}
  {{if .Thin}}<p class="note">{{.Thin}} {{plural .Thin "community" "communities"}} with fewer than {{.MinCommunitySize}} declarations {{plural .Thin "is" "are"}} not listed in this report.</p>{{end}}
  {{if gt .AmbiguousPct 20}}<p class="warning">{{.AmbiguousPct}}% of the edges are ambiguous. Review them before drawing conclusions from this graph.</p>{{end}}
</section>{{end}}

{{if .Questions}}<section id="questions">
  <h2>Questions worth asking</h2>
  {{if .NoSignal}}<p class="empty">{{.NoSignal}}</p>{{else}}
  <p class="lede">What the shape of the graph suggests looking into.</p>
  <ul class="plain questions">
    {{range .Main}}{{if .Question}}<li><strong>{{.Question}}</strong><p>{{.Why}}</p></li>{{end}}{{end}}
  </ul>
  {{if .Hints}}<h3>From ambiguous edges</h3>
  <p class="note">The extractor was unsure about these connections. Verify before acting on them.</p>
  <ul class="plain questions">
    {{range .Hints}}{{if .Question}}<li><strong>{{.Question}}</strong><p>{{.Why}}</p></li>{{end}}{{end}}
  </ul>{{end}}
  {{end}}
</section>{{end}}

<footer>Generated by funcscan on {{.Date}}.</footer>
</main>
</div>
<script>
(function () {
  var box = document.querySelector(".filter");
  if (!box) return;
  var groups = [].slice.call(document.querySelectorAll("details.community"));
  var none = document.getElementById("no-match");
  box.hidden = false;
  box.addEventListener("input", function () {
    var words = box.value.toLowerCase().split(/\s+/).filter(Boolean), shown = 0;
    groups.forEach(function (g) {
      var hay = g.getAttribute("data-search");
      var match = words.every(function (w) { return hay.indexOf(w) !== -1; });
      g.hidden = !match;
      if (match) shown++;
    });
    none.hidden = shown !== 0;
  });
})();
</script>
</body>
</html>
`
