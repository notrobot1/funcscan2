// Package extractors turns source files into graph fragments: nodes for the
// declarations a file holds, edges for the relations it can prove on its own,
// and raw calls left for a corpus-level resolver.
package extractors

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

var goPredeclaredTypes = newSet(
	"bool", "byte", "complex64", "complex128", "error", "float32", "float64",
	"int", "int8", "int16", "int32", "int64", "rune", "string",
	"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any", "comparable",
)

// Go predeclared functions, filtered only when the callee is a BARE identifier.
// The Go resolver looks a callee up by name, so an unexported method that happens
// to share a builtin's name (`func (h *history) append(...)`) absorbs every
// builtin call in the corpus: on an 8.9k-node Go codebase one such method
// collected 330 phantom inbound `calls` edges, inventing twelve database-layer ->
// service-layer edges — a layering violation absent from the source.
//
// Deliberately language-local rather than added to the shared
// LanguageBuiltinGlobals: `new`, `close` and friends are ordinary method names
// in the other languages that consult the shared set — listing them there kills
// every in-file Rust `Type::new()` edge.
//
// Bare-identifier-only for the same reason within Go: `h.append(v)` and
// `pkg.Delete(x)` are selector_expression callees and are genuine calls, so the
// filter must not reach them. Builtin *types* stay out (see
// goPredeclaredTypes): Go conversions are call-shaped too, but they produced
// no phantom edges on that corpus and filtering them would suppress genuine
// constructor-like calls.
//
// The set is the Go spec's predeclared function list in full. Being Go-local and
// bare-identifier-only makes completeness safe here: `len`, `max`, `min` and
// `print` carry the same shadowing hazard as `append`, and a principled boundary
// (the spec list) beats a hand-picked subset.
var goPredeclaredFuncs = newSet(
	"append", "cap", "clear", "close", "complex", "copy", "delete", "imag",
	"len", "make", "max", "min", "new", "panic", "print", "println", "real",
	"recover",
)

// The tree-sitter-go nodes a type argument parses as (`new(T)`, `make([]pkg.T, n)`);
// an ordinary argument is an expression and never one of these.
var goTypeNodes = newSet(
	"type_identifier", "qualified_type", "pointer_type", "generic_type", "slice_type",
	"array_type", "map_type", "channel_type", "parenthesized_type",
)

// Nodes that open a new binding scope for the call walk.
var goScopeNodes = newSet(
	"block", "if_statement", "for_statement", "expression_switch_statement",
	"type_switch_statement", "select_statement", "expression_case", "type_case",
	"communication_case", "default_case", "func_literal",
)

// goAliasReExports reports whether `type A = pkg.A` (or `pkg.A[...]`) re-exports
// another package's type under its own name. Such an alias denotes that very
// type and declares no new name, so it gets no node: references to `A` keep
// resolving to the original instead of tying between the two. Any other alias
// introduces a name.
func goAliasReExports(alias *sitter.Node, source []byte) bool {
	target := alias.ChildByFieldName("type")
	if target != nil && target.Kind() == "generic_type" {
		target = target.ChildByFieldName("type")
	}
	if target == nil || target.Kind() != "qualified_type" {
		return false
	}
	name, targetName := alias.ChildByFieldName("name"), target.ChildByFieldName("name")
	return name != nil && targetName != nil &&
		readText(name, source) == readText(targetName, source)
}

// goTypeParamNames returns the names bound by a declaration's generic type
// parameters.
//
// Covers a function/type's own `[T any, R comparable]` list and, for a method,
// the receiver's `(b Box[T])` type arguments (Go methods cannot declare their
// own type parameters; they reuse the receiver's). These names are local
// bindings, not corpus symbols, so a signature's use of them must not mint a
// sourceless stub.
func goTypeParamNames(decl *sitter.Node, source []byte) stringSet {
	names := stringSet{}
	for _, child := range children(decl) {
		if child.Kind() != "type_parameter_list" {
			continue
		}
		for _, d := range children(child) {
			if d.Kind() != "type_parameter_declaration" {
				continue
			}
			for _, c := range children(d) {
				if c.Kind() == "identifier" {
					if text := readText(c, source); text != "" {
						names[text] = struct{}{}
					}
				}
			}
		}
	}
	if receiver := decl.ChildByFieldName("receiver"); receiver != nil {
		stack := []*sitter.Node{receiver}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n.Kind() != "type_arguments" {
				stack = append(stack, children(n)...)
				continue
			}
			leaves := []*sitter.Node{n}
			for len(leaves) > 0 {
				leaf := leaves[len(leaves)-1]
				leaves = leaves[:len(leaves)-1]
				if leaf.Kind() == "type_identifier" {
					if text := readText(leaf, source); text != "" {
						names[text] = struct{}{}
					}
				}
				leaves = append(leaves, children(leaf)...)
			}
		}
	}
	return names
}

type typeRef struct{ name, role string }

func refRole(generic bool) string {
	if generic {
		return "generic_arg"
	}
	return "type"
}

// goCollectTypeRefs walks a Go type expression and appends (name, role) pairs.
//
// Names in skip (the enclosing declaration's generic type parameters) are
// local bindings, not references, and are dropped.
func goCollectTypeRefs(node *sitter.Node, source []byte, generic bool, out *[]typeRef, skip stringSet) {
	if node == nil {
		return
	}
	switch node.Kind() {
	case "type_identifier":
		text := readText(node, source)
		if text != "" && !goPredeclaredTypes.has(text) && !skip.has(text) {
			*out = append(*out, typeRef{text, refRole(generic)})
		}
	case "qualified_type":
		// Keep the package qualifier so the generic stub rewire cannot attach
		// `testing.T` to an unrelated local type or function named T.
		if text := readText(node, source); text != "" {
			*out = append(*out, typeRef{text, refRole(generic)})
		}
	case "generic_type":
		goCollectTypeRefs(node.ChildByFieldName("type"), source, generic, out, skip)
		for _, c := range children(node) {
			if c.Kind() != "type_arguments" {
				continue
			}
			for _, arg := range namedChildren(c) {
				goCollectTypeRefs(arg, source, true, out, skip)
			}
		}
	default:
		// pointer, slice, array, map, channel and parenthesized types, and any
		// other named node, are transparent: their named children carry the refs.
		if node.IsNamed() {
			for _, c := range namedChildren(node) {
				goCollectTypeRefs(c, source, generic, out, skip)
			}
		}
	}
}

