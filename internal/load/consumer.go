package load

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
)

// workspaceOff is the GOWORK setting that keeps every workspace out of a load,
// the ambient one included.
const workspaceOff = "off"

// Consumer is one loaded consumer module's contribution to a configuration: a
// declared consumer, or a [Result.Nested] one.
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
func loadConsumers(ctx context.Context, trees *syntaxCache, doc *scope.Document, c Configuration, units []Unit) ([]Consumer, error) {
	if len(doc.Consumers) == 0 {
		return nil, nil
	}
	var mains []*packages.Module
	for i := range units {
		if main := mainModule(units[i].Packages); main != nil {
			mains = append(mains, main)
		}
	}
	loaded := make([]Consumer, 0, len(doc.Consumers))
	held := make(map[string]bool, len(doc.Consumers))
	for _, declared := range doc.Consumers {
		one, err := loadConsumer(ctx, trees, doc, c, declared, mains)
		if err != nil {
			return nil, err
		}
		if held[one.ID] {
			return nil, fmt.Errorf("%w: %s: the scope declares module %s twice",
				errConsumer, declared.Path, one.ID)
		}
		held[one.ID] = true
		loaded = append(loaded, one)
	}
	return loaded, nil
}

// Referencing is every loaded consumer whose references the run counts: each declared
// one, then each nested one.
func (r *Result) Referencing() []Consumer {
	return slices.Concat(r.Consumers, r.Nested)
}

// loadNested loads every module below the target that builds against a unit's own
// directory ([replacedUnits]) as a consumer of it, in directory order, and returns
// each one that does not load as dropped, then each unit's underscored packages.
func loadNested(ctx context.Context, trees *syntaxCache, doc *scope.Document, c Configuration, units []Unit) ([]Consumer, []droppedModule, error) {
	target := doc.Target.Path
	dirs, err := nestedModules(target, units)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: find the modules under %s: %w", c.ID, target, err)
	}
	var held nestedLoads
	for _, dir := range dirs {
		consumed := replacedUnits(ctx, dir, units)
		declared := slices.ContainsFunc(doc.Consumers, func(m scope.Module) bool { return sameDir(m.Path, dir) })
		if len(consumed) == 0 || declared {
			continue
		}
		if err := held.load(ctx, trees, dir, c, workspaceOff, unitPaths(consumed), unitPrefix(target, dir)); err != nil {
			return nil, nil, err
		}
	}
	for i := range units {
		if err := held.underscored(ctx, trees, c, target, &units[i]); err != nil {
			return nil, nil, err
		}
	}
	return held.loaded, held.dropped, nil
}

// nestedLoads accumulates the [Result.Nested] consumers and the modules dropped
// while loading them.
type nestedLoads struct {
	loaded  []Consumer
	dropped []droppedModule
}

