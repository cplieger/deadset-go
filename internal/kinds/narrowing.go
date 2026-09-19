package kinds

import (
	"go/types"

	"github.com/cplieger/deadset-go/internal/graph"
)

// The codes of the narrowing kinds.
const (
	unnecessaryExportCode   = "DS1101"
	unnecessaryExposureCode = "DS1102"
	unreachableExportCode   = "DS1103"
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
	in           *Input
	reach        map[graph.SymbolID]narrowScope
	named        map[graph.SymbolID]int
	fromTest     map[graph.SymbolID]int
	unnarrowable map[graph.SymbolID]bool
}

// newNarrowing folds every reference of the inventory into the spread of the
// declaration it names, and keys the declarations whose references are not the whole
// story.
//
// Three things are not. An exemption class stands for a mechanism that reaches a
// declaration by a name, which is the name a narrowing would take away; the
// write-only kind claims a declaration nothing reads is deletable, which is the more
// specific answer about it than a narrower visibility; and a method that satisfies an
// interface the target names as a type keeps its name from that interface, whatever
// the references to it are.
//
// A declared cross-language edge is not one of them. It stands for a consumer in
// another language, and the narrowing finding about a declaration an edge names is
// reported here and then published as the pending finding of that edge's evaluation,
// which is the answer a merge resolves against the paired side.
//
// The exemptions are the sweep's own, because an exemption on a declaration every
// relation found live is only in what the sweep was given and that is the one a
// narrowing has to ask about.
func newNarrowing(in *Input) *narrowing {
	n := &narrowing{
		in:           in,
		reach:        make(map[graph.SymbolID]narrowScope),
		named:        make(map[graph.SymbolID]int),
		fromTest:     make(map[graph.SymbolID]int),
		unnarrowable: make(map[graph.SymbolID]bool, len(in.Exempt)),
	}
	for _, exemption := range in.Exempt {
		n.unnarrowable[exemption.ID] = true
	}
	for i := range in.Merged.References {
		n.widen(&in.Merged.References[i])
	}
	n.holdWriteOnly()
	n.holdInterfaceSatisfying()
	return n
}

