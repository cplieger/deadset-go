// Package load resolves one build configuration of a target into type-checked
// packages, test variants included, over a topology read once per run. A load is
// fail-closed: a package that does not type-check ends the run with every
// diagnostic and no package set.
package load

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
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

// loadPattern matches every package of the target directory's module tree, and
// every package below a node_modules directory too, which [loadPackages] drops.
const loadPattern = "./..."

// goCommand is the toolchain command a listing runs, which go/packages looks up
// on this process's PATH.
const goCommand = "go"

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

// HostConfiguration is the host platform with no build tags: the GOOS and GOARCH
// environment variables where they are set and not empty, else the running
// binary's own target; a value only the go env file sets does not count
// ([matrix.1]). It spawns nothing.
func HostConfiguration() Configuration {
	goos, goarch := cmp.Or(os.Getenv("GOOS"), runtime.GOOS), cmp.Or(os.Getenv("GOARCH"), runtime.GOARCH)
	return Configuration{ID: goos + "-" + goarch, OS: goos, Arch: goarch, Origin: host}
}

// Load resolves configuration c of every module top holds; cancelling ctx stops
// it. A file or component the project lacks returns a setup *[failure.Error], a go
// command absent from PATH a run *[failure.Error] of missing-toolchain, any other
// metadata or [typeCheck] error a *[Error], an unusable consumer [errConsumer],
// each with a zero Result; a walked unit, a nested module or a reader that does not
// load is dropped into [Result.Dropped] instead while a unit is left. Load makes no
// network request, runs no compiler and pins the settings that decide what loads.
func Load(ctx context.Context, top *Topology, c *Configuration) (Result, error) {
	return load(ctx, top, c, newSyntaxCache())
}

// load is [Load] over trees, which a [Matrix] shares across its configurations.
func load(ctx context.Context, top *Topology, c *Configuration, trees *syntaxCache) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("load %s: %w", c.ID, err)
	}
	if c.ID == "" || c.OS == "" || c.Arch == "" {
		return Result{}, fmt.Errorf("%w: id=%q os=%q arch=%q", errConfiguration, c.ID, c.OS, c.Arch)
	}
	if _, err := exec.LookPath(goCommand); err != nil {
		return Result{}, failure.Wrap(vocab.RunMissingToolchain, fmt.Errorf("the %s command that lists the target's packages is not on PATH %q: %w", goCommand, os.Getenv("PATH"), err))
	}
	overlay, remove, err := top.writeOverlay()
	if err != nil {
		return Result{}, fmt.Errorf("load %s: write the overlay that hides what the toolchain may block on: %w", c.ID, err)
	}
	defer remove()
	l := &loader{ctx: ctx, top: top, c: c, trees: trees, toolchain: toolchain{overlay: overlay, noEnvFile: top.noEnvFile}}
	r := Result{Configuration: *c, Fset: trees.fset}
	if failed := l.units(&r); failed != nil {
		return Result{}, failed
	}
	slices.Sort(r.ExcludedByCgo)
	r.ExcludedByCgo = slices.Compact(r.ExcludedByCgo)
	r.declared = declaredDirs(r.Packages)

	if failed := l.readers(&r); failed != nil {
		return Result{}, failed
	}
	if r.Consumers, err = l.consumers(r.Units); err != nil {
		return Result{}, err
	}
	r.Hidden, r.Obscured = top.obscured(&r)
	r.markUnanalysedConsumers(len(top.Unread) > 0 || len(r.Hidden) > 0)
	return r, nil
}

// loader is one configuration's load of one topology.
type loader struct {
	ctx       context.Context
	top       *Topology
	c         *Configuration
	trees     *syntaxCache
	toolchain toolchain
}

// toolchain is what every toolchain one load starts runs with besides the
// configuration: the overlay file that hides the topology's hidden entries, empty
// where it hides none, and whether one started in a directory runs without the go
// env file.
type toolchain struct {
	noEnvFile func(dir string) bool
	overlay   string
}

