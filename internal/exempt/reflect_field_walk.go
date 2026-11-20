package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
)

// The names of the reflection package a field walk is read through.
const (
	reflectValueType = "Value"
	reflectValueOf   = "ValueOf"
	reflectIndirect  = "Indirect"
	reflectElem      = "Elem"
	reflectInterface = "Interface"
	// reflectTypeAssert is reflect.TypeAssert[T](v), which asserts T on what
	// v.Interface() returns exactly as v.Interface().(T) does (go1.27.4
	// src/reflect/value.go, TypeAssert).
	reflectTypeAssert = "TypeAssert"
	// reflectTypeType is reflect.Type, whose Elem, Key, Field, FieldByIndex, In
	// and Out the walk reads the type a value is built from through, and
	// reflectStructField the StructField whose Type field it reads.
	reflectTypeType     = "Type"
	reflectStructField  = "StructField"
	reflectTypeOf       = "TypeOf"
	reflectTypeFor      = "TypeFor"
	reflectField        = "Field"
	reflectFieldByIndex = "FieldByIndex"
	reflectNew          = "New"
)

// reflectValueConstructors are the functions of reflect that build a value of
// the reflect.Type their first argument is (Zero, MakeSlice, MakeMap,
// MakeMapWithSize, MakeChan, MakeFunc), true where the value is a pointer to one
// (New, NewAt).
var reflectValueConstructors = map[string]bool{
	"Zero": false, "MakeSlice": false, "MakeMap": false, "MakeMapWithSize": false,
	"MakeChan": false, "MakeFunc": false, reflectNew: true, "NewAt": true,
}

// fieldAssertion is one assertion to interface types of what Interface returns
// for a reflect.Value: what the walk followed the receiver back to, and where the
// assertion is written. followed is false where the walk could not follow the
// receiver to the operands handed to reflect.ValueOf, reflect.TypeOf or
// reflect.TypeFor.
type fieldAssertion struct {
	at       ast.Node
	asserted []assertedInterface
	held     []reflectHeld
	followed bool
	unseen   bool // a call through a function value the analysis does not follow, of Interface's shape
}

// assertedInterface is one interface an assertion names, with its spelling.
type assertedInterface struct {
	iface *types.Interface
	name  string
}

// reflectHeld is one value a reflect.Value expression may hold, or one type a
// reflect.Type or reflect.StructField expression may describe: the operand it was
// followed back to, and the static type of the value, or the type described, at
// the expression where every step between them is enumerated, nil where one is
// not. A value reflect.Zero or reflect.New builds is one of the type described.
type reflectHeld struct {
	at     types.Type
	origin reflectOrigin
	ro     readOnly
}

// readOnly is whether a reflect.Value was read from a field that is not exported,
// as reflect's flagEmbedRO and flagStickyRO record it: Interface panics on such a
// value, and a value read through it keeps the sticky mark (go1.27.3
// src/reflect/value.go, Field, Index, MapIndex and Elem).
type readOnly uint8

const (
	readWritable readOnly = iota
	readEmbedded          // read from an embedded field that is not exported
	readSticky            // read from, or through, a field that is neither
)

// reflectOrigin is one operand the program hands to reflect.ValueOf or
// reflect.TypeOf, with the body it is written in, so the types an interface-typed
// operand carries can be read once every store is, or one type argument of
// reflect.TypeFor, which is exact: the type itself, an interface one describing no
// value behind it.
type reflectOrigin struct {
	t     types.Type
	expr  ast.Expr
	info  *types.Info
	fn    *types.Func
	exact bool
}

// assertedValue reads one assertion of interface types on what Interface returns,
// reached as a direct call, a method value or a method expression of
// reflect.Value: it follows the receiver back to the operands handed to
// reflect.ValueOf ([reflectWalk.held]). A call through a function value the
// analysis does not follow that has Interface's signature is read as Interface on a
// receiver it cannot follow, where the program takes Interface as a value anywhere.
func assertedValue(walk *reflectWalk, x ast.Expr, asserted []assertedInterface, at ast.Node) (fieldAssertion, bool) {
	call, isCall := ast.Unparen(x).(*ast.CallExpr)
	if !isCall || len(asserted) == 0 {
		return fieldAssertion{}, false
	}
	callees, dynamic := walk.callees(walk.info, call)
	a := fieldAssertion{asserted: asserted, at: at, followed: true}
	reads := false
	for _, callee := range callees {
		if !isReflectValueMethod(callee.Fn, reflectInterface) {
			continue
		}
		reads = true
		recv, inBody := walk.receiver(call, callee)
		a.follow(walk, recv, inBody)
	}
	if dynamic && returnsInterfaceValue(walk.info.TypeOf(call.Fun)) {
		reads, a.unseen = true, true
	}
	return a, reads
}

