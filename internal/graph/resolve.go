package graph

import (
	"fmt"
	"go/token"
	"go/types"

	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// Resolver answers which declaration of the inventory is written at a position,
// and renders a position the way a report carries it.
//
// A declaration is identified by where it is written, so a types.Object resolves
// through its own position and nothing else: a package and its in-package test
// variant produce distinct objects for one declaration and share that position.
// Only a declaration the inventory holds is resolved, because the inventory is
// what decides which declarations the analysis reasons about.
type Resolver struct {
	pos     *positions
	symbols map[token.Pos]SymbolID
}

// NewResolver prepares the resolver of one loaded configuration, rendering
// against targetRoot and reading source through read. It indexes every
// declaration the type checker defined over the load, so resolving one is a
// lookup that reads nothing; a declaration whose position does not render ends
// the construction, because every file a load compiles is a file of the target
// and one that cannot be read is a source the run cannot reason about. It refuses
// a result missing the file set or the reader the rendering needs.
func NewResolver(r *load.Result, targetRoot string, read ReadFile, symbols []Symbol) (*Resolver, error) {
	if r == nil || r.Fset == nil {
		return nil, fmt.Errorf("%w: no file set", ErrIncompleteLoad)
	}
	if read == nil {
		return nil, fmt.Errorf("%w: no source reader", ErrIncompleteLoad)
	}

	rs := &Resolver{
		pos:     newPositions(r.Fset, targetRoot, read),
		symbols: make(map[token.Pos]SymbolID, len(symbols)),
	}
	if err := rs.index(r.Packages, symbols); err != nil {
		return nil, err
	}
	return rs, nil
}

// index keys the identifier of every declaration the inventory holds by the
// position it is written at, rendering the position of every declaration the type
// checker defined over the load and keeping the ones the inventory holds. A
// declaration a variant defines again shares that position, so one declaration is
// indexed once whatever the number of variants that type-check it.
//
// The map the type checker records definitions in has no order, so the failure
// returned is the one written first, whatever order the walk met it in.
func (rs *Resolver) index(pkgs []*packages.Package, symbols []Symbol) error {
	sites := make(map[site]SymbolID, len(symbols))
	for i := range symbols {
		s := &symbols[i]
		sites[site{file: s.Pos.Filename, line: s.Pos.Line, col: s.Pos.Column}] = s.ID
	}

	var failure error
	var failed token.Position
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		for _, obj := range p.TypesInfo.Defs {
			at, err := rs.hold(obj, sites)
			if err != nil && (failure == nil || ByPosition(at, failed) < 0) {
				failure, failed = err, at
			}
		}
	}
	return failure
}

// hold keys obj's identifier by the position obj is declared at, when the
// inventory holds a declaration at that position. Nothing identifies an
// identifier that denotes no object, a declaration already indexed by another
// variant, or one the inventory does not hold, and none of the three is a
// failure. A position that does not render is, and it comes back unrendered with
// the error that says why.
func (rs *Resolver) hold(obj types.Object, sites map[site]SymbolID) (token.Position, error) {
	if obj == nil {
		return token.Position{}, nil
	}
	if _, indexed := rs.symbols[obj.Pos()]; indexed {
		return token.Position{}, nil
	}
	position, err := rs.pos.render(obj.Pos())
	if err != nil {
		return rs.pos.fset.Position(obj.Pos()), err
	}
	if id, held := sites[site{file: position.Filename, line: position.Line, col: position.Column}]; held {
		rs.symbols[obj.Pos()] = id
	}
	return token.Position{}, nil
}

// Object returns the identifier of the declaration obj is declared at. A nil
// object, and one declared outside the target root or at a position no symbol of
// the inventory holds, name none.
func (rs *Resolver) Object(obj types.Object) (SymbolID, bool) {
	if obj == nil {
		return "", false
	}
	return rs.At(obj.Pos())
}

// At returns the identifier of the declaration written at pos. A position no
// target declaration is written at names none, which an invalid position, a
// declaration of another module and a receiver, a parameter or a local of the
// target all are, and answering so is a lookup that reads no source.
func (rs *Resolver) At(pos token.Pos) (SymbolID, bool) {
	id, held := rs.symbols[pos]
	return id, held
}

// Render converts pos into the position a report carries: a path relative to the
// target root with the solidus as separator, and a column counting UTF-16 code
// units. Every file it reads it reads once.
func (rs *Resolver) Render(pos token.Pos) (token.Position, error) {
	return rs.pos.render(pos)
}
