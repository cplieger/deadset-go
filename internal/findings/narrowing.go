package findings

import (
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The narrower visibilities a narrowing finding names, each the narrowest of the
// three scopes that contains every reference to the subject.
const (
	visibilityFile    = "file"
	visibilityPackage = "package"
	visibilityModule  = "module"
)

// narrowScope is how far the references to one declaration reach.
type narrowScope uint8

// The scopes, ordered from the narrowest, so the wider of two reaches is the
// larger value.
const (
	scopeFile narrowScope = iota
	scopePackage
	scopeModule
	scopeOutside
)

// narrowing is one run's reference spread, folded once over the matrix inventory,
// with the declarations no narrowing may report.
type narrowing struct {
	in           *pass
	reach        map[inventory.SymbolID]narrowScope
	named        map[inventory.SymbolID]int
	fromTest     map[inventory.SymbolID]int
	unnarrowable map[inventory.SymbolID]bool

	// exposed is how far code reaches each type's values through its own package's
	// exported declarations ([exposureOf]).
	exposed map[inventory.SymbolID]narrowScope
}

// newNarrowing folds every reference into the spread of the declaration it names,
// and keys as unnarrowable the declarations whose references are not the whole
// story: one an exemption retains (the sweep's own set, which holds exemptions on
// live declarations too), one the write-only kind reports, a method satisfying an
// interface the target uses as a type, a method an outside interface fixes
// ([readFixed]), and a root a driver finds by its exported name ([driverNamed]). A
// declared cross-language edge is a reference from outside, and its narrowing
// finding becomes the edge's pending evaluation.
func newNarrowing(in *pass) *narrowing {
	n := &narrowing{
		in:           in,
		reach:        make(map[inventory.SymbolID]narrowScope),
		named:        make(map[inventory.SymbolID]int),
		fromTest:     make(map[inventory.SymbolID]int),
		unnarrowable: make(map[inventory.SymbolID]bool, len(in.exempted)+len(in.writeOnly)+len(in.fixed)),
	}
	if in.Merged == nil {
		return n
	}
	for _, held := range []map[inventory.SymbolID]bool{in.exempted, in.fixed} {
		for id := range held {
			n.unnarrowable[id] = true
		}
	}
	for id := range in.writeOnly {
		n.unnarrowable[id] = true
	}
	for i := range in.Merged.Roots {
		if root := &in.Merged.Roots[i]; driverNamed(root.Kind) {
			n.unnarrowable[root.ID] = true
		}
	}
	for i := range in.Merged.References {
		n.widen(&in.Merged.References[i])
	}
	n.exposed = exposureOf(in.carriers, n.reach)
	for id := range in.satisfying {
		n.unnarrowable[id] = true
	}
	return n
}

// driverNamed reports whether a root of kind is one a driver finds by its exported
// name, so unexporting it stops the driver from running it whatever else refers to
// it: the go test driver's tests, benchmarks, fuzz tests, examples and suite
// methods, and go-fuzz's entry points.
func driverNamed(kind roots.RootKind) bool {
	return kind == roots.RootTest || kind == roots.RootFuzz
}

// widen folds one reference into the spread of the declaration it names.
//
// Every reference counts, whichever file made it, because the scope a narrowing
// names is the scope the source writes and a test file of the target is inside it.
func (n *narrowing) widen(r *facts.Reference) {
	to := n.in.symbol(r.To)
	if to == nil {
		return
	}
	n.named[r.To]++
	if r.Test {
		n.fromTest[r.To]++
	}
	n.reach[r.To] = max(n.reach[r.To], n.scopeOf(to, r))
}

// readSatisfying keys every method of the target one configuration compiles that
// satisfies an interface the target declares and something names as a type, an
// assertion of satisfaction included: unexporting it takes the type out of the
// interface's implementors, so the conversion stops compiling. Another module's
// interface is the interface satisfaction class's.
func (in *pass) readSatisfying(one *Configured) {
	if one.Result == nil || one.Resolve == nil {
		return
	}
	for _, iface := range one.Facts.Interfaces() {
		if len(in.interfaces.typeUsedBy[iface.ID]) == 0 {
			continue
		}
		for _, impl := range iface.Implementations {
			for _, id := range impl.Implementors {
				if id != "" {
					in.satisfying[id] = true
				}
			}
		}
	}
}

// scopeOf answers how far one reference to a declaration reaches, which the
// referencing declaration decides: one the inventory does not hold is a loaded
// consumer's, outside the module, or a [load.TestBuild] package's, another
// package of the module, and an external test package is a package of its own.
func (n *narrowing) scopeOf(to *inventory.Symbol, r *facts.Reference) narrowScope {
	from := n.in.symbol(r.From)
	switch {
	case from == nil && r.Build == load.TestBuild:
		return scopeModule
	case from == nil:
		return scopeOutside
	case from.PkgPath != to.PkgPath:
		return scopeModule
	case from.File != to.File:
		return scopePackage
	default:
		return scopeFile
	}
}

// closed reports whether every reference to one declaration that can exist is one
// this run loaded.
//
// A package nothing outside can import is closed whatever the run knows about
// consumers. An importable one is closed only where the configuration declares the
// consumer set complete and every declared consumer loaded, because the callers of a
// published package are otherwise unknown.
func (in *pass) closed(symbol *inventory.Symbol) bool {
	return !in.importable(symbol) || in.consumersLoaded()
}

// narrowable reports whether one declaration is a subject of the two narrowing
// kinds: an exported production declaration of a closed world that a reference names,
// that the sweep found live, and that nothing beyond the reference set speaks for.
// A type parameter, an interface method and a struct field never are: a caller
// supplies a type argument by position, an interface method's exportedness is its
// implementors' contract, and an encoder, a template or a reflective lookup names a
// field by string where no reference does.
func (n *narrowing) narrowable(symbol *inventory.Symbol) bool {
	switch {
	case !narrowableShape(symbol):
		return false
	case n.unnarrowable[symbol.ID]:
		return false
	case !n.in.closed(symbol), n.in.candidateOf(symbol.ID) != nil, symbol.TestSupport:
		return false
	default:
		return n.named[symbol.ID] > 0
	}
}

// narrowableShape reports whether one declaration's shape admits it to both
// narrowing kinds' population: an exported declaration that is no type parameter, no
// method an interface declares and no field of a struct.
func narrowableShape(symbol *inventory.Symbol) bool {
	switch symbol.Kind {
	case inventory.KindTypeParam, inventory.KindInterfaceMethod, inventory.KindField:
		return false
	default:
		return symbol.Exported
	}
}

// subjects lists the declarations the two narrowing kinds judge, in the order the
// inventory holds them.
func (n *narrowing) subjects() []*inventory.Symbol {
	var held []*inventory.Symbol
	for i := range n.in.Merged.Symbols {
		if symbol := &n.in.Merged.Symbols[i]; n.narrowable(symbol) {
			held = append(held, symbol)
		}
	}
	return held
}

// unnecessaryExport reports an exported declaration whose every reference is inside
// the package that declares it, naming the narrower visibility they support: the
// file, or the package. It reports only where the run loaded every reference that can
// exist: a published package's only under a consumer set declared complete and
// loaded whole, while a main package, an external test package and an internal tree
// are closed.
func unnecessaryExport(in *pass) ([]draft, error) {
	if in == nil || in.Merged == nil || in.Sweep == nil {
		return nil, nil
	}
	n := in.narrowed
	subjects := n.subjects()
	found := make([]draft, 0, len(subjects))
	for _, symbol := range subjects {
		visibility := ""
		switch n.reach[symbol.ID] {
		case scopeFile:
			visibility = visibilityFile
		case scopePackage:
			visibility = visibilityPackage
		case scopeModule, scopeOutside:
			continue
		}
		if n.exposed[symbol.ID] >= scopeModule {
			continue
		}
		message := "exported " + in.named(symbol.ID) +
			" is referenced only inside the " + visibility + " that declares it"
		if one, held := n.finding(symbol, vocab.UnnecessaryExport, visibility, message); held {
			found = append(found, one)
		}
	}
	return found, nil
}

// unnecessaryExposure reports an exported declaration of a package an importer
// outside the module can name whose every reference is inside the target module, so
// the declaration can move behind an internal boundary.
//
// It reports under the same closed-world precondition UnnecessaryExport does.
func unnecessaryExposure(in *pass) ([]draft, error) {
	if in == nil || in.Merged == nil || in.Sweep == nil {
		return nil, nil
	}
	n := in.narrowed
	subjects := n.subjects()
	found := make([]draft, 0, len(subjects))
	for _, symbol := range subjects {
		if !in.importable(symbol) || n.reach[symbol.ID] != scopeModule || n.exposed[symbol.ID] >= scopeOutside {
			continue
		}
		message := "exported " + in.named(symbol.ID) + " is referenced only inside this module"
		if one, held := n.finding(symbol, vocab.UnnecessaryExposure, visibilityModule, message); held {
			found = append(found, one)
		}
	}
	return found, nil
}

// carrier is one declaration through which code reaches a type's values without
// naming the type. A type carrier passes on how far its own values reach.
type carrier struct {
	id     inventory.SymbolID
	isType bool
}

// readCarriers keys every type of the target by its carriers in its own package
// in one configuration, adding to held: an exported function, method, variable or
// constant whose type names it, an exported field whose type names it, an exported
// member of the type itself, and a type whose definition names it ([typeNamesIn]).
func readCarriers(one *Configured, held map[inventory.SymbolID][]carrier) {
	if one.Facts == nil {
		return
	}
	for p := range one.Facts.Typed() {
		k := carrierKeys{held: held, one: one, path: p.Types.Path()}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			switch object := scope.Lookup(name).(type) {
			case *types.TypeName:
				k.typeName(object)
			case *types.Func, *types.Var, *types.Const:
				if object.Exported() {
					k.names(object, false, object.Type())
				}
			}
		}
	}
}

