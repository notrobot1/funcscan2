package viz

import (
	"sort"
	"strings"
	"unicode"
)

// Layer is a part of an architecture that can be recognised by the words its
// symbols and files are named with. A community whose names hold at least
// two of the Keywords (as whole words, in lower case) joins the layer's
// section; of several layers that fit, the one with the most hits wins, and
// the earlier one in the list on a tie.
type Layer struct {
	// ID is the anchor of the layer's section.
	ID   string
	Name string
	// Keywords are lower-case words.
	Keywords []string
}

// GraphifyLayers are the layers of the graphify code base, the layout the
// original tool applies to every project. They are of little use elsewhere:
// "go", "path" and "test", for instance, are keywords, and match nearly every
// community of a Go project. Kept for output identical to the original.
var GraphifyLayers = []Layer{
	{"extract-pipeline", "Extraction Pipeline", []string{
		"extract", "extractor", "tree", "sitter", "parser", "language",
		"python", "javascript", "typescript", "rust", "java", "go",
		"ast", "calls", "imports", "multilang"}},
	{"build-graph", "Graph Build", []string{
		"build", "graph", "merge", "dedup", "node", "edge", "hyperedge",
		"json", "schema", "normalize", "confidence"}},
	{"analysis-clustering", "Analysis & Clustering", []string{
		"cluster", "community", "leiden", "cohesion", "analyze", "god",
		"surprise", "question", "query", "path", "explain", "benchmark"}},
	{"outputs-docs", "Outputs & Docs", []string{
		"export", "html", "wiki", "obsidian", "canvas", "svg", "graphml",
		"report", "callflow", "mermaid", "tree", "documentation"}},
	{"cli-skills", "CLI & Skill Installers", []string{
		"main", "install", "uninstall", "skill", "agent", "claude",
		"codex", "opencode", "aider", "copilot", "kiro", "vscode",
		"hook", "command"}},
	{"ingest-cache-update", "Ingestion & Updates", []string{
		"ingest", "fetch", "download", "url", "html", "markdown",
		"cache", "manifest", "watch", "update", "incremental",
		"transcribe", "video", "audio", "google"}},
	{"serve-api", "Serving API", []string{
		"serve", "api", "request", "response", "endpoint", "router",
		"handle", "upload", "search", "delete", "enrich"}},
	{"security-global", "Security & Global Graph", []string{
		"security", "safe", "ssrf", "xss", "path", "traversal",
		"global", "prefix", "prune", "repo", "clone"}},
	{"tests-fixtures", "Tests & Fixtures", []string{
		"test", "tests", "fixture", "fixtures", "sample", "assert",
		"pytest", "mock"}},
}

func isWordChar(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') }

// countWord counts the places where word stands in text as a whole word.
func countWord(text, word string) int {
	count := 0
	for from := 0; ; {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return count
		}
		start, end := from+i, from+i+len(word)
		if (start == 0 || !isWordChar(text[start-1])) && (end == len(text) || !isWordChar(text[end])) {
			count++
			from = end
		} else {
			from = start + 1
		}
	}
}

// communityIndex groups the nodes by community, and lists the communities
// largest first.
func communityIndex(nodes []*cfNode) (index map[string][]*cfNode, order []string) {
	index = map[string][]*cfNode{}
	for _, n := range nodes {
		if _, seen := index[n.community]; !seen {
			order = append(order, n.community)
		}
		index[n.community] = append(index[n.community], n)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if a, b := len(index[order[i]]), len(index[order[j]]); a != b {
			return a > b
		}
		return order[i] < order[j]
	})
	return index, order
}

var keywordStopwords = set(
	"the", "and", "for", "with", "from", "this", "that", "class", "function",
	"method", "file", "src", "lib", "core", "index", "main", "init", "py",
	"ts", "tsx", "js", "jsx", "go", "rs", "java", "html", "css",
)

