package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// walkUses is rule M3 over every file of the program and rule M4 over the
// target's importable packages: it records, per enumerated type, the first use a
// production file and a test file make that the list does not name, the first
// exported declaration holding it, and every constant expression of the type.
func (s *enumGroupScan) walkUses() {
	if len(s.enums.keys) == 0 {
		return
	}
	seen := make(map[string]bool)
	for _, p := range s.in.Facts.Program() {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range s.in.Facts.Files(p) {
			name := s.in.offsetOf(f.Package).File
			if seen[name] {
				continue
			}
			seen[name] = true
			w := &usesWalk{s: s, info: p.TypesInfo}
			ast.Inspect(f, w.visit)
		}
	}
	for _, p := range s.in.Facts.Packages() {
		if p.TypesInfo != nil {
			s.declarations(p)
		}
	}
}

// declarations retains, for every enumerated type it holds, a package-level
// variable or function of the target that another compilation unit reaches by
// name: one a //go:linkname or //export directive names, or one a source in
// another language than Go names (rule N); and in a library every exported
// declaration of an importable package (rule M4).
func (s *enumGroupScan) declarations(p *packages.Package) {
	linked := directiveNames(p)
	scope := p.Types.Scope()
	published := s.published(p)
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !s.in.Resolve.Inside(obj.Pos()) {
			continue
		}
		held := s.holds.of(obj.Type())
		if held.empty() {
			continue
		}
		switch {
		case published && obj.Exported() && !s.in.Resolve.Positions().InTestFile(obj.Pos()):
			s.exported(held, obj.Pos())
		case linked[name] || s.in.nonGo().named(p, obj):
			s.fail(held, obj.Pos(), "is reached by name from another compilation unit")
		}
	}
}

// exported records an exported declaration of an importable package that holds
// the enumerated types held.
func (s *enumGroupScan) exported(held egBits, pos token.Pos) {
	site := s.siteAt(pos, "is held by an exported declaration of an importable package")
	held.each(func(i int) {
		enum := s.enums.byKey[s.enums.keys[i]]
		if enum.exported == nil || site.before(enum.exported) {
			enum.exported = site
		}
	})
}

// directiveNames is every name a //go:linkname directive gives as its local name
// and every name an //export directive gives C in one package's files.
func directiveNames(p *packages.Package) map[string]bool {
	found := make(map[string]bool)
	for _, f := range p.Syntax {
		for _, group := range f.Comments {
			for _, c := range group.List {
				fields := strings.Fields(c.Text)
				if len(fields) >= 2 && (fields[0] == "//go:linkname" || fields[0] == "//export") {
					found[fields[1]] = true
				}
			}
		}
	}
	return found
}

// fail records a use the list does not name for every enumerated type held.
func (s *enumGroupScan) fail(held egBits, pos token.Pos, detail string) {
	site := s.siteAt(pos, detail)
	held.each(func(i int) {
		enum := s.enums.byKey[s.enums.keys[i]]
		at := &enum.useProduction
		if site.test {
			at = &enum.useTest
		}
		if *at == nil || site.before(*at) {
			*at = site
		}
	})
}

// egItem is one constant expression of an enumerated type that is no member's
// name: its value, whether that value is fixed, the constant it plainly names if
// any, every constant it names, and where it is written.
type egItem struct {
	value constant.Value
	plain *egConst
	site  *egSite
	label string
	names []*egConst
	fixed bool
}

// usesWalk reads the uses of the enumerated types in one file.
type usesWalk struct {
	s     *enumGroupScan
	info  *types.Info
	stack []ast.Node
	funcs []*types.Signature // the enclosing functions, innermost last
}

func (w *usesWalk) visit(n ast.Node) bool {
	if n == nil {
		top := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]
		switch top.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			w.funcs = w.funcs[:len(w.funcs)-1]
		}
		return true
	}
	if !w.node(n) {
		return false
	}
	w.stack = append(w.stack, n)
	switch n := n.(type) {
	case *ast.FuncDecl:
		var sig *types.Signature
		if fn, ok := w.info.Defs[n.Name].(*types.Func); ok {
			sig = fn.Signature()
		}
		w.funcs = append(w.funcs, sig)
	case *ast.FuncLit:
		sig, _ := typeUnder(w.info.TypeOf(n)).(*types.Signature)
		w.funcs = append(w.funcs, sig)
	}
	return true
}

