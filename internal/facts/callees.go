package facts

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"golang.org/x/tools/go/packages"
)

// Callee is one function a call reaches, and how the call's arguments bind to it:
// argument i supplies parameter i-Shift of Fn's signature, so a method expression's
// first argument, its receiver, supplies none.
type Callee struct {
	Fn *types.Func
	// Recv is the receiver a method value binds, the operand of its selector, which
	// lies in the body the selector is written in; nil for a function and a method
	// expression, whose receiver is the call's first argument.
	Recv  ast.Expr
	Shift int
}

// Callees answers which functions a call reaches, through a function value as
// directly: the program's every write of a function-typed variable or field, or of
// an element of a map, slice or array of functions, read once per configuration. A
// place is keyed by where it is declared, so every reading of one file writes one
// place. A write or an escape a test file makes reaches the calls of test files
// alone: a call of a production file is the site its exemption records, so a test
// file's write seen there would hold as production evidence.
type Callees struct {
	pos      *inventory.Positions
	program  map[string]bool
	infos    map[*types.Info]bool // the program's, whose positions the places are keyed in
	declared map[place]bool
	open     map[scoped]bool // a write the analysis does not follow reaches the place
	writes   map[place][]written
	solved   map[scoped]solvedPlace
}

// scoped is one place as the calls of test files, or of production files, see it.
type scoped struct {
	at   place
	test bool
}

// place is one function-typed variable or field, or the elements of a container of
// functions one variable or field holds.
type place struct {
	at       inventory.Offset
	elements bool
}

// written is one value a write puts in a place: a function, or what another place
// holds.
type written struct {
	callee Callee
	from   place
	linked bool
	test   bool // a test file writes it
}

type solvedPlace struct {
	callees []Callee
	dynamic bool
}

// calleesOf reads every write of pkgs.
func calleesOf(pkgs []*packages.Package, pos *inventory.Positions) *Callees {
	c := &Callees{
		pos:      pos,
		program:  make(map[string]bool),
		infos:    make(map[*types.Info]bool),
		declared: make(map[place]bool),
		open:     make(map[scoped]bool),
		writes:   make(map[place][]written),
		solved:   make(map[scoped]solvedPlace),
	}
	for _, p := range pkgs {
		if p.Types != nil {
			c.program[p.PkgPath] = true
		}
		c.infos[p.TypesInfo] = true
	}
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		w := &calleeWalk{c: c, info: p.TypesInfo, published: load.Published(p.PkgPath, p.Name == mainPackage)}
		for _, f := range p.Syntax {
			w.test = pos.InTestFile(f.Pos())
			ast.PreorderStack(f, nil, w.visit)
		}
	}
	return c
}

// Of is every function one call reaches, and whether it may also reach a function
// the analysis does not know: a call through a parameter, a call result, a function
// literal, or a place code outside the analysed program may write. A call of a
// builtin or a conversion reaches none. A call in a package read apart from the
// program reaches a function value the analysis does not know.
func (c *Callees) Of(info *types.Info, call *ast.CallExpr) (found []Callee, dynamic bool) {
	fun := ast.Unparen(call.Fun)
	if tv, typed := info.Types[fun]; typed && tv.IsType() {
		return nil, false
	}
	if _, isBuiltin := info.Uses[calledIdent(fun)].(*types.Builtin); isBuiltin {
		return nil, false
	}
	w := &calleeWalk{c: c, info: info}
	test := c.pos.InTestFile(call.Pos())
	for _, v := range w.values(fun) {
		if v.linked && !c.infos[info] {
			dynamic = true
			continue
		}
		if !v.linked {
			found = c.appendCallee(found, v.callee)
			continue
		}
		held := c.solve(scoped{at: v.from, test: test})
		for _, callee := range held.callees {
			found = c.appendCallee(found, callee)
		}
		dynamic = dynamic || held.dynamic
	}
	return found, dynamic
}

