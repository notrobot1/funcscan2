// Package csharp is C# language support for the funcscan extraction pipeline.
package csharp

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
	sitter "github.com/tree-sitter/go-tree-sitter"
	tscsharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
)

// C# built-in types.
var csharpBuiltins = newSet(
	"bool", "byte", "char", "decimal", "double", "float", "int", "long",
	"object", "sbyte", "short", "string", "uint", "ulong", "ushort", "void",
	"Boolean", "Byte", "Char", "Decimal", "Double", "Single", "Int32", "Int64",
	"Object", "String", "UInt32", "UInt64",
	"Exception", "ArgumentException", "InvalidOperationException", "NullReferenceException",
	"Console", "Math", "Convert", "Environment",
	"List", "Dictionary", "HashSet", "Queue", "Stack", "Array",
	"IEnumerable", "IList", "IDictionary", "ICollection", "IDisposable",
	"Task", "Thread", "Action", "Func", "Predicate",
	"Enum", "Attribute", "Type", "Delegate", "Event",
	"var",
)

// C# type declaration node kinds.
var csharpClassTypes = newSet(
	"class_declaration",
	"interface_declaration",
	"enum_declaration",
	"struct_declaration",
	"record_declaration",
	"record_struct_declaration",
	"delegate_declaration",
)

// C# method node kinds.
var csharpFuncTypes = newSet(
	"method_declaration",
	"constructor_declaration",
	"operator_declaration",
	"conversion_operator_declaration",
	"destructor_declaration",
)

type csharpExtractor struct {
	source  []byte
	strPath string
	stem    string

	fileNID   string
	nsScope   string // current namespace (dotted)

	nodes   []*base.Node
	edges   []*base.Edge
	seenIDs map[string]struct{}

	callableNIDs      map[string]struct{}
	callableClassNIDs map[string]struct{}
	interfaceNIDs     map[string]struct{}

	// localTypes: simple name → nodeID (declared in this file)
	localTypes map[string]string

	// fieldTypes: classNID → (fieldName → typeName)
	fieldTypes map[string]map[string]string

	// usingNamespaces: list of imported namespaces (for type resolution)
	usingNamespaces []string
	// usingAliases: alias → fqn
	usingAliases map[string]string

	rawCalls []*base.RawCall
}