// follow adds to a what the reflect.Value recv may hold, where the walk can read
// it in its body.
func (a *fieldAssertion) follow(walk *reflectWalk, recv ast.Expr, inBody bool) {
	if !inBody {
		a.followed = false
		return
	}
	held, followed := walk.held(recv, 0)
	a.held = append(a.held, held...)
	a.followed = a.followed && followed
}

// typeAssertCalled is the assertion a call of reflect.TypeAssert[T](v) makes, T
// an interface, read as v.Interface().(T) ([assertedValue]).
func typeAssertCalled(walk *reflectWalk, call *ast.CallExpr) (fieldAssertion, bool) {
	asserted, is := typeAssertOf(walk.info, call.Fun)
	if !is || len(asserted) == 0 || len(call.Args) != 1 {
		return fieldAssertion{}, false
	}
	a := fieldAssertion{asserted: asserted, at: call, followed: true}
	a.follow(walk, call.Args[0], true)
	return a, true
}

// typeAssertOf reports whether expr names an instantiation of reflect.TypeAssert,
// and the interface its type argument is, if it is one.
func typeAssertOf(info *types.Info, expr ast.Expr) ([]assertedInterface, bool) {
	t, is := reflectInstance(info, expr, reflectTypeAssert)
	if !is {
		return nil, false
	}
	if iface, isInterface := types.Unalias(t).Underlying().(*types.Interface); isInterface {
		return []assertedInterface{{iface: iface, name: types.TypeString(t, nil)}}, true
	}
	return nil, true
}

// reflectInstance is the one type argument of the instantiation of the generic
// function of reflect named name that expr names, if it names one.
func reflectInstance(info *types.Info, expr ast.Expr, name string) (types.Type, bool) {
	var id *ast.Ident
	switch e := ast.Unparen(expr).(type) {
	case *ast.IndexExpr:
		expr = e.X
	case *ast.IndexListExpr:
		expr = e.X
	}
	switch e := ast.Unparen(expr).(type) {
	case *ast.SelectorExpr:
		id = e.Sel
	case *ast.Ident:
		id = e
	default:
		return nil, false
	}
	fn, isFunc := info.Uses[id].(*types.Func)
	instance, instantiated := info.Instances[id]
	if !isFunc || !instantiated || !isReflectFunction(fn, name) || instance.TypeArgs.Len() != 1 {
		return nil, false
	}
	return instance.TypeArgs.At(0), true
}

// returnsInterfaceValue reports whether t is a function type of Interface's
// shape, as a method value (no parameter) or a method expression (a reflect.Value
// parameter), returning one value of an empty interface.
func returnsInterfaceValue(t types.Type) bool {
	sig, isFunc := types.Unalias(t).Underlying().(*types.Signature)
	if !isFunc || sig.Results().Len() != 1 || !isEmptyInterface(sig.Results().At(0).Type()) {
		return false
	}
	switch sig.Params().Len() {
	case 0:
		return true
	case 1:
		return isReflectValue(sig.Params().At(0).Type())
	}
	return false
}

// fieldAssertion records what one assertion on what Interface returns retains.
// A value the walk computes one static type for has the methods each asserted
// interface requires retained on that type. Any other value has every method of
// every type reachable from the operand it was followed back to retained, and an
// assertion whose receiver the walk did not follow retains every method of every
// type reachable from every operand a reflect.Value may hold a value of
// ([flowGraph.reflectedValues]).
func (f *encodingFlow) fieldAssertion(a *fieldAssertion) error {
	followed := a.followed && (!a.unseen || !f.graph.interfaceValued)
	if followed && len(a.held) == 0 {
		return nil
	}
	site, err := f.kept.site(a.at.Pos())
	if err != nil {
		return err
	}
	for _, asserted := range a.asserted {
		detail := "converted to " + asserted.name + " from a value read by reflection"
		for _, held := range a.held {
			switch {
			case held.ro != readWritable && held.at != nil:
				continue // Interface panics on a value read from a field that is not exported
			case held.at != nil:
				f.keepImplementing(held.at, asserted.iface, &site, detail)
				continue
			}
			f.keepEveryMethod(f.reflectReach([]reflectOrigin{held.origin}), &site, detail)
		}
		if !followed {
			f.keepEveryMethod(f.reflectReach(f.graph.reflectedValues()), &site, detail)
		}
	}
	return nil
}

// keepImplementing records the methods by which t implements iface.
func (f *encodingFlow) keepImplementing(t types.Type, iface *types.Interface, site *evidence, detail string) {
	if !types.Implements(t, iface) {
		return
	}
	for m := range iface.Methods() {
		if impl, isFunc := lookupMethod(t, m); isFunc {
			f.kept.record(impl, site, detail)
		}
	}
}

