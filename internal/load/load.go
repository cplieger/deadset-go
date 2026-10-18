// Package load resolves one build configuration of a target into type-checked
// packages, test variants included, over a topology read once per run. A load is
// fail-closed: a package that does not type-check ends the run with every
// diagnostic and no package set.
package load

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/failure"
	"golang.org/x/tools/go/packages"
)

// metadataMode is what one configuration's load asks the toolchain for: the
// package graph and its files, and nothing the toolchain would type-check, because
// [typeCheck] checks the packages itself. NeedForTest populates Package.ForTest,
// which names the package a test variant belongs to. NeedFiles populates
// IgnoredFiles, where a file a build constraint excluded appears. NeedModule gives
// the module path a symbol reference is built from and the language version a
// package is checked under. NeedEmbedFiles is absent because nothing reads it.
const metadataMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedImports | packages.NeedDeps | packages.NeedModule | packages.NeedForTest

// loadPattern matches every package of the target directory's module tree.
const loadPattern = "./..."

// goFileExt is the extension of a Go source file, which IgnoredFiles lists among
// assembly and C sources.
const goFileExt = ".go"

// The spelling of the test binary a test load synthesizes: a main package whose
// import path is the tested package's path with this suffix.
const (
	testBinarySuffix = ".test"
	mainPackageName  = "main"
)

var (
	// errConfiguration reports a configuration missing its identifier, its
	// operating system or its architecture.
	errConfiguration = errors.New("load: incomplete configuration")

	// errConsumer reports a declared consumer whose references a configuration
	// cannot count: a module whose identity disagrees with the scope, or one whose
	// own module graph resolves the target somewhere other than the target's own
	// directory. Each is refused rather than passed over, because a run that
	// counted no reference from a declared consumer would report the symbols that
	// consumer uses.
	errConsumer = errors.New("load: unusable consumer")
)

// origin is where a configuration of the matrix comes from, which decides what a
// failing load of it means.
type origin int

const (
	// Declared is a configuration the configuration document lists: one that does
	// not load ends the run, because an answer computed from the others is more
	// permissive than the truth.
	Declared origin = iota

	// host is the configuration of the host the run is on, which a derived matrix
	// always holds: one that does not load ends the run too.
	host

	// Derived is derivation's own pairing of an atom the tree names with an axis: one
	// that does not load is dropped, because the pairing was the guess.
	Derived
)

// Configuration is one build configuration of the matrix.
type Configuration struct {
	ID     string   // the identifier every finding names, "linux-amd64"
	OS     string   // GOOS
	Arch   string   // GOARCH
	Tags   []string // build tags, passed to the toolchain as one -tags flag
	Origin origin
}

// HostConfiguration is the configuration of the host the binary runs on, with no
// build tags. It reads the running binary's own target rather than asking the
// toolchain, so it spawns nothing and cannot disagree with the code it belongs to.
func HostConfiguration() Configuration {
	return Configuration{
		ID:     runtime.GOOS + "-" + runtime.GOARCH,
		OS:     runtime.GOOS,
		Arch:   runtime.GOARCH,
		Origin: host,
	}
}

// Load resolves configuration c of every module top holds; cancelling ctx stops
// it. A file or component the project lacks returns a setup *[failure.Error], any other
// metadata or [typeCheck] error a *[Error], an unusable consumer [errConsumer],
// each with a zero Result; a walked unit, a nested module or a reader that does not
// load is dropped into [Result.Dropped] instead while a unit is left. Load makes no
// network request, runs no compiler and pins the settings that decide what loads.
func Load(ctx context.Context, top *Topology, c *Configuration) (Result, error) {
	return load(ctx, top, c, newSyntaxCache())
}

