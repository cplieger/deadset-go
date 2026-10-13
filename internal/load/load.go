// Package load resolves one build configuration of a target into type-checked
// packages, test variants included. A load is fail-closed: a package that does
// not type-check ends the run with every diagnostic and no package set.
package load

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/scope"
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
	// cannot count: a module whose identity
	// disagrees with the scope, or one whose own module graph resolves the target
	// somewhere other than the target's own directory. Each is refused rather
	// than passed over, because a run that counted no reference from a declared
	// consumer would report the symbols that consumer uses.
	errConsumer = errors.New("load: unusable consumer")
)

// Configuration is one build configuration of the matrix.
type Configuration struct {
	ID   string   // the identifier every finding names, "linux-amd64"
	OS   string   // GOOS
	Arch string   // GOARCH
	Tags []string // build tags, passed to the toolchain as one -tags flag
}

// Result is one configuration's loaded packages.
type Result struct {
	Configuration Configuration

	// Packages holds the target's packages and the test variants that
	// type-check their files again, and never a synthesized test binary, so
	// every file a stage of the analysis meets is a file of the target.
	Packages []*packages.Package

	// Units holds one entry per module of the target, in [Units] order, each
	// with its share of Packages. A reference from one unit to another is to a
	// dependency of the first loaded from the second's directory, which is a
	// second reading of the second's files.
	Units []Unit

	// Nested holds one entry per module that is no declared consumer and whose
	// references count as a declared consumer's do: each module below a target the
	// walk does not search that builds against a unit's directory, in directory order,
	// then each unit's packages below an underscore directory, then each workspace
	// reader [Result.AddReaders] appends.
	Nested []Consumer

	// Dropped holds every module the walk found below the target, and every
	// module Nested would hold, that did not load. A unit one of them requires has
	// it as a consumer the run did not analyze.
	Dropped []droppedModule

	// Consumers holds one entry per declared consumer, in the order the scope
	// declares them. A consumer's packages are kept apart from the target's
	// because the two answer different questions: the declarations a run reports
	// on are the target's alone, while the references it counts come from every
	// module it loaded.
	Consumers []Consumer

	Fset *token.FileSet // one FileSet for the whole configuration

	// TestSupport holds the [Result.PackageKey] of the target's test-support packages:
	// packages only tests reach, which every stage judges as test code. The load
	// leaves it empty and [graph.ClassifyTestSupport] fills it, because the
	// classification turns on the target kind and the roots.
	TestSupport map[string]bool

	// Programs holds the target's files run on their own under the ignore tag,
	// which no package of the target compiles and every stage but the root
	// detection leaves alone.
	Programs []program

	shared map[string]bool // the import paths [Result.PackageKey] joins a directory to

	// ExcludedByCgo holds the target-relative paths, forward slashes, of the
	// files the toolchain ignored for importing "C" that the opaque-C check could
	// not read either. References those files make are outside the reference set,
	// which is a declared limit of the run rather than a file nothing builds. A
	// file the check did read is a file of its package like any other and is not
	// in this list.
	ExcludedByCgo []string
}

// HostConfiguration is the configuration of the host the binary runs on, with no
// build tags. It reads the running binary's own target rather than asking the
// toolchain, so it spawns nothing and cannot disagree with the code it belongs to.
func HostConfiguration() Configuration {
	return Configuration{
		ID:   runtime.GOOS + "-" + runtime.GOARCH,
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
}

// Load resolves configuration c of every unit of doc's target ([Units]) and every
// consumer doc declares; cancelling ctx stops it. A file or component the project
// lacks returns a *[SetupError], any other metadata or [typeCheck] error a *[Error],
// an unusable consumer [errConsumer], each with a zero Result; a walked or nested
// module that does not load is dropped into [Result.Dropped] instead while a unit is
// left. Load makes no network request, runs no compiler and pins the settings that
// decide what loads. A unit another unit builds from its directory is read twice,
// and the resolver joins the two readings by rendered site.
func Load(ctx context.Context, doc scope.Document, c Configuration) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("load %s: %w", c.ID, err)
	}
	if c.ID == "" || c.OS == "" || c.Arch == "" {
		return Result{}, fmt.Errorf("%w: id=%q os=%q arch=%q", errConfiguration, c.ID, c.OS, c.Arch)
	}

	target := doc.Target.Path
	if target == "" {
		return Result{}, fmt.Errorf("load %s: %w", c.ID, scope.ErrNoTarget)
	}
	if _, err := os.Stat(target); err != nil {
		return Result{}, fmt.Errorf("load %s: target %s: %w", c.ID, target, err)
	}

	dirs, err := Units(ctx, target)
	if err != nil {
		return Result{}, fmt.Errorf("load %s: find the modules under %s: %w", c.ID, target, err)
	}
	walked := walkedUnits(target, dirs)
	r := Result{Configuration: c, Fset: token.NewFileSet()}
	if failed := r.loadUnits(ctx, target, dirs, walked); failed != nil {
		return Result{}, failed
	}
	slices.Sort(r.ExcludedByCgo)
	r.ExcludedByCgo = slices.Compact(r.ExcludedByCgo)
	r.markShared()

	if !walked {
		if r.Nested, r.Dropped, err = loadNested(ctx, r.Fset, &doc, c, r.Units); err != nil {
			return Result{}, err
		}
	}
	r.markUnanalysedConsumers(ctx)

	r.Consumers, err = loadConsumers(ctx, r.Fset, &doc, c, r.Units)
	if err != nil {
		return Result{}, err
	}
	return r, nil
}

