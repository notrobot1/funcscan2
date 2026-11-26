// Command example builds the code graph of a directory (or of the listed
// files), groups it into communities, and prints a summary.
//
//	go run ./extractors/example path/to/repo          # summary
//	go run ./extractors/example -json path/to/repo    # the whole graph as JSON
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/notrobot1/funcscan2/cluster"
	"github.com/notrobot1/funcscan2/extractors"
)

func main() {
	asJSON := flag.Bool("json", false, "print the whole graph as JSON")
	list := flag.Bool("list", false, "only list the files that would be extracted")
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

	graph, err := extractors.Extract(files, extractors.Options{Root: *root})
	if err != nil {
		log.Fatal(err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(graph); err != nil {
			log.Fatal(err)
		}
		return
	}

	fmt.Printf("files: %d, nodes: %d, edges: %d\n", len(files), len(graph.Nodes), len(graph.Edges))
	byRelation := map[string]int{}
	for _, e := range graph.Edges {
		byRelation[e.Relation]++
	}
	for relation, count := range byRelation {
		fmt.Printf("  %-14s %d\n", relation, count)
	}

	g := cluster.FromExtract(graph.Nodes, graph.Edges)
	communities := cluster.Cluster(g, cluster.Options{})
	labels := cluster.LabelByHub(g, communities)
	fmt.Printf("\ncommunities: %d (modularity %.3f)\n", len(communities), cluster.Modularity(g, communities))
	for cid := 0; cid < len(communities) && cid < 10; cid++ {
		fmt.Printf("  %3d  %-32s %4d nodes, cohesion %.2f\n",
			cid, labels[cid], len(communities[cid]), cluster.CohesionScore(g, communities[cid]))
	}
	for _, w := range graph.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}