// solve is what one place holds, through every place it holds the value of, as the
// calls of test files or of production files see it.
func (c *Callees) solve(root scoped) solvedPlace {
	if held, known := c.solved[root]; known {
		return held
	}
	var held solvedPlace
	queue := []place{root.at}
	seen := map[place]bool{root.at: true}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if !c.declared[at] || c.open[scoped{at: at}] || root.test && c.open[scoped{at: at, test: true}] {
			held.dynamic = true
		}
		for _, v := range c.writes[at] {
			switch {
			case v.test && !root.test:
			case !v.linked:
				held.callees = c.appendCallee(held.callees, v.callee)
			case !seen[v.from]:
				seen[v.from] = true
				queue = append(queue, v.from)
			}
		}
	}
	c.solved[root] = held
	return held
}

// appendCallee adds one callee once, whichever reading of its declaration it is;
// two method values of one method binding different receivers are two callees.
func (c *Callees) appendCallee(found []Callee, callee Callee) []Callee {
	at := c.pos.Offset(callee.Fn.Pos())
	for _, held := range found {
		if held.Shift == callee.Shift && held.Recv == callee.Recv && held.Fn.FullName() == callee.Fn.FullName() && c.pos.Offset(held.Fn.Pos()) == at {
			return found
		}
	}
	return append(found, callee)
}

// calleeWalk reads the writes of one package variant.
type calleeWalk struct {
	c         *Callees
	info      *types.Info
	published bool
	test      bool // the file walked is a test file
}

func (w *calleeWalk) visit(n ast.Node, stack []ast.Node) bool {
	switch n := n.(type) {
	case *ast.ValueSpec:
		w.valueSpec(n)
	case *ast.AssignStmt:
		w.assignStmt(n)
	case *ast.RangeStmt:
		w.rangeStmt(n)
	case *ast.CompositeLit:
		w.structLiteral(n)
	case *ast.Field:
		w.field(n, stack)
	case *ast.UnaryExpr:
		if n.Op == token.AND {
			w.escape(n.X)
		}
	case *ast.CallExpr:
		w.conversion(n)
	case *ast.Ident:
		w.use(n, stack)
	case *ast.SelectorExpr:
		w.use(n, stack)
	}
	return true
}

// placeOf is the place an expression names, a variable or a field, and its object.
func (w *calleeWalk) placeOf(expr ast.Expr) (place, *types.Var, bool) {
	var object types.Object
	switch e := ast.Unparen(expr).(type) {
	case *ast.Ident:
		object = w.info.ObjectOf(e)
	case *ast.SelectorExpr:
		object = w.info.ObjectOf(e.Sel)
	}
	v, isVar := object.(*types.Var)
	if !isVar {
		return place{}, nil, false
	}
	v = v.Origin()
	return place{at: w.c.pos.Offset(v.Pos())}, v, true
}

// declare records a place whose every write the walk reads, unless code outside
// the analysed program may write it: a variable of a package outside the program, or
// an exported package variable or field a non-test file of a published package
// declares.
func (w *calleeWalk) declare(at place, v *types.Var) {
	w.c.declared[at] = true
	if w.writableOutside(v) {
		w.c.open[scoped{at: at}] = true
	}
}

func (w *calleeWalk) writableOutside(v *types.Var) bool {
	if v.Pkg() == nil || !w.c.program[v.Pkg().Path()] {
		return true
	}
	packageLevel := v.Parent() == v.Pkg().Scope()
	return w.published && v.Exported() && (packageLevel || v.IsField()) && !w.c.pos.InTestFile(v.Pos())
}

// valueSpec reads one declaration of variables, each name with its own value.
func (w *calleeWalk) valueSpec(spec *ast.ValueSpec) {
	for i, name := range spec.Names {
		at, v, isPlace := w.placeOf(name)
		if !isPlace || !holdsFunctions(v.Type()) {
			continue
		}
		w.declare(at, v)
		w.declare(elementsOf(at), v)
		switch {
		case len(spec.Values) == len(spec.Names):
			w.assign(at, v.Type(), spec.Values[i])
		case len(spec.Values) > 0:
			w.openPlace(at)
		}
	}
}