// node reads one node and reports whether its children are read.
func (w *usesWalk) node(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.GenDecl:
		return n.Tok != token.CONST // the block model judges constant declarations
	case *ast.CaseClause:
		w.typeCases(n)
	case *ast.TypeAssertExpr:
		if n.Type != nil {
			w.retainType(w.info.TypeOf(n.Type), n.Type.Pos(), assertedDetail)
		}
	}
	if id, ok := n.(*ast.Ident); ok {
		w.instance(id)
	}
	if e, ok := n.(ast.Expr); ok {
		return w.expr(e)
	}
	return true
}

// retainType records a use the list does not name for every type t holds.
func (w *usesWalk) retainType(t types.Type, pos token.Pos, detail string) {
	if held := w.s.holds.of(t); !held.empty() {
		w.s.fail(held, pos, detail)
	}
}

// instance records a type argument that holds an enumerated type, written or
// inferred.
func (w *usesWalk) instance(id *ast.Ident) {
	inst, ok := w.info.Instances[id]
	if !ok {
		return
	}
	for t := range inst.TypeArgs.Types() {
		w.retainType(t, id.Pos(), "is a type argument")
	}
}

// typeCases records each type a case of a type switch names.
func (w *usesWalk) typeCases(clause *ast.CaseClause) {
	if len(w.stack) < 2 {
		return
	}
	if _, typeSwitch := w.stack[len(w.stack)-2].(*ast.TypeSwitchStmt); !typeSwitch {
		return
	}
	for _, expr := range clause.List {
		w.retainType(w.info.TypeOf(expr), expr.Pos(), assertedDetail)
	}
}

// assertedDetail is the clause of a value taken out of an interface value.
const assertedDetail = "is asserted out of an interface value"

// expr reads one expression and reports whether its children are read. A constant
// expression of an enumerated type that no larger constant expression of the type
// holds is an item of rule M2, judged by its value; the walk reads on into it for
// the constant expressions of other types it holds, each judged as its own.
func (w *usesWalk) expr(e ast.Expr) bool {
	tv, recorded := w.info.Types[e]
	if recorded && tv.IsType() || w.notAValue(e) {
		return true
	}
	t := w.info.TypeOf(e)
	if t == nil {
		return true
	}
	if tv.Value != nil {
		if enum := w.s.enumOf(t); enum != nil && !w.heldByConstant(enum) {
			w.item(enum, e, tv.Value)
			if detail, follows := w.used(e); !follows {
				w.retainType(t, e.Pos(), detail)
			}
		}
		return true
	}
	if !w.s.holds.of(t).empty() {
		w.check(e, t)
	}
	return true
}

// heldByConstant reports whether a larger constant expression of the enumerated
// type holds the expression the walk reads, looking out through the constant
// expressions that enclose it.
func (w *usesWalk) heldByConstant(enum *egEnum) bool {
	for _, n := range slices.Backward(w.stack) {
		e, isExpr := n.(ast.Expr)
		if !isExpr {
			return false
		}
		tv := w.info.Types[e]
		if tv.Value == nil {
			return false
		}
		if w.s.enumOf(tv.Type) == enum {
			return true
		}
	}
	return false
}

// item records one constant expression of an enumerated type that is no
// member's plain name.
func (w *usesWalk) item(enum *egEnum, e ast.Expr, v constant.Value) {
	switch u := ast.Unparen(e).(type) {
	case *ast.Ident, *ast.SelectorExpr:
		if c := w.s.constOf(w.info, u); c != nil && slices.Contains(enum.members, c.id) {
			return
		}
	}
	value := constant.ToInt(v)
	enum.items = append(enum.items, egItem{
		value: value,
		fixed: value.Kind() == constant.Int && w.s.fixedExpr(w.info, e, map[*egConst]bool{}),
		plain: w.s.plainName(w.info, e, w.info.TypeOf(e)),
		names: w.s.namedConsts(w.info, e),
		site:  w.s.siteAt(e.Pos(), "is a constant expression of the type"),
		label: types.ExprString(e),
	})
}

