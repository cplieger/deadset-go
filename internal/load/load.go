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
	Packages      []*packages.Package // the target's packages, test variants included
	Fset          *token.FileSet      // one FileSet for the whole configuration

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
// Every package's Errors is walked, dependencies and test variants included, and
// a non-empty set returns a *[Error] carrying all of them together with a zero
// Result, so nothing downstream can compute a finding from a partial load. Load
// makes no network request, writes no cache, and loads with cgo disabled
// whatever the environment says, so it needs no C toolchain.
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
		Mode:       loadMode,
		Context:    ctx,
		Tests:      true,
		Dir:        target,
		Env:        append(os.Environ(), "CGO_ENABLED=0", "GOOS="+c.OS, "GOARCH="+c.Arch),
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
