package csharp

import (
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
)

// resolveMemberCalls binds `receiver.Method()` raw calls to their definitions.
func resolveMemberCalls(c *base.Corpus) {
	type methodKey struct{ typeName, method string }
	methodIndex := map[methodKey][]string{}

	classLabel := map[string]string{}
	for _, n := range c.Nodes {
		if n.CallableClass {
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
		k := methodKey{ownerLbl, n.Label}
		methodIndex[k] = append(methodIndex[k], n.ID)
	}

	// Field-type tables from file nodes.
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

	classNIDByLabel := map[string][]string{}
	for nid, lbl := range classLabel {
		classNIDByLabel[lbl] = append(classNIDByLabel[lbl], nid)
	}

	// Inheritance chain.
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
		hits := methodIndex[methodKey{typeName, method}]
		if len(hits) > 0 {
			return hits
		}
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

	existingEdges := map[[2]string]struct{}{}
	for _, e := range c.Edges {
		if e.Relation == "calls" {
			existingEdges[[2]string{e.Source, e.Target}] = struct{}{}
		}
	}

	var newEdges []*base.Edge
	for _, rc := range c.RawCalls {
		if rc.Language != "csharp" || !rc.IsMemberCall {
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
		// Strip nullable.
		receiverType = strings.TrimSuffix(receiverType, "?")

		hits := lookupMethod(receiverType, rc.Callee, 0)
		if len(hits) != 1 {
			base.ParkUnresolvedMemberCall(nodeByID(c, rc.CallerNID), rc.Callee, receiverType, "csharp", rc)
			continue
		}
		key := [2]string{rc.CallerNID, hits[0]}
		if _, ok := existingEdges[key]; ok {
			continue
		}
		existingEdges[key] = struct{}{}

		confidence := "INFERRED"
		score := 0.75
		if rc.ReceiverBound {
			confidence = "EXTRACTED"
			score = 1.0
		}
		newEdges = append(newEdges, &base.Edge{
			Source:          rc.CallerNID,
			Target:          hits[0],
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

// resolveInterfaceDispatch links interface methods to their C# implementations.
func resolveInterfaceDispatch(c *base.Corpus) {
	interfaceNIDs := map[string]struct{}{}
	for _, n := range c.Nodes {
		if n.Metadata != nil {
			if lang, _ := n.Metadata["language"].(string); lang == "csharp" {
				if kind, _ := n.Metadata["kind"].(string); kind == "interface" {
					interfaceNIDs[n.ID] = struct{}{}
				}
			}
		}
	}

	var pairs []base.Implements
	for _, e := range c.Edges {
		if e.Relation == "inherits" {
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
