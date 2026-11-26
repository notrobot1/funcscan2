package golang

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/notrobot1/funcscan2/extractors/base"
)

var (
	goCommentRE = regexp.MustCompile(`(?s)//[^\n]*|/\*.*?\*/`)
	goPackageRE = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

// packageClause is the package name a Go file declares, "" if unreadable.
func packageClause(path string) string {
	source, err := base.ReadSourceBytes(path)
	if err != nil {
		return ""
	}
	m := goPackageRE.FindSubmatch(goCommentRE.ReplaceAll(source, []byte(" ")))
	if m == nil {
		return ""
	}
	return string(m[1])
}

// importPathOf is the import path of the package a source file belongs to. A
// relative file is read against the scan root. Results are cached per corpus.
func importPathOf(c *base.Corpus, sourceFile string) string {
	if sourceFile == "" {
		return ""
	}
	cache := c.Memo("go.importPath", func() any { return map[string]string{} }).(map[string]string)
	if cached, ok := cache[sourceFile]; ok {
		return cached
	}
	path := sourceFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.Root, path)
	}
	result := goImportPathForFile(path)
	cache[sourceFile] = result
	return result
}

// mergePackageTypes folds a package's type declared in one file with the
// copies minted by the other files that declare methods on it into ONE node.
//
// The extractor keys a type on its package directory, so `type Server` in a.go
// and `func (s *Server) Close()` in b.go mint the same id twice, differing only
// in source file. Left alone, the id-collision pass would split them by path,
// fragmenting one type into partial nodes that each own some of its methods.
//
// Guards:
//
//   - Only .go nodes in ONE directory — Go's package unit — fold together. The
//     id holds only the directory's NAME, so a/svc and b/svc collide on id
//     while being different packages; each directory folds on its own.
//   - A _test.go member folds only when its package clause names the same
//     package as the group's other files: an external test package
//     (`package svc_test`) shares the directory but declares its own types.
//
// The declaration site wins — the file holding `type X`, the only one with a
// `contains` edge to it. The survivor lists the files it absorbed in
// GoFoldedFiles so that, if the id still collides with another directory's
// type and gets salted, the absorbed files' edges follow it.
func mergePackageTypes(c *base.Corpus) {
	byID := map[string][]*base.Node{}
	var order []string
	for _, n := range c.Nodes {
		// A Go type name holds neither character, while every other label the
		// extractor mints does: `Run()`, `.Close()`, the file node's `a.go`.
		if n.ID == "" || n.FileType != "code" || n.Label == "" || strings.ContainsAny(n.Label, ".(") {
			continue
		}
		if strings.ToLower(filepath.Ext(n.SourceFile)) != ".go" {
			continue
		}
		if _, seen := byID[n.ID]; !seen {
			order = append(order, n.ID)
		}
		byID[n.ID] = append(byID[n.ID], n)
	}

	declaringFiles := map[string]map[string]bool{}
	for _, e := range c.Edges {
		if e.Relation == "contains" {
			if declaringFiles[e.Target] == nil {
				declaringFiles[e.Target] = map[string]bool{}
			}
			declaringFiles[e.Target][e.SourceFile] = true
		}
	}

	dropped := map[*base.Node]bool{}
	for _, nid := range order {
		group := byID[nid]
		if len(group) < 2 {
			continue
		}
		byDir := map[string][]*base.Node{}
		var dirs []string
		for _, n := range group {
			dir := filepath.Dir(n.SourceFile)
			if _, seen := byDir[dir]; !seen {
				dirs = append(dirs, dir)
			}
			byDir[dir] = append(byDir[dir], n)
		}
		for _, dir := range dirs {
			members := byDir[dir]
			if len(members) < 2 || !samePackage(members) {
				continue
			}
			// A type whose declaring file is outside the corpus leaves the
			// whole group method-only; one node is still the answer, so fold
			// on the lowest path.
			candidates := members
			if declared := declaringFiles[nid]; len(declared) > 0 {
				var declaring []*base.Node
				for _, n := range members {
					if declared[n.SourceFile] {
						declaring = append(declaring, n)
					}
				}
				if len(declaring) > 0 {
					candidates = declaring
				}
			}
			keeper := candidates[0]
			for _, n := range candidates[1:] {
				if n.SourceFile < keeper.SourceFile ||
					(n.SourceFile == keeper.SourceFile && n.SourceLocation < keeper.SourceLocation) {
					keeper = n
				}
			}
			for _, n := range members {
				if n != keeper {
					dropped[n] = true
					keeper.GoFoldedFiles = append(keeper.GoFoldedFiles, n.SourceFile)
				}
			}
		}
	}
	if len(dropped) > 0 {
		kept := c.Nodes[:0]
		for _, n := range c.Nodes {
			if !dropped[n] {
				kept = append(kept, n)
			}
		}
		c.Nodes = kept
	}

	// A file in ANOTHER package that names the type (`svc *alpha.Service`)
	// mints a sourceless `alpha.Service` stub, and the id normalisation folds
	// that label onto the very id the declaration carries (`alpha_service`).
	// The id-collision pass would then salt both apart and strand the method
	// edges sibling files emitted. Move such a stub to an id of its own first:
	// resolveTypeReferences still re-points its edges through the referencing
	// file's import; it just no longer collides with the declaration.
	type stubKey struct{ id, origin string }
	renames := map[stubKey]string{}
	for _, n := range c.Nodes {
		if _, typed := byID[n.ID]; typed && n.SourceFile == "" && strings.Contains(n.Label, ".") &&
			strings.ToLower(filepath.Ext(n.OriginFile)) == ".go" {
			newID := base.MakeID("go", "ref", n.Label)
			renames[stubKey{n.ID, n.OriginFile}] = newID
			n.ID = newID
		}
	}
	if len(renames) > 0 {
		for _, e := range c.Edges {
			if renamed, ok := renames[stubKey{e.Target, e.SourceFile}]; ok {
				e.Target = renamed
			}
		}
	}
}

