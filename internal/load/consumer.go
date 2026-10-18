package load

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/deadset-go/v2/internal/failure"
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

// consumers resolves every consumer the topology declares, in the order the scope
// declares them, each from its own directory with its own build list, against the
// units the configuration loaded. A consumer reaches the target's directory through
// a replace directive of its module file or the workspace the scope declares; one
// reaching a copy elsewhere counts no reference, so it is refused.
func (l *loader) consumers(units []Unit) ([]Consumer, error) {
	if len(l.top.Consumers) == 0 {
		return nil, nil
	}
	var mains []*packages.Module
	for i := range units {
		if main := mainModule(units[i].Packages); main != nil {
			mains = append(mains, main)
		}
	}
	loaded := make([]Consumer, 0, len(l.top.Consumers))
	held := make(map[string]bool, len(l.top.Consumers))
	for i := range l.top.Consumers {
		declared := &l.top.Consumers[i]
		one, err := l.consumer(declared, mains)
		if err != nil {
			return nil, err
		}
		if held[one.ID] {
			return nil, fmt.Errorf("%w: %s: the scope declares module %s twice", errConsumer, declared.Dir, one.ID)
		}
		held[one.ID] = true
		loaded = append(loaded, one)
	}
	return loaded, nil
}

// consumer resolves one declared consumer, and refuses one whose references this
// target's run cannot count.
func (l *loader) consumer(declared *discoveredModule, mains []*packages.Module) (Consumer, error) {
	if _, err := os.Stat(declared.Dir); err != nil {
		return Consumer{}, &failure.Error{Failures: []failure.Failure{{Class: vocab.SetupMissingConsumer, Detail: fmt.Sprintf(
			"the declared consumer %s does not exist; check it out at that path and install its dependencies",
			declared.Dir,
		)}}}
	}
	spec := loadSpec{dir: declared.Dir, workspace: l.top.ConsumerWorkspace, file: declared.File}
	for _, main := range mains {
		spec.modules = append(spec.modules, main.Path)
	}
	pkgs, err := l.module(&spec, declared.Dir)
	if setup, failed := errors.AsType[*failure.Error](err); failed && failure.Setup(err) {
		consumer := &failure.Error{}
		for _, one := range setup.Failures {
			consumer.Failures = append(consumer.Failures, failure.Failure{Class: vocab.SetupMissingConsumer, Detail: fmt.Sprintf(
				"the declared consumer %s does not load (%s); check it out at that path and install its dependencies",
				declared.Dir, one.Detail,
			)})
		}
		return Consumer{}, consumer
	}
	if _, diagnosed := errors.AsType[*Error](err); diagnosed {
		return Consumer{}, err
	}
	if err != nil {
		return Consumer{}, fmt.Errorf("load %s: consumer %s: %w", l.c.ID, declared.Dir, err)
	}
	module, err := consumerModuleOf(pkgs, declared, mains)
	if err != nil {
		return Consumer{}, err
	}
	return Consumer{ID: module, Packages: pkgs}, nil
}

// consumerModuleOf returns the module path of one loaded consumer, and refuses a
// consumer whose module disagrees with the scope, is a module of the target, or
// resolves a module of the target at another directory.
func consumerModuleOf(pkgs []*packages.Package, declared *discoveredModule, mains []*packages.Module) (string, error) {
	module := mainModule(pkgs)
	if module == nil || module.Path == "" {
		return "", fmt.Errorf("%w: %s: the load reports no module for it", errConsumer, declared.Dir)
	}
	if declared.ID != "" && declared.ID != module.Path {
		return "", fmt.Errorf("%w: %s: the scope declares module %s and the directory holds %s",
			errConsumer, declared.Dir, declared.ID, module.Path)
	}
	for _, main := range mains {
		if module.Path == main.Path {
			return "", fmt.Errorf("%w: %s: it is the target module %s",
				errConsumer, declared.Dir, main.Path)
		}
		if err := resolvesTarget(pkgs, declared.Dir, main); err != nil {
			return "", err
		}
	}
	return module.Path, nil
}

// resolvesTarget refuses a consumer whose module graph holds the target module at
// a directory other than the one being analyzed: its references would count
// against a copy, and the symbols it uses would be reported. A consumer that
// imports nothing of the target holds the module nowhere and contributes no
// reference, which is an answer.
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
