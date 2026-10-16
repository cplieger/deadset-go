package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/memory"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// failed prints the error a run ended with and returns its exit code. A setup
// failure is one line per failure, each with the setup-failure prefix. A run the
// memory governor ended prints the memory line whatever stage stopped, because the
// governor cancels ctx and the stage running then returns its own wrapping of it.
func failed(ctx context.Context, stderr io.Writer, err error) int {
	if exhausted, ok := errors.AsType[*memory.ExhaustedError](context.Cause(ctx)); ok {
		fmt.Fprintln(stderr, exhausted.Error())
		return vocab.ExitFailure
	}
	if setup, ok := errors.AsType[*load.SetupError](err); ok {
		for _, one := range setup.Failures {
			fmt.Fprintln(stderr, one.Line())
		}
		return vocab.ExitFailure
	}
	fmt.Fprintf(stderr, "deadset-go: %v\n", err)
	return exitCodeFor(err)
}

// exitCodeFor maps an error to the exit code contract/exit-codes.json gives it. A
// configuration refused by its decode, a template directory the scan cannot read, a
// matrix no run can analyze and a malformed ignore file, baseline or edges document
// are usage errors; anything else that stopped the run before a verdict existed, a
// configuration that does not load or a refused finding among them, is a failure.
func exitCodeFor(err error) int {
	if _, refusal := errors.AsType[*config.Error](err); refusal {
		return vocab.ExitUsage
	}
	if errors.Is(err, exempt.ErrTemplateDir) {
		return vocab.ExitUsage
	}
	if errors.Is(err, load.ErrNoConfiguration) || errors.Is(err, graph.ErrMatrix) {
		return vocab.ExitUsage
	}
	if errors.Is(err, core.ErrMalformed) || errors.Is(err, edges.ErrMalformed) {
		return vocab.ExitUsage
	}
	return vocab.ExitFailure
}

// namedUnbuilt writes one line per configuration the run derived and the target does
// not build, with the load error that dropped it, which names every diagnostic.
func namedUnbuilt(w io.Writer, unbuilt []load.Unbuilt) {
	for i := range unbuilt {
		fmt.Fprintf(w, "deadset-go: %s is derived from the target tree, does not build, and is dropped from the matrix: %v\n",
			unbuilt[i].Configuration.ID, unbuilt[i].Err)
	}
}

// printLines writes the answer of a print verb, one line each, then every configured
// root that names nothing on stderr by its issue kind, which fails the run.
func printLines(stdout, stderr io.Writer, verb string, lines []string, unmatched []graph.Unmatched) int {
	for _, line := range lines {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			fmt.Fprintf(stderr, "deadset-go: %s: %v\n", verb, err)
			return vocab.ExitFailure
		}
	}
	for _, one := range unmatched {
		fmt.Fprintf(stderr, "%s: roots.patterns names nothing: %s\n", vocab.UnmatchedRoot, one.Source)
	}
	if len(unmatched) > 0 {
		return vocab.ExitFindings
	}
	return vocab.ExitClean
}