// goStripGenericArgs drops a generic instantiation's type arguments from a
// receiver type's text.
//
// `(b *Box[T])` reads its type field as `*Box[T]` verbatim (after the leading
// `*` is stripped, `Box[T]`), but the type declaration's own id is keyed on the
// bare name alone: `type Box[T any] struct {...}` captures `[T any]` as a
// separate type_parameter_list, never part of the name. Without stripping this
// before building an id from it, a generic receiver's id never matched its own
// type's id, so a generic type's methods attached to a node disconnected from
// the type itself — this must run everywhere a receiver's raw type text turns
// into an id, not only one of them, or the two computations silently disagree.
func goStripGenericArgs(typeText string) string {
	base, _, _ := strings.Cut(typeText, "[")
	return strings.TrimSpace(base)
}

// goReceiverTypeText is a receiver's base type name: pointer and generic
// arguments removed.
func goReceiverTypeText(typeNode *sitter.Node, source []byte) string {
	text := strings.TrimSpace(strings.TrimLeft(readText(typeNode, source), "*"))
	return goStripGenericArgs(text)
}

// goSingleTypeName is the one declared type a Go type expression names, or "".
//
// `Greeter`, `*Greeter`, `pkg.Greeter` and `*pkg.Greeter` qualify, and generic
// arguments do not change the receiver's base type. A slice, map or channel
// element is not the receiver of `x.M()`. A qualified type keeps its package
// qualifier as written (`pkg.Greeter`): no bare local name can stand for it, so
// the resolver binds it only through the file's import of `pkg`.
func goSingleTypeName(node *sitter.Node, source []byte) string {
	for node != nil {
		kind := node.Kind()
		if kind != "pointer_type" && kind != "parenthesized_type" && kind != "generic_type" {
			break
		}
		next := node.ChildByFieldName("type")
		if next == nil {
			if named := namedChildren(node); len(named) > 0 {
				next = named[0]
			}
		}
		node = next
	}
	if node == nil {
		return ""
	}
	if node.Kind() == "qualified_type" {
		pkg, name := node.ChildByFieldName("package"), node.ChildByFieldName("name")
		if pkg == nil || name == nil {
			return ""
		}
		return readText(pkg, source) + "." + readText(name, source)
	}
	if node.Kind() != "type_identifier" {
		return ""
	}
	name := readText(node, source)
	if goPredeclaredTypes.has(name) {
		return ""
	}
	return name
}

// goInferredCompositeBindings pairs same-position declared names with
// composite-literal values.
//
// Shared by `x := T{}` (short_var_declaration) and `var x = T{}` (a type-less
// var_spec): both bind a name to the type of a same-position `T{}` / `&T{}`
// value. Returns name -> inferred type for each pair that is in fact a
// composite literal.
func goInferredCompositeBindings(names, values []*sitter.Node, source []byte) map[string]string {
	inferred := map[string]string{}
	for i := 0; i < len(names) && i < len(values); i++ {
		value := values[i]
		if value.Kind() == "unary_expression" {
			if operand := value.ChildByFieldName("operand"); operand != nil {
				value = operand
			}
		}
		if value.Kind() != "composite_literal" {
			continue
		}
		if typeName := goSingleTypeName(value.ChildByFieldName("type"), source); typeName != "" {
			inferred[readText(names[i], source)] = typeName
		}
	}
	return inferred
}

// goReceiverTypeTable collects declared or composite-literal receiver types,
// preserving unknown bindings.
//
// Unknown local bindings shadow package variables. Package-only tables exclude
// fields and function bodies so unrelated scopes cannot supply a receiver type.
func goReceiverTypeTable(root *sitter.Node, source []byte, packageOnly bool) TypeTable {
	table := TypeTable{}
	rootID := root.Id()
	stack := []*sitter.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		t := n.Kind()
		if t == "block" || (t == "func_literal" && n.Id() != rootID) {
			continue
		}
		if packageOnly && (t == "function_declaration" || t == "method_declaration" ||
			t == "func_literal" || t == "type_declaration") {
			continue
		}
		kids := children(n)
		switch {
		case t == "field_declaration" || t == "parameter_declaration" ||
			t == "variadic_parameter_declaration" || t == "var_spec":
			var names []*sitter.Node
			for _, c := range kids {
				if c.Kind() == "field_identifier" || c.Kind() == "identifier" {
					names = append(names, c)
				}
			}
			typeName := ""
			if t != "variadic_parameter_declaration" {
				typeName = goSingleTypeName(n.ChildByFieldName("type"), source)
			}
			var inferred map[string]string
			if t == "var_spec" && typeName == "" {
				if value := n.ChildByFieldName("value"); value != nil {
					inferred = goInferredCompositeBindings(names, namedChildren(value), source)
				}
			}
			for _, nameNode := range names {
				name := readText(nameNode, source)
				bound := typeName
				if bound == "" {
					bound = inferred[name]
				}
				table.setDefault(name, bound)
			}
		case t == "short_var_declaration":
			left, right := n.ChildByFieldName("left"), n.ChildByFieldName("right")
			if left != nil && right != nil {
				var names []*sitter.Node
				for _, c := range children(left) {
					if c.Kind() == "identifier" {
						names = append(names, c)
					}
				}
				inferred := goInferredCompositeBindings(names, namedChildren(right), source)
				for _, nameNode := range names {
					name := readText(nameNode, source)
					table.setDefault(name, inferred[name])
				}
			}
		case t == "range_clause" && hasChildKind(kids, ":="):
			if left := n.ChildByFieldName("left"); left != nil {
				for _, nameNode := range namedChildren(left) {
					if nameNode.Kind() == "identifier" {
						table.setDefault(readText(nameNode, source), "")
					}
				}
			}
		}
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return table
}

