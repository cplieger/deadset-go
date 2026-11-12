package findings

import (
	"path"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// mainPackageName is the name of a package nothing can import.
const mainPackageName = "main"

// classOrder is the classes from the most to the least certain.
var classOrder = vocab.Classes()

// rank orders the classes, certain above probable above possible, so the lower of
// two is the weaker claim. A value outside the set ranks below every class, which
// is what leaves an unset ceiling capping nothing.
func rank(c vocab.Class) int {
	at := slices.Index(classOrder, c)
	if at < 0 {
		return 0
	}
	return len(classOrder) - at
}

// Withholds reports whether a minimum confidence withholds a finding of class c. A
// minimum outside the set withholds nothing.
func Withholds(minimum, c vocab.Class) bool { return rank(c) < rank(minimum) }

// lower returns the weaker of two classes, and c where other is not a class.
func lower(c, other vocab.Class) vocab.Class {
	if rank(other) == 0 || rank(c) <= rank(other) {
		return c
	}
	return other
}

// classOf is the reachability class of one declaration under this run: possible in
// a package beside code the analysis does not see, in an importable package of an
// application no build links in a run declaring no consumer, and for an importable
// export of a module a dropped module requires. Otherwise it is certain for every
// application subject, an unexported, test-file or non-importable declaration, a
// type parameter and a member [pass.hiddenMember] reports; a library's other export
// is certain where every declared consumer loaded, probable where some did not, and
// possible with no consumer information.
func (in *pass) classOf(id inventory.SymbolID) vocab.Class {
	symbol := in.symbol(id)
	switch {
	case symbol == nil:
		return vocab.Certain
	case in.obscured[path.Dir(symbol.File)]:
		return vocab.Possible
	case in.unlinkedApplicationPackage(symbol):
		return vocab.Possible
	case !symbol.Exported:
		return vocab.Certain
	case in.typeParameterSubject(symbol):
		return vocab.Certain
	case symbol.InTestFile:
		return vocab.Certain
	case !in.importable(symbol):
		return vocab.Certain
	case in.hiddenMember(symbol):
		return vocab.Certain
	case in.unanalysedConsumer(symbol):
		return vocab.Possible
	case in.Config == nil || in.Config.Target.Kind != report.TargetKindLibrary:
		return vocab.Certain
	case in.everyConsumerLoaded():
		return vocab.Certain
	case len(in.Consumers.Declared) > 0:
		return vocab.Probable
	default:
		return vocab.Possible
	}
}

// Unseen is every production declaration of the inventory whose reachability class
// is possible, which programs outside the analysis may call: the sweep counts each
// as a live target of a test that references it. It reads no sweep.
func Unseen(in *Input) (map[inventory.SymbolID]bool, error) {
	a, err := classesOf(in)
	if err != nil {
		return nil, err
	}
	unseen := make(map[inventory.SymbolID]bool)
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if !symbol.InTestFile && !symbol.TestSupport && a.classOf(symbol.ID) == vocab.Possible {
			unseen[symbol.ID] = true
		}
	}
	return unseen, nil
}

// unlinkedApplicationPackage reports whether an importable package of an
// application that no build links declares one symbol, in a run that declares no
// consumer: outside programs may import it, so every declaration of it is possible.
func (in *pass) unlinkedApplicationPackage(symbol *inventory.Symbol) bool {
	if in.Config == nil || in.Config.Target.Kind != report.TargetKindApplication ||
		len(in.Consumers.Declared) > 0 || symbol.InTestFile || !in.importable(symbol) {
		return false
	}
	return in.unlinked[in.packageOf(symbol)]
}

// unanalysedConsumer reports whether the unit declaring one symbol has, under some
// configuration, a module the run dropped requiring it ([load.Unit]).
func (in *pass) unanalysedConsumer(symbol *inventory.Symbol) bool {
	depth, flagged := -1, false
	for unit, consumed := range in.unanalysed {
		length := len(unit)
		if unit == "." {
			length = 0
		} else if !strings.HasPrefix(symbol.File, unit+"/") {
			continue
		}
		if length > depth {
			depth, flagged = length, consumed
		}
	}
	return flagged
}

// publishedAPI reports whether one declaration is published API of a library: an
// exported production declaration of a package outside programs can import.
func (in *pass) publishedAPI(symbol *inventory.Symbol) bool {
	return in.Config != nil && in.Config.Target.Kind == report.TargetKindLibrary && symbol.Exported &&
		!symbol.InTestFile && in.importable(symbol)
}

// everyConsumerLoaded reports whether the run declared at least one consumer and
// loaded every one it declared, which is what makes a library's published surface
// as known as an application's.
//
// A run that declared none has no consumer information, whatever the configuration
// says about the set being complete: the class is what the analysis loaded rather
// than what the configuration asserts.
func (in *pass) everyConsumerLoaded() bool {
	return len(in.Consumers.Declared) > 0 && in.everyDeclaredLoaded()
}

// everyDeclaredLoaded reports whether the load resolved every consumer the scope
// declared, which is vacuously true of a scope that declared none. It is the one
// reading of the loaded set the three consumer facts of this file share.
func (in *pass) everyDeclaredLoaded() bool {
	for _, declared := range in.Consumers.Declared {
		if !slices.Contains(in.Consumers.Loaded, declared) {
			return false
		}
	}
	return true
}

// importable reports whether anything outside the target module can import the
// package declaring symbol: not a main package, not an external test package, and
// not under an internal tree, which for a target analyzed alone is every internal
// package.
func (in *pass) importable(symbol *inventory.Symbol) bool {
	return load.Published(symbol.PkgPath, in.mains[in.packageOf(symbol)])
}

// consumersLoaded reports whether the configuration declares the consumer set
// complete and every consumer the run declared loaded, which is the closed world the
// narrowing kinds need: narrowing a published declaration says no consumer outside
// the set exists, and only the declaration supplies that.
//
// It is not the reachability class's rule: a published surface whose declared
// consumers all loaded is as known as an application's whether or not the set is
// declared complete, which is what the class is about.
func (in *pass) consumersLoaded() bool {
	return in.Consumers.Complete && in.everyDeclaredLoaded()
}

// hiddenMember reports whether one declaration is a field of an unexported type,
// or a method of an unexported interface, that the published API does not reach, so
// no code outside the module holds a value to select it on. A
// concrete type's method is never hidden: a value converted to an interface carries
// it out, where an assertion calls it. A field of an anonymous struct belongs to
// the type that holds the struct.
func (in *pass) hiddenMember(symbol *inventory.Symbol) bool {
	switch symbol.Kind {
	case inventory.KindField, inventory.KindInterfaceMethod:
	default:
		return false
	}
	owner := in.container(symbol)
	for owner != nil && owner.Kind == inventory.KindField {
		owner = in.container(owner)
	}
	if owner == nil || owner.Exported || (owner.Kind != inventory.KindType && owner.Kind != inventory.KindInterface) {
		return false
	}
	return !in.exposed[owner.ID]
}
