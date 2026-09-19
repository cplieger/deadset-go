package kinds

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/packages"
)

// The codes of the interface kinds.
const (
	unusedInterfaceCode             = "DS1201"
	uncalledInterfaceMethodCode     = "DS1203"
	unusedSatisfactionAssertionCode = "DS1204"
)

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

// UnusedInterface reports an interface declaration no symbol names as a type,
// naming the concrete types that implement it and where each is written.
//
// The subject is the sweep's own candidate, so a use from a loaded consumer holds
// the interface live the way any other reference does, and the relation the finding
// carries is the one that found it. A satisfaction assertion is itself a use of the
// interface, so an interface whose only use is an assertion is live and the
// assertion is what is reported.
//
// The interface's own members are not reported beside it: a member whose container
// is a candidate falls with the container and is reported inside its component.
//
// An interface a test file declares is not reported at all, because a production
// sweep drops the only references such a declaration can have and so judges every
// one of them a candidate whatever its test files do with it.
func UnusedInterface(in *Input) ([]Finding, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	facts := interfacesOf(in)

	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || symbol.Kind != graph.KindInterface || testFile(symbol) {
			continue
		}
		one, held := in.finding(candidate.ID, unusedInterfaceCode, unusedInterfaceMessage)
		if !held {
			continue
		}
		if candidate.Relation == graph.Reachability {
			one.Message = unusedInterfaceDeadMessage
		}
		one.Relation = candidate.Relation
		one.TestOnly = testOnly(candidate)
		one.Details.Implementations = facts.implementationsOf(candidate.ID)
		found = append(found, one)
	}
	return found, nil
}

// UncalledInterfaceMethod reports a method an interface declares that no call site
// invokes and no expression selects through the interface, whatever the number of
// implementations. A method selected as a value is selected through the interface,
// so it is invoked for this rule, while a call on a value of a concrete type
// invokes that type's own method and not this one.
//
// Two shapes are exempt. Every method of an interface whose method set holds an
// unexported method is exempt, because such a method set exists to fix which types
// implement the interface rather than to be called through it. And a marker method
// is exempt, being a method whose every implementation in the target carries a body
// with no statement; an implementation the target does not declare is a body this
// analysis cannot read, which leaves the method not a marker.
//
// A method of an interface that is itself a candidate is not reported here: such an
// interface is reported as unused and its members belong to its component. A method
// nothing but a test file invokes through the interface is not reported here
// either, because being referenced only from test files is the more specific fact
// about it, and neither is a method an interface a test file declares declares,
// whose liveness a production sweep cannot judge.
func UncalledInterfaceMethod(in *Input) ([]Finding, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	facts := interfacesOf(in)

	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || symbol.Kind != graph.KindInterfaceMethod || testFile(symbol) {
			continue
		}
		if in.candidateOf(symbol.Parent) != nil || facts.exempt(symbol.Parent, candidate.ID) {
			continue
		}
		one, held := in.finding(candidate.ID, uncalledInterfaceMethodCode, uncalledInterfaceMethodMessage)
		if !held {
			continue
		}
		one.Relation = candidate.Relation
		one.TestOnly = testOnly(candidate)
		one.Details.Implementations = facts.implementationsOf(symbol.Parent)
		found = append(found, one)
	}
	return found, nil
}

// UnusedSatisfactionAssertion reports a compile-time satisfaction assertion, a
// package-level declaration of the blank identifier whose declared type is an
// interface of the target and whose value is of a concrete type, when nothing other
// than such an assertion names that interface as a type.
//
// The assertion is the subject, so the methods it retains stay retained: the
// exemption that holds them names the assertion as its site, and this finding names
// the declaration a maintainer deletes. Two assertions of one interface are two
// findings, because neither is the use that would make the interface worth keeping.
//
// An assertion a test file writes is not reported, on the rule every kind applies
// to a declaration whose liveness a production sweep cannot judge.
//
// The subject is a root rather than a candidate, a blank declaration being live by
// the rule that an initializer nothing can name still runs. The relation the finding
// carries is reference counting, which is the relation under which nothing names the
// assertion.
func UnusedSatisfactionAssertion(in *Input) ([]Finding, error) {
	if in == nil {
		return nil, nil
	}
	facts := interfacesOf(in)

	var found []Finding
	for _, asserted := range facts.assertions {
		symbol := in.symbol(asserted.Var)
		if symbol == nil || testFile(symbol) || facts.usedAsType[asserted.Interface] {
			continue
		}
		one, held := in.finding(asserted.Var, unusedSatisfactionAssertionCode,
			unusedSatisfactionAssertionMessage)
		if !held {
			continue
		}
		one.Relation = graph.ReferenceCounting
		one.Details.Implementations = facts.implementationsOf(asserted.Interface)
		found = append(found, one)
	}
	return found, nil
}

