package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// LabelByHub names each community after its best-connected member, so that a
// report can say "auth" or "ParseConfig" rather than "Community 70". The
// degree is the one in the whole graph; of two equally connected nodes the one
// with the lower id is taken. A community none of whose members is in the
// graph is named "Community N".
func LabelByHub(g *Graph, communities map[int][]string) map[int]string {
	labels := make(map[int]string, len(communities))
	for cid, members := range communities {
		hub, found := "", false
		for _, id := range members {
			if !g.Has(id) {
				continue
			}
			if d, best := g.Degree(id), g.Degree(hub); !found || d > best || (d == best && id < hub) {
				hub, found = id, true
			}
		}
		name := ""
		if found {
			name = g.Label(hub)
			if name == "" {
				name = hub
			}
			name = strings.TrimSuffix(strings.TrimSpace(name), "()")
		}
		if name == "" {
			name = fmt.Sprintf("Community %d", cid)
		}
		labels[cid] = name
	}
	return labels
}

// MemberSigs fingerprints the membership of each community. Stored beside the
// names given to the communities, the fingerprints later tell which
// communities are still the same set of nodes: a community whose fingerprint
// has changed is a different community, and its old name may no longer fit.
// The fingerprint depends only on the member ids, not on their order or on the
// community's number.
func MemberSigs(communities map[int][]string) map[int]string {
	sigs := make(map[int]string, len(communities))
	for cid, members := range communities {
		sorted := append([]string(nil), members...)
		sort.Strings(sorted)
		h := sha256.New()
		for _, id := range sorted {
			h.Write([]byte(id))
			h.Write([]byte{0})
		}
		sigs[cid] = hex.EncodeToString(h.Sum(nil))[:16]
	}
	return sigs
}

// RemapToPrevious renumbers the communities so that each keeps, where it can,
// the number of the earlier community it shares the most nodes with. previous
// maps a node id to its community number in the earlier result.
//
// Pairs are matched greedily, largest overlap first, one to one. Communities
// that match nothing get the numbers still free, lowest first, in order of
// size.
func RemapToPrevious(communities map[int][]string, previous map[string]int) map[int][]string {
	type overlap struct{ count, old, new int }
	var overlaps []overlap
	for cid, nodes := range communities {
		counts := map[int]int{}
		seen := map[string]bool{}
		for _, id := range nodes {
			if old, ok := previous[id]; ok && !seen[id] {
				seen[id] = true
				counts[old]++
			}
		}
		for old, count := range counts {
			overlaps = append(overlaps, overlap{count, old, cid})
		}
	}
	sort.Slice(overlaps, func(i, j int) bool {
		a, b := overlaps[i], overlaps[j]
		if a.count != b.count {
			return a.count > b.count
		}
		if a.old != b.old {
			return a.old < b.old
		}
		return a.new < b.new
	})

	final := map[int]int{} // new number -> the number it ends up with
	taken := map[int]bool{}
	for _, o := range overlaps {
		if _, matched := final[o.new]; matched || taken[o.old] {
			continue
		}
		final[o.new] = o.old
		taken[o.old] = true
	}

	sorted := map[int][]string{}
	var unmatched []int
	for cid, nodes := range communities {
		sorted[cid] = append([]string(nil), nodes...)
		sort.Strings(sorted[cid])
		if _, matched := final[cid]; !matched {
			unmatched = append(unmatched, cid)
		}
	}
	sort.Slice(unmatched, func(i, j int) bool {
		a, b := sorted[unmatched[i]], sorted[unmatched[j]]
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		if c := slices.Compare(a, b); c != 0 {
			return c < 0
		}
		return unmatched[i] < unmatched[j]
	})
	next := 0
	for _, cid := range unmatched {
		for taken[next] {
			next++
		}
		final[cid] = next
		taken[next] = true
	}

	out := make(map[int][]string, len(communities))
	for cid := range communities {
		out[final[cid]] = sorted[cid]
	}
	return out
}

// LabelStillApplies reports whether a name given earlier to community cid
// still fits it.
//
// It does when the membership fingerprint (see MemberSigs) is unchanged.
// Otherwise it does only when the nodes the community shares with its earlier
// self are more than half of both: a single node left over from a large
// community keeps the number, through RemapToPrevious, but not the name.
// Pass "" for a fingerprint that is not known.
func LabelStillApplies(cid int, members []string, previous map[string]int, savedSig, currentSig string) bool {
	if savedSig != "" && savedSig == currentSig {
		return true
	}
	current := make(map[string]bool, len(members))
	for _, id := range members {
		current[id] = true
	}
	saved, shared := 0, 0
	for id, old := range previous {
		if old == cid {
			saved++
			if current[id] {
				shared++
			}
		}
	}
	if saved == 0 || len(current) == 0 {
		return false
	}
	return shared*2 > saved && shared*2 > len(current)
}
