package graph

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Conversion is one site where a value reaches a position typed as an interface:
// an assertion, a conversion, an argument, a result, an assignment, a stored
// element, a send, a range assignment, or a type argument whose constraint
// declares methods.
//
// From is the value's type, possibly an interface, never a type parameter. To is
// the structural interface reached; Interface is the type the site names, and Name
// spells it as the site does, because a defined interface is not derivable from To.
type Conversion struct {
	From      types.Type
	Interface types.Type
	To        *types.Interface
	Name      string
	Site      token.Pos
	Held      token.Pos // on an assertion-derived site, a conversion of From into an interface, one derived site per declaration converting it: the assertion is reached only while that declaration runs
}

// Conversions returns every conversion site of one loaded configuration's
// packages, each file walked once from the first package by identifier that
// compiles it, ordered by position, then by the type converted and the interface
// reached.
//
// A type assertion takes a value out of an interface, so it is no site itself; a
// concrete type the set converts that implements both the operand's interface and
// the asserted one reaches the asserted interface there.
func Conversions(fset *token.FileSet, pkgs []*packages.Package) []Conversion {
	s := &conversionScan{fset: fset}
	walked := make(map[string]bool)
	for _, p := range SortedPackages(pkgs) {
		if p.TypesInfo == nil {
			continue
		}
		s.info = p.TypesInfo
		for _, f := range SortedSyntax(p, fset) {
			name := fset.Position(f.FileStart).Filename
			if walked[name] {
				continue
			}
			walked[name] = true
			s.walk(f)
		}
	}
	s.sites = append(s.sites, s.asserted()...)
	slices.SortFunc(s.sites, s.bySite)
	return s.sites
}

// conversionsIn returns the conversion sites one declaration's syntax holds,
// under the type information of the package compiling it, in syntax order.
func conversionsIn(info *types.Info, decl ast.Node) []Conversion {
	s := &conversionScan{info: info}
	ast.Inspect(decl, s.visit)
	return s.sites
}

// SortedPackages orders packages by identifier, so the variant that walks a file
// a test variant also compiles is the same one on every run.
func SortedPackages(pkgs []*packages.Package) []*packages.Package {
	ordered := slices.Clone(pkgs)
	slices.SortFunc(ordered, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
	return ordered
}

// SortedSyntax orders one package's syntax trees by file name.
func SortedSyntax(p *packages.Package, fset *token.FileSet) []*ast.File {
	files := slices.Clone(p.Syntax)
	slices.SortFunc(files, func(a, b *ast.File) int {
		return strings.Compare(fset.Position(a.FileStart).Filename, fset.Position(b.FileStart).Filename)
	})
	return files
}

// conversionScan accumulates the conversion sites of one loaded configuration.
type conversionScan struct {
	fset       *token.FileSet
	info       *types.Info  // the type information of the variant compiling the file being walked
	results    []types.Type // the result types of the enclosing function
	sites      []Conversion
	holders    []token.Pos // per site, the start of the top-level declaration or specification holding it
	assertions []assertion
	holder     token.Pos
}

// walk visits one file a top-level declaration, or a specification of a group, at
// a time, so each site records the one that holds it.
func (s *conversionScan) walk(f *ast.File) {
	for _, decl := range f.Decls {
		gen, grouped := decl.(*ast.GenDecl)
		if !grouped {
			s.holder = decl.Pos()
			ast.Inspect(decl, s.visit)
			continue
		}
		for _, spec := range gen.Specs {
			s.holder = spec.Pos()
			ast.Inspect(spec, s.visit)
		}
	}
}

// assertion is one type assertion or type-switch case naming an interface, on an
// operand of interface type.
type assertion struct {
	operand *types.Interface
	to      types.Type
	site    token.Pos
}

// bySite orders two conversions by position, then by the type converted and the
// interface reached.
func (s *conversionScan) bySite(a, b Conversion) int {
	p, q := s.fset.Position(a.Site), s.fset.Position(b.Site)
	if c := strings.Compare(p.Filename, q.Filename); c != 0 {
		return c
	}
	if c := p.Offset - q.Offset; c != 0 {
		return c
	}
	if c := strings.Compare(a.From.String(), b.From.String()); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

// visit records the conversions one node carries. A function's body is walked
// under that function's result types, so a return statement knows what it returns
// into.
func (s *conversionScan) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.FuncDecl:
		s.function(s.declaredSignature(n), n.Type, n.Body)
		return false
	case *ast.FuncLit:
		sig, _ := s.typeOf(n).(*types.Signature)
		s.function(sig, n.Type, n.Body)
		return false
	case *ast.ValueSpec:
		s.valueSpec(n)
	case *ast.AssignStmt:
		s.assign(n)
	case *ast.ReturnStmt:
		s.returnStmt(n)
	case *ast.CallExpr:
		s.call(n)
	case *ast.CompositeLit:
		s.composite(n)
	case *ast.SendStmt:
		s.send(n)
	case *ast.RangeStmt:
		s.rangeStmt(n)
	case *ast.Ident:
		s.instance(n)
	case *ast.TypeAssertExpr:
		if n.Type != nil {
			s.assert(n.X, n.Type)
		}
	case *ast.TypeSwitchStmt:
		s.typeSwitch(n)
	}
	return true
}

