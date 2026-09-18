package graph

import (
	"errors"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
)

// resolved is one archive's load, enumeration and resolver.
type resolved struct {
	result  *load.Result
	rs      *Resolver
	symbols []Symbol
}

// resolverOf extracts one archive, loads it for one configuration, enumerates it
// and builds the resolver over what the enumeration returned.
func resolverOf(t *testing.T, archive string) *resolved {
	t.Helper()

	dir := extract(t, archive)
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(%s): %v", archive, err)
	}
	rs, err := NewResolver(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: NewResolver(%s): %v", archive, err)
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
		if r.symbols[i].Kind == KindPackage || r.symbols[i].Kind == KindFile {
			continue
		}
		ids = append(ids, string(r.symbols[i].ID))
	}
	slices.Sort(ids)
	return ids
}

// resolvedDefs sorts every object the type checker defined, over every variant of
// the load, into the declarations the resolver named and the names it named none
// for. Every position renders, because a load carrying a declaration whose
// position does not is a resolver that was never built.
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
			if obj.Pkg() != nil && obj.Pkg().Path() == testingPath {
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
		// The resolver and the enumeration answer the same question from two
		// indexes built independently, so the set the resolver names over every
		// object the type checker defined is the enumeration's own set. A resolver
		// keyed on anything but the rendered position answers a different set.
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
				if obj.Pkg() == nil || obj.Pkg().Path() != testingPath {
					continue
				}
				checked++
				if id, held := r.rs.Object(obj); held {
					t.Errorf("Resolver.Object(%s.%s) named %q, want nothing", testingPath, obj.Name(), id)
				}
			}
		}
		if checked == 0 {
			t.Fatalf("receivers.txtar uses no object of package %s, so the case is untested", testingPath)
		}
	})
}

func TestResolverNamesNothingForAnObjectWithNoPosition(t *testing.T) {
	rs, err := NewResolver(&load.Result{Fset: token.NewFileSet()}, "/target", os.ReadFile, nil)
	if err != nil {
		t.Fatalf("Setup: NewResolver: %v", err)
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

func TestResolverResolvesADeclarationWithoutReadingItsSource(t *testing.T) {
	dir := extract(t, "receivers.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(receivers.txtar): %v", err)
	}

	reader := &countingReader{}
	rs, err := NewResolver(result, root, reader.read, symbols)
	if err != nil {
		t.Fatalf("Setup: NewResolver(receivers.txtar): %v", err)
	}
	indexed := reader.reads
	if indexed == 0 {
		t.Fatalf("NewResolver(receivers.txtar) read %d files, want at least one: it renders every declaration the load defines", indexed)
	}
	r := &resolved{result: result, rs: rs, symbols: symbols}

	// A declaration of the target and a declaration of a dependency are the two
	// answers At has, and neither reaches the reader: the index the constructor
	// built is what holds the first, and a position it does not hold is the
	// second, whether or not the target root contains it.
	held := r.objectsNamed("Catalog")
	if len(held) == 0 {
		t.Fatalf("receivers.txtar defines no Catalog, so the case is untested")
	}
	if id, named := rs.Object(held[0]); !named {
		t.Errorf("Resolver.Object(Catalog) named %q and held = false, want the declaration of the inventory", id)
	}

	foreign := r.dependencyObject()
	if foreign == nil {
		t.Fatalf("receivers.txtar uses no object of package %s, so the case is untested", testingPath)
	}
	if id, named := rs.Object(foreign); named {
		t.Errorf("Resolver.Object(%s.%s) named %q, want none", testingPath, foreign.Name(), id)
	}

	if reader.reads != indexed {
		t.Errorf("two calls to Resolver.Object read %d files, want the %d NewResolver read: resolving a position is a lookup", reader.reads-indexed, indexed)
	}
}

func TestNewResolverFailsOnAFileItCannotRead(t *testing.T) {
	dir := extract(t, "receivers.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(receivers.txtar): %v", err)
	}

	// Every file a load compiles is a file of the target, so one the reader
	// refuses is a source the run cannot reason about rather than a declaration
	// the analysis does not hold: the construction that renders it fails, and no
	// resolver exists to answer "not a symbol" for what it could not read.
	unreadable := "catalog.go"
	read := func(name string) ([]byte, error) {
		if filepath.Base(name) == unreadable {
			return nil, fs.ErrPermission
		}
		return os.ReadFile(name)
	}

	rs, err := NewResolver(result, root, read, symbols)
	if rs != nil {
		t.Errorf("NewResolver(receivers.txtar, a reader refusing %s) returned a resolver, want none", unreadable)
	}
	if !errors.Is(err, ErrSource) {
		t.Errorf("NewResolver(receivers.txtar, a reader refusing %s) error = %v, want one satisfying errors.Is(err, %v)", unreadable, err, ErrSource)
	}
}

func TestNewResolverRefusesALoadMissingWhatTheRenderingNeeds(t *testing.T) {
	cases := map[string]struct {
		result *load.Result
		read   ReadFile
	}{
		"no result":   {read: os.ReadFile},
		"no file set": {result: &load.Result{}, read: os.ReadFile},
		"no reader":   {result: &load.Result{Fset: token.NewFileSet()}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			rs, err := NewResolver(test.result, "/target", test.read, nil)
			if rs != nil {
				t.Errorf("NewResolver(%s) returned a resolver, want none", name)
			}
			if !errors.Is(err, ErrIncompleteLoad) {
				t.Errorf("NewResolver(%s) error = %v, want one wrapping %v", name, err, ErrIncompleteLoad)
			}
		})
	}
}
