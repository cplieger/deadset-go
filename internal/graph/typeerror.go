package graph

import (
	"go/ast"
	"go/token"
	"go/types"
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
// an operand whose type it knows could have named any member of that type, so every
// method and field of the type is rooted too.
func (d *rootDetection) typeErrors(r *load.Result) {
	for _, one := range skippedDecls(r) {
		if !d.rootDeclared(one.decl) {
			continue
		}
		if info := one.pkg.TypesInfo; info != nil {
			d.rootUnresolvedSelections(info, one.decl)
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