// load is [Load] over trees, which [All] shares across the configurations of a matrix.
func load(ctx context.Context, top *Topology, c *Configuration, trees *syntaxCache) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("load %s: %w", c.ID, err)
	}
	if c.ID == "" || c.OS == "" || c.Arch == "" {
		return Result{}, fmt.Errorf("%w: id=%q os=%q arch=%q", errConfiguration, c.ID, c.OS, c.Arch)
	}
	l := &loader{ctx: ctx, top: top, c: c, trees: trees}
	r := Result{Configuration: *c, Fset: trees.fset}
	if failed := l.units(&r); failed != nil {
		return Result{}, failed
	}
	slices.Sort(r.ExcludedByCgo)
	r.ExcludedByCgo = slices.Compact(r.ExcludedByCgo)
	r.declared = declaredDirs(r.Packages)

	if err := l.readers(&r); err != nil {
		return Result{}, err
	}
	r.markUnanalysedConsumers()

	var err error
	if r.Consumers, err = l.consumers(r.Units); err != nil {
		return Result{}, err
	}
	r.sources = newSourceCache(&r)
	return r, nil
}

// loader is one configuration's load of one topology.
type loader struct {
	ctx   context.Context
	top   *Topology
	c     *Configuration
	trees *syntaxCache
}

// readers loads into r every module whose references count as a declared
// consumer's without being one: the nested modules, then the units' underscored
// packages, then the scripts of every module dropped so far, then the workspace
// readers.
func (l *loader) readers(r *Result) error {
	for i := range l.top.Nested {
		if err := l.nested(r, &l.top.Nested[i], unitPrefix(l.top.Root, l.top.Nested[i].Dir)); err != nil {
			return err
		}
	}
	for i := range l.top.Units {
		if err := l.underscored(r, &l.top.Units[i]); err != nil {
			return err
		}
	}
	for _, dropped := range r.Dropped {
		r.Programs = append(r.Programs, checkScripts(l.ctx, r.Fset, dropped.Dir, r.Packages, l.c)...)
	}
	for i := range l.top.Readers {
		if err := l.reader(r, &l.top.Readers[i]); err != nil {
			return err
		}
	}
	return nil
}

// loadSpec is what one load of a module directory runs: the directory, its GOWORK
// setting and the module file its toolchain reads, the further modules [typeCheck]
// checks whole, the prefix a position the toolchain wrote relative to the
// directory is rewritten below, and the patterns it names there.
type loadSpec struct {
	dir, workspace, prefix string
	file                   *ModuleFile
	modules                []string
	patterns               []string
}

// specOf is the load of one discovered module, checking whole the modules it consumes.
func specOf(m *discoveredModule, prefix string) *loadSpec {
	return &loadSpec{dir: m.Dir, workspace: m.Workspace, prefix: prefix, file: m.File, modules: m.Consumes}
}

// module loads one module directory under the configuration, test binaries
// dropped. A load that reports diagnostics returns them as an *[Error] naming
// name, empty for a unit of the target, and any other failure as the load met it.
func (l *loader) module(spec *loadSpec, name string) ([]*packages.Package, error) {
	pkgs, diagnostics, err := loadPackages(l.ctx, l.trees, l.c, spec)
	if err != nil {
		return nil, err
	}
	if len(diagnostics) > 0 {
		return nil, &Error{Configuration: l.c.ID, Module: name, Diagnostics: diagnostics}
	}
	return withoutTestBinaries(pkgs), nil
}

// units loads every unit of the topology into r, dropping a walked unit that does
// not load, and returns the error that ends the load, the first drop's where no
// unit is left.
func (l *loader) units(r *Result) error {
	for i := range l.top.Units {
		unit := &l.top.Units[i]
		failed := l.unit(r, unit)
		switch {
		case failed == nil:
		case l.top.Walked && droppable(failed) && l.ctx.Err() == nil:
			r.Dropped = append(r.Dropped, droppedOf(unit, failed))
		default:
			return failed
		}
	}
	if len(r.Units) == 0 && len(r.Dropped) > 0 {
		return r.Dropped[0].Err
	}
	return nil
}