// notAValue reports whether an expression stands for no value of its own: a name a
// declaration defines, the selected name of a selector, and the field name a struct
// literal's key is.
func (w *usesWalk) notAValue(e ast.Expr) bool {
	id, isIdent := e.(*ast.Ident)
	if !isIdent {
		_, keyed := e.(*ast.KeyValueExpr)
		return keyed
	}
	if w.info.Defs[id] != nil || id.Name == "_" {
		return true
	}
	if _, pkg := w.info.Uses[id].(*types.PkgName); pkg {
		return true
	}
	if len(w.stack) == 0 {
		return false
	}
	switch p := w.stack[len(w.stack)-1].(type) {
	case *ast.SelectorExpr:
		return p.Sel == id
	case *ast.KeyValueExpr:
		if p.Key != id || len(w.stack) < 2 {
			return false
		}
		lit, isLit := w.stack[len(w.stack)-2].(*ast.CompositeLit)
		if !isLit {
			return false
		}
		u := typeUnder(w.info.TypeOf(lit))
		if ptr, isPtr := u.(*types.Pointer); isPtr {
			u = typeUnder(ptr.Elem())
		}
		_, isStruct := u.(*types.Struct)
		return isStruct
	}
	return false
}

// check records the types an expression holds unless both the way it is produced
// and the way it is used are on the list.
func (w *usesWalk) check(e ast.Expr, t types.Type) {
	if detail, follows := w.produced(e); !follows {
		w.retainType(t, e.Pos(), detail)
		return
	}
	if detail, follows := w.used(e); !follows {
		w.retainType(t, e.Pos(), detail)
	}
}

// The clauses of the uses the list does not name.
const (
	usedDetail  = "is used where the analysis does not follow it"
	indexDetail = "is used as an index"
)

// produced reports whether the way one expression is produced is on the list, and
// the clause where it is not.
func (w *usesWalk) produced(e ast.Expr) (string, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		return w.result(e)
	case *ast.TypeAssertExpr:
		return assertedDetail, false
	case *ast.BinaryExpr:
		if !comparison(e.Op) {
			return operatorDetail(e.Op), false
		}
	case *ast.UnaryExpr:
		if e.Op != token.AND && e.Op != token.ARROW {
			return operatorDetail(e.Op), false
		}
	}
	return "", true
}

// result reports whether the value one call returns is on the list: a conversion
// between identical types, make, new, append, min or max, or a call whose every
// callee the analyzed program declares with a body.
func (w *usesWalk) result(call *ast.CallExpr) (string, bool) {
	if fun := w.info.Types[call.Fun]; fun.IsType() {
		if len(call.Args) == 1 && types.Identical(w.info.TypeOf(call.Args[0]), fun.Type) {
			return "", true
		}
		if len(call.Args) == 1 {
			return "is converted from " + types.TypeString(w.info.TypeOf(call.Args[0]), nil), false
		}
		return usedDetail, false
	}
	if b, isBuiltin := w.builtin(call); isBuiltin {
		switch b.Name() {
		case "make", "new", builtinAppend, builtinMin, builtinMax:
			return "", true
		}
		return "is returned by " + b.Name(), false
	}
	if outside, inside := w.callsInside(call); !inside {
		return "is returned by " + outside, false
	}
	return "", true
}

// callsInside reports whether every callee of one call is a function the analyzed
// program declares with a body, and otherwise names one that is not.
func (w *usesWalk) callsInside(call *ast.CallExpr) (string, bool) {
	callees, dynamic := w.s.in.callees(w.info, call)
	if dynamic || len(callees) == 0 {
		return types.ExprString(call.Fun), false
	}
	for _, callee := range callees {
		if !w.s.in.declaresBody(callee.Fn) {
			return callee.Fn.FullName(), false
		}
	}
	return "", true
}