// assignStmt reads one assignment: each left side a place or an element of a
// container place, given its right side's value, and a tuple assignment's left
// sides from what the analysis does not follow, a comma-ok index excepted.
func (w *calleeWalk) assignStmt(n *ast.AssignStmt) {
	if n.Tok != token.ASSIGN && n.Tok != token.DEFINE {
		return
	}
	for i, lhs := range n.Lhs {
		at, v, isPlace := w.placeOf(lhs)
		if n.Tok == token.DEFINE && isPlace {
			w.defined(lhs, at, v)
		}
		rhs := assignedValue(n, i)
		if index, indexed := ast.Unparen(lhs).(*ast.IndexExpr); indexed {
			if container, _, holds := w.placeOf(index.X); holds {
				w.write(elementsOf(container), rhs)
			}
			continue
		}
		if isPlace && holdsFunctions(v.Type()) {
			w.assignOrOpen(at, v.Type(), rhs)
		}
	}
}

// assignOrOpen records rhs written into a place of type t, or leaves the place to
// what the analysis does not follow where the value is not one it follows.
func (w *calleeWalk) assignOrOpen(at place, t types.Type, rhs ast.Expr) {
	if rhs == nil {
		w.openPlace(at)
		return
	}
	w.assign(at, t, rhs)
}

// defined declares a place a short variable declaration defines.
func (w *calleeWalk) defined(lhs ast.Expr, at place, v *types.Var) {
	if id, isIdent := lhs.(*ast.Ident); isIdent && holdsFunctions(v.Type()) && w.info.Defs[id] != nil {
		w.declare(at, v)
		w.declare(elementsOf(at), v)
	}
}

// assignedValue is the value an assignment gives its i-th left side: the matching
// right side, a comma-ok index for the first, or nil for a value the analysis does
// not follow.
func assignedValue(n *ast.AssignStmt, i int) ast.Expr {
	switch {
	case len(n.Rhs) == len(n.Lhs):
		return n.Rhs[i]
	case i == 0 && len(n.Rhs) == 1:
		if index, indexed := ast.Unparen(n.Rhs[0]).(*ast.IndexExpr); indexed {
			return index
		}
	}
	return nil
}

// rangeStmt reads a range over a container place, whose value is each element.
func (w *calleeWalk) rangeStmt(n *ast.RangeStmt) {
	for i, target := range []ast.Expr{n.Key, n.Value} {
		if target == nil {
			continue
		}
		at, v, isPlace := w.placeOf(target)
		if !isPlace || !holdsFunctions(v.Type()) {
			continue
		}
		if n.Tok == token.DEFINE {
			w.declare(at, v)
			w.declare(elementsOf(at), v)
		}
		if container, _, holds := w.placeOf(n.X); i == 1 && holds && holdsFunctionElements(w.info.TypeOf(n.X)) {
			w.link(at, elementsOf(container))
			continue
		}
		w.openPlace(at)
	}
}

// structLiteral reads the fields one struct literal sets.
func (w *calleeWalk) structLiteral(lit *ast.CompositeLit) {
	st, isStruct := w.literalStruct(lit)
	if !isStruct {
		return
	}
	for i, elt := range lit.Elts {
		var field *types.Var
		value := elt
		if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
			if id, named := kv.Key.(*ast.Ident); named {
				field, _ = w.info.ObjectOf(id).(*types.Var)
			}
			value = kv.Value
		} else if i < st.NumFields() {
			field = st.Field(i)
		}
		if field != nil && holdsFunctions(field.Type()) {
			field = field.Origin()
			w.assign(place{at: w.c.pos.Offset(field.Pos())}, field.Type(), value)
		}
	}
}

// field declares each function-typed field of a struct type, and leaves a
// parameter or a result to what its callers write.
func (w *calleeWalk) field(n *ast.Field, stack []ast.Node) {
	if len(stack) < 2 {
		return
	}
	_, inStruct := stack[len(stack)-2].(*ast.StructType)
	for _, name := range n.Names {
		at, v, isPlace := w.placeOf(name)
		if !isPlace || !holdsFunctions(v.Type()) {
			continue
		}
		w.declare(at, v)
		w.declare(elementsOf(at), v)
		if !inStruct {
			w.openPlace(at)
		}
	}
}