// goflags is the GOFLAGS every go command of the load runs under: the default of
// a flag every build command has, which no go env file can then replace (cmd/go
// internal/cfg Getenv reads an empty GOFLAGS from it), and the overlay. GOFLAGS
// reaches every go command go/packages starts, the release-tags query too, which
// takes no build flags; one without the flag ignores it (cmd/go internal/base
// SetFromGOFLAGS). A path holding a space is quoted, as GOFLAGS splits words.
func (tc toolchain) goflags() (string, error) {
	flags := "GOFLAGS=-modcacherw=false"
	switch {
	case tc.overlay == "":
		return flags, nil
	case !strings.ContainsAny(tc.overlay, " \t\n\r'\""):
		return flags + " " + overlayFlag + tc.overlay, nil
	case !strings.Contains(tc.overlay, "'"):
		return flags + " '" + overlayFlag + tc.overlay + "'", nil
	case !strings.Contains(tc.overlay, `"`):
		return flags + ` "` + overlayFlag + tc.overlay + `"`, nil
	}
	return "", fmt.Errorf("the overlay file %s holds both quote characters, which GOFLAGS cannot carry", tc.overlay)
}

// overlayFlag is the go command flag that names its overlay file.
const overlayFlag = "-overlay="

// readers loads into r every module whose references count as a declared
// consumer's without being one: the nested modules, the packages each unit imports
// from outside its packages, the units' underscored packages and the workspace
// readers, then the scripts of each module dropped below the target ([program.7]).
//
// The imported packages come before the underscored ones: an underscored package a
// unit's build compiles is held on the side that build compiles it, and a second,
// production copy would keep live what only a test reaches.
func (l *loader) readers(r *Result) error {
	for i := range l.top.Nested {
		if err := l.nested(r, &l.top.Nested[i]); err != nil {
			return err
		}
	}
	for i := range r.Units {
		production, test := importedOutside(&r.Units[i])
		if len(production) > 0 {
			r.Nested = append(r.Nested, Consumer{ID: r.Units[i].Path, Packages: production, Build: ConsumerBuild})
		}
		if len(test) > 0 {
			r.Nested = append(r.Nested, Consumer{ID: r.Units[i].Path, Packages: test, Build: TestBuild})
		}
	}
	for i := range l.top.Units {
		if err := l.underscored(r, &l.top.Units[i]); err != nil {
			return err
		}
	}
	below := len(r.Dropped)
	for i := range l.top.Readers {
		if err := l.reader(r, &l.top.Readers[i]); err != nil {
			return err
		}
	}
	l.droppedScripts(r, below)
	return nil
}

// droppedScripts adds to r the scripts of each module dropped below the target,
// the first below of r.Dropped, and of each reader a file no toolchain may run
// under dropped ([program.6]).
func (l *loader) droppedScripts(r *Result, below int) {
	for i, dropped := range r.Dropped {
		if i >= below && !dropped.irregular {
			continue
		}
		from := unitDir{dir: dropped.Dir, workspace: workspaceOff, offline: dropped.offline, toolchain: l.toolchain}
		r.Programs = append(r.Programs, checkScripts(l.ctx, l.top, r.Fset, from, r.Packages, l.c)...)
	}
}

// loadSpec is what one load of a module directory runs: the directory, its GOWORK
// setting and the module file its toolchain reads, the further modules [typeCheck]
// checks whole, the patterns it names there, the topology whose root every
// position is rebased to and whose unread and left-out entries it lists around,
// and how its toolchain runs.
type loadSpec struct {
	file           *ModuleFile
	top            *Topology
	dir, workspace string
	toolchain      toolchain
	modules        []string
	patterns       []string
}

// specOf is the load of one discovered module, checking whole the modules it consumes.
func specOf(m *discoveredModule) *loadSpec {
	return &loadSpec{dir: m.Dir, workspace: m.Workspace, file: m.File, modules: m.Consumes}
}

// module loads one module directory under the configuration, test binaries
// dropped. A load that reports diagnostics returns them as an *[Error] naming
// name, empty for a unit of the target, and any other failure as the load met it.
func (l *loader) module(spec *loadSpec, name string) ([]*packages.Package, error) {
	spec.top, spec.toolchain = l.top, l.toolchain
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
	if unit.Refused != nil {
		return unit.Refused
	}
	pkgs, err := l.module(specOf(unit), "")
	if err != nil {
		setup := failure.Setup(err)
		_, diagnosed := errors.AsType[*Error](err)
		if setup || diagnosed {
			return err
		}
		return fmt.Errorf("load %s: %s: %w", l.c.ID, l.top.rel(unit.Dir), err)
	}
	cgo, err := cgoOnly(pkgs, l.c)
	if err != nil {
		return fmt.Errorf("load %s: %w", l.c.ID, err)
	}
	from := unitDir{dir: unit.Dir, workspace: unit.Workspace, toolchain: l.toolchain}
	r.ExcludedByCgo = append(r.ExcludedByCgo, checkOpaqueC(l.ctx, r.Fset, l.top.Root, from, pkgs, cgo, l.c)...)
	r.Programs = append(r.Programs, checkPrograms(l.ctx, r.Fset, from, pkgs, l.c)...)
	r.Packages = append(r.Packages, pkgs...)
	r.Units = append(r.Units, Unit{Dir: unit.Dir, Path: unit.Path, Packages: pkgs})
	return nil
}