// declaredSignature returns the signature of one declared function or method.
func (s *conversionScan) declaredSignature(d *ast.FuncDecl) *types.Signature {
	obj := s.info.Defs[d.Name]
	if obj == nil {
		return nil
	}
	sig, _ := obj.Type().(*types.Signature)
	return sig
}

// function walks one function under its own result types.
func (s *conversionScan) function(sig *types.Signature, t *ast.FuncType, body *ast.BlockStmt) {
	outer := s.results
	s.results = resultTypes(sig)
	if t != nil {
		ast.Inspect(t, s.visit)
	}
	if body != nil {
		ast.Inspect(body, s.visit)
	}
	s.results = outer
}

// resultTypes lists the types one signature returns.
func resultTypes(sig *types.Signature) []types.Type {
	if sig == nil {
		return nil
	}
	return tupleTypes(sig.Results())
}

// tupleTypes lists the types one tuple holds.
func tupleTypes(t *types.Tuple) []types.Type {
	if t == nil {
		return nil
	}
	out := make([]types.Type, 0, t.Len())
	for v := range t.Variables() {
		out = append(out, v.Type())
	}
	return out
}

// valueSpec records a declaration that names an interface type: the satisfaction
// assertion, and every other declaration of an interface-typed variable with an
// initial value.
func (s *conversionScan) valueSpec(spec *ast.ValueSpec) {
	if spec.Type == nil || len(spec.Values) == 0 {
		return
	}
	tv, ok := s.info.Types[spec.Type]
	if !ok || !tv.IsType() {
		return
	}
	declared := make([]types.Type, len(spec.Names))
	for i := range declared {
		declared[i] = tv.Type
	}
	s.into(declared, spec.Values)
}

// assign records an assignment into a variable, a field, an element or a
// dereference of interface type. A short variable declaration gives its
// destination the type of the value, so it converts nothing.
func (s *conversionScan) assign(a *ast.AssignStmt) {
	if a.Tok != token.ASSIGN {
		return
	}
	declared := make([]types.Type, 0, len(a.Lhs))
	for _, l := range a.Lhs {
		declared = append(declared, s.typeOf(l))
	}
	s.into(declared, a.Rhs)
}

// returnStmt records a value returned into a result of interface type.
func (s *conversionScan) returnStmt(r *ast.ReturnStmt) {
	if len(r.Results) == 0 {
		return
	}
	s.into(s.results, r.Results)
}

