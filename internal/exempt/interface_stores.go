package exempt

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
)

// storeNode is one place the analysed program stores interface values in, keyed by
// where it is written: a field, a variable or a composite literal, or one parameter
// of one function, which holds every argument the program passes for it.
type storeNode struct {
	at    token.Position
	param int // the parameter's index, or placeNode
}

// placeNode marks a node that is no parameter, and resultNode the first result of a
// function: result i is resultNode - i.
const (
	placeNode  = -1
	resultNode = -2
)

// storedTypes is, per place of the analysed program that is an interface or holds one
// as its element or map value, the concrete types the program stores in it. A place
// also holds what every place it is filled from holds, which [storedTypes.reaching]
// resolves on first use.
type storedTypes struct {
	sites  *declarationSites
	direct map[storeNode][]types.Type
	from   map[storeNode][]storeNode
	memo   map[storeNode][]types.Type

	// called is every identifier a call names as its callee, and valued every
	// function or method of the program some other identifier uses as a value.
	called map[*ast.Ident]bool
	valued map[*types.Func]bool

	// abstract is every interface method a call takes a result of.
	abstract map[*types.Func]bool
}

// storedValue is what one expression carries into a place: a concrete type, the node
// whose values it carries, or both for a container of interface values.
type storedValue struct {
	concrete types.Type
	from     *storeNode
}

// storeWalk reads the stores of one declaration against the function that encloses
// them.
type storeWalk struct {
	*storedTypes
	info      *types.Info
	fn        *types.Func
	inLiteral map[*ast.ReturnStmt]bool // the returns of the declaration's function literals
}

// storedTypesOf reads every store of the analysed program into a place that holds an
// interface: a field's value in a composite literal, the right side of an assignment
// or a declaration, an element of a composite literal of a slice, array or map type,
// an argument of append onto it, the right side of an assignment to an index of it,
// and an argument a call passes for a parameter. A stored interface-typed parameter
// stands for every argument the program's calls pass for it, and a parameter no call
// passes adds nothing.
func storedTypesOf(in *Input, sites *declarationSites) *storedTypes {
	s := &storedTypes{
		sites:    sites,
		direct:   make(map[storeNode][]types.Type),
		from:     make(map[storeNode][]storeNode),
		memo:     make(map[storeNode][]types.Type),
		called:   make(map[*ast.Ident]bool),
		valued:   make(map[*types.Func]bool),
		abstract: make(map[*types.Func]bool),
	}
	for _, p := range in.Facts.Program() {
		for _, file := range in.Facts.Files(p) {
			for _, decl := range file.Decls {
				w := &storeWalk{
					storedTypes: s, info: p.TypesInfo, fn: enclosingFunc(p.TypesInfo, decl),
					inLiteral: literalReturns(decl),
				}
				ast.Inspect(decl, w.visit)
			}
		}
	}
	s.linkIndirect()
	s.linkImplementations(programNamedTypes(in))
	return s
}

func isAbstract(fn *types.Func) bool {
	recv := fn.Signature().Recv()
	return recv != nil && types.IsInterface(recv.Type())
}

// linkImplementations fills each interface-typed result of an interface method a call
// takes a result of from the same result of every method of the program's defined
// types implementing it, because the call stands for each of them.
func (s *storedTypes) linkImplementations(named []*types.Named) {
	methods := slices.SortedFunc(maps.Keys(s.abstract), func(a, b *types.Func) int { return cmp.Compare(a.Pos(), b.Pos()) })
	w := &storeWalk{storedTypes: s}
	for _, method := range methods {
		iface, returns := returnsInterface(method)
		if !returns {
			continue
		}
		for _, t := range named {
			if impl, implements := implementation(t, iface, method); implements {
				w.linkResults(method, impl)
			}
		}
	}
}

// returnsInterface is the interface one interface method is declared by, where the
// method is written in a file and one of its results is an interface.
func returnsInterface(method *types.Func) (*types.Interface, bool) {
	iface, isInterface := method.Signature().Recv().Type().Underlying().(*types.Interface)
	if !method.Pos().IsValid() || !isInterface {
		return nil, false
	}
	return iface, slices.ContainsFunc(slices.Collect(method.Signature().Results().Variables()),
		func(v *types.Var) bool { return types.IsInterface(v.Type()) })
}

