package run

import (
	"bytes"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	core "github.com/cplieger/deadset-spec/v8/suppress"
)

// Swept is the sweep stage: every suppression record the run read, bound or not,
// what the grammar refused with a finding of its own, and one sweep of the matrix
// seeded by the bound records.
type Swept struct {
	*Prepared

	Marks    []suppress.Record
	refusals []suppress.Refusal
	Liveness graph.Result
}

// Sweep reads the three suppression documents at the target root and sweeps the
// matrix.
func (p *Prepared) Sweep() (*Swept, error) { return p.sweep(nil) }

// round is the baseline one round of a baseline write reads back: the rows recorded
// so far, through the document that write would publish, so a row reported stale is
// positioned where that document holds it.
type round struct {
	provenance core.Provenance
	rows       []core.Recorded
}

// sweep is [Prepared.Sweep] reading the round's rows, where given, in place of the
// target's baseline.
func (p *Prepared) sweep(r *round) (*Swept, error) {
	marks, refusals, err := p.suppressions(r)
	if err != nil {
		return nil, err
	}
	return &Swept{
		Prepared: p,
		Marks:    marks,
		refusals: refusals,
		Liveness: p.Matrix.Sweep(&graph.SweepInput{
			Marked: suppress.Bound(marks),
			Exempt: p.Exemptions,
			Uses:   p.uses,
			Mode:   p.Mode,
		}, graph.TestReferencesOf(p.testEvidence)),
	}, nil
}

// suppressions is the records and refusals of the inline directives, the ignore
// file and the baseline, in that order, which is the order a stale suppression is
// reported in. The directives are read from the first configuration alone; the
// ignore file and the baseline bind against the matrix inventory.
func (p *Prepared) suppressions(r *round) ([]suppress.Record, []suppress.Refusal, error) {
	var records []suppress.Record
	var refusals []suppress.Refusal
	if len(p.per) > 0 {
		inline, refused, err := suppress.Inline(p.per[0].Result, p.per[0].Resolve, p.per[0].Symbols)
		if err != nil {
			return nil, nil, err
		}
		records, refusals = inline, refused
	}
	baseline := suppress.Baseline
	if r != nil {
		baseline = func(_ string, symbols []graph.Symbol) ([]suppress.Record, []suppress.Refusal, error) {
			var body bytes.Buffer
			if err := core.WriteBaseline(&body, r.rows, r.provenance, nil); err != nil {
				return nil, nil, err
			}
			return suppress.BaselineDocument(body.Bytes(), symbols)
		}
	}
	for _, read := range []struct {
		of   func(path string, symbols []graph.Symbol) ([]suppress.Record, []suppress.Refusal, error)
		name string
	}{
		{suppress.IgnoreFile, core.IgnoreFileName},
		{baseline, core.BaselineFileName},
	} {
		held, refused, err := read.of(filepath.Join(p.Root, read.name), p.Merged.Symbols)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, held...)
		refusals = append(refusals, refused...)
	}
	return records, refusals, nil
}

// Retained is every exemption the retained-symbol listing names: those that held a
// dead symbol back, then those that held a write-only finding back, grouped by
// symbol in inventory order.
func (s *Swept) Retained() []graph.Exemption {
	writeOnly := kinds.WriteOnlyRetained(&kinds.Input{
		Config: &s.req.Config,
		Merged: s.Merged,
		Sweep:  &s.Liveness,
		Exempt: s.Exemptions,
		Per:    s.per,
		Mode:   s.Mode,
	})
	if len(writeOnly) == 0 {
		return s.Liveness.Retained
	}
	held := make(map[graph.SymbolID][]graph.Exemption)
	all := slices.Concat(s.Liveness.Retained, writeOnly)
	for i := range all {
		held[all[i].ID] = append(held[all[i].ID], all[i])
	}
	ordered := make([]graph.Exemption, 0, len(s.Liveness.Retained)+len(writeOnly))
	for i := range s.Merged.Symbols {
		ordered = append(ordered, held[s.Merged.Symbols[i].ID]...)
	}
	return ordered
}