// assertion is one compile-time satisfaction assertion: the blank declaration and
// the interface of the target it names.
type assertion struct {
	Var       graph.SymbolID
	Interface graph.SymbolID
}

// implementationCount counts, per method an interface declares, the implementations
// measured and how many of them carry a body with no statement.
type implementationCount struct {
	implementations int
	empty           int
}

// interfaceFacts is what the three interface kinds read, computed over the matrix
// inventory and the type information of every configuration: the concrete
// implementations of every interface the target declares, which interfaces fix
// their implementors, how each of their methods is implemented, which methods a
// call site reaches through the interface, the satisfaction assertions, and which
// interfaces something other than an assertion names as a type.
type interfaceFacts struct {
	implementations map[graph.SymbolID]map[graph.SymbolID]Positioned
	ordered         map[graph.SymbolID][]Positioned
	sumType         map[graph.SymbolID]bool
	counted         map[graph.SymbolID]*implementationCount
	reached         map[graph.SymbolID]bool
	usedAsType      map[graph.SymbolID]bool
	typeUsedBy      map[graph.SymbolID][]graph.SymbolID
	asserted        map[assertion]bool
	assertions      []assertion
}

// interfacesOf computes the facts the three kinds read: the reference set's answers
// first, then the type information of every configuration, and last the facts that
// need both.
func interfacesOf(in *Input) *interfaceFacts {
	f := &interfaceFacts{
		implementations: make(map[graph.SymbolID]map[graph.SymbolID]Positioned),
		ordered:         make(map[graph.SymbolID][]Positioned),
		sumType:         make(map[graph.SymbolID]bool),
		counted:         make(map[graph.SymbolID]*implementationCount),
		reached:         make(map[graph.SymbolID]bool),
		usedAsType:      make(map[graph.SymbolID]bool),
		typeUsedBy:      make(map[graph.SymbolID][]graph.SymbolID),
		asserted:        make(map[assertion]bool),
	}
	if in.Merged != nil {
		f.references(in, in.Merged.References)
	}
	for i := range in.Per {
		f.configuration(in, &in.Per[i])
	}
	f.settle()
	return f
}

// references reads the matrix's reference set once: which interface methods a call
// site invokes or selects through the interface, and which declaration names each
// interface as a type.
//
// A selection through an interface value names the method the interface declares
// rather than the method of the concrete type behind it, which is why a direct call
// on a concrete value reaches nothing here.
func (f *interfaceFacts) references(in *Input, refs []graph.Reference) {
	for i := range refs {
		reference := &refs[i]
		to := in.symbol(reference.To)
		if to == nil {
			continue
		}
		switch {
		case to.Kind == graph.KindInterfaceMethod && selects(reference.Kind):
			f.reached[reference.To] = true
		case to.Kind == graph.KindInterface && namesAsType(reference.Kind):
			f.typeUsedBy[reference.To] = append(f.typeUsedBy[reference.To], reference.From)
		}
	}
}

// selects reports whether one reference kind reaches a method through the value it
// is written on: a call, and a selection that takes the method as a value.
func selects(kind graph.RefKind) bool {
	switch kind {
	case graph.RefCall, graph.RefRead:
		return true
	default:
		return false
	}
}

// namesAsType reports whether one reference kind uses its target as a type, which
// is what an interface exists for: a type position, an embedding, a type assertion
// or type-switch case, and a conversion.
func namesAsType(kind graph.RefKind) bool {
	switch kind {
	case graph.RefTypeUse, graph.RefEmbed, graph.RefAssert, graph.RefConversion:
		return true
	default:
		return false
	}
}

// configuration reads one configuration's type information: which types of the
// target implement which of its interfaces, how each interface method's
// implementations are written, and the satisfaction assertions the configuration
// compiles.
func (f *interfaceFacts) configuration(in *Input, one *Configured) {
	if one.Result == nil || one.Resolve == nil {
		return
	}
	d := declaredIn(one)
	for _, declared := range d.interfaces {
		f.sumType[declared.id] = f.sumType[declared.id] || restrictsImplementors(declared.iface)
		for _, concrete := range d.concrete {
			recv, satisfies := implementing(concrete.typ, declared.iface)
			if !satisfies {
				continue
			}
			f.addImplementation(in, declared.id, concrete.id)
			f.count(one, declared, recv, d.written)
		}
	}
	f.assertionsIn(in, one, &d)
}