// carrierKeys adds the carriers one package of one configuration declares.
type carrierKeys struct {
	held map[inventory.SymbolID][]carrier
	one  *Configured
	path string
}

// names keys every type of the package t spells to the carrier object.
func (k *carrierKeys) names(object types.Object, isType bool, t types.Type) {
	spelled := make(map[*types.TypeName]bool)
	typeNamesIn(spelled, k.path, t)
	for name := range spelled {
		if name != object {
			k.add(name, object, isType)
		}
	}
}

// add keys the type name to the carrier object, both where the inventory holds them.
func (k *carrierKeys) add(name *types.TypeName, object types.Object, isType bool) {
	id, inventoried := k.one.Resolve.Object(name)
	by, held := k.one.Resolve.Object(object)
	if inventoried && held && !slices.Contains(k.held[id], carrier{id: by, isType: isType}) {
		k.held[id] = append(k.held[id], carrier{id: by, isType: isType})
	}
}

// typeName keys the carriers one type declares: its definition, for every type it
// names, and its exported members, for itself and for the types they name.
func (k *carrierKeys) typeName(object *types.TypeName) {
	if object.IsAlias() {
		k.names(object, true, types.Unalias(object.Type()))
		return
	}
	defined, isNamed := object.Type().(*types.Named)
	if !isNamed {
		return
	}
	k.names(object, true, defined.Underlying())
	for method := range defined.Methods() {
		if method.Exported() {
			k.add(object, method, false)
			k.names(method, false, method.Signature())
		}
	}
	switch underlying := defined.Underlying().(type) {
	case *types.Struct:
		k.fields(object, underlying)
	case *types.Interface:
		for method := range underlying.ExplicitMethods() {
			if method.Exported() {
				k.add(object, method, false)
				k.names(method, false, method.Signature())
			}
		}
	}
}

