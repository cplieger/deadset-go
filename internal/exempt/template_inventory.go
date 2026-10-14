package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template/parse"
)

var templateExecutions = map[string]bool{"Execute": true, "ExecuteTemplate": true}

// htmlTemplate is the engine that escapes a value by the context it is written in.
const htmlTemplate = "html/template"

// sealedTemplateMethods are the methods a variable holding a template may be the
// receiver of while every template it holds stays the set its initializer parsed.
var sealedTemplateMethods = map[string]bool{
	"Execute": true, "ExecuteTemplate": true, "Lookup": true, "Name": true, "DefinedTemplates": true, "Templates": true,
}

// templateOrigins answers, for the receiver of a template execution, every
// identifier the templates it may hold name, from the program's own syntax.
type templateOrigins struct {
	fset    *token.FileSet
	inits   map[*types.Var]templateInit
	uses    map[*types.Var]int
	allowed map[*types.Var]int
	root    string
}

// templateInit is the one expression that writes a variable holding a template, with
// the type information and the directory of its file.
type templateInit struct {
	expr ast.Expr
	info *types.Info
	dir  string
}

// newTemplateOrigins indexes every variable of a template type the program declares
// with one initializer, and how its uses read it.
func newTemplateOrigins(in *Input) *templateOrigins {
	o := &templateOrigins{
		root: in.Root, fset: in.Result.Fset, inits: make(map[*types.Var]templateInit),
		uses: make(map[*types.Var]int), allowed: make(map[*types.Var]int),
	}
	if in.Options.PublishedAPI {
		return o
	}
	for _, p := range programPackages(in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, obj := range p.TypesInfo.Uses {
			if v, isVar := obj.(*types.Var); isVar && isTemplate(v.Type()) {
				o.uses[v]++
			}
		}
		for _, f := range p.Syntax {
			o.index(p.TypesInfo, filepath.Dir(o.fset.Position(f.FileStart).Filename), f)
		}
	}
	return o
}

// index records the initializers of one file and its uses of a template variable as
// the receiver of a method that leaves its templates as they are.
func (o *templateOrigins) index(info *types.Info, dir string, f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, name := range n.Names {
					o.initialize(info, dir, name, n.Values[i])
				}
			}
		case *ast.AssignStmt:
			o.defined(info, dir, n)
		case *ast.SelectorExpr:
			o.sealedUse(info, n)
		}
		return true
	})
}

// defined records the initializers a short variable declaration writes.
func (o *templateOrigins) defined(info *types.Info, dir string, stmt *ast.AssignStmt) {
	if stmt.Tok != token.DEFINE || len(stmt.Lhs) != len(stmt.Rhs) {
		return
	}
	for i, lhs := range stmt.Lhs {
		if name, isName := lhs.(*ast.Ident); isName {
			o.initialize(info, dir, name, stmt.Rhs[i])
		}
	}
}

// sealedUse counts a variable that is the receiver of a method leaving its templates
// as they are.
func (o *templateOrigins) sealedUse(info *types.Info, sel *ast.SelectorExpr) {
	if id, isName := ast.Unparen(sel.X).(*ast.Ident); isName && sealedTemplateMethods[sel.Sel.Name] {
		if v, isVar := info.Uses[id].(*types.Var); isVar {
			o.allowed[v]++
		}
	}
}

// initialize records the initializer of one variable a declaration defines.
func (o *templateOrigins) initialize(info *types.Info, dir string, name *ast.Ident, value ast.Expr) {
	if v, isVar := info.Defs[name].(*types.Var); isVar && isTemplate(v.Type()) {
		o.inits[v] = templateInit{expr: value, info: info, dir: dir}
	}
}

// executed is every identifier the templates one execution's receiver may hold name,
// where each of them is a constant or a file the analysis reads
// ([inventory]): the receiver is a variable the program reads only to execute, look
// up or name its templates, written once by a chain of the engines' constructors,
// parses and pass-through methods. It is nil otherwise, and for a library, whose
// callers may execute any template.
func (o *templateOrigins) executed(info *types.Info, dir string, call *ast.CallExpr) map[string]bool {
	sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !isSelector {
		return nil
	}
	inv := &inventory{root: o.root, fset: o.fset, names: make(map[string]bool), complete: true}
	o.origin(info, dir, sel.X, inv, 0)
	if !inv.complete || inv.parsed == 0 {
		return nil
	}
	return inv.names
}

