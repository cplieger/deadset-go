package load

import (
	"context"
	"errors"
	"fmt"

	"github.com/cplieger/deadset-go/internal/scope"
)

// ErrNoConfiguration reports a matrix holding no configuration. A run over none
// loads no package, so it has no declaration to reason about and would answer
// every question with the empty set, which reads exactly like a tree with nothing
// wrong in it.
var ErrNoConfiguration = errors.New("load: the matrix holds no configuration")

// All resolves every configuration of one matrix, in the order the matrix lists
// them, and returns one result per configuration in that same order, which is the
// order a configuration is keyed by from here on.
//
// It stops at the first configuration that does not load and returns that
// configuration's error with no result at all. An answer computed from the
// configurations that did load is systematically more permissive than the truth,
// because a declaration the missing configuration uses looks unused in every
// other one, so a caller is left with nothing to compute from rather than with a
// shorter matrix it could mistake for the whole one. The error is the load's own,
// which names the configuration and every diagnostic it reported.
//
// Cancelling ctx stops the next load and every load already running.
func All(ctx context.Context, doc scope.Document, configurations []Configuration) ([]Result, error) {
	if len(configurations) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoConfiguration, doc.Target.Path)
	}

	results := make([]Result, 0, len(configurations))
	for _, c := range configurations {
		r, err := Load(ctx, doc, c)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}