func hasChildKind(kids []*sitter.Node, kinds ...string) bool {
	for _, c := range kids {
		for _, k := range kinds {
			if c.Kind() == k {
				return true
			}
		}
	}
	return false
}

// goStructFieldTypes returns struct name -> {field name -> TypeName} for every
// struct this file declares.
//
// Only a named field whose type goSingleTypeName accepts is kept, the same rule
// the flat receiver table applies. Unlike that table it stays keyed by the
// struct, so a method in another file of the package can read the fields of its
// own receiver.
func goStructFieldTypes(root *sitter.Node, source []byte) map[string]map[string]string {
	fieldsByStruct := map[string]map[string]string{}
	stack := []*sitter.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Kind() == "function_declaration" || n.Kind() == "method_declaration" {
			continue
		}
		if n.Kind() != "type_spec" {
			stack = append(stack, children(n)...)
			continue
		}
		nameNode, body := n.ChildByFieldName("name"), n.ChildByFieldName("type")
		if nameNode == nil || body == nil || body.Kind() != "struct_type" {
			continue
		}
		fields := map[string]string{}
		for _, fdl := range children(body) {
			if fdl.Kind() != "field_declaration_list" {
				continue
			}
			for _, field := range children(fdl) {
				if field.Kind() != "field_declaration" {
					continue
				}
				typeName := goSingleTypeName(field.ChildByFieldName("type"), source)
				if typeName == "" {
					continue
				}
				for _, c := range children(field) {
					if c.Kind() == "field_identifier" {
						if name := readText(c, source); fields[name] == "" {
							fields[name] = typeName
						}
					}
				}
			}
		}
		if len(fields) > 0 {
			fieldsByStruct[readText(nameNode, source)] = fields
		}
	}
	return fieldsByStruct
}

// functionBody is a body whose calls are walked once every declaration in the
// file is known. The receiver name identifies `s.field` as an owned field;
// scope bindings keep parameters and locals distinct from package variables,
// even with unknown types.
type functionBody struct {
	callerNID  string
	body       *sitter.Node
	selfName   string
	selfType   string
	scopeTypes TypeTable
}

type goExtractor struct {
	source   []byte
	strPath  string
	stem     string
	pkgScope string
	fileNID  string

	nodes   []*Node
	edges   []Edge
	seenIDs stringSet

	functionBodies []functionBody
	// Fed to the `_callable` / `_callable_class` stamp: the indirect-call guard
	// and the cross-repo member-call pass both read them off the node.
	callableNIDs      stringSet
	callableClassNIDs stringSet
	// Go interfaces are satisfied implicitly, so nothing else in the output
	// tells an interface's method requirements from a struct's methods; the
	// dispatch pass reads this stamp to know which owners are interfaces.
	interfaceNIDs stringSet
	// local package name (including aliases) -> written Go import path
	importedPkgs map[string]string

	declRegistry *DeclIDRegistry
	// Node ids are casefolded, so `Run` and `run` declared in one file produce
	// the same id; caseGroups records every spelling behind each plain id. See
	// symbolNID for how a collision is settled.
	caseGroups map[string]stringSet

	// Call-walk state.
	localTypeNames stringSet
	labelToNID     map[string]string
	seenCallPairs  map[[2]string]struct{}
	rawCalls       []RawCall
}

