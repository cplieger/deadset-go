package load

import (
	"context"
	"errors"
	"fmt"

	"github.com/cplieger/deadset-go/v2/internal/failure"
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

// All resolves every configuration of one matrix over top, in matrix order, which
// keys a configuration from here on, and returns one result per configuration it
// built and every one it dropped. A [Derived] configuration that does not load is
// dropped as an [Unbuilt]; any other ends the run with its load's error, and a
// matrix whose every configuration dropped ends with the first dropped setup
// failure, else the first drop's error. A cancelled ctx ends it with the cause.
func All(ctx context.Context, top *Topology, configurations []Configuration) ([]Result, []Unbuilt, error) {
	if len(configurations) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrNoConfiguration, top.Root)
	}
	results := make([]Result, 0, len(configurations))
	var dropped []Unbuilt
	trees := newSyntaxCache()
	for i := range configurations {
		c := &configurations[i]
		r, err := load(ctx, top, c, trees)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, nil, fmt.Errorf("load %s: %w", c.ID, context.Cause(ctx))
		case err != nil && c.Origin != Derived:
			return nil, nil, err
		case err != nil:
			dropped = append(dropped, Unbuilt{Err: err, Configuration: *c})
		default:
			results = append(results, r)
		}
	}
	if len(results) == 0 {
		return nil, nil, droppedError(dropped)
	}
	return results, dropped, nil
}

// droppedError is the error a matrix whose every configuration was dropped ends
// with: the first setup failure among the drops, because a setup failure ends a run
// that has no configuration left to analyze, and otherwise the first drop's error.
func droppedError(dropped []Unbuilt) error {
	for _, one := range dropped {
		if failure.Setup(one.Err) {
			return one.Err
		}
	}
	return dropped[0].Err
}
