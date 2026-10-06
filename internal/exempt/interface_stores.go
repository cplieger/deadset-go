package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
)

// storeNode is one place the analysed program stores interface values in, keyed by
// where it is written: a field, a variable or a composite literal, or one parameter
// of one function, which holds every argument the program passes for it.
type storeNode struct {
	at    token.Position
	param int // the parameter's index, or placeNode
}

// placeNode marks a node that is no parameter.
const placeNode = -1

// storedTypes is, per place of the analysed program that is an interface or holds one
// as its element or map value, the concrete types the program stores in it. A place
// also holds what every place it is filled from holds, which [storedTypes.reaching]
// resolves on first use.
type storedTypes struct {
	sites  *declarationSites
	direct map[storeNode][]types.Type
	from   map[storeNode][]storeNode
	memo   map[storeNode][]types.Type
}

// storedValue is what one expression carries into a place: a concrete type, or the
// node whose values it carries.
type storedValue struct {
	concrete types.Type
	from     *storeNode
}

// storeWalk reads the stores of one declaration against the function that encloses
// them.
type storeWalk struct {
	*storedTypes
	info *types.Info
	fn   *types.Func
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
		sites:  sites,
		direct: make(map[storeNode][]types.Type),
		from:   make(map[storeNode][]storeNode),
		memo:   make(map[storeNode][]types.Type),
	}
	for _, p := range programPackages(in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				w := &storeWalk{storedTypes: s, info: p.TypesInfo, fn: enclosingFunc(p.TypesInfo, decl)}
				ast.Inspect(decl, w.visit)
			}
		}
	}
	return s
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
	}
	return true
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
	if (s.Tok != token.ASSIGN && s.Tok != token.DEFINE) || len(s.Lhs) != len(s.Rhs) {
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
// type, or the interface-typed parameter of the enclosing function it names.
func (w *storeWalk) valueOf(expr ast.Expr) storedValue {
	t := w.info.TypeOf(expr)
	if t == nil {
		return storedValue{}
	}
	if !types.IsInterface(t) {
		return storedValue{concrete: t}
	}
	if node, isParam := w.parameter(parameterNamed(w.info, expr)); isParam {
		return storedValue{from: &node}
	}
	return storedValue{}
}

// store records one value stored in a node. A value of an unnamed basic type carries
// no member, so it is not kept.
func (w *storeWalk) store(node storeNode, value storedValue) {
	switch {
	case value.concrete != nil:
		if _, basic := types.Unalias(value.concrete).(*types.Basic); !basic {
			w.direct[node] = append(w.direct[node], value.concrete)
		}
	case value.from != nil:
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