// call records the arguments of one call, and the operand of one explicit
// conversion, which the syntax spells the same way.
func (s *conversionScan) call(c *ast.CallExpr) {
	tv, ok := s.info.Types[c.Fun]
	if !ok {
		return
	}
	if tv.IsType() {
		if len(c.Args) == 1 {
			s.record(tv.Type, c.Args[0])
		}
		return
	}
	sig, ok := tv.Type.Underlying().(*types.Signature)
	if !ok {
		return
	}
	s.errorsAs(c)
	// One call supplying every parameter from one multi-valued operand.
	if len(c.Args) == 1 && !c.Ellipsis.IsValid() {
		if tuple, ok := s.typeOf(c.Args[0]).(*types.Tuple); ok {
			for i := range tuple.Len() {
				s.keep(parameterType(sig, i, false), tuple.At(i).Type(), c.Args[0].Pos())
			}
			return
		}
	}
	for i, arg := range c.Args {
		s.record(parameterType(sig, i, c.Ellipsis.IsValid()), arg)
	}
}

// errorsAs keeps a call of errors.As or errors.AsType as the assertion of an error
// to the target type it is, which the standard library makes by reflection.
func (s *conversionScan) errorsAs(c *ast.CallExpr) {
	name := calleeName(c.Fun)
	fn, isFunc := s.info.Uses[name].(*types.Func)
	if name == nil || !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != "errors" {
		return
	}
	var target types.Type
	switch fn.Name() {
	case "As":
		if len(c.Args) == 2 {
			if pointer, isPointer := s.typeOf(c.Args[1]).(*types.Pointer); isPointer {
				target = pointer.Elem()
			}
		}
	case "AsType":
		if inst, instantiated := s.info.Instances[name]; instantiated && inst.TypeArgs.Len() == 1 {
			target = inst.TypeArgs.At(0)
		}
	}
	if target == nil || interfaceOf(target) == nil {
		return
	}
	errorType, _ := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	s.assertions = append(s.assertions, assertion{operand: errorType, to: target, site: c.Pos()})
}

// calleeName is the identifier a call names its function by, through a package
// qualifier and an instantiation, and nil for any other callee.
func calleeName(fun ast.Expr) *ast.Ident {
	switch f := ast.Unparen(fun).(type) {
	case *ast.IndexExpr:
		return calleeName(f.X)
	case *ast.IndexListExpr:
		return calleeName(f.X)
	case *ast.SelectorExpr:
		return f.Sel
	case *ast.Ident:
		return f
	default:
		return nil
	}
}

// parameterType returns the type of the parameter the argument at index reaches.
// An argument past the last parameter of a variadic signature reaches the
// element type of that parameter, unless the call spreads a slice into it, where
// it reaches the slice.
func parameterType(sig *types.Signature, index int, spread bool) types.Type {
	params := sig.Params()
	if params == nil || params.Len() == 0 {
		return nil
	}
	last := params.Len() - 1
	if index < last {
		return params.At(index).Type()
	}
	final := params.At(last).Type()
	if !sig.Variadic() {
		if index > last {
			return nil
		}
		return final
	}
	if spread {
		return final
	}
	if slice, ok := final.(*types.Slice); ok {
		return slice.Elem()
	}
	return final
}

// composite records the elements of one composite literal stored into an
// interface-typed element, key or field. The literal's own type is read from the
// type information rather than from the syntax, so an element whose type the
// outer literal elides is recorded too.
func (s *conversionScan) composite(lit *ast.CompositeLit) {
	switch u := underlying(s.typeOf(lit)).(type) {
	case *types.Slice:
		s.elements(u.Elem(), nil, lit.Elts)
	case *types.Array:
		s.elements(u.Elem(), nil, lit.Elts)
	case *types.Map:
		s.elements(u.Elem(), u.Key(), lit.Elts)
	case *types.Struct:
		s.fields(u, lit.Elts)
	}
}