// fields keys owner to each exported field of st, and of each struct with no name
// such a field holds, and each type a field names to that field.
func (k *carrierKeys) fields(owner *types.TypeName, st *types.Struct) {
	for field := range st.Fields() {
		if !field.Exported() {
			continue
		}
		k.add(owner, field, false)
		k.names(field, false, field.Type())
		if inner, unnamed := types.Unalias(field.Type()).(*types.Struct); unnamed {
			k.fields(owner, inner)
		}
	}
}

// exposureOf is how far code reaches each type's values through its carriers: the
// widest reach of a carrier, a type carrier's own exposure included, solved to a
// fixpoint because a type's definition passes its exposure on.
func exposureOf(carriers map[inventory.SymbolID][]carrier, reach map[inventory.SymbolID]narrowScope) map[inventory.SymbolID]narrowScope {
	exposed := make(map[inventory.SymbolID]narrowScope, len(carriers))
	for changed := true; changed; {
		changed = false
		for id, held := range carriers {
			for _, c := range held {
				widest := reach[c.id]
				if c.isType {
					widest = max(widest, exposed[c.id])
				}
				if widest > exposed[id] {
					exposed[id], changed = widest, true
				}
			}
		}
	}
	return exposed
}

// typeNamesIn adds to held every type of the package at path that t spells, through
// any composite type, a signature's parameters and results, a type argument and the
// exported members of a struct or an interface with no name: an unexported member is
// out of reach of code outside the package.
func typeNamesIn(held map[*types.TypeName]bool, path string, t types.Type) {
	(&typeNameWalk{held: held, path: path}).walk(t)
}

// typeNameWalk is one [typeNamesIn] walk.
type typeNameWalk struct {
	held map[*types.TypeName]bool
	path string
}

func (w *typeNameWalk) keep(name *types.TypeName) {
	if name.Pkg() != nil && name.Pkg().Path() == w.path {
		w.held[name] = true
	}
}

