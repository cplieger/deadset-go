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

// Matrix loads the configurations of one topology over one syntax cache: a
// configuration loaded again reads the same trees and shares the same dependency
// checks, so its positions and its dependencies' types are those of its first load,
// and a caller may drop a configuration's [Result] and load it again.
type Matrix struct {
	top   *Topology
	trees *syntaxCache
}

// NewMatrix starts the loads of top.
func NewMatrix(top *Topology) *Matrix {
	return &Matrix{top: top, trees: newSyntaxCache()}
}

// Load loads one configuration of the matrix, as [Load] does.
func (m *Matrix) Load(ctx context.Context, c *Configuration) (Result, error) {
	return load(ctx, m.top, c, m.trees)
}

// All loads every configuration in matrix order, which keys a configuration from
// here on, hands each one it built to built before it loads the next, and returns
// the configurations it built and every one it dropped. A [Derived] configuration
// that does not load is dropped as an [Unbuilt]; any other ends the run with its
// load's error, and a matrix whose every configuration dropped ends with the first
// dropped setup failure, else the first drop's error. A cancelled ctx ends it with
// the cause, and an error from built ends it with that error.
func (m *Matrix) All(ctx context.Context, configurations []Configuration, built func(*Result) error) ([]Configuration, []Unbuilt, error) {
	if len(configurations) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrNoConfiguration, m.top.Root)
	}
	kept := make([]Configuration, 0, len(configurations))
	var dropped []Unbuilt
	for i := range configurations {
		c := &configurations[i]
		r, err := m.Load(ctx, c)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, nil, fmt.Errorf("load %s: %w", c.ID, context.Cause(ctx))
		case err != nil && c.Origin != Derived:
			return nil, nil, err
		case err != nil:
			dropped = append(dropped, Unbuilt{Err: err, Configuration: *c})
		default:
			if err := built(&r); err != nil {
				return nil, nil, err
			}
			kept = append(kept, *c)
		}
	}
	if len(kept) == 0 {
		return nil, nil, droppedError(dropped)
	}
	return kept, dropped, nil
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
