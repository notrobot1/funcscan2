package extractors

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
)

const sep = string(filepath.Separator)

// resolve makes a path absolute and follows its symlinks. Unlike
// filepath.EvalSymlinks it also answers for a path that does not exist: the
// part that exists is resolved and the rest is appended as written. Results
// are remembered for the run, since the same few hundred files are asked about
// once per node and edge.
func (p *pipeline) resolve(path string) string {
	if hit, ok := p.resolved[path]; ok {
		return hit
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	resolved := abs
	rest := ""
	for dir := abs; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			resolved = filepath.Join(real, rest)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
	p.resolved[path] = resolved
	return resolved
}

// relativeTo returns path relative to root when root is one of its parents,
// lexically.
func relativeTo(path, root string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	if path == root {
		return ".", true
	}
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	if strings.HasPrefix(path, prefix) {
		return path[len(prefix):], true
	}
	return "", false
}

// rootRelative is the path relative to the scan root, as written if that
// works and through its resolved form otherwise.
func (p *pipeline) rootRelative(path string) (string, bool) {
	if rel, ok := relativeTo(path, p.corpus.Root); ok {
		return rel, true
	}
	return relativeTo(p.resolve(path), p.corpus.Root)
}

// sourceKey identifies a source file within the corpus: its path relative to
// the scan root, or the path as written when it lies outside.
func (p *pipeline) sourceKey(sourceFile string) string {
	if sourceFile == "" {
		return ""
	}
	if rel, ok := relativeTo(p.resolve(sourceFile), p.corpus.Root); ok {
		return rel
	}
	return sourceFile
}

// fileNodeID is the canonical id of a file: its root-relative path without
// the extension.
func fileNodeID(rel string) string {
	return base.MakeID(base.FileStem(rel))
}

// commonRoot is the deepest directory that holds every path, as written.
func commonRoot(paths []string) string {
	switch len(paths) {
	case 0:
		return "."
	case 1:
		return filepath.Dir(paths[0])
	}
	split := func(path string) []string {
		parts := strings.Split(filepath.ToSlash(path), "/")
		if filepath.IsAbs(path) {
			parts[0] = "/" // the anchor counts as a component of its own
			if vol := filepath.VolumeName(path); vol != "" {
				parts[0] = vol + "/"
			}
		}
		return parts
	}
	common := split(paths[0])
	for _, path := range paths[1:] {
		parts := split(path)
		n := 0
		for n < len(common) && n < len(parts) && common[n] == parts[n] {
			n++
		}
		common = common[:n]
	}
	if len(common) == 0 {
		return "."
	}
	return filepath.FromSlash(common[0] + strings.Join(common[1:], "/"))
}

// portableOutOfRoot names a file outside the scan root without leaking where
// the checkout lives: a short walk-up path, or just the base name when the
// file is far away (its parent directories would be someone's home).
func (p *pipeline) portableOutOfRoot(path string) string {
	rel, err := filepath.Rel(p.corpus.Root, path)
	if err != nil {
		return filepath.Base(path)
	}
	rel = filepath.ToSlash(rel)
	ups := 0
	for _, segment := range strings.Split(rel, "/") {
		if segment != ".." {
			break
		}
		ups++
	}
	if ups > 3 {
		return filepath.Base(path)
	}
	return rel
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
