package golang

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type stringSet map[string]struct{}

func newSet(items ...string) stringSet {
	s := make(stringSet, len(items))
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}

func (s stringSet) has(v string) bool {
	_, ok := s[v]
	return ok
}

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

var goModuleLine = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

// goImportPathForFile returns the canonical Go import path of the package a
// source file belongs to: the `module` path of the nearest go.mod plus the
// directory's path below it. Empty when the file is not inside a module.
func goImportPathForFile(absPath string) string {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}
	pkgDir := filepath.Dir(absPath)
	for dir := pkgDir; ; dir = filepath.Dir(dir) {
		goMod := filepath.Join(dir, "go.mod")
		if info, err := os.Stat(goMod); err == nil && info.Mode().IsRegular() {
			// The nearest go.mod settles the module, even when it is unreadable.
			data, _ := os.ReadFile(goMod)
			match := goModuleLine.FindSubmatch(data)
			if match == nil {
				return ""
			}
			module := string(match[1])
			rel, err := filepath.Rel(dir, pkgDir)
			if err != nil {
				return ""
			}
			if rel == "." {
				return module
			}
			return module + "/" + filepath.ToSlash(rel)
		}
		if dir == filepath.Dir(dir) {
			return ""
		}
	}
}

// nameSalt is the six-hex-digit salt of a name in a case-only id collision.
func nameSalt(name string) string {
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:])[:6]
}
