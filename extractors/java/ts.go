package java

import sitter "github.com/tree-sitter/go-tree-sitter"

type stringSet map[string]struct{}

func newSet(items ...string) stringSet {
	s := make(stringSet, len(items))
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}

func (s stringSet) has(v string) bool { _, ok := s[v]; return ok }

func readText(n *sitter.Node, source []byte) string {
	if n == nil {
		return ""
	}
	return string(source[n.StartByte():n.EndByte()])
}

func children(n *sitter.Node) []*sitter.Node {
	count := n.ChildCount()
	out := make([]*sitter.Node, 0, count)
	for i := uint(0); i < count; i++ {
		if c := n.Child(i); c != nil {
			out = append(out, c)
		}
	}
	return out
}

func namedChildren(n *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	for _, c := range children(n) {
		if c.IsNamed() {
			out = append(out, c)
		}
	}
	return out
}

func line(n *sitter.Node) int {
	return int(n.StartPosition().Row) + 1
}
