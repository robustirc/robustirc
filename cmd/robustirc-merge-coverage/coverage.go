// robustirc-merge-coverage is a tool to merge two coverage reports, summing
// their counters.
//
// See coverage.sh for how to use it.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

var (
	inputName = flag.String("input",
		"",
		"comma-separated list of paths to input files to read")
	outputName = flag.String("output",
		"",
		"path to write output to")
)

type CoverBlock struct {
	Stmts uint16
	Count uint32
}

func main() {
	flag.Parse()

	total := make(map[string]CoverBlock)

	for _, name := range strings.Split(*inputName, ",") {
		in, err := os.Open(name)
		if err != nil {
			log.Fatal(err)
		}
		defer in.Close()

		scanner := bufio.NewScanner(in)
		// Skip the first line, it contains “mode: count”
		scanner.Scan()
		for scanner.Scan() {
			var b CoverBlock
			var name string

			_, err := fmt.Sscanf(scanner.Text(), "%s %d %d", &name,
				&b.Stmts,
				&b.Count)
			if err != nil {
				log.Fatal(err)
			}
			if existing, ok := total[name]; !ok {
				total[name] = b
			} else {
				existing.Count += b.Count
				total[name] = existing
			}
		}
		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	}

	out, err := os.Create(*outputName)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	fmt.Fprintf(out, "mode: count\n")

	for name, block := range total {
		_, err := fmt.Fprintf(out, "%s %d %d\n", name,
			block.Stmts,
			block.Count)
		if err != nil {
			log.Fatal(err)
		}
	}
}
