package extractors

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/notrobot1/funcscan2/extractors/base"
)

// remapIDs rewrites ids that were minted from the path a file was read at into
// their canonical, root-relative form, so the same repository yields the same
// graph wherever it is checked out and however its paths were spelled.
//
// Two shapes are rewritten. A file node's id is the whole path
// (`/home/me/repo/svc/server.go` -> `svc_server`). A symbol's id starts with
// its file's path without the extension (`…_repo_svc_server_run` ->
// `svc_server_run`); it is rewritten only on nodes of that very file, so two
// files that share a prefix cannot contaminate each other.
func (p *pipeline) remapIDs() {
	c := p.corpus
	idRemap := map[string]string{}
	// The files that minted each old file-level id. A symbol of ANOTHER file
	// that merely spells the same string must not follow the rename.
	idOwner := map[string]map[string]bool{}
	type prefixPair struct{ old, new string }
	prefixRemap := map[string][]prefixPair{} // resolved file -> its symbol prefixes
	stemForms := map[string][]string{}       // resolved file -> every prefix its symbol ids may carry

	// Files named only as the target of an import edge are canonicalised too:
	// the edge's target id was minted from their path.
	remapPaths := append([]string(nil), c.Paths...)
	seen := map[string]bool{}
	for _, path := range c.Paths {
		seen[p.resolve(path)] = true
	}
	for _, e := range c.Edges {
		if e.TargetFile == "" {
			continue
		}
		target := p.resolve(e.TargetFile)
		if seen[target] {
			continue
		}
		seen[target] = true
		if _, inRoot := relativeTo(target, c.Root); !inRoot || !isFile(target) {
			// Outside the root, or not there at all: the edge keeps its raw
			// target and stays dangling rather than pointing at an invented
			// node.
			continue
		}
		remapPaths = append(remapPaths, target)
		if e.TargetFile != target {
			remapPaths = append(remapPaths, e.TargetFile)
		}
	}

	own := func(id, file string) {
		if idOwner[id] == nil {
			idOwner[id] = map[string]bool{}
		}
		idOwner[id][file] = true
	}
	for _, path := range remapPaths {
		rel, ok := p.rootRelative(path)
		if !ok {
			continue
		}
		newID := fileNodeID(rel)
		abs := p.resolve(path)
		// Both spellings of the path are registered: ids are minted from the
		// path as written, but anything resolved on the way (an import
		// target, a symlinked root) carries the resolved form.
		for _, form := range []string{path, abs} {
			if oldID := base.MakeID(form); oldID != newID {
				idRemap[oldID] = newID
				own(oldID, abs)
			}
		}
		oldPrefix, oldPrefixAbs := fileNodeID(path), fileNodeID(abs)
		var prefixes []prefixPair
		if oldPrefix != newID {
			prefixes = append(prefixes, prefixPair{oldPrefix, newID})
		}
		if oldPrefixAbs != newID && oldPrefixAbs != oldPrefix {
			prefixes = append(prefixes, prefixPair{oldPrefixAbs, newID})
		}
		if len(prefixes) > 0 {
			prefixRemap[abs] = prefixes
		}
		stemForms[abs] = []string{oldPrefixAbs, oldPrefix, newID}
	}

	// A key is contested when a node of a different file carries it as one of
	// that file's OWN symbol ids. Such a node keeps its id; everything that
	// refers to the key's owner still follows the rename.
	contested := map[string]bool{}
	held := map[string]map[string]bool{}
	for _, n := range c.Nodes {
		if _, ok := idRemap[n.ID]; !ok || n.SourceFile == "" {
			continue
		}
		file := p.resolve(n.SourceFile)
		if idOwner[n.ID][file] {
			continue
		}
		for _, form := range stemForms[file] {
			if strings.HasPrefix(n.ID, form+"_") {
				contested[n.ID] = true
				if held[n.ID] == nil {
					held[n.ID] = map[string]bool{}
				}
				held[n.ID][file] = true
				break
			}
		}
	}
	remapped := func(id, ownerFile string) (string, bool) {
		newID, ok := idRemap[id]
		if !ok || !contested[id] || ownerFile == "" {
			return newID, ok
		}
		file := p.resolve(ownerFile)
		if !idOwner[id][file] && held[id][file] {
			return "", false
		}
		return newID, true
	}

	if len(idRemap) > 0 {
		for _, n := range c.Nodes {
			if newID, ok := remapped(n.ID, n.SourceFile); ok {
				n.ID = newID
			}
		}
		for _, e := range c.Edges {
			if newID, ok := remapped(e.Source, e.SourceFile); ok {
				e.Source = newID
			}
			targetOwner := e.TargetFile
			if targetOwner == "" {
				targetOwner = e.SourceFile
			}
			if newID, ok := remapped(e.Target, targetOwner); ok {
				e.Target = newID
			}
		}
		// A call made at the top level of a file names the file itself as its
		// caller.
		for _, rc := range c.RawCalls {
			if newID, ok := remapped(rc.CallerNID, rc.SourceFile); ok {
				rc.CallerNID = newID
			}
		}
	}

	if len(prefixRemap) == 0 {
		return
	}
	symbolRemap := map[string]string{}
	for _, n := range c.Nodes {
		// A package node's id is its name, shared by every manifest that
		// mentions the package; it is not a symbol of this file.
		if n.SourceFile == "" || n.Type == "package" {
			continue
		}
		for _, prefix := range prefixRemap[p.resolve(n.SourceFile)] {
			if strings.HasPrefix(n.ID, prefix.old+"_") {
				if canonical := prefix.new + n.ID[len(prefix.old):]; canonical != n.ID {
					symbolRemap[n.ID] = canonical
				}
				break
			}
			if strings.HasPrefix(n.ID, prefix.new+"_") {
				break
			}
		}
	}
	if len(symbolRemap) == 0 {
		return
	}
	rename := func(id *string) {
		if newID, ok := symbolRemap[*id]; ok {
			*id = newID
		}
	}
	for _, n := range c.Nodes {
		rename(&n.ID)
	}
	for _, e := range c.Edges {
		rename(&e.Source)
		rename(&e.Target)
	}
	for _, rc := range c.RawCalls {
		rename(&rc.CallerNID)
	}
}

