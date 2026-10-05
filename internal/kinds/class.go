package kinds

import (
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
)

// internalElement is the path element that makes a package importable only from
// inside the module tree above it.
const internalElement = "internal"

// testPackageSuffix ends the import path of an external test package, which
// nothing can import at all.
const testPackageSuffix = "_test"

// mainPackageName is the name of a package nothing can import.
const mainPackageName = "main"

// rank orders the classes, certain above probable above possible, so the lower of
// two is the weaker claim. A value outside the set ranks below every class, which
// is what leaves an unset ceiling capping nothing.
func (c Class) rank() int {
	switch c {
	case Certain:
		return 3
	case Probable:
		return 2
	case Possible:
		return 1
	default:
		return 0
	}
}

// lower returns the weaker of two classes, and c where other is not a class.
func (c Class) lower(other Class) Class {
	if other.rank() == 0 || c.rank() <= other.rank() {
		return c
	}
	return other
}

// ClassOf is the reachability class of one declaration under this run. It is
// certain wherever no reference can come from outside the graph: a subject with no
// declaration, an unexported declaration, a declaration of a test file, a type
// parameter of a function or method, any declaration of an application, and an
// exported one in a main package, an external test package or an internal tree. An
// exported declaration of a library's importable surface is certain where every
// declared consumer loaded, probable where some did not, and possible with no
// consumer information; whether the consumer set is declared complete decides nothing.
func (in *Input) ClassOf(id graph.SymbolID) Class {
	symbol := in.symbol(id)
	switch {
	case symbol == nil || !symbol.Exported:
		return Certain
	case symbol.Kind == graph.KindTypeParam && declaresTypeParameters(in.symbol(symbol.Parent)):
		return Certain
	case testFile(symbol):
		return Certain
	case !in.importable(symbol.PkgPath):
		return Certain
	case in.Config == nil || in.Config.Target.Kind != config.Library:
		return Certain
	case in.everyConsumerLoaded():
		return Certain
	case len(in.Consumers.Declared) > 0:
		return Probable
	default:
		return Possible
	}
}

// componentCap is the lowest reachability class among the roots of the dead
// component one declaration falls with, which caps the confidence of every finding
// of that component so the minimum confidence withholds or reports it whole. It is
// no class where the declaration falls with no component.
func (in *Input) componentCap(id graph.SymbolID) Class {
	held := in.index()
	component := held.components[id]
	if component == nil {
		return ""
	}
	if least, computed := held.caps[component.Index]; computed {
		return least
	}
	least := Certain
	for _, root := range component.Roots {
		least = least.lower(in.ClassOf(root))
	}
	held.caps[component.Index] = least
	return least
}

// everyConsumerLoaded reports whether the run declared at least one consumer and
// loaded every one it declared, which is what makes a library's published surface
// as known as an application's.
//
// A run that declared none has no consumer information, whatever the configuration
// says about the set being complete: the class is what the analysis loaded rather
// than what the configuration asserts.
func (in *Input) everyConsumerLoaded() bool {
	return len(in.Consumers.Declared) > 0 && in.everyDeclaredLoaded()
}

// everyDeclaredLoaded reports whether the load resolved every consumer the scope
// declared, which is vacuously true of a scope that declared none. It is the one
// reading of the loaded set the three consumer facts of this file share.
func (in *Input) everyDeclaredLoaded() bool {
	for _, declared := range in.Consumers.Declared {
		if !slices.Contains(in.Consumers.Loaded, declared) {
			return false
		}
	}
	return true
}

// importable reports whether anything outside the target module can import one
// package: not a main package, not an external test package, and not under an
// internal tree, which for a target analyzed alone is every internal package.
func (in *Input) importable(pkgPath string) bool {
	if in.index().mains[pkgPath] || strings.HasSuffix(pkgPath, testPackageSuffix) {
		return false
	}
	return !slices.Contains(strings.Split(pkgPath, "/"), internalElement)
}

// consumersLoaded reports whether the configuration declares the consumer set
// complete and every consumer the run declared loaded, which is the closed world the
// narrowing kinds need: narrowing a published declaration says no consumer outside
// the set exists, and only the declaration supplies that.
//
// It is not the reachability class's rule: a published surface whose declared
// consumers all loaded is as known as an application's whether or not the set is
// declared complete, which is what the class is about.
func (in *Input) consumersLoaded() bool {
	return in.Consumers.Complete && in.everyDeclaredLoaded()
}
