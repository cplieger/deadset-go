package exempt

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
)

// unreadTemplateCalls are the methods of a template engine that add to a set what
// no template text the analysis reads holds: a parse tree built elsewhere, or
// delimiters the reads do not parse with.
var unreadTemplateCalls = map[string]bool{"AddParseTree": true, "Delims": true}

// templateParsePackage is the package whose trees an engine's template holds.
const templateParsePackage = "text/template/parse"

// everyParse is every identifier the templates the program parses with the engine
// at path name, nil unless the analysis reads every one ([inventory.parse]) and no
// template can come from elsewhere: no tree added, no delimiters set, no parse method
// named but to call it, no template or parse-tree field written, no assertion to a
// type holding a template, and no outside package's API mentioning the template
// type. Every template any execution of the engine runs is then one of these.
func (o *templateOrigins) everyParse(engine string) map[string]bool {
	inv, read := o.every[engine]
	if !read {
		inv = o.readEveryParse(engine)
		o.every[engine] = inv
	}
	if !inv.complete || inv.parsed == 0 {
		return nil
	}
	return inv.names
}

func (o *templateOrigins) readEveryParse(engine string) *templateSet {
	inv := o.inventory()
	if outsideMentionsTemplate(o.in, engine) {
		inv.complete = false
		return inv
	}
	for _, p := range programPackages(o.in.Result) {
		if p.TypesInfo != nil && inv.complete {
			o.readPackage(p.TypesInfo, p.Syntax, engine, inv)
		}
	}
	return inv
}

// readPackage reads one package for [templateOrigins.everyParse]: every node of its
// files, then every name of a parse or unread method of the engine it uses but calls.
func (o *templateOrigins) readPackage(info *types.Info, files []*ast.File, engine string, inv *templateSet) {
	called := make(map[*ast.Ident]bool)
	for _, file := range files {
		dir := filepath.Dir(o.fset.Position(file.FileStart).Filename)
		ast.Inspect(file, func(n ast.Node) bool {
			if inv.complete {
				o.readNode(info, dir, engine, n, inv, called)
			}
			return inv.complete
		})
	}
	for id, obj := range info.Uses {
		if fn, isFunc := obj.(*types.Func); isFunc && isEngineFunc(fn, engine) &&
			(templateParses[fn.Name()] || unreadTemplateCalls[fn.Name()]) && !called[id] {
			inv.complete = false
		}
	}
}

// readNode reads one node of the program for [templateOrigins.everyParse].
func (o *templateOrigins) readNode(info *types.Info, dir, engine string, n ast.Node, inv *templateSet, called map[*ast.Ident]bool) {
	switch n := n.(type) {
	case *ast.CallExpr:
		o.readCall(info, dir, engine, n, inv, called)
	case *ast.TypeAssertExpr:
		if n.Type != nil && mentionsTemplate(info.TypeOf(n.Type), engine, map[types.Type]bool{}) {
			inv.complete = false
		}
	case *ast.CaseClause:
		for _, e := range n.List {
			if tv, typed := info.Types[e]; typed && tv.IsType() && mentionsTemplate(tv.Type, engine, map[types.Type]bool{}) {
				inv.complete = false
			}
		}
	case *ast.AssignStmt:
		for _, lhs := range n.Lhs {
			if writesTemplateField(info, lhs) {
				inv.complete = false
			}
		}
	}
}

// readCall reads one call of a function or method of the engine.
func (*templateOrigins) readCall(info *types.Info, dir, engine string, call *ast.CallExpr, inv *templateSet, called map[*ast.Ident]bool) {
	fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc || !isEngineFunc(fn, engine) {
		return
	}
	if id := calledIdent(call.Fun); id != nil {
		called[id] = true
	}
	switch {
	case unreadTemplateCalls[fn.Name()]:
		inv.complete = false
	case templateParses[fn.Name()]:
		inv.parse(info, dir, engine, fn.Name(), call)
	}
}

// writesTemplateField reports whether one assignment target is a field of an
// engine's template or of a parse tree.
func writesTemplateField(info *types.Info, lhs ast.Expr) bool {
	sel, isSelector := ast.Unparen(lhs).(*ast.SelectorExpr)
	if !isSelector {
		return false
	}
	field, isVar := info.Uses[sel.Sel].(*types.Var)
	return isVar && field.IsField() && field.Pkg() != nil &&
		(templatePackages[field.Pkg().Path()] || field.Pkg().Path() == templateParsePackage)
}

// calledIdent is the identifier naming the function a call's callee expression
// names.
func calledIdent(fun ast.Expr) *ast.Ident {
	switch e := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return e
	case *ast.SelectorExpr:
		return e.Sel
	case *ast.IndexExpr:
		return calledIdent(e.X)
	case *ast.IndexListExpr:
		return calledIdent(e.X)
	}
	return nil
}

// isEngineFunc reports whether fn is a function or method of the engine at path.
func isEngineFunc(fn *types.Func, engine string) bool {
	return fn.Pkg() != nil && fn.Pkg().Path() == engine
}

// outsideMentionsTemplate reports whether a package outside the program that the
// program imports, directly or not, mentions the template type of the engine at
// path in its exported API, through which the program could receive a template it
// does not parse. The engines and their parse package are the template's own.
func outsideMentionsTemplate(in *Input, engine string) bool {
	seen := make(map[types.Type]bool)
	for _, pkg := range outsideClosure(in) {
		if templatePackages[pkg.Path()] || pkg.Path() == templateParsePackage {
			continue
		}
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if _, isConst := obj.(*types.Const); isConst || !obj.Exported() {
				continue
			}
			if mentionsTemplate(obj.Type(), engine, seen) {
				return true
			}
		}
	}
	return false
}

// mentionsTemplate reports whether t is or holds the engine's template type: an
// element, a field, a parameter or result, a method of a defined type or an
// interface, or a type argument. A type already in seen is not walked again.
func mentionsTemplate(t types.Type, engine string, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	held := func(inner types.Type) bool { return mentionsTemplate(inner, engine, seen) }
	switch t := t.(type) {
	case *types.Alias:
		return held(types.Unalias(t))
	case *types.Named:
		return namedMentions(t, engine, held)
	case *types.Map:
		return held(t.Key()) || held(t.Elem())
	case interface{ Elem() types.Type }:
		return held(t.Elem())
	case *types.Signature:
		return held(t.Params()) || held(t.Results())
	case *types.Tuple:
		return slices.ContainsFunc(slices.Collect(t.Variables()), func(v *types.Var) bool { return held(v.Type()) })
	case *types.Struct:
		return slices.ContainsFunc(slices.Collect(t.Fields()), func(v *types.Var) bool {
			return (v.Exported() || v.Embedded()) && held(v.Type())
		})
	case *types.Interface:
		return slices.ContainsFunc(slices.Collect(t.Methods()), func(m *types.Func) bool { return held(m.Type()) })
	}
	return false
}

// namedMentions is [mentionsTemplate] for a defined type: the engine's template
// itself, a type argument, an exported method or the underlying type holding it.
func namedMentions(t *types.Named, engine string, held func(types.Type) bool) bool {
	if obj := t.Obj(); obj.Pkg() != nil && obj.Pkg().Path() == engine && obj.Name() == "Template" {
		return true
	}
	return slices.ContainsFunc(slices.Collect(t.TypeArgs().Types()), held) ||
		slices.ContainsFunc(slices.Collect(t.Methods()), func(m *types.Func) bool { return m.Exported() && held(m.Type()) }) ||
		held(t.Underlying())
}
