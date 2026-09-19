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
// An unexported declaration is certain, because every reference to it is inside
// the module the analysis loaded. An exported one in a package nothing outside can
// import is certain for the same reason: a main package, an external test package
// and an internal tree have no importer the analysis cannot see. An exported one of
// an application is certain, because an application's callers are all in the
// graph. That leaves an exported declaration of a library's importable surface,
// which is certain when the configuration declares the consumer set complete and
// every declared consumer loaded, probable when consumers are declared and not all
// loaded, and possible when the run has no consumer information at all.
func (in *Input) ClassOf(id graph.SymbolID) Class {
	symbol := in.symbol(id)
	switch {
	case symbol == nil || !symbol.Exported:
		return Certain
	case !in.importable(symbol.PkgPath):
		return Certain
	case in.Config == nil || in.Config.Target.Kind != config.Library:
		return Certain
	case in.consumersLoaded():
		return Certain
	case len(in.Consumers.Declared) > 0:
		return Probable
	default:
		return Possible
	}
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

// consumersLoaded reports whether every consumer the run declared loaded and the
// configuration declares the set complete, which is the run's own fact the
// severity map and the reachability class both read.
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