// elements records the elements of a slice, an array or a map literal. A keyed
// element of a slice or an array carries an index rather than a key, so key is
// nil there and the key records nothing.
func (s *conversionScan) elements(elem, key types.Type, elts []ast.Expr) {
	for _, e := range elts {
		kv, keyed := e.(*ast.KeyValueExpr)
		if !keyed {
			s.record(elem, e)
			continue
		}
		if key != nil {
			s.record(key, kv.Key)
		}
		s.record(elem, kv.Value)
	}
}

// fields records the elements of one struct literal, keyed or positional.
func (s *conversionScan) fields(st *types.Struct, elts []ast.Expr) {
	for i, e := range elts {
		kv, keyed := e.(*ast.KeyValueExpr)
		if !keyed {
			if i < st.NumFields() {
				s.record(st.Field(i).Type(), e)
			}
			continue
		}
		name, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if field, ok := s.info.Uses[name].(*types.Var); ok {
			s.record(field.Type(), kv.Value)
		}
	}
}

// send records a value sent into a channel of interface element type.
func (s *conversionScan) send(st *ast.SendStmt) {
	if ch, ok := underlying(s.typeOf(st.Chan)).(*types.Chan); ok {
		s.record(ch.Elem(), st.Value)
	}
}

// rangeStmt records a range clause that assigns into an existing variable of
// interface type. A clause that declares its variables gives each the type the
// range yields, so it converts nothing.
func (s *conversionScan) rangeStmt(r *ast.RangeStmt) {
	if r.Tok != token.ASSIGN {
		return
	}
	key, value := s.rangeTypes(r.X)
	if r.Key != nil {
		s.keep(s.typeOf(r.Key), key, r.X.Pos())
	}
	if r.Value != nil {
		s.keep(s.typeOf(r.Value), value, r.X.Pos())
	}
}

// rangeTypes returns the two types a range clause over x yields. A clause over
// an integer or a channel yields one value, so the second is nil.
func (s *conversionScan) rangeTypes(x ast.Expr) (key, value types.Type) {
	switch u := underlying(s.typeOf(x)).(type) {
	case *types.Basic:
		if u.Info()&types.IsString != 0 {
			return types.Typ[types.Int], types.Typ[types.Rune]
		}
		return s.typeOf(x), nil
	case *types.Slice:
		return types.Typ[types.Int], u.Elem()
	case *types.Array:
		return types.Typ[types.Int], u.Elem()
	case *types.Pointer:
		if a, ok := underlying(u.Elem()).(*types.Array); ok {
			return types.Typ[types.Int], a.Elem()
		}
	case *types.Map:
		return u.Key(), u.Elem()
	case *types.Chan:
		return u.Elem(), nil
	case *types.Signature:
		return yieldTypes(u)
	}
	return nil, nil
}

// yieldTypes returns the two types the yield function of a range-over-function
// clause takes.
func yieldTypes(sig *types.Signature) (key, value types.Type) {
	if sig.Params().Len() != 1 {
		return nil, nil
	}
	yield, ok := underlying(sig.Params().At(0).Type()).(*types.Signature)
	if !ok {
		return nil, nil
	}
	declared := tupleTypes(yield.Params())
	for len(declared) < 2 {
		declared = append(declared, nil)
	}
	return declared[0], declared[1]
}

// into records every value of one assignment or declaration against the type of
// the destination it reaches, including the one multi-valued operand that
// supplies every destination.
func (s *conversionScan) into(declared []types.Type, values []ast.Expr) {
	if len(values) == 1 && len(declared) > 1 {
		if tuple, ok := s.typeOf(values[0]).(*types.Tuple); ok && tuple.Len() == len(declared) {
			for i := range declared {
				s.keep(declared[i], tuple.At(i).Type(), values[0].Pos())
			}
		}
		return
	}
	if len(declared) != len(values) {
		return
	}
	for i, v := range values {
		s.record(declared[i], v)
	}
}

// record keeps the site where the value of one expression reaches dst. An
// expression that denotes a type rather than a value reaches nothing: the
// argument of make and of new is spelled like an argument and is a type.
func (s *conversionScan) record(dst types.Type, value ast.Expr) {
	tv, ok := s.info.Types[value]
	if ok && tv.IsType() {
		return
	}
	s.keep(dst, s.typeOf(value), value.Pos())
}

