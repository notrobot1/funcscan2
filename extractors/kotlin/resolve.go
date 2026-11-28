package kotlin

import (
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
)

// resolveImportTargets rewrites import edge targets from bare last-segment IDs
// to the actual node ID of the imported symbol, when it can be found
// unambiguously in the corpus.
//
// _import_kotlin stamps a RawCall with Callee="__import__", Receiver=simpleName,
// ImportPath=fqn, FieldOwner=alias. Here we rewrite the corresponding "imports"
// edge target to the matched node ID.
func resolveImportTargets(c *base.Corpus) {
	// Build package → {label → []nodeID} index.
	type pkgSymbols = map[string][]string // label → nodeIDs
	pkgIndex := map[string]pkgSymbols{}

	for _, n := range c.Nodes {
		if n.SourceFile == "" || n.Type == "namespace" {
			continue
		}
		label := strings.TrimSuffix(strings.TrimSuffix(n.Label, "()"), "()")
		if label == "" || strings.HasPrefix(label, ".") {
			continue
		}
		pkg, _ := n.Metadata["namespace"].(string)
		if pkg == "" {
			continue
		}
		if pkgIndex[pkg] == nil {
			pkgIndex[pkg] = pkgSymbols{}
		}
		pkgIndex[pkg][label] = append(pkgIndex[pkg][label], n.ID)
	}

	// Build a map of import metadata from RawCalls.
	type importMeta struct {
		fqn   string
		alias string
		file  string
	}
	var imports []importMeta
	var remainingCalls []*base.RawCall
	for _, rc := range c.RawCalls {
		if rc.Language == "kotlin" && rc.Callee == "__import__" {
			imports = append(imports, importMeta{
				fqn:   string(rc.ImportPath),
				alias: string(rc.FieldOwner),
				file:  rc.SourceFile,
			})
		} else {
			remainingCalls = append(remainingCalls, rc)
		}
	}
	c.RawCalls = remainingCalls

	// Build file→imports map.
	type fileImport struct {
		fqn       string
		localName string
	}
	fileImports := map[string][]fileImport{}
	for _, imp := range imports {
		local := imp.alias
		if local == "" {
			parts := strings.Split(imp.fqn, ".")
			local = parts[len(parts)-1]
		}
		fileImports[imp.file] = append(fileImports[imp.file], fileImport{imp.fqn, local})
	}

	// Rewrite import edges.
	for _, e := range c.Edges {
		if e.Relation != "imports" {
			continue
		}
		if !isKotlinFile(e.SourceFile) {
			continue
		}
		// Find the import metadata for this edge's source file.
		for _, imp := range fileImports[e.SourceFile] {
			parts := strings.Split(imp.fqn, ".")
			if len(parts) < 2 {
				continue
			}
			name := parts[len(parts)-1]
			pkg := strings.Join(parts[:len(parts)-1], ".")
			// Check if the edge target's label matches the imported name.
			tgtNode := nodeByID(c, e.Target)
			if tgtNode == nil || tgtNode.Label != name {
				continue
			}
			// Try to find a unique match in the package.
			syms := pkgIndex[pkg]
			if syms == nil {
				continue
			}
			candidates := syms[name]
			if len(candidates) == 1 {
				e.Target = candidates[0]
			}
			break
		}
	}
}

