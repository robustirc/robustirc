package main

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func verifyLockDefer(path string, t *testing.T) {
	t.Parallel()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err.Error())
	}
	defer f.Close()
	// XXX: Ideally, we would analyze the Go source code and find all
	// Lock() calls on sync.{RW,}Mutex and their deferred Unlock()
	// counterpart. In practice, analyzing the text is good enough for
	// now.
	scanner := bufio.NewScanner(f)
	var prev string
	for scanner.Scan() {
		if strings.Contains(prev, "Lock()") {
			if !strings.Contains(scanner.Text(), "Unlock()") {
				t.Fatalf("Lock() without Unlock() in next line: %q %q", prev, scanner.Text())
			}
		}
		prev = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("Reading %q: %v", path, err.Error())
	}
}

var (
	whitelistedFiles = []string{
		// TODO: verify that refactoring outputstream.go to use
		// functions for every lock actually results in a measurable
		// performance decrease.
		"outputstream.go",
	}
)

func TestLockDefer(t *testing.T) {
	// List all .go files that make up RobustIRC
	output, err := exec.Command("go",
		"list",
		"-f",
		`{{ range $f := .GoFiles }}{{ $.Dir }}/{{ $f }}{{ "\n" }}{{ end }}`,
		"github.com/robustirc/robustirc/...").Output()
	if err != nil {
		t.Fatalf("Could not list Go files: %v", err)
	}

	for _, path := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		var skip bool
		for _, whitelistedFile := range whitelistedFiles {
			if strings.HasSuffix(path, whitelistedFile) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		path := path // capture range variable
		t.Run(path, func(t *testing.T) {
			verifyLockDefer(path, t)
		})
	}
}
