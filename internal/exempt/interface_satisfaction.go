package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/graph"
)

// InterfaceSatisfactionDetector retains every method that satisfies an interface
// a value of the method's receiver type reaches, as types.Implements decides at each
// conversion site of the target or of a loaded consumer, and what code outside the
// program can assert on the value ([outsideAssertions]). Each retention is a use by
// the declaration holding the site, and an assertion's is a use by the asserted
// type, so the assertion and the methods fall with a type only dead code builds. A
// method satisfying two interfaces at two sites is retained once per site.
func InterfaceSatisfactionDetector(in *Input) ([]graph.Exemption, error) {
	sites, err := programConversions(in)
	if err != nil {
		return nil, err
	}
	sites = append(slices.Clip(sites), publishedConversions(in)...)
	spans := declarationSpans(in)
	spans.outside = newOutsideAssertions(in)

	var retained []graph.Exemption
	for i := range sites {
		retained, err = spans.retain(in, &sites[i], retained)
		if err != nil {
			return nil, err
		}
	}

	slices.SortFunc(retained, byHeldSymbol)
	return slices.CompactFunc(retained, sameExemption), nil
}

// span is one top-level declaration's source range and the symbol it declares.
type span struct {
	id       graph.SymbolID
	from, to token.Pos
}

// spans indexes, per file, the top-level declarations and the satisfaction
// assertions: a blank variable declared with a type and one value.
type spans struct {
	outside      *outsideAssertions
	declarations []span
	assertions   []span
}

// declarationSpans reads the spans of every file the configuration compiles.
func declarationSpans(in *Input) *spans {
	held := &spans{}
	for _, p := range graph.SortedPackages(in.Result.Packages) {
		for _, f := range graph.SortedSyntax(p, in.Result.Fset) {
			for _, decl := range f.Decls {
				held.add(in, decl)
			}
		}
	}
	return held
}

// add records one top-level declaration.
func (s *spans) add(in *Input, decl ast.Decl) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if id, ok := in.Resolve.At(d.Name.Pos()); ok {
			s.declarations = append(s.declarations, span{from: d.Pos(), to: d.End(), id: id})
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			s.addSpec(in, spec)
		}
	}
}

// addSpec records one specification of a declaration group.
func (s *spans) addSpec(in *Input, spec ast.Spec) {
	switch one := spec.(type) {
	case *ast.TypeSpec:
		if id, ok := in.Resolve.At(one.Name.Pos()); ok {
			s.declarations = append(s.declarations, span{from: one.Pos(), to: one.End(), id: id})
		}
	case *ast.ValueSpec:
		if len(one.Names) == 1 && one.Names[0].Name == "_" && one.Type != nil && len(one.Values) == 1 {
			if id, ok := in.Resolve.At(one.Names[0].Pos()); ok {
				s.assertions = append(s.assertions, span{from: one.Values[0].Pos(), to: one.Values[0].End(), id: id})
			}
			return
		}
		for _, name := range one.Names {
			if id, ok := in.Resolve.At(name.Pos()); ok && name.Name != "_" {
				s.declarations = append(s.declarations, span{from: one.Pos(), to: one.End(), id: id})
				return
			}
		}
	}
}

// holderOf is the declaration whose use one conversion is: the asserted type for a
// satisfaction assertion, with the assertion itself, and otherwise the top-level
// declaration that holds the site. A site derived from others is held by the
// declaration of each [graph.Conversion.Held] position ([spans.holdersAt]). Empty
// where no declaration of the inventory does.
func (s *spans) holderOf(in *Input, c *graph.Conversion) (holder graph.SymbolID, coHolders []graph.SymbolID, asserted graph.SymbolID) {
	if len(c.Held) == 0 {
		holder, asserted = s.declarationAt(in, c, c.Site)
		return holder, nil, asserted
	}
	holder, coHolders = s.holdersAt(in, c, c.Held)
	return holder, coHolders, ""
}

// holdersAt is the declaration holding each of several positions of one conversion,
// each once: the first the holder, the rest its co-holders, sorted.
func (s *spans) holdersAt(in *Input, c *graph.Conversion, positions []token.Pos) (holder graph.SymbolID, coHolders []graph.SymbolID) {
	for _, at := range positions {
		one, _ := s.declarationAt(in, c, at)
		switch {
		case one == "", one == holder, slices.Contains(coHolders, one):
		case holder == "":
			holder = one
		default:
			coHolders = append(coHolders, one)
		}
	}
	slices.Sort(coHolders)
	return holder, coHolders
}

