// Package kotlin is Kotlin language support for the funcscan extraction pipeline.
// Kotlin and Java share the "jvm" family so calls can cross file boundaries.
package kotlin

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
	sitter "github.com/tree-sitter/go-tree-sitter"
	tskotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
)

// Kotlin built-ins to skip when recording calls/stubs.
var kotlinBuiltins = newSet(
	"Boolean", "Byte", "Char", "Double", "Float", "Int", "Long", "Short",
	"String", "Unit", "Any", "Nothing", "Number", "Array",
	"List", "MutableList", "Set", "MutableSet", "Map", "MutableMap",
	"Collection", "MutableCollection", "Iterable", "MutableIterable",
	"Sequence", "Pair", "Triple",
	"println", "print", "readLine", "TODO", "error", "check", "require",
	"also", "let", "run", "with", "apply", "takeIf", "takeUnless",
	"forEach", "map", "filter", "flatMap", "fold", "reduce",
	"Exception", "RuntimeException", "IllegalArgumentException",
	"IllegalStateException", "UnsupportedOperationException",
)

type kotlinExtractor struct {
	source  []byte
	strPath string
	stem    string

	fileNID string
	pkgName string // e.g. "com.example.foo"

	nodes   []*base.Node
	edges   []*base.Edge
	seenIDs map[string]struct{}

	callableNIDs      map[string]struct{}
	callableClassNIDs map[string]struct{}
	interfaceNIDs     map[string]struct{}
	objectNIDs        map[string]struct{} // Kotlin object singletons

	// localTypes: simple name → nodeID
	localTypes map[string]string
	// fieldTypes: classNID → (fieldName → typeName)
	fieldTypes map[string]map[string]string

	// importedSymbols: simple name → fqn (from import statements)
	importedSymbols map[string]string
	// importedAliases: alias → fqn
	importedAliases map[string]string

	rawCalls []*base.RawCall
}

