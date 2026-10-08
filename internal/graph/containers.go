package graph

import (
	"go/ast"
	"go/types"
)

// standardContainers are the standard-library functions that store or compare the
// values they are given and hand them back to the program unchanged, by package and
// receiver type, each with the argument positions it compares as a map key is: a
// key, and the old value a compare-and-swap or compare-and-delete compares.
var standardContainers = map[[2]string]map[string][]int{
	{"sync", "Map"}: {
		"Clear": nil, "CompareAndDelete": {0, 1}, "CompareAndSwap": {0, 1}, "Delete": {0}, "Load": {0},
		"LoadAndDelete": {0}, "LoadOrStore": {0}, "Range": nil, "Store": {0}, "Swap": {0},
	},
	{"sync/atomic", "Value"}: {"CompareAndSwap": {0}, "Load": nil, "Store": nil, "Swap": nil},
	{"context", ""}:          {"WithValue": {1}},
}

// StandardContainer reports whether fn is a method of sync.Map, a method of
// atomic.Value or context.WithValue, and returns the argument positions it compares
// as a map key is.
func StandardContainer(fn *types.Func) (compared []int, isContainer bool) {
	if fn == nil || fn.Pkg() == nil {
		return nil, false
	}
	receiver := ""
	if recv := fn.Signature().Recv(); recv != nil {
		t := types.Unalias(recv.Type())
		if pointer, isPointer := t.(*types.Pointer); isPointer {
			t = types.Unalias(pointer.Elem())
		}
		named, isNamed := t.(*types.Named)
		if !isNamed {
			return nil, false
		}
		receiver = named.Obj().Name()
	}
	compared, isContainer = standardContainers[[2]string{fn.Pkg().Path(), receiver}][fn.Name()]
	return compared, isContainer
}

// readContainerKeys records the fields one call to a standard container reads by
// comparing an argument as a map key.
func (p *referencePass) readContainerKeys(call *ast.CallExpr, encl SymbolID) {
	sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	var fn *types.Func
	shift := 0
	switch {
	case isSelector:
		fn, _ = p.info.Uses[sel.Sel].(*types.Func)
		if selection, selected := p.info.Selections[sel]; selected && selection.Kind() == types.MethodExpr {
			shift = 1
		}
	default:
		if id, isIdent := ast.Unparen(call.Fun).(*ast.Ident); isIdent {
			fn, _ = p.info.Uses[id].(*types.Func)
		}
	}
	compared, isContainer := StandardContainer(fn)
	if !isContainer {
		return
	}
	for _, at := range compared {
		if at+shift < len(call.Args) {
			arg := call.Args[at+shift]
			p.readComparison(encl, arg.Pos(), arg)
		}
	}
}