// keepEveryMethod records every method, exported or not, of each type.
func (f *encodingFlow) keepEveryMethod(reached []*types.Named, site *evidence, detail string) {
	for _, named := range reached {
		for m := range named.Origin().Methods() {
			f.kept.record(m, site, detail)
		}
	}
}

// reflectReach is every defined type reachable from the operands' static types,
// closed over every field, element, map key and value, and at an interface-typed
// field or operand every type the program stores there ([storedTypes]); a type
// parameter stands for each type argument it is instantiated with. Where the
// stores do not cover a position, every defined type of the program is reachable.
// An exact origin reaches its own type, and nothing where that is an interface.
func (f *encodingFlow) reflectReach(origins []reflectOrigin) []*types.Named {
	r := &reachWalk{f: f, seen: make(map[string]bool)}
	for _, o := range origins {
		if o.exact {
			if _, isParam := types.Unalias(o.t).(*types.TypeParam); isParam || !types.IsInterface(o.t) {
				r.queue = append(r.queue, reachItem{t: o.t})
			}
			continue
		}
		if !types.IsInterface(o.t) {
			r.queue = append(r.queue, reachItem{t: o.t})
			continue
		}
		held := f.graph.stored.value(o.info, o.fn, o.expr)
		if held == nil {
			r.everyType()
		}
		for _, t := range held {
			r.queue = append(r.queue, reachItem{t: t})
		}
	}
	r.walk()
	return r.found
}

// reachWalk is the state of one [encodingFlow.reflectReach] walk.
type reachWalk struct {
	f     *encodingFlow
	seen  map[string]bool
	queue []reachItem
	found []*types.Named
	every bool
}

// reachItem is one type the walk reaches, and whether a store set of the field it
// was reached through already gives the types an interface below it holds.
type reachItem struct {
	t       types.Type
	covered bool
}

// everyType adds every defined type of the program to the walk, once.
func (r *reachWalk) everyType() {
	if r.every {
		return
	}
	r.every = true
	for _, named := range programDeclaredTypes(r.f.kept.in) {
		r.queue = append(r.queue, reachItem{t: named})
	}
}

func (r *reachWalk) walk() {
	for len(r.queue) > 0 {
		item := r.queue[0]
		r.queue = r.queue[1:]
		t := types.Unalias(item.t)
		key := resolve.Spell(t, nil)
		if item.covered {
			key += "\x00covered"
		}
		if r.seen[key] {
			continue
		}
		r.seen[key] = true
		r.step(t, item.covered)
	}
}

// step adds what one reached type reaches.
func (r *reachWalk) step(t types.Type, covered bool) {
	if param, isParam := t.(*types.TypeParam); isParam {
		for _, bound := range bindings(r.f.typeArgs, param) {
			r.queue = append(r.queue, reachItem{t: bound})
		}
		return
	}
	if named, isNamed := t.(*types.Named); isNamed && !types.IsInterface(named) {
		r.found = append(r.found, named)
		r.queue = append(r.queue, reachItem{t: named.Underlying()})
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Interface:
		if !covered {
			r.everyType()
		}
	case *types.Struct:
		r.fields(u)
	case *types.Map:
		r.queue = append(r.queue, reachItem{t: u.Key(), covered: covered}, reachItem{t: u.Elem(), covered: covered})
	case interface{ Elem() types.Type }: // a pointer, slice, array or channel
		r.queue = append(r.queue, reachItem{t: u.Elem(), covered: covered})
	}
}

// fields adds the type of every field of a reached struct, and for a field holding
// an interface the types its store set gives, which cover the interface below it.
func (r *reachWalk) fields(st *types.Struct) {
	for field := range st.Fields() {
		fieldCovered := false
		if holdsInterface(field.Type()) {
			fieldCovered = true
			for _, stored := range r.f.graph.stored.of(field) {
				r.queue = append(r.queue, reachItem{t: stored})
			}
		}
		r.queue = append(r.queue, reachItem{t: field.Type(), covered: fieldCovered})
	}
}

// programDeclaredTypes is every defined type the analysed program declares at
// package level that is not an interface, generic ones included.
func programDeclaredTypes(in *Program) []*types.Named {
	var found []*types.Named
	for _, p := range in.Facts.Program() {
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			typeName, isType := scope.Lookup(name).(*types.TypeName)
			if !isType || typeName.IsAlias() {
				continue
			}
			if named, isNamed := typeName.Type().(*types.Named); isNamed && !types.IsInterface(named) {
				found = append(found, named)
			}
		}
	}
	return found
}

// lookupMethod is the method of t one interface method names.
func lookupMethod(t types.Type, m *types.Func) (*types.Func, bool) {
	obj, _, _ := types.LookupFieldOrMethod(t, true, m.Pkg(), m.Name())
	fn, isFunc := obj.(*types.Func)
	return fn, isFunc
}