// unit loads one unit into r: its packages, the files the opaque-C check reads
// back, and its programs.
func (l *loader) unit(r *Result, unit *discoveredModule) error {
	pkgs, err := l.module(specOf(unit, unitPrefix(l.top.Root, unit.Dir)), "")
	if err != nil {
		setup := failure.Setup(err)
		_, diagnosed := errors.AsType[*Error](err)
		if setup || diagnosed {
			return err
		}
		return fmt.Errorf("load %s: %s: %w", l.c.ID, unit.Dir, err)
	}
	cgo, err := cgoOnly(pkgs, l.c)
	if err != nil {
		return fmt.Errorf("load %s: %w", l.c.ID, err)
	}
	from := unitDir{dir: unit.Dir, workspace: unit.Workspace}
	r.ExcludedByCgo = append(r.ExcludedByCgo, checkOpaqueC(l.ctx, r.Fset, l.top.Root, from, pkgs, cgo, l.c)...)
	r.Programs = append(r.Programs, checkPrograms(l.ctx, r.Fset, from, pkgs, l.c)...)
	r.Packages = append(r.Packages, pkgs...)
	r.Units = append(r.Units, Unit{Dir: unit.Dir, Path: unit.Path, Packages: pkgs})
	return nil
}

// nested loads one module below the target that builds against a unit's
// directory as a [Result.Nested] consumer, dropping it where it does not load. A
// module below the target is named by prefix, its target-relative directory, which
// a note may carry.
func (l *loader) nested(r *Result, m *discoveredModule, prefix string) error {
	name := prefix
	if name == "" {
		name = m.Dir
	}
	pkgs, err := l.module(specOf(m, prefix), name)
	switch {
	case err == nil:
	case droppable(err) && l.ctx.Err() == nil:
		r.Dropped = append(r.Dropped, droppedOf(m, err))
		return nil
	default:
		return fmt.Errorf("load %s: %s: %w", l.c.ID, m.Dir, err)
	}
	if module := mainModule(pkgs); module != nil {
		r.Nested = append(r.Nested, Consumer{ID: module.Path, Packages: pkgs})
	}
	return nil
}

// reader loads one workspace reader as a [Result.Nested] consumer through the
// workspace, as [loader.nested] loads a module below the target. A reader whose
// module file the toolchain reads and this parser refuses is dropped unloaded,
// which makes every unit's exports possible ([program.8], [program.9]). One whose
// file the toolchain cannot read never gets here: the units' loads through the
// workspace read that file too, and end the run.
func (l *loader) reader(r *Result, m *discoveredModule) error {
	if m.FileErr != nil {
		r.Dropped = append(r.Dropped, droppedOf(m, fmt.Errorf("load %s: %s: %w", l.c.ID, m.Dir, m.FileErr)))
		return nil
	}
	return l.nested(r, m, "")
}

// underscored loads the packages of unit's own module below an underscore directory
// ([underscoredPackages]), an example tree among them, as a nested consumer of the
// unit. No build of the module compiles them, so where they do not load together
// each loads alone, and one that does not load counts nothing.
func (l *loader) underscored(r *Result, unit *discoveredModule) error {
	if len(unit.Underscored) == 0 {
		return nil
	}
	try := func(patterns []string) (bool, error) {
		spec := specOf(unit, unitPrefix(l.top.Root, unit.Dir))
		spec.modules, spec.patterns = []string{unit.Path}, patterns
		pkgs, err := l.module(spec, "")
		switch {
		case err == nil:
			r.Nested = append(r.Nested, Consumer{ID: unit.Path, Packages: pkgs})
			return true, nil
		case droppable(err) && l.ctx.Err() == nil:
			return false, nil
		default:
			return false, fmt.Errorf("load %s: %s: %w", l.c.ID, unit.Dir, err)
		}
	}
	if loaded, err := try(unit.Underscored); loaded || err != nil || len(unit.Underscored) == 1 {
		return err
	}
	for _, pattern := range unit.Underscored {
		if _, err := try([]string{pattern}); err != nil {
			return err
		}
	}
	return nil
}