// linkResults fills each interface-typed result of one interface method from the same
// result of one implementation.
func (w *storeWalk) linkResults(method, impl *types.Func) {
	results := method.Signature().Results()
	for i := range results.Len() {
		if types.IsInterface(results.At(i).Type()) {
			w.link(w.result(method, i), w.result(impl, i))
		}
	}
}

// implementation is the method of t, or of a pointer to it, that implements one
// method of iface, where either implements iface.
func implementation(t *types.Named, iface *types.Interface, method *types.Func) (*types.Func, bool) {
	for _, held := range []types.Type{t, types.NewPointer(t)} {
		if !types.Implements(held, iface) {
			continue
		}
		obj, _, _ := types.LookupFieldOrMethod(held, false, method.Pkg(), method.Name())
		impl, isFunc := obj.(*types.Func)
		return impl, isFunc
	}
	return nil, false
}

// programNamedTypes is every non-generic defined type the analysed program declares at
// package level that is not an interface, in package and then name order.
func programNamedTypes(in *Input) []*types.Named {
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
			named, isNamed := typeName.Type().(*types.Named)
			if isNamed && named.TypeParams().Len() == 0 && !types.IsInterface(named) {
				found = append(found, named)
			}
		}
	}
	return found
}

// indirectResult is the node of result i of a call, with n results, through a
// function value: it holds what every function or method of the program used as a
// value returns there, because which one the value is the analysis does not follow.
func indirectResult(i, n int) storeNode {
	return storeNode{at: token.Position{Filename: "<indirect>", Line: n}, param: resultNode - i}
}

// linkIndirect fills each indirect result node from the results of every function
// or method of the program used as a value.
func (s *storedTypes) linkIndirect() {
	for fn := range s.valued {
		results := fn.Signature().Results()
		for i := range results.Len() {
			if types.IsInterface(results.At(i).Type()) {
				w := &storeWalk{storedTypes: s}
				w.link(indirectResult(i, results.Len()), w.result(fn, i))
			}
		}
	}
}

// literalReturns is every return statement of the function literals one declaration
// holds, which return from the literal rather than from the declared function.
func literalReturns(decl ast.Decl) map[*ast.ReturnStmt]bool {
	held := make(map[*ast.ReturnStmt]bool)
	ast.Inspect(decl, func(n ast.Node) bool {
		if lit, isLit := n.(*ast.FuncLit); isLit {
			ast.Inspect(lit.Body, func(inner ast.Node) bool {
				if ret, isReturn := inner.(*ast.ReturnStmt); isReturn {
					held[ret] = true
				}
				return true
			})
			return false
		}
		return true
	})
	return held
}

// enclosingFunc is the function one declaration declares, and nil for any other.
func enclosingFunc(info *types.Info, decl ast.Decl) *types.Func {
	fd, isFunc := decl.(*ast.FuncDecl)
	if !isFunc {
		return nil
	}
	fn, _ := info.Defs[fd.Name].(*types.Func)
	return fn
}

// of is the types the program stores in one field, and nil for a field that holds
// no interface.
func (s *storedTypes) of(field *types.Var) []types.Type {
	if s == nil || !holdsInterface(field.Type()) {
		return nil
	}
	return s.reaching(storeNode{at: s.sites.of(field), param: placeNode})
}

// argument is the types the program stores in the value one argument expression
// carries, where that value is a slice, an array or a map holding interface values:
// a composite literal, a field, or a variable, a parameter of fn among them.
func (s *storedTypes) argument(info *types.Info, fn *types.Func, arg ast.Expr) []types.Type {
	if s == nil {
		return nil
	}
	w := &storeWalk{storedTypes: s, info: info, fn: fn}
	node, carries := w.carrier(arg)
	if !carries {
		return nil
	}
	return s.reaching(node)
}

// value is the concrete types one interface-typed argument expression carries: what
// the program stores in the variable it names, or returns from the function whose
// call it is, and nil for anything else.
func (s *storedTypes) value(info *types.Info, fn *types.Func, arg ast.Expr) []types.Type {
	if s == nil {
		return nil
	}
	if t := info.TypeOf(arg); t == nil || !types.IsInterface(t) {
		return nil
	}
	w := &storeWalk{storedTypes: s, info: info, fn: fn}
	if held := w.valueOf(arg); held.from != nil {
		return s.reaching(*held.from)
	}
	return nil
}

