package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// partitionCacheName is the file, inside Options.CacheDir, that holds the last
// result and the fingerprint of the graph it was computed for.
const partitionCacheName = ".funcscan_partition_cache.json"

type cachedPartition struct {
	Version      int                 `json:"version"`
	TopologyHash string              `json:"topology_hash"`
	Communities  map[string][]string `json:"communities"`
}

// topologyHash fingerprints everything the result depends on: the nodes, the
// edges with their weight and confidence, and the options.
//
// Each node and edge is hashed on its own and the hashes are XORed together,
// so the fingerprint does not depend on the order they are visited in and
// needs no sorting — it has to stay much cheaper than the clustering it saves.
// The two ends of an edge are put in order first, so (A, B) and (B, A) are the
// same edge.
func topologyHash(g *Graph, resolution, hubsPercentile, ambiguousScale float64) string {
	var nodes, edges [sha256.Size]byte
	xor := func(acc *[sha256.Size]byte, payload string) {
		sum := sha256.Sum256([]byte(payload))
		for i := range acc {
			acc[i] ^= sum[i]
		}
	}
	for _, id := range g.Nodes() {
		xor(&nodes, "n\x00"+id)
	}
	for _, e := range g.Edges() {
		a, b := e.U, e.V
		if b < a {
			a, b = b, a
		}
		xor(&edges, "e\x00"+a+"\x01"+b+"\x01"+strconv.FormatFloat(e.Weight, 'g', -1, 64)+"\x01"+e.Confidence)
	}
	h := sha256.New()
	fmt.Fprintf(h, "topology-v1|%g|%g|%g|%d|%d|", resolution, hubsPercentile, ambiguousScale, g.NumNodes(), g.NumEdges())
	h.Write(nodes[:])
	h.Write(edges[:])
	return "v1:" + hex.EncodeToString(h.Sum(nil))
}

// loadCachedPartition returns the stored result if it was computed for
// exactly this fingerprint, and nil otherwise — including when the file is
// missing or unreadable.
func loadCachedPartition(path, fingerprint string) map[int][]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cached cachedPartition
	if json.Unmarshal(data, &cached) != nil || cached.TopologyHash != fingerprint || cached.Communities == nil {
		return nil
	}
	out := make(map[int][]string, len(cached.Communities))
	for key, nodes := range cached.Communities {
		cid, err := strconv.Atoi(key)
		if err != nil {
			return nil
		}
		out[cid] = nodes
	}
	return out
}

// storeCachedPartition writes the result beside its fingerprint. The file is
// written under another name and renamed into place, so a reader never sees
// half of it. Failure is ignored: the cache only saves time.
func storeCachedPartition(path, fingerprint string, communities map[int][]string) {
	cached := cachedPartition{Version: 1, TopologyHash: fingerprint, Communities: map[string][]string{}}
	for cid, nodes := range communities {
		cached.Communities[strconv.Itoa(cid)] = nodes
	}
	data, err := json.Marshal(cached)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), partitionCacheName+".*")
	if err != nil {
		return
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}