// reflectWalk reads the reflect.Value expressions of one function body, each local of
// that type through every value the body assigns to it, and each parameter of that
// type through the argument every call passes where the function is one the program
// only calls by name.
type reflectWalk struct {
	info     *types.Info
	body     *ast.BlockStmt
	fn       *types.Func
	assigned map[*types.Var][]ast.Expr
	opaque   map[*types.Var]bool                                     // a local some write the walk does not read reaches
	callees  func(*types.Info, *ast.CallExpr) ([]facts.Callee, bool) // [Program.callees]
	flow     *encodingFlow
}

// maxReflectSteps bounds how many steps one reading passes through, which a cycle
// of assignments or of calls would otherwise follow without end.
const maxReflectSteps = 8

// receiver is the reflect.Value one call of a method of reflect.Value is called on:
// a method expression's first argument, or the receiver a method value or a direct
// call binds, which the walk follows only where it lies in the body it walks.
func (w *reflectWalk) receiver(call *ast.CallExpr, callee facts.Callee) (ast.Expr, bool) {
	if callee.Shift == 1 {
		if len(call.Args) == 0 {
			return nil, false
		}
		return call.Args[0], true
	}
	recv := callee.Recv
	if recv == nil || recv.Pos() < w.body.Pos() || recv.End() > w.body.End() {
		return nil, false
	}
	return recv, true
}

// held is every value one reflect.Value expression may hold, or every type one
// reflect.Type or reflect.StructField expression describes, and whether the walk
// followed each back to an operand of reflect.ValueOf or reflect.TypeOf or a type
// argument of reflect.TypeFor: through reflect.Indirect, the methods of
// reflect.Value and reflect.Type that derive from their receiver, a StructField's
// Type, a constructor ([reflectValueConstructors]), each value a local may hold,
// and the argument each call passes a parameter of a function called only by name.
func (w *reflectWalk) held(expr ast.Expr, steps int) ([]reflectHeld, bool) {
	if steps > maxReflectSteps {
		return nil, false
	}
	switch e := ast.Unparen(expr).(type) {
	case *ast.CallExpr:
		return w.called(e, steps)
	case *ast.Ident:
		return w.variable(e, steps)
	case *ast.SelectorExpr:
		return w.fieldType(e, steps)
	default:
		return nil, false
	}
}

// fieldType is [reflectWalk.held] for a selector: the Type field of a
// reflect.StructField is the type the field describes.
func (w *reflectWalk) fieldType(sel *ast.SelectorExpr, steps int) ([]reflectHeld, bool) {
	field, isField := w.info.Uses[sel.Sel].(*types.Var)
	if !isField || !field.IsField() || field.Name() != reflectTypeType || !isReflectNamed(w.info.TypeOf(sel.X), reflectStructField) {
		return nil, false
	}
	return w.held(sel.X, steps+1)
}

// called is [reflectWalk.held] for a call.
func (w *reflectWalk) called(call *ast.CallExpr, steps int) ([]reflectHeld, bool) {
	callees, dynamic := w.callees(w.info, call)
	if dynamic || len(callees) == 0 {
		return nil, false
	}
	var found []reflectHeld
	for _, callee := range callees {
		held, followed := w.calledOne(call, callee, steps)
		if !followed {
			return nil, false
		}
		found = append(found, held...)
	}
	return found, true
}

// calledOne is [reflectWalk.called] for one function the call reaches.
func (w *reflectWalk) calledOne(call *ast.CallExpr, callee facts.Callee, steps int) ([]reflectHeld, bool) {
	fn := callee.Fn
	switch {
	case (isReflectFunction(fn, reflectValueOf) || isReflectFunction(fn, reflectTypeOf)) && len(call.Args) == 1:
		t := w.info.TypeOf(call.Args[0])
		if t == nil {
			return nil, false
		}
		origin := reflectOrigin{t: t, expr: call.Args[0], info: w.info, fn: w.fn}
		return []reflectHeld{{origin: origin, at: enumeratedType(t)}}, true
	case isReflectFunction(fn, reflectTypeFor):
		t, is := reflectInstance(w.info, call.Fun, reflectTypeFor)
		if !is {
			return nil, false
		}
		origin := reflectOrigin{t: t, expr: call, info: w.info, fn: w.fn, exact: true}
		return []reflectHeld{{origin: origin, at: enumeratedType(t)}}, true
	case isReflectFunction(fn, reflectIndirect) && len(call.Args) == 1:
		return w.derived(call.Args[0], steps, func(one *reflectHeld) []reflectHeld {
			return []reflectHeld{{origin: one.origin, at: indirected(one.at), ro: one.ro}}
		})
	case isReflectValueConstructor(fn) && len(call.Args) > 0:
		pointer := reflectValueConstructors[fn.Name()]
		return w.derived(call.Args[0], steps, func(one *reflectHeld) []reflectHeld {
			if pointer && one.at != nil {
				return []reflectHeld{{origin: one.origin, at: types.NewPointer(one.at)}}
			}
			return []reflectHeld{{origin: one.origin, at: one.at}}
		})
	case isReflectValueMethod(fn, ""), isReflectTypeMethod(fn):
		recv, inBody := w.receiver(call, callee)
		if !inBody {
			return nil, false
		}
		return w.derived(recv, steps, w.methodStep(fn, call.Args[callee.Shift:]))
	default:
		return nil, false
	}
}