// ExtractGo extracts functions, methods, type declarations, and imports from a
// .go file.
func ExtractGo(path string) (*Result, error) {
	path = normalizePath(path)
	source, err := readSourceBytes(path)
	if err != nil {
		return nil, err
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(tsgo.Language())); err != nil {
		return nil, fmt.Errorf("load tree-sitter-go: %w", err)
	}
	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse %s: tree-sitter returned no tree", path)
	}
	defer tree.Close()
	root := tree.RootNode()

	stem := fileStem(path)
	// Use directory name as package scope so methods on the same type across
	// multiple files in a package share one canonical type node.
	pkgScope := parentDirName(path)
	if pkgScope == "" {
		pkgScope = stem
	}
	x := &goExtractor{
		source:            source,
		strPath:           path,
		stem:              stem,
		pkgScope:          pkgScope,
		seenIDs:           stringSet{},
		callableNIDs:      stringSet{},
		callableClassNIDs: stringSet{},
		interfaceNIDs:     stringSet{},
		importedPkgs:      map[string]string{},
		caseGroups:        map[string]stringSet{},
		labelToNID:        map[string]string{},
		seenCallPairs:     map[[2]string]struct{}{},
	}
	x.fileNID = makeID(path)
	x.addNode(x.fileNID, filepath.Base(path), 1)
	x.declRegistry = NewDeclIDRegistry(x.fileNID)

	x.scanTypeNames(root)
	x.scanDeclarations(root)
	x.walk(root)

	// A type carries both markers: `_callable_class` is the narrowing, not a
	// separate kind. A type also carries its import path from go.mod: a `pkg.T`
	// receiver binds only to a `T` declared at the path the caller imports
	// `pkg` from, and on an incremental rebuild an unchanged declaring file
	// reaches the resolver only as its nodes.
	packagePath := ""
	if abs, err := filepath.Abs(path); err == nil {
		packagePath = goImportPathForFile(abs)
	}
	for _, n := range x.nodes {
		isClass := x.callableClassNIDs.has(n.ID)
		if isClass || x.callableNIDs.has(n.ID) {
			n.Callable = true
			if isClass {
				n.CallableClass = true
				n.GoPackagePath = packagePath
			}
		}
		if x.interfaceNIDs.has(n.ID) {
			n.Metadata = map[string]string{"language": "go", "kind": "interface"}
		}
	}

	// The field types of the structs this file declares ride on the file node,
	// keyed by struct id, so a method in a sibling file can type `s.field`. Not
	// on the struct node: every file holding a method of the struct emits that
	// id too, so an incremental rebuild of a method file would shadow the
	// declaring file's copy. A `pkg.T` field is stored as `<import path>.T`:
	// the method file reading it may not import `pkg` at all, so the qualifier
	// is settled by this file's imports.
	fieldsByStruct := goStructFieldTypes(root, source)
	structNames := make([]string, 0, len(fieldsByStruct))
	for name := range fieldsByStruct {
		structNames = append(structNames, name)
	}
	sort.Strings(structNames)
	structFields := map[string]map[string]string{}
	for _, name := range structNames {
		typed := map[string]string{}
		for field, typeName := range fieldsByStruct[name] {
			alias, bare, qualified := strings.Cut(typeName, ".")
			if !qualified {
				typed[field] = typeName
			} else if importPath, ok := x.importedPkgs[alias]; ok {
				typed[field] = importPath + "." + bare
			}
		}
		if len(typed) > 0 {
			structFields[x.typeSymbolNID(name)] = typed
		}
	}
	if len(structFields) > 0 {
		x.nodes[0].GoFields = structFields // nodes[0] is the file node
	}

	typeTable := goReceiverTypeTable(root, source, false)
	packageTypes := goReceiverTypeTable(root, source, true)

	x.localTypeNames = stringSet{}
	for _, n := range x.nodes {
		if x.callableClassNIDs.has(n.ID) {
			x.localTypeNames[n.Label] = struct{}{}
		}
	}
	methodNIDs := stringSet{}
	for _, e := range x.edges {
		if e.Relation == "method" {
			methodNIDs[e.Target] = struct{}{}
		}
	}
	for _, n := range x.nodes {
		if methodNIDs.has(n.ID) {
			continue
		}
		normalised := strings.TrimLeft(strings.Trim(n.Label, "()"), ".")
		x.labelToNID[normalised] = n.ID
	}

	for _, fb := range x.functionBodies {
		x.walkCalls(fb.body, fb.callerNID, fb.selfName, fb.selfType, fb.scopeTypes)
	}

	cleanEdges := make([]Edge, 0, len(x.edges))
	for _, e := range x.edges {
		isImport := e.Relation == "imports" || e.Relation == "imports_from"
		if x.seenIDs.has(e.Source) && (x.seenIDs.has(e.Target) || isImport) {
			cleanEdges = append(cleanEdges, e)
		}
	}
	rawCalls := x.rawCalls
	if rawCalls == nil {
		rawCalls = []RawCall{}
	}

	return &Result{
		Nodes:     x.nodes,
		Edges:     cleanEdges,
		RawCalls:  rawCalls,
		GoImports: x.importedPkgs,
		GoTypeTable: GoTypeTable{
			Path:        path,
			Table:       typeTable,
			PackageVars: packageTypes,
			PackagePath: OptString(packagePath),
		},
	}, nil
}

func (x *goExtractor) text(n *sitter.Node) string { return readText(n, x.source) }

func (x *goExtractor) addNode(nid, label string, line int) {
	if x.seenIDs.has(nid) {
		return
	}
	x.seenIDs[nid] = struct{}{}
	x.nodes = append(x.nodes, &Node{
		ID:             nid,
		Label:          label,
		FileType:       "code",
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line),
	})
}

func (x *goExtractor) addEdge(src, tgt, relation string, line int, context string) {
	x.edges = append(x.edges, Edge{
		Source:         src,
		Target:         tgt,
		Relation:       relation,
		Context:        context,
		Confidence:     "EXTRACTED",
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line),
		Weight:         1.0,
	})
}

func (x *goExtractor) ensureNamedNode(name string) string {
	nid := x.typeSymbolNID(name)
	if x.seenIDs.has(nid) {
		return nid
	}
	nid = makeID(name)
	if !x.seenIDs.has(nid) {
		// The name isn't declared in this file, so this is a cross-file
		// reference (e.g. a type defined in another file of the package). Emit
		// a SOURCELESS stub so the corpus-level rewire can collapse it onto the
		// real definition. A sourced stub here would bake the referencing
		// file's path into the id once colliding ids are disambiguated and
		// block the rewire: the phantom-duplicate-node bug.
		x.seenIDs[nid] = struct{}{}
		x.nodes = append(x.nodes, &Node{
			ID:         nid,
			Label:      name,
			FileType:   "code",
			OriginFile: x.strPath,
		})
	}
	return nid
}

// emitTypeRefs adds a `references` edge from owner to every type a type
// expression names.
func (x *goExtractor) emitTypeRefs(owner string, typeNode *sitter.Node, line int, context string, skip stringSet) {
	var refs []typeRef
	goCollectTypeRefs(typeNode, x.source, false, &refs, skip)
	for _, ref := range refs {
		ctx := context
		if ref.role == "generic_arg" {
			ctx = "generic_arg"
		}
		if tgt := x.ensureNamedNode(ref.name); tgt != owner {
			x.addEdge(owner, tgt, "references", line, ctx)
		}
	}
}

func (x *goExtractor) emitMethodRefs(funcNode *sitter.Node, funcNID string, line int, typeParams stringSet) {
	// The signature's own generic type parameters (and, for a method, those
	// bound by the receiver or an enclosing generic interface) are local
	// bindings, not references — dropping them keeps a `[T any]` signature from
	// minting a sourceless `T` god node every generic function shares.
	skip := goTypeParamNames(funcNode, x.source)
	for name := range typeParams {
		skip[name] = struct{}{}
	}
	if params := funcNode.ChildByFieldName("parameters"); params != nil {
		for _, p := range children(params) {
			// `opts ...Option` is a variadic_parameter_declaration, with the
			// same `type` field.
			if p.Kind() != "parameter_declaration" && p.Kind() != "variadic_parameter_declaration" {
				continue
			}
			x.emitTypeRefs(funcNID, p.ChildByFieldName("type"), line, "parameter_type", skip)
		}
	}
	result := funcNode.ChildByFieldName("result")
	if result == nil {
		return
	}
	if result.Kind() != "parameter_list" {
		x.emitTypeRefs(funcNID, result, line, "return_type", skip)
		return
	}
	for _, p := range children(result) {
		if p.Kind() != "parameter_declaration" {
			continue
		}
		typeNode := p.ChildByFieldName("type")
		if typeNode == nil {
			if named := namedChildren(p); len(named) > 0 {
				typeNode = named[0]
			}
		}
		x.emitTypeRefs(funcNID, typeNode, line, "return_type", skip)
	}
}

