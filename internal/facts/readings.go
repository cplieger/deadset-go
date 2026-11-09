package facts

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/deadset-go/v2/internal/resolve"
)

// Readings answers the questions that compare a type of one reading of a package
// with a type of another: each unit, test variant and consumer load checks the
// packages it compiles whole on its own, so one package path declares distinct
// types there, which go/types never finds identical, and a dependency read from
// two module versions does the same. Every comparison whose two sides can come from
// two loads goes through it; one whose sides one file's type information gives
// asks go/types.
type Readings struct {
	names  *readings
	spelt  map[types.Type]string
	stated map[statedKey]statedType
	seen   map[readingKey]*types.Package
	mu     sync.Mutex
}

type statedKey struct {
	t    types.Type
	from *types.Package
}

type statedType struct {
	t  types.Type
	ok bool
}

type readingKey struct {
	from *types.Package
	path string
}

// readingsOver answers for the packages whose files fset places.
func readingsOver(fset *token.FileSet) *Readings {
	return &Readings{
		names: newReadings(fset), spelt: make(map[types.Type]string),
		stated: make(map[statedKey]statedType), seen: make(map[readingKey]*types.Package),
	}
}

// Same reports whether a and b are one type: identical, of one spelling where two
// readings of one package's files are one package ([readings]), or identical once b
// is stated as the code declaring a's defined type sees it ([Readings.In]), or a as
// b's does.
func (r *Readings) Same(a, b types.Type) bool {
	if types.Identical(a, b) {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spelling(a) == r.spelling(b) {
		return true
	}
	if home := definedPackage(a); home != nil {
		stated, ok := r.in(b, home)
		return ok && types.Identical(a, stated)
	}
	if home := definedPackage(b); home != nil {
		stated, ok := r.in(a, home)
		return ok && types.Identical(stated, b)
	}
	return false
}

// In is t stated as the code of from sees it: every package t names replaced by
// the package of its path from imports at any depth ([Readings.Of], [Restated]).
// False where a type t names has no counterpart there.
func (r *Readings) In(t types.Type, from *types.Package) (types.Type, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.in(t, from)
}

// Implements reports whether a value of exactly t implements iface ([Satisfies]),
// iface stated as the code declaring t's defined type sees it ([Readings.For]).
func (r *Readings) Implements(t types.Type, iface *types.Interface) bool {
	return Satisfies(t, r.For(t, iface))
}

// For is iface stated as the code declaring t's defined type, or the type a pointer
// t points to, sees it, and iface itself for any other t or where it cannot be
// stated there.
func (r *Readings) For(t types.Type, iface *types.Interface) *types.Interface {
	home := definedPackage(t)
	if home == nil {
		return iface
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if stated, ok := r.in(iface, home); ok {
		if held, isInterface := stated.(*types.Interface); isInterface {
			return held
		}
	}
	return iface
}

// Of is the package of pkg's path that from imports at any depth, from itself
// included, and pkg where from reaches none: the reading of pkg the code of from
// sees.
func (r *Readings) Of(from, pkg *types.Package) *types.Package {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.of(from, pkg)
}

func (r *Readings) in(t types.Type, from *types.Package) (types.Type, bool) {
	key := statedKey{t: t, from: from}
	if held, known := r.stated[key]; known {
		return held.t, held.ok
	}
	stated, ok := Restated(t, func(p *types.Package) *types.Package { return r.of(from, p) })
	r.stated[key] = statedType{t: stated, ok: ok}
	return stated, ok
}

func (r *Readings) of(from, pkg *types.Package) *types.Package {
	if from == nil {
		return pkg
	}
	key := readingKey{from: from, path: pkg.Path()}
	if held, known := r.seen[key]; known {
		if held == nil {
			return pkg
		}
		return held
	}
	found := importedReading(from, pkg.Path())
	r.seen[key] = found
	if found == nil {
		return pkg
	}
	return found
}

// importedReading is the package of path from imports at any depth, from itself
// included, and nil where it reaches none.
func importedReading(from *types.Package, path string) *types.Package {
	seen := map[*types.Package]bool{from: true}
	queue := []*types.Package{from}
	for len(queue) > 0 {
		one := queue[0]
		queue = queue[1:]
		if one.Path() == path {
			return one
		}
		for _, imported := range one.Imports() {
			if !seen[imported] {
				seen[imported] = true
				queue = append(queue, imported)
			}
		}
	}
	return nil
}

func (r *Readings) spelling(t types.Type) string {
	held, known := r.spelt[t]
	if !known {
		held = r.names.identity(t)
		r.spelt[t] = held
	}
	return held
}

// definedPackage is the package declaring the defined type a value of t is, or a
// pointer to it points to, and nil for any other type.
func definedPackage(t types.Type) *types.Package {
	if pointer, isPointer := types.Unalias(t).(*types.Pointer); isPointer {
		t = pointer.Elem()
	}
	if named, isNamed := types.Unalias(t).(*types.Named); isNamed {
		return named.Obj().Pkg()
	}
	return nil
}

// readings names each package of one run by its path and the files it compiles,
// which fset places: a unit's own reading of a package and another unit's reading
// of it share a name, while a test variant, which compiles more files, and a module
// of the same path elsewhere do not. A package fset places no file of is named by
// its own reading.
type readings struct {
	fset  *token.FileSet
	names map[*types.Package]string
}

func newReadings(fset *token.FileSet) *readings {
	return &readings{fset: fset, names: make(map[*types.Package]string)}
}

// identity spells one type so that two types share a spelling only when they are
// identical, a type of two readings of one package being one type: the package of
// each name is spelled by its reading's name ([readings.of]), because two variants of
// one package declare two distinct types under one path.
func (r *readings) identity(t types.Type) string {
	return resolve.Spell(t, r.of)
}

// of is the name of one package.
func (r *readings) of(p *types.Package) string {
	if held, known := r.names[p]; known {
		return held
	}
	var files []string
	for child := range p.Scope().Children() {
		if r.fset == nil {
			break
		}
		if f := r.fset.File(child.Pos()); f != nil {
			files = append(files, f.Name())
		}
	}
	name := fmt.Sprintf("%p", p)
	if len(files) > 0 {
		slices.Sort(files)
		name = p.Path() + "\x00" + strings.Join(files, "\x00")
	}
	r.names[p] = name
	return name
}

// identity is [readings.identity] of t, spelt once per type.
func (r *Readings) identity(t types.Type) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spelling(t)
}
