package load

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/cplieger/deadset-go/v2/internal/memory"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// Result is one configuration's loaded program. Nothing changes it once [Load]
// returns it.
type Result struct {
	Fset *token.FileSet // one FileSet for the whole configuration, shared by every configuration one [Matrix] loads

	// Packages holds the target's packages and the test variants that
	// type-check their files again, and never a synthesized test binary, so
	// every file a stage of the analysis meets is a file of the target.
	Packages []*packages.Package

	// Units holds one entry per unit of the topology that loaded, in unit order,
	// each with its share of Packages. A reference from one unit to another is to a
	// dependency of the first loaded from the second's directory, which is a
	// second reading of the second's files.
	Units []Unit

	// Nested holds one entry per module that is no declared consumer and whose
	// references count as a declared consumer's do: each nested module of the
	// topology, then each unit's packages below an underscore directory, then the
	// packages each unit imports from outside its packages, then each workspace
	// reader.
	Nested []Consumer

	// Dropped holds every walked unit, nested module and reader that did not load.
	// A unit one of them requires has it as a consumer the run did not analyze.
	Dropped []Dropped

	// Consumers holds one entry per declared consumer, in the order the scope
	// declares them. A consumer's packages are kept apart from the target's
	// because the two answer different questions: the declarations a run reports
	// on are the target's alone, while the references it counts come from every
	// module it loaded.
	Consumers []Consumer

	// Programs holds the target's files run on their own under the ignore tag, and
	// the scripts of every dropped module below the target and of every reader a
	// file no toolchain may run under dropped, which no package of the target
	// compiles and every stage but the root detection leaves alone.
	Programs []programFile

	// Hidden holds every entry with a source file's name that every toolchain read
	// as absent in a directory holding a listed package or in a package directory of
	// a unit, and Obscured the directory of each, and of each Go file there the run
	// cannot open, whose packages hold code the analysis does not see ([program.6]).
	Hidden   []string
	Obscured []string

	// ExcludedByCgo holds the target-relative paths, forward slashes, of the
	// files the toolchain ignored for importing "C" that the opaque-C check could
	// not read either. A file the check did read is a file of its package like any
	// other and is not in this list.
	ExcludedByCgo []string

	// declared maps the import path of every package of Packages to the one
	// directory declaring it, and to nothing where two do.
	declared map[string]string

	// root is the target root the load names the files it checks whole by
	// ([Result.Respell]).
	root scope.Root

	Configuration Configuration
}

// Unit is one unit of the target a load read: a module, from its own directory
// with its own module file and build list, or the target itself where discovery
// finds no module to read.
type Unit struct {
	Dir  string // the directory the unit's toolchain runs from, at or below the target root
	Path string // the module path its module file declares, empty where none governs it

	// Packages holds the unit's own packages and test variants, a subset of
	// [Result.Packages] in the same order.
	Packages []*packages.Package

	// UnanalysedConsumer reports a unit whose module a [Dropped] module's module
	// file requires, or any unit of a tree with a path the run cannot read
	// ([Topology.Unread]) or an entry it hid ([Result.Hidden]), so code the run did
	// not analyze may import its exports.
	UnanalysedConsumer bool
}

// Dropped is one module the configuration did not load: its directory and the
// error that dropped it.
type Dropped struct {
	Err error
	Dir string

	requires []string // the modules its module file requires

	// Unreadable reports that its module file, or the workspace file governing
	// Dir, could not be read, so it may require any unit and every export of the
	// target is possible ([Result.markUnanalysedConsumers]).
	Unreadable bool

	irregular bool // whether a file no toolchain may run under dropped it
	offline   bool // whether its scripts' toolchain would read such a file
}

// droppedOf is m dropped with err.
func droppedOf(m *discoveredModule, err error) Dropped {
	d := Dropped{
		Dir: m.Dir, Err: err, Unreadable: m.File == nil || m.FileErr != nil || m.WorkspaceErr != nil,
		irregular: irregular(m.Refused), offline: m.scriptsBlock,
	}
	if !d.Unreadable {
		d.requires = m.File.required()
	}
	return d
}

