package analyze

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/notrobot1/funcscan2/cluster"
	"github.com/notrobot1/funcscan2/graph"
)

// Question types.
const (
	AmbiguousEdge  = "ambiguous_edge"  // an edge the extractor was unsure of
	BridgeNode     = "bridge_node"     // a node that holds communities together
	VerifyInferred = "verify_inferred" // a central node resting on guessed edges
	IsolatedNodes  = "isolated_nodes"  // nodes barely connected to anything
	LowCohesion    = "low_cohesion"    // a community that hardly holds together
	NoSignal       = "no_signal"       // nothing to ask; Question is empty
)

// Question is something the graph suggests looking into.
type Question struct {
	Type     string `json:"type"`
	Question string `json:"question"`
	Why      string `json:"why"`
	// LowConfidence marks a question raised by a doubtful edge, to be shown
	// apart from those raised by the structure of the graph.
	LowConfidence bool `json:"low_confidence,omitempty"`
}

// A graph of more nodes than this has its bridge nodes estimated from a
// sample of starting nodes instead of from all of them.
const (
	betweennessExactLimit = 1000
	betweennessSamples    = 100
	betweennessSeed       = 42
)

// SuggestQuestions returns up to topN questions that the shape of the graph
// raises:
//
//   - what an AMBIGUOUS edge really means;
//   - why a node sits between communities (the three with the highest
//     betweenness);
//   - whether the INFERRED edges of the most connected nodes are right;
//   - what connects the nodes that have at most one edge;
//   - whether a community whose nodes are barely linked should be split.
//
// The types take turns, so that many questions of one type do not crowd out
// the others. When the graph raises none, a single NoSignal entry says so.
func SuggestQuestions(g *graph.Graph, communities map[int][]string, labels map[int]string, topN int) []Question {
	community := communityOf(communities)
	communityName := func(cid int) string {
		if label, ok := labels[cid]; ok {
			return label
		}
		return fmt.Sprintf("Community %d", cid)
	}
	byType := map[string][]Question{}
	var types []string
	ask := func(q Question) {
		if _, seen := byType[q.Type]; !seen {
			types = append(types, q.Type)
		}
		byType[q.Type] = append(byType[q.Type], q)
	}

	for _, e := range g.Edges() {
		if e.Confidence != "AMBIGUOUS" {
			continue
		}
		relation := e.Relation
		if relation == "" {
			relation = "related to"
		}
		ask(Question{
			Type:          AmbiguousEdge,
			Question:      fmt.Sprintf("What is the exact relationship between `%s` and `%s`?", name(g, e.U), name(g, e.V)),
			Why:           fmt.Sprintf("Edge tagged AMBIGUOUS (relation: %s) - confidence is low.", relation),
			LowConfidence: true,
		})
	}

	if g.NumEdges() > 0 {
		samples := 0
		if g.NumNodes() > betweennessExactLimit {
			samples = betweennessSamples
		}
		scores := Betweenness(g, samples, betweennessSeed)
		var bridges []string
		for _, id := range g.Nodes() {
			if scores[id] > 0 && !IsFileNode(g, id) && !IsConceptNode(g, id) {
				bridges = append(bridges, id)
			}
		}
		sort.SliceStable(bridges, func(i, j int) bool { return scores[bridges[i]] > scores[bridges[j]] })
		for i := 0; i < len(bridges) && i < 3; i++ {
			id := bridges[i]
			own, placed := community[id]
			ownName := "unknown"
			if placed {
				ownName = communityName(own)
			}
			others := map[int]bool{}
			for _, neighbour := range g.Neighbors(id) {
				if cid, ok := community[neighbour]; ok && (!placed || cid != own) {
					others[cid] = true
				}
			}
			if len(others) == 0 {
				continue
			}
			var names []string
			for _, cid := range sortedCommunities(others) {
				names = append(names, "`"+communityName(cid)+"`")
			}
			ask(Question{
				Type:     BridgeNode,
				Question: fmt.Sprintf("Why does `%s` connect `%s` to %s?", name(g, id), ownName, strings.Join(names, ", ")),
				Why:      fmt.Sprintf("High betweenness centrality (%.3f) - this node is a cross-community bridge.", scores[id]),
			})
		}
	}

	var real []string
	for _, id := range g.Nodes() {
		if !IsFileNode(g, id) {
			real = append(real, id)
		}
	}
	real = byDegree(g, real)
	for i := 0; i < len(real) && i < 5; i++ {
		id := real[i]
		var inferred []graph.EdgeRef
		for _, e := range g.EdgesOf(id) {
			if e.Confidence == "INFERRED" {
				inferred = append(inferred, e)
			}
		}
		if len(inferred) < 2 {
			continue
		}
		var others [2]string
		for k, e := range inferred[:2] {
			source, target := e.Source, e.Target
			if !g.Has(source) {
				source = e.U
			}
			if !g.Has(target) {
				target = e.V
			}
			other := source
			if source == id {
				other = target
			}
			others[k] = name(g, other)
		}
		ask(Question{
			Type: VerifyInferred,
			Question: fmt.Sprintf("Are the %d inferred relationships involving `%s` (e.g. with `%s` and `%s`) actually correct?",
				len(inferred), name(g, id), others[0], others[1]),
			Why: fmt.Sprintf("`%s` has %d INFERRED edges - model-reasoned connections that need verification.", name(g, id), len(inferred)),
		})
	}

	var isolated []string
	documented := false
	for _, id := range g.Nodes() {
		switch g.Node(id).FileType {
		case "document", "paper", "image":
			documented = true
		}
		if g.Degree(id) <= 1 && !IsFileNode(g, id) && !IsConceptNode(g, id) &&
			!IsContainedOnly(g, id) && g.Node(id).FileType != "rationale" {
			isolated = append(isolated, id)
		}
	}
	if len(isolated) > 0 {
		reason := "possible missing edges"
		if documented {
			reason = "possible documentation gaps or missing edges"
		}
		var names []string
		for i := 0; i < len(isolated) && i < 3; i++ {
			names = append(names, "`"+name(g, isolated[i])+"`")
		}
		ask(Question{
			Type:     IsolatedNodes,
			Question: fmt.Sprintf("What connects %s to the rest of the system?", strings.Join(names, ", ")),
			Why:      fmt.Sprintf("%d weakly-connected nodes found - %s.", len(isolated), reason),
		})
	}

	for _, cid := range sortedCommunities(communities) {
		nodes := communities[cid]
		if score := cluster.CohesionScore(g, nodes); score < 0.15 && len(nodes) >= 5 {
			ask(Question{
				Type:     LowCohesion,
				Question: fmt.Sprintf("Should `%s` be split into smaller, more focused modules?", communityName(cid)),
				Why:      fmt.Sprintf("Cohesion score %s - nodes in this community are weakly interconnected.", formatFloat(score)),
			})
		}
	}

	if len(types) == 0 {
		return []Question{{
			Type: NoSignal,
			Why: "Not enough signal to generate questions. " +
				"This usually means the graph has no AMBIGUOUS edges, no bridge nodes, " +
				"no INFERRED relationships, and all communities are tightly cohesive.",
		}}
	}

	// One question of each type in turn, in the order the types first came up.
	out := []Question{}
	for round := 0; len(out) < topN; round++ {
		added := false
		for _, t := range types {
			if round < len(byType[t]) && len(out) < topN {
				out = append(out, byType[t][round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}

// formatFloat writes a number with as many digits as it takes to tell it from
// its neighbours, and always as a decimal: 0.0, not 0.
func formatFloat(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}