// held is [storedTypes.value] for an argument that names no parameter of fn: what a
// local or a call carries, which no caller's argument records at its own site.
func (s *storedTypes) held(info *types.Info, fn *types.Func, arg ast.Expr) []types.Type {
	if s == nil {
		return nil
	}
	w := &storeWalk{storedTypes: s, info: info, fn: fn}
	if _, isParam := w.parameter(parameterNamed(info, arg)); isParam {
		return nil
	}
	return s.value(info, fn, arg)
}

// reaching is every concrete type stored in one node or in a node it is filled from,
// each once.
func (s *storedTypes) reaching(n storeNode) []types.Type {
	if held, known := s.memo[n]; known {
		return held
	}
	var found []types.Type
	keys := make(map[string]bool)
	seen := map[storeNode]bool{n: true}
	queue := []storeNode{n}
	for len(queue) > 0 {
		one := queue[0]
		queue = queue[1:]
		for _, t := range s.direct[one] {
			if key := types.TypeString(t, nil); !keys[key] {
				keys[key] = true
				found = append(found, t)
			}
		}
		for _, source := range s.from[one] {
			if !seen[source] {
				seen[source] = true
				queue = append(queue, source)
			}
		}
	}
	s.memo[n] = found
	return found
}

// holdsInterface reports whether a place of type t stores interface values: t is an
// interface, or a slice, an array or a map whose element is one.
func holdsInterface(t types.Type) bool {
	if types.IsInterface(t) {
		return true
	}
	return holdsInterfaceElements(t)
}

// holdsInterfaceElements reports whether t is a slice, an array or a map whose
// element is an interface.
func holdsInterfaceElements(t types.Type) bool {
	switch u := types.Unalias(t).Underlying().(type) {
	case *types.Slice:
		return types.IsInterface(u.Elem())
	case *types.Array:
		return types.IsInterface(u.Elem())
	case *types.Map:
		return types.IsInterface(u.Elem())
	}
	return false
}

// visit reads the stores one node makes.
func (w *storeWalk) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.CompositeLit:
		w.compositeLit(n)
	case *ast.AssignStmt:
		w.assign(n)
	case *ast.ValueSpec:
		w.valueSpec(n)
	case *ast.CallExpr:
		w.call(n)
		if name := calleeIdent(n.Fun); name != nil {
			w.called[name] = true
		}
	case *ast.Ident:
		if fn, isFunc := w.info.Uses[n].(*types.Func); isFunc && !w.called[n] {
			w.valued[fn] = true
		}
	case *ast.ReturnStmt:
		w.returned(n)
	}
	return true
}

// returned reads the values one return statement of the declared function stores
// in its interface-typed results.
func (w *storeWalk) returned(ret *ast.ReturnStmt) {
	if w.fn == nil || w.inLiteral[ret] {
		return
	}
	results := w.fn.Signature().Results()
	if len(ret.Results) == 1 && results.Len() > 1 {
		w.returnedCall(results, ret.Results[0])
		return
	}
	if len(ret.Results) != results.Len() {
		return
	}
	for i, value := range ret.Results {
		if types.IsInterface(results.At(i).Type()) {
			w.store(w.result(w.fn, i), w.valueOf(value))
		}
	}
}

// returnedCall reads the values a return statement whose one operand is a call of
// several results stores in the declared function's interface-typed results: each
// result of the call at the same position.
func (w *storeWalk) returnedCall(results *types.Tuple, value ast.Expr) {
	call, isCall := ast.Unparen(value).(*ast.CallExpr)
	if !isCall {
		return
	}
	held, isTuple := w.info.TypeOf(call).(*types.Tuple)
	nodes, resolved := w.resultsOf(call)
	if !isTuple || !resolved || held.Len() != results.Len() || len(nodes) != results.Len() {
		return
	}
	for i := range results.Len() {
		if !types.IsInterface(results.At(i).Type()) {
			continue
		}
		if t := held.At(i).Type(); !types.IsInterface(t) {
			w.store(w.result(w.fn, i), storedValue{concrete: t})
		} else {
			w.link(w.result(w.fn, i), nodes[i])
		}
	}
}

