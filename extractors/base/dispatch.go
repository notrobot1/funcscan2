package base

import (
	"path/filepath"
	"sort"
	"strings"
)

// DispatchRelation links an interface method to a method that implements it.
const DispatchRelation = "dispatches_to"

// dispatchConfidence is the INFERRED confidence of a dispatch edge given how
// many implementers the interface has. One implementer is forced; three or
// more floor at a weak inference instead of falling per extra implementer.
func dispatchConfidence(implementers int) float64 {
	switch implementers {
	case 1:
		return 0.85
	case 2:
		return 0.75
	}
	return 0.65
}

// DeclaredIn reports whether the node is a declaration living in a file with
// one of the suffixes. A sourceless stub has no members worth dispatching to,
// and a pair across languages is a name collision, not an implementation.
func DeclaredIn(n *Node, suffixes ...string) bool {
	if n == nil || n.SourceFile == "" {
		return false
	}
	ext := filepath.Ext(n.SourceFile)
	for _, s := range suffixes {
		if ext == s {
			return true
		}
	}
	return false
}

// MethodLabel is a method node's bare name: `.Close()` -> `Close`. Case is
// kept.
func MethodLabel(n *Node) string {
	label := strings.TrimPrefix(strings.TrimSpace(n.Label), ".")
	name, _, _ := strings.Cut(label, "(")
	return name
}

// Implements states that the type node Impl implements the interface node
// Interface.
type Implements struct{ Impl, Interface string }

// ResolveInterfaceDispatch links each interface method to every implementing
// method that owns it unambiguously.
//
// A call through an injected dependency lands on the interface's method node,
// because that is what the call site names. The implementing method is a
// separate node, and without this link every chain through an interface is
// cut at that point. With N implementers the answer to "what can this call
// reach" is all N, so each gets an edge, at a confidence that falls as N
// grows.
//
// The pass is purely additive. pairs come from the language: read off
// declarations where the language has them, inferred from method sets where it
// does not (Go). Both ends of a pair must be declared in a file with one of
// the suffixes.
func ResolveInterfaceDispatch(c *Corpus, pairs []Implements, suffixes ...string) {
	nodeByID := make(map[string]*Node, len(c.Nodes))
	for _, n := range c.Nodes {
		nodeByID[n.ID] = n
	}
	implementers := map[string][]string{}
	seenPair := map[Implements]bool{}
	for _, p := range pairs {
		if !seenPair[p] {
			seenPair[p] = true
			implementers[p.Interface] = append(implementers[p.Interface], p.Impl)
		}
	}
	// owner -> method name -> method ids. A set, so one method arriving on two
	// `method` edges cannot look like two same-named methods.
	methodsOf := map[string]map[string]map[string]bool{}
	existing := map[[2]string]bool{}
	for _, e := range c.Edges {
		switch e.Relation {
		case DispatchRelation:
			existing[[2]string{e.Source, e.Target}] = true
		case "method":
			m := nodeByID[e.Target]
			if !DeclaredIn(m, suffixes...) || m.Metadata["kind"] == "constructor" {
				continue
			}
			name := MethodLabel(m)
			if name == "" {
				continue
			}
			if methodsOf[e.Source] == nil {
				methodsOf[e.Source] = map[string]map[string]bool{}
			}
			if methodsOf[e.Source][name] == nil {
				methodsOf[e.Source][name] = map[string]bool{}
			}
			methodsOf[e.Source][name][e.Target] = true
		}
	}
	if len(implementers) == 0 || len(methodsOf) == 0 {
		return
	}

	for _, interfaceNID := range sortedKeys(implementers) {
		interfaceNode := nodeByID[interfaceNID]
		interfaceMethods := methodsOf[interfaceNID]
		if !DeclaredIn(interfaceNode, suffixes...) || len(interfaceMethods) == 0 {
			continue
		}
		// Gate first, then count: a stub or a cross-language collision never
		// gets an edge, so it must not dilute the confidence of those that do.
		var impls []string
		for _, impl := range implementers[interfaceNID] {
			if DeclaredIn(nodeByID[impl], suffixes...) {
				impls = append(impls, impl)
			}
		}
		sort.Strings(impls)
		score := dispatchConfidence(len(impls))
		for _, impl := range impls {
			implNode := nodeByID[impl]
			for _, name := range sortedKeys(interfaceMethods) {
				declared, candidates := interfaceMethods[name], methodsOf[impl][name]
				// One of each pairs up. An implementer declaring several
				// methods of one name is ambiguous on its own and is skipped.
				if len(declared) != 1 || len(candidates) != 1 {
					continue
				}
				source, target := sortedKeys(declared)[0], sortedKeys(candidates)[0]
				pair := [2]string{source, target}
				if source == target || existing[pair] {
					continue
				}
				existing[pair] = true
				c.Edges = append(c.Edges, &Edge{
					Source:          source,
					Target:          target,
					Relation:        DispatchRelation,
					Context:         "call",
					Confidence:      "INFERRED",
					ConfidenceScore: score,
					SourceFile:      implNode.SourceFile,
					SourceLocation:  implNode.SourceLocation,
					Weight:          1.0,
				})
			}
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