// markUnanalysedConsumers flags every unit whose module the module file of a
// dropped module requires, and every unit where that file cannot be read, because
// then it may require any of them, or where unread holds, because code the tree
// holds where the run cannot read it may import any of them.
func (r *Result) markUnanalysedConsumers(unread bool) {
	for i := range r.Units {
		r.Units[i].UnanalysedConsumer = unread
	}
	for _, dropped := range r.Dropped {
		for i := range r.Units {
			if dropped.Unreadable || slices.Contains(dropped.requires, r.Units[i].Path) {
				r.Units[i].UnanalysedConsumer = true
			}
		}
	}
}

// Referencing is every loaded consumer whose references the run counts: each declared
// one, then each nested one.
func (r *Result) Referencing() []Consumer {
	return slices.Concat(r.Consumers, r.Nested)
}

// Respell is the directory at path as the target root spells it where the root
// holds it ([scope.Root.Respell]), and path itself elsewhere: the directory the
// load names the files of a module it checks whole below, so a consumer's
// directory the root holds through a link still holds its files.
func (r *Result) Respell(path string) string { return r.root.Respell(path) }

// Unit returns r narrowed to the unit rooted at dir: its packages alone, so an
// answer about one module's build list or imports reads that module's load. It
// returns nil where r holds no such unit.
func (r *Result) Unit(dir string) *Result {
	unit := scope.RootOf(dir)
	for i := range r.Units {
		if !unit.Is(r.Units[i].Dir) {
			continue
		}
		narrowed := *r
		narrowed.Packages = r.Units[i].Packages
		narrowed.Units = r.Units[i : i+1]
		return &narrowed
	}
	return nil
}

// PackageID names one package of a load by its import path and the directory its
// files are read from, which tell apart two modules of the target declaring one
// module path.
type PackageID struct {
	Path string
	Dir  string
}

// ID is the identity of any package the load holds, [Result.idAt] of its import
// path and the directory of its first file.
func (r *Result) ID(p *packages.Package) PackageID {
	return r.idAt(p.PkgPath, packageDir(p))
}

// idAt is the identity of the package of import path pkgPath read from dir: the
// target's package of that path where one directory of the target declares it, so
// a copy a module builds the target from reads as the target's package, and
// otherwise the package of that path in dir.
func (r *Result) idAt(pkgPath, dir string) PackageID {
	if declared, held := r.declared[pkgPath]; held && declared != "" {
		return PackageID{Path: pkgPath, Dir: declared}
	}
	return PackageID{Path: pkgPath, Dir: dir}
}

// packageDir is the directory of p's first file, empty for a package with none.
func packageDir(p *packages.Package) string {
	if len(p.GoFiles) == 0 {
		return ""
	}
	return filepath.Dir(p.GoFiles[0])
}

// declaredDirs maps the import path of every package of pkgs with a file to its
// directory, and to nothing where two directories declare it.
func declaredDirs(pkgs []*packages.Package) map[string]string {
	dirs := make(map[string]string)
	for _, p := range pkgs {
		dir := packageDir(p)
		if dir == "" {
			continue
		}
		if held, seen := dirs[p.PkgPath]; !seen {
			dirs[p.PkgPath] = dir
		} else if held != "" && !scope.Same(held, dir) {
			dirs[p.PkgPath] = ""
		}
	}
	return dirs
}

// Source is one package outside the program read from its source: the files the
// configuration compiles, parsed into Fset, and what checking them against the
// load's own packages recorded. A type error leaves what it could not check out.
type Source struct {
	Fset  *token.FileSet
	Info  *types.Info
	Files []*ast.File
}

