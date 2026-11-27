package naming

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/notrobot1/funcscan2/graph"
)

func testGraph() (*graph.Graph, map[int][]string) {
	g := graph.New()
	g.AddNode("srv", "Server")
	g.AddNode("h", "Handle()")
	g.AddNode("r", "route()")
	g.AddNode("cfg", "Config")
	g.AddNode("p", "parse()")
	g.AddEdge("srv", "h", 1, "")
	g.AddEdge("srv", "r", 1, "")
	g.AddEdge("cfg", "p", 1, "")
	return g, map[int][]string{0: {"h", "r", "srv"}, 1: {"cfg", "p"}}
}

func TestWithoutAModelCommunitiesAreNamedAfterTheirHubs(t *testing.T) {
	g, communities := testGraph()
	labels, err := Communities(context.Background(), g, communities, Options{})
	if err != nil || labels[0] != "Server" || labels[1] != "Config" {
		t.Errorf("labels %v, err %v", labels, err)
	}
}

func TestModelNamesReplaceHubNames(t *testing.T) {
	g, communities := testGraph()
	var asked string
	llm := LLMFunc(func(_ context.Context, prompt string) (string, error) {
		asked = prompt
		return "Sure:\n```json\n{\"0\": \" HTTP serving \", \"7\": \"ignored\"}\n```", nil
	})
	labels, err := Communities(context.Background(), g, communities, Options{LLM: llm, Central: []string{"srv"}})
	if err != nil {
		t.Fatal(err)
	}
	if labels[0] != "HTTP serving" {
		t.Errorf("community 0: %q", labels[0])
	}
	if labels[1] != "Config" {
		t.Errorf("a community the model skipped keeps its hub name, got %q", labels[1])
	}
	if _, extra := labels[7]; extra {
		t.Error("a name for a community that was not asked about was kept")
	}
	if !strings.Contains(asked, "0: Server, Handle, route") || !strings.Contains(asked, "1: Config, parse") {
		t.Errorf("prompt does not list the communities, central node first:\n%s", asked)
	}
}

func TestAFailedBatchCostsOnlyItsOwnNames(t *testing.T) {
	g, communities := testGraph()
	calls := 0
	llm := LLMFunc(func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("rate limited")
		}
		return `{"1": "Config loading"}`, nil
	})
	labels, err := Communities(context.Background(), g, communities, Options{LLM: llm, BatchSize: 1})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err: %v", err)
	}
	if labels[0] != "Server" || labels[1] != "Config loading" {
		t.Errorf("labels: %v", labels)
	}
}

func TestACutOffReplyKeepsTheNamesThatArrived(t *testing.T) {
	got, err := parseReply(`{"0": "Caf\u00e9 \"core\"", "1": "Half a na`, []int{0, 1})
	if err != nil || got[0] != `Café "core"` || len(got) != 1 {
		t.Errorf("got %v, err %v", got, err)
	}
	if _, err := parseReply("I cannot help with that.", []int{0}); err == nil {
		t.Error("a reply with no names should be an error")
	}
}