// result is the node of one result of one function.
func (w *storeWalk) result(fn *types.Func, i int) storeNode {
	return storeNode{at: w.sites.of(fn), param: resultNode - i}
}

// local is the node of an interface-typed variable one name declares or assigns,
// a parameter and a field excepted.
func (w *storeWalk) local(expr ast.Expr) (storeNode, bool) {
	id, isIdent := ast.Unparen(expr).(*ast.Ident)
	if !isIdent {
		return storeNode{}, false
	}
	v, isVar := w.info.ObjectOf(id).(*types.Var)
	if !isVar || v.IsField() || !types.IsInterface(v.Type()) {
		return storeNode{}, false
	}
	if _, isParam := w.parameter(v); isParam {
		return storeNode{}, false
	}
	return storeNode{at: w.sites.of(v), param: placeNode}, true
}

// fromCall links the interface-typed variables one assignment from a single call
// declares or assigns to the results of the program function it calls.
func (w *storeWalk) fromCall(lhs []ast.Expr, rhs ast.Expr) {
	call, isCall := ast.Unparen(rhs).(*ast.CallExpr)
	if !isCall {
		return
	}
	results, held := w.resultsOf(call)
	if !held || len(results) != len(lhs) {
		return
	}
	for i, target := range lhs {
		if node, isLocal := w.local(target); isLocal {
			w.link(node, results[i])
		}
	}
}

// resultsOf is the result nodes of one call: of the function it names, or of the
// function values it may call through.
func (w *storeWalk) resultsOf(call *ast.CallExpr) ([]storeNode, bool) {
	if callee, isFunc := resolveObject(w.info, call.Fun).(*types.Func); isFunc {
		if isAbstract(callee) {
			w.abstract[callee] = true
		}
		nodes := make([]storeNode, callee.Signature().Results().Len())
		for i := range nodes {
			nodes[i] = w.result(callee, i)
		}
		return nodes, true
	}
	t := w.info.TypeOf(call.Fun)
	if t == nil {
		return nil, false
	}
	sig, isSignature := t.Underlying().(*types.Signature)
	if !isSignature {
		return nil, false
	}
	nodes := make([]storeNode, sig.Results().Len())
	for i := range nodes {
		nodes[i] = indirectResult(i, len(nodes))
	}
	return nodes, true
}

// compositeLit reads the fields one struct literal sets, and the elements one
// literal of a type holding interface elements holds.
func (w *storeWalk) compositeLit(lit *ast.CompositeLit) {
	t := w.info.TypeOf(lit)
	if t == nil {
		return
	}
	if holdsInterfaceElements(t) {
		node := w.literal(lit)
		for _, elt := range lit.Elts {
			if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
				elt = kv.Value
			}
			w.store(node, w.valueOf(elt))
		}
		return
	}
	if st, isStruct := types.Unalias(t).Underlying().(*types.Struct); isStruct {
		w.structLit(st, lit)
	}
}

// structLit reads the fields one literal of a struct type sets.
func (w *storeWalk) structLit(st *types.Struct, lit *ast.CompositeLit) {
	for i, elt := range lit.Elts {
		if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
			if key, isIdent := kv.Key.(*ast.Ident); isIdent {
				if field, isVar := w.info.Uses[key].(*types.Var); isVar && field.IsField() {
					w.storeInto(w.field(field), field.Type(), kv.Value)
				}
			}
			continue
		}
		if i < st.NumFields() {
			w.storeInto(w.field(st.Field(i)), st.Field(i).Type(), elt)
		}
	}
}

// assign reads the stores one assignment makes into a place, or into an index of
// one.
func (w *storeWalk) assign(s *ast.AssignStmt) {
	if s.Tok != token.ASSIGN && s.Tok != token.DEFINE {
		return
	}
	if len(s.Rhs) == 1 {
		w.fromCall(s.Lhs, s.Rhs[0])
	}
	if len(s.Lhs) != len(s.Rhs) {
		return
	}
	for i, lhs := range s.Lhs {
		if target, indexed := ast.Unparen(lhs).(*ast.IndexExpr); indexed {
			if node, holds := w.container(target.X); holds {
				w.store(node, w.valueOf(s.Rhs[i]))
			}
			continue
		}
		if node, t, isPlace := w.place(lhs); isPlace {
			w.storeInto(node, t, s.Rhs[i])
		}
	}
}

