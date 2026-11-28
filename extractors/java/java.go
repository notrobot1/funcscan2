// Package java is Java language support: per-file extractor and cross-file
// passes for the funcscan extraction pipeline.
package java

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
	sitter "github.com/tree-sitter/go-tree-sitter"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
)

// Java built-in types that should not produce cross-file stubs.
var javaBuiltins = newSet(
	"boolean", "byte", "char", "double", "float", "int", "long", "short", "void",
	"Boolean", "Byte", "Character", "Double", "Float", "Integer", "Long", "Short",
	"String", "Object", "Number", "Math", "System", "StringBuilder", "StringBuffer",
	"Comparable", "Iterable", "Cloneable", "Runnable", "Serializable",
	"Exception", "RuntimeException", "Error", "Throwable",
	"Override", "Deprecated", "SuppressWarnings", "FunctionalInterface",
	"List", "Map", "Set", "Collection", "Iterator", "Optional",
	"ArrayList", "HashMap", "HashSet", "LinkedList", "TreeMap", "TreeSet",
	"Arrays", "Collections", "Objects", "Class",
	"Thread", "Enum",
)

// Class types in tree-sitter-java grammar.
var javaClassTypes = newSet(
	"class_declaration",
	"interface_declaration",
	"enum_declaration",
	"record_declaration",
	"annotation_type_declaration",
)

// Function types.
var javaFuncTypes = newSet(
	"method_declaration",
	"constructor_declaration",
)

// javaExtractor holds the state of one file's extraction.
type javaExtractor struct {
	source  []byte
	strPath string
	stem    string

	fileNID  string
	pkgName  string // e.g. "com.example.foo"
	pkgScope string // last segment, used for id scoping

	nodes   []*base.Node
	edges   []*base.Edge
	seenIDs map[string]struct{}

	callableNIDs      map[string]struct{}
	callableClassNIDs map[string]struct{}
	interfaceNIDs     map[string]struct{}

	// localTypes: simple class name -> node id (declared in this file)
	localTypes map[string]string

	// fieldTypes: classNID -> (fieldName -> typeName)
	fieldTypes map[string]map[string]string

	// importedClasses: simple name -> fully-qualified name
	importedClasses map[string]string
	// importedPackages: package prefix -> true (wildcard imports)
	importedPackages []string

	rawCalls []*base.RawCall
}

// ExtractJava extracts classes, interfaces, methods, constructors, and imports
// from a .java file.
func ExtractJava(path string) (*base.FileResult, error) {
	path = base.NormalizePath(path)
	source, err := base.ReadSourceBytes(path)
	if err != nil {
		return nil, err
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(tsjava.Language())); err != nil {
		return nil, fmt.Errorf("load tree-sitter-java: %w", err)
	}
	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse %s: tree-sitter returned no tree", path)
	}
	defer tree.Close()
	root := tree.RootNode()

	stem := base.FileStem(path)
	x := &javaExtractor{
		source:            source,
		strPath:           path,
		stem:              stem,
		pkgScope:          base.ParentDirName(path),
		seenIDs:           map[string]struct{}{},
		callableNIDs:      map[string]struct{}{},
		callableClassNIDs: map[string]struct{}{},
		interfaceNIDs:     map[string]struct{}{},
		localTypes:        map[string]string{},
		fieldTypes:        map[string]map[string]string{},
		importedClasses:   map[string]string{},
	}
	x.fileNID = base.MakeID(path)
	x.addNode(x.fileNID, filepath.Base(path), 1)

	// First pass: read package declaration and collect import bindings.
	for _, child := range children(root) {
		switch child.Kind() {
		case "package_declaration":
			x.pkgName = x.readDottedName(child)
			if x.pkgName != "" {
				// Use last segment as package scope for id construction.
				parts := strings.Split(x.pkgName, ".")
				x.pkgScope = parts[len(parts)-1]
			}
		case "import_declaration":
			x.walkImport(child)
		}
	}

	// Second pass: walk top-level type declarations.
	for _, child := range children(root) {
		if javaClassTypes.has(child.Kind()) {
			x.walkTypeDecl(child, x.fileNID, "")
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
			n.Metadata["language"] = "java"
			n.Metadata["kind"] = "interface"
		}
	}

	// Attach field type tables to the file node for cross-file resolver.
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

func (x *javaExtractor) text(n *sitter.Node) string { return readText(n, x.source) }