// escape leaves a place whose address is taken to what the analysis does not
// follow, since a write through the pointer names no place.
func (w *calleeWalk) escape(expr ast.Expr) {
	if index, indexed := ast.Unparen(expr).(*ast.IndexExpr); indexed {
		if container, v, holds := w.placeOf(index.X); holds && holdsFunctionElements(v.Type()) {
			w.openPlace(elementsOf(container))
		}
		return
	}
	if at, v, isPlace := w.placeOf(expr); isPlace && holdsFunctions(v.Type()) {
		w.openPlace(at)
	}
}

// use leaves the elements of a container place to what the analysis does not
// follow wherever the container goes anywhere but an index, a range, a builtin that
// reads it or a write the walk reads, because the code it reaches may write an
// element through it.
func (w *calleeWalk) use(n ast.Expr, stack []ast.Node) {
	if len(stack) == 0 {
		return
	}
	if sel, selects := stack[len(stack)-1].(*ast.SelectorExpr); selects && sel.Sel == n {
		return
	}
	at, v, isPlace := w.placeOf(n)
	if !isPlace || !holdsFunctionElements(v.Type()) {
		return
	}
	expr, parents := n, stack
	for len(parents) > 1 {
		paren, wrapped := parents[len(parents)-1].(*ast.ParenExpr)
		if !wrapped {
			break
		}
		expr, parents = ast.Expr(paren), parents[:len(parents)-1]
	}
	if !w.keepsInPlace(parents, expr) {
		w.openPlace(elementsOf(at))
	}
}

// keepsInPlace reports whether the node above a container's expression keeps the
// container's elements where the walk reads every write of them: an index of it, a
// range over it, a builtin other than copy, the left side of an assignment, or the
// value of a declaration, an assignment or a struct literal's field that holds
// functions, which the walk links to it.
func (w *calleeWalk) keepsInPlace(parents []ast.Node, expr ast.Expr) bool {
	switch p := parents[len(parents)-1].(type) {
	case *ast.IndexExpr:
		return p.X == expr
	case *ast.RangeStmt:
		return p.X == expr
	case *ast.CallExpr:
		builtin, isBuiltin := w.info.Uses[calledIdent(p.Fun)].(*types.Builtin)
		return isBuiltin && builtin.Name() != "copy"
	case *ast.AssignStmt:
		return slices.Contains(p.Lhs, expr) || w.linkedValue(p.Lhs, p.Rhs, expr)
	case *ast.ValueSpec:
		names := identExprs(p.Names)
		return slices.Contains(names, expr) || w.linkedValue(names, p.Values, expr)
	case *ast.KeyValueExpr:
		return p.Key == expr || p.Value == expr && len(parents) > 1 && w.isStructLiteral(parents[len(parents)-2]) && holdsFunctions(w.info.TypeOf(p.Key))
	case *ast.CompositeLit:
		return w.positionalFunctionField(p, expr)
	}
	return false
}

// positionalFunctionField reports whether expr is an unkeyed element of a struct
// literal whose field holds functions.
func (w *calleeWalk) positionalFunctionField(lit *ast.CompositeLit, expr ast.Expr) bool {
	st, isStruct := w.literalStruct(lit)
	i := slices.Index(lit.Elts, expr)
	return isStruct && i >= 0 && i < st.NumFields() && holdsFunctions(st.Field(i).Type())
}

// linkedValue reports whether expr is one of values given, one for one, to a left
// side that holds functions.
func (w *calleeWalk) linkedValue(lhs, values []ast.Expr, expr ast.Expr) bool {
	i := slices.Index(values, expr)
	return i >= 0 && len(lhs) == len(values) && holdsFunctions(w.info.TypeOf(lhs[i]))
}

