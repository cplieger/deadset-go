package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
)

// storedTypes is, per field of the analysed program that is an interface or holds
// one as its element or map value, the concrete types the program stores in it.
type storedTypes struct {
	sites *declarationSites
	held  map[token.Position][]types.Type
}

// of is the types the program stores in one field, and nil for a field that holds
// no interface.
func (s *storedTypes) of(field *types.Var) []types.Type {
	if s == nil || !holdsInterface(field.Type()) {
		return nil
	}
	return s.held[s.sites.of(field)]
}

// holdsInterface reports whether a field of type t stores interface values: t is an
// interface, or a slice, an array or a map whose element is one.
func holdsInterface(t types.Type) bool {
	if types.IsInterface(t) {
		return true
	}
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

// parameterKey names one parameter of one function of the program.
type parameterKey struct {
	fn token.Position
	at int
}

// storedValue is what one expression stored in an interface-typed place carries: a
// concrete type, or the interface-typed parameter of the enclosing function it
// names, which stands for every argument a call passes for it.
type storedValue struct {
	concrete types.Type
	param    *parameterKey
}

// storeWalk accumulates the stores of the analysed program, then resolves every
// stored parameter through the calls that pass it.
type storeWalk struct {
	sites   *declarationSites
	held    map[token.Position][]types.Type
	seen    map[token.Position]map[string]bool
	pending map[parameterKey][]token.Position
	calls   map[token.Position][]passedArgument
}

// passedArgument is one argument a call passes for an interface-typed parameter.
type passedArgument struct {
	value storedValue
	at    int
}

// storedTypesOf reads every store of the analysed program into a field that holds
// an interface: the field's value in a composite literal, the right side of an
// assignment to the field, an element of a composite literal of its slice, array or
// map type, an argument of append onto it, and the right side of an assignment to an
// index of it. A stored interface-typed parameter stands for every argument the
// program's calls pass for it, to a fixpoint, and one no call passes adds nothing.
func storedTypesOf(in *Input, sites *declarationSites) *storedTypes {
	w := &storeWalk{
		sites:   sites,
		held:    make(map[token.Position][]types.Type),
		seen:    make(map[token.Position]map[string]bool),
		pending: make(map[parameterKey][]token.Position),
		calls:   make(map[token.Position][]passedArgument),
	}
	for _, p := range programPackages(in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
			w.file(p.TypesInfo, file)
		}
	}
	w.resolve()
	return &storedTypes{sites: sites, held: w.held}
}

// file reads the stores and the calls of one file, each against the function that
// encloses it.
func (w *storeWalk) file(info *types.Info, file *ast.File) {
	for _, decl := range file.Decls {
		var fn *types.Func
		if fd, isFunc := decl.(*ast.FuncDecl); isFunc {
			fn, _ = info.Defs[fd.Name].(*types.Func)
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				w.compositeLit(info, n, fn)
			case *ast.AssignStmt:
				w.assign(info, n, fn)
			case *ast.CallExpr:
				w.call(info, n, fn)
			}
			return true
		})
	}
}

// compositeLit reads the fields one struct literal sets.
func (w *storeWalk) compositeLit(info *types.Info, lit *ast.CompositeLit, fn *types.Func) {
	t := info.TypeOf(lit)
	if t == nil {
		return
	}
	st, isStruct := types.Unalias(t).Underlying().(*types.Struct)
	if !isStruct {
		return
	}
	for i, elt := range lit.Elts {
		if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
			if key, isIdent := kv.Key.(*ast.Ident); isIdent {
				if field, isVar := info.Uses[key].(*types.Var); isVar && field.IsField() {
					w.storeInto(info, field, kv.Value, fn)
				}
			}
			continue
		}
		if i < st.NumFields() {
			w.storeInto(info, st.Field(i), elt, fn)
		}
	}
}