// droppable reports whether one module's load error is about the project, a setup
// failure, the diagnostics of a load or the go command refusing to list it, rather
// than about the run.
func droppable(err error) bool {
	setup := failure.Setup(err)
	_, diagnosed := errors.AsType[*Error](err)
	_, listed := errors.AsType[*listError](err)
	return setup || diagnosed || listed
}

// loadPackages resolves and type-checks every package of the module rooted at
// spec's directory, or the packages its patterns name there, under configuration c.
// A load that reports errors returns them as diagnostics and no packages, each
// position the toolchain wrote relative to the directory rewritten below the prefix.
func loadPackages(ctx context.Context, trees *syntaxCache, c *Configuration, spec *loadSpec) ([]*packages.Package, []Diagnostic, error) {
	patterns := spec.patterns
	if len(patterns) == 0 {
		patterns = []string{loadPattern}
	}
	cfg := &packages.Config{
		Mode:       metadataMode,
		Context:    ctx,
		Tests:      true,
		Dir:        spec.dir,
		Env:        loadEnv(c, spec.workspace),
		BuildFlags: buildFlags(c.Tags),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, nil, &listError{err: err}
	}
	if spec.workspace != workspaceOff && len(spec.modules) == 0 {
		demoteOtherMembers(pkgs, spec.dir)
	}
	if err := dropUnbuiltErrors(pkgs, c); err != nil {
		return nil, nil, err
	}
	if diagnostics := collect(pkgs, spec.prefix); len(diagnostics) > 0 {
		if refused := setupFailures(diagnostics, expectedModules(spec.file, pkgs)); refused != nil {
			return nil, nil, refused
		}
		return nil, diagnostics, nil
	}
	if err := typeCheck(ctx, trees, spec.dir+"\x00"+strings.Join(spec.modules, "\x00"), pkgs, c.Arch, spec.modules); err != nil {
		return nil, nil, err
	}
	if diagnostics := collect(pkgs, spec.prefix); len(diagnostics) > 0 {
		return nil, diagnostics, nil
	}
	return pkgs, nil, nil
}

// expectedModules is every module the project expects to exist: the main modules
// the listing names, which a workspace makes several of and which a failed listing
// still names, and every module its module file requires or replaces. A module file
// that cannot be read contributes nothing, so the main modules alone decide.
func expectedModules(file *ModuleFile, pkgs []*packages.Package) []string {
	var modules []string
	add := func(path string) {
		if path != "" && !slices.Contains(modules, path) {
			modules = append(modules, path)
		}
	}
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Main {
			add(p.Module.Path)
		}
	}
	if file == nil {
		return modules
	}
	for _, r := range file.Syntax.Require {
		add(r.Mod.Path)
	}
	for _, r := range file.Syntax.Replace {
		add(r.Old.Path)
	}
	return modules
}

// demoteOtherMembers leaves the module rooted at dir the one main module of a
// workspace load. Every other member is a dependency of that unit, as a module a
// replace directive names is, so no stage reads it as the unit's own code.
func demoteOtherMembers(pkgs []*packages.Package, dir string) {
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Module == nil || !p.Module.Main || sameDir(p.Module.Dir, dir) {
			return
		}
		dependency := *p.Module
		dependency.Main = false
		p.Module = &dependency
	})
}

// positionlessOnly reports whether a package with no file the configuration
// compiles failed with list errors alone, none of them positioned: the toolchain's
// answer for a directory it builds nothing of, which is no error of the program.
func positionlessOnly(p *packages.Package) bool {
	return len(p.GoFiles) == 0 && len(p.CompiledGoFiles) == 0 && len(p.Errors) > 0 &&
		!slices.ContainsFunc(p.Errors, func(e packages.Error) bool { return e.Kind != packages.ListError || e.Pos != "" })
}