// nested loads one module below the target that builds against a unit's
// directory as a [Result.Nested] consumer, dropping it where it does not load. The
// module is named by its directory relative to the root, which a note may carry.
func (l *loader) nested(r *Result, m *discoveredModule) error {
	name := l.top.rel(m.Dir)
	if m.Refused != nil {
		r.Dropped = append(r.Dropped, droppedOf(m, fmt.Errorf("load %s: %s: %w", l.c.ID, name, m.Refused)))
		return nil
	}
	pkgs, err := l.module(specOf(m), name)
	switch {
	case err == nil:
	case droppable(err) && l.ctx.Err() == nil:
		r.Dropped = append(r.Dropped, droppedOf(m, err))
		return nil
	default:
		return fmt.Errorf("load %s: %s: %w", l.c.ID, name, err)
	}
	if module := mainModule(pkgs); module != nil {
		r.Nested = append(r.Nested, Consumer{ID: module.Path, Packages: pkgs, Build: ConsumerBuild})
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
		r.Dropped = append(r.Dropped, droppedOf(m, fmt.Errorf("load %s: %s: %w", l.c.ID, l.top.rel(m.Dir), m.FileErr)))
		return nil
	}
	return l.nested(r, m)
}

// importedOutside is every package of unit's module outside its packages
// ([ModuleTree.OutsideDir]) that the module's build compiles all the same, so its
// references count and nothing of it is reported: those the production code of
// unit's packages imports, then those only their test variants import. A test
// binary recompiles a production-imported package against a tested one ("l
// [p.test]"), and that variant is production code too, so no file is compiled by
// packages of both sides.
func importedOutside(unit *Unit) (production, test []*packages.Package) {
	if unit.Path == "" {
		return nil, nil
	}
	tree := treeAt(unit.Dir)
	var built, tested []*packages.Package
	for _, p := range unit.Packages {
		if p.ForTest == "" {
			built = append(built, p)
		} else {
			tested = append(tested, p)
		}
	}
	held := make(map[string]bool)
	produced := make(map[string]bool) // the import paths production holds
	outside := func(testing bool) func(*packages.Package) {
		return func(p *packages.Package) {
			if held[p.ID] || p.Module == nil || p.Module.Path != unit.Path || !tree.outside(unit.Dir, p.Dir) {
				return
			}
			held[p.ID] = true
			if testing && !produced[p.PkgPath] {
				test = append(test, p)
				return
			}
			produced[p.PkgPath] = true
			production = append(production, p)
		}
	}
	packages.Visit(built, nil, outside(false))
	packages.Visit(tested, nil, outside(true))
	return production, test
}