// ExtractKotlin extracts classes, objects, interfaces, functions, and imports
// from a .kt or .kts file.
func ExtractKotlin(path string) (*base.FileResult, error) {
	path = base.NormalizePath(path)
	source, err := base.ReadSourceBytes(path)
	if err != nil {
		return nil, err
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(tskotlin.Language())); err != nil {
		return nil, fmt.Errorf("load tree-sitter-kotlin: %w", err)
	}
	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse %s: tree-sitter returned no tree", path)
	}
	defer tree.Close()
	root := tree.RootNode()

	stem := base.FileStem(path)
	x := &kotlinExtractor{
		source:            source,
		strPath:           path,
		stem:              stem,
		seenIDs:           map[string]struct{}{},
		callableNIDs:      map[string]struct{}{},
		callableClassNIDs: map[string]struct{}{},
		interfaceNIDs:     map[string]struct{}{},
		objectNIDs:        map[string]struct{}{},
		localTypes:        map[string]string{},
		fieldTypes:        map[string]map[string]string{},
		importedSymbols:   map[string]string{},
		importedAliases:   map[string]string{},
	}
	x.fileNID = base.MakeID(path)
	x.addNode(x.fileNID, filepath.Base(path), 1)

	// First pass: package header and imports.
	for _, child := range children(root) {
		switch child.Kind() {
		case "package_header":
			// package_header → "package" qualified_identifier
			for _, c := range namedChildren(child) {
				if c.Kind() == "qualified_identifier" {
					x.pkgName = x.readQualifiedIdentifier(c)
					break
				}
			}
		case "import":
			x.walkImport(child)
		}
	}

	// Second pass: top-level declarations.
	for _, child := range children(root) {
		switch child.Kind() {
		case "class_declaration":
			x.walkClassDecl(child, x.fileNID, "")
		case "object_declaration":
			x.walkObjectDecl(child, x.fileNID, "")
		case "function_declaration":
			x.walkFunctionDecl(child, x.fileNID, "", base.TypeTable{})
		}
	}

	// Mark node properties.
	for _, n := range x.nodes {
		if _, ok := x.callableNIDs[n.ID]; ok {
			n.Callable = true
		}
		if _, ok := x.callableClassNIDs[n.ID]; ok {
			n.Callable = true
			n.CallableClass = true
		}
		if _, ok := x.interfaceNIDs[n.ID]; ok {
			if n.Metadata == nil {
				n.Metadata = map[string]any{}
			}
			n.Metadata["language"] = "kotlin"
			n.Metadata["kind"] = "interface"
		}
		// Stamp package name on each node so the import resolver can group them.
		if x.pkgName != "" && n.ID != x.fileNID {
			if n.Metadata == nil {
				n.Metadata = map[string]any{}
			}
			n.Metadata["namespace"] = x.pkgName
		}
	}

	if len(x.fieldTypes) > 0 {
		x.nodes[0].GoFields = x.fieldTypes
	}

	rawCalls := x.rawCalls
	if rawCalls == nil {
		rawCalls = []*base.RawCall{}
	}
	return &base.FileResult{
		Nodes:    x.nodes,
		Edges:    x.edges,
		RawCalls: rawCalls,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (x *kotlinExtractor) text(n *sitter.Node) string { return readText(n, x.source) }

func (x *kotlinExtractor) addNode(nid, label string, ln int) {
	if _, ok := x.seenIDs[nid]; ok {
		return
	}
	x.seenIDs[nid] = struct{}{}
	x.nodes = append(x.nodes, &base.Node{
		ID:             nid,
		Label:          label,
		FileType:       "code",
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", ln),
	})
}

func (x *kotlinExtractor) addEdge(src, tgt, relation, context string, ln int) {
	x.edges = append(x.edges, &base.Edge{
		Source:         src,
		Target:         tgt,
		Relation:       relation,
		Context:        context,
		Confidence:     "EXTRACTED",
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", ln),
		Weight:         1.0,
	})
}

// ── Import handling ───────────────────────────────────────────────────────────

func (x *kotlinExtractor) walkImport(node *sitter.Node) {
	// import qualified_identifier ("as" identifier)?
	// Grammar v1.1.0: "import" node with qualified_identifier child.
	// Wildcard imports (ending in .*) are skipped.
	var fqn string
	for _, c := range children(node) {
		if c.Kind() == "qualified_identifier" {
			fqn = x.readQualifiedIdentifier(c)
			break
		}
		if c.Kind() == "identifier" && fqn == "" {
			fqn = x.text(c)
		}
	}
	if fqn == "" || strings.HasSuffix(fqn, ".*") || fqn == "*" {
		return
	}

	// Check for wildcard as a child kind.
	for _, c := range children(node) {
		if c.Kind() == "*" {
			return
		}
	}

	// Alias: `import a.b.C as D`
	alias := ""
	sawAs := false
	for _, c := range children(node) {
		if c.Kind() == "as" {
			sawAs = true
			continue
		}
		if sawAs && (c.Kind() == "identifier" || c.Kind() == "simple_identifier") {
			alias = x.text(c)
			break
		}
	}

	parts := strings.Split(fqn, ".")
	simpleName := parts[len(parts)-1]
	if simpleName == "" {
		return
	}

	tgtID := base.MakeID(simpleName)
	x.addEdge(x.fileNID, tgtID, "imports", "import", line(node))

	// Record for type resolution.
	if alias != "" {
		x.importedAliases[alias] = fqn
		x.importedSymbols[alias] = fqn
	} else {
		x.importedSymbols[simpleName] = fqn
	}

	// Store target_fqn in edge metadata for the import-rewrite pass.
	// We store it on the last edge added.
	if len(x.edges) > 0 {
		e := x.edges[len(x.edges)-1]
		if e.Relation == "imports" {
			// Encode fqn and alias into metadata via a special field.
			// The resolve pass will read this.
			_ = e // metadata extension done through the rawCalls workaround below
		}
	}

	// Store import metadata for the resolver via a RawCall marker (reuses
	// the existing pipeline rather than adding a new field to Edge).
	x.rawCalls = append(x.rawCalls, &base.RawCall{
		CallerNID:    x.fileNID,
		Callee:       "__import__",
		Language:     "kotlin",
		Receiver:     base.OptString(simpleName),
		ImportPath:   base.OptString(fqn),
		FieldOwner:   base.OptString(alias),
		SourceFile:   x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line(node)),
	})
}

