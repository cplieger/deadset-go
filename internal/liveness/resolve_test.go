package liveness

import (
	"errors"
	"go/token"
	"go/types"
	"os"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
)

// resolved is one archive's load, enumeration and resolver.
type resolved struct {
	result  *load.Result
	rs      *resolve.Resolver
	symbols []inventory.Symbol
}

// resolverOf extracts one archive, loads it for one configuration, enumerates it
// and builds the resolver over what the enumeration returned.
func resolverOf(t *testing.T, archive string) *resolved {
	t.Helper()

	dir := extract(t, archive)
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, rs, err := inventoryOf(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: inventoryOf(%s): %v", archive, err)
	}
	return &resolved{result: result, rs: rs, symbols: symbols}
}

// declared lists the identifiers of the declarations the enumeration holds that a
// types.Object can name, which is every kind but a package and a file: an import
// names a package at the import spec rather than at the package clause, and no
// object is a file.
func (r *resolved) declared() []string {
	ids := make([]string, 0, len(r.symbols))
	for i := range r.symbols {
		if r.symbols[i].Kind == inventory.KindPackage || r.symbols[i].Kind == inventory.KindFile {
			continue
		}
		ids = append(ids, string(r.symbols[i].ID))
	}
	slices.Sort(ids)
	return ids
}

// resolvedDefs sorts every object the type checker defined, over every variant of
// the load, into the declarations the resolver named and the names it named none
// for.
func (r *resolved) resolvedDefs() (named, none []string) {
	for _, p := range r.result.Packages {
		for _, obj := range p.TypesInfo.Defs {
			if obj == nil {
				continue
			}
			if id, held := r.rs.Object(obj); held {
				named = append(named, string(id))
				continue
			}
			none = append(none, obj.Name())
		}
	}
	for _, names := range [][]string{named, none} {
		slices.Sort(names)
	}
	return slices.Compact(named), slices.Compact(none)
}

// dependencyObject returns one object of package testing the load uses, which is
// a declaration the target does not hold and the target root does not contain.
func (r *resolved) dependencyObject() types.Object {
	for _, p := range r.result.Packages {
		for _, obj := range p.TypesInfo.Uses {
			if obj.Pkg() != nil && obj.Pkg().Path() == "testing" {
				return obj
			}
		}
	}
	return nil
}

// objectsNamed returns every object of every variant that carries one name, which
// for a file several variants type-check is one object per variant.
func (r *resolved) objectsNamed(name string) []types.Object {
	var found []types.Object
	for _, p := range r.result.Packages {
		for ident, obj := range p.TypesInfo.Defs {
			if obj != nil && ident.Name == name {
				found = append(found, obj)
			}
		}
	}
	return found
}

func TestResolverOverALoadedConfiguration(t *testing.T) {
	r := resolverOf(t, "receivers.txtar")
	named, none := r.resolvedDefs()

	t.Run("it names every declaration the inventory holds", func(t *testing.T) {
		// The set the resolver names over every object the type checker defined is
		// the enumeration's own set: an object resolves through the position its
		// declaration is written at, and nothing else.
		if want := r.declared(); !slices.Equal(named, want) {
			t.Errorf("Resolver.Object over every definition of receivers.txtar named %v, want %v", named, want)
		}
	})

	t.Run("it names nothing the inventory does not hold", func(t *testing.T) {
		// A receiver, a parameter and a local are declarations the enumeration
		// does not hold, because the inventory is what decides which declarations
		// the analysis reasons about. The receiver and the local are both spelled
		// c, and both parameters named name resolve to nothing.
		if want := []string{"c", "name", "t"}; !slices.Equal(none, want) {
			t.Errorf("Resolver.Object over every definition of receivers.txtar named none for %v, want %v", none, want)
		}
	})

	t.Run("it names one declaration for every variant that type-checks it", func(t *testing.T) {
		// A package and its in-package test variant type-check catalog.go
		// independently and produce distinct objects for one declaration, so a
		// resolver keyed on object identity would answer two declarations here.
		objects := r.objectsNamed("Catalog")
		if len(objects) != 2 {
			t.Fatalf("receivers.txtar defines Catalog in %d variants, want 2", len(objects))
		}
		first, held := r.rs.Object(objects[0])
		second, alsoHeld := r.rs.Object(objects[1])
		if !held || !alsoHeld || first != second {
			t.Errorf("Resolver.Object over the two Catalog objects named %q and %q, want one identifier twice", first, second)
		}
	})

	t.Run("it names no object declared outside the target root", func(t *testing.T) {
		// An object of a module the load compiled but the target does not hold is
		// a declaration this analysis does not reason about, and its position is
		// outside the root the rendering is relative to.
		checked := 0
		for _, p := range r.result.Packages {
			for _, obj := range p.TypesInfo.Uses {
				if obj.Pkg() == nil || obj.Pkg().Path() != "testing" {
					continue
				}
				checked++
				if id, held := r.rs.Object(obj); held {
					t.Errorf("Resolver.Object(%s.%s) named %q, want nothing", "testing", obj.Name(), id)
				}
			}
		}
		if checked == 0 {
			t.Fatalf("receivers.txtar uses no object of package %s, so the case is untested", "testing")
		}
	})
}