// builtin is the builtin one call calls, and false for any other call.
func (w *usesWalk) builtin(call *ast.CallExpr) (*types.Builtin, bool) {
	b, isBuiltin := w.info.Uses[calledIdent(call.Fun)].(*types.Builtin)
	return b, isBuiltin
}

// used reports whether the way one expression is used is on the list, and the
// clause where it is not.
func (w *usesWalk) used(e ast.Expr) (string, bool) {
	e, at := w.unparen(e)
	if at < 0 {
		return usedDetail, false
	}
	switch p := w.stack[at].(type) {
	case *ast.BinaryExpr:
		return w.compared(p)
	case *ast.UnaryExpr:
		if p.Op == token.AND || p.Op == token.ARROW {
			return "", true
		}
		return operatorDetail(p.Op), false
	case *ast.StarExpr:
		return "", true
	case *ast.SelectorExpr:
		return w.selected(p)
	case *ast.IndexExpr:
		return w.indexed(p, e)
	case *ast.SliceExpr:
		if p.X == e {
			return "", true
		}
		return indexDetail, false
	case *ast.CallExpr:
		return w.argument(p, e)
	case *ast.CompositeLit:
		return w.element(p, e)
	case *ast.KeyValueExpr:
		return w.keyed(p, e, at)
	}
	return w.statement(w.stack[at], e)
}

// unparen is the outermost parenthesized form of an expression the walk's stack
// holds, and the place on the stack of the node that uses it, -1 where none does.
func (w *usesWalk) unparen(e ast.Expr) (outer ast.Expr, at int) {
	at = len(w.stack) - 1
	for at >= 0 {
		paren, isParen := w.stack[at].(*ast.ParenExpr)
		if !isParen {
			break
		}
		e, at = paren, at-1
	}
	return e, at
}

// compared reports whether an operand of a binary operator is on the list: a
// comparison against a value of the same type or the untyped nil.
func (w *usesWalk) compared(p *ast.BinaryExpr) (string, bool) {
	if !comparison(p.Op) {
		return operatorDetail(p.Op), false
	}
	x, y := w.info.TypeOf(p.X), w.info.TypeOf(p.Y)
	if types.Identical(x, y) || untypedNil(x) || untypedNil(y) {
		return "", true
	}
	return "is compared with a value of another type", false
}

// untypedNil reports whether t is the type of the untyped nil.
func untypedNil(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind() == types.UntypedNil
}

// statement reports whether a statement's use of an expression is on the list.
func (w *usesWalk) statement(parent ast.Node, e ast.Expr) (string, bool) {
	switch p := parent.(type) {
	case *ast.ExprStmt, *ast.GoStmt, *ast.DeferStmt, *ast.SwitchStmt, *ast.CaseClause:
		return "", true
	case *ast.AssignStmt:
		return w.assigned(p, e)
	case *ast.ValueSpec:
		return w.declared(p, e)
	case *ast.ReturnStmt:
		return w.returned(p, e)
	case *ast.RangeStmt:
		return w.ranged(p, e)
	case *ast.SendStmt:
		if p.Chan == e {
			return "", true
		}
		if ch, isChan := typeUnder(w.info.TypeOf(p.Chan)).(*types.Chan); isChan {
			return sameTypeInto(ch.Elem(), w.info.TypeOf(e))
		}
	case *ast.IncDecStmt:
		return operatorDetail(p.Tok), false
	case *ast.ArrayType:
		return "is used as an array length", false
	}
	return usedDetail, false
}

// selected reports whether a value selected from is on the list: a field, a
// method the analyzed program declares with a body, and a method promoted from an
// embedded field whose type holds no enumerated type.
func (w *usesWalk) selected(p *ast.SelectorExpr) (string, bool) {
	sel := w.info.Selections[p]
	if sel == nil || sel.Kind() == types.FieldVal {
		return "", true
	}
	fn, isFunc := sel.Obj().(*types.Func)
	if !isFunc {
		return usedDetail, false
	}
	if w.s.in.declaresBody(fn) {
		return "", true
	}
	if len(sel.Index()) > 1 && w.s.holds.of(fn.Signature().Recv().Type()).empty() {
		return "", true
	}
	return "is passed to " + fn.FullName(), false
}

