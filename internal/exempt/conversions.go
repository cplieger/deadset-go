package exempt

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/packages"
)

// conversion is the scan's own record: one Conversion, plus how the site spells
// the interface reached. The spelling is what a message carries and the shared
// value does not hold it, because the interface a Conversion names is the
// structural type and a defined interface's name is not derivable from it.
type conversion struct {
	Conversion
	name string
}

// Conversions returns every site in the loaded configuration where a value of a
// concrete type reaches a position typed as an interface: a satisfaction
// assertion, an explicit conversion, an argument, a result, an assignment to a
// variable or a field, an element stored into a container, a channel send and a
// range assignment.
//
// A type assertion and a type-switch case are not conversions: they take a value
// out of an interface rather than putting one in, so nothing there tells the
// analysis that the concrete type's methods are reached.
//
// A site whose value is itself of interface type records nothing, because the
// methods of an interface are the subject of the interface kinds rather than of
// an exemption, and neither does a site whose value has the type of a type
// parameter, whose method set comes from the constraint and holds interface
// methods only.
//
// The order is by position, then by the type converted and the interface reached,
// so two calls over one result return the same slice.
func Conversions(in *Input) ([]Conversion, error) {
	found, err := conversionSites(in)
	if err != nil {
		return nil, err
	}
	sites := make([]Conversion, 0, len(found))
	for i := range found {
		sites = append(sites, found[i].Conversion)
	}
	return sites, nil
}

// conversionSites is the scan behind Conversions, keeping the interface spelling
// the exported value drops.
func conversionSites(in *Input) ([]conversion, error) {
	if in == nil || in.Result == nil || in.Result.Fset == nil {
		return nil, fmt.Errorf("%w: no file set", graph.ErrIncompleteLoad)
	}

	s := &conversionScan{fset: in.Result.Fset, walked: make(map[string]bool)}
	for _, p := range sortedPackages(in.Result.Packages) {
		if p.TypesInfo == nil {
			continue
		}
		s.info = p.TypesInfo
		for _, f := range sortedFiles(p, in.Result.Fset) {
			name := in.Result.Fset.Position(f.FileStart).Filename
			if s.walked[name] {
				continue
			}
			s.walked[name] = true
			ast.Inspect(f, s.visit)
		}
	}
	slices.SortFunc(s.sites, s.bySite)
	return s.sites, nil
}

// sortedPackages orders the loaded packages by identifier, so the variant that
// walks a file a test variant also compiles is the same one on every run.
func sortedPackages(pkgs []*packages.Package) []*packages.Package {
	ordered := slices.Clone(pkgs)
	slices.SortFunc(ordered, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
	return ordered
}

// sortedFiles orders one package's syntax trees by file name.
func sortedFiles(p *packages.Package, fset *token.FileSet) []*ast.File {
	files := slices.Clone(p.Syntax)
	slices.SortFunc(files, func(a, b *ast.File) int {
		return strings.Compare(fset.Position(a.FileStart).Filename, fset.Position(b.FileStart).Filename)
	})
	return files
}

// conversionScan accumulates the conversion sites of one loaded configuration.
type conversionScan struct {
	fset    *token.FileSet
	info    *types.Info // the type information of the variant compiling the file being walked
	walked  map[string]bool
	results []types.Type // the result types of the enclosing function
	sites   []conversion
}

// bySite orders two conversions by position, then by the type converted and the
// interface reached.
func (s *conversionScan) bySite(a, b conversion) int {
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
	return strings.Compare(a.name, b.name)
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
	sig, ok := tv.Type.(*types.Signature)
	if !ok {
		return
	}
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

// keep holds one site, once dst names an interface and from names a concrete
// type of the program.
func (s *conversionScan) keep(dst, from types.Type, pos token.Pos) {
	iface := interfaceOf(dst)
	if iface == nil || !concrete(from) {
		return
	}
	reached := Conversion{From: from, To: iface, Site: pos}
	s.sites = append(s.sites, conversion{Conversion: reached, name: spell(dst)})
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
// interface: not an interface, not a type parameter, and not the untyped nil or
// an invalid type the type checker left behind.
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
	_, iface := from.Underlying().(*types.Interface)
	return !iface
}

// spell names one interface type the way a message carries it.
func spell(dst types.Type) string {
	return types.TypeString(dst, func(p *types.Package) string { return p.Name() })
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
