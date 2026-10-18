package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/memory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// failed prints the error a run ended with and returns its exit code. A run that
// ends with exit code 3 prints the class lines of [failure.Write]. A run the
// memory governor or a signal cancelled names that cause whatever stage stopped,
// because the stage running then returns its own wrapping of the cancellation.
func failed(ctx context.Context, stderr io.Writer, err error) int {
	if ctx.Err() != nil {
		cause := context.Cause(ctx)
		if exhausted, ok := errors.AsType[*memory.ExhaustedError](cause); ok {
			return fail(stderr, failure.New(vocab.RunMemoryExhausted, exhausted.Error()+"; run on a machine with more memory, or narrow the scope"))
		}
		return fail(stderr, failure.Wrap(vocab.RunInterruptedRun, cause))
	}
	if _, classed := errors.AsType[*failure.Error](err); classed {
		return fail(stderr, err)
	}
	if code := exitCodeFor(err); code == vocab.ExitUsage {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return code
	}
	return fail(stderr, err)
}

// fail prints err's failures, a product fault where it holds none, and returns
// exit code 3.
func fail(stderr io.Writer, err error) int {
	_ = failure.Write(stderr, failure.Of(err))
	return vocab.ExitFailure
}

func unwrittenAnswer(stderr io.Writer, verb string, err error) int {
	return fail(stderr, failure.Wrap(vocab.RunUnwrittenOutput, fmt.Errorf("%s: write the answer to standard output: %w", verb, err)))
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
	if errors.Is(err, exempt.ErrTemplateDir) || errors.Is(err, load.ErrNoConfiguration) || errors.Is(err, graph.ErrMatrix) ||
		errors.Is(err, core.ErrMalformed) || errors.Is(err, edges.ErrMalformed) {
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
func printLines(stdout, stderr io.Writer, verb string, lines []string, unmatched []roots.Unmatched) int {
	for _, line := range lines {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return unwrittenAnswer(stderr, verb, err)
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