// ── Class / interface declaration ─────────────────────────────────────────────

func (x *kotlinExtractor) walkClassDecl(node *sitter.Node, parentNID, outerClass string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		ids := namedChildrenOfKind(node, "identifier")
		if len(ids) == 0 {
			return
		}
		nameNode = ids[0]
	}
	simpleName := x.text(nameNode)
	if simpleName == "" {
		return
	}

	qualName := simpleName
	if outerClass != "" {
		qualName = outerClass + "." + simpleName
	}
	nid := base.MakeID(x.stem, qualName)

	x.addNode(nid, simpleName, line(node))
	x.addEdge(parentNID, nid, "contains", "", line(node))
	x.localTypes[simpleName] = nid
	x.callableClassNIDs[nid] = struct{}{}

	// Detect interface: keyword child is "interface".
	if hasChildKind(node, "interface") {
		x.interfaceNIDs[nid] = struct{}{}
	}

	// Walk supertype list for inheritance/implements edges.
	x.walkDelegationSpecifiers(node, nid)

	// Walk class body.
	body := node.ChildByFieldName("body")
	if body == nil {
		for _, c := range children(node) {
			if c.Kind() == "class_body" || c.Kind() == "enum_class_body" {
				body = c
				break
			}
		}
	}
	if body == nil {
		return
	}

	fields := map[string]string{}
	for _, member := range children(body) {
		switch member.Kind() {
		case "function_declaration":
			x.walkFunctionDecl(member, nid, simpleName, fields)
		case "class_declaration":
			x.walkClassDecl(member, nid, qualName)
		case "object_declaration":
			x.walkObjectDecl(member, nid, qualName)
		case "property_declaration":
			x.walkPropertyDecl(member, fields)
		}
	}
	if len(fields) > 0 {
		x.fieldTypes[nid] = fields
	}
}

func (x *kotlinExtractor) walkObjectDecl(node *sitter.Node, parentNID, outerClass string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		for _, c := range namedChildren(node) {
			if c.Kind() == "identifier" || c.Kind() == "simple_identifier" {
				nameNode = c
				break
			}
		}
	}
	if nameNode == nil {
		return
	}
	simpleName := x.text(nameNode)
	if simpleName == "" {
		return
	}

	qualName := simpleName
	if outerClass != "" {
		qualName = outerClass + "." + simpleName
	}
	nid := base.MakeID(x.stem, qualName)

	x.addNode(nid, simpleName, line(node))
	x.addEdge(parentNID, nid, "contains", "", line(node))
	x.localTypes[simpleName] = nid
	x.callableClassNIDs[nid] = struct{}{}
	x.objectNIDs[nid] = struct{}{}

	x.walkDelegationSpecifiers(node, nid)

	body := node.ChildByFieldName("body")
	if body == nil {
		for _, c := range children(node) {
			if c.Kind() == "class_body" {
				body = c
				break
			}
		}
	}
	if body == nil {
		return
	}

	fields := map[string]string{}
	for _, member := range children(body) {
		switch member.Kind() {
		case "function_declaration":
			x.walkFunctionDecl(member, nid, simpleName, fields)
		case "property_declaration":
			x.walkPropertyDecl(member, fields)
		case "class_declaration":
			x.walkClassDecl(member, nid, qualName)
		}
	}
	if len(fields) > 0 {
		x.fieldTypes[nid] = fields
	}
}