// addImplementation records one concrete type of the target as an implementation of
// one of its interfaces, once per type whatever the number of configurations that
// compile both.
func (f *interfaceFacts) addImplementation(in *Input, iface, concrete graph.SymbolID) {
	symbol := in.symbol(concrete)
	if symbol == nil {
		return
	}
	if f.implementations[iface] == nil {
		f.implementations[iface] = make(map[graph.SymbolID]Positioned)
	}
	f.implementations[iface][concrete] = Positioned{
		Ref:      cmp.Or(in.Refs[concrete], symbol.Ref),
		Name:     symbol.Name,
		Position: positionOf(symbol),
	}
}

// count records, for each method one interface declares, the implementation just
// found and whether its body carries no statement. A method promoted from an
// embedded field is measured where it is written, which is the declaration a
// deletion would remove; a method the target does not declare has no body to read
// and counts as an implementation that is not empty.
func (f *interfaceFacts) count(one *Configured, declared declaredInterface, recv types.Type, written map[token.Pos]*ast.FuncDecl) {
	set := types.NewMethodSet(recv)
	for method := range declared.iface.Methods() {
		id, held := one.Resolve.Object(method)
		if !held {
			continue
		}
		if f.counted[id] == nil {
			f.counted[id] = &implementationCount{}
		}
		f.counted[id].implementations++
		if sel := set.Lookup(method.Pkg(), method.Name()); sel != nil && emptyBody(written, sel.Obj()) {
			f.counted[id].empty++
		}
	}
}

// assertionsIn records the satisfaction assertions one configuration compiles: a
// package-level declaration of the blank identifier, typed as an interface the
// target declares, whose value is of a concrete type that satisfies it. The
// asserted type is an implementation of that interface, whatever else implements
// it.
func (f *interfaceFacts) assertionsIn(in *Input, one *Configured, d *declared) {
	for _, blank := range d.blanks {
		iface, named, isInterface := interfaceNamed(blank.info, blank.spec.Type)
		if !isInterface {
			continue
		}
		id, held := one.Resolve.Object(named.Obj())
		if symbol := in.symbol(id); !held || symbol == nil || symbol.Kind != graph.KindInterface {
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
		f.asserted[assertion{Var: declaration, Interface: id}] = true
		f.addImplementation(in, id, concreteOf(one, value))
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
		implementations := make([]Positioned, 0, len(held))
		for _, positioned := range held {
			implementations = append(implementations, positioned)
		}
		slices.SortFunc(implementations, byWrittenPosition)
		f.ordered[iface] = implementations
	}
}

// exempt reports whether one method of one interface is not reported: because a
// call site reaches it through the interface, because the interface's method set
// fixes its implementors, or because the method is a marker.
func (f *interfaceFacts) exempt(iface, method graph.SymbolID) bool {
	if f.reached[method] || f.sumType[iface] {
		return true
	}
	counted := f.counted[method]
	return counted != nil && counted.implementations > 0 && counted.empty == counted.implementations
}

// implementationsOf returns the concrete implementations of one interface, in the
// order they are written.
func (f *interfaceFacts) implementationsOf(iface graph.SymbolID) []Positioned {
	return f.ordered[iface]
}

// positionOf renders one declaration's position the way a finding carries it. The
// inventory's position is already target-relative and already counts UTF-16 code
// units, so nothing is converted here.
func positionOf(symbol *graph.Symbol) Position {
	return Position{
		Path:    symbol.Pos.Filename,
		Line:    symbol.Pos.Line,
		Column:  symbol.Pos.Column,
		EndLine: max(symbol.Pos.Line, symbol.EndLine),
	}
}

// byWrittenPosition orders two named symbols by where each is written, then by the
// reference that names it.
func byWrittenPosition(a, b Positioned) int {
	return cmp.Or(
		strings.Compare(a.Position.Path, b.Position.Path),
		cmp.Compare(a.Position.Line, b.Position.Line),
		cmp.Compare(a.Position.Column, b.Position.Column),
		strings.Compare(a.Ref, b.Ref),
	)
}

// declaredInterface is one interface the target declares, as one configuration
// type-checks it.
type declaredInterface struct {
	iface *types.Interface
	id    graph.SymbolID
}

// declaredType is one concrete type the target declares, as one configuration
// type-checks it.
type declaredType struct {
	typ types.Type
	id  graph.SymbolID
}

// blankDeclaration is one package-level declaration of the blank identifier, with
// the type information of the variant that compiled it.
type blankDeclaration struct {
	info  *types.Info
	spec  *ast.ValueSpec
	name  *ast.Ident
	value ast.Expr
}

// declared is what one configuration's type information holds for these kinds.
type declared struct {
	written    map[token.Pos]*ast.FuncDecl
	seen       map[graph.SymbolID]bool
	interfaces []declaredInterface
	concrete   []declaredType
	blanks     []blankDeclaration
}

// declaredIn reads one configuration's type information: the interfaces and the
// concrete types the target declares, the function declarations the marker rule
// measures, and the package-level blank declarations that may be satisfaction
// assertions. A declaration several package variants type-check is read from the
// first variant by identifier, so one run answers what the next one does.
//
// A generic type is in neither type set, because whether an uninstantiated generic
// type implements an interface is not a question the type checker answers.
func declaredIn(one *Configured) declared {
	d := declared{
		written: make(map[token.Pos]*ast.FuncDecl),
		seen:    make(map[graph.SymbolID]bool),
	}
	for _, p := range sortedPackages(one.Result.Packages) {
		if p.TypesInfo == nil {
			continue
		}
		d.collect(one, p)
	}
	slices.SortFunc(d.interfaces, func(a, b declaredInterface) int {
		return strings.Compare(string(a.id), string(b.id))
	})
	slices.SortFunc(d.concrete, func(a, b declaredType) int {
		return strings.Compare(string(a.id), string(b.id))
	})
	return d
}

// collect reads one package variant: the named types it defines, the functions it
// declares and the blank declarations of its files.
func (d *declared) collect(one *Configured, p *packages.Package) {
	for _, object := range p.TypesInfo.Defs {
		name, isType := object.(*types.TypeName)
		if !isType {
			continue
		}
		id, held := one.Resolve.Object(name)
		if !held || d.seen[id] {
			continue
		}
		d.seen[id] = true
		d.keep(id, name)
	}
	for _, file := range p.Syntax {
		d.declarations(p.TypesInfo, file)
	}
}

// keep files one named type of the target under what it is: an interface whose
// methods these kinds judge, or a concrete type that may implement one.
func (d *declared) keep(id graph.SymbolID, name *types.TypeName) {
	named, isNamed := types.Unalias(name.Type()).(*types.Named)
	if !isNamed || named.TypeParams().Len() > 0 {
		return
	}
	if iface, isInterface := named.Underlying().(*types.Interface); isInterface {
		d.interfaces = append(d.interfaces, declaredInterface{iface: iface, id: id})
		return
	}
	d.concrete = append(d.concrete, declaredType{typ: named, id: id})
}

// declarations reads one file: the bodies the marker rule measures, and the
// package-level blank declarations carrying both a type and a value, which is the
// shape of a satisfaction assertion.
func (d *declared) declarations(info *types.Info, file *ast.File) {
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			d.written[decl.Name.Pos()] = decl
		case *ast.GenDecl:
			if decl.Tok == token.VAR {
				d.blanks = append(d.blanks, blanksOf(info, decl)...)
			}
		}
	}
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