// keep holds one site, once dst names an interface and from names a type whose
// method set is its own.
func (s *conversionScan) keep(dst, from types.Type, pos token.Pos) {
	iface := interfaceOf(dst)
	if iface == nil || !concrete(from) {
		return
	}
	s.sites = append(s.sites, Conversion{From: from, Interface: dst, To: iface, Name: spell(dst), Site: pos})
	s.holders = append(s.holders, s.holder)
}

// interfaceOf returns the interface dst is, and nil when it is not one. The
// underlying type of a type parameter is its constraint, which is an interface
// the parameter does not stand for, so a type parameter names none.
func interfaceOf(dst types.Type) *types.Interface {
	if dst == nil {
		return nil
	}
	if _, ok := dst.(*types.TypeParam); ok {
		return nil
	}
	iface, _ := dst.Underlying().(*types.Interface)
	return iface
}

// concrete reports whether from is a type whose own method set can satisfy an
// interface: an interface or a defined or literal type, and not a type parameter,
// the untyped nil or an invalid type the type checker left behind.
func concrete(from types.Type) bool {
	if from == nil {
		return false
	}
	switch t := from.(type) {
	case *types.TypeParam, *types.Tuple:
		return false
	case *types.Basic:
		if t.Kind() == types.Invalid || t.Info()&types.IsUntyped != 0 {
			return false
		}
	}
	return true
}

// spell names one interface type the way a message carries it.
func spell(dst types.Type) string {
	return types.TypeString(dst, func(p *types.Package) string { return p.Name() })
}

// instance records the type arguments one instantiation passes to type
// parameters whose constraints declare methods: each argument reaches the methods
// its constraint requires, through which the generic body calls them.
func (s *conversionScan) instance(id *ast.Ident) {
	inst, ok := s.info.Instances[id]
	if !ok {
		return
	}
	var params *types.TypeParamList
	switch obj := s.info.Uses[id].(type) {
	case *types.Func:
		params = obj.Signature().TypeParams()
	case *types.TypeName:
		if named, isNamed := obj.Type().(*types.Named); isNamed {
			params = named.TypeParams()
		}
	}
	for i := range min(params.Len(), inst.TypeArgs.Len()) {
		constraint := params.At(i).Constraint()
		from := inst.TypeArgs.At(i)
		if !concrete(from) {
			continue
		}
		if required := requiredMethods(constraint, params, inst.TypeArgs); required != nil {
			s.sites = append(s.sites, Conversion{From: from, Interface: constraint, To: required, Name: spell(constraint), Site: id.Pos()})
			s.holders = append(s.holders, s.holder)
		}
	}
}

// requiredMethods is the interface of the methods one constraint requires of the
// type arguments of one instantiation, with each method's signature written at
// those arguments, and nil where it requires none. The type terms are left out,
// because an argument the program compiles with is in the type set.
func requiredMethods(constraint types.Type, params *types.TypeParamList, args *types.TypeList) *types.Interface {
	iface := interfaceOf(constraint)
	if iface == nil || iface.NumMethods() == 0 {
		return nil
	}
	return substitutedInterface(iface, params, args)
}

// instantiatedAt is one generic type's instance with every type parameter of
// params among its type arguments replaced by the argument at its index, and the
// type itself where it has no type argument or the instantiation fails.
func instantiatedAt(named *types.Named, params *types.TypeParamList, args *types.TypeList) types.Type {
	if named.TypeArgs().Len() == 0 {
		return named
	}
	mapped := make([]types.Type, named.TypeArgs().Len())
	for j := range mapped {
		mapped[j] = substituted(named.TypeArgs().At(j), params, args)
	}
	instantiated, err := types.Instantiate(nil, named.Origin(), mapped, false)
	if err != nil {
		return named
	}
	return instantiated
}