// assign reads the stores one assignment makes into a field, or into an index of
// one.
func (w *storeWalk) assign(info *types.Info, s *ast.AssignStmt, fn *types.Func) {
	if s.Tok != token.ASSIGN || len(s.Lhs) != len(s.Rhs) {
		return
	}
	for i, lhs := range s.Lhs {
		switch target := ast.Unparen(lhs).(type) {
		case *ast.SelectorExpr:
			if field := selectedField(info, target); field != nil {
				w.storeInto(info, field, s.Rhs[i], fn)
			}
		case *ast.IndexExpr:
			sel, isSelector := ast.Unparen(target.X).(*ast.SelectorExpr)
			if !isSelector {
				continue
			}
			if field := selectedField(info, sel); field != nil && !types.IsInterface(field.Type()) && holdsInterface(field.Type()) {
				w.store(field, w.valueOf(info, s.Rhs[i], fn))
			}
		}
	}
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

// storeInto reads what one value stored as a field's value puts there: the value
// itself for an interface field, and the elements of a literal or the appended
// arguments for a field holding interface elements.
func (w *storeWalk) storeInto(info *types.Info, field *types.Var, value ast.Expr, fn *types.Func) {
	if types.IsInterface(field.Type()) {
		w.store(field, w.valueOf(info, value, fn))
		return
	}
	if !holdsInterface(field.Type()) {
		return
	}
	switch v := ast.Unparen(value).(type) {
	case *ast.CompositeLit:
		for _, elt := range v.Elts {
			if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
				elt = kv.Value
			}
			w.store(field, w.valueOf(info, elt, fn))
		}
	case *ast.CallExpr:
		if !isAppend(info, v) || v.Ellipsis.IsValid() {
			return
		}
		for _, arg := range v.Args[1:] {
			w.store(field, w.valueOf(info, arg, fn))
		}
	}
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

// valueOf is what one expression carries into an interface-typed place.
func (w *storeWalk) valueOf(info *types.Info, expr ast.Expr, fn *types.Func) storedValue {
	t := info.TypeOf(expr)
	if t == nil {
		return storedValue{}
	}
	if !types.IsInterface(t) {
		return storedValue{concrete: t}
	}
	if fn == nil {
		return storedValue{}
	}
	named := parameterNamed(info, expr)
	sig := fn.Signature()
	for i := range sig.Params().Len() {
		if sig.Params().At(i) == named && (!sig.Variadic() || i < sig.Params().Len()-1) {
			return storedValue{param: &parameterKey{fn: w.sites.of(fn), at: i}}
		}
	}
	return storedValue{}
}

// store records one value stored in a field.
func (w *storeWalk) store(field *types.Var, value storedValue) {
	at := w.sites.of(field)
	switch {
	case value.concrete != nil:
		w.add(at, value.concrete)
	case value.param != nil:
		w.pending[*value.param] = append(w.pending[*value.param], at)
	}
}

// add records one concrete type stored in the field at one key, once.
func (w *storeWalk) add(at token.Position, t types.Type) {
	key := types.TypeString(t, nil)
	if w.seen[at] == nil {
		w.seen[at] = make(map[string]bool)
	}
	if !w.seen[at][key] {
		w.seen[at][key] = true
		w.held[at] = append(w.held[at], t)
	}
}

// call records every argument one call passes for an interface-typed parameter
// other than a variadic one.
func (w *storeWalk) call(info *types.Info, call *ast.CallExpr, fn *types.Func) {
	callee, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc {
		return
	}
	sig := callee.Signature()
	last := sig.Params().Len() - 1
	for i, arg := range call.Args {
		at, supplies := parameterAt(sig, i)
		if !supplies || (sig.Variadic() && at == last) || !types.IsInterface(sig.Params().At(at).Type()) {
			continue
		}
		key := w.sites.of(callee)
		w.calls[key] = append(w.calls[key], passedArgument{at: at, value: w.valueOf(info, arg, fn)})
	}
}

// resolve carries every stored parameter to the arguments the program passes for
// it, and those that are parameters in turn, until no field gains a source.
func (w *storeWalk) resolve() {
	type reached struct {
		field token.Position
		param parameterKey
	}
	done := make(map[reached]bool)
	var queue []reached
	for param, fields := range w.pending {
		for _, field := range fields {
			queue = append(queue, reached{param: param, field: field})
		}
	}
	for len(queue) > 0 {
		one := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if done[one] {
			continue
		}
		done[one] = true
		for _, arg := range w.calls[one.param.fn] {
			switch {
			case arg.at != one.param.at:
			case arg.value.concrete != nil:
				w.add(one.field, arg.value.concrete)
			case arg.value.param != nil:
				queue = append(queue, reached{param: *arg.value.param, field: one.field})
			}
		}
	}
}
