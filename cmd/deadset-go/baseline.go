package main

import (
	"bytes"
	"context"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	"github.com/cplieger/deadset-spec/v8/report"
	core "github.com/cplieger/deadset-spec/v8/suppress"
)

// baselineReader is the reader of the baseline a run binds against: the document at
// the target root, or the rows a round of a baseline write reads back where it was
// given some.
func baselineReader(rows *[]core.Recorded) func(string, []graph.Symbol) ([]suppress.Record, []suppress.Refusal, error) {
	if rows == nil {
		return suppress.Baseline
	}
	return func(_ string, symbols []graph.Symbol) ([]suppress.Record, []suppress.Refusal, error) {
		var body bytes.Buffer
		if err := core.WriteBaseline(&body, *rows, baselineProvenance(), nil); err != nil {
			return nil, nil, err
		}
		return suppress.BaselineDocument(body.Bytes(), symbols)
	}
}

// baselineProvenance is the reason every row this analyzer writes carries.
func baselineProvenance() core.Provenance {
	return core.Provenance{Analyzer: name, Version: version()}
}

// baselineRows is the rows a baseline write records: [core.Fixpoint] over rounds of
// the target, each reading back the rows recorded so far, the directives and the
// ignore file read as on every run. A pending finding lives in an edge evaluation
// rather than in the report's findings, so it records no row. The rounds share one
// load and one exemption set, which no suppression record changes.
func baselineRows(ctx context.Context, resolved *resolution, options *exempt.Options,
	answered *corpusRecord,
) ([]core.Recorded, error) {
	shared := &preparation{}
	return core.Fixpoint(func(read []core.Recorded) (recorded, stale []core.Recorded, err error) {
		round := *resolved
		round.prepared = shared
		round.baseline = &read
		built, _, err := reportOf(ctx, &round, options, answered)
		if err != nil {
			return nil, nil, err
		}
		for i := range built.Findings {
			if found := &built.Findings[i]; core.Recordable(found) {
				recorded = append(recorded, core.Recorded{Code: found.Code, Symbol: found.Symbol.Ref, Path: found.Position.Path})
			}
		}
		return recorded, staleRows(built.StaleSuppressions), nil
	})
}

// staleRows is every baseline row a round reported stale, by the entry it names.
func staleRows(stale []report.StaleSuppression) []core.Recorded {
	var held []core.Recorded
	for i := range stale {
		if stale[i].Mechanism != string(core.Baseline) {
			continue
		}
		entry := &stale[i].Entry
		held = append(held, core.Recorded{Code: entry.Code, Symbol: entry.Symbol, Path: entry.Path})
	}
	return held
}