// emitPackageVarTypeRefs credits the file with the types a package-level `var`
// names, in its declared type (`var x T`), a composite literal (`pkg.T{}`,
// `&T{}`) or a type argument (`new(*pkg.T)`, `make(map[K]V)`): the file owns
// the initialiser. Without this a file holding only a dependency-injection set
// (`wire.Bind(new(Iface), new(*Impl))`) referenced nothing. A closure body is
// function code and is left to the call walk.
func (x *goExtractor) emitPackageVarTypeRefs(varDecl *sitter.Node) {
	stack := []*sitter.Node{varDecl}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Kind() == "func_literal" {
			continue
		}
		kids := children(n)
		var typeNodes []*sitter.Node
		switch n.Kind() {
		case "var_spec", "composite_literal":
			if typeNode := n.ChildByFieldName("type"); typeNode != nil {
				typeNodes = append(typeNodes, typeNode)
			}
		case "argument_list":
			for _, c := range kids {
				if goTypeNodes.has(c.Kind()) {
					typeNodes = append(typeNodes, c)
				}
			}
		}
		consumed := map[uintptr]struct{}{}
		for _, typeNode := range typeNodes {
			consumed[typeNode.Id()] = struct{}{}
			var refs []typeRef
			goCollectTypeRefs(typeNode, x.source, false, &refs, nil)
			for _, ref := range refs {
				ctx := "variable_type"
				if ref.role == "generic_arg" {
					ctx = "generic_arg"
				}
				x.addEdge(x.fileNID, x.ensureNamedNode(ref.name), "references", line(typeNode), ctx)
			}
		}
		for _, c := range kids {
			if _, done := consumed[c.Id()]; !done {
				stack = append(stack, c)
			}
		}
	}
}

// receiverOf reads a method's receiver: its base type name and its binding name.
func (x *goExtractor) receiverOf(method *sitter.Node) (typeName, name string) {
	receiver := method.ChildByFieldName("receiver")
	if receiver == nil {
		return "", ""
	}
	for _, param := range children(receiver) {
		if param.Kind() != "parameter_declaration" {
			continue
		}
		if typeNode := param.ChildByFieldName("type"); typeNode != nil {
			typeName = goReceiverTypeText(typeNode, x.source)
		}
		if nameNode := param.ChildByFieldName("name"); nameNode != nil {
			name = x.text(nameNode)
		}
		break
	}
	return typeName, name
}

func (x *goExtractor) plainSymbolNID(node *sitter.Node) (nid, name string, ok bool) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return "", "", false
	}
	name = x.text(nameNode)
	base := x.stem
	if node.Kind() == "method_declaration" {
		if receiverType, _ := x.receiverOf(node); receiverType != "" {
			base = x.typeSymbolNID(receiverType)
		}
	}
	return makeID(base, name), name, true
}

func (x *goExtractor) recordSpelling(plainNID, name string) {
	group := x.caseGroups[plainNID]
	if group == nil {
		group = stringSet{}
		x.caseGroups[plainNID] = group
	}
	group[name] = struct{}{}
}

// scanTypeNames runs first: a method is keyed under its owner's final id, which
// is salted when the owner's own name collides. An alias (`type A = B`) is a
// type_alias, not a type_spec, and declares a type name all the same.
func (x *goExtractor) scanTypeNames(node *sitter.Node) {
	kind := node.Kind()
	if kind == "type_spec" || (kind == "type_alias" && !goAliasReExports(node, x.source)) {
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := x.text(nameNode)
			x.recordSpelling(makeID(x.pkgScope, name), name)
		}
	}
	if kind == "function_declaration" || kind == "method_declaration" {
		return
	}
	for _, child := range children(node) {
		x.scanTypeNames(child)
	}
}

func (x *goExtractor) scanDeclarations(node *sitter.Node) {
	kind := node.Kind()
	if kind == "type_spec" {
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			ownerNID := x.typeSymbolNID(x.text(nameNode))
			for _, body := range children(node) {
				if body.Kind() != "interface_type" {
					continue
				}
				for _, elem := range children(body) {
					if elem.Kind() != "method_elem" {
						continue
					}
					if methodName := elem.ChildByFieldName("name"); methodName != nil {
						name := x.text(methodName)
						x.recordSpelling(makeID(ownerNID, name), name)
					}
				}
			}
		}
	}
	if kind == "function_declaration" || kind == "method_declaration" {
		if plainNID, name, ok := x.plainSymbolNID(node); ok {
			x.recordSpelling(plainNID, name)
		}
		return
	}
	for _, child := range children(node) {
		x.scanDeclarations(child)
	}
}

func isExported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// symbolNID settles the final id of a declaration.
//
// Node ids are casefolded, so `Run` and `run` declared in one file produce the
// same id and the second would be silently dropped — the unexported half
// vanished from the graph and its call sites bound by bare name to a same-named
// function in another package, which Go's visibility rules make impossible.
// Salt the non-canonical member of a case-only collision so both survive.
//
// The EXPORTED member keeps the plain id. Only exported symbols are reachable
// across packages, so cross-package edges (and edges cached from files an
// incremental rebuild does not touch) target the exported one — keeping its id
// stable means adding/removing an unexported sibling in an update never
// re-points them. Docs likewise reference the exported API, so the casefolded
// id a semantic node produces lands on the symbol it actually describes. Calls
// to the unexported sibling can only come from the same package and only
// resolve once the sibling exists, so salting it re-points nothing. When the
// collision has no unique exported member (`Run`/`RUN`), every member is salted
// rather than picking one arbitrarily, so the result never depends on
// declaration order.
//
// Type declarations follow the same rule: `type ResponseWriter interface` and
// `type responseWriter struct` share an id too, and the struct used to vanish
// with its methods hung off the interface. A salted type's methods, receivers
// and in-file references go through the salted id. The census is per file, so
// a pair split across two files of one package is not covered here.
func (x *goExtractor) symbolNID(plainNID, name, kind string) string {
	nid := plainNID
	if names := x.caseGroups[plainNID]; len(names) >= 2 {
		var exported []string
		for n := range names {
			if isExported(n) {
				exported = append(exported, n)
			}
		}
		if !(len(exported) == 1 && exported[0] == name) {
			nid = makeID(plainNID, nameSalt(name))
		}
	}
	return x.declRegistry.Resolve(nid, name, kind, x.seenIDs)
}

