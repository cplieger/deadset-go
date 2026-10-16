package run

import (
	"context"

	"github.com/cplieger/deadset-spec/v8/report"
	core "github.com/cplieger/deadset-spec/v8/suppress"
)

// BaselineRows is the rows a baseline write records: [core.Fixpoint] over rounds of
// the sweep, each reading back the rows recorded so far, the directives and the
// ignore file read as on every run. The rounds share p, which no suppression record
// changes. A pending finding lives in an edge evaluation rather than in the
// findings, so it records no row.
func BaselineRows(ctx context.Context, p *Prepared, self *Self) ([]core.Recorded, error) {
	provenance := Provenance(self)
	return core.Fixpoint(func(read []core.Recorded) (recorded, stale []core.Recorded, err error) {
		swept, err := p.sweep(&round{rows: read, provenance: provenance})
		if err != nil {
			return nil, nil, err
		}
		found, err := swept.Findings(ctx)
		if err != nil {
			return nil, nil, err
		}
		built, err := found.Report(self)
		if err != nil {
			return nil, nil, err
		}
		for i := range built.Findings {
			if f := &built.Findings[i]; core.Recordable(f) {
				recorded = append(recorded, core.Recorded{Code: f.Code, Symbol: f.Symbol.Ref, Path: f.Position.Path})
			}
		}
		return recorded, staleRows(built.StaleSuppressions), nil
	})
}

// Provenance is the reason every baseline row this analyzer writes carries.
func Provenance(self *Self) core.Provenance {
	return core.Provenance{Analyzer: self.Analyzer.Name, Version: self.Analyzer.Version}
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