// samePackage reports whether the files behind the nodes can be folded: always
// when no test file is among them, otherwise only when every package clause is
// readable and they all agree.
func samePackage(members []*base.Node) bool {
	hasTest := false
	for _, n := range members {
		if strings.HasSuffix(filepath.Base(n.SourceFile), "_test.go") {
			hasTest = true
		}
	}
	if !hasTest {
		return true
	}
	first := packageClause(members[0].SourceFile)
	if first == "" {
		return false
	}
	for _, n := range members[1:] {
		if packageClause(n.SourceFile) != first {
			return false
		}
	}
	return true
}

// resolveTypeReferences binds qualified type references (`pkg.T`) through the
// referencing file's imports and the exact module path of each declaration. A
// reference to a package outside the corpus is moved to a stub labelled with
// the full import path, which the bare-name stub matching cannot mistake for a
// local type of the same name.
func resolveTypeReferences(c *base.Corpus) {
	importsByFile := map[string]map[string]string{}
	actualPath := map[string]string{}
	for i, result := range c.PerFile {
		if result == nil {
			continue
		}
		for _, n := range result.Nodes {
			if n.SourceFile != "" {
				importsByFile[n.SourceFile] = result.GoImports
				actualPath[n.SourceFile] = c.Paths[i]
			}
		}
	}
	if len(importsByFile) == 0 {
		return
	}

	contained := map[string]bool{}
	for _, e := range c.Edges {
		if e.Relation == "contains" {
			contained[e.Target] = true
		}
	}
	fqnToIDs := map[string][]string{}
	for _, n := range c.Nodes {
		if n.SourceFile == "" || n.Label == "" || n.ID == "" || !contained[n.ID] || !base.IsTypeLikeDefinition(n) {
			continue
		}
		path := n.SourceFile
		if actual, ok := actualPath[path]; ok {
			path = actual
		}
		if packagePath := importPathOf(c, path); packagePath != "" {
			fqn := packagePath + "." + n.Label
			fqnToIDs[fqn] = append(fqnToIDs[fqn], n.ID)
		}
	}

	qualifiedStubs := map[string]string{}
	nodeIDs := map[string]bool{}
	for _, n := range c.Nodes {
		if n.ID == "" {
			continue
		}
		nodeIDs[n.ID] = true
		if n.SourceFile == "" && strings.Contains(n.Label, ".") {
			qualifiedStubs[n.ID] = n.Label
		}
	}
	if len(qualifiedStubs) == 0 {
		return
	}

	externalStubIDs := map[string]string{}
	var newNodes []*base.Node
	externalStub := func(fqn string) string {
		if nid, ok := externalStubIDs[fqn]; ok {
			return nid
		}
		nid := base.MakeID("go", "type", fqn)
		if !nodeIDs[nid] {
			newNodes = append(newNodes, &base.Node{ID: nid, Label: fqn, FileType: "code"})
			nodeIDs[nid] = true
		}
		externalStubIDs[fqn] = nid
		return nid
	}

	repointedFrom := map[string]bool{}
	for _, e := range c.Edges {
		if e.Relation != "references" && e.Relation != "embeds" {
			continue
		}
		qualified, ok := qualifiedStubs[e.Target]
		if !ok {
			continue
		}
		dot := strings.LastIndex(qualified, ".")
		alias, typeName := qualified[:dot], qualified[dot+1:]
		importPath := importsByFile[e.SourceFile][alias]
		if importPath == "" || typeName == "" {
			continue
		}
		fqn := importPath + "." + typeName
		repointedFrom[e.Target] = true
		if candidates := fqnToIDs[fqn]; len(candidates) == 1 {
			e.Target = candidates[0]
		} else {
			e.Target = externalStub(fqn)
		}
	}
	c.Nodes = append(c.Nodes, newNodes...)
	if len(repointedFrom) == 0 {
		return
	}
	referenced := map[string]bool{}
	for _, e := range c.Edges {
		referenced[e.Source] = true
		referenced[e.Target] = true
	}
	kept := c.Nodes[:0]
	for _, n := range c.Nodes {
		if !repointedFrom[n.ID] || referenced[n.ID] {
			kept = append(kept, n)
		}
	}
	c.Nodes = kept
}