// indexed reports whether a value an index expression reads is on the list: the
// indexed operand, and a key of a map whose key type is its own.
func (w *usesWalk) indexed(p *ast.IndexExpr, e ast.Expr) (string, bool) {
	if p.X == e {
		return "", true
	}
	if m, isMap := typeUnder(w.info.TypeOf(p.X)).(*types.Map); isMap && types.Identical(m.Key(), w.info.TypeOf(e)) {
		return "", true
	}
	return indexDetail, false
}

// argument reports whether one call's use of an expression is on the list: the
// function it calls, a conversion to its own type, an argument of a builtin the
// list names, and an argument of identical type to a call whose every callee the
// analyzed program declares with a body.
func (w *usesWalk) argument(call *ast.CallExpr, e ast.Expr) (string, bool) {
	if call.Fun == e {
		return "", true
	}
	t := w.info.TypeOf(e)
	if fun := w.info.Types[call.Fun]; fun.IsType() {
		if types.Identical(fun.Type, t) {
			return "", true
		}
		return "is converted to " + types.TypeString(fun.Type, nil), false
	}
	arg := slices.Index(call.Args, e)
	if b, isBuiltin := w.builtin(call); isBuiltin {
		return w.builtinArgument(call, b, arg, t)
	}
	if outside, inside := w.callsInside(call); !inside {
		return "is passed to " + outside, false
	}
	sig, _ := typeUnder(w.info.TypeOf(call.Fun)).(*types.Signature)
	if results, isTuple := t.(*types.Tuple); isTuple && len(call.Args) == 1 && sig != nil {
		for i := range results.Len() {
			if detail, follows := sameTypeInto(argumentType(sig, call, i), results.At(i).Type()); !follows {
				return detail, false
			}
		}
		return "", true
	}
	return sameTypeInto(argumentType(sig, call, arg), t)
}

// builtinArgument reports whether an argument of a builtin is on the list.
func (w *usesWalk) builtinArgument(call *ast.CallExpr, b *types.Builtin, arg int, t types.Type) (string, bool) {
	result := w.info.TypeOf(call)
	switch b.Name() {
	case "len", "cap", "clear", "copy":
		return "", true
	case builtinMin, builtinMax:
		return sameTypeInto(result, t)
	case "delete":
		if arg == 0 {
			return "", true
		}
		if m, isMap := typeUnder(w.info.TypeOf(call.Args[0])).(*types.Map); isMap {
			return sameTypeInto(m.Key(), t)
		}
	case builtinAppend:
		if arg == 0 || call.Ellipsis.IsValid() {
			return sameTypeInto(result, t)
		}
		if list, isSlice := typeUnder(result).(*types.Slice); isSlice {
			return sameTypeInto(list.Elem(), t)
		}
	}
	return "is passed to " + b.Name(), false
}

// sameTypeInto reports whether a value of type from put into a place of type to is
// on the list: the two are identical.
func sameTypeInto(to, from types.Type) (string, bool) {
	switch {
	case to == nil:
		return usedDetail, false
	case types.Identical(to, from):
		return "", true
	case types.IsInterface(to):
		return "is put into an interface value", false
	}
	return "is converted to " + types.TypeString(to, nil), false
}

// assigned reports whether an assignment's use of one side is on the list.
func (w *usesWalk) assigned(p *ast.AssignStmt, e ast.Expr) (string, bool) {
	if p.Tok != token.ASSIGN && p.Tok != token.DEFINE {
		return operatorDetail(p.Tok), false
	}
	if at := slices.Index(p.Lhs, e); at >= 0 {
		return w.pair(w.info.TypeOf(e), p.Rhs, at, len(p.Lhs))
	}
	at := slices.Index(p.Rhs, e)
	lhs := make([]types.Type, len(p.Lhs))
	for i, dst := range p.Lhs {
		if id, blank := dst.(*ast.Ident); !blank || id.Name != "_" {
			lhs[i] = w.info.TypeOf(dst)
		}
	}
	return w.into(lhs, e, at, len(p.Rhs))
}

