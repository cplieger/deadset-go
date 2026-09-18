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
	"runtime"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/scope"
	"golang.org/x/tools/go/packages"
)

// loadMode is the information one configuration's load needs.
//
// NeedTypesInfo carries Uses, Defs, Implicits and Selections, which are the
// reference pass. NeedForTest populates Package.ForTest, which names the package
// a test variant belongs to. NeedFiles populates IgnoredFiles, where a file a
// build constraint excluded appears. NeedModule gives the module path a symbol
// reference is built from. NeedEmbedFiles is absent because nothing reads it.
const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo |
	packages.NeedImports | packages.NeedDeps | packages.NeedModule |
	packages.NeedForTest

// loadPattern matches every package of the target directory's module tree.
const loadPattern = "./..."

// The spelling of the test binary a test load synthesizes: a main package whose
// import path is the tested package's path with this suffix.
const (
	testBinarySuffix = ".test"
	mainPackageName  = "main"
)

var (
	// ErrConfiguration reports a configuration missing its identifier, its
	// operating system or its architecture.
	ErrConfiguration = errors.New("load: incomplete configuration")

	// ErrConsumersUnimplemented reports a scope document declaring consumers.
	// Loading them is not implemented, and a run that counted no reference from
	// a declared consumer would report symbols those consumers use, so the load
	// refuses the document instead of loading the target alone.
	ErrConsumersUnimplemented = errors.New("load: consumer loading is unimplemented")
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
	Fset     *token.FileSet // one FileSet for the whole configuration

	// ExcludedByCgo holds the target-relative paths, forward slashes, of the
	// files the toolchain ignored solely because they import "C". References
	// those files make are outside the reference set, which is a declared limit
	// of the run rather than a file nothing builds.
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

// Load resolves configuration c of doc's target. Cancelling ctx stops the load.
//
// Every package's Errors is walked, dependencies, test variants and synthesized
// test binaries included, and a non-empty set returns a *[Error] carrying all of
// them together with a zero Result, so nothing downstream can compute a finding
// from a partial load. The test binaries are then dropped from the Result. Load
// makes no network request, writes no cache, and pins the toolchain settings that
// decide what loads whatever the environment says, so it needs no C toolchain and
// no caller has to neutralise its own environment first.
func Load(ctx context.Context, doc scope.Document, c Configuration) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("load %s: %w", c.ID, err)
	}
	if c.ID == "" || c.OS == "" || c.Arch == "" {
		return Result{}, fmt.Errorf("%w: id=%q os=%q arch=%q", ErrConfiguration, c.ID, c.OS, c.Arch)
	}
	if len(doc.Consumers) > 0 {
		return Result{}, fmt.Errorf("%w: %s", ErrConsumersUnimplemented, doc.Consumers[0].Path)
	}

	target := doc.Target.Path
	if target == "" {
		return Result{}, fmt.Errorf("load %s: %w", c.ID, scope.ErrNoTarget)
	}
	if _, err := os.Stat(target); err != nil {
		return Result{}, fmt.Errorf("load %s: target %s: %w", c.ID, target, err)
	}

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode:    loadMode,
		Context: ctx,
		Tests:   true,
		Dir:     target,
		// os/exec keeps the last value of a repeated key, so these win over the
		// inherited environment. An ambient go.work or GOFLAGS reaches the child
		// toolchain and changes which packages and which files load, which would
		// make one target's result depend on where the run was started.
		Env: append(os.Environ(),
			"CGO_ENABLED=0", "GOOS="+c.OS, "GOARCH="+c.Arch, "GOWORK=off", "GOFLAGS="),
		BuildFlags: buildFlags(c.Tags),
		Fset:       fset,
	}
	pkgs, err := packages.Load(cfg, loadPattern)
	if err != nil {
		return Result{}, fmt.Errorf("load %s: %s: %w", c.ID, target, err)
	}

	if diagnostics := collect(pkgs); len(diagnostics) > 0 {
		return Result{}, &Error{Configuration: c.ID, Diagnostics: diagnostics}
	}

	pkgs = withoutTestBinaries(pkgs)
	excluded, err := excludedByCgo(target, pkgs, c)
	if err != nil {
		return Result{}, fmt.Errorf("load %s: %w", c.ID, err)
	}

	return Result{
		Configuration: c,
		Packages:      pkgs,
		Fset:          fset,
		ExcludedByCgo: excluded,
	}, nil
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
// deterministic order.
func collect(roots []*packages.Package) []Diagnostic {
	var diagnostics []Diagnostic
	packages.Visit(roots, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			diagnostics = append(diagnostics, Diagnostic{
				Package:  p.ID,
				Position: e.Pos,
				Message:  e.Msg,
			})
		}
	})
	slices.SortFunc(diagnostics, compareDiagnostics)
	return slices.CompactFunc(diagnostics, sameDiagnostic)
}