func (w *typeNameWalk) walk(t types.Type) {
	switch t := t.(type) {
	case *types.Alias:
		w.keep(t.Obj())
		w.walk(t.Rhs())
	case *types.Named:
		w.keep(t.Obj())
		for arg := range t.TypeArgs().Types() {
			w.walk(arg)
		}
	case *types.Pointer:
		w.walk(t.Elem())
	case *types.Slice:
		w.walk(t.Elem())
	case *types.Array:
		w.walk(t.Elem())
	case *types.Chan:
		w.walk(t.Elem())
	case *types.Map:
		w.walk(t.Key())
		w.walk(t.Elem())
	case *types.Signature:
		w.tuple(t.Params())
		w.tuple(t.Results())
	case *types.Struct:
		w.structFields(t)
	case *types.Interface:
		w.interfaceMembers(t)
	}
}

func (w *typeNameWalk) tuple(tuple *types.Tuple) {
	for v := range tuple.Variables() {
		w.walk(v.Type())
	}
}

func (w *typeNameWalk) structFields(st *types.Struct) {
	for field := range st.Fields() {
		if field.Exported() {
			w.walk(field.Type())
		}
	}
}

func (w *typeNameWalk) interfaceMembers(iface *types.Interface) {
	for method := range iface.ExplicitMethods() {
		if method.Exported() {
			w.walk(method.Type())
		}
	}
	for embedded := range iface.EmbeddedTypes() {
		w.walk(embedded)
	}
}

// finding renders one finding of a narrowing kind.
func (n *narrowing) finding(symbol *inventory.Symbol, code vocab.Code, visibility, message string) (draft, bool) {
	one, held := n.in.finding(symbol.ID, code, message)
	if !held {
		return draft{}, false
	}
	one.TestOnly = n.fromTest[symbol.ID] == n.named[symbol.ID]
	one.Details = Details{NarrowerVisibility: visibility}
	return one, true
}

// unreachableExport reports an unused exported declaration of a package nothing
// outside can import: a main package, an external test package, or a package under
// an internal tree. Unimportability is a property of the package graph, so the kind
// needs no consumer information, and its subject may be declared in a test file: an
// exported declaration of an external test package, or an exported test-file
// declaration nothing reaches, is in this population and no other.
func unreachableExport(in *pass) ([]draft, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	var found []draft
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || !in.unreachableExport(candidate, symbol) {
			continue
		}
		one, held := in.finding(candidate.ID, vocab.UnreachableExport,
			unreachableMessage(in, candidate))
		if !held {
			continue
		}
		found = append(found, one)
	}
	return found, nil
}

// unreachableExport reports whether one candidate is this kind's population: the
// unused-exported population of a package nothing outside can import. Every arm is
// the unused-declaration rule's own, in its order, yielding the candidate to the
// kind that makes the more specific claim, so one declaration is reported once.
func (in *pass) unreachableExport(candidate *liveness.Candidate, symbol *inventory.Symbol) bool {
	switch {
	case !symbol.Exported:
		return false
	case candidate.UnreferencedTest:
		return in.unreferencedTestExport(candidate, symbol)
	case in.importable(symbol), candidate.TestOfDeadCode, symbol.InTestFile:
		return false
	case in.deadParent(symbol):
		return false
	case interfaceDeclaration(symbol.Kind), in.readOrWriteSubject(candidate, symbol):
		return false
	case candidate.ProductionRefs == 0 && in.deprecated[candidate.ID]:
		return false
	case testKept(candidate):
		return false
	default:
		return !member(symbol.Kind)
	}
}

// unreferencedTestExport reports whether an exported test-file declaration nothing
// reaches is this kind's population, whatever its package: no other program compiles
// a test file, so its export reaches nobody.
func (in *pass) unreferencedTestExport(candidate *liveness.Candidate, symbol *inventory.Symbol) bool {
	return !member(symbol.Kind) && !interfaceDeclaration(symbol.Kind) && !in.deadParent(symbol) &&
		!in.readOrWriteSubject(candidate, symbol)
}

// unreachableMessage is what one unreachable-export finding says, in the reader's
// words: what the subject is, what the analysis found about its references, and why
// its export reaches nobody.
//
// The relation decides the middle clause. A candidate found by reference counting has
// no reference at all; one found by reachability has references, every one of them
// from a declaration that is itself dead.
func unreachableMessage(in *pass, candidate *liveness.Candidate) string {
	found := "has no reference in the target"
	if candidate.Relation == liveness.Reachability {
		found = "is referenced only from declarations that are themselves dead"
	}
	return "exported " + in.named(candidate.ID) + " " + found +
		", and nothing outside can import the package that declares it"
}
