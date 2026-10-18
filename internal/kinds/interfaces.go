package kinds

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// satisfactionAssertionSubject is what the subject vocabulary calls a compile-time
// assertion that a type satisfies an interface. The language spells such an assertion
// as a variable, and the vocabulary carries a value of its own for it, so a finding
// about one names the subject a maintainer deletes rather than the spelling it took.
const satisfactionAssertionSubject = "satisfaction-assertion"

// What each interface kind says, in the reader's words. The unused-interface kind
// says two things, because the relation that found the subject decides which is
// true: a candidate found by reference counting is named as a type by nothing at
// all, and one found by reachability is named only from declarations that are
// themselves dead.
const (
	unusedInterfaceMessage             = "nothing in the target names the interface as a type"
	unusedInterfaceDeadMessage         = "the interface is named as a type only from declarations that are themselves dead"
	uncalledInterfaceMethodMessage     = "no call site invokes or selects the method through the interface"
	unusedSatisfactionAssertionMessage = "the assertion is the only thing that names the interface as a type"
)

// unusedInterface reports an interface declaration no symbol names as a type,
// naming the concrete types that implement it and where each is written. The
// subject is the sweep's own candidate, so a loaded consumer's use holds it live; a
// satisfaction assertion is a use, so an interface used only by one is live and the
// assertion is reported. Its members fall with it inside its component. An
// interface a test file declares is not reported, because a production sweep
// drops every reference such a declaration can have.
func unusedInterface(in *Input) ([]Finding, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	known := interfacesOf(in)

	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || symbol.Kind != inventory.KindInterface || symbol.InTestFile {
			continue
		}
		one, held := in.finding(candidate.ID, vocab.UnusedInterface, unusedInterfaceMessage)
		if !held {
			continue
		}
		if candidate.Relation == liveness.Reachability {
			one.Message = unusedInterfaceDeadMessage
		}
		one.Relation = candidate.Relation
		one.TestOnly = testOnly(candidate)
		one.Details.Implementations = known.implementationsOf(candidate.ID)
		found = append(found, one)
	}
	return found, nil
}

// uncalledInterfaceMethod reports a method an interface declares that no call site
// of production code invokes and no expression selects through the interface,
// whatever the number of implementations; a call on a concrete value invokes that
// type's own method. [interfaceFacts.exempt] owns the exempt shapes. A method of an
// interface that is itself a candidate falls with the interface, one only test code
// invokes is [Input.testCalledInterfaceMethod]'s, and one an interface of a test file
// declares is not judged by a production sweep.
func uncalledInterfaceMethod(in *Input) ([]Finding, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	known := interfacesOf(in)

	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || symbol.Kind != inventory.KindInterfaceMethod || symbol.InTestFile {
			continue
		}
		if in.candidateOf(symbol.Parent) != nil || known.exempt(candidate.ID) || testOnly(candidate) {
			continue
		}
		one, held := in.finding(candidate.ID, vocab.UncalledInterfaceMethod, uncalledInterfaceMethodMessage)
		if !held {
			continue
		}
		one.Relation = candidate.Relation
		one.Details.Implementations = known.implementationsOf(symbol.Parent)
		found = append(found, one)
	}
	return found, nil
}

// unusedSatisfactionAssertion reports a compile-time satisfaction assertion, a
// package-level blank declaration typed as an interface of the target with a value of
// a concrete type, when nothing but such assertions names that interface as a type.
// Each assertion is one finding at its own position under the interface's reference
// and name, because every blank declaration renders as its package's reference. An
// assertion of a test file or one an exemption retained is not reported, and neither
// is a library's assertion whose interface and type are both published API, a promise
// it states to every importer.
func unusedSatisfactionAssertion(in *Input) ([]Finding, error) {
	if in == nil {
		return nil, nil
	}
	known := interfacesOf(in)

	var found []Finding
	for _, asserted := range known.assertions {
		symbol, iface := in.symbol(asserted.Var), in.symbol(asserted.Interface)
		if symbol == nil || iface == nil || symbol.InTestFile || known.usedAsType[asserted.Interface] ||
			in.index().retained[asserted.Var] {
			continue
		}
		one, held := in.finding(asserted.Var, vocab.UnusedSatisfactionAssertion,
			unusedSatisfactionAssertionMessage)
		if !held {
			continue
		}
		one.Symbol.Kind = satisfactionAssertionSubject
		one.Symbol.Ref, one.Symbol.Name = iface.Ref, iface.Name
		// A program the run does not hold may name a published interface, whatever consumers loaded.
		if in.publishedAPI(iface) {
			if concrete := in.symbol(known.assertedType[asserted]); concrete != nil && in.publishedAPI(concrete) {
				continue
			}
			one.ceiling = vocab.Possible
		}
		one.Details.Implementations = known.implementationsOf(asserted.Interface)
		found = append(found, one)
	}
	return found, nil
}