// declarationAt is the declaration holding one position of a conversion, and the
// satisfaction assertion where the position is in one.
func (s *spans) declarationAt(in *Input, c *graph.Conversion, at token.Pos) (holder, asserted graph.SymbolID) {
	for _, a := range s.assertions {
		if a.from <= at && at < a.to {
			return typeDeclaration(in, c.From), a.id
		}
	}
	for _, d := range s.declarations {
		if d.from <= at && at < d.to {
			return d.id, ""
		}
	}
	return "", ""
}

// retain appends what one conversion site retains: the methods of the concrete type
// that answer the interface, the satisfaction assertion the site is, if it is one,
// and the methods by which the type implements an interface the source of an outside
// package asserts: the interface's own package, and for an argument the callee's.
func (s *spans) retain(in *Input, c *graph.Conversion, retained []graph.Exemption) ([]graph.Exemption, error) {
	generic := genericOrigin(c.From)
	if generic && !matchesByArity(c.From, c.To) || !generic && !types.Implements(c.From, c.To) {
		return retained, nil
	}
	methods := satisfying(c.From, c.To)
	if len(methods) == 0 {
		return retained, nil
	}
	consumer, site, err := in.Resolve.Site(c.Site)
	if err != nil {
		return nil, err
	}
	holder, coHolders, asserted := s.holderOf(in, c)
	if asserted != "" && holder != "" {
		retained = append(retained, graph.Exemption{
			ID: asserted, Class: string(InterfaceSatisfaction), Consumer: consumer, Site: site,
			Detail: "asserts " + c.Name, Holder: holder,
		})
	}
	for _, m := range methods {
		id, inventoried := in.Resolve.Object(m.method)
		if !inventoried {
			continue
		}
		via, _ := in.Resolve.Object(m.answers)
		retained = append(retained, graph.Exemption{
			ID:        id,
			Class:     string(InterfaceSatisfaction),
			Consumer:  consumer,
			Site:      site,
			Detail:    "satisfies " + c.Name,
			Holder:    holder,
			CoHolders: coHolders,
			Via:       via,
		})
	}
	if generic {
		return retained, nil
	}
	held := graph.Exemption{Class: string(InterfaceSatisfaction), Consumer: consumer, Site: site, Holder: holder, CoHolders: coHolders}
	for _, pkg := range s.outsidePackages(c) {
		retained = s.retainOutside(in, c, pkg, methods, &held, retained)
	}
	if types.Identical(c.To, errorInterface) && !types.IsInterface(c.From) {
		retained = s.retainErrorAssertions(in, c, methods, &held, retained)
	}
	return retained, nil
}

// errorInterface is the method set of the predeclared error type.
var errorInterface, _ = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// retainErrorAssertions appends, for a site converting a type to error, each method
// by which the type implements an interface holding error's method that a package
// outside the program declares and its own source asserts ([outsideAssertions.ofError]),
// the methods error already retained excepted.
func (s *spans) retainErrorAssertions(in *Input, c *graph.Conversion, methods []answer, held *graph.Exemption, retained []graph.Exemption) []graph.Exemption {
	for _, one := range s.outside.ofError(c.From) {
		if slices.ContainsFunc(methods, func(m answer) bool { return m.method == one.method }) {
			continue
		}
		if id, inventoried := in.Resolve.Object(one.method); inventoried {
			retention := *held
			retention.ID = id
			retention.Detail = "satisfies what " + one.asserted.Obj().Pkg().Path() + " asserts through " + c.Name +
				" as " + one.asserted.Obj().Name()
			retained = append(retained, retention)
		}
	}
	return retained
}

// outsidePackages are the packages outside the program whose assertions one site
// answers: the interface's own package, and an argument's callee's, one hop, since a
// package the callee hands the value on to is not read.
func (s *spans) outsidePackages(c *graph.Conversion) []*types.Package {
	pkg, asserts := s.outside.of(c.Interface)
	var pkgs []*types.Package
	if asserts {
		pkgs = append(pkgs, pkg)
	}
	if c.Callee != nil && !s.outside.program[c.Callee.Path()] && (pkg == nil || c.Callee.Path() != pkg.Path()) {
		pkgs = append(pkgs, c.Callee)
	}
	return pkgs
}

