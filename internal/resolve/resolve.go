// Package resolve answers which declaration of one configuration's inventory a
// types.Object names, for every pass over that configuration.
package resolve

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"golang.org/x/tools/go/packages"
)

// Resolver answers which declaration of the inventory a position or an object
// names, and renders a position the way a report carries it. An object resolves
// through the position its declaration is written at: a package and its test
// variant share it, and a second parse of a target file writes the declaration at
// the same byte of the same file. Resolving renders nothing and never fails;
// rendering fails with [inventory.ErrSource] wherever it happens.
type Resolver struct {
	inv       *inventory.Inventory
	pos       *inventory.Positions
	written   map[inventory.Offset]inventory.SymbolID
	paths     map[string]bool           // the import paths of the target's packages, the only ones declaring a symbol
	dirs      map[*types.Package]string // TypeKey's directory of each package, on first use
	consumers []consumerPositions
	decls     []ast.Decl // every top-level declaration, by position
	holders   []extent   // every range declaring a symbol of the inventory, by position
}

// extent is one declaration's source range, with the symbol it declares.
type extent struct {
	id       inventory.SymbolID
	from, to token.Pos
}

// consumerPositions renders the files of one loaded consumer against its own root,
// the directory of its module, or against no root where its packages name none.
type consumerPositions struct {
	pos    *inventory.Positions
	id     string
	rooted bool
}

// New prepares the resolver of one loaded configuration over the inventory
// [inventory.Of] built from the same result. A declaration the type checker
// defined in a target file whose position does not render ends the construction
// with [inventory.ErrSource], the one written first: such a file is a source no
// pass can reason about, whether or not the inventory holds the declaration.
func New(r *load.Result, inv *inventory.Inventory) (*Resolver, error) {
	rs := &Resolver{
		inv:     inv,
		pos:     inv.Positions(),
		written: make(map[inventory.Offset]inventory.SymbolID, len(inv.Symbols)),
		paths:   make(map[string]bool),
	}
	for _, consumer := range r.Referencing() {
		held := consumerPositions{id: consumer.ID, pos: rs.pos.Under("")}
		if module := consumerModule(&consumer); module != nil {
			held = consumerPositions{id: consumer.ID, pos: rs.pos.Under(module.Dir), rooted: true}
		}
		rs.consumers = append(rs.consumers, held)
	}
	for pos, id := range inv.Sites() {
		rs.written[rs.pos.Offset(pos)] = id
	}
	for _, p := range r.Packages {
		rs.paths[p.PkgPath] = true
	}
	rs.decls, rs.holders = declarationsOf(r.Packages, inv)
	if err := rs.renders(r.Packages); err != nil {
		return nil, err
	}
	return rs, nil
}

// renders returns the failure written first among the declarations the packages'
// type checker defined whose position does not render.
func (rs *Resolver) renders(pkgs []*packages.Package) error {
	var failure error
	var failed token.Position
	for pos := range defined(pkgs) {
		if _, err := rs.pos.Render(pos); err != nil {
			if at := rs.pos.Fset.Position(pos); failure == nil || inventory.ByPosition(at, failed) < 0 {
				failure, failed = err, at
			}
		}
	}
	return failure
}

// defined is the position of every object the packages' type checker defined, once
// however many variants define it.
func defined(pkgs []*packages.Package) map[token.Pos]bool {
	at := make(map[token.Pos]bool)
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		for _, obj := range p.TypesInfo.Defs {
			if obj != nil {
				at[obj.Pos()] = true
			}
		}
	}
	return at
}

// consumerModule returns the module one consumer's packages belong to, which is
// what its files are rendered relative to.
func consumerModule(consumer *load.Consumer) *packages.Module {
	for _, pkg := range consumer.Packages {
		if pkg.Module != nil && pkg.Module.Dir != "" {
			return pkg.Module
		}
	}
	return nil
}

// Object returns the identifier of the declaration obj is declared at. A nil
// object, and one declared outside the target's packages or at a position no
// symbol of the inventory holds, name none. An object of a second parse of a target
// file, which a consumer's load or another unit's reads into the file set, names
// the declaration written at the same byte of that file.
func (rs *Resolver) Object(obj types.Object) (inventory.SymbolID, bool) {
	if obj == nil || obj.Pkg() == nil || !rs.paths[obj.Pkg().Path()] {
		return "", false
	}
	if id, held := rs.inv.At(obj.Pos()); held || !obj.Pos().IsValid() {
		return id, held
	}
	id, held := rs.written[rs.pos.Offset(obj.Pos())]
	return id, held
}

// declarationsOf indexes every file the packages parse, once: each top-level
// declaration, and each range declaring a symbol of the inventory ([holdersOf]).
func declarationsOf(pkgs []*packages.Package, inv *inventory.Inventory) (decls []ast.Decl, holders []extent) {
	seen := make(map[*ast.File]bool)
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			if seen[f] {
				continue
			}
			seen[f] = true
			for _, decl := range f.Decls {
				decls = append(decls, decl)
				holders = append(holders, holdersOf(decl, inv)...)
			}
		}
	}
	slices.SortFunc(decls, func(a, b ast.Decl) int { return cmp.Compare(a.Pos(), b.Pos()) })
	slices.SortFunc(holders, func(a, b extent) int { return cmp.Compare(a.from, b.from) })
	return decls, holders
}

// holdersOf is each range of one top-level declaration that declares a symbol of
// the inventory: a function, a type specification, or a value specification by the
// first name it declares that the inventory holds.
func holdersOf(decl ast.Decl, inv *inventory.Inventory) []extent {
	var held []extent
	switch d := decl.(type) {
	case *ast.FuncDecl:
		held = appendHolder(held, inv, d, d.Name)
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			held = append(held, specHolder(spec, inv)...)
		}
	}
	return held
}