// unbuilt reports whether one package of the main module is a directory whose
// every Go file the configuration's constraints exclude, which the pattern still
// names and the configuration does not build. The toolchain reports such a
// package with a positionless error only while its build cache holds no entry for
// it, so the error is dropped and the package is read the same way on every run.
func unbuilt(p *packages.Package) bool {
	return p.Module != nil && p.Module.Main && len(p.IgnoredFiles) > 0 && positionlessOnly(p)
}

// dropUnbuiltErrors clears the errors of every package reachable from pkgs that
// [unbuilt] or [unbuiltForCgo] reports, and returns the first error reading a
// file's imports met.
func dropUnbuiltErrors(pkgs []*packages.Package, c *Configuration) error {
	var failed error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if unbuilt(p) {
			p.Errors = nil
			return
		}
		cgo, err := unbuiltForCgo(p, c)
		if err != nil && failed == nil {
			failed = err
		}
		if cgo {
			p.Errors = nil
		}
	})
	return failed
}

// unbuiltForCgo reports whether one package, of any module, has no Go file only
// because [loadEnv] disables cgo: every Go file it ignored imports "C" and the
// configuration selects it with cgo enabled. The configuration builds such a
// package, so its positionless error is the analysis's own setting and no error
// of the program. Its declarations are absent from the load, so a reference to
// one is a type error [typeCheck] handles as it handles any other.
func unbuiltForCgo(p *packages.Package, c *Configuration) (bool, error) {
	if !positionlessOnly(p) {
		return false, nil
	}
	ctxt := cgoEnabledContext(c)
	found := false
	for _, file := range p.IgnoredFiles {
		if filepath.Ext(file) != goFileExt {
			continue
		}
		alone, err := excludedByCgoAlone(&ctxt, file)
		if err != nil || !alone {
			return false, err
		}
		found = true
	}
	return found, nil
}

// loadEnv is the environment every load of configuration c runs the toolchain
// under, with workspace as the child toolchain's GOWORK setting. os/exec keeps the
// last value of a repeated key, so these settings win over the inherited ones: an
// ambient go.work or GOFLAGS would make a module's result depend on where the run
// started, and GOPROXY=off makes the no-network rule the toolchain's, a module the
// cache does not hold ending the load instead of being fetched.
func loadEnv(c *Configuration, workspace string) []string {
	return append(os.Environ(),
		"CGO_ENABLED=0", "GOOS="+c.OS, "GOARCH="+c.Arch,
		"GOWORK="+workspace, "GOFLAGS=", "GOPROXY=off")
}

// withoutTestBinaries returns every package of a test load except the test
// binaries the toolchain synthesizes, keeping the order the load reported.
//
// A test binary is a main package the toolchain writes for a tested package
// rather than a package of the target: its one file is generated into the build
// cache, it declares nothing the target wrote, and the test functions it calls
// are reached by the rules that make a test function a root.
func withoutTestBinaries(pkgs []*packages.Package) []*packages.Package {
	binaries := make(map[string]bool)
	for _, p := range pkgs {
		if p.ForTest != "" {
			binaries[p.ForTest+testBinarySuffix] = true
		}
	}
	kept := make([]*packages.Package, 0, len(pkgs))
	for _, p := range pkgs {
		if p.Name == mainPackageName && binaries[p.PkgPath] {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

// buildFlags renders a configuration's tags as the toolchain flag that selects
// them, and nothing when the configuration declares none.
func buildFlags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	return []string{"-tags=" + strings.Join(tags, ",")}
}

// collect walks every package reachable from roots, dependencies and test
// variants included, and returns every error any of them reported in a
// deterministic order, a position the toolchain wrote relative to the directory it
// ran in rewritten below prefix.
func collect(roots []*packages.Package, prefix string) []Diagnostic {
	var diagnostics []Diagnostic
	packages.Visit(roots, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			diagnostics = append(diagnostics, Diagnostic{
				Package:  p.ID,
				Position: rebased(e.Pos, prefix),
				Message:  e.Msg,
			})
		}
	})
	slices.SortFunc(diagnostics, compareDiagnostics)
	return slices.CompactFunc(diagnostics, sameDiagnostic)
}