// underscored loads the packages of unit's own module below an underscore directory
// ([underscoredPackages]), an example tree among them, as a nested consumer of the
// unit. No build of the module compiles them, so where they do not load together
// each loads alone, and one that does not load counts nothing.
func (n *nestedLoads) underscored(ctx context.Context, trees *syntaxCache, c Configuration, target string, unit *Unit) error {
	if unit.Path == "" {
		return nil
	}
	patterns, err := underscoredPackages(unit.Dir)
	if err != nil || len(patterns) == 0 {
		return err
	}
	workspace, err := unitWorkspace(ctx, unit.Dir)
	if err != nil {
		return fmt.Errorf("load %s: %s: %w", c.ID, unit.Dir, err)
	}
	try := func(patterns []string) (bool, error) {
		pkgs, diagnostics, err := loadPackages(ctx, trees, unit.Dir, c, workspace, []string{unit.Path}, unitPrefix(target, unit.Dir), patterns...)
		switch {
		case err == nil && len(diagnostics) == 0:
			n.loaded = append(n.loaded, Consumer{ID: unit.Path, Packages: withoutTestBinaries(pkgs)})
			return true, nil
		case err == nil || droppable(err) && ctx.Err() == nil:
			return false, nil
		default:
			return false, fmt.Errorf("load %s: %s: %w", c.ID, unit.Dir, err)
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

// AddReaders loads every module of readers whose module file requires a unit of r,
// through the workspace file at workspace, as a [Result.Nested] consumer, in the
// order given, and drops one that does not load. A caller passes the modules a
// workspace lists that it counts the references of without naming them.
func (r *Result) AddReaders(ctx context.Context, workspace string, readers []scope.Module) error {
	if workspace == "" {
		workspace = workspaceOff
	}
	var held nestedLoads
	for _, reader := range readers {
		required, _ := requiredModules(ctx, reader.Path)
		consumed := slices.DeleteFunc(unitPaths(r.Units), func(path string) bool { return !slices.Contains(required, path) })
		if len(consumed) == 0 {
			continue
		}
		if err := held.load(ctx, r.trees, reader.Path, r.Configuration, workspace, consumed, ""); err != nil {
			return err
		}
	}
	r.Nested = append(r.Nested, held.loaded...)
	r.Dropped = append(r.Dropped, held.dropped...)
	r.markUnanalysedConsumers(ctx)
	return nil
}

// load loads the module rooted at dir as a nested consumer of the units whose module
// paths modules lists, dropping it where it does not load. A module below the target
// is named by prefix, its target-relative directory, which a note may carry.
func (n *nestedLoads) load(ctx context.Context, trees *syntaxCache, dir string, c Configuration, workspace string, modules []string, prefix string) error {
	pkgs, diagnostics, err := loadPackages(ctx, trees, dir, c, workspace, modules, prefix)
	if len(diagnostics) > 0 {
		name := prefix
		if name == "" {
			name = dir
		}
		err = &Error{Configuration: c.ID, Module: name, Diagnostics: diagnostics}
	}
	switch {
	case err == nil:
	case droppable(err) && ctx.Err() == nil:
		n.dropped = append(n.dropped, droppedModule{Dir: dir, Err: err})
		return nil
	default:
		return fmt.Errorf("load %s: %s: %w", c.ID, dir, err)
	}
	pkgs = withoutTestBinaries(pkgs)
	if module := mainModule(pkgs); module != nil {
		n.loaded = append(n.loaded, Consumer{ID: module.Path, Packages: pkgs})
	}
	return nil
}

func unitPaths(units []Unit) []string {
	paths := make([]string, 0, len(units))
	for i := range units {
		if units[i].Path != "" {
			paths = append(paths, units[i].Path)
		}
	}
	return paths
}

// loadConsumer resolves one declared consumer, and refuses one whose references
// this target's run cannot count.
func loadConsumer(ctx context.Context, trees *syntaxCache, doc *scope.Document, c Configuration, declared scope.Module, mains []*packages.Module) (Consumer, error) {
	if _, err := os.Stat(declared.Path); err != nil {
		return Consumer{}, &SetupError{Failures: []setupFailure{{Class: vocab.SetupMissingConsumer, Detail: fmt.Sprintf(
			"the declared consumer %s does not exist; check it out at that path and install its dependencies",
			declared.Path,
		)}}}
	}

	workspace := workspaceOff
	if doc.Workspace != "" {
		workspace = doc.Workspace
	}
	targetModules := make([]string, 0, len(mains))
	for _, main := range mains {
		targetModules = append(targetModules, main.Path)
	}
	pkgs, diagnostics, err := loadPackages(ctx, trees, declared.Path, c, workspace, targetModules, "")
	if setup, failed := errors.AsType[*SetupError](err); failed {
		consumer := &SetupError{}
		for _, one := range setup.Failures {
			consumer.Failures = append(consumer.Failures, setupFailure{Class: vocab.SetupMissingConsumer, Detail: fmt.Sprintf(
				"the declared consumer %s does not load (%s); check it out at that path and install its dependencies",
				declared.Path, one.Detail,
			)})
		}
		return Consumer{}, consumer
	}
	if err != nil {
		return Consumer{}, fmt.Errorf("load %s: consumer %s: %w", c.ID, declared.Path, err)
	}
	if len(diagnostics) > 0 {
		return Consumer{}, &Error{Configuration: c.ID, Module: declared.Path, Diagnostics: diagnostics}
	}
	pkgs = withoutTestBinaries(pkgs)
	module, err := consumerModuleOf(pkgs, declared, mains)
	if err != nil {
		return Consumer{}, err
	}
	return Consumer{ID: module, Packages: pkgs}, nil
}

// consumerModuleOf returns the module path of one loaded consumer, and refuses a
// consumer whose module disagrees with the scope, is a module of the target, or
// resolves a module of the target at another directory.
func consumerModuleOf(pkgs []*packages.Package, declared scope.Module, mains []*packages.Module) (string, error) {
	module := mainModule(pkgs)
	if module == nil || module.Path == "" {
		return "", fmt.Errorf("%w: %s: the load reports no module for it", errConsumer, declared.Path)
	}
	if declared.ID != "" && declared.ID != module.Path {
		return "", fmt.Errorf("%w: %s: the scope declares module %s and the directory holds %s",
			errConsumer, declared.Path, declared.ID, module.Path)
	}
	for _, main := range mains {
		if module.Path == main.Path {
			return "", fmt.Errorf("%w: %s: it is the target module %s",
				errConsumer, declared.Path, main.Path)
		}
		if err := resolvesTarget(pkgs, declared.Path, main); err != nil {
			return "", err
		}
	}
	return module.Path, nil
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
	return fmt.Errorf("%w: %s: it resolves %s at %s rather than at %s. A consumer reaches the target through a replace directive in its own module file or through a workspace the scope declares",
		errConsumer, path, main.Path, resolved, main.Dir)
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
