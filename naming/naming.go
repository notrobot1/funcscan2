// Package naming gives communities names a person can read.
//
// Out of the box it names each community after its best-connected node
// (cluster.LabelByHub): quick, repeatable, and often good enough — "Server",
// "parseConfig". Given a language model it asks the model for a short name
// that says what the community is about — "HTTP request routing" — and keeps
// the hub name wherever the model gives none.
//
//	labels, err := naming.Communities(ctx, g, communities, naming.Options{})          // hub names
//	labels, err := naming.Communities(ctx, g, communities, naming.Options{LLM: myLLM}) // model names
//
// No model client ships with the package: LLM is the one method a caller
// implements to plug in a provider.
package naming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/notrobot1/funcscan2/cluster"
	"github.com/notrobot1/funcscan2/graph"
)

// LLM is a language model that answers a prompt with text.
type LLM interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// LLMFunc lets an ordinary function serve as an LLM.
type LLMFunc func(ctx context.Context, prompt string) (string, error)

// Complete calls f.
func (f LLMFunc) Complete(ctx context.Context, prompt string) (string, error) { return f(ctx, prompt) }

// Options tune Communities. The zero value names communities after their
// hubs, without a model.
type Options struct {
	// LLM names the communities. Nil means hub names only.
	LLM LLM
	// Central lists node ids to show the model first for each community: its
	// most connected nodes say the most about it (see analyze.GodNodes).
	Central []string
	// TopK is how many node labels of each community the model is shown.
	// Zero means 12.
	TopK int
	// BatchSize is how many communities go into one prompt. Zero means 100,
	// which fits a 16k-token context with TopK at its default.
	BatchSize int
	// MaxCommunities, when above zero, has the model name only that many
	// communities, the largest ones; the rest keep their hub names.
	MaxCommunities int
}

const (
	defaultTopK      = 12
	defaultBatchSize = 100
	maxLabelLength   = 60 // longer node labels are cut, to keep the prompt small
)

// Communities returns a name for every community.
//
// Every community gets one: the model's where it gave one, the hub name
// otherwise. A failed or unreadable model reply costs only the names of the
// communities in that prompt; the error then says which prompts failed, and
// the returned names are still complete and usable.
func Communities(ctx context.Context, g *graph.Graph, communities map[int][]string, opts Options) (map[int]string, error) {
	labels := cluster.LabelByHub(g, communities)
	if opts.LLM == nil || len(communities) == 0 {
		return labels, nil
	}
	topK, batchSize := opts.TopK, opts.BatchSize
	if topK <= 0 {
		topK = defaultTopK
	}
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	lines, ids := promptLines(g, communities, opts.Central, opts.MaxCommunities, topK)

	var failures []error
	for start := 0; start < len(lines); start += batchSize {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		end := min(start+batchSize, len(lines))
		reply, err := opts.LLM.Complete(ctx, prompt(lines[start:end]))
		if err == nil {
			var named map[int]string
			if named, err = parseReply(reply, ids[start:end]); err == nil {
				for cid, name := range named {
					labels[cid] = name
				}
				continue
			}
		}
		failures = append(failures, fmt.Errorf("communities %d-%d of %d: %w", start+1, end, len(lines), err))
	}
	return labels, errors.Join(failures...)
}

// promptLines describes each community to the model in one line: its number
// and up to topK labels of its nodes, the central ones first. Largest
// communities come first. A community none of whose nodes has a label is left
// out. ids holds the community number of each line.
func promptLines(g *graph.Graph, communities map[int][]string, central []string, limit, topK int) (lines []string, ids []int) {
	isCentral := make(map[string]bool, len(central))
	for _, id := range central {
		isCentral[id] = true
	}
	order := make([]int, 0, len(communities))
	for cid := range communities {
		order = append(order, cid)
	}
	sort.Slice(order, func(i, j int) bool {
		if a, b := len(communities[order[i]]), len(communities[order[j]]); a != b {
			return a > b
		}
		return order[i] < order[j]
	})
	if limit > 0 && len(order) > limit {
		order = order[:limit]
	}
	for _, cid := range order {
		members := communities[cid]
		ranked := make([]string, 0, len(members))
		for _, id := range members {
			if isCentral[id] {
				ranked = append(ranked, id)
			}
		}
		for _, id := range members {
			if !isCentral[id] {
				ranked = append(ranked, id)
			}
		}
		var names []string
		seen := map[string]bool{}
		for _, id := range ranked {
			label := g.Label(id)
			if label == "" {
				label = id
			}
			label = strings.Trim(strings.TrimSpace(label), "()")
			if r := []rune(label); len(r) > maxLabelLength {
				label = string(r[:maxLabelLength])
			}
			if key := strings.ToLower(label); label != "" && !seen[key] {
				seen[key] = true
				names = append(names, label)
			}
			if len(names) >= topK {
				break
			}
		}
		if len(names) > 0 {
			lines = append(lines, fmt.Sprintf("%d: %s", cid, strings.Join(names, ", ")))
			ids = append(ids, cid)
		}
	}
	return lines, ids
}

// prompt asks for a name for each of the communities described by lines.
func prompt(lines []string) string {
	return "Below are groups of related symbols from a codebase, one group per line, " +
		"as \"<id>: <symbol>, <symbol>, ...\".\n" +
		"Give each group a short name (2 to 5 words) that says what the group is about, " +
		"in terms a developer new to the code would understand. Do not just repeat a symbol.\n" +
		"Respond ONLY with a JSON object mapping the group id (as a string) to its name, " +
		"for example {\"0\": \"HTTP request routing\", \"1\": \"Config file parsing\"}.\n\n" +
		strings.Join(lines, "\n")
}

var (
	codeFence = regexp.MustCompile("(?i)^\\s*```(?:json)?\\s*|\\s*```\\s*$")
	// One complete "<id>": "<name>" pair, for a reply that broke off halfway.
	replyPair = regexp.MustCompile(`"?(-?\d+)"?\s*:\s*"([^"\\]*(?:\\.[^"\\]*)*)"`)
)

// parseReply reads the model's JSON object of names. Text around the object
// and a Markdown code fence are tolerated, and so is a reply that was cut
// off: the names that did arrive whole are kept. Only ids that were asked
// about are returned.
func parseReply(reply string, ids []int) (map[int]string, error) {
	cleaned := codeFence.ReplaceAllString(strings.TrimSpace(reply), "")
	if !strings.HasPrefix(cleaned, "{") {
		if start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}"); start >= 0 && end > start {
			cleaned = cleaned[start : end+1]
		}
	}
	names := map[string]string{}
	var object map[string]any
	if json.Unmarshal([]byte(cleaned), &object) == nil {
		for key, value := range object {
			if name, ok := value.(string); ok {
				names[key] = name
			}
		}
	} else {
		pairs := replyPair.FindAllStringSubmatch(cleaned, -1)
		if len(pairs) == 0 {
			if len(reply) > 120 {
				reply = reply[:120]
			}
			return nil, fmt.Errorf("the reply is not a JSON object of names: %q", reply)
		}
		for _, pair := range pairs {
			name := pair[2]
			var decoded string
			if json.Unmarshal([]byte(`"`+name+`"`), &decoded) == nil {
				name = decoded
			}
			names[pair[1]] = name
		}
	}
	out := map[int]string{}
	for _, cid := range ids {
		if name := strings.TrimSpace(names[strconv.Itoa(cid)]); name != "" {
			out[cid] = name
		}
	}
	return out, nil
}
