package main

import (
	"fmt"
	"io"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

const printConfigUsage = "usage: deadset-go print-config [--target=DIR] [--config=FILE] [--central=FILE] [--min-confidence=CLASS]"

// printConfig writes the resolved configuration, with the provenance of every
// setting, to stdout.
func printConfig(args []string, stdout, stderr io.Writer) int {
	req, code := resolve("print-config", printConfigUsage, args, stderr)
	if code != vocab.ExitClean {
		return code
	}
	if err := config.Print(stdout, req.Config, req.Provenance); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return vocab.ExitFailure
	}
	return vocab.ExitClean
}