func TestResolverNamesNothingForAnObjectWithNoPosition(t *testing.T) {
	empty := &load.Result{Fset: token.NewFileSet()}
	inv, err := inventory.Of(empty, "/target", os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: inventory.Of: %v", err)
	}
	rs, err := resolve.New(empty, inv)
	if err != nil {
		t.Fatalf("Setup: resolve.New: %v", err)
	}

	cases := map[string]types.Object{
		"no object":                  nil,
		"a predeclared type":         types.Universe.Lookup("error"),
		"a predeclared constant":     types.Universe.Lookup("true"),
		"a predeclared function":     types.Universe.Lookup("append"),
		"a predeclared zero pointer": types.Universe.Lookup("nil"),
	}
	for name, obj := range cases {
		t.Run(name, func(t *testing.T) {
			if id, held := rs.Object(obj); held {
				t.Errorf("Resolver.Object(%s) named %q, want nothing", name, id)
			}
		})
	}
	if _, err := rs.Render(token.NoPos); err == nil {
		t.Errorf("Resolver.Render(token.NoPos) returned no error, want one")
	}
}

// countingReader reads a file the way os.ReadFile does and counts the reads, so a
// test can tell a lookup from a read.
type countingReader struct{ reads int }

func (c *countingReader) read(name string) ([]byte, error) {
	c.reads++
	return os.ReadFile(name)
}

func TestResolverResolvesWithoutReadingASource(t *testing.T) {
	dir := extract(t, "receivers.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	reader := &countingReader{}
	symbols, rs, err := inventoryOf(result, root, reader.read)
	if err != nil {
		t.Fatalf("Setup: inventoryOf(receivers.txtar): %v", err)
	}
	enumerated := reader.reads
	r := &resolved{result: result, rs: rs, symbols: symbols}

	held := r.objectsNamed("Catalog")
	if len(held) == 0 {
		t.Fatalf("receivers.txtar defines no Catalog, so the case is untested")
	}
	if id, named := rs.Object(held[0]); !named {
		t.Errorf("Resolver.Object(Catalog) named %q and held = false, want the declaration of the inventory", id)
	}
	foreign := r.dependencyObject()
	if foreign == nil {
		t.Fatalf("receivers.txtar uses no object of package %s, so the case is untested", "testing")
	}
	if id, named := rs.Object(foreign); named {
		t.Errorf("Resolver.Object(%s.%s) named %q, want none", "testing", foreign.Name(), id)
	}
	if reader.reads != enumerated {
		t.Errorf("building the resolver and two calls to Resolver.Object read %d files, want none: resolving is a lookup", reader.reads-enumerated)
	}
}

func TestResolverRefusesATargetFileWhosePositionsDoNotRender(t *testing.T) {
	dir := extract(t, "unrendered-local.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	inv, err := inventory.Of(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: inventory.Of(unrendered-local.txtar): %v", err)
	}

	if _, err := resolve.New(result, inv); !errors.Is(err, inventory.ErrSource) {
		t.Errorf("resolve.New(unrendered-local.txtar) returned %v, want %v: the local x renders outside short.txt", err, inventory.ErrSource)
	}
}
