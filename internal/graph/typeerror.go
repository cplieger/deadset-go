package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// TypeErrorSkip is one type error the compiler reported in the target and the
// declaration it skipped: the target-relative file, the line of the error, the
// compiler's message on one line, and the first and last line of the top-level
// declaration that holds the error, zero where no declaration does.
type TypeErrorSkip struct {
	Path     string
	Message  string
	Line     int
	FromLine int
	ToLine   int
}

// skippedDecl is one type error of a target package with the top-level declaration
// whose source holds its position, nil where none does.
type skippedDecl struct {
	pkg  *packages.Package
	decl ast.Decl
	err  types.Error
}

// skippedDecls pairs every type error of the target's packages with its declaration.
func skippedDecls(r *load.Result) []skippedDecl {
	var held []skippedDecl
	for _, p := range r.Packages {
		if p.Module == nil || !p.Module.Main || len(p.TypeErrors) == 0 {
			continue
		}
		for _, e := range p.TypeErrors {
			held = append(held, skippedDecl{pkg: p, decl: enclosingDecl(p.Syntax, e.Pos), err: e})
		}
	}
	return held
}

// enclosingDecl is the top-level declaration whose source holds pos.
func enclosingDecl(files []*ast.File, pos token.Pos) ast.Decl {
	for _, f := range files {
		if pos < f.FileStart || pos > f.FileEnd {
			continue
		}
		for _, d := range f.Decls {
			if d.Pos() <= pos && pos < d.End() {
				return d
			}
		}
	}
	return nil
}

// TypeErrorSkips lists every type error of one configuration's target, each with
// the declaration it skipped. A package and its test variant check one file twice,
// so an error may be listed twice; the report holds each once.
func TypeErrorSkips(r *load.Result, targetRoot string) []TypeErrorSkip {
	pos := newPositions(r.Fset, targetRoot, nil)
	var held []TypeErrorSkip
	for _, one := range skippedDecls(r) {
		at := r.Fset.Position(one.err.Pos)
		path := pos.relative(at.Filename)
		if path == "" {
			continue
		}
		skip := TypeErrorSkip{Path: path, Line: at.Line, Message: strings.Join(strings.Fields(one.err.Msg), " ")}
		if one.decl != nil {
			skip.FromLine = r.Fset.Position(one.decl.Pos()).Line
			skip.ToLine = r.Fset.Position(one.decl.End()).Line
		}
		held = append(held, skip)
	}
	return held
}

// typeErrors roots every declaration a type error skipped, because the skip only
// withholds: nothing is reported about the declaration and every reference in it
// the compiler resolved still counts. A selector the compiler could not resolve on
// an operand whose type it knows could have named any member of that type, and a
// value reaching a position whose type it could not resolve could reach an
// interface, so every method and field of the type is rooted too.
func (d *rootDetection) typeErrors(r *load.Result) {
	for _, one := range skippedDecls(r) {
		if !d.rootDeclared(one.decl) {
			continue
		}
		if info := one.pkg.TypesInfo; info != nil {
			d.rootUnresolvedSelections(info, one.decl)
			d.rootUnresolvedDestinations(info, one.decl, nil)
		}
	}
}

// rootDeclared roots every name one declaration declares, and reports false for a
// node that is not a declaration.
func (d *rootDetection) rootDeclared(node ast.Node) bool {
	switch decl := node.(type) {
	case *ast.FuncDecl:
		d.addIfInside(decl.Name.Pos())
	case *ast.GenDecl:
		for _, spec := range decl.Specs {
			switch spec := spec.(type) {
			case *ast.ValueSpec:
				for _, name := range spec.Names {
					d.addIfInside(name.Pos())
				}
			case *ast.TypeSpec:
				d.addIfInside(spec.Name.Pos())
			}
		}
	default:
		return false
	}
	return true
}

// rootUnresolvedSelections roots every member of the operand type of each selector
// inside node the compiler could not resolve.
func (d *rootDetection) rootUnresolvedSelections(info *types.Info, node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || info.Selections[sel] != nil || info.Uses[sel.Sel] != nil {
			return true
		}
		if operand, known := info.Types[sel.X]; known && operand.Type != nil {
			d.rootMembers(operand.Type)
		}
		return true
	})
}