func identExprs(ids []*ast.Ident) []ast.Expr {
	exprs := make([]ast.Expr, len(ids))
	for i, id := range ids {
		exprs[i] = id
	}
	return exprs
}

func (w *calleeWalk) isStructLiteral(n ast.Node) bool {
	lit, isLiteral := n.(*ast.CompositeLit)
	if !isLiteral {
		return false
	}
	_, isStruct := w.literalStruct(lit)
	return isStruct
}

// literalStruct is the struct type one composite literal builds.
func (w *calleeWalk) literalStruct(lit *ast.CompositeLit) (*types.Struct, bool) {
	t := w.info.TypeOf(lit)
	if t == nil {
		return nil, false
	}
	st, isStruct := types.Unalias(t).Underlying().(*types.Struct)
	return st, isStruct
}

func calledIdent(fun ast.Expr) *ast.Ident {
	id, _ := ast.Unparen(fun).(*ast.Ident)
	return id
}

// conversion links each field of a struct type a conversion produces to the field
// of the converted value's type it copies.
func (w *calleeWalk) conversion(call *ast.CallExpr) {
	tv, converts := w.info.Types[call.Fun]
	if !converts || !tv.IsType() || len(call.Args) != 1 {
		return
	}
	converted := w.info.TypeOf(call.Args[0])
	if converted == nil {
		return
	}
	to, isStruct := types.Unalias(tv.Type).Underlying().(*types.Struct)
	from, fromStruct := types.Unalias(converted).Underlying().(*types.Struct)
	if !isStruct || !fromStruct || to.NumFields() != from.NumFields() {
		return
	}
	for i := range to.NumFields() {
		if holdsFunctions(to.Field(i).Type()) {
			into := place{at: w.c.pos.Offset(to.Field(i).Origin().Pos())}
			out := place{at: w.c.pos.Offset(from.Field(i).Origin().Pos())}
			w.link(into, out)
			w.link(elementsOf(into), elementsOf(out))
		}
	}
}

// assign records one value written into a place of type t: a function value, or
// for a container of functions the elements a literal, an append or another
// container gives it.
func (w *calleeWalk) assign(at place, t types.Type, value ast.Expr) {
	if isFuncType(t) {
		w.write(at, value)
		return
	}
	switch e := ast.Unparen(value).(type) {
	case *ast.CompositeLit:
		for _, elt := range e.Elts {
			if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
				elt = kv.Value
			}
			w.write(elementsOf(at), elt)
		}
	case *ast.CallExpr:
		w.containerCall(at, e)
	default:
		if from, _, isPlace := w.placeOf(e); isPlace {
			w.link(elementsOf(at), elementsOf(from))
			w.link(elementsOf(from), elementsOf(at))
			return
		}
		if id, isIdent := e.(*ast.Ident); !isIdent || !isNil(w.info, id) {
			w.openPlace(elementsOf(at))
		}
	}
}

// containerCall records the elements a call gives a container place: a make adds
// none, an append the ones it appends to what it extends, and any other call the
// ones the analysis does not follow.
func (w *calleeWalk) containerCall(at place, call *ast.CallExpr) {
	builtin, isBuiltin := w.info.Uses[calledIdent(call.Fun)].(*types.Builtin)
	switch {
	case isBuiltin && builtin.Name() == "make":
	case isBuiltin && builtin.Name() == "append" && len(call.Args) > 0:
		w.assign(at, w.info.TypeOf(call.Args[0]), call.Args[0])
		for i, arg := range call.Args[1:] {
			if call.Ellipsis.IsValid() && i == len(call.Args)-2 {
				w.assign(at, w.info.TypeOf(arg), arg)
				continue
			}
			w.write(elementsOf(at), arg)
		}
	default:
		w.openPlace(elementsOf(at))
	}
}

// write records the values one expression gives a function place.
func (w *calleeWalk) write(at place, value ast.Expr) {
	if value == nil {
		w.openPlace(at)
		return
	}
	for _, v := range w.values(value) {
		v.test = w.test
		w.c.writes[at] = append(w.c.writes[at], v)
	}
}

