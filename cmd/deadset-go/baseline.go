package main

import (
	"bytes"
	"context"
	"slices"

	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/report"
	"github.com/cplieger/deadset-go/internal/suppress"
)

// baselineMechanism is the mechanism a stale-suppression record of a baseline row
// names.
const baselineMechanism = "baseline"

// baselineReader is the reader of the baseline a run binds against: the document at
// the target root, or the rows a round of a baseline write reads back where it was
// given some.
func baselineReader(rows *[]suppress.Recorded) func(string, []graph.Symbol) ([]suppress.Record, []suppress.Refusal, error) {
	if rows == nil {
		return suppress.Baseline
	}
	return func(_ string, symbols []graph.Symbol) ([]suppress.Record, []suppress.Refusal, error) {
		var body bytes.Buffer
		if err := suppress.WriteBaseline(&body, *rows, baselineProvenance()); err != nil {
			return nil, nil, err
		}
		return suppress.BaselineDocument(body.Bytes(), symbols)
	}
}

// baselineProvenance is the reason every row this analyzer writes carries.
func baselineProvenance() suppress.Provenance {
	return suppress.Provenance{Analyzer: name, Version: version()}
}

// baselineRows is the rows a baseline write records: the fixpoint of rounds over the
// target, the first reading no baseline and each later one reading back the rows
// recorded so far, the directives and the ignore file read as on every run.
//
// A round records one row per finding of its report, in report order, after the
// rows earlier rounds recorded, and removes every row it reports stale. It records
// no row for a finding about a row of a document, which no record binds to, no row
// identical to one already recorded, and no row an earlier round removed. A
// pending finding lives in an edge evaluation rather than in the report's findings,
// so it records none either. The write ends with the first round that records no
// row and removes none.
//
// The rounds share one load and one exemption set, which no suppression record
// changes, and a write whose rounds report N distinct rows ends within 2N + 1
// rounds.
func baselineRows(ctx context.Context, resolved *resolution, options *exempt.Options,
	answered *corpusRecord,
) ([]suppress.Recorded, error) {
	rows := []suppress.Recorded{}
	removed := make(map[suppress.Recorded]bool)
	shared := &preparation{}
	for {
		round := *resolved
		round.prepared = shared
		held := slices.Clone(rows)
		round.baseline = &held
		envelope, _, err := reportOf(ctx, &round, options, answered)
		if err != nil {
			return nil, err
		}
		report.Sort(&envelope, round.config.Reporters.Sort)

		stale := staleRows(envelope.StaleSuppressions)
		kept := slices.DeleteFunc(slices.Clone(rows), func(row suppress.Recorded) bool { return stale[row] })
		for row := range stale {
			removed[row] = true
		}
		kept, recorded := recordRound(envelope.Findings, kept, removed)
		if recorded == 0 && len(stale) == 0 {
			return kept, nil
		}
		rows = kept
	}
}

// recordRound appends to the rows recorded so far one row per finding of a round's
// report, in report order, and returns them with the number it appended: none for a
// finding about a row of a document, for a row already held and for a row an
// earlier round removed.
func recordRound(findings []kinds.Finding, kept []suppress.Recorded,
	removed map[suppress.Recorded]bool,
) (rows []suppress.Recorded, recorded int) {
	for i := range findings {
		row, records := recordedRow(&findings[i])
		if !records || removed[row] || slices.Contains(kept, row) {
			continue
		}
		kept = append(kept, row)
		recorded++
	}
	return kept, recorded
}

// recordedRow is the row one finding records, and false for a finding about a row
// of a document, which no record binds to: its row would be stale on the first read
// back.
func recordedRow(found *kinds.Finding) (suppress.Recorded, bool) {
	if kinds.AboutDocumentRow(found) {
		return suppress.Recorded{}, false
	}
	return suppress.Recorded{Code: found.Code, Symbol: found.Symbol.Ref, Path: found.Position.Path}, true
}

// staleRows is every baseline row a round reported stale, by the entry it names.
func staleRows(stale []report.StaleSuppression) map[suppress.Recorded]bool {
	held := make(map[suppress.Recorded]bool)
	for i := range stale {
		if stale[i].Mechanism != baselineMechanism {
			continue
		}
		entry := &stale[i].Entry
		held[suppress.Recorded{Code: entry.Code, Symbol: entry.Symbol, Path: entry.Path}] = true
	}
	return held
}