// ExtractCSharp extracts namespaces, classes, interfaces, methods, and usings
// from a .cs file.
func ExtractCSharp(path string) (*base.FileResult, error) {
	path = base.NormalizePath(path)
	source, err := base.ReadSourceBytes(path)
	if err != nil {
		return nil, err
	}

	// Apply source normalizations that work around tree-sitter-c-sharp grammar
	// limitations. Each pass is byte-length preserving so reported line/column
	// offsets stay accurate (#2876 contract). Apply in the same order as the
	// Python original: preprocessor blanking first, then verbatim-interpolation
	// fix, then contextual-name escaping.
	parseSource := source
	if norm := blankPreprocessor(parseSource); norm != nil {
		parseSource = norm
	}
	if norm := escapeContextualNames(parseSource); norm != nil {
		parseSource = norm
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(tscsharp.Language())); err != nil {
		return nil, fmt.Errorf("load tree-sitter-c-sharp: %w", err)
	}
	tree := parser.Parse(parseSource, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse %s: tree-sitter returned no tree", path)
	}
	defer tree.Close()
	root := tree.RootNode()

	stem := base.FileStem(path)
	x := &csharpExtractor{
		source:            source,
		strPath:           path,
		stem:              stem,
		nsScope:           base.ParentDirName(path),
		seenIDs:           map[string]struct{}{},
		callableNIDs:      map[string]struct{}{},
		callableClassNIDs: map[string]struct{}{},
		interfaceNIDs:     map[string]struct{}{},
		localTypes:        map[string]string{},
		fieldTypes:        map[string]map[string]string{},
		usingAliases:      map[string]string{},
	}
	x.fileNID = base.MakeID(path)
	x.addNode(x.fileNID, filepath.Base(path), 1)

	// First pass: collect using directives.
	for _, child := range children(root) {
		if child.Kind() == "using_directive" {
			x.walkUsing(child)
		}
	}

	// Second pass: walk top-level declarations.
	for _, child := range children(root) {
		switch child.Kind() {
		case "namespace_declaration", "file_scoped_namespace_declaration":
			x.walkNamespace(child, x.fileNID)
		default:
			if csharpClassTypes.has(child.Kind()) {
				x.walkTypeDecl(child, x.fileNID, x.nsScope)
			}
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
			n.Metadata["language"] = "csharp"
			n.Metadata["kind"] = "interface"
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

// ── Node/edge helpers ─────────────────────────────────────────────────────────

func (x *csharpExtractor) text(n *sitter.Node) string { return readText(n, x.source) }

func (x *csharpExtractor) addNode(nid, label string, ln int) {
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

func (x *csharpExtractor) addEdge(src, tgt, relation, context string, ln int) {
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

// ── Using directives ──────────────────────────────────────────────────────────

func (x *csharpExtractor) walkUsing(node *sitter.Node) {
	// using_directive:
	//   "using" "static"? ("global" "::")? qualified_name ";"
	//   "using" identifier "=" qualified_name ";"  (alias)
	raw := x.readQualifiedName(node)
	if raw == "" {
		return
	}

	isAlias := false
	aliasName := ""
	for _, c := range children(node) {
		if c.Kind() == "name_equals" {
			// alias form: `using A = B.C`
			nameNode := c.ChildByFieldName("name")
			if nameNode == nil {
				nameNode = c.Child(0)
			}
			if nameNode != nil {
				aliasName = x.text(nameNode)
			}
			isAlias = true
		}
	}

	tgtID := base.MakeID(raw)
	x.addEdge(x.fileNID, tgtID, "imports", "import", line(node))
	if isAlias && aliasName != "" {
		x.usingAliases[aliasName] = raw
	} else {
		x.usingNamespaces = append(x.usingNamespaces, raw)
	}
}

// ── Namespace ─────────────────────────────────────────────────────────────────

func (x *csharpExtractor) walkNamespace(node *sitter.Node, parentNID string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	nsName := x.readQualifiedNameNode(nameNode)
	if nsName == "" {
		return
	}

	nid := base.MakeID(x.stem, nsName)
	x.addNode(nid, nsName, line(node))
	x.addEdge(parentNID, nid, "contains", "", line(node))

	// Walk namespace body.
	body := node.ChildByFieldName("body")
	if body == nil {
		// file-scoped namespace has no body node; walk siblings.
		for _, c := range children(node) {
			if csharpClassTypes.has(c.Kind()) {
				x.walkTypeDecl(c, nid, nsName)
			} else if c.Kind() == "namespace_declaration" || c.Kind() == "file_scoped_namespace_declaration" {
				x.walkNamespace(c, nid)
			}
		}
		return
	}
	for _, c := range children(body) {
		if csharpClassTypes.has(c.Kind()) {
			x.walkTypeDecl(c, nid, nsName)
		} else if c.Kind() == "namespace_declaration" {
			x.walkNamespace(c, nid)
		}
	}
	x.nsScope = nsName
}

// ── Type declarations ─────────────────────────────────────────────────────────

func (x *csharpExtractor) walkTypeDecl(node *sitter.Node, parentNID, nsScope string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	simpleName := x.text(nameNode)
	if simpleName == "" {
		return
	}

	nid := base.MakeID(x.stem, nsScope, simpleName)
	x.addNode(nid, simpleName, line(node))
	x.addEdge(parentNID, nid, "contains", "", line(node))
	x.localTypes[simpleName] = nid
	x.callableClassNIDs[nid] = struct{}{}

	if node.Kind() == "interface_declaration" {
		x.interfaceNIDs[nid] = struct{}{}
	}

	// Base types / interfaces.
	x.walkBaseList(node, nid)

	// Walk declaration body.
	body := node.ChildByFieldName("body")
	if body == nil {
		// Some node kinds name it differently.
		for _, c := range children(node) {
			if strings.HasSuffix(c.Kind(), "_body") || c.Kind() == "declaration_list" {
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
		switch {
		case csharpFuncTypes.has(member.Kind()):
			x.walkMethod(member, nid, simpleName, fields)
		case csharpClassTypes.has(member.Kind()):
			x.walkTypeDecl(member, nid, nsScope+"."+simpleName)
		case member.Kind() == "field_declaration":
			x.walkField(member, fields)
		case member.Kind() == "property_declaration":
			x.walkProperty(member, fields)
		}
	}
	if len(fields) > 0 {
		x.fieldTypes[nid] = fields
	}
}

// walkBaseList emits inherits/implements edges from the base_list.
func (x *csharpExtractor) walkBaseList(node *sitter.Node, classNID string) {
	for _, c := range children(node) {
		if c.Kind() != "base_list" {
			continue
		}
		// In tree-sitter-c-sharp, base_list children are type nodes directly
		// (identifier, generic_name, qualified_name).
		for _, bt := range namedChildren(c) {
			name := x.simpleTypeName(bt)
			if name == "" || csharpBuiltins.has(name) {
				continue
			}
			tgtID := base.MakeID(name)
			x.addEdge(classNID, tgtID, "inherits", "type", line(c))
		}
	}
}

func (x *csharpExtractor) walkField(node *sitter.Node, fields map[string]string) {
	// field_declaration: variable_declaration ";"
	// variable_declaration: type declarator+
	typeName := ""
	for _, c := range namedChildren(node) {
		if c.Kind() == "variable_declaration" {
			typeNode := c.ChildByFieldName("type")
			if typeNode != nil {
				typeName = x.simpleTypeName(typeNode)
			}
			for _, v := range namedChildren(c) {
				if v.Kind() == "variable_declarator" {
					nameNode := v.ChildByFieldName("name")
					if nameNode != nil {
						fields[x.text(nameNode)] = typeName
					}
				}
			}
			return
		}
	}
	for _, c := range namedChildren(node) {
		if c.Kind() == "variable_declaration" {
			for _, v := range namedChildren(c) {
				if v.Kind() == "variable_declarator" {
					nameNode := v.ChildByFieldName("name")
					if nameNode != nil {
						fields[x.text(nameNode)] = typeName
					}
				}
			}
		}
		// Also handle direct variable_declarator children.
		if c.Kind() == "variable_declarator" {
			nameNode := c.ChildByFieldName("name")
			if nameNode != nil {
				fields[x.text(nameNode)] = typeName
			}
		}
	}
}

func (x *csharpExtractor) walkProperty(node *sitter.Node, fields map[string]string) {
	nameNode := node.ChildByFieldName("name")
	typeNode := node.ChildByFieldName("type")
	if nameNode != nil {
		typeName := ""
		if typeNode != nil {
			typeName = x.simpleTypeName(typeNode)
		}
		fields[x.text(nameNode)] = typeName
	}
}

// ── Method walking ────────────────────────────────────────────────────────────

func (x *csharpExtractor) walkMethod(node *sitter.Node, classNID, className string, classFields map[string]string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		// Constructor: name from class name.
		if node.Kind() == "constructor_declaration" {
			nameNode = node.ChildByFieldName("identifier")
		}
		if nameNode == nil {
			return
		}
	}
	methodName := x.text(nameNode)
	if methodName == "" {
		return
	}

	nid := base.MakeID(x.stem, className, methodName)
	if _, ok := x.seenIDs[nid]; ok {
		params := node.ChildByFieldName("parameters")
		paramCount := 0
		if params != nil {
			for _, c := range namedChildren(params) {
				if strings.HasSuffix(c.Kind(), "parameter") {
					paramCount++
				}
			}
		}
		nid = base.MakeID(x.stem, className, methodName, fmt.Sprintf("%d", paramCount))
	}

	x.addNode(nid, methodName, line(node))
	x.addEdge(classNID, nid, "method", "method", line(node))
	x.callableNIDs[nid] = struct{}{}

	// Collect parameter types for the call walk.
	paramTypes := base.TypeTable{}
	for k, v := range classFields {
		paramTypes[k] = v
	}
	paramTypes["this"] = className
	paramTypes["base"] = className

	params := node.ChildByFieldName("parameters")
	if params != nil {
		for _, param := range namedChildren(params) {
			if param.Kind() == "parameter" {
				pName := param.ChildByFieldName("name")
				pType := param.ChildByFieldName("type")
				if pName != nil && pType != nil {
					paramTypes.SetDefault(x.text(pName), x.simpleTypeName(pType))
				}
			}
		}
	}

	body := node.ChildByFieldName("body")
	if body != nil {
		x.walkCalls(body, nid, className, paramTypes)
	}
}

// ── Call walking ──────────────────────────────────────────────────────────────

func (x *csharpExtractor) walkCalls(node *sitter.Node, callerNID, selfType string, typeTable base.TypeTable) {
	if node == nil {
		return
	}
	switch node.Kind() {
	case "invocation_expression":
		x.recordCall(node, callerNID, selfType, typeTable)
	case "object_creation_expression":
		x.recordNewCall(node, callerNID)
	case "local_declaration_statement":
		// `Foo bar = ...`
		decl := node.ChildByFieldName("declaration")
		if decl != nil {
			x.collectLocalTypes(decl, typeTable)
		}
	}
	for _, c := range children(node) {
		x.walkCalls(c, callerNID, selfType, typeTable)
	}
}

func (x *csharpExtractor) collectLocalTypes(node *sitter.Node, typeTable base.TypeTable) {
	// variable_declaration: type declarator+
	typeNode := node.ChildByFieldName("type")
	typeName := ""
	if typeNode != nil {
		typeName = x.simpleTypeName(typeNode)
		if typeName == "var" {
			typeName = "" // unknown type
		}
	}
	for _, c := range namedChildren(node) {
		if c.Kind() == "variable_declarator" {
			nameNode := c.ChildByFieldName("name")
			if nameNode != nil {
				typeTable.SetDefault(x.text(nameNode), typeName)
			}
		}
	}
}

func (x *csharpExtractor) recordCall(node *sitter.Node, callerNID, selfType string, typeTable base.TypeTable) {
	// invocation_expression:
	//   function: (identifier | member_access_expression | ...) arguments
	funcNode := node.ChildByFieldName("function")
	if funcNode == nil {
		return
	}

	callee := ""
	receiverText := ""
	isMember := false

	switch funcNode.Kind() {
	case "identifier":
		callee = x.text(funcNode)
	case "member_access_expression":
		// expr.Name
		exprNode := funcNode.ChildByFieldName("expression")
		nameNode := funcNode.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		callee = x.text(nameNode)
		if exprNode != nil {
			receiverText = x.simpleReceiverName(exprNode)
		}
		isMember = true
	case "generic_name":
		// Method<T>()
		nameNode := funcNode.ChildByFieldName("identifier")
		if nameNode != nil {
			callee = x.text(nameNode)
		}
	default:
		return
	}
	if callee == "" {
		return
	}

	receiverType := ""
	receiverBound := false
	if isMember {
		switch receiverText {
		case "this":
			receiverType = selfType
			receiverBound = true
		case "base":
			receiverType = selfType
			receiverBound = true
		case "":
		default:
			if t, ok := typeTable[receiverText]; ok && t != "" {
				receiverType = t
				receiverBound = true
			} else {
				receiverType = receiverText
			}
		}
	}

	rc := &base.RawCall{
		CallerNID:      callerNID,
		Callee:         callee,
		IsMemberCall:   isMember,
		Language:       "csharp",
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line(node)),
	}
	if isMember {
		rc.MemberReceiver = base.OptString(receiverText)
		rc.ReceiverType = base.OptString(receiverType)
		rc.ReceiverBound = receiverBound
	}
	x.rawCalls = append(x.rawCalls, rc)
}

func (x *csharpExtractor) recordNewCall(node *sitter.Node, callerNID string) {
	typeNode := node.ChildByFieldName("type")
	if typeNode == nil {
		return
	}
	typeName := x.simpleTypeName(typeNode)
	if typeName == "" || csharpBuiltins.has(typeName) {
		return
	}
	x.rawCalls = append(x.rawCalls, &base.RawCall{
		CallerNID:      callerNID,
		Callee:         typeName,
		IsMemberCall:   false,
		Language:       "csharp",
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line(node)),
	})
}

// ── Type name helpers ─────────────────────────────────────────────────────────

func (x *csharpExtractor) simpleTypeName(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind() {
	case "identifier":
		return x.text(node)
	case "generic_name":
		nameNode := node.ChildByFieldName("identifier")
		if nameNode != nil {
			return x.text(nameNode)
		}
	case "qualified_name":
		// A.B.C → take last part
		nameNode := node.ChildByFieldName("name")
		if nameNode != nil {
			return x.simpleTypeName(nameNode)
		}
	case "nullable_type":
		typeNode := node.ChildByFieldName("type")
		if typeNode != nil {
			return x.simpleTypeName(typeNode)
		}
	case "array_type":
		typeNode := node.ChildByFieldName("type")
		if typeNode != nil {
			return x.simpleTypeName(typeNode)
		}
	case "predefined_type":
		return x.text(node)
	}
	// Fallback: look for an identifier child.
	for _, c := range namedChildren(node) {
		if c.Kind() == "identifier" {
			return x.text(c)
		}
	}
	return ""
}

func (x *csharpExtractor) simpleReceiverName(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind() {
	case "identifier":
		return x.text(node)
	case "this_expression":
		return "this"
	case "base_expression":
		return "base"
	case "member_access_expression":
		nameNode := node.ChildByFieldName("name")
		if nameNode != nil {
			return x.text(nameNode)
		}
	}
	return ""
}

// readQualifiedName reads a dotted name from an arbitrary subtree.
func (x *csharpExtractor) readQualifiedName(node *sitter.Node) string {
	for _, c := range namedChildren(node) {
		if name := x.readQualifiedNameNode(c); name != "" {
			return name
		}
	}
	return ""
}

func (x *csharpExtractor) readQualifiedNameNode(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind() {
	case "identifier":
		return x.text(node)
	case "qualified_name":
		// tree-sitter-c-sharp uses "qualifier" and "name" (not "left"/"right")
		qualifier := node.ChildByFieldName("qualifier")
		name := node.ChildByFieldName("name")
		l := x.readQualifiedNameNode(qualifier)
		r := x.readQualifiedNameNode(name)
		if l != "" && r != "" {
			return l + "." + r
		}
		// Fallback: collect all identifier children in order
		if l == "" && r == "" {
			var parts []string
			for _, c := range children(node) {
				if c.Kind() == "identifier" {
					parts = append(parts, x.text(c))
				}
			}
			if len(parts) > 0 {
				result := parts[0]
				for _, p := range parts[1:] {
					result += "." + p
				}
				return result
			}
		}
		return l + r
	case "alias_qualified_name":
		nameNode := node.ChildByFieldName("name")
		if nameNode != nil {
			return x.readQualifiedNameNode(nameNode)
		}
	case "generic_name":
		// Foo<T> → just "Foo"
		nameNode := node.ChildByFieldName("identifier")
		if nameNode != nil {
			return x.text(nameNode)
		}
	}
	return ""
}