// maxTemplateChain stops an origin at a cycle of variables written from each other.
const maxTemplateChain = 8

// origin adds what the templates one expression of a template type holds name.
func (o *templateOrigins) origin(info *types.Info, dir string, e ast.Expr, inv *inventory, depth int) {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		v, isVar := info.Uses[e].(*types.Var)
		init, written := o.inits[v]
		if !isVar || !written || depth > maxTemplateChain || o.uses[v] != o.allowed[v] {
			inv.complete = false
			return
		}
		o.origin(init.info, init.dir, init.expr, inv, depth+1)
	case *ast.CallExpr:
		o.call(info, dir, e, inv, depth)
	default:
		inv.complete = false
	}
}

// call adds what the templates one call of a template engine returns name.
func (o *templateOrigins) call(info *types.Info, dir string, call *ast.CallExpr, inv *inventory, depth int) {
	fn, _ := resolveObject(info, call.Fun).(*types.Func)
	if fn == nil || fn.Pkg() == nil || !templatePackages[fn.Pkg().Path()] {
		inv.complete = false
		return
	}
	if fn.Signature().Recv() != nil {
		o.method(info, dir, fn, call, inv, depth)
		return
	}
	switch name := fn.Name(); {
	case name == "Must" && len(call.Args) > 0:
		o.origin(info, dir, call.Args[0], inv, depth)
	case name == "New":
	case templateParses[name]:
		inv.parse(info, dir, fn.Pkg().Path(), name, call)
	default:
		inv.complete = false
	}
}

// method adds what the templates one method call of a template engine returns name.
func (o *templateOrigins) method(info *types.Info, dir string, fn *types.Func, call *ast.CallExpr, inv *inventory, depth int) {
	switch name := fn.Name(); {
	case templateParses[name]:
		inv.parse(info, dir, fn.Pkg().Path(), name, call)
	case name == "New", name == "Funcs", name == "Option", name == "Lookup":
	default:
		inv.complete = false
		return
	}
	o.receiver(info, dir, call, inv, depth)
}

// receiver adds what the templates the receiver of one method call holds name.
func (o *templateOrigins) receiver(info *types.Info, dir string, call *ast.CallExpr, inv *inventory, depth int) {
	if sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr); isSelector {
		o.origin(info, dir, sel.X, inv, depth)
		return
	}
	inv.complete = false
}

// templateParses are the functions and methods of the template engines that add
// templates to a set from a constant string, a file name or a pattern.
var templateParses = map[string]bool{"Parse": true, "ParseFiles": true, "ParseGlob": true, "ParseFS": true}

// inventory accumulates the identifiers the templates of one set name, and whether
// every template of the set is one the analysis read: a constant string, a constant
// file name or pattern resolved against the target root or its file's directory, or
// a constant pattern of an embedded file system, each of which parses, and none an
// html/template file holding a JavaScript context ([encodesWhole]).
type inventory struct {
	fset     *token.FileSet
	names    map[string]bool
	root     string
	parsed   int
	complete bool
}

// parse reads one call that adds templates to a set of the engine at path.
func (inv *inventory) parse(info *types.Info, dir, engine, name string, call *ast.CallExpr) {
	args, ok := constantStrings(info, call.Args)
	switch {
	case name == "Parse" && ok && len(args) == 1:
		inv.read(engine, "", []byte(args[0]))
	case name == "ParseFiles" && ok && len(args) > 0:
		for _, file := range args {
			inv.files(engine, inv.matching(dir, file, false))
		}
	case name == "ParseGlob" && ok && len(args) == 1:
		inv.files(engine, inv.matching(dir, args[0], true))
	case name == "ParseFS" && len(call.Args) > 1:
		inv.parseFS(info, engine, call)
	default:
		inv.complete = false
	}
}