// assertion is one compile-time satisfaction assertion: the blank declaration and
// the interface of the target it names.
type assertion struct {
	Var       inventory.SymbolID
	Interface inventory.SymbolID
}

// interfaceFacts is what the three interface kinds read, computed over the matrix
// inventory and the type information of every configuration: the concrete
// implementations of every interface the target declares, which methods a sealed
// method set holds, how each method is implemented, which methods a call site
// reaches through the interface, the satisfaction assertions, and which interfaces
// something other than an assertion names as a type.
type interfaceFacts struct {
	implementations map[inventory.SymbolID]map[inventory.SymbolID]positioned
	ordered         map[inventory.SymbolID][]positioned
	methods         map[inventory.SymbolID]*facts.InterfaceMethod
	reached         map[inventory.SymbolID]bool
	called          map[inventory.SymbolID]bool
	usedAsType      map[inventory.SymbolID]bool
	typeUsedBy      map[inventory.SymbolID][]inventory.SymbolID
	asserted        map[assertion]bool
	assertedType    map[assertion]inventory.SymbolID
	assertions      []assertion
}

// interfacesOf computes the facts the three kinds read: the reference set's answers
// first, then the type information of every configuration, and last the facts that
// need both.
func interfacesOf(in *Input) *interfaceFacts {
	f := &interfaceFacts{
		implementations: make(map[inventory.SymbolID]map[inventory.SymbolID]positioned),
		ordered:         make(map[inventory.SymbolID][]positioned),
		reached:         make(map[inventory.SymbolID]bool),
		called:          make(map[inventory.SymbolID]bool),
		usedAsType:      make(map[inventory.SymbolID]bool),
		typeUsedBy:      make(map[inventory.SymbolID][]inventory.SymbolID),
		asserted:        make(map[assertion]bool),
		assertedType:    make(map[assertion]inventory.SymbolID),
	}
	if in.Merged != nil {
		f.methods = in.Merged.InterfaceMethods
		f.references(in, in.Merged.References)
	}
	for i := range in.Per {
		f.configuration(in, &in.Per[i])
	}
	f.settle()
	return f
}

// references reads the matrix's reference set once: which interface methods a call
// site the mode counts invokes or selects through the interface, and which
// declaration names each interface as a type.
//
// A selection through an interface value names the method the interface declares
// rather than the method of the concrete type behind it, which is why a direct call
// on a concrete value reaches nothing here.
func (f *interfaceFacts) references(in *Input, refs []facts.Reference) {
	for i := range refs {
		reference := &refs[i]
		to := in.symbol(reference.To)
		if to == nil {
			continue
		}
		switch {
		case to.Kind == inventory.KindInterfaceMethod && reference.Kind.Selects():
			f.called[reference.To] = true
			f.reached[reference.To] = f.reached[reference.To] || !in.Mode.Uncounted(reference)
		case to.Kind == inventory.KindInterface && namesAsType(reference.Kind):
			f.typeUsedBy[reference.To] = append(f.typeUsedBy[reference.To], reference.From)
		}
	}
}

// namesAsType reports whether one reference kind uses its target as a type, which
// is what an interface exists for: a type position, an embedding, a type assertion
// or type-switch case, and a conversion.
func namesAsType(kind facts.RefKind) bool {
	switch kind {
	case facts.RefTypeUse, facts.RefEmbed, facts.RefAssert, facts.RefConversion:
		return true
	default:
		return false
	}
}

