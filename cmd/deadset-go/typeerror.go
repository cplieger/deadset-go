package main

import (
	"fmt"
	"io"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/report"
)

// withoutSkipped drops every finding positioned inside a declaration a type error
// skipped: nothing is reported about the declaration or any part of it.
func withoutSkipped(findings []kinds.Finding, skips []graph.TypeErrorSkip) []kinds.Finding {
	if len(skips) == 0 {
		return findings
	}
	return slices.DeleteFunc(findings, func(found kinds.Finding) bool {
		return slices.ContainsFunc(skips, func(skip graph.TypeErrorSkip) bool {
			return skip.FromLine > 0 && skip.Path == found.Position.Path &&
				skip.FromLine <= found.Position.Line && found.Position.Line <= skip.ToLine
		})
	})
}

// typeErrorSkipsReported is the report's record of every type error that skipped
// something.
func typeErrorSkipsReported(skips []graph.TypeErrorSkip) []report.TypeErrorSkip {
	held := make([]report.TypeErrorSkip, 0, len(skips))
	for _, skip := range skips {
		held = append(held, report.TypeErrorSkip{Path: skip.Path, Line: skip.Line, Message: skip.Message})
	}
	return held
}

// namedSkips writes one line per type-error skip, so a maintainer reading the run
// learns which declaration the analysis did not evaluate and why.
func namedSkips(w io.Writer, skips []report.TypeErrorSkip) {
	for _, skip := range skips {
		fmt.Fprintf(w, "deadset-go: %s:%d: %s: the function or statement holding this type error is not evaluated\n",
			skip.Path, skip.Line, skip.Message)
	}
}