// pair reports whether the left side at position at of an assignment receives a
// value of its own type from rhs, the several results of one call among them.
func (w *usesWalk) pair(to types.Type, rhs []ast.Expr, at, lhs int) (string, bool) {
	if len(rhs) == 1 && lhs > 1 {
		results, isTuple := w.info.TypeOf(rhs[0]).(*types.Tuple)
		if !isTuple {
			if at == 0 {
				return sameTypeInto(to, w.info.TypeOf(rhs[0]))
			}
			return "", true // the second value of a comma-ok form is a bool
		}
		if at >= results.Len() {
			return usedDetail, false
		}
		return sameTypeInto(to, results.At(at).Type())
	}
	if at < len(rhs) {
		return sameTypeInto(to, w.info.TypeOf(rhs[at]))
	}
	return usedDetail, false
}

// into reports whether the value at position at of a right side goes into a
// destination of its own type, a call's several results each into its own.
func (w *usesWalk) into(to []types.Type, e ast.Expr, at, rhs int) (string, bool) {
	t := w.info.TypeOf(e)
	if results, isTuple := t.(*types.Tuple); isTuple && rhs == 1 {
		return resultsInto(to, results)
	}
	if rhs == 1 && len(to) == 2 && at == 0 {
		// A comma-ok form: the value goes into the first destination.
		if to[0] == nil {
			return "", true
		}
		return sameTypeInto(to[0], t)
	}
	if at < 0 || at >= len(to) {
		return usedDetail, false
	}
	if to[at] == nil {
		return "", true // the blank identifier discards the value
	}
	return sameTypeInto(to[at], t)
}

// resultsInto reports whether each of a call's results goes into a destination of
// its own type, or the blank identifier.
func resultsInto(to []types.Type, results *types.Tuple) (string, bool) {
	for i := range min(len(to), results.Len()) {
		if to[i] == nil {
			continue
		}
		if detail, follows := sameTypeInto(to[i], results.At(i).Type()); !follows {
			return detail, false
		}
	}
	return "", true
}

// declared reports whether a variable declaration's value goes into a variable of
// its own type.
func (w *usesWalk) declared(spec *ast.ValueSpec, e ast.Expr) (string, bool) {
	to := make([]types.Type, len(spec.Names))
	for i, name := range spec.Names {
		if name.Name != "_" {
			to[i] = w.info.TypeOf(name)
		}
	}
	return w.into(to, e, slices.Index(spec.Values, e), len(spec.Values))
}

// returned reports whether a returned value goes into a result of its own type.
func (w *usesWalk) returned(ret *ast.ReturnStmt, e ast.Expr) (string, bool) {
	if len(w.funcs) == 0 || w.funcs[len(w.funcs)-1] == nil {
		return usedDetail, false
	}
	results := w.funcs[len(w.funcs)-1].Results()
	to := make([]types.Type, results.Len())
	for i := range to {
		to[i] = results.At(i).Type()
	}
	return w.into(to, e, slices.Index(ret.Results, e), len(ret.Results))
}

// element reports whether a positional element of a composite literal goes into a
// slot of its own type.
func (w *usesWalk) element(lit *ast.CompositeLit, e ast.Expr) (string, bool) {
	slot, _ := literalSlot(typeUnder(w.info.TypeOf(lit)), nil, slices.Index(lit.Elts, e))
	return sameTypeInto(slot, w.info.TypeOf(e))
}

// keyed reports whether the key or the value of a keyed element of a composite
// literal goes into a slot of its own type, a map's key among them.
func (w *usesWalk) keyed(kv *ast.KeyValueExpr, e ast.Expr, at int) (string, bool) {
	if at < 1 {
		return usedDetail, false
	}
	lit, isLit := w.stack[at-1].(*ast.CompositeLit)
	if !isLit {
		return usedDetail, false
	}
	slot, key := literalSlot(typeUnder(w.info.TypeOf(lit)), kv.Key, 0)
	if kv.Key == e {
		if key == nil {
			return indexDetail, false
		}
		return sameTypeInto(key, w.info.TypeOf(e))
	}
	return sameTypeInto(slot, w.info.TypeOf(e))
}