func set(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

// sectionKeywords picks the words that turn up most in the labels and file
// names of the nodes.
func sectionKeywords(nodes []*cfNode, limit int) []string {
	var counts tally
	for _, n := range nodes {
		text := strings.NewReplacer("/", " ", "_", " ", "-", " ").Replace(n.label + " " + n.sourceFile)
		for _, raw := range strings.Fields(text) {
			word := strings.Map(func(r rune) rune {
				if unicode.IsLetter(r) || unicode.IsNumber(r) {
					return r
				}
				return -1
			}, strings.ToLower(raw))
			if len([]rune(word)) >= 3 && !keywordStopwords[word] {
				counts.add(word, 1)
			}
		}
	}
	return counts.mostCommon(limit)
}

func titleCase(word string) string {
	r := []rune(word)
	if len(r) == 0 {
		return word
	}
	return string(unicode.ToUpper(r[0])) + strings.ToLower(string(r[1:]))
}

func communityLabel(cid string, labels map[string]string, nodes []*cfNode) string {
	if label := labels[cid]; label != "" {
		return label
	}
	if keywords := sectionKeywords(nodes, 3); len(keywords) > 0 {
		for i, word := range keywords {
			keywords[i] = titleCase(word)
		}
		return strings.Join(keywords, " ")
	}
	return "Community " + cid
}

// deriveSections arranges the communities into sections when the caller
// names none. A community whose names speak of one of the layers joins that
// layer's section; the largest of the rest get sections of their own; what is
// left over goes into "Other". At most maxSections sections come out, not
// counting the overview.
func deriveSections(nodes []*cfNode, labels map[string]string, maxSections int, layers []Layer) []cfSection {
	index, order := communityIndex(nodes)
	type group struct {
		cfSection
		priority int
	}
	groups := map[string]*group{}
	type loose struct{ cid, label string }
	var unassigned []loose

	for _, cid := range order {
		members := index[cid]
		label := communityLabel(cid, labels, members)
		parts := []string{label}
		for i := 0; i < len(members) && i < 80; i++ {
			parts = append(parts, members[i].label, members[i].sourceFile, "", members[i].fileType)
		}
		text := strings.ToLower(strings.Join(parts, " "))
		best, bestScore := -1, 0
		for i, a := range layers {
			score := 0
			for _, keyword := range a.Keywords {
				score += countWord(text, keyword)
			}
			if score > bestScore {
				best, bestScore = i, score
			}
		}
		if best < 0 || bestScore < 2 {
			unassigned = append(unassigned, loose{cid, label})
			continue
		}
		a := layers[best]
		if groups[a.ID] == nil {
			groups[a.ID] = &group{cfSection{id: a.ID, name: a.Name}, best}
		}
		groups[a.ID].communities = append(groups[a.ID].communities, cid)
	}

	ranked := make([]*group, 0, len(groups))
	for _, g := range groups {
		ranked = append(ranked, g)
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].priority < ranked[j].priority })
	limit := max(1, max(1, maxSections)-1)
	var overflow []string
	if len(ranked) > limit {
		for _, g := range ranked[limit:] {
			overflow = append(overflow, g.communities...)
		}
		ranked = ranked[:limit]
	}

	sections := []cfSection{{id: "overview", name: "Architecture Overview"}}
	for _, g := range ranked {
		sections = append(sections, g.cfSection)
	}
	slots := max(0, maxSections-(len(sections)-1)-1)
	for i, u := range unassigned {
		if i >= slots {
			overflow = append(overflow, u.cid)
			continue
		}
		sections = append(sections, cfSection{id: u.label, name: u.label, communities: []string{u.cid}})
	}
	if len(overflow) > 0 {
		sections = append(sections, cfSection{id: "other", name: "Other", communities: overflow})
	}
	return sections
}

// normalizeSections gives every section a unique, URL-safe id and puts the
// overview first.
func normalizeSections(sections []cfSection) []cfSection {
	out := []cfSection{{id: "overview", name: "Architecture Overview"}}
	used := map[string]bool{"overview": true, "hyperedges": true, "stats": true}
	for i, s := range sections {
		id, name := s.id, s.name
		if id == "" {
			id = name
		}
		if id == "" {
			id = "section-" + itoa(i+1)
		}
		if name == "" {
			name = id
		}
		if strings.ToLower(id) == "overview" {
			out[0].name = name
			continue
		}
		out = append(out, cfSection{id: anchorID(id, "section-"+itoa(i+1), used), name: name, communities: s.communities})
	}
	return out
}

// classified is the edges sorted by where they run: inside one section, or
// from one section to another.
type classified struct {
	intra       map[string][]*cfEdge
	inter       []*cfEdge
	nodeSection map[string]string
}

func classifyEdges(edges []*cfEdge, sections []cfSection, sectionNodes map[string][]*cfNode) classified {
	c := classified{intra: map[string][]*cfEdge{}, nodeSection: map[string]string{}}
	for _, s := range sections {
		for _, n := range sectionNodes[s.id] {
			c.nodeSection[n.id] = s.id
		}
	}
	for _, e := range edges {
		from, okFrom := c.nodeSection[e.source]
		to, okTo := c.nodeSection[e.target]
		switch {
		case !okFrom || !okTo:
		case from == to:
			c.intra[from] = append(c.intra[from], e)
		default:
			c.inter = append(c.inter, e)
		}
	}
	return c
}

// sectionLink is the edges from one section to another, added up.
type sectionLink struct {
	from, to  string
	count     int
	relations tally
}

func sectionLinks(c classified) []*sectionLink {
	byPair := map[[2]string]*sectionLink{}
	var links []*sectionLink
	for _, e := range c.inter {
		if !e.drawable() {
			continue
		}
		key := [2]string{c.nodeSection[e.source], c.nodeSection[e.target]}
		link := byPair[key]
		if link == nil {
			link = &sectionLink{from: key[0], to: key[1]}
			byPair[key] = link
			links = append(links, link)
		}
		link.count++
		link.relations.add(e.relation, 1)
	}
	return links
}