// disambiguateCollidingIDs separates distinct declarations that ended up with
// the same id — two files that each declare a `helper()`, say, in directories
// whose names normalise alike — by salting the id with the path of its file.
// Ids that do not collide are left exactly as they are.
//
// Module and namespace nodes are exempt: three files importing one module mean
// the SAME module, and must stay one node.
func (p *pipeline) disambiguateCollidingIDs() {
	c := p.corpus
	nodeKey := func(n *base.Node) string {
		if n.SourceFile != "" {
			return p.sourceKey(n.SourceFile)
		}
		return p.sourceKey(n.OriginFile)
	}

	byID := map[string][]*base.Node{}
	var order []string
	// Files whose copy of a node an earlier merge folded into it. Their edges
	// still name the shared id under their OWN path, so they must follow the
	// node they were folded into.
	folded := map[*base.Node][]string{}
	for _, n := range c.Nodes {
		if len(n.GoFoldedFiles) > 0 {
			folded[n] = n.GoFoldedFiles
			n.GoFoldedFiles = nil
		}
		if n.Type == "module" || n.Type == "namespace" || n.ID == "" {
			continue
		}
		if _, ok := byID[n.ID]; !ok {
			order = append(order, n.ID)
		}
		byID[n.ID] = append(byID[n.ID], n)
	}

	type scoped struct{ id, file string }
	remap := map[scoped]string{}
	ambiguous := map[string]bool{}
	for _, oldID := range order {
		group := byID[oldID]
		keys := map[string]bool{}
		for _, n := range group {
			keys[nodeKey(n)] = true
		}
		if len(group) < 2 || len(keys) < 2 {
			continue
		}
		ambiguous[oldID] = true
		// The salt is the file's path. The id normalisation collapses every
		// separator, though, so two different paths (`a/b_c.go`, `a_b/c.go`)
		// can still produce the same salted id; those get a short hash of the
		// raw path on top, which does tell them apart.
		naive := map[string]string{}
		count := map[string]int{}
		for key := range keys {
			if key != "" {
				naive[key] = base.MakeID(key, oldID)
				count[naive[key]]++
			}
		}
		for _, n := range group {
			key := nodeKey(n)
			if key == "" {
				continue
			}
			newID := naive[key]
			if count[newID] > 1 {
				sum := sha1.Sum([]byte(key))
				newID = base.MakeID(key, oldID, hex.EncodeToString(sum[:])[:6])
			}
			remap[scoped{oldID, key}] = newID
			n.ID = newID
		}
		for _, n := range group {
			for _, file := range folded[n] {
				key := p.sourceKey(file)
				if key == "" || keys[key] {
					continue
				}
				if _, taken := remap[scoped{oldID, key}]; !taken {
					remap[scoped{oldID, key}] = n.ID
				}
			}
		}
	}

	if len(remap) > 0 {
		follow := func(id *string, file string) {
			if newID, ok := remap[scoped{*id, file}]; ok {
				*id = newID
			}
		}
		for _, e := range c.Edges {
			edgeFile := p.sourceKey(e.SourceFile)
			follow(&e.Source, edgeFile)
			// An import edge's target is a node of the IMPORTED file. Keying
			// its salt by the importer would point it back at the importer's
			// own variant, so the resolved target file decides when known.
			targetFile := edgeFile
			if e.TargetFile != "" && (e.Relation == "imports" || e.Relation == "imports_from" || e.Relation == "re_exports") {
				targetFile = p.sourceKey(e.TargetFile)
			}
			follow(&e.Target, targetFile)
		}
		for _, rc := range c.RawCalls {
			follow(&rc.CallerNID, p.sourceKey(rc.SourceFile))
		}
	}
	// The target-file hint has done its job; its absolute path goes no further.
	for _, e := range c.Edges {
		e.TargetFile = ""
	}
}

