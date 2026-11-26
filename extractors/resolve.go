package extractors

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
)

var labelNoise = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// labelKey reduces a label to the name it stands for: `Foo()` and `Foo` share
// a key. Underscores stay — a private `_Response` is not `Response`.
func labelKey(n *base.Node, fold bool) string {
	key := labelNoise.ReplaceAllString(strings.TrimSpace(n.Label), "")
	if fold {
		return strings.ToLower(key)
	}
	return key
}

// isTopLevelFunction reports a free function definition (`name()`), not a
// method (`.name()`) or a qualified member.
func isTopLevelFunction(n *base.Node) bool {
	label := strings.TrimSpace(n.Label)
	return n.FileType == "code" && strings.HasSuffix(label, ")") && !strings.Contains(label, ".")
}

func (p *pipeline) caseInsensitive(sourceFile string) bool {
	lang := languageFor(sourceFile)
	return sourceFile != "" && lang != nil && lang.CaseInsensitive
}

var supertypeRelations = map[string]bool{"inherits": true, "implements": true, "extends": true}

// rewireUniqueStubs replaces a sourceless stub — a name some file referenced
// without declaring it — by the one real definition carrying that name, when
// there is exactly one.
//
// The match is by exact case: a `Path` reference must not land on a `PATH`
// constant. Case is folded only against definitions of case-insensitive
// languages. A stub nothing declares as a type may fall back to a unique
// top-level function of that name, unless the stub is used as a supertype
// (nothing inherits from a function) or the function is of another language
// family than every file referring to the stub. And whatever the candidate, an
// edge is never re-pointed across language families.
func (p *pipeline) rewireUniqueStubs() {
	c := p.corpus
	realByLabel := map[string][]*base.Node{}
	realByLabelFolded := map[string][]*base.Node{}
	funcByLabel := map[string][]*base.Node{}
	var stubs []*base.Node
	for _, n := range c.Nodes {
		key := labelKey(n, false)
		if key == "" {
			continue
		}
		switch {
		case n.SourceFile == "":
			stubs = append(stubs, n)
		case base.IsTypeLikeDefinition(n):
			realByLabel[key] = append(realByLabel[key], n)
			if p.caseInsensitive(n.SourceFile) {
				folded := labelKey(n, true)
				realByLabelFolded[folded] = append(realByLabelFolded[folded], n)
			}
		case isTopLevelFunction(n):
			funcByLabel[key] = append(funcByLabel[key], n)
		}
	}

	stubIDs := map[string]bool{}
	for _, stub := range stubs {
		stubIDs[stub.ID] = true
	}
	stubFamilies := map[string]map[string]bool{}
	supertypeStubs := map[string]bool{}
	for _, e := range c.Edges {
		family := c.Family(e.SourceFile)
		for _, id := range [2]string{e.Source, e.Target} {
			if stubIDs[id] && family != "" {
				if stubFamilies[id] == nil {
					stubFamilies[id] = map[string]bool{}
				}
				stubFamilies[id][family] = true
			}
		}
		if stubIDs[e.Target] && supertypeRelations[e.Relation] {
			supertypeStubs[e.Target] = true
		}
	}

	remap := map[string]string{}
	for _, stub := range stubs {
		if stub.ID == "" {
			continue
		}
		candidates := realByLabel[labelKey(stub, false)]
		if len(candidates) != 1 {
			candidates = realByLabelFolded[labelKey(stub, true)]
		}
		if len(candidates) != 1 {
			functions := funcByLabel[labelKey(stub, false)]
			if len(functions) == 1 && !supertypeStubs[stub.ID] {
				families := stubFamilies[stub.ID]
				family := c.Family(functions[0].SourceFile)
				if len(families) == 0 || family == "" || families[family] {
					candidates = functions
				}
			}
		}
		if len(candidates) == 1 && candidates[0].ID != "" && candidates[0].ID != stub.ID {
			remap[stub.ID] = candidates[0].ID
		}
	}
	if len(remap) == 0 {
		return
	}

	fileOf := map[string]string{}
	for _, n := range c.Nodes {
		fileOf[n.ID] = n.SourceFile
	}
	crossFamily := func(e *base.Edge, candidate string) bool {
		edgeFamily, candidateFamily := c.Family(e.SourceFile), c.Family(fileOf[candidate])
		return edgeFamily != "" && candidateFamily != "" && edgeFamily != candidateFamily
	}
	for _, e := range c.Edges {
		if target, ok := remap[e.Source]; ok && !crossFamily(e, target) {
			e.Source = target
		}
		if target, ok := remap[e.Target]; ok && !crossFamily(e, target) {
			e.Target = target
		}
	}

	// A stub every edge has left is gone; one that some edge still names (it
	// was refused a cross-family rewire) stays.
	referenced := map[string]bool{}
	for _, e := range c.Edges {
		referenced[e.Source], referenced[e.Target] = true, true
	}
	kept := c.Nodes[:0]
	for _, n := range c.Nodes {
		if _, rewired := remap[n.ID]; !rewired || referenced[n.ID] {
			kept = append(kept, n)
		}
	}
	c.Nodes = kept
}