// rootUnresolvedDestinations roots every member of the type of each value inside
// node that reaches a result, an argument, an assigned name, a declared variable
// or a composite literal element whose type the compiler could not resolve.
func (d *rootDetection) rootUnresolvedDestinations(info *types.Info, node ast.Node, results []types.Type) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body != nil {
				d.rootUnresolvedDestinations(info, n.Body, resultsOf(info.TypeOf(n.Name)))
			}
			return false
		case *ast.FuncLit:
			d.rootUnresolvedDestinations(info, n.Body, resultsOf(info.TypeOf(n)))
			return false
		case *ast.ReturnStmt:
			d.rootInto(info, results, n.Results)
		case *ast.CallExpr:
			d.rootArguments(info, n)
		case *ast.AssignStmt:
			d.rootInto(info, typesOf(info, n.Lhs), n.Rhs)
		case *ast.ValueSpec:
			if n.Type != nil {
				d.rootInto(info, slices.Repeat([]types.Type{info.TypeOf(n.Type)}, len(n.Values)), n.Values)
			}
		case *ast.CompositeLit:
			d.rootElements(info, n)
		}
		return true
	})
}

// rootElements roots the members of each element of a composite literal whose
// type, or whose element type, the compiler could not resolve.
func (d *rootDetection) rootElements(info *types.Info, lit *ast.CompositeLit) {
	if t := info.TypeOf(lit); !unresolved(t) && !unresolved(elementOf(t)) {
		return
	}
	for _, elt := range lit.Elts {
		if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
			elt = kv.Value
		}
		d.rootReached(info.TypeOf(elt))
	}
}

// rootArguments roots the members of each argument a call passes to a parameter,
// or to a function, whose type the compiler could not resolve.
func (d *rootDetection) rootArguments(info *types.Info, call *ast.CallExpr) {
	fun := info.TypeOf(call.Fun)
	if tv, known := info.Types[call.Fun]; known && tv.IsType() {
		d.rootInto(info, []types.Type{fun}, call.Args)
		return
	}
	sig, isSignature := types.Unalias(fun).(*types.Signature)
	for i, arg := range call.Args {
		if fun == nil || unresolved(fun) || isSignature && unresolved(parameterType(sig, i, call.Ellipsis.IsValid())) {
			d.rootReached(info.TypeOf(arg))
		}
	}
}

// rootInto roots the members of each value whose destination type is unresolved,
// pairing destinations and values one to one.
func (d *rootDetection) rootInto(info *types.Info, destinations []types.Type, values []ast.Expr) {
	if len(destinations) != len(values) {
		return
	}
	for i, value := range values {
		if unresolved(destinations[i]) { //nolint:gosec // G602: the guard above makes both lengths equal
			d.rootReached(info.TypeOf(value))
		}
	}
}

// elementOf is the element type of a slice, an array or a map, nil for another type.
func elementOf(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	switch u := t.Underlying().(type) {
	case *types.Slice:
		return u.Elem()
	case *types.Array:
		return u.Elem()
	case *types.Map:
		return u.Elem()
	default:
		return nil
	}
}

// unresolved reports whether the compiler left a type invalid.
func unresolved(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind() == types.Invalid
}

// resultsOf lists the result types of a function type, nothing for another type.
func resultsOf(t types.Type) []types.Type {
	sig, ok := t.(*types.Signature)
	if !ok {
		return nil
	}
	return tupleTypes(sig.Results())
}

// typesOf lists the type of each expression.
func typesOf(info *types.Info, exprs []ast.Expr) []types.Type {
	held := make([]types.Type, 0, len(exprs))
	for _, e := range exprs {
		held = append(held, info.TypeOf(e))
	}
	return held
}

// rootReached roots the members of the type of a value an unresolved position
// receives, or of each result of a function value, which that position may call.
func (d *rootDetection) rootReached(t types.Type) {
	if sig, isFunc := types.Unalias(t).(*types.Signature); isFunc {
		for _, result := range tupleTypes(sig.Results()) {
			d.rootMembers(result)
		}
		return
	}
	d.rootMembers(t)
}

// rootMembers roots every method and field one type declares.
func (d *rootDetection) rootMembers(t types.Type) {
	if pointer, ok := types.Unalias(t).(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return
	}
	for method := range named.Methods() {
		d.addIfInside(method.Pos())
	}
	if structure, ok := named.Underlying().(*types.Struct); ok {
		for field := range structure.Fields() {
			d.addIfInside(field.Pos())
		}
	}
}

// addIfInside roots the declaration at pos where the target holds it, and nothing
// where pos is in a file outside the target, a dependency's member among them.
func (d *rootDetection) addIfInside(pos token.Pos) {
	rendered, err := d.pos.render(pos)
	if err != nil {
		return
	}
	if id, held := d.byPosition[rendered]; held {
		d.add(id, RootTypeError, "")
	}
}
