package extractors

import (
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"strings"
)

// declSalt is the stable 12-hex digest appended when a declaration id collides.
func declSalt(name string) string {
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
	salted    stringSet
	spelled   map[string]string
	respelled map[respellKey]string
}

// NewDeclIDRegistry returns a registry in which reservedIDs (the file's own
// node id) can never be claimed by a declaration.
func NewDeclIDRegistry(reservedIDs ...string) *DeclIDRegistry {
	r := &DeclIDRegistry{
		assigned:  map[declKey]string{},
		owners:    map[string]declKey{},
		salted:    stringSet{},
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
	if isTaken && owned && owner.kind != "file" && !r.salted.has(nid) &&
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
	base := makeID(nid, declSalt(name))
	candidate := base
	for counter := 2; ; counter++ {
		_, isTaken := taken[candidate]
		_, owned := r.owners[candidate]
		if !isTaken && !owned {
			break
		}
		candidate = makeID(base, strconv.Itoa(counter))
	}
	r.salted[candidate] = struct{}{}
	r.spelled[candidate] = name
	return candidate
}

// nameSalt is the six-hex-digit salt of a name in a case-only id collision.
func nameSalt(name string) string {
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:])[:6]
}
