package exempt_test

import (
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

func init() {
	exempt.Sweeper = func(in *exempt.Input, rooted roots.RootOptions, held []exempt.Exemption) ([]exempt.SweptCandidate, error) {
		refs, _, err := facts.References(in.Result, in.Resolve, in.Symbols, roots.TestClasses{})
		if err != nil {
			return nil, err
		}
		seeds, _ := roots.Roots(in.Result, in.Resolve, in.Symbols, rooted)
		r := liveness.New(in.Symbols, refs, seeds).Sweep(&liveness.SweepPolicy{Exempt: held})
		swept := make([]exempt.SweptCandidate, 0, len(r.Candidates))
		for _, c := range r.Candidates {
			swept = append(swept, exempt.SweptCandidate{ID: c.ID, Relation: c.Relation.String(), ProductionRefs: c.ProductionRefs, TestRefs: c.TestRefs})
		}
		return swept, nil
	}
}