// derived is what one step returns for each value or type from that it reads.
func (w *reflectWalk) derived(from ast.Expr, steps int, step func(*reflectHeld) []reflectHeld) ([]reflectHeld, bool) {
	inner, followed := w.held(from, steps+1)
	if !followed {
		return nil, false
	}
	var found []reflectHeld
	for i := range inner {
		found = append(found, step(&inner[i])...)
	}
	return found, true
}

// methodStep is the step one method of reflect.Value or reflect.Type takes from
// its receiver: a value's Type describes the value's own type, writable whatever
// field the value was read from.
func (w *reflectWalk) methodStep(fn *types.Func, args []ast.Expr) func(*reflectHeld) []reflectHeld {
	switch {
	case isReflectTypeMethod(fn):
		return func(one *reflectHeld) []reflectHeld { return w.typeStep(fn.Name(), one, args) }
	case fn.Name() == reflectTypeType:
		return func(one *reflectHeld) []reflectHeld { return []reflectHeld{{origin: one.origin, at: one.at}} }
	default:
		return func(one *reflectHeld) []reflectHeld { return w.step(fn.Name(), one, args) }
	}
}

// variable is [reflectWalk.held] for an identifier: every value the body gives a
// local, or the argument each call passes a parameter.
func (w *reflectWalk) variable(id *ast.Ident, steps int) ([]reflectHeld, bool) {
	v, isVar := w.info.Uses[id].(*types.Var)
	if !isVar || w.opaque[v] {
		return nil, false
	}
	values, local := w.assigned[v]
	var found []reflectHeld
	for _, value := range values {
		held, followed := w.held(value, steps+1)
		if !followed {
			return nil, false
		}
		found = append(found, held...)
	}
	args, isParam, bound := w.flow.reflectArguments(w.fn, v)
	switch {
	case isParam && !bound, !isParam && !local:
		return nil, false
	}
	for _, arg := range args {
		caller, read := w.flow.callerWalk(arg)
		if !read {
			return nil, false
		}
		held, followed := caller.held(arg.expr, steps+1)
		if !followed {
			return nil, false
		}
		found = append(found, held...)
	}
	return found, true
}

// reflectWalkOf is the walk of one function declaration's body, built once.
func (f *encodingFlow) reflectWalkOf(info *types.Info, fd *ast.FuncDecl) *reflectWalk {
	if w, built := f.reflectWalks[fd]; built {
		return w
	}
	fn, _ := info.Defs[fd.Name].(*types.Func)
	assigned, opaque := reflectValueAssignments(info, fd.Body)
	w := &reflectWalk{info: info, body: fd.Body, fn: fn, assigned: assigned, opaque: opaque, callees: f.kept.in.callees, flow: f}
	f.reflectWalks[fd] = w
	return w
}

// reflectArguments is what each call passes one parameter of fn, whether v is a
// parameter of fn at all, and whether the program only ever calls fn by name, so
// every argument is known: the rule [valueFlow.arguments] reads for an argument
// naming a template. In a library an exported function an importer can call
// passes arguments the analysis does not see.
func (f *encodingFlow) reflectArguments(fn *types.Func, v *types.Var) (args []flowBinding, isParam, bound bool) {
	flow := f.valueFlow()
	param, isParam := flow.params[v]
	if !isParam || fn == nil || param.fn != fn {
		return nil, false, false
	}
	if f.kept.in.Options.PublishedAPI && fn.Exported() {
		return nil, true, false
	}
	args, bound = flow.arguments(v, param, flowAt{})
	return args, true, bound
}

// callerWalk is the walk of the function declaration whose body one argument is
// written in.
func (f *encodingFlow) callerWalk(arg flowBinding) (*reflectWalk, bool) {
	for _, d := range f.valueFlow().decls {
		body := d.decl.Body
		if d.info == arg.at.info && body.Pos() <= arg.expr.Pos() && arg.expr.End() <= body.End() {
			return f.reflectWalkOf(d.info, d.decl), true
		}
	}
	return nil, false
}