// substituted is t with every type parameter of params replaced by the argument
// at its index, through every type constructor and through the type arguments of
// a generic type. A signature loses its receiver.
func substituted(t types.Type, params *types.TypeParamList, args *types.TypeList) types.Type {
	switch u := types.Unalias(t).(type) {
	case *types.TypeParam:
		for k := range min(params.Len(), args.Len()) {
			if params.At(k) == u {
				return args.At(k)
			}
		}
	case *types.Pointer:
		return types.NewPointer(substituted(u.Elem(), params, args))
	case *types.Slice:
		return types.NewSlice(substituted(u.Elem(), params, args))
	case *types.Array:
		return types.NewArray(substituted(u.Elem(), params, args), u.Len())
	case *types.Map:
		return types.NewMap(substituted(u.Key(), params, args), substituted(u.Elem(), params, args))
	case *types.Chan:
		return types.NewChan(u.Dir(), substituted(u.Elem(), params, args))
	case *types.Signature:
		return types.NewSignatureType(nil, nil, nil,
			substitutedTuple(u.Params(), params, args), substitutedTuple(u.Results(), params, args), u.Variadic())
	case *types.Struct:
		return substitutedStruct(u, params, args)
	case *types.Interface:
		return substitutedInterface(u, params, args)
	case *types.Named:
		return instantiatedAt(u, params, args)
	}
	return t
}

// substitutedStruct is one struct type with [substituted] applied to every field's
// type.
func substitutedStruct(u *types.Struct, params *types.TypeParamList, args *types.TypeList) *types.Struct {
	fields := make([]*types.Var, u.NumFields())
	tags := make([]string, u.NumFields())
	for i := range u.NumFields() {
		f := u.Field(i)
		fields[i] = types.NewField(f.Pos(), f.Pkg(), f.Name(), substituted(f.Type(), params, args), f.Embedded())
		tags[i] = u.Tag(i)
	}
	return types.NewStruct(fields, tags)
}

// substitutedInterface is the interface of one interface type's methods with
// [substituted] applied to every signature. Its type terms are left out.
func substitutedInterface(u *types.Interface, params *types.TypeParamList, args *types.TypeList) *types.Interface {
	methods := make([]*types.Func, 0, u.NumMethods())
	for m := range u.Methods() {
		sig, _ := substituted(m.Signature(), params, args).(*types.Signature)
		methods = append(methods, types.NewFunc(m.Pos(), m.Pkg(), m.Name(), sig))
	}
	return types.NewInterfaceType(methods, nil).Complete()
}

// substitutedTuple is one parameter or result list with [substituted] applied to
// every variable's type.
func substitutedTuple(tuple *types.Tuple, params *types.TypeParamList, args *types.TypeList) *types.Tuple {
	vars := make([]*types.Var, 0, tuple.Len())
	for v := range tuple.Variables() {
		vars = append(vars, types.NewParam(v.Pos(), v.Pkg(), v.Name(), substituted(v.Type(), params, args)))
	}
	return types.NewTuple(vars...)
}

// assert keeps one type assertion of an interface-typed operand to an interface.
func (s *conversionScan) assert(operand, asserted ast.Expr) {
	from := interfaceOf(s.typeOf(operand))
	to := s.typeOf(asserted)
	if from == nil || interfaceOf(to) == nil {
		return
	}
	s.assertions = append(s.assertions, assertion{operand: from, to: to, site: asserted.Pos()})
}

// typeSwitch keeps every interface case of one type switch.
func (s *conversionScan) typeSwitch(ts *ast.TypeSwitchStmt) {
	var operand ast.Expr
	switch a := ts.Assign.(type) {
	case *ast.ExprStmt:
		operand = a.X
	case *ast.AssignStmt:
		if len(a.Rhs) == 1 {
			operand = a.Rhs[0]
		}
	}
	guard, ok := ast.Unparen(operand).(*ast.TypeAssertExpr)
	if !ok {
		return
	}
	for _, stmt := range ts.Body.List {
		if clause, isClause := stmt.(*ast.CaseClause); isClause {
			for _, expr := range clause.List {
				s.assert(guard.X, expr)
			}
		}
	}
}