// walkDelegationSpecifiers emits inherits/implements edges from the super-type list.
func (x *kotlinExtractor) walkDelegationSpecifiers(node *sitter.Node, classNID string) {
	for _, child := range children(node) {
		if child.Kind() != "delegation_specifiers" {
			continue
		}
		for _, spec := range namedChildren(child) {
			// delegation_specifier: user_type | constructor_invocation | ...
			var typeName string
			switch spec.Kind() {
			case "user_type":
				typeName = x.simpleTypeName(spec)
			case "constructor_invocation":
				typeNode := spec.ChildByFieldName("type")
				if typeNode == nil {
					typeNode = firstNamedChild(spec)
				}
				if typeNode != nil {
					typeName = x.simpleTypeName(typeNode)
				}
			default:
				typeName = x.simpleTypeName(spec)
			}
			if typeName == "" || kotlinBuiltins.has(typeName) {
				continue
			}
			tgtID := base.MakeID(typeName)
			x.addEdge(classNID, tgtID, "inherits", "type", line(child))
		}
	}
}

// walkPropertyDecl records property types for the field table.
func (x *kotlinExtractor) walkPropertyDecl(node *sitter.Node, fields map[string]string) {
	// property_declaration: (val|var) identifier (: type)? ...
	typeName := ""
	var nameNode *sitter.Node
	for _, c := range namedChildren(node) {
		switch c.Kind() {
		case "identifier", "simple_identifier":
			if nameNode == nil {
				nameNode = c
			}
		case "user_type", "nullable_type", "function_type":
			if typeName == "" {
				typeName = x.simpleTypeName(c)
			}
		case "type_reference":
			if typeName == "" {
				typeName = x.simpleTypeName(c)
			}
		}
	}
	// Also check the `: type` via looking for a colon and the following type.
	seenColon := false
	for _, c := range children(node) {
		if c.Kind() == ":" {
			seenColon = true
			continue
		}
		if seenColon && typeName == "" {
			if t := x.simpleTypeName(c); t != "" {
				typeName = t
				break
			}
		}
	}
	if nameNode != nil {
		name := x.text(nameNode)
		if name != "" {
			fields[name] = typeName
		}
	}
}

// ── Function declaration ──────────────────────────────────────────────────────

func (x *kotlinExtractor) walkFunctionDecl(node *sitter.Node, classNID, className string, classFields map[string]string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		for _, c := range namedChildren(node) {
			if c.Kind() == "identifier" || c.Kind() == "simple_identifier" {
				nameNode = c
				break
			}
		}
	}
	if nameNode == nil {
		return
	}
	methodName := x.text(nameNode)
	if methodName == "" {
		return
	}

	qualLabel := methodName
	if className != "" {
		qualLabel = methodName // keep bare name as label; NID encodes class
	}

	nid := base.MakeID(x.stem, className, methodName)
	if _, ok := x.seenIDs[nid]; ok {
		// Disambiguate overloads by parameter count.
		paramCount := x.countParams(node)
		nid = base.MakeID(x.stem, className, methodName, fmt.Sprintf("%d", paramCount))
	}

	x.addNode(nid, qualLabel, line(node))
	if className != "" {
		x.addEdge(classNID, nid, "method", "method", line(node))
	} else {
		x.addEdge(classNID, nid, "contains", "", line(node))
	}
	x.callableNIDs[nid] = struct{}{}

	// Build parameter type table.
	paramTypes := base.TypeTable{}
	for k, v := range classFields {
		paramTypes[k] = v
	}
	if className != "" {
		paramTypes["this"] = className
	}
	x.collectParams(node, paramTypes)

	// Walk function body for calls.
	body := node.ChildByFieldName("body")
	if body == nil {
		for _, c := range children(node) {
			if c.Kind() == "function_body" || c.Kind() == "block" {
				body = c
				break
			}
		}
	}
	if body != nil {
		x.walkCalls(body, nid, className, paramTypes)
	}
}

