package base

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// NormalizeID folds one id string to its canonical form: caseless, every run
// of non-word characters replaced by a single underscore, no underscores at the
// edges. Letters of any script survive, so CJK or Cyrillic names keep distinct
// ids.
//
// Case folding and NFKC do not commute (folding can expand a character into a
// base letter plus a combining mark, which NFKC may then recompose), so the two
// are iterated to a fixpoint before the non-word filter runs. That keeps the
// result idempotent and the same whether or not the caller already folded it.
func NormalizeID(s string) string {
	cur := s
	if isASCII(s) {
		cur = strings.ToLower(s)
	} else {
		fold := cases.Fold()
		for i := 0; i < 6; i++ {
			next := norm.NFKC.String(fold.String(cur))
			if next == cur {
				break
			}
			cur = next
		}
	}
	var b strings.Builder
	pendingSep := false
	for _, r := range cur {
		if r != '_' && (unicode.IsLetter(r) || unicode.IsNumber(r)) {
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		} else {
			pendingSep = true
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// MakeID builds a canonical node id from one or more name parts: the parts are
// joined with `_` (stray `_`/`.` edges stripped from each) and normalised.
func MakeID(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, strings.Trim(p, "_."))
		}
	}
	return NormalizeID(strings.Join(kept, "_"))
}

// NormalizePath tidies a path the way the ids expect it: `.` segments and
// doubled separators are dropped, `..` is kept as written (it is not resolved
// against the file system or collapsed lexically).
func NormalizePath(path string) string {
	slashed := filepath.ToSlash(path)
	var parts []string
	for _, part := range strings.Split(slashed, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	joined := strings.Join(parts, "/")
	if strings.HasPrefix(slashed, "/") {
		joined = "/" + joined
	}
	if joined == "" {
		joined = "."
	}
	return filepath.FromSlash(joined)
}

// FileStem is the node-id prefix for a file and its symbols: the whole
// (normalised) path, extension dropped, with forward slashes. Using every
// segment means same-named files in different directories get distinct ids. An
// absolute path is encoded whole; pass repo-relative paths to keep the on-disk
// location out of the ids.
func FileStem(path string) string {
	slashed := filepath.ToSlash(path)
	name := slashed[strings.LastIndex(slashed, "/")+1:]
	if name == "" || name == "." {
		return ""
	}
	// A leading dot (`.gitignore`) or a trailing one is not an extension.
	if dot := strings.LastIndex(name, "."); dot > 0 && dot < len(name)-1 {
		return slashed[:len(slashed)-(len(name)-dot)]
	}
	return slashed
}

// ParentDirName is the name of the file's directory as written in the path,
// empty when the path names none.
func ParentDirName(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

// DeclSalt is the stable 12-hex digest appended when a declaration id collides.
func DeclSalt(name string) string {
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:])[:12]
}

type declKey struct{ kind, nid string }

type respellKey struct{ kind, nid, name string }

// DeclIDRegistry assigns unique ids to the declarations of one file.
//
// Ownership is keyed by the id the declaration would have used plus its kind.
// The first declaration to claim an id keeps it; a later one whose name
// normalises alike is salted apart when it is a different kind spelled
// differently (`type Foo` / `func foo`) or the same kind spelled differently
// only by leading/trailing underscores (`_save` / `save`). A same-kind
// respelling that differs otherwise (`Foo` / `FOO`) and an identical spelling
// reuse the id.
type DeclIDRegistry struct {
	assigned  map[declKey]string
	owners    map[string]declKey
	salted    map[string]bool
	spelled   map[string]string
	respelled map[respellKey]string
}

// NewDeclIDRegistry returns a registry in which reservedIDs (the file's own
// node id) can never be claimed by a declaration.
func NewDeclIDRegistry(reservedIDs ...string) *DeclIDRegistry {
	r := &DeclIDRegistry{
		assigned:  map[declKey]string{},
		owners:    map[string]declKey{},
		salted:    map[string]bool{},
		spelled:   map[string]string{},
		respelled: map[respellKey]string{},
	}
	for _, rid := range reservedIDs {
		r.owners[rid] = declKey{"file", rid}
	}
	return r
}

// Resolve returns the final id for the declaration `name` of the given kind
// that would naturally use nid. taken holds the ids already emitted as nodes.
func (r *DeclIDRegistry) Resolve(nid, name, kind string, taken map[string]struct{}) string {
	key := declKey{kind, nid}
	owner, owned := r.owners[nid]
	_, isTaken := taken[nid]
	if owned && owner == key {
		first, ok := r.spelled[nid]
		if !ok {
			first = name
		}
		if name == first || strings.Trim(name, "_") != strings.Trim(first, "_") {
			return r.assigned[key]
		}
		// Same kind, spelled apart only by underscores (`_save` then `save`):
		// two declarations, not a re-declaration. Salted from the spelling like
		// any other collision, memoised per spelling.
		rkey := respellKey{kind, nid, name}
		if id, ok := r.respelled[rkey]; ok {
			return id
		}
		candidate := r.salt(nid, name, taken)
		r.owners[candidate] = declKey{kind, candidate}
		r.assigned[declKey{kind, candidate}] = candidate
		r.respelled[rkey] = candidate
		return candidate
	}
	if !isTaken && !owned {
		r.owners[nid] = key
		r.assigned[key] = nid
		r.spelled[nid] = name
		return nid
	}
	if id, ok := r.assigned[key]; ok {
		return id
	}
	// An id already used by a real declaration is a duplicate reference to that
	// declaration; reuse it instead of minting a second node. Only a salted id,
	// whose owner stole the spelling from this declaration, forces a
	// counter-salt. A different kind only counts as a duplicate when spelled
	// the same: `type Foo` + `func foo` are two.
	if isTaken && owned && owner.kind != "file" && !r.salted[nid] &&
		(owner.kind == kind || r.spelled[nid] == name) {
		return nid
	}
	candidate := r.salt(nid, name, taken)
	r.owners[candidate] = key
	r.assigned[key] = candidate
	return candidate
}

// salt mints and records a salted id for name; the caller records ownership.
func (r *DeclIDRegistry) salt(nid, name string, taken map[string]struct{}) string {
	base := MakeID(nid, DeclSalt(name))
	candidate := base
	for counter := 2; ; counter++ {
		_, isTaken := taken[candidate]
		_, owned := r.owners[candidate]
		if !isTaken && !owned {
			break
		}
		candidate = MakeID(base, strconv.Itoa(counter))
	}
	r.salted[candidate] = true
	r.spelled[candidate] = name
	return candidate
}