func (x *goExtractor) typeSymbolNID(name string) string {
	plainNID := makeID(x.pkgScope, name)
	if !x.caseGroups[plainNID].has(name) {
		return x.declRegistry.Resolve(plainNID, name, "type", x.seenIDs)
	}
	return x.symbolNID(plainNID, name, "type")
}

func (x *goExtractor) walk(node *sitter.Node) {
	switch node.Kind() {
	case "function_declaration":
		x.walkFunction(node)
	case "method_declaration":
		x.walkMethod(node)
	case "type_declaration":
		x.walkTypeDeclaration(node)
	case "import_declaration":
		x.walkImports(node)
	case "var_declaration":
		if parent := node.Parent(); parent == nil || parent.Kind() != "source_file" {
			x.walkChildren(node)
			return
		}
		// A package-level `var` initialiser runs at package init and has no
		// enclosing function, so its calls, including those inside a
		// `var f = func() {...}` closure, are credited to the file, the owner a
		// module-level call already gets elsewhere. Without this they were
		// never walked and the callee lost the caller.
		x.emitPackageVarTypeRefs(node)
		x.functionBodies = append(x.functionBodies, functionBody{
			callerNID:  x.fileNID,
			body:       node,
			scopeTypes: goReceiverTypeTable(node, x.source, false),
		})
	default:
		x.walkChildren(node)
	}
}

func (x *goExtractor) walkChildren(node *sitter.Node) {
	for _, child := range children(node) {
		x.walk(child)
	}
}

func (x *goExtractor) walkFunction(node *sitter.Node) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	funcName := x.text(nameNode)
	ln := line(node)
	funcNID := x.symbolNID(makeID(x.stem, funcName), funcName, "fn")
	x.addNode(funcNID, funcName+"()", ln)
	x.callableNIDs[funcNID] = struct{}{}
	x.addEdge(x.fileNID, funcNID, "contains", ln, "")
	x.emitMethodRefs(node, funcNID, ln, nil)
	if body := node.ChildByFieldName("body"); body != nil {
		x.functionBodies = append(x.functionBodies, functionBody{
			callerNID:  funcNID,
			body:       body,
			scopeTypes: goReceiverTypeTable(node, x.source, false),
		})
	}
}

func (x *goExtractor) walkMethod(node *sitter.Node) {
	receiverType, receiverName := x.receiverOf(node)
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	methodName := x.text(nameNode)
	ln := line(node)

	var methodNID string
	if receiverType != "" {
		parentNID := x.typeSymbolNID(receiverType)
		x.addNode(parentNID, receiverType, ln)
		x.callableClassNIDs[parentNID] = struct{}{}
		methodNID = x.symbolNID(makeID(parentNID, methodName), methodName, "fn")
		x.addNode(methodNID, "."+methodName+"()", ln)
		x.addEdge(parentNID, methodNID, "method", ln, "")
	} else {
		methodNID = x.symbolNID(makeID(x.stem, methodName), methodName, "fn")
		x.addNode(methodNID, methodName+"()", ln)
		x.addEdge(x.fileNID, methodNID, "contains", ln, "")
	}
	x.callableNIDs[methodNID] = struct{}{}

	x.emitMethodRefs(node, methodNID, ln, nil)
	if body := node.ChildByFieldName("body"); body != nil {
		x.functionBodies = append(x.functionBodies, functionBody{
			callerNID:  methodNID,
			body:       body,
			selfName:   receiverName,
			selfType:   receiverType,
			scopeTypes: goReceiverTypeTable(node, x.source, false),
		})
	}
}

func (x *goExtractor) walkTypeDeclaration(node *sitter.Node) {
	for _, child := range children(node) {
		kind := child.Kind()
		if kind != "type_spec" && kind != "type_alias" {
			continue
		}
		if kind == "type_alias" && goAliasReExports(child, x.source) {
			continue
		}
		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		typeName := x.text(nameNode)
		ln := line(child)
		typeNID := x.typeSymbolNID(typeName)
		x.addNode(typeNID, typeName, ln)
		x.callableClassNIDs[typeNID] = struct{}{}
		x.addEdge(x.fileNID, typeNID, "contains", ln, "")
		if kind == "type_alias" {
			// `type A = B` is a declaration like `type A B`: references to it
			// land here instead of minting a sourceless stub. Its right-hand
			// side is not walked.
			continue
		}
		// A generic type's own parameters (`type Box[T any]`) are local
		// bindings; a field or method using `T` must not mint a stub.
		typeParams := goTypeParamNames(child, x.source)
		// Type body: struct fields (with embeds) or interface embedding.
		for _, body := range children(child) {
			if body.Kind() == "struct_type" {
				x.walkStructBody(body, typeNID, typeParams)
				break
			}
			if body.Kind() == "interface_type" {
				x.walkInterfaceBody(body, typeNID, typeParams)
				break
			}
		}
	}
}

