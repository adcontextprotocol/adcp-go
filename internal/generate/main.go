// Command generate reads JSON Schema files from a directory and produces
// Go type definitions. It is invoked via go:generate from the tmp package.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	schemaDir := flag.String("schema", "", "directory containing JSON Schema files")
	outFile := flag.String("out", "", "output Go file path")
	pkg := flag.String("pkg", "tmp", "Go package name for generated file")
	flag.Parse()

	if *schemaDir == "" || *outFile == "" {
		fmt.Fprintln(os.Stderr, "usage: generate -schema <dir> -out <file> [-pkg <name>]")
		os.Exit(1)
	}

	absSchema, err := filepath.Abs(*schemaDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve schema dir: %v\n", err)
		os.Exit(1)
	}

	ir, err := LoadSchemas(absSchema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load schemas: %v\n", err)
		os.Exit(1)
	}

	src, err := Render(*pkg, ir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*outFile, src, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
}
