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
// and a caller may drop a configuration's [Result] and load it again. Once
// [Matrix.All] has loaded every configuration, unread holds the identity of each
// directory the run does not read, counted the run's counted set
// ([compiledFiles]) and leftOut each configuration's [Result.LeftOut], by its ID.
type Matrix struct {
	top     *Topology
	trees   *syntaxCache
	unread  map[dirID]bool
	leftOut map[string][]LeftOutPackage
	counted readFiles
}

// NewMatrix starts the loads of top.
func NewMatrix(top *Topology) *Matrix {
	return &Matrix{top: top, trees: newSyntaxCache(top.root)}
}

// Load loads one configuration of the matrix, as [Load] does. Once [Matrix.All]
// has run, the run's read set and counted set decide its LeftOut; before, its
// own do.
func (m *Matrix) Load(ctx context.Context, c *Configuration) (Result, error) {
	r, err := load(ctx, m.top, c, m.trees)
	switch {
	case err != nil:
	case m.unread == nil:
		r.settleAlone()
	default:
		r.LeftOut = r.settlement.leftOut(m.unread, m.counted)
		r.settlement = settlement{}
	}
	return r, err
}

// LeftOut is the [Result.LeftOut] of the configuration of id that [Matrix.All]
// built, which the run's read set and counted set decided once every
// configuration had loaded.
func (m *Matrix) LeftOut(id string) []LeftOutPackage {
	return m.leftOut[id]
}

// All loads every configuration in matrix order, which keys a configuration from
// here on, hands each one it built to built before it loads the next, with no
// LeftOut yet, and then decides each LeftOut by one read set and one counted set
// of the run ([Matrix.LeftOut], [program.4]). It returns the configurations it built and
// each [Derived] one that did not load, as an [Unbuilt]; any other failed load
// ends it with its error, a matrix whose every configuration dropped with the
// first dropped setup failure, else the first drop's error, a cancelled ctx with
// the cause, and an error from built with that error.
func (m *Matrix) All(ctx context.Context, configurations []Configuration, built func(*Result) error) ([]Configuration, []Unbuilt, error) {
	if len(configurations) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrNoConfiguration, m.top.Root)
	}
	kept := make([]Configuration, 0, len(configurations))
	var dropped []Unbuilt
	read, counted := newReadFiles(), newReadFiles()
	var settled []settlement
	for i := range configurations {
		c := &configurations[i]
		r, err := load(ctx, m.top, c, m.trees)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, nil, fmt.Errorf("load %s: %w", c.ID, context.Cause(ctx))
		case err != nil && c.Origin != Derived:
			return nil, nil, err
		case err != nil:
			dropped = append(dropped, Unbuilt{Err: err, Configuration: *c})
		default:
			read.merge(r.settlement.compiled)
			counted.merge(r.settlement.counted)
			s := r.settlement
			s.compiled, s.counted = readFiles{}, readFiles{}
			settled = append(settled, s)
			r.settlement = settlement{}
			if err := built(&r); err != nil {
				return nil, nil, err
			}
			kept = append(kept, *c)
		}
	}
	if len(kept) == 0 {
		return nil, nil, droppedError(dropped)
	}
	m.settle(read, counted, kept, settled)
	return kept, dropped, nil
}

// settle decides, by read, the read set of the run, which directories of every
// configuration's candidates the run does not read, and from that and counted,
// the run's counted set, each configuration's LeftOut: settled holds the
// settlement of each of kept.
func (m *Matrix) settle(read, counted readFiles, kept []Configuration, settled []settlement) {
	var candidates []fileCandidate
	for i := range settled {
		candidates = append(candidates, settled[i].candidates...)
	}
	m.unread, m.counted = unreadDirs(read, candidates), counted
	m.leftOut = make(map[string][]LeftOutPackage, len(kept))
	for i := range kept {
		m.leftOut[kept[i].ID] = settled[i].leftOut(m.unread, m.counted)
	}
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