func (x *goExtractor) walkStructBody(body *sitter.Node, typeNID string, typeParams stringSet) {
	for _, fdl := range children(body) {
		if fdl.Kind() != "field_declaration_list" {
			continue
		}
		for _, field := range children(fdl) {
			if field.Kind() != "field_declaration" {
				continue
			}
			fieldKids := children(field)
			hasName := hasChildKind(fieldKids, "field_identifier")
			typeNode := field.ChildByFieldName("type")
			if typeNode == nil {
				for _, fc := range fieldKids {
					if fc.IsNamed() && fc.Kind() != "field_identifier" {
						typeNode = fc
						break
					}
				}
			}
			ln := line(field)
			var refs []typeRef
			goCollectTypeRefs(typeNode, x.source, false, &refs, typeParams)
			for _, ref := range refs {
				tgt := x.ensureNamedNode(ref.name)
				switch {
				case tgt == typeNID:
				case !hasName && ref.role == "type":
					x.addEdge(typeNID, tgt, "embeds", ln, "")
				case ref.role == "generic_arg":
					x.addEdge(typeNID, tgt, "references", ln, "generic_arg")
				default:
					x.addEdge(typeNID, tgt, "references", ln, "field")
				}
			}
		}
	}
}

func (x *goExtractor) walkInterfaceBody(body *sitter.Node, typeNID string, typeParams stringSet) {
	x.interfaceNIDs[typeNID] = struct{}{}
	for _, elem := range children(body) {
		if elem.Kind() == "method_elem" {
			// A method requirement declared in the interface body is part of
			// the interface's contract. Emit it as a method node so the
			// interface isn't left an empty shell and calls against the
			// interface can resolve (mirrors receiver methods).
			nameNode := elem.ChildByFieldName("name")
			if nameNode == nil {
				for _, mc := range children(elem) {
					if mc.Kind() == "field_identifier" {
						nameNode = mc
						break
					}
				}
			}
			if nameNode == nil {
				continue
			}
			name := x.text(nameNode)
			ln := line(elem)
			methodNID := x.symbolNID(makeID(typeNID, name), name, "fn")
			x.addNode(methodNID, "."+name+"()", ln)
			x.addEdge(typeNID, methodNID, "method", ln, "")
			x.emitMethodRefs(elem, methodNID, ln, typeParams)
			continue
		}
		if elem.Kind() != "type_elem" {
			continue
		}
		// A type_elem that is a generics type-set constraint — a union
		// (`A | B`) or an approximation (`~T`) — is NOT interface embedding. Go
		// only embeds a lone interface type; union/approximation terms can
		// never be embedded, so they must not emit `embeds` heritage edges.
		// Keep the type link as a `references` (type_constraint) edge.
		elemKids := children(elem)
		isTypeSet := hasChildKind(elemKids, "|", "negated_type")
		var refs []typeRef
		for _, sub := range elemKids {
			if sub.IsNamed() {
				goCollectTypeRefs(sub, x.source, false, &refs, typeParams)
			}
		}
		ln := line(elem)
		for _, ref := range refs {
			tgt := x.ensureNamedNode(ref.name)
			switch {
			case tgt == typeNID:
			case isTypeSet:
				x.addEdge(typeNID, tgt, "references", ln, "type_constraint")
			case ref.role == "type":
				x.addEdge(typeNID, tgt, "embeds", ln, "")
			default:
				x.addEdge(typeNID, tgt, "references", ln, "generic_arg")
			}
		}
	}
}

func (x *goExtractor) walkImports(node *sitter.Node) {
	for _, child := range children(node) {
		switch child.Kind() {
		case "import_spec_list":
			for _, spec := range children(child) {
				if spec.Kind() == "import_spec" {
					x.addImport(spec)
				}
			}
		case "import_spec":
			x.addImport(child)
		}
	}
}

func (x *goExtractor) addImport(spec *sitter.Node) {
	pathNode := spec.ChildByFieldName("path")
	if pathNode == nil {
		return
	}
	raw := strings.Trim(x.text(pathNode), `"`)
	// Prefix with go_pkg_ so stdlib names (e.g. "context") don't collide with
	// local files of the same basename.
	x.addEdge(x.fileNID, makeID("go", "pkg", raw), "imports_from", line(spec), "import")
	// Track local name (alias or last path segment).
	localName := raw[strings.LastIndex(raw, "/")+1:]
	if alias := spec.ChildByFieldName("name"); alias != nil {
		localName = x.text(alias)
	}
	if localName != "" && localName != "_" && localName != "." {
		x.importedPkgs[localName] = raw
	}
}

// ownFieldSelector reports whether operand is `s.field` with `s` this method's
// own receiver, and returns the field name. Any other head could be a package
// or a variable whose type the flat table does not know.
func (x *goExtractor) ownFieldSelector(operand *sitter.Node, selfName, selfType string, scope TypeTable) (string, bool) {
	inner := operand.ChildByFieldName("operand")
	if selfName == "" || inner == nil || inner.Kind() != "identifier" ||
		x.text(inner) != selfName || scope[selfName] != selfType {
		return "", false
	}
	return x.text(operand.ChildByFieldName("field")), true
}

func (x *goExtractor) walkCalls(node *sitter.Node, callerNID, selfName, selfType string, scope TypeTable) {
	kind := node.Kind()
	if kind == "function_declaration" || kind == "method_declaration" {
		return
	}
	if goScopeNodes.has(kind) {
		scope = scope.clone()
		if kind == "func_literal" {
			scope.update(goReceiverTypeTable(node, x.source, false))
		}
	}
	if kind == "call_expression" {
		x.recordCall(node, callerNID, selfName, selfType, scope)
		x.recordMethodValueArgs(node, callerNID, selfName, selfType, scope)
	}
	for _, child := range children(node) {
		x.walkCalls(child, callerNID, selfName, selfType, scope)
		switch child.Kind() {
		case "short_var_declaration", "var_spec", "range_clause":
			scope.update(goReceiverTypeTable(child, x.source, false))
		}
	}
}