// finalize makes the graph portable and stamps its provenance: source files
// become relative to the scan root, whatever id still embeds an absolute path
// is rewritten, and the fields that only served the pipeline are cleared.
func (p *pipeline) finalize() {
	c := p.corpus
	owned := map[string]bool{}
	for _, n := range c.Nodes {
		owned[n.ID] = true
	}

	type entry struct {
		sourceFile, canonicalID string
		keys                    []string
	}
	entries := map[string]*entry{}
	entryFor := func(sourceFile string) *entry {
		if hit, ok := entries[sourceFile]; ok {
			return hit
		}
		// Only the directory is resolved: resolving the file itself would
		// follow a file symlink onto its target and merge two sources.
		rel, inRoot := relativeTo(sourceFile, c.Root)
		if !inRoot {
			rel, inRoot = relativeTo(filepath.Join(p.resolve(filepath.Dir(sourceFile)), filepath.Base(sourceFile)), c.Root)
		}
		e := &entry{}
		if inRoot {
			e.sourceFile, e.canonicalID = filepath.ToSlash(rel), fileNodeID(rel)
		} else {
			e.sourceFile = p.portableOutOfRoot(sourceFile)
			e.canonicalID = base.MakeID("ext", e.sourceFile)
		}
		resolved := p.resolve(sourceFile)
		// Every id form the absolute path can have left behind: the whole
		// path (a file id) and the path without its extension (the prefix of
		// a symbol id), as written and resolved.
		for _, key := range []string{
			base.MakeID(sourceFile), base.MakeID(resolved),
			base.MakeID(base.FileStem(sourceFile)), base.MakeID(base.FileStem(resolved)),
		} {
			e.keys = append(e.keys, key)
		}
		entries[sourceFile] = e
		return e
	}

	// Backstop for ids no earlier pass reached. Only ids derived from an
	// absolute path are renamed, and a key a real node owns under some other
	// derivation is never touched, so no edge is pointed at a node it did not
	// already reference.
	leftover := map[string]string{}
	for _, n := range c.Nodes {
		if filepath.IsAbs(n.SourceFile) {
			e := entryFor(n.SourceFile)
			for _, key := range e.keys {
				if _, known := leftover[key]; key == e.canonicalID || known || (owned[key] && n.ID != key) {
					continue
				}
				leftover[key] = e.canonicalID
			}
			n.SourceFile = e.sourceFile
		}
		// The call-site file of a parked call is not the node's own file, so
		// it is made relative separately.
		if parked, ok := n.Metadata[base.UnresolvedCallsKey].([]map[string]any); ok {
			for _, call := range parked {
				if file, _ := call["file"].(string); filepath.IsAbs(file) {
					call["file"] = entryFor(file).sourceFile
				}
			}
		}
	}
	for _, e := range c.Edges {
		if filepath.IsAbs(e.SourceFile) {
			e.SourceFile = entryFor(e.SourceFile).sourceFile
		}
	}
	if len(leftover) > 0 {
		canonical := func(id string) string {
			if newID, ok := leftover[id]; ok {
				return newID
			}
			if owned[id] {
				return id
			}
			// An endpoint no node stands for, minted as <absolute file
			// prefix>_<symbol>: rewrite the longest known prefix. It stays
			// dangling, but no longer names the machine it was built on.
			for cut := strings.LastIndex(id, "_"); cut > 0; cut = strings.LastIndex(id[:cut], "_") {
				if prefix, ok := leftover[id[:cut]]; ok {
					return prefix + id[cut:]
				}
			}
			return id
		}
		for _, n := range c.Nodes {
			n.ID = canonical(n.ID)
		}
		for _, e := range c.Edges {
			e.Source, e.Target = canonical(e.Source), canonical(e.Target)
		}
	}

	for _, n := range c.Nodes {
		n.OriginFile = ""
		n.Origin = "ast"
		n.SourceFile = filepath.ToSlash(n.SourceFile)
	}
	for _, e := range c.Edges {
		e.Origin = "ast"
		e.SourceFile = filepath.ToSlash(e.SourceFile)
	}
}