// loadUnits loads every unit of dirs into r, dropping a walked unit that does not
// load, and returns the error that ends the load, the first drop's where no unit is
// left.
func (r *Result) loadUnits(ctx context.Context, target string, dirs []string, walked bool) error {
	for _, dir := range dirs {
		failed := r.loadUnit(ctx, target, dir)
		switch {
		case failed == nil:
		case walked && droppable(failed) && ctx.Err() == nil:
			r.Dropped = append(r.Dropped, droppedModule{Dir: dir, Err: failed})
		default:
			return failed
		}
	}
	if len(r.Units) == 0 && len(r.Dropped) > 0 {
		return r.Dropped[0].Err
	}
	return nil
}

// loadUnit loads the unit whose toolchain runs from dir into r, as [Load] loads every unit.
// The unit's own module file decides its versions, through the workspace that
// lists it where one governs dir, and never through the ambient setting.
func (r *Result) loadUnit(ctx context.Context, target, dir string) error {
	c := r.Configuration
	workspace, err := unitWorkspace(ctx, dir)
	if err != nil {
		return fmt.Errorf("load %s: %s: %w", c.ID, dir, err)
	}
	pkgs, diagnostics, err := loadPackages(ctx, r.Fset, dir, c, workspace, nil, unitPrefix(target, dir))
	if _, setup := errors.AsType[*SetupError](err); setup {
		return err
	}
	if err != nil {
		return fmt.Errorf("load %s: %s: %w", c.ID, dir, err)
	}
	if len(diagnostics) > 0 {
		return &Error{Configuration: c.ID, Diagnostics: diagnostics}
	}

	pkgs = withoutTestBinaries(pkgs)
	cgo, err := cgoOnly(pkgs, c)
	if err != nil {
		return fmt.Errorf("load %s: %w", c.ID, err)
	}
	from := unitDir{dir: dir, workspace: workspace}
	r.ExcludedByCgo = append(r.ExcludedByCgo, checkOpaqueC(ctx, r.Fset, target, from, pkgs, cgo, c)...)
	r.Programs = append(r.Programs, checkPrograms(ctx, r.Fset, from, pkgs, c)...)
	r.Packages = append(r.Packages, pkgs...)
	unit := Unit{Dir: dir, Packages: pkgs}
	if module := mainModule(pkgs); module != nil {
		unit.Path = module.Path
	}
	r.Units = append(r.Units, unit)
	return nil
}

// loadPackages resolves and type-checks every package of the module rooted at dir,
// or the packages patterns names there, under configuration c, with workspace as the child toolchain's GOWORK setting and
// modules as the further modules [typeCheck] checks whole. A load that reports errors
// returns them as diagnostics and no packages, each position the toolchain wrote
// relative to dir rewritten below prefix.
func loadPackages(ctx context.Context, fset *token.FileSet, dir string, c Configuration, workspace string, modules []string, prefix string, patterns ...string) ([]*packages.Package, []Diagnostic, error) {
	if len(patterns) == 0 {
		patterns = []string{loadPattern}
	}
	cfg := &packages.Config{
		Mode:       metadataMode,
		Context:    ctx,
		Tests:      true,
		Dir:        dir,
		Env:        loadEnv(c, workspace),
		BuildFlags: buildFlags(c.Tags),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, nil, &listError{err: err}
	}
	if workspace != workspaceOff && len(modules) == 0 {
		demoteOtherMembers(pkgs, dir)
	}
	if err := dropUnbuiltErrors(pkgs, c); err != nil {
		return nil, nil, err
	}
	if diagnostics := collect(pkgs, prefix); len(diagnostics) > 0 {
		if failure := setupFailures(diagnostics, expectedModules(ctx, dir, c, workspace, pkgs)); failure != nil {
			return nil, nil, failure
		}
		return nil, diagnostics, nil
	}
	if err := typeCheck(ctx, fset, pkgs, c.Arch, modules); err != nil {
		return nil, nil, err
	}
	if diagnostics := collect(pkgs, prefix); len(diagnostics) > 0 {
		return nil, diagnostics, nil
	}
	return pkgs, nil, nil
}

// unbuilt reports whether one package of the main module is a directory whose
// every Go file the configuration's constraints exclude, which the pattern still
// names and the configuration does not build. The toolchain reports such a
// package with a positionless error only while its build cache holds no entry for
// it, so the error is dropped and the package is read the same way on every run.
func unbuilt(p *packages.Package) bool {
	if p.Module == nil || !p.Module.Main || len(p.GoFiles) > 0 || len(p.CompiledGoFiles) > 0 || len(p.IgnoredFiles) == 0 {
		return false
	}
	return len(p.Errors) > 0 && !slices.ContainsFunc(p.Errors, func(e packages.Error) bool {
		return e.Kind != packages.ListError || e.Pos != ""
	})
}

// dropUnbuiltErrors clears the errors of every package reachable from pkgs that
// [unbuilt] or [unbuiltForCgo] reports, and returns the first error reading a
// file's imports met.
func dropUnbuiltErrors(pkgs []*packages.Package, c Configuration) error {
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
func unbuiltForCgo(p *packages.Package, c Configuration) (bool, error) {
	if len(p.GoFiles) > 0 || len(p.CompiledGoFiles) > 0 || len(p.Errors) == 0 ||
		slices.ContainsFunc(p.Errors, func(e packages.Error) bool { return e.Kind != packages.ListError || e.Pos != "" }) {
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
// under, with workspace as the child toolchain's GOWORK setting.
//
// os/exec keeps the last value of a repeated key, so the settings pinned here win
// over the inherited environment. An ambient go.work or GOFLAGS reaches the child
// toolchain and changes which packages and which files load, which would make one
// module's result depend on where the run was started, and GOPROXY=off is what
// makes the analysis's no-network rule the toolchain's rule too: a module the
// local cache does not hold ends the load with the toolchain's own message
// instead of being fetched.
func loadEnv(c Configuration, workspace string) []string {
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
