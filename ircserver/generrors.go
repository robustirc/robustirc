// +build ignore

package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	output, err := os.Create("errors.go")
	if err != nil {
		log.Fatal(err)
	}
	defer output.Close()

	fmt.Fprintf(output, `package ircserver

import "github.com/sorcix/irc"

var ErrorCodes = make(map[string]bool)

func init() {
`)

	// Iterate over all top-level declarations (including constants) of
	// github.com/sorcix/irc and put ERR_ constants into a map. This map can
	// then be used to judge whether an IRC message represents an error or not.
	pkg, err := build.Import("github.com/sorcix/irc", "", build.ImportComment)
	if err != nil {
		log.Fatal(err)
	}

	fset := token.NewFileSet()

	for _, name := range pkg.GoFiles {
		path := filepath.Join(pkg.Dir, name)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			log.Fatal(err)
		}

		ast.FilterFile(f, func(ident string) bool {
			if !strings.HasPrefix(ident, "ERR_") {
				return false
			}
			fmt.Fprintf(output, "\tErrorCodes[irc.%s] = true\n", ident)
			return true
		})
	}

	fmt.Fprintf(output, `}
`)
}