// underscored loads the packages of unit's own module below an underscore directory
// ([underscoredPackages]), an example tree among them, as a nested consumer of the
// unit, but for one [importedOutside] already holds for it. No build of the module
// compiles the rest, so where they do not load together each loads alone, and one
// that does not load counts nothing.
func (l *loader) underscored(r *Result, unit *discoveredModule) error {
	held := make(map[string]bool)
	for i := range r.Nested {
		if r.Nested[i].ID == unit.Path {
			for _, p := range r.Nested[i].Packages {
				held[p.Dir] = true
			}
		}
	}
	patterns := slices.DeleteFunc(slices.Clone(unit.Underscored), func(pattern string) bool {
		return held[filepath.Join(unit.Dir, filepath.FromSlash(pattern))]
	})
	if len(patterns) == 0 {
		return nil
	}
	try := func(patterns []string) (bool, error) {
		spec := specOf(unit)
		spec.modules, spec.patterns = []string{unit.Path}, patterns
		pkgs, err := l.module(spec, "")
		switch {
		case err == nil:
			r.Nested = append(r.Nested, Consumer{ID: unit.Path, Packages: pkgs, Build: ConsumerBuild})
			return true, nil
		case droppable(err) && l.ctx.Err() == nil:
			return false, nil
		default:
			return false, fmt.Errorf("load %s: %s: %w", l.c.ID, l.top.rel(unit.Dir), err)
		}
	}
	if loaded, err := try(patterns); loaded || err != nil || len(patterns) == 1 {
		return err
	}
	for _, pattern := range patterns {
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
// spec's directory, in no directory [leavesOut] names, or the packages its patterns
// name there, under configuration c.
// A load that reports errors returns them as diagnostics and no packages, each
// position the toolchain wrote relative to the directory rewritten below the prefix.
func loadPackages(ctx context.Context, trees *syntaxCache, c *Configuration, spec *loadSpec) ([]*packages.Package, []Diagnostic, error) {
	patterns, byDir, err := listPatterns(ctx, spec)
	if err != nil || len(patterns) == 0 {
		return nil, nil, err
	}
	cfg, err := listConfig(ctx, c, spec)
	if err != nil {
		return nil, nil, err
	}
	pkgs, err := trees.list(cfg, patterns)
	if err != nil {
		return nil, nil, listFailure(spec, &listError{err: err})
	}
	pkgs = listedOf(pkgs, spec, byDir)
	if err := dropUnbuiltErrors(pkgs, c); err != nil {
		return nil, nil, err
	}
	pkgs = dropUnreadErrors(pkgs, spec)
	if diagnostics := collect(pkgs, spec.top.Root, spec.dir); len(diagnostics) > 0 {
		if refused := spec.top.hiddenCause(spec, "", pkgs); refused != nil {
			return nil, nil, refused
		}
		if refused := setupFailures(diagnostics, expectedModules(spec.file, pkgs)); refused != nil {
			return nil, nil, refused
		}
		return nil, diagnostics, nil
	}
	if err := typeCheck(ctx, trees, spec.dir+"\x00"+strings.Join(spec.modules, "\x00"), pkgs, c.Arch, spec.modules); err != nil {
		return nil, nil, err
	}
	if diagnostics := collect(pkgs, spec.top.Root, spec.dir); len(diagnostics) > 0 {
		if refused := spec.top.hiddenCause(spec, "", pkgs); refused != nil {
			return nil, nil, refused
		}
		return nil, diagnostics, nil
	}
	return pkgs, nil, nil
}

// listFailure is the failure of a load of spec whose listing failed with listed:
// the setup failure of an entry the run hid that it needs, or of a configuration
// file it cannot read, else listed itself.
func listFailure(spec *loadSpec, listed *listError) error {
	if refused := spec.top.hiddenCause(spec, listed.Error(), nil); refused != nil {
		return refused
	}
	if refused := unreadableConfiguration(listed, spec.top.rel(spec.dir)); refused != nil {
		return refused
	}
	return listed
}

// listedOf is the packages of spec's listing pkgs that the load reads: none in a
// directory [leavesOut] names where spec names no pattern, none [unbuilt] where
// the patterns name directories, and under a workspace every other member's
// package demoted to a dependency ([demoteOtherMembers]).
func listedOf(pkgs []*packages.Package, spec *loadSpec, byDir bool) []*packages.Package {
	if len(spec.patterns) == 0 {
		pkgs = slices.DeleteFunc(pkgs, func(p *packages.Package) bool { return leftOutBelow(spec.dir, p.Dir) })
	}
	if byDir {
		pkgs = slices.DeleteFunc(pkgs, unbuilt)
	}
	if spec.workspace != workspaceOff && len(spec.modules) == 0 {
		demoteOtherMembers(pkgs, spec.dir)
	}
	return pkgs
}

// listConfig is the go/packages configuration of one load of spec under c.
func listConfig(ctx context.Context, c *Configuration, spec *loadSpec) (*packages.Config, error) {
	env, err := loadEnv(c, spec.dir, spec.workspace, spec.toolchain)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", c.ID, err)
	}
	return &packages.Config{
		Mode:       metadataMode,
		Context:    ctx,
		Tests:      true,
		Dir:        spec.dir,
		Env:        env,
		BuildFlags: buildFlags(c.Tags),
	}, nil
}

// listing is one module listing's packages, as the toolchain reported them, or its
// error.
type listing struct {
	err  error
	pkgs []*packages.Package
}

// list is the toolchain's listing of patterns under cfg, listed once per matrix: a
// configuration loaded again reads a copy of its first listing, so it loads the
// packages and files its first load did whatever changed on disk or in the module
// cache since, and every answer the run kept from the first load still holds.
func (t *syntaxCache) list(cfg *packages.Config, patterns []string) ([]*packages.Package, error) {
	// The overlay file is written again for each load, and what it holds is the
	// topology's, which every configuration of a matrix shares.
	env := slices.DeleteFunc(slices.Clone(cfg.Env), func(v string) bool { return strings.HasPrefix(v, "GOFLAGS=") })
	key := strings.Join(slices.Concat([]string{cfg.Dir}, patterns, []string{""}, cfg.BuildFlags, []string{""}, env), "\x00")
	t.mu.Lock()
	held, known := t.lists[key]
	t.mu.Unlock()
	if known {
		return copyGraph(held.pkgs), held.err
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if cfg.Context.Err() != nil {
		return pkgs, err
	}
	t.mu.Lock()
	t.lists[key] = &listing{pkgs: copyGraph(pkgs), err: err}
	t.mu.Unlock()
	return pkgs, err
}

// copyGraph copies every package reachable from roots, each import naming the
// copy, so the load's checks and edits of one copy leave every other untouched.
func copyGraph(roots []*packages.Package) []*packages.Package {
	copies := make(map[*packages.Package]*packages.Package)
	packages.Visit(roots, nil, func(p *packages.Package) {
		c := *p
		c.Errors = slices.Clone(p.Errors)
		c.GoFiles = slices.Clone(p.GoFiles)
		c.CompiledGoFiles = slices.Clone(p.CompiledGoFiles)
		c.OtherFiles = slices.Clone(p.OtherFiles)
		c.EmbedPatterns = slices.Clone(p.EmbedPatterns)
		c.EmbedFiles = slices.Clone(p.EmbedFiles)
		c.IgnoredFiles = slices.Clone(p.IgnoredFiles)
		if p.Module != nil {
			module := *p.Module
			c.Module = &module
		}
		if p.Imports != nil {
			c.Imports = make(map[string]*packages.Package, len(p.Imports))
			for path, imported := range p.Imports {
				c.Imports[path] = copies[imported]
			}
		}
		copies[p] = &c
	})
	copied := make([]*packages.Package, len(roots))
	for i, p := range roots {
		copied[i] = copies[p]
	}
	return copied
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

// listPatterns is the patterns the load of spec lists, and whether they name each
// directory of the tree by its own path ([treePatterns]).
func listPatterns(ctx context.Context, spec *loadSpec) (patterns []string, byDir bool, err error) {
	switch {
	case len(spec.patterns) > 0:
		return spec.patterns, false, nil
	case !spec.top.unreadBelow(spec.dir):
		return []string{loadPattern}, false, nil
	}
	patterns, err = treePatterns(ctx, spec)
	return patterns, true, err
}

// unreadBelow reports whether an entry strictly below dir is [Unread].
func (top *Topology) unreadBelow(dir string) bool {
	return top != nil && slices.ContainsFunc(top.Unread, func(path string) bool { return scope.Below(dir, path) })
}

// treePatterns names by its own path each directory holding a Go file of the
// module tree at spec's directory, and under a workspace of each member module
// below it: where an entry below it is [Unread], a directory the toolchain cannot
// list ends its walk of [loadPattern] there, leaving out every package after it.
// The toolchain answers a directory named so whose every Go file the
// configuration's constraints exclude with a package, where the wildcard leaves it
// out, so [loadPackages] drops it, and loads no package for no pattern.
func treePatterns(ctx context.Context, spec *loadSpec) ([]string, error) {
	members := scope.Workspace{}
	if spec.workspace != workspaceOff {
		var err error
		if members, err = scope.WorkspaceOf(ctx, spec.top.Root, spec.dir); err != nil {
			return nil, fmt.Errorf("load: %s: %w", spec.top.rel(spec.dir), err)
		}
	}
	return dirPatterns(ctx, spec, &members)
}

// dirPatterns is the pattern of each directory of the tree at spec's directory
// that holds a Go file, entering a member module of the workspace and no other.
func dirPatterns(ctx context.Context, spec *loadSpec, members *scope.Workspace) ([]string, error) {
	tree := TreeOf(spec.file)
	var patterns []string
	enters := func(dir string) bool { return !tree.Skips(dir) && (!IsModuleRoot(dir) || members.Lists(dir)) }
	err := spec.top.Walk(spec.dir, enters, func(path string, kind Kind) error {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case kind != File || !buildconstraint.IsGoFile(filepath.Base(path)):
			return nil
		}
		pattern := "./" + unitPrefix(spec.dir, filepath.Dir(path))
		if pattern == "./" {
			pattern = "."
		}
		if !slices.Contains(patterns, pattern) {
			patterns = append(patterns, pattern)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load: find the packages under %s: %w", spec.top.rel(spec.dir), err)
	}
	return patterns, nil
}

// dropUnreadErrors clears every list error reporting that opening an entry the
// walks met as [Unread] or [leftOut] failed, and drops a root left with no file and
// no error: the toolchain's answer for a pattern whose walk met such a directory.
// The toolchain lists every other package and file all the same, and leaves such
// a Go file out of the package's compiled files.
func dropUnreadErrors(pkgs []*packages.Package, spec *loadSpec) []*packages.Package {
	if spec.top == nil || len(spec.top.Unread) == 0 && len(spec.top.LeftOut) == 0 {
		return pkgs
	}
	opened := func(e packages.Error) bool {
		names := func(path string) bool { return opens(e.Msg, spec.dir, path) }
		return e.Kind == packages.ListError && (slices.ContainsFunc(spec.top.Unread, names) || slices.ContainsFunc(spec.top.LeftOut, names))
	}
	cleared := make(map[*packages.Package]bool)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		kept := slices.DeleteFunc(p.Errors, opened)
		cleared[p] = len(kept) == 0 && len(p.Errors) > 0
		p.Errors = kept
	})
	return slices.DeleteFunc(pkgs, func(p *packages.Package) bool {
		return cleared[p] && len(p.GoFiles) == 0 && len(p.CompiledGoFiles) == 0
	})
}

// opens reports whether a toolchain message reports that opening path failed, the
// toolchain naming it relative to the directory it ran in or in full.
func opens(msg, from, path string) bool {
	if strings.Contains(msg, "open "+path+": ") {
		return true
	}
	rel, err := filepath.Rel(from, path)
	return err == nil && filepath.IsLocal(rel) && strings.Contains(msg, "open "+rel+": ")
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
// under in dir, with workspace as the child toolchain's GOWORK setting and tc's GOFLAGS
// ([toolchain.goflags]), without the go env file where tc says so for dir. os/exec keeps
// the last value of a repeated key, so these settings win over the inherited ones:
// an ambient go.work or GOFLAGS would make a module's result depend on where the
// run started, and GOPROXY=off makes the no-network rule the toolchain's, a module
// the cache does not hold ending the load instead of being fetched.
func loadEnv(c *Configuration, dir, workspace string, tc toolchain) ([]string, error) {
	goflags, err := tc.goflags()
	if err != nil {
		return nil, err
	}
	env := append(os.Environ(),
		"CGO_ENABLED=0", "GOOS="+c.OS, "GOARCH="+c.Arch,
		"GOWORK="+workspace, goflags, "GOPROXY=off")
	if tc.noEnvFile != nil && tc.noEnvFile(dir) {
		env = append(env, "GOENV=off")
	}
	return env, nil
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

// reportsErrors reports whether a package reachable from roots reported an error.
func reportsErrors(roots []*packages.Package) bool {
	reported := false
	packages.Visit(roots, nil, func(p *packages.Package) { reported = reported || len(p.Errors) > 0 })
	return reported
}

// collect walks every package reachable from roots, dependencies and test
// variants included, and returns every error any of them reported in a
// deterministic order, each position rebased to root from dir, the directory the
// toolchain ran in ([rebased]).
func collect(roots []*packages.Package, root, dir string) []Diagnostic {
	var diagnostics []Diagnostic
	packages.Visit(roots, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			diagnostics = append(diagnostics, Diagnostic{
				Package:  p.ID,
				Position: rebased(e.Pos, root, dir, p.Module),
				Message:  e.Msg,
			})
		}
	})
	slices.SortFunc(diagnostics, compareDiagnostics)
	return slices.CompactFunc(diagnostics, sameDiagnostic)
}