// configuration reads one configuration's interfaces: which types of the target
// implement which of its interfaces, and the satisfaction assertions the
// configuration compiles.
func (f *interfaceFacts) configuration(in *Input, one *Configured) {
	if one.Result == nil || one.Resolve == nil {
		return
	}
	for _, iface := range one.Facts.Interfaces() {
		for _, impl := range iface.Implementations {
			f.addImplementation(in, iface.ID, impl.Concrete)
		}
	}
	f.assertionsIn(in, one, blanksIn(one))
}

// addImplementation records one concrete type of the target as an implementation of
// one of its interfaces, once per type whatever the number of configurations that
// compile both.
func (f *interfaceFacts) addImplementation(in *Input, iface, concrete inventory.SymbolID) {
	symbol := in.symbol(concrete)
	if symbol == nil {
		return
	}
	if f.implementations[iface] == nil {
		f.implementations[iface] = make(map[inventory.SymbolID]positioned)
	}
	f.implementations[iface][concrete] = positioned{
		Ref:      cmp.Or(in.Refs[concrete], symbol.Ref),
		Name:     symbol.Name,
		Position: positionOf(symbol),
	}
}

// assertionsIn records the satisfaction assertions one configuration compiles: a
// package-level declaration of the blank identifier, typed as an interface the
// target declares, whose value is of a concrete type that satisfies it. The
// asserted type is an implementation of that interface, whatever else implements
// it.
func (f *interfaceFacts) assertionsIn(in *Input, one *Configured, blanks []blankDeclaration) {
	for _, blank := range blanks {
		iface, named, isInterface := interfaceNamed(blank.info, blank.spec.Type)
		if !isInterface {
			continue
		}
		id, held := one.Resolve.Object(named.Obj())
		if symbol := in.symbol(id); !held || symbol == nil || symbol.Kind != inventory.KindInterface {
			continue
		}
		value := blank.info.TypeOf(blank.value)
		if value == nil || isInterfaceType(value) || !types.Implements(value, iface) {
			continue
		}
		declaration, inventoried := one.Resolve.At(blank.name.Pos())
		if !inventoried {
			continue
		}
		asserted, concrete := assertion{Var: declaration, Interface: id}, concreteOf(one, value)
		f.asserted[asserted] = true
		f.assertedType[asserted] = concrete
		f.addImplementation(in, id, concrete)
	}
}

// settle fixes the facts that need every configuration's answer: the assertions in
// one order, which interfaces something other than an assertion names as a type,
// and each interface's implementations in the order they are written.
func (f *interfaceFacts) settle() {
	for asserted := range f.asserted {
		f.assertions = append(f.assertions, asserted)
	}
	slices.SortFunc(f.assertions, func(a, b assertion) int {
		return cmp.Or(
			strings.Compare(string(a.Var), string(b.Var)),
			strings.Compare(string(a.Interface), string(b.Interface)),
		)
	})
	for iface, users := range f.typeUsedBy {
		for _, from := range users {
			if !f.asserted[assertion{Var: from, Interface: iface}] {
				f.usedAsType[iface] = true
			}
		}
	}
	for iface, held := range f.implementations {
		implementations := make([]positioned, 0, len(held))
		for _, positioned := range held {
			implementations = append(implementations, positioned)
		}
		slices.SortFunc(implementations, byWrittenPosition)
		f.ordered[iface] = implementations
	}
}

// exempt reports whether one method of an interface is not reported: because a call
// site the mode counts reaches it through the interface, because an exported
// interface's method set that fixes its implementors holds it ([interfaceFacts.seal]),
// or because the method is a marker, whose every implementation in the target has a
// body with no statement; one the target does not declare has no body to read.
func (f *interfaceFacts) exempt(method inventory.SymbolID) bool {
	if f.reached[method] {
		return true
	}
	m := f.methods[method]
	return m != nil && m.Fixed()
}

// testCalledInterfaceMethod reports whether one candidate is an interface method
// only test code invokes through the interface and that the uncalled-method kind's
// precondition does not exempt, which the test-only kind reports as it reports any
// declaration only tests reference. A method of an interface that is itself a
// candidate falls with the interface.
func (in *Input) testCalledInterfaceMethod(candidate *liveness.Candidate, symbol *inventory.Symbol) bool {
	if symbol.Kind != inventory.KindInterfaceMethod || !testOnly(candidate) || in.candidateOf(symbol.Parent) != nil {
		return false
	}
	held := in.index()
	if held.interfaces == nil {
		held.interfaces = interfacesOf(in)
	}
	return !held.interfaces.exempt(candidate.ID)
}

