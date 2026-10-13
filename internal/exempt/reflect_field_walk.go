package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
)

// reflectFieldReads are the methods of reflect.Value that return a field of the struct
// the value holds.
var reflectFieldReads = map[string]bool{"Field": true, "FieldByIndex": true, "FieldByName": true, "FieldByNameFunc": true}

// The names of the reflection package a field walk is read through.
const (
	reflectValueType = "Value"
	reflectValueOf   = "ValueOf"
	reflectIndirect  = "Indirect"
	reflectElem      = "Elem"
	reflectInterface = "Interface"
)

// walkFieldAssertions records every assertion to an interface type of what Interface
// returns for a reflect.Value holding a field of a struct: the assertion converts the
// field's value, whichever field the index or the name selects, so it retains the
// methods by which each exported field type of the struct implements the interface.
func (f *encodingFlow) walkFieldAssertions() error {
	for _, one := range programFunctions(f.kept.in) {
		if one.decl.Body == nil {
			continue
		}
		walk := reflectWalk{info: one.info, assigned: reflectValueAssignments(one.info, one.decl.Body)}
		var failed error
		ast.Inspect(one.decl.Body, func(n ast.Node) bool {
			if assertion, isAssertion := n.(*ast.TypeAssertExpr); isAssertion && failed == nil {
				failed = f.assertedField(&walk, assertion)
			}
			return failed == nil
		})
		if failed != nil {
			return failed
		}
	}
	return nil
}

// assertedField records what one type assertion retains under [encodingFlow.walkFieldAssertions].
func (f *encodingFlow) assertedField(walk *reflectWalk, assertion *ast.TypeAssertExpr) error {
	if assertion.Type == nil {
		return nil
	}
	asserted := walk.info.TypeOf(assertion.Type)
	if asserted == nil {
		return nil
	}
	iface, isInterface := types.Unalias(asserted).Underlying().(*types.Interface)
	call, isCall := ast.Unparen(assertion.X).(*ast.CallExpr)
	if !isInterface || !isCall {
		return nil
	}
	held, reads := walk.method(call, map[string]bool{reflectInterface: true})
	if !reads {
		return nil
	}
	st, walks := walk.fieldOf(held, 0)
	if !walks {
		return nil
	}
	return f.fieldAssertion(st, iface, types.TypeString(asserted, nil), assertion.Pos())
}

// fieldAssertion records the methods by which each exported field type of one struct
// implements one interface.
func (f *encodingFlow) fieldAssertion(st *types.Struct, iface *types.Interface, name string, at token.Pos) error {
	site, err := f.kept.site(at)
	if err != nil {
		return err
	}
	for field := range st.Fields() {
		if !field.Exported() || !types.Implements(field.Type(), iface) {
			continue
		}
		for m := range iface.Methods() {
			if impl, isFunc := lookupMethod(field.Type(), m); isFunc {
				f.kept.record(impl, site, "converted to "+name+" from a field read by reflection")
			}
		}
	}
	return nil
}

// lookupMethod is the method of t one interface method names.
func lookupMethod(t types.Type, m *types.Func) (*types.Func, bool) {
	obj, _, _ := types.LookupFieldOrMethod(t, true, m.Pkg(), m.Name())
	fn, isFunc := obj.(*types.Func)
	return fn, isFunc
}

// reflectWalk reads the reflect.Value expressions of one function body, each local of
// that type through every value the body assigns to it.
type reflectWalk struct {
	info     *types.Info
	assigned map[*types.Var][]ast.Expr
}

// maxReflectSteps bounds how many locals one reading passes through, which a cycle of
// assignments would otherwise follow without end.
const maxReflectSteps = 8

// fieldOf is the struct whose field one reflect.Value expression holds.
func (w *reflectWalk) fieldOf(expr ast.Expr, steps int) (*types.Struct, bool) {
	if steps > maxReflectSteps {
		return nil, false
	}
	if call, isCall := ast.Unparen(expr).(*ast.CallExpr); isCall {
		recv, reads := w.method(call, reflectFieldReads)
		if !reads {
			return nil, false
		}
		held, known := w.valueOf(recv, steps+1)
		if !known {
			return nil, false
		}
		st, isStruct := types.Unalias(held).Underlying().(*types.Struct)
		return st, isStruct
	}
	for _, value := range w.values(expr) {
		if st, walks := w.fieldOf(value, steps+1); walks {
			return st, true
		}
	}
	return nil, false
}