// parseFS reads one call that adds the templates of an embedded file system.
func (inv *inventory) parseFS(info *types.Info, engine string, call *ast.CallExpr) {
	base, embedded := inv.embeddedDir(info, call.Args[0])
	patterns, constants := constantStrings(info, call.Args[1:])
	if !embedded || !constants {
		inv.complete = false
		return
	}
	for _, pattern := range patterns {
		inv.files(engine, globWithin(inv.root, filepath.Join(base, filepath.FromSlash(pattern))))
	}
}

// matching is the files one constant name or pattern names, against the target root
// first and the directory of the file calling the parse second, as a program run
// from either would read them.
func (inv *inventory) matching(dir, name string, pattern bool) []string {
	for _, base := range []string{inv.root, dir} {
		joined := filepath.Join(base, filepath.FromSlash(name))
		if filepath.IsAbs(name) {
			joined = filepath.Clean(name)
		}
		var found []string
		if pattern {
			found = globWithin(inv.root, joined)
		} else if info, err := os.Stat(joined); err == nil && !info.IsDir() && within(inv.root, joined) {
			found = []string{joined}
		}
		if len(found) > 0 {
			return found
		}
	}
	return nil
}

// files reads every template file one parse names, and makes the inventory
// incomplete where it names none.
func (inv *inventory) files(engine string, paths []string) {
	if len(paths) == 0 {
		inv.complete = false
		return
	}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			inv.complete = false
			return
		}
		inv.read(engine, path, src)
	}
}

// read adds the identifiers one template's text names.
func (inv *inventory) read(engine, name string, src []byte) {
	if engine == htmlTemplate && encodesWhole(src) {
		inv.complete = false
		return
	}
	tree := parse.New(name)
	tree.Mode = parse.SkipFuncCheck
	if _, err := tree.Parse(string(src), "", "", make(map[string]*parse.Tree)); err != nil {
		inv.complete = false
		return
	}
	for _, ref := range templateFileRefs(name, src, Delimiters{}) {
		inv.names[ref.name] = true
	}
	inv.parsed++
}

// eventHandler matches an HTML start tag holding an event-handler attribute.
var eventHandler = regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`)

// encodesWhole reports whether an html/template source holds a JavaScript context,
// a script element or an event-handler attribute, where the engine encodes a value
// as JSON whole (https://pkg.go.dev/html/template#hdr-Contexts).
func encodesWhole(src []byte) bool {
	return strings.Contains(strings.ToLower(string(src)), "<script") || eventHandler.Match(src)
}

func globWithin(root, pattern string) []string {
	matched, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var held []string
	for _, path := range matched {
		if info, err := os.Stat(path); err == nil && !info.IsDir() && within(root, path) {
			held = append(held, path)
		}
	}
	return held
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

// constantStrings is the value of each expression, and true where every one is a
// constant string.
func constantStrings(info *types.Info, exprs []ast.Expr) ([]string, bool) {
	held := make([]string, 0, len(exprs))
	for _, e := range exprs {
		tv, ok := info.Types[e]
		if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
			return nil, false
		}
		held = append(held, constant.StringVal(tv.Value))
	}
	return held, true
}

// embeddedDir is the directory of the file declaring the package-level embed.FS
// variable e names, against which its patterns resolve, and true where e is one.
func (inv *inventory) embeddedDir(info *types.Info, e ast.Expr) (string, bool) {
	id, isIdent := ast.Unparen(e).(*ast.Ident)
	if !isIdent {
		return "", false
	}
	v, isVar := info.Uses[id].(*types.Var)
	if !isVar || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() {
		return "", false
	}
	named, isNamed := types.Unalias(v.Type()).(*types.Named)
	if !isNamed || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "embed" || named.Obj().Name() != "FS" {
		return "", false
	}
	return filepath.Dir(inv.fset.Position(v.Pos()).Filename), true
}

// isTemplate reports whether t is a template of either engine, or a pointer to one.
func isTemplate(t types.Type) bool {
	t = types.Unalias(t)
	if pointer, isPointer := t.(*types.Pointer); isPointer {
		t = types.Unalias(pointer.Elem())
	}
	named, isNamed := t.(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && templatePackages[named.Obj().Pkg().Path()] && named.Obj().Name() == "Template"
}