// implementationsOf returns the concrete implementations of one interface, in the
// order they are written, and an empty list for an interface nothing implements.
func (f *interfaceFacts) implementationsOf(iface inventory.SymbolID) []positioned {
	if held, ok := f.ordered[iface]; ok {
		return held
	}
	return []positioned{}
}

// positionOf renders one declaration's position the way a finding carries it. The
// inventory's position is already target-relative and already counts UTF-16 code
// units, so nothing is converted here.
func positionOf(symbol *inventory.Symbol) Position {
	return Position{
		Path:    symbol.Pos.Filename,
		Line:    symbol.Pos.Line,
		Column:  symbol.Pos.Column,
		EndLine: max(symbol.Pos.Line, symbol.EndLine),
	}
}

// byWrittenPosition orders two named symbols as the report orders a component's
// members.
func byWrittenPosition(a, b positioned) int {
	keyA := report.Positioned{Ref: a.Ref, Position: a.Position.document()}
	keyB := report.Positioned{Ref: b.Ref, Position: b.Position.document()}
	return report.ComparePositioned(&keyA, &keyB)
}

// blankDeclaration is one package-level declaration of the blank identifier, with
// the type information of the variant that compiled it.
type blankDeclaration struct {
	info  *types.Info
	spec  *ast.ValueSpec
	name  *ast.Ident
	value ast.Expr
}

// blanksIn is every package-level blank declaration of one configuration that may
// be a satisfaction assertion, read from the first variant of the walk order.
func blanksIn(one *Configured) []blankDeclaration {
	var blanks []blankDeclaration
	for _, p := range one.Facts.Packages() {
		for _, file := range one.Facts.Files(p) {
			for _, decl := range file.Decls {
				if decl, isGen := decl.(*ast.GenDecl); isGen && decl.Tok == token.VAR {
					blanks = append(blanks, blanksOf(p.TypesInfo, decl)...)
				}
			}
		}
	}
	return blanks
}

// blanksOf returns the blank declarations of one variable declaration group that
// carry both a declared type and a value of their own.
func blanksOf(info *types.Info, decl *ast.GenDecl) []blankDeclaration {
	var found []blankDeclaration
	for _, spec := range decl.Specs {
		value, isValue := spec.(*ast.ValueSpec)
		if !isValue || value.Type == nil || len(value.Values) != len(value.Names) {
			continue
		}
		for i, name := range value.Names {
			if name.Name != "_" {
				continue
			}
			found = append(found, blankDeclaration{info: info, spec: value, name: name, value: value.Values[i]})
		}
	}
	return found
}

// interfaceNamed returns the interface one type expression names and the defined
// type that names it.
func interfaceNamed(info *types.Info, expr ast.Expr) (*types.Interface, *types.Named, bool) {
	tv, held := info.Types[expr]
	if !held || !tv.IsType() {
		return nil, nil, false
	}
	named, isNamed := types.Unalias(tv.Type).(*types.Named)
	if !isNamed {
		return nil, nil, false
	}
	iface, isInterface := named.Underlying().(*types.Interface)
	if !isInterface {
		return nil, nil, false
	}
	return iface, named, true
}

// isInterfaceType reports whether one type is an interface or a type parameter,
// neither of which is the concrete type a satisfaction assertion converts: an
// interface reaching an interface says nothing about a concrete type's methods, and
// a type parameter's method set is its constraint's.
func isInterfaceType(t types.Type) bool {
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return true
	}
	_, isInterface := types.Unalias(t).Underlying().(*types.Interface)
	return isInterface
}

// concreteOf returns the declaration of the concrete type one asserted value has,
// through the pointer the assertion may take.
func concreteOf(one *Configured, t types.Type) inventory.SymbolID {
	held := types.Unalias(t)
	if pointer, isPointer := held.(*types.Pointer); isPointer {
		held = types.Unalias(pointer.Elem())
	}
	named, isNamed := held.(*types.Named)
	if !isNamed {
		return ""
	}
	id, _ := one.Resolve.Object(named.Obj())
	return id
}