// widen folds one reference into the spread of the declaration it names.
//
// Every reference counts, whichever file made it, because the scope a narrowing
// names is the scope the source writes and a test file of the target is inside it.
func (n *narrowing) widen(r *graph.Reference) {
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

// holdWriteOnly keys every declaration the write-only kind reports, asking that kind
// rather than restating its rule, so the two cannot disagree about a declaration
// nothing reads.
func (n *narrowing) holdWriteOnly() {
	exempted := make(map[graph.SymbolID]bool, len(n.in.Exempt))
	for _, exemption := range n.in.Exempt {
		exempted[exemption.ID] = true
	}
	counted := writesAndReads(n.in)
	for i := range n.in.Merged.Symbols {
		symbol := &n.in.Merged.Symbols[i]
		if _, reports := writeOnly(n.in, symbol, counted[symbol.ID], exempted); reports {
			n.unnarrowable[symbol.ID] = true
		}
	}
}

// holdInterfaceSatisfying keys every method of the target that satisfies an
// interface the target declares and something names as a type.
//
// Such a method cannot be unexported: unexporting it takes the type out of the
// interface's implementors, so the conversion the interface exists for stops
// compiling, and the edit a narrowing finding licenses is one no maintainer can
// make on the subject alone. The interfaces this reads are the target's own, because
// a method converted to an interface of another module is one the interface
// satisfaction class already holds; an assertion of satisfaction counts as naming
// the interface, because the assertion is itself a use the narrowing would break.
func (n *narrowing) holdInterfaceSatisfying() {
	facts := interfacesOf(n.in)
	for i := range n.in.Per {
		one := &n.in.Per[i]
		if one.Result == nil || one.Resolve == nil {
			continue
		}
		d := declaredIn(one)
		for _, declared := range d.interfaces {
			if len(facts.typeUsedBy[declared.id]) == 0 {
				continue
			}
			n.holdSatisfiers(one, declared, d.concrete)
		}
	}
}

// holdSatisfiers keys the methods of every concrete type of the target that
// satisfies one interface, which are the methods that interface's method set names.
func (n *narrowing) holdSatisfiers(one *Configured, declared declaredInterface, concrete []declaredType) {
	for _, held := range concrete {
		recv, satisfies := implementing(held.typ, declared.iface)
		if !satisfies {
			continue
		}
		set := types.NewMethodSet(recv)
		for method := range declared.iface.Methods() {
			sel := set.Lookup(method.Pkg(), method.Name())
			if sel == nil {
				continue
			}
			if id, inventoried := one.Resolve.Object(sel.Obj()); inventoried {
				n.unnarrowable[id] = true
			}
		}
	}
}

// scopeOf answers how far one reference to a declaration reaches.
//
// The referencing declaration decides it, and a run enumerates the target's
// declarations alone, so a reference from a declaration the inventory does not hold
// is a reference from outside the module. Every reference a loaded consumer makes is
// one of those, which is what leaves a declaration a consumer references out of both
// narrowing kinds however narrow the target's own references are. An external test
// package is a package of its own, so a reference from one reaches past the package
// it tests.
func (n *narrowing) scopeOf(to *graph.Symbol, r *graph.Reference) narrowScope {
	from := n.in.symbol(r.From)
	switch {
	case from == nil:
		return scopeOutside
	case from.PkgPath != to.PkgPath:
		return scopeModule
	case r.Pos.Filename != to.Pos.Filename:
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
func (in *Input) closed(symbol *graph.Symbol) bool {
	return !in.importable(symbol.PkgPath) || in.consumersLoaded()
}

// narrowable reports whether one declaration is a subject of the two narrowing
// kinds: an exported declaration of a closed world that a reference names, that the
// sweep found live, and that nothing beyond the reference set speaks for.
//
// Three kinds of declaration are not subjects whatever their references are, because
// for each of them the exported name answers to something other than the references.
// A type parameter is exported by its name and no consumer can write it, because a
// caller supplies a type argument by position. An interface method's exportedness is
// its interface's contract, so unexporting it changes the method set every
// implementing type must provide. A struct field's exportedness is what an encoder,
// a template and a reflective lookup read it by, and those name it by string where
// no reference does.
func (n *narrowing) narrowable(symbol *graph.Symbol) bool {
	switch {
	case !symbol.Exported, unnarrowableKind(symbol.Kind):
		return false
	case n.unnarrowable[symbol.ID]:
		return false
	case !n.in.closed(symbol), n.in.candidateOf(symbol.ID) != nil:
		return false
	default:
		return n.named[symbol.ID] > 0
	}
}

// unnarrowableKind reports whether one kind of declaration is outside both
// narrowing kinds' population: a type parameter, a method an interface declares, and
// a field of a struct.
func unnarrowableKind(kind graph.SymbolKind) bool {
	switch kind {
	case graph.KindTypeParam, graph.KindInterfaceMethod, graph.KindField:
		return true
	default:
		return false
	}
}

// subjects lists the declarations the two narrowing kinds judge, in the order the
// inventory holds them.
func (n *narrowing) subjects() []*graph.Symbol {
	var held []*graph.Symbol
	for i := range n.in.Merged.Symbols {
		if symbol := &n.in.Merged.Symbols[i]; n.narrowable(symbol) {
			held = append(held, symbol)
		}
	}
	return held
}

// UnnecessaryExport reports an exported declaration whose every reference is inside
// the package that declares it, naming the narrower visibility those references
// support: the file where every one of them is in the declaring file, and the
// package otherwise.
//
// The kind reports only where every reference that can exist is one the run loaded: a
// published package's callers are unknown unless the configuration declares the
// consumer set complete and every declared consumer loaded, while a main package, an
// external test package and an internal tree are closed whatever the run knows about
// consumers.
func UnnecessaryExport(in *Input) ([]Finding, error) {
	if in == nil || in.Merged == nil || in.Sweep == nil {
		return nil, nil
	}
	n := newNarrowing(in)
	subjects := n.subjects()
	found := make([]Finding, 0, len(subjects))
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
		message := "exported " + in.word(symbol.ID) +
			" is referenced only inside the " + visibility + " that declares it"
		if one, held := n.finding(symbol, unnecessaryExportCode, visibility, message); held {
			found = append(found, one)
		}
	}
	return found, nil
}

// UnnecessaryExposure reports an exported declaration of a package an importer
// outside the module can name whose every reference is inside the target module, so
// the declaration can move behind an internal boundary.
//
// It reports under the same closed-world precondition UnnecessaryExport does.
func UnnecessaryExposure(in *Input) ([]Finding, error) {
	if in == nil || in.Merged == nil || in.Sweep == nil {
		return nil, nil
	}
	n := newNarrowing(in)
	subjects := n.subjects()
	found := make([]Finding, 0, len(subjects))
	for _, symbol := range subjects {
		if !in.importable(symbol.PkgPath) || n.reach[symbol.ID] != scopeModule {
			continue
		}
		message := "exported " + in.word(symbol.ID) + " is referenced only inside this module"
		if one, held := n.finding(symbol, unnecessaryExposureCode, visibilityModule, message); held {
			found = append(found, one)
		}
	}
	return found, nil
}

// finding renders one finding of a narrowing kind.
func (n *narrowing) finding(symbol *graph.Symbol, code, visibility, message string) (Finding, bool) {
	one, held := n.in.finding(symbol.ID, code, message)
	if !held {
		return Finding{}, false
	}
	one.TestOnly = n.fromTest[symbol.ID] == n.named[symbol.ID]
	one.Details = Details{NarrowerVisibility: visibility}
	return one, true
}

// UnreachableExport reports an unused exported declaration of a package nothing
// outside can import: a main package, an external test package, or a package under an
// internal tree.
//
// The subject is a candidate of the sweep, so the kind is the unused-exported answer
// for a declaration whose export reaches nobody, and it needs no consumer
// information: unimportability is a property of the package graph rather than of the
// consumer set. Because the claim is about the package graph rather than about the
// references, the subject may be declared in a test file, which is what puts an
// exported declaration of an external test package in this population and in no
// other.
func UnreachableExport(in *Input) ([]Finding, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || !in.unreachableExport(candidate, symbol) {
			continue
		}
		one, held := in.finding(candidate.ID, unreachableExportCode,
			unreachableMessage(in, candidate))
		if !held {
			continue
		}
		found = append(found, one)
	}
	return found, nil
}

// unreachableExport reports whether one candidate is this kind's population, which is
// the unused-exported population of a package nothing outside can import.
//
// Every arm yields the candidate to the kind that makes the more specific claim about
// it, so one declaration is reported once. The arms are the unused-declaration rule's
// own, in its order, because this kind is that rule's answer for an unimportable
// package: a test whose every subject is dead, a member of a container that is itself
// dead, an interface or one of its methods, a subject of the read-and-write kinds, a
// declaration carrying a deprecation marker, a declaration only a test file
// references, and a field of a struct.
func (in *Input) unreachableExport(candidate *graph.Candidate, symbol *graph.Symbol) bool {
	if !symbol.Exported || in.importable(symbol.PkgPath) {
		return false
	}
	switch {
	case candidate.TestOfDeadCode:
		return false
	case symbol.Parent != "" && in.candidateOf(symbol.Parent) != nil:
		return false
	case interfaceDeclaration(symbol.Kind), in.readOrWriteSubject(candidate, symbol):
		return false
	case candidate.ProductionRefs == 0 && in.deprecations()[candidate.ID]:
		return false
	case candidate.ProductionRefs == 0 && candidate.TestRefs > 0:
		return false
	default:
		return !member(symbol.Kind)
	}
}

// unreachableMessage is what one unreachable-export finding says, in the reader's
// words: what the subject is, what the analysis found about its references, and why
// its export reaches nobody.
//
// The relation decides the middle clause. A candidate found by reference counting has
// no reference at all; one found by reachability has references, every one of them
// from a declaration that is itself dead.
func unreachableMessage(in *Input, candidate *graph.Candidate) string {
	found := "has no reference in the target"
	if candidate.Relation == graph.Reachability {
		found = "is referenced only from declarations that are themselves dead"
	}
	return "exported " + in.word(candidate.ID) + " " + found +
		", and nothing outside can import the package that declares it"
}