func (w *calleeWalk) link(at, from place) {
	w.c.writes[at] = append(w.c.writes[at], written{from: from, linked: true, test: w.test})
}

func (w *calleeWalk) openPlace(at place) { w.c.open[scoped{at: at, test: w.test}] = true }

// unknownPlace is held by no write and declared by none, so a value read from it is
// one the analysis does not follow.
var unknownPlace = place{at: inventory.Offset{File: "<unknown>"}}

// values is what one function-typed expression may hold: a function or method it
// names, a method value or expression among them, what a place it names or indexes
// holds, and otherwise a value the analysis does not follow.
func (w *calleeWalk) values(expr ast.Expr) []written {
	switch e := ast.Unparen(expr).(type) {
	case *ast.Ident:
		if isNil(w.info, e) {
			return nil
		}
		return w.named(e, w.info.Uses[e])
	case *ast.SelectorExpr:
		return w.selected(e)
	case *ast.IndexExpr:
		if fn, isFunc := w.info.Uses[calledIdentOrSel(e.X)].(*types.Func); isFunc {
			return []written{{callee: Callee{Fn: fn}}}
		}
		if container, _, holds := w.placeOf(e.X); holds {
			return []written{{from: elementsOf(container), linked: true}}
		}
	case *ast.IndexListExpr:
		if fn, isFunc := w.info.Uses[calledIdentOrSel(e.X)].(*types.Func); isFunc {
			return []written{{callee: Callee{Fn: fn}}}
		}
	case *ast.CallExpr:
		if tv, converts := w.info.Types[e.Fun]; converts && tv.IsType() && len(e.Args) == 1 {
			return w.values(e.Args[0])
		}
	}
	return []written{{from: unknownPlace, linked: true}}
}

// selected is what a selector holds: the method or function it names, a method
// expression's receiver supplying no parameter, or what the place it names holds.
func (w *calleeWalk) selected(e *ast.SelectorExpr) []written {
	fn, isFunc := w.info.Uses[e.Sel].(*types.Func)
	if !isFunc {
		return w.named(e, w.info.Uses[e.Sel])
	}
	callee := Callee{Fn: fn}
	if sel, selected := w.info.Selections[e]; selected {
		switch sel.Kind() {
		case types.MethodExpr:
			callee.Shift = 1
		case types.MethodVal:
			callee.Recv = e.X
		}
	}
	return []written{{callee: callee}}
}

// named is what an identifier or a selector naming object holds.
func (w *calleeWalk) named(expr ast.Expr, object types.Object) []written {
	switch o := object.(type) {
	case *types.Func:
		return []written{{callee: Callee{Fn: o}}}
	case *types.Var:
		at, _, _ := w.placeOf(expr)
		return []written{{from: at, linked: true}}
	}
	return []written{{from: unknownPlace, linked: true}}
}

func calledIdentOrSel(expr ast.Expr) *ast.Ident {
	switch e := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return e
	case *ast.SelectorExpr:
		return e.Sel
	}
	return nil
}

func elementsOf(at place) place { return place{at: at.at, elements: true} }

func isNil(info *types.Info, id *ast.Ident) bool {
	_, isNil := info.Uses[id].(*types.Nil)
	return isNil
}

// isFuncType reports whether a value of t is a function.
func isFuncType(t types.Type) bool {
	if t == nil {
		return false
	}
	_, isSignature := types.Unalias(t).Underlying().(*types.Signature)
	return isSignature
}

// holdsFunctionElements reports whether t is a map, slice or array of functions.
func holdsFunctionElements(t types.Type) bool {
	if t == nil {
		return false
	}
	switch u := types.Unalias(t).Underlying().(type) {
	case *types.Map:
		return isFuncType(u.Elem())
	case *types.Slice:
		return isFuncType(u.Elem())
	case *types.Array:
		return isFuncType(u.Elem())
	}
	return false
}

// holdsFunctions reports whether a place of type t holds a function or functions.
func holdsFunctions(t types.Type) bool { return isFuncType(t) || holdsFunctionElements(t) }