// resolveCalls binds the calls no file could resolve on its own to
// definitions in other files.
//
// A call is matched by the callee's name. One definition of that name in the
// whole corpus is the target. Several are not guessed between: an import in
// the caller's file that names exactly one of them decides; failing that, test
// doubles are set aside and the definition closest to the call site wins if
// there is exactly one such; otherwise the call gets no edge. Common short
// names (`log`, `get`, `run`) would otherwise wire unrelated code together.
//
// An edge backed by an import of the callee itself is EXTRACTED; one resolved
// by name alone is INFERRED.
//
// Member calls (`x.f()`) are not handled here at all: a method name says
// nothing without the receiver's type, which each language's own resolver
// works out afterwards.
func (p *pipeline) resolveCalls() {
	c := p.corpus

	labelToIDs := map[string][]string{}
	labelToIDsFolded := map[string][]string{}
	callable := map[string]bool{}
	class := map[string]bool{}
	fileNodeOf := c.FileNodeIDs()   // source file -> its file node
	nodeFile := map[string]string{} // node id -> source file
	nodeFileNode := map[string]string{}
	for _, n := range c.Nodes {
		if n.Callable {
			callable[n.ID] = true
		}
		if n.CallableClass {
			class[n.ID] = true
		}
		if n.SourceFile != "" {
			nodeFile[n.ID] = n.SourceFile
			if fileNode, ok := fileNodeOf[n.SourceFile]; ok {
				nodeFileNode[n.ID] = fileNode
			} else if rel, ok := relativeTo(n.SourceFile, c.Root); ok {
				nodeFileNode[n.ID] = fileNodeID(rel)
			} else {
				nodeFileNode[n.ID] = fileNodeID(n.SourceFile)
			}
		}
		// Rationale labels are prose and namespaces are not callable; neither
		// is something a call can name.
		if n.FileType == "rationale" || n.Type == "namespace" {
			continue
		}
		name := strings.TrimLeft(strings.Trim(n.Label, "()"), ".")
		if name == "" {
			continue
		}
		labelToIDs[name] = append(labelToIDs[name], n.ID)
		if p.caseInsensitive(n.SourceFile) {
			folded := strings.ToLower(name)
			labelToIDsFolded[folded] = append(labelToIDsFolded[folded], n.ID)
		}
	}

	// What each file imports: symbols directly (`imports`), and whole files
	// (`imports_from`).
	symbolImports := map[string]map[string]bool{}
	moduleImports := map[string]map[string]bool{}
	existing := map[[2]string]bool{}
	callLike := map[[2]string]bool{}
	add := func(index map[string]map[string]bool, from, to string) {
		if index[from] == nil {
			index[from] = map[string]bool{}
		}
		index[from][to] = true
	}
	for _, e := range c.Edges {
		pair := [2]string{e.Source, e.Target}
		existing[pair] = true
		switch e.Relation {
		case "calls", "indirect_call":
			callLike[pair] = true
		case "imports":
			add(symbolImports, e.Source, e.Target)
		case "imports_from":
			add(moduleImports, e.Source, e.Target)
		}
	}

	for _, rc := range c.RawCalls {
		callee := rc.Callee
		if callee == "" || rc.IsMemberCall {
			continue
		}
		if _, builtin := base.LanguageBuiltinGlobals[callee]; builtin {
			continue
		}
		lang := languageFor(rc.SourceFile)
		if lang != nil && lang.SkipCall != nil && lang.SkipCall(rc) {
			continue
		}

		candidates := labelToIDs[callee]
		if len(candidates) == 0 && lang != nil && lang.CaseInsensitive {
			candidates = labelToIDsFolded[strings.ToLower(callee)]
		}
		// A call never binds to a definition of another language family.
		// Candidates of unknown family (stubs, non-code nodes) are kept.
		if lang != nil {
			var sameFamily []string
			for _, candidate := range candidates {
				if family := c.Family(nodeFile[candidate]); family == "" || family == lang.FamilyName() {
					sameFamily = append(sameFamily, candidate)
				}
			}
			candidates = sameFamily
		}
		exact := false
		if lang != nil && lang.FilterCallCandidates != nil && len(candidates) > 0 {
			candidates, exact = lang.FilterCallCandidates(c, rc, candidates)
		}
		if len(candidates) == 0 {
			continue
		}

		caller := rc.CallerNID
		callerFile := fileNodeOf[rc.SourceFile]
		if callerFile == "" {
			callerFile = nodeFileNode[caller]
		}
		importedSymbols, importedModules := symbolImports[callerFile], moduleImports[callerFile]
		fileImported := func(candidate string) bool {
			fileNode, ok := nodeFileNode[candidate]
			return ok && importedModules[fileNode]
		}

		var target string
		evidence := false
		if len(candidates) == 1 {
			target = candidates[0]
			evidence = exact || importedSymbols[target] || fileImported(target)
		} else {
			var bySymbol, byModule []string
			for _, candidate := range candidates {
				if importedSymbols[candidate] {
					bySymbol = append(bySymbol, candidate)
				}
				if fileImported(candidate) {
					byModule = append(byModule, candidate)
				}
			}
			switch {
			case len(bySymbol) == 1:
				target, evidence = bySymbol[0], true
			case len(byModule) == 1:
				// Importing the file proves the caller can see it, not that it
				// meant this symbol of it: enough to pick, not to certify.
				target = byModule[0]
			default:
				files := make(map[string]string, len(candidates))
				for _, candidate := range candidates {
					files[candidate] = nodeFile[candidate]
				}
				target = disambiguateCandidates(candidates, files, rc.SourceFile)
				if target == "" {
					continue
				}
			}
		}
		// A sourceless candidate is an unresolved external name, not a
		// definition; a name-only call must not turn it into a hub.
		if nodeFile[target] == "" {
			continue
		}
		pair := [2]string{caller, target}

		if rc.Indirect {
			// A function passed by name (`pool.Submit(handler)`) is handed over
			// to be called later. It stays INFERRED even when imported — the
			// name is used as a value here — and only a real function counts:
			// never a type, never a same-named variable.
			if target != caller && !callLike[pair] && callable[target] && !class[target] {
				callLike[pair] = true
				context := rc.Context
				if context == "" {
					context = "argument"
				}
				c.Edges = append(c.Edges, &base.Edge{
					Source: caller, Target: target, Relation: "indirect_call", Context: context,
					Confidence: "INFERRED", ConfidenceScore: 0.85,
					SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1.0,
				})
			}
			continue
		}
		if !evidence && lang != nil && lang.RequireImportEvidence {
			continue
		}
		if target == caller || existing[pair] {
			continue
		}
		existing[pair] = true
		edge := &base.Edge{
			Source: caller, Target: target, Relation: "calls", Context: "call",
			Confidence: "INFERRED", ConfidenceScore: 0.85,
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1.0,
		}
		if evidence {
			edge.Confidence, edge.ConfidenceScore = "EXTRACTED", 1.0
		}
		c.Edges = append(c.Edges, edge)
	}
}

