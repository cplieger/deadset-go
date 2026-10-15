package kinds

import (
	"go/types"
	"iter"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// internalElement is the path element that makes a package importable only from
// inside the module tree above it.
const internalElement = "internal"

// testPackageSuffix ends the import path of an external test package, which
// nothing can import at all.
const testPackageSuffix = "_test"

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

// lower returns the weaker of two classes, and c where other is not a class.
func lower(c, other vocab.Class) vocab.Class {
	if rank(other) == 0 || rank(c) <= rank(other) {
		return c
	}
	return other
}

// classOf is the reachability class of one declaration under this run: possible for
// a declaration of an importable package of an application that no build links, in a
// run that declares no consumer, and for an importable export of a module a dropped
// module requires. Otherwise it is certain for every application subject, an
// unexported, test-file or non-importable declaration, a function's type parameter
// and a member [Input.hiddenMember] reports; a library's other export is certain where
// every declared consumer loaded, probable where some did not, and possible with no
// consumer information.
func (in *Input) classOf(id graph.SymbolID) vocab.Class {
	symbol := in.symbol(id)
	switch {
	case symbol == nil:
		return vocab.Certain
	case in.unlinkedApplicationPackage(symbol):
		return vocab.Possible
	case !symbol.Exported:
		return vocab.Certain
	case symbol.Kind == graph.KindTypeParam && declaresTypeParameters(in.symbol(symbol.Parent)):
		return vocab.Certain
	case testFile(symbol):
		return vocab.Certain
	case !in.importable(symbol):
		return vocab.Certain
	case in.hiddenMember(symbol):
		return vocab.Certain
	case in.unanalysedConsumer(symbol):
		return vocab.Possible
	case in.Config == nil || in.Config.Target.Kind.Of(vocab.Go) != config.Library:
		return vocab.Certain
	case in.everyConsumerLoaded():
		return vocab.Certain
	case len(in.Consumers.Declared) > 0:
		return vocab.Probable
	default:
		return vocab.Possible
	}
}

// unlinkedApplicationPackage reports whether an importable package of an
// application that no build links declares one symbol, in a run that declares no
// consumer: outside programs may import it, so every declaration of it is possible.
func (in *Input) unlinkedApplicationPackage(symbol *graph.Symbol) bool {
	if in.Config == nil || in.Config.Target.Kind.Of(vocab.Go) != config.Application ||
		len(in.Consumers.Declared) > 0 || testFile(symbol) || !in.importable(symbol) {
		return false
	}
	held := in.index()
	if held.unlinked == nil {
		held.unlinked = unlinkedPackages(in.Per, in.Root, held)
	}
	return held.unlinked[in.packageOf(symbol)]
}

// unlinkedPackages is every package no build links in any configuration that loads
// it, by [index.packageKey].
func unlinkedPackages(per []Configured, root string, x *index) map[string]bool {
	unlinked := make(map[string]bool)
	linked := make(map[string]bool)
	for _, one := range per {
		if one.Result == nil {
			continue
		}
		none := graph.UnlinkedPackages(one.Result)
		for _, p := range one.Result.Packages {
			if p.ForTest != "" {
				continue
			}
			key := x.packageKey(p.PkgPath, packageDir(p, root))
			if none[one.Result.KeyOf(p)] {
				unlinked[key] = true
			} else {
				linked[key] = true
			}
		}
	}
	for key := range linked {
		delete(unlinked, key)
	}
	return unlinked
}

// unanalysedConsumer reports whether the unit declaring one symbol has, under some
// configuration, a module the run dropped requiring it ([load.Unit]).
func (in *Input) unanalysedConsumer(symbol *graph.Symbol) bool {
	held := in.index()
	if held.unanalysed == nil {
		held.unanalysed = unanalysedUnits(in.Per, in.Root)
	}
	depth, flagged := -1, false
	for unit, consumed := range held.unanalysed {
		length := len(unit)
		if unit == "." {
			length = 0
		} else if !strings.HasPrefix(symbol.Pos.Filename, unit+"/") {
			continue
		}
		if length > depth {
			depth, flagged = length, consumed
		}
	}
	return flagged
}

// unanalysedUnits is every unit of every configuration by its directory relative to
// root, flagged where some configuration gave it an unanalysed consumer.
func unanalysedUnits(per []Configured, root string) map[string]bool {
	units := make(map[string]bool)
	for _, one := range per {
		if one.Result == nil {
			continue
		}
		for _, unit := range one.Result.Units {
			rel, err := filepath.Rel(root, unit.Dir)
			if err == nil {
				dir := filepath.ToSlash(rel)
				units[dir] = units[dir] || unit.UnanalysedConsumer
			}
		}
	}
	return units
}

// publishedAPI reports whether one declaration is published API of a library: an
// exported production declaration of a package outside programs can import.
func (in *Input) publishedAPI(symbol *graph.Symbol) bool {
	return in.Config != nil && in.Config.Target.Kind.Of(vocab.Go) == config.Library && symbol.Exported &&
		!testFile(symbol) && in.importable(symbol)
}

// componentCap is the lowest reachability class among the roots of the dead
// component one declaration falls with, which caps the confidence of every finding
// of that component so the minimum confidence withholds or reports it whole. It is
// no class where the declaration falls with no component.
func (in *Input) componentCap(id graph.SymbolID) vocab.Class {
	held := in.index()
	component := held.components[id]
	if component == nil {
		return ""
	}
	if least, computed := held.caps[component.Index]; computed {
		return least
	}
	least := vocab.Certain
	for _, root := range component.Roots {
		least = lower(least, in.classOf(root))
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

// importable reports whether anything outside the target module can import the
// package declaring symbol: not a main package, not an external test package, and
// not under an internal tree, which for a target analyzed alone is every internal
// package.
func (in *Input) importable(symbol *graph.Symbol) bool {
	if in.index().mains[in.packageOf(symbol)] || strings.HasSuffix(symbol.PkgPath, testPackageSuffix) {
		return false
	}
	return !slices.Contains(strings.Split(symbol.PkgPath, "/"), internalElement)
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

// hiddenMember reports whether one declaration is a field of an unexported type,
// or a method of an unexported interface, that no exported declaration of its
// package exposes, so no code outside the module holds a value to select it on. A
// concrete type's method is never hidden: a value converted to an interface carries
// it out, where an assertion calls it. A field of an anonymous struct belongs to
// the type that holds the struct.
func (in *Input) hiddenMember(symbol *graph.Symbol) bool {
	switch symbol.Kind {
	case graph.KindField, graph.KindInterfaceMethod:
	default:
		return false
	}
	owner := in.symbol(symbol.Parent)
	for owner != nil && owner.Kind == graph.KindField {
		owner = in.symbol(owner.Parent)
	}
	if owner == nil || owner.Exported || (owner.Kind != graph.KindType && owner.Kind != graph.KindInterface) {
		return false
	}
	held := in.index()
	if held.exposed == nil {
		held.exposed = exposedTypes(in)
	}
	return !held.exposed[owner.ID]
}

// exposedTypes is every type of the target that code outside its package can name
// or hold a value of: an exported type, and every type an exported declaration's
// type spells, closed over the exported fields, the embedded fields and the
// exported methods of every type it holds.
func exposedTypes(in *Input) map[graph.SymbolID]bool {
	exposed := make(map[graph.SymbolID]bool)
	for one, p := range in.typedPackages() {
		for name := range exposedIn(p.Types) {
			if id, held := one.Resolve.Object(name); held {
				exposed[id] = true
			}
		}
	}
	return exposed
}

// exposedIn is every type of one package that code outside it can name or hold a
// value of.
func exposedIn(pkg *types.Package) map[*types.TypeName]bool {
	reached := make(map[*types.TypeName]bool)
	var queue []*types.TypeName
	visit := func(t types.Type) {
		named := make(map[*types.TypeName]bool)
		typeNamesIn(named, pkg.Path(), t)
		for name := range named {
			if !reached[name] {
				reached[name] = true
				queue = append(queue, name)
			}
		}
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if object := scope.Lookup(name); object.Exported() {
			visit(object.Type())
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		visitMembers(name, visit)
	}
	return reached
}

// visitMembers visits the type of every member of one type that code outside its
// package can reach: the exported methods, the exported and embedded fields, and
// the embedded interfaces, an embedded type promoting its own exported members.
func visitMembers(name *types.TypeName, visit func(types.Type)) {
	if named, ok := name.Type().(*types.Named); ok {
		visitExported(named.Methods(), visit)
	}
	switch u := name.Type().Underlying().(type) {
	case *types.Struct:
		for field := range u.Fields() {
			if field.Exported() || field.Embedded() {
				visit(field.Type())
			}
		}
	case *types.Interface:
		visitExported(u.Methods(), visit)
		for embedded := range u.EmbeddedTypes() {
			visit(embedded)
		}
	default:
		visit(u)
	}
}

// visitExported visits the type of every exported method of a sequence.
func visitExported(methods iter.Seq[*types.Func], visit func(types.Type)) {
	for method := range methods {
		if method.Exported() {
			visit(method.Type())
		}
	}
}