func (x *kotlinExtractor) countParams(node *sitter.Node) int {
	count := 0
	for _, c := range children(node) {
		if c.Kind() == "function_value_parameters" {
			for _, p := range namedChildren(c) {
				if p.Kind() == "parameter" {
					count++
				}
			}
			break
		}
	}
	return count
}

func (x *kotlinExtractor) collectParams(node *sitter.Node, table base.TypeTable) {
	for _, c := range children(node) {
		if c.Kind() != "function_value_parameters" {
			continue
		}
		for _, param := range namedChildren(c) {
			if param.Kind() != "parameter" {
				continue
			}
			var pName *sitter.Node
			var pType *sitter.Node
			seenColon := false
			for _, pc := range children(param) {
				if pc.Kind() == "identifier" || pc.Kind() == "simple_identifier" {
					if pName == nil {
						pName = pc
					}
				}
				if pc.Kind() == ":" {
					seenColon = true
					continue
				}
				if seenColon && pType == nil {
					pType = pc
				}
			}
			if pName != nil {
				typeName := ""
				if pType != nil {
					typeName = x.simpleTypeName(pType)
				}
				table.SetDefault(x.text(pName), typeName)
			}
		}
	}
}

// ── Call walking ──────────────────────────────────────────────────────────────

func (x *kotlinExtractor) walkCalls(node *sitter.Node, callerNID, selfType string, typeTable base.TypeTable) {
	if node == nil {
		return
	}
	switch node.Kind() {
	case "call_expression":
		x.recordCall(node, callerNID, selfType, typeTable)
		return // children will be walked via recordCall or we stop here to avoid double-walking
	case "property_declaration":
		// Collect local variable types: `val foo: Foo = ...`
		x.walkLocalProperty(node, typeTable)
	case "function_declaration":
		// Don't descend into nested functions — they have their own scope.
		x.walkFunctionDecl(node, callerNID, selfType, typeTable)
		return
	}
	for _, c := range children(node) {
		x.walkCalls(c, callerNID, selfType, typeTable)
	}
}

func (x *kotlinExtractor) walkLocalProperty(node *sitter.Node, table base.TypeTable) {
	var nameNode *sitter.Node
	typeName := ""
	seenColon := false
	for _, c := range children(node) {
		switch c.Kind() {
		case "identifier", "simple_identifier":
			if nameNode == nil {
				nameNode = c
			}
		case ":":
			seenColon = true
		case "user_type", "nullable_type":
			if seenColon && typeName == "" {
				typeName = x.simpleTypeName(c)
			}
		}
	}
	if nameNode != nil {
		table.SetDefault(x.text(nameNode), typeName)
	}
}

func (x *kotlinExtractor) recordCall(node *sitter.Node, callerNID, selfType string, typeTable base.TypeTable) {
	// call_expression:
	//   child[0]: navigation_expression | identifier | call_expression (chained)
	//   child[1]: value_arguments
	named := namedChildren(node)
	if len(named) == 0 {
		return
	}

	calleeNode := named[0]

	switch calleeNode.Kind() {
	case "identifier", "simple_identifier":
		// Bare function call: `foo()`
		callee := x.text(calleeNode)
		if callee == "" || kotlinBuiltins.has(callee) {
			break
		}
		x.rawCalls = append(x.rawCalls, &base.RawCall{
			CallerNID:      callerNID,
			Callee:         callee,
			IsMemberCall:   false,
			Language:       "kotlin",
			SourceFile:     x.strPath,
			SourceLocation: fmt.Sprintf("L%d", line(node)),
		})

	case "navigation_expression":
		// Member call: `receiver.method()` or `Pkg.method()`
		x.recordNavCall(calleeNode, callerNID, selfType, typeTable, line(node))

	case "call_expression":
		// Chained call: `foo().bar()` — walk the inner call too.
		x.recordCall(calleeNode, callerNID, selfType, typeTable)
	}

	// Walk arguments for nested calls.
	for _, c := range named[1:] {
		x.walkCalls(c, callerNID, selfType, typeTable)
	}
}