var testDirs = map[string]bool{"tests": true, "test": true, "spec": true, "specs": true, "__tests__": true}

var testFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^test_.*`),
	regexp.MustCompile(`(?i).*_test\..+$`),
	regexp.MustCompile(`(?i).*\.test\..+$`),
	regexp.MustCompile(`(?i).*\.spec\..+$`),
	regexp.MustCompile(`(?i).*_spec\..+$`),
	regexp.MustCompile(`(?i).*\.tests\.ps1$`),
	regexp.MustCompile(`.*Test\.java$`),
	regexp.MustCompile(`.*Tests\.java$`),
	regexp.MustCompile(`.*Tests\.cs$`),
}

// isTestPath classifies a source path as test code: a path segment that IS a
// test directory name, or a file name following a test naming convention.
// Whole segments and file names only, so `latest.go` and `src/contest/` are
// not tests.
func isTestPath(path string) bool {
	if path == "" {
		return false
	}
	segments := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	for _, segment := range segments {
		if testDirs[strings.ToLower(segment)] {
			return true
		}
	}
	name := segments[len(segments)-1]
	for _, pattern := range testFilePatterns {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}

func slashed(path string) string { return strings.ReplaceAll(path, `\`, "/") }

func parentDir(path string) string { return filepath.ToSlash(filepath.Dir(slashed(path))) }

// disambiguateCandidates picks one of several same-named definitions for a
// call, or "" when none stands out.
//
//  1. Test code. A call from non-test code never means a test double, so test
//     candidates are dropped. A call from a test prefers a definition in its
//     own file, then any test definition.
//  2. Proximity, over what is left: the same file, else the same directory,
//     else the longest shared directory prefix — each only when exactly one
//     candidate qualifies.
func disambiguateCandidates(candidates []string, files map[string]string, callSite string) string {
	var tests, others []string
	for _, candidate := range candidates {
		if isTestPath(files[candidate]) {
			tests = append(tests, candidate)
		} else {
			others = append(others, candidate)
		}
	}
	survivors := others
	if isTestPath(callSite) {
		var sameFile []string
		for _, candidate := range tests {
			if slashed(files[candidate]) == slashed(callSite) {
				sameFile = append(sameFile, candidate)
			}
		}
		if len(sameFile) == 1 {
			return sameFile[0]
		}
		if len(tests) > 0 {
			survivors = tests
		}
	}
	switch len(survivors) {
	case 0:
		return ""
	case 1:
		return survivors[0]
	}
	if callSite == "" {
		return ""
	}

	only := func(matches []string) (string, bool) {
		switch len(matches) {
		case 0:
			return "", false
		case 1:
			return matches[0], true
		}
		return "", true // several: genuinely ambiguous, stop here
	}
	var sameFile, sameDir []string
	callDir := parentDir(callSite)
	for _, candidate := range survivors {
		if slashed(files[candidate]) == slashed(callSite) {
			sameFile = append(sameFile, candidate)
		}
		if parentDir(files[candidate]) == callDir {
			sameDir = append(sameDir, candidate)
		}
	}
	if winner, decided := only(sameFile); decided {
		return winner
	}
	if winner, decided := only(sameDir); decided {
		return winner
	}

	callParts := strings.Split(callDir, "/")
	best, winner, tied := 0, "", false
	for _, candidate := range survivors {
		parts := strings.Split(parentDir(files[candidate]), "/")
		shared := 0
		for shared < len(parts) && shared < len(callParts) && parts[shared] == callParts[shared] {
			shared++
		}
		switch {
		case shared > best:
			best, winner, tied = shared, candidate, false
		case shared == best:
			tied = true
		}
	}
	if tied || best == 0 {
		return ""
	}
	return winner
}