// specHolder is [holdersOf] for one specification of a declaration group.
func specHolder(spec ast.Spec, inv *inventory.Inventory) []extent {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return appendHolder(nil, inv, s, s.Name)
	case *ast.ValueSpec:
		for _, name := range s.Names {
			if one := appendHolder(nil, inv, s, name); one != nil {
				return one
			}
		}
	}
	return nil
}

// appendHolder appends node's range where name is no blank and declares a symbol
// of the inventory.
func appendHolder(held []extent, inv *inventory.Inventory, node ast.Node, name *ast.Ident) []extent {
	if id, ok := inv.At(name.Pos()); ok && name.Name != "_" {
		return append(held, extent{id: id, from: node.Pos(), to: node.End()})
	}
	return held
}

// Holder returns the identifier of the declaration of the inventory whose source
// holds pos: the function, the type specification or the value specification
// [declarationsOf] indexes. A position no such declaration holds names none.
func (rs *Resolver) Holder(pos token.Pos) (inventory.SymbolID, bool) {
	at := enclosing(rs.holders, pos, func(e extent) (token.Pos, token.Pos) { return e.from, e.to })
	if at < 0 {
		return "", false
	}
	return rs.holders[at].id, true
}

// Declaration returns the top-level declaration whose source holds pos, a whole
// group for a position inside one, and nil where none does.
func (rs *Resolver) Declaration(pos token.Pos) ast.Decl {
	at := enclosing(rs.decls, pos, func(d ast.Decl) (token.Pos, token.Pos) { return d.Pos(), d.End() })
	if at < 0 {
		return nil
	}
	return rs.decls[at]
}

// enclosing is the index of the range of spans, ordered by start and disjoint, that
// holds pos, and -1 where none does.
func enclosing[S any](spans []S, pos token.Pos, bounds func(S) (from, to token.Pos)) int {
	at, exact := slices.BinarySearchFunc(spans, pos, func(s S, p token.Pos) int {
		from, _ := bounds(s)
		return cmp.Compare(from, p)
	})
	if exact {
		return at
	}
	if at > 0 {
		if _, to := bounds(spans[at-1]); pos < to {
			return at - 1
		}
	}
	return -1
}

// At returns the identifier of the declaration this configuration's parse writes
// at pos ([inventory.Inventory.At]).
func (rs *Resolver) At(pos token.Pos) (inventory.SymbolID, bool) {
	return rs.inv.At(pos)
}

// Inside reports whether pos is written in a file under the target root, which is
// what Render can render. It reads no source.
func (rs *Resolver) Inside(pos token.Pos) bool {
	q := rs.pos.Fset.Position(pos)
	return q.IsValid() && rs.pos.Relative(q.Filename) != ""
}

// TypeKey spells t as one type's identity across the variants of a load: the
// spelling types.TypeString gives, each package qualified by its import path and
// the directory its declarations are read from. Two readings of one package's
// files spell alike, and two packages of one import path read from two
// directories, two modules of the target declaring one module path among them,
// do not.
func (rs *Resolver) TypeKey(t types.Type) string {
	return types.TypeString(t, rs.PackageKey)
}

// PackageKey is the qualifier [Resolver.TypeKey] spells p with.
func (rs *Resolver) PackageKey(p *types.Package) string {
	return p.Path() + "\x00" + rs.packageDir(p)
}

// packageDir is the directory of the file declaring the first object of p's scope
// that has a position, and the empty string for a package declaring none.
func (rs *Resolver) packageDir(p *types.Package) string {
	if dir, known := rs.dirs[p]; known {
		return dir
	}
	dir := ""
	scope := p.Scope()
	for _, name := range scope.Names() {
		if at := rs.pos.Fset.PositionFor(scope.Lookup(name).Pos(), false); at.IsValid() {
			dir = filepath.Dir(at.Filename)
			break
		}
	}
	if rs.dirs == nil {
		rs.dirs = make(map[*types.Package]string)
	}
	rs.dirs[p] = dir
	return dir
}

// Positions is the renderer of the target's files, the inventory's.
func (rs *Resolver) Positions() *inventory.Positions { return rs.pos }

// Consumer is the renderer of the files of the consumer at place i of
// [load.Result.Referencing], the one [Resolver.Site] renders its evidence with.
func (rs *Resolver) Consumer(i int) *inventory.Positions { return rs.consumers[i].pos }

// Render converts pos into the position a report carries: a path relative to the
// target root with the solidus as separator, and a column counting UTF-16 code
// units. Every file it reads it reads once.
func (rs *Resolver) Render(pos token.Pos) (token.Position, error) {
	return rs.pos.Render(pos)
}

// Site renders the position a piece of evidence is written at: against the target
// root for a file of the target, and against a loaded consumer's root for a file of
// that consumer, whose module path it returns. A consumer whose root lies under the
// target root holds the files under its own.
func (rs *Resolver) Site(pos token.Pos) (consumer string, at token.Position, err error) {
	name := rs.pos.Fset.Position(pos).Filename
	held, owner := rs.pos, ""
	if rs.pos.Relative(name) == "" {
		held = nil
	}
	for _, c := range rs.consumers {
		if c.rooted && c.pos.Relative(name) != "" && (held == nil || len(c.pos.Root) > len(held.Root)) {
			held, owner = c.pos, c.id
		}
	}
	if held == nil {
		held = rs.pos
	}
	at, err = held.Render(pos)
	return owner, at, err
}