// literalSlot is the type of the slot one element of a composite literal of
// underlying type u fills, the field its key names or the one at its position,
// and the key type of a map literal.
func literalSlot(u types.Type, key ast.Expr, at int) (slot, mapKey types.Type) {
	if p, isPointer := u.(*types.Pointer); isPointer {
		u = typeUnder(p.Elem())
	}
	switch u := u.(type) {
	case *types.Struct:
		return structField(u, key, at), nil
	case *types.Slice:
		return u.Elem(), nil
	case *types.Array:
		return u.Elem(), nil
	case *types.Map:
		return u.Elem(), u.Key()
	}
	return nil, nil
}

// structField is the type of the field one element of a struct literal sets: the
// field its key names, or the one at its position.
func structField(u *types.Struct, key ast.Expr, at int) types.Type {
	if id, named := key.(*ast.Ident); named {
		for field := range u.Fields() {
			if field.Name() == id.Name {
				return field.Type()
			}
		}
		return nil
	}
	if key == nil && at >= 0 && at < u.NumFields() {
		return u.Field(at).Type()
	}
	return nil
}

// ranged reports whether a range statement's use of an expression is on the list:
// a range over a slice, an array, a pointer to an array, a map or a channel, and a
// key or value of the type the range gives it.
func (w *usesWalk) ranged(r *ast.RangeStmt, e ast.Expr) (string, bool) {
	over := w.info.TypeOf(r.X)
	if r.X == e {
		switch typeUnder(over).(type) {
		case *types.Slice, *types.Array, *types.Map, *types.Chan:
			return "", true
		case *types.Pointer:
			if _, isArray := typeUnder(typeUnder(over).(*types.Pointer).Elem()).(*types.Array); isArray {
				return "", true
			}
		}
		return "is ranged over", false
	}
	key, value := rangeTypes(over)
	if r.Key == e {
		return sameTypeInto(w.info.TypeOf(e), key)
	}
	return sameTypeInto(w.info.TypeOf(e), value)
}

// rangeTypes are the types of the key and the value a range over a value of type t
// assigns, nil where it assigns none the list follows.
func rangeTypes(t types.Type) (key, value types.Type) {
	switch u := typeUnder(t).(type) {
	case *types.Pointer:
		if array, isArray := typeUnder(u.Elem()).(*types.Array); isArray {
			return types.Typ[types.Int], array.Elem()
		}
	case *types.Slice:
		return types.Typ[types.Int], u.Elem()
	case *types.Array:
		return types.Typ[types.Int], u.Elem()
	case *types.Map:
		return u.Key(), u.Elem()
	case *types.Chan:
		return u.Elem(), nil
	}
	return nil, nil
}

// argumentType is the type the argument at position arg of a call is passed as, an
// element of the variadic parameter where the call does not spread a slice into it,
// and nil where the signature accounts for no such argument.
func argumentType(sig *types.Signature, call *ast.CallExpr, arg int) types.Type {
	if sig == nil || arg < 0 {
		return nil
	}
	at, supplies := parameterAt(sig, arg)
	if !supplies {
		return nil
	}
	t := sig.Params().At(at).Type()
	if sig.Variadic() && at == sig.Params().Len()-1 && !call.Ellipsis.IsValid() {
		if list, isSlice := t.Underlying().(*types.Slice); isSlice {
			return list.Elem()
		}
	}
	return t
}

// comparison reports whether an operator compares its operands.
func comparison(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

// operatorDetail is the clause of a value an operator computes or consumes.
func operatorDetail(op token.Token) string {
	return "is an operand of the " + op.String() + " operator"
}

// typeUnder is the underlying type of t, and nil for no type.
func typeUnder(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}
