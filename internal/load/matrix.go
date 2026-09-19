package load

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/cplieger/deadset-go/internal/scope"
)

// ErrNoConfiguration reports a matrix holding no configuration. A run over none
// loads no package, so it has no declaration to reason about and would answer
// every question with the empty set, which reads exactly like a tree with nothing
// wrong in it.
var ErrNoConfiguration = errors.New("load: the matrix holds no configuration")

// Unbuilt is one configuration of a matrix the target does not build: the
// configuration and the error its load reported.
type Unbuilt struct {
	Err           error
	Configuration Configuration
}

// All resolves every configuration of one matrix, in the order the matrix lists
// them, and returns one result per configuration it built in that same order, which
// is the order a configuration is keyed by from here on, together with every
// configuration it dropped.
//
// derived names the configurations by identifier that the caller derived from the
// target tree rather than reading from a configuration document, which is what
// decides what a failing load means. A configuration the maintainer DECLARED that
// does not load stops the run: the error is returned with no result at all, because
// an answer computed from the configurations that did load is systematically more
// permissive than the truth, a declaration the missing configuration uses looking
// unused in every other one. A configuration the caller DERIVED is the analyzer's own
// answer about what the target builds, so one that does not load is dropped from the
// matrix and returned as an [Unbuilt] for the caller to report; the run then answers
// over the configurations the target does build rather than having no answer at all.
// Both errors are the load's own, which names the configuration and every diagnostic
// it reported.
//
// A matrix whose every configuration was dropped leaves nothing to analyse, so the
// first dropped configuration's error is returned in that case: at least one
// configuration of every matrix this analyzer builds is one it did not derive, the
// host's own, and a caller that names every configuration as derived is asking about
// a target it cannot read at all.
//
// Cancelling ctx stops the next load and every load already running.
func All(ctx context.Context, doc scope.Document, configurations []Configuration, derived []string) ([]Result, []Unbuilt, error) {
	if len(configurations) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrNoConfiguration, doc.Target.Path)
	}

	results := make([]Result, 0, len(configurations))
	var dropped []Unbuilt
	for _, c := range configurations {
		r, err := Load(ctx, doc, c)
		switch {
		case err != nil && !slices.Contains(derived, c.ID):
			return nil, nil, err
		case err != nil:
			dropped = append(dropped, Unbuilt{Err: err, Configuration: c})
		default:
			results = append(results, r)
		}
	}
	if len(results) == 0 {
		return nil, nil, dropped[0].Err
	}
	return results, dropped, nil
}