// asserted returns the sites the scan's assertions reach: every concrete type the
// conversion set holds, at every assertion whose operand's interface and asserted
// interface it implements both.
func (s *conversionScan) asserted() []Conversion {
	held := s.concreteTypes()
	implementing := make(map[string][]heldType)
	var reached []Conversion
	for _, a := range s.assertions {
		to := interfaceOf(a.to)
		if to.NumMethods() == 0 {
			continue
		}
		pair := identity(a.operand) + "\x00" + identity(a.to)
		both, computed := implementing[pair]
		if !computed {
			both = implementingBoth(held, a.operand, to)
			implementing[pair] = both
		}
		for _, h := range both {
			site := a.site
			if h.testOnly.IsValid() {
				site = h.testOnly
			}
			for _, held := range h.held {
				reached = append(reached, Conversion{From: h.from, Interface: a.to, To: to, Name: spell(a.to), Site: site, Held: held})
			}
		}
	}
	return reached
}

// heldType is one concrete type the scan's sites convert. testOnly is its first
// site when a test file holds every one of them, and no position otherwise: an
// assertion reaching the type then reaches it only while the tests run, so the
// derived site is that test file's. held is the first site of each holder that
// converts it, test-file holders only when testOnly is set.
type heldType struct {
	from     types.Type
	held     []token.Pos
	testOnly token.Pos
}

// implementingBoth keeps the types of held that implement both interfaces.
func implementingBoth(held []heldType, a, b *types.Interface) []heldType {
	var both []heldType
	for _, h := range held {
		if types.Implements(h.from, a) && types.Implements(h.from, b) {
			both = append(both, h)
		}
	}
	return both
}

// concreteTypes lists, once each in the order of their first sites, every type the
// scan's sites convert that is not an interface, with the first site of each holder
// that converts it.
func (s *conversionScan) concreteTypes() []heldType {
	held, index := s.heldTypes()
	seen := make(map[string]bool)
	for i := range s.sites {
		c := &s.sites[i]
		key := identity(c.From)
		at, kept := index[key]
		if !kept {
			continue
		}
		if _, test := IsTestFile(s.fset.Position(c.Site).Filename); test != held[at].testOnly.IsValid() {
			continue
		}
		if holder := fmt.Sprintf("%s\x00%d", key, s.holders[i]); !seen[holder] {
			seen[holder] = true
			held[at].held = append(held[at].held, c.Site)
		}
	}
	return held
}

// heldTypes lists the types concreteTypes does, each without its holders, and the
// index of each by identity.
func (s *conversionScan) heldTypes() (held []heldType, index map[string]int) {
	index = make(map[string]int)
	for i := range s.sites {
		c := &s.sites[i]
		if types.IsInterface(c.From) {
			continue
		}
		key := identity(c.From)
		_, test := IsTestFile(s.fset.Position(c.Site).Filename)
		at, seen := index[key]
		switch {
		case !seen && test:
			index[key] = len(held)
			held = append(held, heldType{from: c.From, testOnly: c.Site})
		case !seen:
			index[key] = len(held)
			held = append(held, heldType{from: c.From})
		case !test:
			held[at].testOnly = token.NoPos
		}
	}
	return held, index
}

// identity spells one type so that two types share a spelling only when they are
// identical: the package of each name is spelled by its own identity, because two
// variants of one package declare two distinct types under one path.
func identity(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return fmt.Sprintf("%p", p) })
}

// underlying returns the underlying type of t, and nil for a nil t.
func underlying(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}

// typeOf returns the type one expression has, and nil when the type checker
// recorded none.
func (s *conversionScan) typeOf(e ast.Expr) types.Type {
	if e == nil {
		return nil
	}
	return s.info.TypeOf(e)
}
