// Command example builds the code graph of a directory (or of the listed
// files), groups it into communities, analyses it, and prints a summary.
//
//	go run ./extractors/example path/to/repo          # summary
//	go run ./extractors/example -json path/to/repo    # the whole graph as JSON
//	go run ./extractors/example -html report.html path/to/repo
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/notrobot1/funcscan2/analyze"
	"github.com/notrobot1/funcscan2/cluster"
	"github.com/notrobot1/funcscan2/extractors"
	"github.com/notrobot1/funcscan2/graph"
	"github.com/notrobot1/funcscan2/naming"
	"github.com/notrobot1/funcscan2/report"
)

func main() {
	asJSON := flag.Bool("json", false, "print the whole graph as JSON")
	list := flag.Bool("list", false, "only list the files that would be extracted")
	htmlOut := flag.String("html", "", "write the HTML report to this file")
	root := flag.String("root", "", "directory ids and paths are relative to (default: the first argument if it is a directory)")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("usage: example [-json] [-root dir] <dir or file>...")
	}

	var files []string
	for _, arg := range flag.Args() {
		found, err := extractors.CollectFiles(arg)
		if err != nil {
			log.Fatal(err)
		}
		files = append(files, found...)
	}
	if *list {
		for _, f := range files {
			fmt.Println(f)
		}
		return
	}
	if *root == "" {
		if info, err := os.Stat(flag.Arg(0)); err == nil && info.IsDir() {
			*root = flag.Arg(0)
		}
	}

	result, err := extractors.Extract(files, extractors.Options{Root: *root})
	if err != nil {
		log.Fatal(err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
		return
	}

	fmt.Printf("files: %d, nodes: %d, edges: %d\n", len(files), len(result.Nodes), len(result.Edges))
	byRelation := map[string]int{}
	for _, e := range result.Edges {
		byRelation[e.Relation]++
	}
	for relation, count := range byRelation {
		fmt.Printf("  %-14s %d\n", relation, count)
	}

	g := graph.FromExtract(result.Nodes, result.Edges)
	communities := cluster.Cluster(g, cluster.Options{})
	// Hub names; set naming.Options.LLM to have a model name the communities.
	labels, _ := naming.Communities(context.Background(), g, communities, naming.Options{})
	fmt.Printf("\ncommunities: %d (modularity %.3f)\n", len(communities), cluster.Modularity(g, communities))
	for cid := 0; cid < len(communities) && cid < 10; cid++ {
		fmt.Printf("  %3d  %-32s %4d nodes, cohesion %.2f\n",
			cid, labels[cid], len(communities[cid]), cluster.CohesionScore(g, communities[cid]))
	}

	fmt.Println("\nmost connected:")
	for _, n := range analyze.GodNodes(g, 5, 0) {
		fmt.Printf("  %-32s %d edges\n", n.Label, n.Degree)
	}
	fmt.Println("\nsurprising connections:")
	for _, c := range analyze.SurprisingConnections(g, communities, 3) {
		fmt.Printf("  %s -%s-> %s  (%s%s)\n", c.Source, c.Relation, c.Target, c.Why, c.Note)
	}
	if cycles := analyze.FindImportCycles(g, 5, 5); len(cycles) > 0 {
		fmt.Println("\nimport cycles:")
		for _, c := range cycles {
			fmt.Printf("  %v\n", c.Files)
		}
	}
	fmt.Println("\nquestions:")
	for _, q := range analyze.SuggestQuestions(g, communities, labels, 4) {
		fmt.Printf("  [%s] %s\n", q.Type, q.Question)
	}
	if *htmlOut != "" {
		r := report.New(g, communities, report.Options{Root: *root, Labels: labels})
		if err := r.WriteHTML(*htmlOut); err != nil {
			log.Fatal(err)
		}
		fmt.Println("\nreport written to", *htmlOut)
	}
	for _, w := range result.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}