// valueOf is the static type of what one reflect.Value expression holds: the operand of
// reflect.ValueOf, read through Elem and reflect.Indirect.
func (w *reflectWalk) valueOf(expr ast.Expr, steps int) (types.Type, bool) {
	if steps > maxReflectSteps {
		return nil, false
	}
	call, isCall := ast.Unparen(expr).(*ast.CallExpr)
	if !isCall {
		for _, value := range w.values(expr) {
			if t, known := w.valueOf(value, steps+1); known {
				return t, true
			}
		}
		return nil, false
	}
	if recv, elem := w.method(call, map[string]bool{reflectElem: true}); elem {
		return w.pointedTo(recv, steps)
	}
	fn, isFunc := resolveObject(w.info, call.Fun).(*types.Func)
	if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != reflectPackage || fn.Signature().Recv() != nil || len(call.Args) != 1 {
		return nil, false
	}
	switch fn.Name() {
	case reflectValueOf:
		t := w.info.TypeOf(call.Args[0])
		return t, t != nil && !types.IsInterface(t)
	case reflectIndirect:
		return w.pointedTo(call.Args[0], steps)
	}
	return nil, false
}

// pointedTo is the element type of what one reflect.Value expression holds, where it
// holds a pointer.
func (w *reflectWalk) pointedTo(expr ast.Expr, steps int) (types.Type, bool) {
	held, known := w.valueOf(expr, steps+1)
	if !known {
		return nil, false
	}
	pointer, isPointer := types.Unalias(held).Underlying().(*types.Pointer)
	if !isPointer {
		return nil, false
	}
	return pointer.Elem(), true
}

// method is the receiver of one call of a method of reflect.Value whose name is one of
// names.
func (w *reflectWalk) method(call *ast.CallExpr, names map[string]bool) (ast.Expr, bool) {
	sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !isSelector {
		return nil, false
	}
	fn, isFunc := w.info.Uses[sel.Sel].(*types.Func)
	if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != reflectPackage || !names[fn.Name()] ||
		receiverName(fn.Signature().Recv().Type()) != reflectValueType {
		return nil, false
	}
	return sel.X, true
}

func (w *reflectWalk) values(expr ast.Expr) []ast.Expr {
	ident, isIdent := ast.Unparen(expr).(*ast.Ident)
	if !isIdent {
		return nil
	}
	v, isVar := w.info.Uses[ident].(*types.Var)
	if !isVar {
		return nil
	}
	return w.assigned[v]
}

// reflectValueAssignments is, for every local of type reflect.Value one body declares
// or assigns one value to at a time, the values it is given.
func reflectValueAssignments(info *types.Info, body *ast.BlockStmt) map[*types.Var][]ast.Expr {
	held := make(map[*types.Var][]ast.Expr)
	keep := func(names, values []ast.Expr) {
		if len(names) != len(values) {
			return
		}
		for i, name := range names {
			if v, isValue := reflectValueVar(info, name); isValue {
				held[v] = append(held[v], values[i])
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			keep(n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			names := make([]ast.Expr, len(n.Names))
			for i, name := range n.Names {
				names[i] = name
			}
			keep(names, n.Values)
		}
		return true
	})
	return held
}

// reflectValueVar is the variable of type reflect.Value one name declares or assigns.
func reflectValueVar(info *types.Info, name ast.Expr) (*types.Var, bool) {
	ident, isIdent := name.(*ast.Ident)
	if !isIdent {
		return nil, false
	}
	v, isVar := info.ObjectOf(ident).(*types.Var)
	return v, isVar && receiverName(v.Type()) == reflectValueType && isReflectPackage(v.Type())
}

func isReflectPackage(t types.Type) bool {
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == reflectPackage
}