// retainOutside appends each method by which the converted type implements an
// interface the source of one outside package asserts ([outsideAssertions]), the
// methods the converted-to interface already retained excepted, each recorded as
// held records it.
func (s *spans) retainOutside(in *Input, c *graph.Conversion, pkg *types.Package, methods []answer, held *graph.Exemption, retained []graph.Exemption) []graph.Exemption {
	detail := "satisfies what " + pkg.Path() + " asserts through " + c.Name
	for _, method := range s.outside.methods(c.From, pkg) {
		if slices.ContainsFunc(methods, func(m answer) bool { return m.method == method }) {
			continue
		}
		if id, inventoried := in.Resolve.Object(method); inventoried {
			one := *held
			one.ID, one.Detail = id, detail
			retained = append(retained, one)
		}
	}
	return retained
}

// outsideAssertions answers what code outside the program asserts on a value it
// holds through a defined interface its own package declares: every interface a type
// assertion or a type switch case of that package's source names
// ([assertable.asserted]); and on an error, the interfaces holding error's method
// that a package declares and asserts itself ([outsideAssertions.ofError]).
type outsideAssertions struct {
	resolve   *graph.Resolver
	program   map[string]bool
	assert    *assertable
	methodsOf map[outsideKey][]types.Object    // the methods retained, by type and package
	names     map[*types.Named]map[string]bool // the names a type or a pointer to it declares
	errorsOf  map[string][]errorAnswer         // [outsideAssertions.ofError], by exact type
	holding   *[]*types.Named                  // [outsideAssertions.errorHolders], on first use
}

// errorAnswer is one method of a type and the interface holding error's method
// whose assertion retains it.
type errorAnswer struct {
	method   types.Object
	asserted *types.Named
}

// outsideKey is one converted type and one package outside the program holding it.
type outsideKey struct {
	t   string
	pkg string
}

// newOutsideAssertions answers over one input.
func newOutsideAssertions(in *Input) *outsideAssertions {
	program := make(map[string]bool)
	for _, p := range programPackages(in.Result) {
		program[p.PkgPath] = true
	}
	return &outsideAssertions{
		resolve:   in.Resolve,
		program:   program,
		assert:    assertableOf(in),
		methodsOf: make(map[outsideKey][]types.Object),
		names:     make(map[*types.Named]map[string]bool),
		errorsOf:  make(map[string][]errorAnswer),
	}
}

// ofError is every method by which t, exactly as a site converts it to error,
// implements a defined interface holding error's method that a package outside the
// program declares and asserts in its own source, as code there may assert it on
// the error the program hands it. Only the source of a package declaring such an
// interface that t implements is read, and an interface declaring no method beyond
// Error is skipped, since the error site already retains Error.
func (o *outsideAssertions) ofError(t types.Type) []errorAnswer {
	named, isNamed := definedType(t)
	if !isNamed {
		return nil
	}
	key := o.resolve.TypeKey(t)
	if held, known := o.errorsOf[key]; known {
		return held
	}
	names, known := o.names[named]
	if !known {
		names = declaredMethodNames(named)
		o.names[named] = names
	}
	var held []errorAnswer
	for _, holder := range o.errorHolders() {
		iface, _ := holder.Underlying().(*types.Interface)
		if iface.NumMethods() <= 1 || !namesEvery(names, iface) || !types.Implements(t, iface) || !o.assert.assertsOwn(holder) {
			continue
		}
		for _, one := range satisfying(t, iface) {
			held = append(held, errorAnswer{method: one.method, asserted: holder})
		}
	}
	o.errorsOf[key] = held
	return held
}

// errorHolders is every non-generic defined interface whose method set holds
// error's that a package of the program's import closure outside the program
// declares, by package path and name.
func (o *outsideAssertions) errorHolders() []*types.Named {
	if o.holding != nil {
		return *o.holding
	}
	var found []*types.Named
	for _, pkg := range outsideClosure(o.assert.in) {
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			if named, holds := errorHolder(scope.Lookup(name)); holds {
				found = append(found, named)
			}
		}
	}
	o.holding = &found
	return found
}