// valueFlow is the program's value flow, the template reading's where it built one.
func (f *encodingFlow) valueFlow() *valueFlow {
	if f.flow == nil {
		if f.templates != nil && f.templates.flow != nil {
			f.flow = f.templates.flow
		} else {
			f.flow = newValueFlow(f.kept.in)
		}
	}
	return f.flow
}

// step is every value one method of reflect.Value returns, called on one held
// value, with its static type where the step is enumerated: Field and Index, whose
// argument selects one of the struct's fields or the slice's or array's element
// type, every one where it is no constant; FieldByIndex with a constant path;
// FieldByName with a constant name; MapIndex over a map; and Elem on a pointer. A
// value of an interface type among them is not enumerated, nor is any other method,
// argument or value: its static type is nil.
func (w *reflectWalk) step(method string, held *reflectHeld, args []ast.Expr) []reflectHeld {
	unknown := []reflectHeld{{origin: held.origin}}
	if held.at == nil {
		return unknown
	}
	t := types.Unalias(held.at)
	var next []reflectHeld
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		if method != reflectElem {
			return unknown
		}
		next = append(next, reflectHeld{at: u.Elem(), ro: held.ro})
	case *types.Struct:
		fields, known := w.structStep(method, t, u, args)
		if !known {
			return unknown
		}
		for _, f := range fields {
			next = append(next, reflectHeld{at: f.t, ro: f.read(held.ro)})
		}
	case *types.Slice:
		if method != "Index" {
			return unknown
		}
		next = append(next, reflectHeld{at: u.Elem(), ro: held.ro.through()})
	case *types.Array:
		if method != "Index" {
			return unknown
		}
		next = append(next, reflectHeld{at: u.Elem(), ro: held.ro.through()})
	case *types.Map:
		if method != "MapIndex" {
			return unknown
		}
		next = append(next, reflectHeld{at: u.Elem(), ro: held.ro.through()})
	default:
		return unknown
	}
	for i := range next {
		next[i].origin = held.origin
		next[i].at = enumeratedType(next[i].at)
	}
	return next
}

// typeStep is every type one method of reflect.Type returns, called on one held
// type, where the step is enumerated: Elem of a pointer, slice, array, map or
// channel, Key of a map, Field and FieldByIndex of a struct as [structStep]
// selects them (a StructField, whose Type the walk reads), and In and Out of a
// function with a constant index, or every parameter or result where it is no
// constant. A type built from no value carries no read-only mark. A type of an
// interface among them is not enumerated, nor is any other method, argument or
// type: its type is nil.
func (w *reflectWalk) typeStep(method string, held *reflectHeld, args []ast.Expr) []reflectHeld {
	if held.at == nil {
		return []reflectHeld{{origin: held.origin}}
	}
	next, known := w.typeSelected(method, types.Unalias(held.at), args)
	if !known {
		return []reflectHeld{{origin: held.origin}}
	}
	found := make([]reflectHeld, 0, len(next))
	for _, one := range next {
		found = append(found, reflectHeld{origin: held.origin, at: enumeratedType(one)})
	}
	return found
}

// typeSelected is the types one method of reflect.Type returns called on t, and
// false where [reflectWalk.typeStep] does not enumerate them.
func (w *reflectWalk) typeSelected(method string, t types.Type, args []ast.Expr) ([]types.Type, bool) {
	switch u := t.Underlying().(type) {
	case *types.Struct:
		if method != reflectField && method != reflectFieldByIndex {
			return nil, false
		}
		fields, known := w.structStep(method, t, u, args)
		next := make([]types.Type, 0, len(fields))
		for _, f := range fields {
			next = append(next, f.t)
		}
		return next, known
	case *types.Map:
		switch method {
		case reflectElem:
			return []types.Type{u.Elem()}, true
		case "Key":
			return []types.Type{u.Key()}, true
		}
	case *types.Signature:
		switch method {
		case "In":
			return w.tupleSteps(u.Params(), args), true
		case "Out":
			return w.tupleSteps(u.Results(), args), true
		}
	case interface{ Elem() types.Type }: // a pointer, slice, array or channel
		if method == reflectElem {
			return []types.Type{u.Elem()}, true
		}
	}
	return nil, false
}

// tupleSteps is the type of each parameter or result In or Out may return: the
// one a constant index selects, or every one where the index is no constant.
func (w *reflectWalk) tupleSteps(tuple *types.Tuple, args []ast.Expr) []types.Type {
	if i, known := w.constantIndex(args); known {
		if i >= tuple.Len() {
			return nil
		}
		return []types.Type{tuple.At(i).Type()}
	}
	found := make([]types.Type, 0, tuple.Len())
	for v := range tuple.Variables() {
		found = append(found, v.Type())
	}
	return found
}