// sortedPackages orders one configuration's packages by identifier, so the variant
// that answers for a declaration several variants type-check is the same one on
// every run.
func sortedPackages(pkgs []*packages.Package) []*packages.Package {
	ordered := slices.Clone(pkgs)
	slices.SortFunc(ordered, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
	return ordered
}

// restrictsImplementors reports whether one interface's method set holds an
// unexported method, which is the shape whose method set exists to fix the types
// that implement it rather than to be called through.
func restrictsImplementors(iface *types.Interface) bool {
	for method := range iface.Methods() {
		if !method.Exported() {
			return true
		}
	}
	return false
}

// implementing returns the form of one concrete type that satisfies one interface,
// the pointer to it included, and reports whether either does. An interface with no
// method is satisfied by every type, so naming those types says nothing about it
// and none is recorded.
func implementing(t types.Type, iface *types.Interface) (types.Type, bool) {
	if iface.NumMethods() == 0 {
		return nil, false
	}
	if types.Implements(t, iface) {
		return t, true
	}
	if pointer := types.NewPointer(t); types.Implements(pointer, iface) {
		return pointer, true
	}
	return nil, false
}

// emptyBody reports whether the declaration of one method carries a body with no
// statement. A method the target does not declare, and one declared with no body at
// all, is a body this analysis cannot read and is not empty.
func emptyBody(written map[token.Pos]*ast.FuncDecl, object types.Object) bool {
	decl, held := written[object.Pos()]
	return held && decl.Body != nil && len(decl.Body.List) == 0
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
func concreteOf(one *Configured, t types.Type) graph.SymbolID {
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
