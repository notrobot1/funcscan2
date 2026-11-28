package java

import (
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
)

// resolveMemberCalls binds `receiver.method()` raw calls to their definitions.
//
// Strategy:
//  1. Build an index: (typeName, methodName) -> []nodeID from all Java nodes.
//  2. Build a field-type index from GoFields on file nodes.
//  3. Build an inheritance chain from inherits/implements edges.
//  4. For each raw Java member call with a receiver type: walk the type chain
//     and emit a "calls" edge to the first unambiguous match.
func resolveMemberCalls(c *base.Corpus) {
	// Index methods by (simpleName, methodLabel) → []nodeID.
	type methodKey struct{ typeName, method string }
	methodIndex := map[methodKey][]string{}

	// Map nodeID → simple class label (for reverse lookup).
	classLabel := map[string]string{}
	for _, n := range c.Nodes {
		if n.CallableClass {
			classLabel[n.ID] = n.Label
			// Also index by label alone so a simple "Foo" matches node "stem_Foo".
		}
	}

	// Build method index: for each method node, find its owner class.
	ownerOf := map[string]string{} // methodNID → classNID
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
		k := methodKey{ownerLbl, n.Label}
		methodIndex[k] = append(methodIndex[k], n.ID)
	}

	// Build field-type lookup from file nodes' GoFields.
	// fieldTypes: classNID → (fieldName → typeName)
	fieldTypes := map[string]map[string]string{}
	for _, n := range c.Nodes {
		if n.GoFields != nil {
			for classNID, fields := range n.GoFields {
				if fieldTypes[classNID] == nil {
					fieldTypes[classNID] = map[string]string{}
				}
				for f, t := range fields {
					fieldTypes[classNID][f] = t
				}
			}
		}
	}

	// Build class label → classNID index.
	classNIDByLabel := map[string][]string{}
	for nid, lbl := range classLabel {
		classNIDByLabel[lbl] = append(classNIDByLabel[lbl], nid)
	}

	// Build inheritance chain: classNID → []parentNID
	parents := map[string][]string{}
	for _, e := range c.Edges {
		if e.Relation == "inherits" || e.Relation == "implements" {
			// Source is the child class. We need the target's classNID.
			for _, parentNID := range classNIDByLabel[labelOf(c, e.Target)] {
				parents[e.Source] = append(parents[e.Source], parentNID)
			}
		}
	}

	// lookupMethod walks the inheritance chain for (typeName, methodName).
	var lookupMethod func(typeName, method string, depth int) []string
	lookupMethod = func(typeName, method string, depth int) []string {
		if depth > 8 {
			return nil
		}
		hits := methodIndex[methodKey{typeName, method}]
		if len(hits) > 0 {
			return hits
		}
		// Walk to parent classes.
		for _, classNID := range classNIDByLabel[typeName] {
			for _, parentNID := range parents[classNID] {
				parentLabel := classLabel[parentNID]
				if parentLabel == "" {
					continue
				}
				if result := lookupMethod(parentLabel, method, depth+1); len(result) > 0 {
					return result
				}
			}
		}
		return nil
	}

	// Build a set of existing edges to avoid duplicates.
	existingEdges := map[[2]string]struct{}{}
	for _, e := range c.Edges {
		if e.Relation == "calls" {
			existingEdges[[2]string{e.Source, e.Target}] = struct{}{}
		}
	}

	var newEdges []*base.Edge
	for _, rc := range c.RawCalls {
		if rc.Language != "java" || !rc.IsMemberCall {
			continue
		}
		receiverType := string(rc.ReceiverType)
		if receiverType == "" {
			continue
		}
		// Strip generic parameters: List<String> → List
		if idx := strings.IndexByte(receiverType, '<'); idx >= 0 {
			receiverType = receiverType[:idx]
		}
		// Try bare type name first, then check if it matches a known class.
		hits := lookupMethod(receiverType, rc.Callee, 0)
		if len(hits) != 1 {
			// Try last segment in case it was stored as fully qualified.
			if parts := strings.Split(receiverType, "."); len(parts) > 1 {
				hits = lookupMethod(parts[len(parts)-1], rc.Callee, 0)
			}
		}
		if len(hits) != 1 {
			base.ParkUnresolvedMemberCall(nodeByID(c, rc.CallerNID), rc.Callee, receiverType, "java", rc)
			continue
		}
		key := [2]string{rc.CallerNID, hits[0]}
		if _, ok := existingEdges[key]; ok {
			continue
		}
		existingEdges[key] = struct{}{}
		newEdges = append(newEdges, &base.Edge{
			Source:         rc.CallerNID,
			Target:         hits[0],
			Relation:       "calls",
			Context:        "call",
			Confidence:     "INFERRED",
			ConfidenceScore: 0.8,
			SourceFile:     rc.SourceFile,
			SourceLocation: rc.SourceLocation,
			Weight:         1.0,
		})
	}
	c.Edges = append(c.Edges, newEdges...)
}

// resolveInterfaceDispatch links interface methods to their implementations
// using the shared dispatch helper.
func resolveInterfaceDispatch(c *base.Corpus) {
	// Find interface nodes.
	interfaceNIDs := map[string]struct{}{}
	for _, n := range c.Nodes {
		if n.Metadata != nil {
			if lang, _ := n.Metadata["language"].(string); lang == "java" {
				if kind, _ := n.Metadata["kind"].(string); kind == "interface" {
					interfaceNIDs[n.ID] = struct{}{}
				}
			}
		}
	}

	// Find implements edges: class → interface.
	var pairs []base.Implements
	for _, e := range c.Edges {
		if e.Relation == "implements" {
			if _, ok := interfaceNIDs[e.Target]; ok {
				pairs = append(pairs, base.Implements{Impl: e.Source, Interface: e.Target})
			}
		}
	}
	base.ResolveInterfaceDispatch(c, pairs)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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
