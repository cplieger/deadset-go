package load

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"

	"github.com/cplieger/deadset-go/internal/scope"
	"golang.org/x/tools/go/packages"
)

// workspaceOff is the GOWORK setting that keeps every workspace out of a load,
// the ambient one included.
const workspaceOff = "off"

// Consumer is one loaded consumer module's contribution to a configuration.
type Consumer struct {
	// ID is the consumer's module path as its own module file declares it, which
	// is the identifier a finding names among the consumers a run loaded.
	ID string

	// Packages holds the consumer's own packages and the test variants that
	// type-check their files again, and never a synthesized test binary. No
	// declaration of a consumer is ever reported; what these packages carry is
	// the references the consumer makes to the target.
	Packages []*packages.Package
}

// loadConsumers resolves every consumer the scope declares, in the order it
// declares them, and returns nothing when it declares none.
//
// Each consumer is one module loaded from its own directory, because a consumer
// has its own module file and its own build list, and one load runs per module
// directory. A consumer reaches the target's own directory either through its
// module file, a replace directive being the form that names a directory, or
// through the workspace the scope declares; a consumer that reaches a copy of the
// target somewhere else counts no reference against the target being analyzed, so
// it is refused rather than counted as zero.
func loadConsumers(ctx context.Context, fset *token.FileSet, doc *scope.Document, c Configuration, target []*packages.Package) ([]Consumer, error) {
	if len(doc.Consumers) == 0 {
		return nil, nil
	}
	main := mainModule(target)
	loaded := make([]Consumer, 0, len(doc.Consumers))
	held := make(map[string]bool, len(doc.Consumers))
	for _, declared := range doc.Consumers {
		one, err := loadConsumer(ctx, fset, doc, c, declared, main)
		if err != nil {
			return nil, err
		}
		if held[one.ID] {
			return nil, fmt.Errorf("%w: %s: the scope declares module %s twice",
				ErrConsumer, declared.Path, one.ID)
		}
		held[one.ID] = true
		loaded = append(loaded, one)
	}
	return loaded, nil
}

// loadConsumer resolves one declared consumer, and refuses one whose references
// this target's run cannot count.
func loadConsumer(ctx context.Context, fset *token.FileSet, doc *scope.Document, c Configuration, declared scope.Module, main *packages.Module) (Consumer, error) {
	if _, err := os.Stat(declared.Path); err != nil {
		return Consumer{}, fmt.Errorf("%w: %s: %w", ErrConsumer, declared.Path, err)
	}

	workspace := workspaceOff
	if doc.Workspace != "" {
		workspace = doc.Workspace
	}
	pkgs, err := loadPackages(ctx, fset, declared.Path, c, workspace)
	if err != nil {
		return Consumer{}, fmt.Errorf("load %s: consumer %s: %w", c.ID, declared.Path, err)
	}
	if diagnostics := collect(pkgs); len(diagnostics) > 0 {
		return Consumer{}, &Error{Configuration: c.ID, Module: declared.Path, Diagnostics: diagnostics}
	}
	pkgs = withoutTestBinaries(pkgs)

	module := mainModule(pkgs)
	if module == nil || module.Path == "" {
		return Consumer{}, fmt.Errorf("%w: %s: the load reports no module for it", ErrConsumer, declared.Path)
	}
	if declared.ID != "" && declared.ID != module.Path {
		return Consumer{}, fmt.Errorf("%w: %s: the scope declares module %s and the directory holds %s",
			ErrConsumer, declared.Path, declared.ID, module.Path)
	}
	if main != nil && module.Path == main.Path {
		return Consumer{}, fmt.Errorf("%w: %s: it is the target module %s",
			ErrConsumer, declared.Path, main.Path)
	}
	if err := resolvesTarget(pkgs, declared.Path, main); err != nil {
		return Consumer{}, err
	}
	return Consumer{ID: module.Path, Packages: pkgs}, nil
}

// resolvesTarget refuses a consumer whose module graph holds the target module at
// a directory other than the one being analyzed.
//
// A reference is a reference to the declaration written at one position, so a
// consumer that resolved the target from the module cache references a copy of
// the target's declarations rather than the target's own, and every reference it
// makes would be counted against nothing. Reporting the symbols such a consumer
// uses is the false positive this whole stage exists to remove, so the run ends
// here and the message names both directories and the two ways a consumer reaches
// the target.
//
// A consumer that imports nothing of the target holds the target module nowhere
// in its graph and is loaded as it is: a module that declares a consumer
// relationship it does not exercise contributes no reference, which is an answer
// rather than a fault.
func resolvesTarget(pkgs []*packages.Package, path string, main *packages.Module) error {
	if main == nil || main.Path == "" || main.Dir == "" {
		return nil
	}
	resolved := ""
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Module != nil && p.Module.Path == main.Path && p.Module.Dir != "" {
			resolved = p.Module.Dir
		}
	})
	if resolved == "" || sameDir(resolved, main.Dir) {
		return nil
	}
	return fmt.Errorf("%w: %s: it resolves %s at %s rather than at %s; a consumer reaches the target through a replace directive in its own module file or through a workspace the scope declares",
		ErrConsumer, path, main.Path, resolved, main.Dir)
}

// sameDir reports whether two paths name one directory, comparing them lexically
// the way every other path of a run is compared: a root spelled through a
// symbolic link is a different spelling of the same directory, and the analysis
// reads the spelling the toolchain reported.
func sameDir(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// mainModule returns the module the given root packages belong to, and nil when
// none of them names one. A load's roots are the packages of one directory tree,
// so they belong to one module; a package with no module information contributes
// none.
func mainModule(pkgs []*packages.Package) *packages.Module {
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Path != "" {
			return p.Module
		}
	}
	return nil
}