func (x *goExtractor) recordCall(node *sitter.Node, callerNID, selfName, selfType string, scope TypeTable) {
	funcNode := node.ChildByFieldName("function")
	if funcNode == nil {
		return
	}
	var (
		calleeName         string
		isMemberCall       bool
		isBareIdentifier   bool
		packageReceiver    string
		memberReceiver     string
		memberReceiverType string
		fieldOwner         string
		importPath         string
	)
	switch funcNode.Kind() {
	case "identifier":
		isBareIdentifier = true
		calleeName = x.text(funcNode)
	case "selector_expression":
		field := funcNode.ChildByFieldName("field")
		operand := funcNode.ChildByFieldName("operand")
		receiverName := x.text(operand)
		// A visible local binding takes precedence over a same-named import,
		// even when its receiver type is unknown.
		_, isImport := x.importedPkgs[receiverName]
		isMemberCall = scope.has(receiverName) || !isImport
		switch {
		case !isMemberCall:
			packageReceiver = receiverName
			importPath = x.importedPkgs[receiverName]
		case operand == nil:
		case operand.Kind() == "identifier":
			memberReceiver = receiverName
			memberReceiverType = scope[receiverName]
			if !scope.has(receiverName) && x.localTypeNames.has(receiverName) {
				memberReceiverType = receiverName
			}
		case operand.Kind() == "parenthesized_expression":
			// `(*T).Method(x)`: a method expression on a local type.
			expression := operand
			for expression.Kind() == "parenthesized_expression" {
				named := namedChildren(expression)
				if len(named) == 0 {
					break
				}
				expression = named[0]
			}
			if expression.Kind() == "unary_expression" && strings.HasPrefix(x.text(expression), "*") {
				name := x.text(expression.ChildByFieldName("operand"))
				if x.localTypeNames.has(name) && !scope.has(name) {
					memberReceiver = name
					memberReceiverType = name
				}
			}
		case operand.Kind() == "selector_expression":
			// `s.logger.Log()` names the receiver by the field only when `s`
			// is this method's own receiver.
			if fieldName, ok := x.ownFieldSelector(operand, selfName, selfType, scope); ok {
				memberReceiver = fieldName
				fieldOwner = selfType
			}
		}
		if field != nil {
			calleeName = x.text(field)
		}
	}
	// A bare `append(s, x)` is the builtin, never the same-named method a
	// sibling file happens to declare. Skipping here drops the in-file phantom
	// edge and keeps the name out of rawCalls, so the cross-file pass cannot
	// bind it either.
	if isBareIdentifier && goPredeclaredFuncs.has(calleeName) {
		return
	}
	if calleeName == "" {
		return
	}
	if _, builtin := LanguageBuiltinGlobals[calleeName]; builtin {
		return
	}
	// A member call must be resolved through its receiver's type; a bare name
	// cannot prove the method belongs to that receiver.
	tgtNID := ""
	if importPath == "" && !isMemberCall {
		tgtNID = x.labelToNID[calleeName]
	}
	if tgtNID != "" && tgtNID != callerNID {
		pair := [2]string{callerNID, tgtNID}
		if _, seen := x.seenCallPairs[pair]; !seen {
			x.seenCallPairs[pair] = struct{}{}
			x.addEdge(callerNID, tgtNID, "calls", line(node), "call")
		}
		return
	}
	receiverBound := false
	if fieldOwner == "" && memberReceiver != "" {
		receiverBound = scope.has(memberReceiver)
	}
	x.rawCalls = append(x.rawCalls, RawCall{
		CallerNID:    callerNID,
		Callee:       calleeName,
		IsMemberCall: isMemberCall,
		Language:     "go",
		Receiver:     OptString(packageReceiver),
		// A key of its own: `receiver` means "imported package" here, and
		// member-call resolvers for other languages select on `is_member_call`
		// plus `receiver` without checking the language, so a Go name placed
		// there would bind to their types in a mixed corpus.
		MemberReceiver: OptString(memberReceiver),
		ReceiverType:   OptString(memberReceiverType),
		ReceiverBound:  receiverBound,
		// Field ownership remains explicit when the struct is declared in a
		// sibling file of the package.
		FieldOwner:     OptString(fieldOwner),
		ImportPath:     OptString(importPath),
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line(node)),
	})
}

// recordMethodValueArgs records method values passed as arguments.
//
// A method value passed as an argument (`routing.Wrap(s.Handle)`,
// `r.Get("/p", s.Handle)`) is how Go registers a handler or callback. It is not
// a call here, so the method would have no caller. Record it for the
// member-call resolver as an indirect call through the receiver's declared
// type. `s.field.Handle` is typed through the field, as a direct `s.field.M()`
// is, only when `s` is this method's own receiver.
func (x *goExtractor) recordMethodValueArgs(node *sitter.Node, callerNID, selfName, selfType string, scope TypeTable) {
	args := node.ChildByFieldName("arguments")
	if args == nil {
		return
	}
	for _, arg := range children(args) {
		if arg.Kind() != "selector_expression" {
			continue
		}
		operand, field := arg.ChildByFieldName("operand"), arg.ChildByFieldName("field")
		if operand == nil || field == nil {
			continue
		}
		var name, receiverType, fieldOwner string
		receiverBound := false
		switch operand.Kind() {
		case "identifier":
			name = x.text(operand)
			if _, isImport := x.importedPkgs[name]; isImport && !scope.has(name) {
				continue
			}
			receiverType = scope[name]
			receiverBound = scope.has(name)
		case "selector_expression":
			fieldName, ok := x.ownFieldSelector(operand, selfName, selfType, scope)
			if !ok {
				continue
			}
			name = fieldName
			fieldOwner = selfType
			// Python's `name in scope if not field_owner else False`: an empty
			// owner (a receiver without a type) still consults the scope.
			receiverBound = fieldOwner == "" && scope.has(name)
		default:
			continue
		}
		x.rawCalls = append(x.rawCalls, RawCall{
			CallerNID:      callerNID,
			Callee:         x.text(field),
			IsMemberCall:   true,
			Indirect:       true,
			Context:        "argument",
			Language:       "go",
			MemberReceiver: OptString(name),
			ReceiverType:   OptString(receiverType),
			ReceiverBound:  receiverBound,
			FieldOwner:     OptString(fieldOwner),
			SourceFile:     x.strPath,
			SourceLocation: fmt.Sprintf("L%d", line(arg)),
		})
	}
}