// valueSpec reads the stores one variable declaration makes.
func (w *storeWalk) valueSpec(spec *ast.ValueSpec) {
	if len(spec.Values) == 1 {
		names := make([]ast.Expr, 0, len(spec.Names))
		for _, name := range spec.Names {
			names = append(names, name)
		}
		w.fromCall(names, spec.Values[0])
	}
	if len(spec.Names) != len(spec.Values) {
		return
	}
	for i, name := range spec.Names {
		if node, t, isPlace := w.place(name); isPlace {
			w.storeInto(node, t, spec.Values[i])
		}
	}
}

// storeInto reads what one value stored in a place of type t puts there: the value
// itself in an interface-typed place, and in a place holding interface elements what
// the container stored there holds.
func (w *storeWalk) storeInto(node storeNode, t types.Type, value ast.Expr) {
	if types.IsInterface(t) {
		w.store(node, w.valueOf(value))
		return
	}
	if holdsInterfaceElements(t) {
		w.fill(node, value)
	}
}

// fill reads one container value stored in a place holding interface elements: a
// literal, a field or a variable it copies, and the operands of append onto one.
func (w *storeWalk) fill(node storeNode, value ast.Expr) {
	call, isCall := ast.Unparen(value).(*ast.CallExpr)
	if !isCall || !isAppend(w.info, call) {
		if source, carries := w.carrier(value); carries {
			w.link(node, source)
		}
		return
	}
	w.fill(node, call.Args[0])
	if call.Ellipsis.IsValid() {
		if len(call.Args) == 2 {
			w.fill(node, call.Args[1])
		}
		return
	}
	for _, arg := range call.Args[1:] {
		w.store(node, w.valueOf(arg))
	}
}

// carrier is the node whose values one expression of a type holding interface
// elements carries: a composite literal, a field or a variable.
func (w *storeWalk) carrier(expr ast.Expr) (storeNode, bool) {
	if lit, isLit := ast.Unparen(expr).(*ast.CompositeLit); isLit {
		if t := w.info.TypeOf(lit); t != nil && holdsInterfaceElements(t) {
			return w.literal(lit), true
		}
		return storeNode{}, false
	}
	return w.container(expr)
}

// container is the node of one place holding interface elements an expression names.
func (w *storeWalk) container(expr ast.Expr) (storeNode, bool) {
	node, t, isPlace := w.place(expr)
	if !isPlace || !holdsInterfaceElements(t) {
		return storeNode{}, false
	}
	return node, true
}

// place is the node of the field one selector selects, or of the variable holding
// interface elements one name names, with its type. An interface-typed variable is
// no place: what flows out of one is read only where it is a parameter
// ([storeWalk.valueOf]).
func (w *storeWalk) place(expr ast.Expr) (storeNode, types.Type, bool) {
	switch e := ast.Unparen(expr).(type) {
	case *ast.SelectorExpr:
		if field := selectedField(w.info, e); field != nil {
			return w.field(field), field.Type(), true
		}
	case *ast.Ident:
		v, isVar := w.info.ObjectOf(e).(*types.Var)
		if !isVar || v.IsField() || !holdsInterfaceElements(v.Type()) {
			break
		}
		if node, isParam := w.parameter(v); isParam {
			return node, v.Type(), true
		}
		return storeNode{at: w.sites.of(v), param: placeNode}, v.Type(), true
	}
	return storeNode{}, nil, false
}

// field is the node of one field.
func (w *storeWalk) field(field *types.Var) storeNode {
	return storeNode{at: w.sites.of(field), param: placeNode}
}

// literal is the node of one composite literal.
func (w *storeWalk) literal(lit *ast.CompositeLit) storeNode {
	return storeNode{at: w.sites.position(lit.Lbrace), param: placeNode}
}

// parameter is the node of v where v is a parameter of the enclosing function other
// than a variadic one.
func (w *storeWalk) parameter(v *types.Var) (storeNode, bool) {
	if w.fn == nil || v == nil {
		return storeNode{}, false
	}
	sig := w.fn.Signature()
	for i := range sig.Params().Len() {
		if sig.Params().At(i) == v && (!sig.Variadic() || i < sig.Params().Len()-1) {
			return storeNode{at: w.sites.of(w.fn), param: i}, true
		}
	}
	return storeNode{}, false
}

