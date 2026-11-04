package exempt

import (
	"go/ast"
	"go/constant"
	"go/types"
)

// enabledSwitches is the decoder packages ([strictDecoders]) one of whose switches
// some file of the analysed program enables ([enablingSwitch]).
func enabledSwitches(in *Program) map[string]bool {
	enabled := make(map[string]bool)
	for _, p := range in.Facts.Program() {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
			called := make(map[*ast.Ident]bool)
			ast.Inspect(file, func(n ast.Node) bool {
				if path, enabling := enablingSwitch(p.TypesInfo, n, called); enabling {
					enabled[path] = true
				}
				return true
			})
		}
	}
	return enabled
}

// enablingSwitch is the package of the switch one node enables: a call of one that
// [enables] it, or any other use of one, a method value of a switch taking a flag
// included, since nothing shows that flag false. called holds the names of the
// switch calls met so far, which a walk in source order meets before their names.
func enablingSwitch(info *types.Info, n ast.Node, called map[*ast.Ident]bool) (string, bool) {
	switch e := n.(type) {
	case *ast.CallExpr:
		sw, path, isSwitch := strictSwitch(info, e.Fun)
		if !isSwitch {
			break
		}
		called[calleeName(e.Fun)] = true
		if enables(info, e, sw) {
			return path, true
		}
	case *ast.Ident:
		if sw, path, isSwitch := switchObject(info.Uses[e]); isSwitch && (flag(sw) == nil || !called[e]) {
			return path, true
		}
	}
	return "", false
}

// calleeName is the name a call's function expression ends in, nil for any other.
func calleeName(fun ast.Expr) *ast.Ident {
	switch e := ast.Unparen(fun).(type) {
	case *ast.SelectorExpr:
		return e.Sel
	case *ast.Ident:
		return e
	}
	return nil
}

// strictSwitch is the switch an expression names, with its package's path.
func strictSwitch(info *types.Info, fun ast.Expr) (*types.Func, string, bool) {
	return switchObject(resolveObject(info, fun))
}

// switchObject is obj as a switch of a decoder package, with that package's path.
func switchObject(obj types.Object) (*types.Func, string, bool) {
	fn, isFunc := obj.(*types.Func)
	if !isFunc || fn.Pkg() == nil || !strictDecoders[fn.Pkg().Path()].switches[fn.Name()] {
		return nil, "", false
	}
	return fn, fn.Pkg().Path(), true
}

// flag is the one boolean parameter a switch takes, and nil for a switch that enables
// by being named.
func flag(sw *types.Func) *types.Var {
	params := sw.Signature().Params()
	if params.Len() != 1 {
		return nil
	}
	if basic, isBasic := params.At(0).Type().Underlying().(*types.Basic); isBasic && basic.Kind() == types.Bool {
		return params.At(0)
	}
	return nil
}

// enables reports whether one call of a switch enables it: every call but one passing
// its flag the constant false, because a flag the program computes may be true. The
// flag is the last argument, which a method expression's call passes after the decoder.
func enables(info *types.Info, call *ast.CallExpr, sw *types.Func) bool {
	if flag(sw) == nil {
		return true
	}
	if len(call.Args) == 0 {
		return false
	}
	value := info.Types[call.Args[len(call.Args)-1]].Value
	return value == nil || value.Kind() != constant.Bool || constant.BoolVal(value)
}

// narrowStrict drops the strict read of a decoding entry point of a decoder package
// where the call at hand decodes through no decoder that took a switch
// ([encodingFlow.strictAt]); a named destination's entry point then reads no field.
func (f *encodingFlow) narrowStrict(fn *types.Func, info *types.Info, scope ast.Node, call *ast.CallExpr, r *reach) {
	if !r.strict || call == nil || info == nil || fn.Pkg() == nil || strictDecoders[fn.Pkg().Path()].switches == nil ||
		f.strictAt(fn, info, scope, call) {
		return
	}
	r.strict = false
	if namedDestination(fn.Pkg().Path()) {
		r.fields = false
	}
}

// strictAt reports whether one call of a decoding entry point fn, written in scope,
// decodes strictly: fn is a switch itself, an option the call passes enables a switch
// of fn's package, or the decoder the method is called on took one. Where the call
// passes an option, or runs through a decoder, the analysis cannot follow in scope,
// it is strict if the program enables a switch of the package anywhere.
func (f *encodingFlow) strictAt(fn *types.Func, info *types.Info, scope ast.Node, call *ast.CallExpr) bool {
	path := fn.Pkg().Path()
	if strictDecoders[path].switches[fn.Name()] {
		return true
	}
	enabled, followed := optionsEnable(info, fn.Signature(), call, path)
	if sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr); isSelector && fn.Signature().Recv() != nil {
		took, seen := decoderEnables(info, scope, sel.X, path)
		enabled, followed = enabled || took, followed && seen
	}
	return enabled || !followed && f.strict[path]
}

// optionsEnable reports whether an option one call passes in its variadic parameter
// enables a switch of the package at path, and whether every option is one the
// analysis follows: a constant, a function of that package named, or a call of one
// over options it follows.
func optionsEnable(info *types.Info, sig *types.Signature, call *ast.CallExpr, path string) (enabled, followed bool) {
	if !sig.Variadic() {
		return false, true
	}
	if call.Ellipsis.IsValid() {
		return false, false
	}
	at := sig.Params().Len() - 1
	if len(call.Args) <= at {
		return false, true
	}
	return argumentsEnable(info, call.Args[at:], path)
}