// Sources reads the [Source] of packages of one load, each once outside memory
// pressure. Its reads are the largest allocation of a run, so it lives with its
// caller, never with the [Result]: dropping it frees every tree and type record it
// read, and pressure drops the reads it holds ([memory.OnPressureOf]). names is the
// root whose names the load gave its files ([scope.Root.Name]).
type Sources struct {
	byTypes map[*types.Package]*packages.Package
	read    map[*types.Package]*sourceRead
	pressed func() bool
	names   scope.Root
	mu      sync.Mutex
}

// sourceRead is one package's read, which concurrent readers of the package wait
// for rather than read again.
type sourceRead struct {
	source *Source
	ready  chan struct{}
}

// Sources starts a reader over every package r holds, a consumer's and a
// dependency's included, by its types.
func (r *Result) Sources() *Sources {
	c := &Sources{
		names:   r.root,
		byTypes: make(map[*types.Package]*packages.Package),
		read:    make(map[*types.Package]*sourceRead),
		pressed: memory.Pressed,
	}
	roots := slices.Clone(r.Packages)
	for _, consumer := range r.Referencing() {
		roots = append(roots, consumer.Packages...)
	}
	packages.Visit(roots, nil, func(p *packages.Package) {
		if p.Types != nil {
			c.byTypes[p.Types] = p
		}
	})
	memory.OnPressureOf(c, (*Sources).drop)
	return c
}

// Read returns the source of the loaded package pkg, and false where the load
// holds no package of it or none of its files parses. Two reads of one package
// may return two readings, each checked against the same loaded packages.
func (c *Sources) Read(pkg *types.Package) (*Source, bool) {
	c.mu.Lock()
	if held, known := c.read[pkg]; known {
		c.mu.Unlock()
		<-held.ready
		return held.source, held.source != nil
	}
	held := &sourceRead{ready: make(chan struct{})}
	if !c.pressed() {
		c.read[pkg] = held
	}
	p := c.byTypes[pkg]
	c.mu.Unlock()
	if p != nil {
		held.source = readSource(c.names, p)
	}
	close(held.ready)
	return held.source, held.source != nil
}

// drop forgets every source c read, without waiting for a read in progress.
func (c *Sources) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.read)
}

// readSource parses and type-checks one package's compiled files, each by the name
// names gave it, into a file set of its own, so the line tables go with the Source;
// its imports resolve to the packages the load holds.
func readSource(names scope.Root, p *packages.Package) *Source {
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range p.CompiledGoFiles {
		src, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if f, err := names.Parse(fset, name, src, parser.SkipObjectResolution); err == nil && f.Name.Name == p.Name {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil
	}
	info := &types.Info{
		Defs:      make(map[*ast.Ident]types.Object),
		Uses:      make(map[*ast.Ident]types.Object),
		Types:     make(map[ast.Expr]types.TypeAndValue),
		Instances: make(map[*ast.Ident]types.Instance),
	}
	conf := types.Config{Importer: importer{pkg: p}, Error: func(error) {}}
	// The Error function above drops every error: a package the configuration
	// cannot fully check still answers what it resolved.
	_, _ = conf.Check(p.PkgPath, fset, files, info)
	return &Source{Fset: fset, Files: files, Info: info}
}

// TypesIDs is the [PackageID] of every package the load reads, the target's
// dependencies and its consumers' included, by its types.
func (r *Result) TypesIDs() map[*types.Package]PackageID {
	ids := make(map[*types.Package]PackageID)
	record := func(p *packages.Package) {
		if p.Types != nil {
			ids[p.Types] = r.ID(p)
		}
	}
	packages.Visit(r.Packages, nil, record)
	for _, consumer := range r.Referencing() {
		packages.Visit(consumer.Packages, nil, record)
	}
	return ids
}

// TypesID is the identity ids holds for pkg, its import path alone where the
// load read no such package.
func TypesID(ids map[*types.Package]PackageID, pkg *types.Package) PackageID {
	if id, held := ids[pkg]; held {
		return id
	}
	return PackageID{Path: pkg.Path()}
}