// selectedField is the field one selector selects, and nil where it selects none.
func selectedField(info *types.Info, sel *ast.SelectorExpr) *types.Var {
	selection := info.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return nil
	}
	field, _ := selection.Obj().(*types.Var)
	return field
}

// isAppend reports whether one call is to the append built-in.
func isAppend(info *types.Info, call *ast.CallExpr) bool {
	id, isIdent := ast.Unparen(call.Fun).(*ast.Ident)
	if !isIdent {
		return false
	}
	builtin, isBuiltin := info.Uses[id].(*types.Builtin)
	return isBuiltin && builtin.Name() == "append" && len(call.Args) > 0
}

// valueOf is what one expression carries into an interface-typed place: its concrete
// type, with what it holds where it is a container of interface values
// ([storeWalk.carrier]), the interface-typed parameter of the enclosing function or the
// interface-typed variable it names, or the one result of the call it is.
func (w *storeWalk) valueOf(expr ast.Expr) storedValue {
	t := w.info.TypeOf(expr)
	if t == nil {
		return storedValue{}
	}
	if !types.IsInterface(t) {
		if node, carries := w.carrier(expr); carries {
			return storedValue{concrete: t, from: &node}
		}
		return storedValue{concrete: t}
	}
	if node, isParam := w.parameter(parameterNamed(w.info, expr)); isParam {
		return storedValue{from: &node}
	}
	if node, isLocal := w.local(expr); isLocal {
		return storedValue{from: &node}
	}
	if call, isCall := ast.Unparen(expr).(*ast.CallExpr); isCall {
		if results, held := w.resultsOf(call); held && len(results) == 1 {
			return storedValue{from: &results[0]}
		}
	}
	if node, isField := w.fieldRead(expr); isField {
		return storedValue{from: &node}
	}
	return storedValue{}
}

// fieldRead is the node of the field one selector expression reads.
func (w *storeWalk) fieldRead(expr ast.Expr) (storeNode, bool) {
	sel, isSelector := ast.Unparen(expr).(*ast.SelectorExpr)
	if !isSelector {
		return storeNode{}, false
	}
	selection := w.info.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return storeNode{}, false
	}
	field, isField := selection.Obj().(*types.Var)
	if !isField {
		return storeNode{}, false
	}
	return storeNode{at: w.sites.of(field), param: placeNode}, true
}

// store records one value stored in a node. A value of an unnamed basic type carries
// no member, so it is not kept.
func (w *storeWalk) store(node storeNode, value storedValue) {
	if value.concrete != nil {
		if _, basic := types.Unalias(value.concrete).(*types.Basic); !basic {
			w.direct[node] = append(w.direct[node], value.concrete)
		}
	}
	if value.from != nil {
		w.link(node, *value.from)
	}
}

// link records that one node holds what another holds.
func (w *storeWalk) link(node, source storeNode) {
	if node != source {
		w.from[node] = append(w.from[node], source)
	}
}

// call records what one call passes for every parameter of its callee that is an
// interface or holds interface elements, other than a variadic one.
func (w *storeWalk) call(call *ast.CallExpr) {
	callee, isFunc := resolveObject(w.info, call.Fun).(*types.Func)
	if !isFunc {
		return
	}
	sig := callee.Signature()
	last := sig.Params().Len() - 1
	for i, arg := range call.Args {
		at, supplies := parameterAt(sig, i)
		if !supplies || (sig.Variadic() && at == last) {
			continue
		}
		t := sig.Params().At(at).Type()
		if holdsInterface(t) {
			w.storeInto(storeNode{at: w.sites.of(callee), param: at}, t, arg)
		}
	}
}

// calleeIdent is the identifier one call's callee expression names it by: the name
// itself, or the selector of a qualified or selected one, through an instantiation.
func calleeIdent(fun ast.Expr) *ast.Ident {
	switch f := ast.Unparen(fun).(type) {
	case *ast.IndexExpr:
		return calleeIdent(f.X)
	case *ast.IndexListExpr:
		return calleeIdent(f.X)
	case *ast.SelectorExpr:
		return f.Sel
	case *ast.Ident:
		return f
	default:
		return nil
	}
}