// errorHolder is the defined type one package-level object declares, where it is a
// non-generic interface whose method set holds error's.
func errorHolder(obj types.Object) (*types.Named, bool) {
	typeName, isType := obj.(*types.TypeName)
	if !isType || typeName.IsAlias() {
		return nil, false
	}
	named, isNamed := typeName.Type().(*types.Named)
	if !isNamed || named.TypeParams().Len() > 0 {
		return nil, false
	}
	iface, isInterface := named.Underlying().(*types.Interface)
	return named, isInterface && iface.IsMethodSet() && types.Implements(iface, errorInterface)
}

// of is the package that declares one interface a site names, where that interface
// is a defined type of a package that is neither the target nor a loaded consumer.
func (o *outsideAssertions) of(iface types.Type) (*types.Package, bool) {
	named, isNamed := types.Unalias(iface).(*types.Named)
	if !isNamed || named.Obj().Pkg() == nil || o.program[named.Obj().Pkg().Path()] {
		return nil, false
	}
	return named.Obj().Pkg(), true
}

// methods is every method by which a value of t, or a pointer to it, implements an
// interface code of pkg asserts, each the declaration a deletion would remove.
func (o *outsideAssertions) methods(t types.Type, pkg *types.Package) []types.Object {
	named, isNamed := definedType(t)
	if !isNamed {
		return nil
	}
	key := outsideKey{t: o.resolve.TypeKey(named), pkg: pkg.Path()}
	if held, known := o.methodsOf[key]; known {
		return held
	}
	names, known := o.names[named]
	if !known {
		names = declaredMethodNames(named)
		o.names[named] = names
	}
	satisfied := satisfiedMethods(named, names, o.assert.asserted(pkg))
	var held []types.Object
	for selection := range types.NewMethodSet(types.NewPointer(named)).Methods() {
		if satisfied[selection.Obj().Name()] {
			held = append(held, selection.Obj())
		}
	}
	o.methodsOf[key] = held
	return held
}

// definedType is the defined type a value of t is, or a pointer to it points to.
func definedType(t types.Type) (*types.Named, bool) {
	if pointer, isPointer := types.Unalias(t).(*types.Pointer); isPointer {
		t = pointer.Elem()
	}
	named, isNamed := types.Unalias(t).(*types.Named)
	return named, isNamed
}

// typeDeclaration is the declaration of a defined type, or of the type a pointer
// points to.
func typeDeclaration(in *Input, t types.Type) graph.SymbolID {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return ""
	}
	id, _ := in.Resolve.Object(named.Origin().Obj())
	return id
}

// answer is one method of a type and the interface method it answers.
type answer struct {
	method  types.Object
	answers types.Object
}

// satisfying returns the method of from that answers each method to requires, in
// the order to declares them. A method promoted from an embedded field is the
// method of the type embedded, which is the declaration a deletion would remove.
func satisfying(from types.Type, to *types.Interface) []answer {
	set := types.NewMethodSet(from)
	methods := make([]answer, 0, to.NumMethods())
	for m := range to.Methods() {
		sel := set.Lookup(m.Pkg(), m.Name())
		if sel == nil {
			continue
		}
		methods = append(methods, answer{method: sel.Obj(), answers: m})
	}
	return methods
}

// byHeldSymbol orders two exemptions by site, then by the symbol retained, the
// detail recorded and the holder.
//
//nolint:gocritic // slices.SortFunc fixes a comparator's parameters to values.
func byHeldSymbol(a, b graph.Exemption) int {
	if c := bySite(&a, &b); c != 0 {
		return c
	}
	if c := strings.Compare(string(a.ID), string(b.ID)); c != 0 {
		return c
	}
	if c := strings.Compare(a.Detail, b.Detail); c != 0 {
		return c
	}
	if c := strings.Compare(string(a.Holder), string(b.Holder)); c != 0 {
		return c
	}
	return slices.Compare(a.CoHolders, b.CoHolders)
}

// sameExemption reports whether two exemptions state the same fact.
//
//nolint:gocritic // slices.CompactFunc fixes a comparator's parameters to values.
func sameExemption(a, b graph.Exemption) bool {
	return a.ID == b.ID && a.Class == b.Class && a.Consumer == b.Consumer && a.Site == b.Site && a.Detail == b.Detail &&
		a.Holder == b.Holder && slices.Equal(a.CoHolders, b.CoHolders) && a.Via == b.Via
}
