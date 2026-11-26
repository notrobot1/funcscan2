// Command example runs the Go extractor on one file and prints what it found.
//
//	go run ./example path/to/file.go          # summary
//	go run ./example -json path/to/file.go    # full result as JSON
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/notrobot1/funcscan2/extractors"
)

func main() {
	asJSON := flag.Bool("json", false, "print the full result as JSON")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: example [-json] <file.go>")
	}

	result, err := extractors.ExtractGo(flag.Arg(0))
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

	fmt.Printf("nodes: %d, edges: %d, raw calls: %d\n\n",
		len(result.Nodes), len(result.Edges), len(result.RawCalls))
	for _, n := range result.Nodes {
		fmt.Printf("node  %-40s %-24s %s\n", n.ID, n.Label, n.SourceLocation)
	}
	fmt.Println()
	for _, e := range result.Edges {
		fmt.Printf("edge  %s -[%s]-> %s  (%s)\n", e.Source, e.Relation, e.Target, e.SourceLocation)
	}
	fmt.Println()
	for _, c := range result.RawCalls {
		fmt.Printf("call  %s -> %s  (%s)\n", c.CallerNID, c.Callee, c.SourceLocation)
	}
}
