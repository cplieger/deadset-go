package main

import (
	"context"
	"fmt"
	"io"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

const printConfigUsage = "usage: deadset-go print-config [--target=DIR] [--config=FILE] [--central=FILE] [--min-confidence=CLASS]"

// printConfig writes the resolved configuration, with the provenance of every
// setting, to stdout.
func printConfig(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, code := parsed("print-config", printConfigUsage, args, stderr)
	if code != vocab.ExitClean {
		return code
	}
	resolved, code := flags.resolution(ctx, stderr)
	if code != vocab.ExitClean {
		return code
	}
	if err := resolved.Print(stdout); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return vocab.ExitFailure
	}
	return vocab.ExitClean
}