// structStep is the fields one method of reflect.Value selects of a struct value
// of type t, and false where the walk does not enumerate them: Field with any
// argument, FieldByIndex with a constant path and FieldByName with a constant
// name.
func (w *reflectWalk) structStep(method string, t types.Type, st *types.Struct, args []ast.Expr) ([]fieldStep, bool) {
	switch method {
	case reflectField:
		return w.fieldSteps(st, args), true
	case reflectFieldByIndex:
		path, known := w.constantPath(args)
		if !known {
			return nil, false
		}
		return fieldPath(t, path), true
	case "FieldByName":
		if len(args) != 1 {
			return nil, false
		}
		name, known := constantString(w.info, args[0])
		if !known {
			return nil, false
		}
		return fieldNamed(t, name), true
	default:
		return nil, false
	}
}

// through is the mark a value read through an element or a map value of a value
// carrying this one keeps: any mark becomes sticky (flag.ro).
func (ro readOnly) through() readOnly {
	if ro != readWritable {
		return readSticky
	}
	return readWritable
}

// fieldStep is one field a step may read, by its type, and whether it is exported
// and embedded.
type fieldStep struct {
	t        types.Type
	exported bool
	embedded bool
}

// read is the mark of a value read from this field of a value carrying ro: the
// sticky mark stays, the embedded one does not, and a field that is not exported
// adds its own (reflect.Value.Field).
func (f fieldStep) read(ro readOnly) readOnly {
	switch {
	case ro == readSticky || !f.exported && !f.embedded:
		return readSticky
	case !f.exported:
		return readEmbedded
	default:
		return readWritable
	}
}

// fieldSteps is each field Field may read from one struct: the one a constant index
// selects, or every one where the index is no constant.
func (w *reflectWalk) fieldSteps(st *types.Struct, args []ast.Expr) []fieldStep {
	if i, known := w.constantIndex(args); known {
		if i >= st.NumFields() {
			return nil
		}
		return []fieldStep{stepOf(st.Field(i))}
	}
	found := make([]fieldStep, 0, st.NumFields())
	for field := range st.Fields() {
		found = append(found, stepOf(field))
	}
	return found
}

func stepOf(field *types.Var) fieldStep {
	return fieldStep{t: field.Type(), exported: field.Exported(), embedded: field.Embedded()}
}

// enumeratedType is t where it is a static type that is no interface, and nil
// otherwise.
func enumeratedType(t types.Type) types.Type {
	if t == nil || types.IsInterface(t) {
		return nil
	}
	return t
}

// indirected is what reflect.Indirect returns for a value of static type t: the
// element of a pointer, t itself otherwise.
func indirected(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	if pointer, isPointer := types.Unalias(t).Underlying().(*types.Pointer); isPointer {
		return enumeratedType(pointer.Elem())
	}
	return t
}

// constantIndex is the one constant integer argument of a call.
func (w *reflectWalk) constantIndex(args []ast.Expr) (int, bool) {
	if len(args) != 1 {
		return 0, false
	}
	value := w.info.Types[args[0]].Value
	if value == nil || value.Kind() != constant.Int {
		return 0, false
	}
	i, exact := constant.Int64Val(value)
	return int(i), exact && i >= 0
}

// constantPath is the one argument of FieldByIndex where it is a slice literal of
// constant integers.
func (w *reflectWalk) constantPath(args []ast.Expr) ([]int, bool) {
	if len(args) != 1 {
		return nil, false
	}
	lit, isLit := ast.Unparen(args[0]).(*ast.CompositeLit)
	if !isLit {
		return nil, false
	}
	path := make([]int, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		i, known := w.constantIndex([]ast.Expr{elt})
		if !known {
			return nil, false
		}
		path = append(path, i)
	}
	return path, true
}

// fieldPath is the field one index path selects from struct type t, through
// embedded pointers, as one step whose mark is that of the last field read through
// the others; nothing where it selects none.
func fieldPath(t types.Type, path []int) []fieldStep {
	ro := readWritable
	var last fieldStep
	for at, i := range path {
		if pointer, isPointer := types.Unalias(t).Underlying().(*types.Pointer); isPointer {
			t = pointer.Elem()
		}
		st, isStruct := types.Unalias(t).Underlying().(*types.Struct)
		if !isStruct || i >= st.NumFields() {
			return nil
		}
		if at > 0 {
			ro = last.read(ro)
		}
		last = stepOf(st.Field(i))
		t = last.t
	}
	if ro == readSticky {
		last.exported, last.embedded = false, false
	}
	return []fieldStep{last}
}

// fieldNamed is the field FieldByName selects by one name from struct type t, a
// promoted one included, nothing where it selects none. A promoted field is read
// through embedded fields only, whose embedded mark does not stay.
func fieldNamed(t types.Type, name string) []fieldStep {
	var pkg *types.Package
	if named, isNamed := types.Unalias(t).(*types.Named); isNamed {
		pkg = named.Obj().Pkg()
	}
	obj, _, _ := types.LookupFieldOrMethod(t, false, pkg, name)
	field, isField := obj.(*types.Var)
	if !isField {
		return nil
	}
	return []fieldStep{stepOf(field)}
}