func (x *javaExtractor) addNode(nid, label string, ln int) {
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

func (x *javaExtractor) addEdge(src, tgt, relation, context string, ln int) {
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

func (x *javaExtractor) walkImport(node *sitter.Node) {
	// import_declaration:
	//   "import" "static"? scoped_identifier(".")? identifier "*"? ";"
	raw := x.readDottedName(node)
	if raw == "" {
		return
	}
	isStatic := false
	isWild := false
	for _, c := range children(node) {
		switch c.Kind() {
		case "static":
			isStatic = true
		case "asterisk":
			isWild = true
		}
	}
	_ = isStatic

	tgtID := base.MakeID(raw)
	x.addEdge(x.fileNID, tgtID, "imports", "import", line(node))

	if isWild {
		x.importedPackages = append(x.importedPackages, raw)
		return
	}
	// Record simple-name → fqn for type lookup.
	parts := strings.Split(raw, ".")
	simpleName := parts[len(parts)-1]
	if simpleName != "" && simpleName != "*" {
		x.importedClasses[simpleName] = raw
	}
}

// ── Type declaration walking ──────────────────────────────────────────────────

func (x *javaExtractor) walkTypeDecl(node *sitter.Node, parentNID, outerClass string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	simpleName := x.text(nameNode)
	if simpleName == "" {
		return
	}

	// Build a qualified class name and node id.
	qualName := simpleName
	if outerClass != "" {
		qualName = outerClass + "." + simpleName
	}
	nid := base.MakeID(x.stem, qualName)

	x.addNode(nid, simpleName, line(node))
	x.addEdge(parentNID, nid, "contains", "", line(node))
	x.localTypes[simpleName] = nid
	x.callableClassNIDs[nid] = struct{}{}
	if node.Kind() == "interface_declaration" {
		x.interfaceNIDs[nid] = struct{}{}
	}

	// Walk superclass/interfaces for inheritance edges.
	x.walkSuperTypes(node, nid)

	// Walk class body.
	body := node.ChildByFieldName("body")
	if body == nil {
		// enum_declaration uses "body" for enum_body
		for _, c := range children(node) {
			if strings.HasSuffix(c.Kind(), "_body") {
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
		case javaFuncTypes.has(member.Kind()):
			x.walkMethod(member, nid, simpleName, fields)
		case javaClassTypes.has(member.Kind()):
			x.walkTypeDecl(member, nid, qualName)
		case member.Kind() == "field_declaration":
			x.walkField(member, fields)
		}
	}
	if len(fields) > 0 {
		x.fieldTypes[nid] = fields
	}
}

// walkSuperTypes emits inherits/implements edges for extends/implements clauses.
func (x *javaExtractor) walkSuperTypes(node *sitter.Node, classNID string) {
	for _, child := range children(node) {
		switch child.Kind() {
		case "superclass":
			// superclass: "extends" type
			for _, c := range children(child) {
				if name := x.simpleTypeName(c); name != "" && !javaBuiltins.has(name) {
					tgtID := x.resolveType(name)
					x.addEdge(classNID, tgtID, "inherits", "type", line(child))
				}
			}
		case "super_interfaces", "extends_interfaces":
			// super_interfaces: "implements" type_list
			for _, c := range children(child) {
				if c.Kind() == "type_list" {
					for _, t := range children(c) {
						if name := x.simpleTypeName(t); name != "" && !javaBuiltins.has(name) {
							tgtID := x.resolveType(name)
							x.addEdge(classNID, tgtID, "implements", "type", line(c))
						}
					}
				}
			}
		}
	}
}

// walkField records a field declaration's type for the type table.
func (x *javaExtractor) walkField(node *sitter.Node, fields map[string]string) {
	typeNode := node.ChildByFieldName("type")
	typeName := ""
	if typeNode != nil {
		typeName = x.simpleTypeName(typeNode)
	}
	for _, c := range children(node) {
		if c.Kind() == "variable_declarator" {
			nameNode := c.ChildByFieldName("name")
			if nameNode != nil {
				fieldName := x.text(nameNode)
				if fieldName != "" {
					fields[fieldName] = typeName
				}
			}
		}
	}
}

// walkMethod emits a method/constructor node and walks its call sites.
func (x *javaExtractor) walkMethod(node *sitter.Node, classNID, className string, classFields map[string]string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	methodName := x.text(nameNode)
	if methodName == "" {
		return
	}

	nid := base.MakeID(x.stem, className, methodName)
	// Disambiguate overloads by parameter count if needed.
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

	// Collect local parameter types for the call walk.
	paramTypes := base.TypeTable{}
	for k, v := range classFields {
		paramTypes[k] = v // inherit field types
	}
	paramTypes["this"] = className
	paramTypes["super"] = className

	params := node.ChildByFieldName("parameters")
	if params != nil {
		for _, param := range namedChildren(params) {
			if param.Kind() == "formal_parameter" || param.Kind() == "spread_parameter" {
				pName := param.ChildByFieldName("name")
				pType := param.ChildByFieldName("type")
				if pName != nil && pType != nil {
					paramTypes.SetDefault(x.text(pName), x.simpleTypeName(pType))
				}
			}
		}
	}

	// Walk method body for calls.
	body := node.ChildByFieldName("body")
	if body != nil {
		x.walkCalls(body, nid, className, paramTypes)
	}
}

// ── Call walking ──────────────────────────────────────────────────────────────

func (x *javaExtractor) walkCalls(node *sitter.Node, callerNID, selfType string, typeTable base.TypeTable) {
	if node == nil {
		return
	}
	switch node.Kind() {
	case "method_invocation":
		x.recordCall(node, callerNID, selfType, typeTable)
	case "object_creation_expression":
		x.recordNewCall(node, callerNID)
	case "local_variable_declaration":
		// Collect local variable types: `Foo bar = ...`
		typeNode := node.ChildByFieldName("type")
		typeName := ""
		if typeNode != nil {
			typeName = x.simpleTypeName(typeNode)
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
	for _, c := range children(node) {
		x.walkCalls(c, callerNID, selfType, typeTable)
	}
}

func (x *javaExtractor) recordCall(node *sitter.Node, callerNID, selfType string, typeTable base.TypeTable) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	callee := x.text(nameNode)
	if callee == "" {
		return
	}

	objectNode := node.ChildByFieldName("object")
	isMember := objectNode != nil
	receiverText := ""
	receiverType := ""
	receiverBound := false

	if isMember {
		receiverText = x.simpleReceiverName(objectNode)
		if receiverText == "this" || receiverText == selfType {
			receiverType = selfType
			receiverBound = true
		} else if receiverText != "" {
			if t, ok := typeTable[receiverText]; ok && t != "" {
				receiverType = t
				receiverBound = true
			} else if t == "" && ok {
				receiverType = ""
				receiverBound = false
			} else {
				// Might be a class name (static call) or unresolved variable.
				receiverType = receiverText
			}
		}
	}

	rc := &base.RawCall{
		CallerNID:    callerNID,
		Callee:       callee,
		IsMemberCall: isMember,
		Language:     "java",
		SourceFile:   x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line(node)),
	}
	if isMember {
		rc.MemberReceiver = base.OptString(receiverText)
		rc.ReceiverType = base.OptString(receiverType)
		rc.ReceiverBound = receiverBound
	}
	x.rawCalls = append(x.rawCalls, rc)
}

func (x *javaExtractor) recordNewCall(node *sitter.Node, callerNID string) {
	typeNode := node.ChildByFieldName("type")
	if typeNode == nil {
		return
	}
	typeName := x.simpleTypeName(typeNode)
	if typeName == "" || javaBuiltins.has(typeName) {
		return
	}
	x.rawCalls = append(x.rawCalls, &base.RawCall{
		CallerNID:      callerNID,
		Callee:         typeName,
		IsMemberCall:   false,
		Language:       "java",
		SourceFile:     x.strPath,
		SourceLocation: fmt.Sprintf("L%d", line(node)),
	})
}

// ── Type name helpers ─────────────────────────────────────────────────────────

// simpleTypeName returns the bare type name from a type node (strips generics, arrays, qualifiers).
func (x *javaExtractor) simpleTypeName(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind() {
	case "type_identifier":
		return x.text(node)
	case "generic_type":
		// generic_type: type_identifier type_arguments
		nameNode := node.ChildByFieldName("name")
		if nameNode == nil {
			nameNode = node.Child(0)
		}
		if nameNode != nil {
			return x.simpleTypeName(nameNode)
		}
	case "array_type":
		elem := node.ChildByFieldName("element")
		if elem != nil {
			return x.simpleTypeName(elem)
		}
	case "scoped_type_identifier":
		// com.example.Foo → take last identifier
		nameNode := node.ChildByFieldName("name")
		if nameNode != nil {
			return x.text(nameNode)
		}
	case "void_type", "integral_type", "floating_point_type", "boolean_type":
		return x.text(node)
	}
	// Fallback: look for a type_identifier child.
	for _, c := range namedChildren(node) {
		if c.Kind() == "type_identifier" {
			return x.text(c)
		}
	}
	return ""
}

// simpleReceiverName returns the bare name of a call receiver (variable, this, class name).
func (x *javaExtractor) simpleReceiverName(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind() {
	case "identifier":
		return x.text(node)
	case "this":
		return "this"
	case "field_access":
		// this.foo → "this", or obj.field → field
		obj := node.ChildByFieldName("object")
		field := node.ChildByFieldName("field")
		if obj != nil && x.simpleReceiverName(obj) == "this" && field != nil {
			return x.text(field)
		}
	}
	return ""
}

// resolveType maps a simple class name to a node id, using imports if available.
func (x *javaExtractor) resolveType(name string) string {
	if fqn, ok := x.importedClasses[name]; ok {
		return base.MakeID(fqn)
	}
	return base.MakeID(name)
}

// readDottedName reads a dotted identifier sequence from an import or package declaration.
func (x *javaExtractor) readDottedName(node *sitter.Node) string {
	var parts []string
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Kind() {
		case "scoped_identifier":
			scope := n.ChildByFieldName("scope")
			name := n.ChildByFieldName("name")
			if scope != nil {
				walk(scope)
			}
			if name != nil {
				parts = append(parts, x.text(name))
			}
		case "identifier":
			parts = append(parts, x.text(n))
		default:
			for _, c := range namedChildren(n) {
				walk(c)
			}
		}
	}
	for _, c := range namedChildren(node) {
		walk(c)
	}
	return strings.Join(parts, ".")
}
