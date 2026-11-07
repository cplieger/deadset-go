package run

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Swept is the sweep stage: every suppression record the run read, bound or not,
// what the grammar refused with a finding of its own, the sweep of the matrix
// seeded by the records that mark, and the sweep with no mark where one marks.
type Swept struct {
	*Prepared

	unmarked *liveness.Result
	Marks    []core.Record
	refusals []core.Refusal
	Liveness liveness.Result
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
	swept, unmarked := p.Graph.Sweeps(&liveness.SweepPolicy{
		Marked:       suppress.Marked(marks),
		Exempt:       p.Exemptions,
		TestEvidence: p.testEvidence,
		Unseen:       p.unseen,
		Mode:         p.Mode,
	})
	return &Swept{Prepared: p, Marks: marks, refusals: refusals, Liveness: swept, unmarked: unmarked}, nil
}

// suppressions is the records and refusals of the inline directives of every
// configuration, the ignore file and the baseline, in that order, which is the
// order a stale suppression is reported in. The ignore file and the baseline bind
// against the matrix inventory.
func (p *Prepared) suppressions(r *round) ([]core.Record, []core.Refusal, error) {
	records, refusals, err := suppress.Inline(p.loads.Len(), func(config int) (*suppress.Configuration, error) {
		one, err := p.loads.At(config)
		if err != nil {
			return nil, err
		}
		return &suppress.Configuration{Result: one.Result, Resolve: one.Resolve, Symbols: one.Symbols}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	baseline := suppress.Baseline
	if r != nil {
		baseline = func(_ string, symbols []inventory.Symbol) ([]core.Record, []core.Refusal, error) {
			var body bytes.Buffer
			if err := core.WriteBaseline(&body, r.rows, r.provenance, nil); err != nil {
				return nil, nil, err
			}
			return suppress.BaselineDocument(body.Bytes(), symbols)
		}
	}
	for _, read := range []struct {
		of   func(path string, symbols []inventory.Symbol) ([]core.Record, []core.Refusal, error)
		name string
	}{
		{suppress.IgnoreFile, core.IgnoreFileName},
		{baseline, core.BaselineFileName},
	} {
		held, refused, err := read.of(filepath.Join(p.Root, read.name), p.Merged.Symbols)
		if errors.Is(err, suppress.ErrUnreadable) {
			err = failure.Wrap(vocab.SetupUnreadableDocument, err)
		}
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
func (s *Swept) Retained() ([]exempt.Exemption, error) {
	writeOnly, err := findings.WriteOnlyRetained(&findings.Input{
		Config: &s.req.Config,
		Merged: s.Merged,
		Sweep:  &s.Liveness,
		Exempt: s.Exemptions,
		Per:    s.loads,
		Mode:   s.Mode,
	})
	if err != nil {
		return nil, err
	}
	if len(writeOnly) == 0 {
		return s.Liveness.Retained, nil
	}
	held := make(map[inventory.SymbolID][]exempt.Exemption)
	all := slices.Concat(s.Liveness.Retained, writeOnly)
	for i := range all {
		held[all[i].ID] = append(held[all[i].ID], all[i])
	}
	ordered := make([]exempt.Exemption, 0, len(s.Liveness.Retained)+len(writeOnly))
	for i := range s.Merged.Symbols {
		ordered = append(ordered, held[s.Merged.Symbols[i].ID]...)
	}
	return ordered, nil
}