// isReflectFunction reports whether fn is the function of the reflect package
// named name.
func isReflectFunction(fn *types.Func, name string) bool {
	return fn.Pkg() != nil && fn.Pkg().Path() == reflectPackage && fn.Signature().Recv() == nil && fn.Name() == name
}

// isReflectValueMethod reports whether fn is a method of reflect.Value, the one
// named name where name is not empty.
func isReflectValueMethod(fn *types.Func, name string) bool {
	recv := fn.Signature().Recv()
	return recv != nil && fn.Pkg() != nil && fn.Pkg().Path() == reflectPackage &&
		receiverName(recv.Type()) == reflectValueType && (name == "" || fn.Name() == name)
}

// isReflectValue reports whether t is reflect.Value.
func isReflectValue(t types.Type) bool {
	return isReflectNamed(t, reflectValueType)
}

// isReflectTypeMethod reports whether fn is a method of reflect.Type.
func isReflectTypeMethod(fn *types.Func) bool {
	recv := fn.Signature().Recv()
	return recv != nil && isReflectNamed(recv.Type(), reflectTypeType)
}

// isReflectValueConstructor reports whether fn is one of the functions of reflect
// that build a value of a reflect.Type ([reflectValueConstructors]).
func isReflectValueConstructor(fn *types.Func) bool {
	_, constructs := reflectValueConstructors[fn.Name()]
	return constructs && isReflectFunction(fn, fn.Name())
}

// isReflectNamed reports whether t is the type of the reflect package named name.
func isReflectNamed(t types.Type, name string) bool {
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.Obj().Name() == name && isReflectPackage(named)
}

// reflectValueAssignments is, for every local of type reflect.Value, reflect.Type
// or reflect.StructField one body declares or assigns one value to at a time, the
// values it is given, and every local of those types a write the walk does not
// read reaches: a multi-value assignment, a range clause, or its address taken.
func reflectValueAssignments(info *types.Info, body *ast.BlockStmt) (held map[*types.Var][]ast.Expr, opaque map[*types.Var]bool) {
	a := &reflectAssignments{info: info, held: make(map[*types.Var][]ast.Expr), opaque: make(map[*types.Var]bool)}
	ast.Inspect(body, a.visit)
	return a.held, a.opaque
}

// reflectAssignments is the state of one [reflectValueAssignments] walk.
type reflectAssignments struct {
	info   *types.Info
	held   map[*types.Var][]ast.Expr
	opaque map[*types.Var]bool
}

// visit reads one node of the body.
func (a *reflectAssignments) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.AssignStmt:
		a.keep(n.Lhs, n.Rhs)
	case *ast.ValueSpec:
		names := make([]ast.Expr, len(n.Names))
		for i, name := range n.Names {
			names[i] = name
		}
		if len(n.Values) > 0 {
			a.keep(names, n.Values)
		}
	case *ast.RangeStmt:
		a.hide(n.Key)
		a.hide(n.Value)
	case *ast.UnaryExpr:
		if n.Op == token.AND {
			a.hide(ast.Unparen(n.X))
		}
	}
	return true
}

// keep records the values one assignment or declaration gives each local the walk
// reads it names ([reflectWalkedVar]), one value to a name, and hides one a
// multi-value form gives a value.
func (a *reflectAssignments) keep(names, values []ast.Expr) {
	for i, name := range names {
		v, isValue := reflectWalkedVar(a.info, name)
		switch {
		case !isValue:
		case len(names) != len(values):
			a.opaque[v] = true
		default:
			a.held[v] = append(a.held[v], values[i])
		}
	}
}

// hide marks a local the walk reads that a write the walk does not read reaches.
func (a *reflectAssignments) hide(name ast.Expr) {
	if v, isValue := reflectWalkedVar(a.info, name); isValue {
		a.opaque[v] = true
	}
}

// reflectWalkedVar is the variable of type reflect.Value, reflect.Type or
// reflect.StructField one name declares or assigns.
func reflectWalkedVar(info *types.Info, name ast.Expr) (*types.Var, bool) {
	ident, isIdent := name.(*ast.Ident)
	if !isIdent {
		return nil, false
	}
	v, isVar := info.ObjectOf(ident).(*types.Var)
	if !isVar {
		return nil, false
	}
	t := v.Type()
	return v, isReflectValue(t) || isReflectNamed(t, reflectTypeType) || isReflectNamed(t, reflectStructField)
}

func isReflectPackage(t types.Type) bool {
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == reflectPackage
}
