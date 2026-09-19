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

// ClassOf is the reachability class of one declaration under this run.
//
// A subject the inventory holds no declaration for is certain: a source file, a
// module requirement and a directive of the module file have no visibility, so
// there is no question about callers the analysis cannot see.
//
// An unexported declaration is certain, because every reference to it is inside
// the module the analysis loaded. An exported one in a package nothing outside can
// import is certain for the same reason: a main package, an external test package
// and an internal tree have no importer the analysis cannot see. A type parameter
// of a function or a method is certain whatever the target is, because nothing
// outside the declaration that introduces it can name it: a caller supplies a type
// argument by position. An exported declaration of an application is certain,
// because an application's callers are all in the graph.
//
// That leaves an exported declaration of a library's importable surface, which is
// certain where every consumer the scope declared loaded, probable where consumers
// are declared and not all of them loaded, and possible where the run has no
// consumer information at all. Whether the configuration declares the consumer set
// complete decides nothing here: completeness is what opens the narrowing kinds on
// a published API, and a consumer set the run loaded whole is what the class is
// about.
func (in *Input) ClassOf(id graph.SymbolID) Class {
	symbol := in.symbol(id)
	switch {
	case symbol == nil || !symbol.Exported:
		return Certain
	case symbol.Kind == graph.KindTypeParam && declaresTypeParameters(in.symbol(symbol.Parent)):
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

// everyConsumerLoaded reports whether the run declared at least one consumer and
// loaded every one it declared, which is what makes a library's published surface
// as known as an application's.
//
// A run that declared none has no consumer information, whatever the configuration
// says about the set being complete: the class is what the analysis loaded rather
// than what the configuration asserts.
func (in *Input) everyConsumerLoaded() bool {
	if len(in.Consumers.Declared) == 0 {
		return false
	}
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
// narrowing kinds need and the fact the severity map reads.
//
// It is not the reachability class's rule: a published surface whose declared
// consumers all loaded is as known as an application's whether or not the set is
// declared complete, while narrowing a published declaration needs the assertion
// that no consumer outside the set exists.
func (in *Input) consumersLoaded() bool {
	if !in.Consumers.Complete {
		return false
	}
	for _, declared := range in.Consumers.Declared {
		if !slices.Contains(in.Consumers.Loaded, declared) {
			return false
		}
	}
	return true
}
