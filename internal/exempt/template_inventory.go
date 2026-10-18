package exempt

import (
	"go/ast"
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
	in      *Input
	fset    *token.FileSet
	inits   map[*types.Var]templateInit
	uses    map[*types.Var]int
	allowed map[*types.Var]int
	flow    *valueFlow              // built on the first parse read
	every   map[string]*templateSet // [templateOrigins.everyParse]'s answer per engine
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
		in: in, root: in.Root, fset: in.Result.Fset, inits: make(map[*types.Var]templateInit),
		uses: make(map[*types.Var]int), allowed: make(map[*types.Var]int), every: make(map[string]*templateSet),
	}
	if in.Options.PublishedAPI {
		return nil
	}
	for _, p := range in.Facts.Program() {
		for _, obj := range p.TypesInfo.Uses {
			if v, isVar := obj.(*types.Var); isVar && isTemplate(v.Type()) {
				o.uses[v]++
			}
		}
		for _, f := range in.Facts.Files(p) {
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
// parses and pass-through methods; or else every template the program parses with
// the receiver's engine ([templateOrigins.everyParse]). It is nil otherwise, and
// for a library, whose callers may execute any template.
func (o *templateOrigins) executed(info *types.Info, dir string, call *ast.CallExpr) map[string]bool {
	sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	fn, _ := resolveObject(info, call.Fun).(*types.Func)
	if o == nil || !isSelector || fn == nil || fn.Pkg() == nil {
		return nil
	}
	inv := o.inventory()
	o.origin(info, dir, sel.X, inv, 0)
	if inv.complete && inv.parsed > 0 {
		return inv.names
	}
	return o.everyParse(fn.Pkg().Path())
}

// inventory is an empty inventory reading through the program's value flow.
func (o *templateOrigins) inventory() *templateSet {
	if o.flow == nil {
		o.flow = newValueFlow(o.in)
	}
	return &templateSet{root: o.root, names: make(map[string]bool), complete: true, flow: o.flow}
}

// maxTemplateChain stops an origin at a cycle of variables written from each other.
const maxTemplateChain = 8

// origin adds what the templates one expression of a template type holds name.
func (o *templateOrigins) origin(info *types.Info, dir string, e ast.Expr, inv *templateSet, depth int) {
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
func (o *templateOrigins) call(info *types.Info, dir string, call *ast.CallExpr, inv *templateSet, depth int) {
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
func (o *templateOrigins) method(info *types.Info, dir string, fn *types.Func, call *ast.CallExpr, inv *templateSet, depth int) {
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
func (o *templateOrigins) receiver(info *types.Info, dir string, call *ast.CallExpr, inv *templateSet, depth int) {
	if sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr); isSelector {
		o.origin(info, dir, sel.X, inv, depth)
		return
	}
	inv.complete = false
}

// templateParses are the functions and methods of the template engines that add
// templates to a set from a constant string, a file name or a pattern.
var templateParses = map[string]bool{"Parse": true, "ParseFiles": true, "ParseGlob": true, "ParseFS": true}

// templateSet accumulates the identifiers the templates of one set name, and whether
// every template of the set is one the analysis read: a constant string, a constant
// file name or pattern resolved against the target root or its file's directory, or
// a constant pattern of an embedded file system, each of which parses, and none an
// html/template file holding a JavaScript context ([encodesWhole]).
type templateSet struct {
	names    map[string]bool
	flow     *valueFlow
	root     string
	parsed   int
	complete bool
}

// parse reads one call that adds templates to a set of the engine at path. Each
// argument naming a template is followed through the program's value flow
// ([valueFlow]) to the constants, files and file systems it may be.
func (inv *templateSet) parse(info *types.Info, dir, engine, name string, call *ast.CallExpr) {
	at := flowAt{info: info, dir: dir}
	switch {
	case name == "Parse" && len(call.Args) == 1:
		inv.parseText(engine, call.Args[0], at)
	case name == "ParseFiles" && len(call.Args) > 0 && !call.Ellipsis.IsValid():
		for _, arg := range call.Args {
			inv.named(engine, arg, at, false)
		}
	case name == "ParseGlob" && len(call.Args) == 1:
		inv.named(engine, call.Args[0], at, true)
	case name == "ParseFS" && len(call.Args) > 1 && !call.Ellipsis.IsValid():
		inv.parseFS(engine, call, at)
	default:
		inv.complete = false
	}
}

// parseText reads the text one Parse call parses.
func (inv *templateSet) parseText(engine string, text ast.Expr, at flowAt) {
	sources, ok := inv.flow.texts(text, at)
	if !ok {
		inv.complete = false
		return
	}
	for _, source := range sources {
		if source.file {
			inv.files(engine, source.files)
		} else {
			inv.read(engine, "", []byte(source.text))
		}
	}
}

// named reads the files one file name or pattern argument names.
func (inv *templateSet) named(engine string, arg ast.Expr, at flowAt, pattern bool) {
	names, ok := inv.flow.constants(arg, at)
	if !ok {
		inv.complete = false
		return
	}
	for _, name := range names {
		inv.files(engine, inv.matching(at.dir, name, pattern))
	}
}

// parseFS reads one call that adds the templates of a file system
// [valueFlow.roots] bounds, each pattern matched against the files it holds.
func (inv *templateSet) parseFS(engine string, call *ast.CallExpr, at flowAt) {
	roots, ok := inv.flow.roots(call.Args[0], at)
	if !ok {
		inv.complete = false
		return
	}
	for _, arg := range call.Args[1:] {
		patterns, constants := inv.flow.constants(arg, at)
		if !constants {
			inv.complete = false
			return
		}
		for _, root := range roots {
			for _, pattern := range patterns {
				inv.files(engine, root.match(pattern))
			}
		}
	}
}

// matching is the files one constant name or pattern names, against the target root
// first and the directory of the file calling the parse second, as a program run
// from either would read them.
func (inv *templateSet) matching(dir, name string, pattern bool) []string {
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
func (inv *templateSet) files(engine string, paths []string) {
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
func (inv *templateSet) read(engine, name string, src []byte) {
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

// isTemplate reports whether t is a template of either engine, or a pointer to one.
func isTemplate(t types.Type) bool {
	t = types.Unalias(t)
	if pointer, isPointer := t.(*types.Pointer); isPointer {
		t = types.Unalias(pointer.Elem())
	}
	named, isNamed := t.(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && templatePackages[named.Obj().Pkg().Path()] && named.Obj().Name() == "Template"
}