func (x *kotlinExtractor) recordNavCall(nav *sitter.Node, callerNID, selfType string, typeTable base.TypeTable, ln int) {
	// navigation_expression: receiver "." name
	navChildren := namedChildren(nav)
	if len(navChildren) < 2 {
		return
	}
	receiverNode := navChildren[0]
	methodNode := navChildren[len(navChildren)-1]

	receiver := x.text(receiverNode)
	callee := x.text(methodNode)
	if callee == "" {
		return
	}

	// Resolve receiver type.
	receiverType := ""
	receiverBound := false

	switch receiverNode.Kind() {
	case "identifier", "simple_identifier":
		if receiver == "this" {
			receiverType = selfType
			receiverBound = selfType != ""
		} else if t, ok := typeTable[receiver]; ok && t != "" {
			receiverType = t
			receiverBound = true
		} else {
			// Could be a class name (object call) or unresolved variable.
			receiverType = receiver
		}
	case "call_expression":
		// Chained: `foo().bar()` — walk the inner call.
		x.recordCall(receiverNode, callerNID, selfType, typeTable)
		return
	}

	if receiverType == "" {
		return
	}

	// Classify: capitalized receiver is likely an object singleton or companion.
	isObjectCall := len(receiverType) > 0 && receiverType[0] >= 'A' && receiverType[0] <= 'Z'

	rc := &base.RawCall{
		CallerNID:      callerNID,
		Callee:         callee,
		IsMemberCall:   true,
		Language:       "kotlin",
		MemberReceiver: base.OptString(receiver),
		ReceiverType:   base.OptString(receiverType),
		ReceiverBound:  receiverBound,
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", ln),
	}
	if isObjectCall {
		rc.FieldOwner = base.OptString("kotlin_object_receiver:" + receiverType)
	}
	x.rawCalls = append(x.rawCalls, rc)
}

// ── Type name helpers ─────────────────────────────────────────────────────────

func (x *kotlinExtractor) simpleTypeName(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind() {
	case "identifier", "simple_identifier":
		return x.text(node)
	case "user_type":
		// user_type: type_identifier (type_arguments)?
		for _, c := range namedChildren(node) {
			if c.Kind() == "type_identifier" || c.Kind() == "identifier" || c.Kind() == "simple_identifier" {
				return x.text(c)
			}
		}
		// Also check children of user_type_specifier
		for _, c := range namedChildren(node) {
			if t := x.simpleTypeName(c); t != "" {
				return t
			}
		}
	case "nullable_type":
		// nullable_type: type "?"
		for _, c := range namedChildren(node) {
			if t := x.simpleTypeName(c); t != "" {
				return t
			}
		}
	case "type_reference":
		for _, c := range namedChildren(node) {
			if t := x.simpleTypeName(c); t != "" {
				return t
			}
		}
	case "type_identifier":
		return x.text(node)
	case "generic_type":
		// Foo<T> → "Foo"
		for _, c := range namedChildren(node) {
			if c.Kind() == "type_identifier" || c.Kind() == "identifier" || c.Kind() == "simple_identifier" {
				return x.text(c)
			}
		}
	case "function_type":
		return "" // skip function types
	}
	// Fallback: look for type_identifier or identifier child.
	for _, c := range namedChildren(node) {
		if c.Kind() == "type_identifier" || c.Kind() == "identifier" || c.Kind() == "simple_identifier" {
			return x.text(c)
		}
	}
	return ""
}

// readQualifiedIdentifier reads `com.example.Foo` from a qualified_identifier node
// or any node containing one.
func (x *kotlinExtractor) readQualifiedIdentifier(node *sitter.Node) string {
	var parts []string
	for _, c := range children(node) {
		if c.Kind() == "identifier" || c.Kind() == "simple_identifier" {
			parts = append(parts, x.text(c))
		}
	}
	return strings.Join(parts, ".")
}