// resolveMemberCalls binds Kotlin `Receiver.method()` calls where the receiver
// is a known class/object declared in the corpus.
func resolveMemberCalls(c *base.Corpus) {
	type methodKey struct{ typeName, method string }
	methodIndex := map[methodKey][]string{}

	classLabel := map[string]string{}
	for _, n := range c.Nodes {
		if n.CallableClass && isJVMFile(c, n.SourceFile) {
			classLabel[n.ID] = n.Label
		}
	}

	ownerOf := map[string]string{}
	for _, e := range c.Edges {
		if e.Relation == "method" {
			ownerOf[e.Target] = e.Source
		}
	}
	for _, n := range c.Nodes {
		if !n.Callable || n.CallableClass {
			continue
		}
		ownerNID := ownerOf[n.ID]
		if ownerNID == "" {
			continue
		}
		ownerLbl := classLabel[ownerNID]
		if ownerLbl == "" {
			continue
		}
		k := methodKey{ownerLbl, strings.TrimSuffix(strings.TrimSuffix(n.Label, "()"), "()")}
		methodIndex[k] = append(methodIndex[k], n.ID)
	}

	classNIDByLabel := map[string][]string{}
	for nid, lbl := range classLabel {
		classNIDByLabel[lbl] = append(classNIDByLabel[lbl], nid)
	}

	parents := map[string][]string{}
	for _, e := range c.Edges {
		if e.Relation == "inherits" || e.Relation == "implements" {
			for _, parentNID := range classNIDByLabel[labelOf(c, e.Target)] {
				parents[e.Source] = append(parents[e.Source], parentNID)
			}
		}
	}

	var lookupMethod func(typeName, method string, depth int) []string
	lookupMethod = func(typeName, method string, depth int) []string {
		if depth > 8 {
			return nil
		}
		if hits := methodIndex[methodKey{typeName, method}]; len(hits) > 0 {
			return hits
		}
		for _, classNID := range classNIDByLabel[typeName] {
			for _, parentNID := range parents[classNID] {
				if result := lookupMethod(classLabel[parentNID], method, depth+1); len(result) > 0 {
					return result
				}
			}
		}
		return nil
	}

	existingEdges := map[[2]string]struct{}{}
	for _, e := range c.Edges {
		if e.Relation == "calls" {
			existingEdges[[2]string{e.Source, e.Target}] = struct{}{}
		}
	}

	var newEdges []*base.Edge
	for _, rc := range c.RawCalls {
		if rc.Language != "kotlin" || !rc.IsMemberCall {
			continue
		}
		receiverType := string(rc.ReceiverType)
		if receiverType == "" {
			continue
		}
		// Strip generic parameters.
		if idx := strings.IndexByte(receiverType, '<'); idx >= 0 {
			receiverType = receiverType[:idx]
		}
		// Check for kotlin_object_receiver marker.
		if strings.HasPrefix(string(rc.FieldOwner), "kotlin_object_receiver:") {
			receiverType = strings.TrimPrefix(string(rc.FieldOwner), "kotlin_object_receiver:")
		}

		callee := rc.Callee
		hits := lookupMethod(receiverType, callee, 0)
		if len(hits) == 0 {
			// Try imported alias.
			base.ParkUnresolvedMemberCall(nodeByID(c, rc.CallerNID), callee, receiverType, "kotlin", rc)
			continue
		}
		// Multiple hits → pick unique method node IDs (overloads share one node).
		seen := map[string]struct{}{}
		var unique []string
		for _, h := range hits {
			if _, ok := seen[h]; !ok {
				seen[h] = struct{}{}
				unique = append(unique, h)
			}
		}
		if len(unique) != 1 {
			continue
		}
		key := [2]string{rc.CallerNID, unique[0]}
		if _, ok := existingEdges[key]; ok {
			continue
		}
		existingEdges[key] = struct{}{}

		confidence := "INFERRED"
		score := 0.85
		if rc.ReceiverBound {
			confidence = "EXTRACTED"
			score = 1.0
		}
		newEdges = append(newEdges, &base.Edge{
			Source:          rc.CallerNID,
			Target:          unique[0],
			Relation:        "calls",
			Context:         "call",
			Confidence:      confidence,
			ConfidenceScore: score,
			SourceFile:      rc.SourceFile,
			SourceLocation:  rc.SourceLocation,
			Weight:          1.0,
		})
	}
	c.Edges = append(c.Edges, newEdges...)
}

// resolveInterfaceDispatch links Kotlin interface methods to implementations.
func resolveInterfaceDispatch(c *base.Corpus) {
	interfaceNIDs := map[string]struct{}{}
	for _, n := range c.Nodes {
		if n.Metadata != nil {
			if lang, _ := n.Metadata["language"].(string); lang == "kotlin" {
				if kind, _ := n.Metadata["kind"].(string); kind == "interface" {
					interfaceNIDs[n.ID] = struct{}{}
				}
			}
		}
	}

	var pairs []base.Implements
	for _, e := range c.Edges {
		if e.Relation == "inherits" || e.Relation == "implements" {
			if _, ok := interfaceNIDs[e.Target]; ok {
				pairs = append(pairs, base.Implements{Impl: e.Source, Interface: e.Target})
			}
		}
	}
	base.ResolveInterfaceDispatch(c, pairs)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func isKotlinFile(path string) bool {
	return strings.HasSuffix(path, ".kt") || strings.HasSuffix(path, ".kts")
}

func isJVMFile(c *base.Corpus, path string) bool {
	if path == "" || c.LanguageOf == nil {
		return false
	}
	lang := c.LanguageOf(path)
	return lang != nil && lang.FamilyName() == "jvm"
}

func labelOf(c *base.Corpus, nid string) string {
	for _, n := range c.Nodes {
		if n.ID == nid {
			return n.Label
		}
	}
	return ""
}

func nodeByID(c *base.Corpus, nid string) *base.Node {
	for _, n := range c.Nodes {
		if n.ID == nid {
			return n
		}
	}
	return nil
}