// argumentsEnable is [optionsEnable] for a list of option expressions.
func argumentsEnable(info *types.Info, args []ast.Expr, path string) (enabled, followed bool) {
	followed = true
	for _, arg := range args {
		if info.Types[arg].Value != nil {
			continue
		}
		var on, seen bool
		switch e := ast.Unparen(arg).(type) {
		case *ast.CallExpr:
			fn, isFunc := resolveObject(info, e.Fun).(*types.Func)
			switch {
			case !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != path:
			case strictDecoders[path].switches[fn.Name()]:
				on, seen = enables(info, e, fn), true
			default:
				on, seen = argumentsEnable(info, e.Args, path)
			}
		case *ast.Ident, *ast.SelectorExpr:
			fn, isFunc := resolveObject(info, e).(*types.Func)
			on, seen = isFunc && strictDecoders[path].switches[fn.Name()], isFunc && fn.Pkg() != nil && fn.Pkg().Path() == path
		}
		enabled, followed = enabled || on, followed && seen
	}
	return enabled, followed
}

// decoderEnables reports whether the decoder recv names took a switch of the package
// at path, and whether the analysis follows every way it is made and used: made by a
// call of that package, or held by a local variable of scope that only such calls
// give a value and that is used only to call a method on.
func decoderEnables(info *types.Info, scope ast.Node, recv ast.Expr, path string) (enabled, followed bool) {
	recv = ast.Unparen(recv)
	if call, isCall := recv.(*ast.CallExpr); isCall {
		return constructorEnables(info, call, path)
	}
	v := localVariable(info, recv)
	if v == nil || scope == nil {
		return false, false
	}
	followed, assigned := true, false
	var stack []ast.Node
	ast.Inspect(scope, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		if use, isIdent := n.(*ast.Ident); isIdent && (info.Uses[use] == v || info.Defs[use] == v) {
			on, seen, gives := variableUse(info, stack, use, path)
			enabled, followed, assigned = enabled || on, followed && seen, assigned || gives
		}
		return true
	})
	return enabled, followed && assigned
}

// localVariable is the local variable expr names, nil where it names none.
func localVariable(info *types.Info, expr ast.Expr) *types.Var {
	id, isIdent := expr.(*ast.Ident)
	if !isIdent {
		return nil
	}
	v, isVar := info.Uses[id].(*types.Var)
	if !isVar || v.IsField() || v.Pkg() == nil || v.Parent() == nil || v.Parent() == v.Pkg().Scope() {
		return nil
	}
	return v
}

// variableUse reads one occurrence of a decoder's local variable, stack the nodes from
// the scope down to it: a method called on it, where it enables a switch it names,
// or an assignment giving it a value, which [constructorEnables] reads. Any other
// occurrence is one the analysis does not follow.
func variableUse(info *types.Info, stack []ast.Node, use *ast.Ident, path string) (enabled, followed, gives bool) {
	parent := stack[len(stack)-2]
	switch p := parent.(type) {
	case *ast.SelectorExpr:
		call, calls := callOf(stack[:len(stack)-1])
		if !calls || p.X != use {
			return false, false, false
		}
		sw, _, isSwitch := strictSwitch(info, p)
		return isSwitch && enables(info, call, sw), true, false
	case *ast.AssignStmt:
		if k := identIndex(p.Lhs, use); k >= 0 && len(p.Lhs) == len(p.Rhs) {
			return valueEnables(info, p.Rhs[k], path)
		}
	case *ast.ValueSpec:
		if k := identIndex(p.Names, use); k >= 0 && len(p.Values) == 0 {
			return false, true, false
		} else if k >= 0 && len(p.Names) == len(p.Values) {
			return valueEnables(info, p.Values[k], path)
		}
	}
	return false, false, false
}

// valueEnables is [constructorEnables] for a value a decoder's variable is given, a
// value of any other expression being one the analysis does not follow.
func valueEnables(info *types.Info, value ast.Expr, path string) (enabled, followed, gives bool) {
	call, isCall := ast.Unparen(value).(*ast.CallExpr)
	if !isCall {
		return false, false, true
	}
	enabled, followed = constructorEnables(info, call, path)
	return enabled, followed, true
}

// constructorEnables reports whether a call making a decoder of the package at path
// passes an option enabling one of its switches, and whether the analysis follows the
// call: a function of that package, over options it follows.
func constructorEnables(info *types.Info, call *ast.CallExpr, path string) (enabled, followed bool) {
	fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != path || fn.Signature().Recv() != nil {
		return false, false
	}
	return optionsEnable(info, fn.Signature(), call, path)
}

// callOf is the call whose function the last node of stack is, outside parentheses.
func callOf(stack []ast.Node) (*ast.CallExpr, bool) {
	fun := stack[len(stack)-1]
	for i := len(stack) - 2; i >= 0; i-- {
		if paren, isParen := stack[i].(*ast.ParenExpr); isParen {
			fun = paren
			continue
		}
		call, isCall := stack[i].(*ast.CallExpr)
		return call, isCall && call.Fun == fun
	}
	return nil, false
}

// identIndex is the position of id among exprs or names, -1 where it is none.
func identIndex[E ast.Node](list []E, id *ast.Ident) int {
	for k, e := range list {
		if ast.Node(e) == ast.Node(id) {
			return k
		}
	}
	return -1
}