// skipCall keeps Go's predeclared functions out of the shared call pass: a
// bare `append(s, x)` is the builtin, never a same-named function elsewhere.
func skipCall(rc *base.RawCall) bool {
	return goPredeclaredFuncs.has(rc.Callee)
}

type callIndex struct {
	methods    map[string]bool   // ids that are the target of a `method` edge
	sourceFile map[string]string // node id -> source file
}

// filterCallCandidates applies Go's scoping to a bare or package-qualified
// call: a method is never reached by a bare name, and `pkg.F()` reaches only
// an F declared at the exact path the caller imports pkg from. A call into a
// package outside the corpus therefore finds no candidate instead of binding
// to a same-named function by accident.
func filterCallCandidates(c *base.Corpus, rc *base.RawCall, candidates []string) ([]string, bool) {
	index := c.Memo("go.callIndex", func() any {
		idx := &callIndex{methods: map[string]bool{}, sourceFile: map[string]string{}}
		for _, e := range c.Edges {
			if e.Relation == "method" {
				idx.methods[e.Target] = true
			}
		}
		for _, n := range c.Nodes {
			if n.SourceFile != "" {
				idx.sourceFile[n.ID] = n.SourceFile
			}
		}
		return idx
	}).(*callIndex)

	importPath := string(rc.ImportPath)
	var kept []string
	for _, candidate := range candidates {
		if index.methods[candidate] {
			continue
		}
		if importPath != "" && importPathOf(c, index.sourceFile[candidate]) != importPath {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept, importPath != ""
}

// repointImports moves intra-module `imports_from` edges from the synthetic
// `go_pkg_<import path>` sink onto the imported package's file nodes.
//
// The extractor mints the sink from the raw import string with no module
// lookup, so the edge never reaches a file and reachability misses every
// consumer that arrives through an import. Standard-library and external
// imports have no file here and stay on their sinks.
func repointImports(c *base.Corpus) {
	fileNodes := c.FileNodeIDs()
	files := make([]string, 0, len(fileNodes))
	for sf := range fileNodes {
		if strings.HasSuffix(sf, ".go") {
			files = append(files, sf)
		}
	}
	sort.Strings(files)
	packageFiles := map[string][]string{} // sink id -> file node ids
	for _, sf := range files {
		if importPath := importPathOf(c, sf); importPath != "" {
			sink := base.MakeID("go", "pkg", importPath)
			packageFiles[sink] = append(packageFiles[sink], fileNodes[sf])
		}
	}
	if len(packageFiles) == 0 {
		return
	}
	existing := map[[2]string]bool{}
	for _, e := range c.Edges {
		existing[[2]string{e.Source, e.Target}] = true
	}
	rewritten := make([]*base.Edge, 0, len(c.Edges))
	for _, e := range c.Edges {
		targets, ok := packageFiles[e.Target]
		if e.Relation != "imports_from" || !ok {
			rewritten = append(rewritten, e)
			continue
		}
		for _, fileNID := range targets {
			pair := [2]string{e.Source, fileNID}
			if fileNID == e.Source || existing[pair] {
				continue
			}
			existing[pair] = true
			repointed := *e
			repointed.Target = fileNID
			rewritten = append(rewritten, &repointed)
		}
	}
	c.Edges = rewritten
}

// memberKey strips the label decoration from a method or type name:
// `.Close()` -> `Close`.
func memberKey(label string) string {
	label = strings.TrimSpace(label)
	return strings.TrimSuffix(strings.TrimPrefix(label, "."), "()")
}

// resolveMemberCalls binds Go member calls (`g.Greet()`) through the
// receiver's declared type.
//
// Local bindings carry their receiver type; only unbound names may fall back
// to package variables. Fields resolve through their owning struct. The edge
// is always INFERRED: the target comes from the receiver's declared type, not
// from the call site.
//
// Names are matched case-sensitively. Go exports by capitalisation, so `Run`
// and `run` on one type are two methods with two visibilities.
//
// A package-qualified type (`plugins.Client`, `*svc.Greeter`) resolves through
// the caller's import of that package: only a declaration whose file sits at
// the imported path qualifies. A bare type name is a same-package reference,
// so two packages declaring the same name do not make it ambiguous.
//
// A receiver typed to a type this corpus declares nowhere is parked on the
// caller for a merge of several graphs to finish.
func resolveMemberCalls(c *base.Corpus) {
	var raw []*base.RawCall
	for _, result := range c.PerFile {
		if result == nil {
			continue
		}
		for _, rc := range result.RawCalls {
			if rc.Language == "go" && rc.IsMemberCall && rc.MemberReceiver != "" &&
				rc.Callee != "" && rc.CallerNID != "" {
				raw = append(raw, rc)
			}
		}
	}
	if len(raw) == 0 {
		return
	}
	packageVars := map[string]base.TypeTable{}
	importsByFile := map[string]map[string]string{}
	packagePathByFile := map[string]string{}
	for _, result := range c.PerFile {
		if result == nil || result.GoTypeTable == nil || result.GoTypeTable.Path == "" {
			continue
		}
		table := result.GoTypeTable
		packageVars[table.Path] = table.PackageVars
		importsByFile[table.Path] = result.GoImports
		if table.PackagePath != "" {
			packagePathByFile[table.Path] = string(table.PackagePath)
		}
	}

	// A Go type id folds in the package directory, so two packages declaring
	// the same name stay separate candidates until package evidence narrows
	// the lookup. A node keeps every import path it is declared at.
	typeDefs := map[string][]string{}
	nodeByID := map[string]*base.Node{}
	fieldsByType := map[string][]map[string]string{}
	packagePaths := map[string]map[string]bool{}
	for _, n := range c.Nodes {
		nodeByID[n.ID] = n
		if n.CallableClass && c.Family(n.SourceFile) == "go" {
			key := memberKey(n.Label)
			typeDefs[key] = append(typeDefs[key], n.ID)
			if n.GoPackagePath != "" {
				if packagePaths[n.ID] == nil {
					packagePaths[n.ID] = map[string]bool{}
				}
				packagePaths[n.ID][n.GoPackagePath] = true
			}
		}
		for structNID, fields := range n.GoFields {
			fieldsByType[structNID] = append(fieldsByType[structNID], fields)
		}
	}
	samePackageTypes := func(label, callerPackage string) []string {
		candidates := typeDefs[memberKey(label)]
		if callerPackage != "" {
			var scoped []string
			for _, nid := range candidates {
				if packagePaths[nid][callerPackage] {
					scoped = append(scoped, nid)
				}
			}
			if len(scoped) > 0 {
				return scoped
			}
		}
		return candidates
	}

	type methodKey struct{ owner, name string }
	methodIndex := map[methodKey][]string{}
	existing := map[[2]string]bool{}
	for _, e := range c.Edges {
		existing[[2]string{e.Source, e.Target}] = true
		if e.Relation != "method" {
			continue
		}
		if target := nodeByID[e.Target]; target != nil {
			key := methodKey{e.Source, memberKey(target.Label)}
			methodIndex[key] = append(methodIndex[key], e.Target)
		}
	}

	// One edge survives per pair, so direct calls take priority over method
	// values.
	sort.SliceStable(raw, func(i, j int) bool { return !raw[i].Indirect && raw[j].Indirect })
	for _, rc := range raw {
		receiver, callee, caller := string(rc.MemberReceiver), rc.Callee, rc.CallerNID
		callerPackage := packagePathByFile[rc.SourceFile]
		// The enclosing method's own receiver, parameters and locals are typed
		// by their declaration. A field receiver is typed from its owning
		// struct's declaration instead, which may sit in a sibling file.
		typeName := string(rc.ReceiverType)
		fromSibling := false
		if rc.FieldOwner != "" {
			typeName = ""
			owners := samePackageTypes(string(rc.FieldOwner), callerPackage)
			if len(owners) == 1 && len(fieldsByType[owners[0]]) == 1 {
				typeName = fieldsByType[owners[0]][0][receiver]
				fromSibling = true
			}
		} else if typeName == "" && !rc.ReceiverBound {
			typeName = packageVars[rc.SourceFile][receiver]
		}
		if typeName == "" {
			continue
		}
		if _, builtin := base.LanguageBuiltinGlobals[typeName]; builtin {
			continue
		}

		var defs []string
		if strings.Contains(typeName, ".") {
			// `pkg.T`: bind only to the T declared at the path this file
			// imports pkg from. No match means the standard library or a
			// module outside this corpus, which stays unresolved rather than
			// parked. A sibling file's field already carries the import path
			// in place of the alias.
			var importPath, bare string
			if fromSibling {
				dot := strings.LastIndex(typeName, ".")
				importPath, bare = typeName[:dot], typeName[dot+1:]
			} else {
				alias, rest, _ := strings.Cut(typeName, ".")
				importPath, bare = importsByFile[rc.SourceFile][alias], rest
			}
			if importPath != "" {
				for _, nid := range typeDefs[memberKey(bare)] {
					if packagePaths[nid][importPath] {
						defs = append(defs, nid)
					}
				}
			}
			if len(defs) == 0 {
				continue
			}
		} else {
			defs = samePackageTypes(typeName, callerPackage)
		}
		if len(defs) == 0 {
			// Declared nowhere here — usually in a repository this build does
			// not contain. A method value is not parked: `f(x.y)` may just as
			// well pass a field, which is no call at all.
			if !rc.Indirect {
				base.ParkUnresolvedMemberCall(nodeByID[caller], callee, typeName, "go", rc)
			}
			continue
		}
		if len(defs) != 1 { // ambiguous: no guess
			continue
		}
		targets := methodIndex[methodKey{defs[0], memberKey(callee)}]
		if len(targets) != 1 {
			continue
		}
		target := targets[0]
		pair := [2]string{caller, target}
		if target == caller || existing[pair] {
			continue
		}
		existing[pair] = true
		edge := &base.Edge{
			Source:          caller,
			Target:          target,
			Relation:        "calls",
			Context:         "call",
			Confidence:      "INFERRED",
			ConfidenceScore: 0.85,
			SourceFile:      rc.SourceFile,
			SourceLocation:  rc.SourceLocation,
			Weight:          1.0,
		}
		if rc.Indirect {
			// A method value (`routing.Wrap(s.Handle)`) hands the method over
			// to be called later.
			edge.Relation = "indirect_call"
			edge.Context = rc.Context
			if edge.Context == "" {
				edge.Context = "argument"
			}
		}
		c.Edges = append(c.Edges, edge)
	}
}

// Past this many structural implementers an interface is skipped, not
// truncated: which ten to keep would be arbitrary.
const maxImplicitImplementers = 10

func isInterface(n *base.Node) bool {
	return n != nil && n.Metadata["language"] == "go" && n.Metadata["kind"] == "interface"
}

func exported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// resolveInterfaceDispatch links each Go interface method to the methods of
// every type that satisfies the interface.
//
// `d.provider.Get()` where provider is an interface resolves to the
// interface's own `.Get()` requirement, because that is what the call site
// names; the concrete `Provider.Get()` is a separate node. Go has no
// `implements` declaration to read, so the pairs are inferred from method
// sets: a named type T implements interface I when T's methods (value and
// pointer receivers alike) include every method name I requires. Names only:
// method nodes carry no signature, and Go has no overloads.
//
// Guards:
//
//   - Both ends are declared in a .go file.
//   - An interface with no methods, or one that is a type-set constraint, is
//     skipped: every type satisfies the first, and no call goes through the
//     second.
//   - An interface that embeds another is used only when every embedded
//     interface is declared in the corpus, with its methods counted in.
//   - An unexported method can only be implemented in the interface's own
//     package, so only types in the same directory count.
//   - An interface matched by more than maxImplicitImplementers types is
//     skipped: name-only matching makes a lone common name (`Close`,
//     `String`) match unrelated types.
//
// Struct embedding is not followed: a type whose method comes from an embedded
// field does not own that method node.
func resolveInterfaceDispatch(c *base.Corpus) {
	nodeByID := map[string]*base.Node{}
	interfaces := map[string]*base.Node{}
	for _, n := range c.Nodes {
		nodeByID[n.ID] = n
	}
	for _, n := range c.Nodes {
		if isInterface(n) && base.DeclaredIn(n, ".go") {
			interfaces[n.ID] = n
		}
	}
	if len(interfaces) == 0 {
		return
	}
	methodNames := map[string]map[string]bool{}
	embeds := map[string][]string{}
	typeSetConstrained := map[string]bool{}
	for _, e := range c.Edges {
		_, fromInterface := interfaces[e.Source]
		switch {
		case e.Relation == "method":
			m := nodeByID[e.Target]
			if base.DeclaredIn(m, ".go") && base.MethodLabel(m) != "" {
				if methodNames[e.Source] == nil {
					methodNames[e.Source] = map[string]bool{}
				}
				methodNames[e.Source][base.MethodLabel(m)] = true
			}
		case fromInterface && e.Relation == "embeds":
			embeds[e.Source] = append(embeds[e.Source], e.Target)
		case fromInterface && e.Relation == "references" && e.Context == "type_constraint":
			typeSetConstrained[e.Source] = true
		}
	}

	// required returns the interface's full method set; ok is false when it is
	// not fully known (an embedded interface outside the corpus, or a cycle).
	var required func(iface string, visiting map[string]bool) (map[string]bool, bool)
	required = func(iface string, visiting map[string]bool) (map[string]bool, bool) {
		names := map[string]bool{}
		for name := range methodNames[iface] {
			names[name] = true
		}
		for _, embedded := range embeds[iface] {
			if _, known := interfaces[embedded]; !known || visiting[embedded] {
				return nil, false
			}
			visiting[iface] = true
			inner, ok := required(embedded, visiting)
			delete(visiting, iface)
			if !ok {
				return nil, false
			}
			for name := range inner {
				names[name] = true
			}
		}
		return names, true
	}

	var owners []string
	for owner := range methodNames {
		if _, isIface := interfaces[owner]; !isIface && base.DeclaredIn(nodeByID[owner], ".go") {
			owners = append(owners, owner)
		}
	}
	sort.Strings(owners)

	ifaceIDs := make([]string, 0, len(interfaces))
	for id := range interfaces {
		ifaceIDs = append(ifaceIDs, id)
	}
	sort.Strings(ifaceIDs)

	var pairs []base.Implements
	for _, iface := range ifaceIDs {
		if typeSetConstrained[iface] || len(methodNames[iface]) == 0 {
			continue
		}
		needed, ok := required(iface, map[string]bool{})
		if !ok || len(needed) == 0 {
			continue
		}
		pkg := filepath.Dir(interfaces[iface].SourceFile)
		samePackageOnly := false
		for name := range needed {
			if !exported(name) {
				samePackageOnly = true
			}
		}
		var impls []string
		for _, owner := range owners {
			if samePackageOnly && filepath.Dir(nodeByID[owner].SourceFile) != pkg {
				continue
			}
			satisfied := true
			for name := range needed {
				if !methodNames[owner][name] {
					satisfied = false
					break
				}
			}
			if satisfied {
				impls = append(impls, owner)
			}
		}
		if len(impls) == 0 || len(impls) > maxImplicitImplementers {
			continue
		}
		for _, impl := range impls {
			pairs = append(pairs, base.Implements{Impl: impl, Interface: iface})
		}
	}
	if len(pairs) > 0 {
		base.ResolveInterfaceDispatch(c, pairs, ".go")
	}
}
