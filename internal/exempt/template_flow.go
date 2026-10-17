package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// valueFlow follows a value of the program back to the expressions that may have
// produced it: through the writes of a variable that is never addressed, the
// arguments of every call of a function only ever called, and the return
// statements of a function the program declares, with the arguments of the call
// being followed bound to the callee's parameters.
type valueFlow struct {
	fset      *token.FileSet
	writes    map[*types.Var][]flowWrite
	escaped   map[*types.Var]bool
	params    map[*types.Var]flowParam
	decls     map[*types.Func]flowDecl
	calls     map[*types.Func][]flowCall
	uses      map[*types.Func]int
	embedded  map[*types.Var][]string
	embedDirs map[*types.Var]string
	root      string
}

// flowWrite is one expression written to a variable; index is the result of a
// multi-value call it takes, -1 for a single value, and a nil expr the zero value.
type flowWrite struct {
	expr  ast.Expr
	at    flowAt
	index int
}

// flowParam is the function a parameter belongs to, and its position; -1 is the
// receiver.
type flowParam struct {
	fn       *types.Func
	position int
}

// flowDecl is the declaration of one function of the program.
type flowDecl struct {
	decl *ast.FuncDecl
	info *types.Info
	dir  string
}

// flowCall is one call of a function of the program.
type flowCall struct {
	call *ast.CallExpr
	info *types.Info
	dir  string
}

// flowAt is where an expression is evaluated: its file's type information and
// directory, and the call arguments bound to the parameters of the function it is
// written in.
type flowAt struct {
	info  *types.Info
	bound map[*types.Var]flowBinding
	dir   string
}

// flowBinding is the argument one call passes a parameter, where the call is.
type flowBinding struct {
	expr ast.Expr
	at   flowAt
}

// flowLeaf is an expression a value may come from that the flow does not follow:
// a constant, a call of a function outside the program, or anything else.
type flowLeaf struct {
	expr  ast.Expr
	at    flowAt
	index int
}

// maxFlowDepth bounds how many writes, calls and returns one value is followed
// through, which also stops a cycle of variables written from each other.
const maxFlowDepth = 12

func newValueFlow(in *Input) *valueFlow {
	f := &valueFlow{
		root: in.Root, fset: in.Result.Fset,
		writes: make(map[*types.Var][]flowWrite), escaped: make(map[*types.Var]bool),
		params: make(map[*types.Var]flowParam), decls: make(map[*types.Func]flowDecl),
		calls: make(map[*types.Func][]flowCall), uses: make(map[*types.Func]int),
		embedded: make(map[*types.Var][]string), embedDirs: make(map[*types.Var]string),
	}
	for _, p := range programPackages(in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, obj := range p.TypesInfo.Uses {
			if fn, isFunc := obj.(*types.Func); isFunc {
				f.uses[fn.Origin()]++
			}
		}
		for _, file := range p.Syntax {
			f.index(p.TypesInfo, filepath.Dir(f.fset.Position(file.FileStart).Filename), file)
		}
	}
	return f
}

// index records the writes, declarations, calls and embedded file systems of one
// file.
func (f *valueFlow) index(info *types.Info, dir string, file *ast.File) {
	at := flowAt{info: info, dir: dir}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GenDecl:
			f.embeds(info, dir, n)
		case *ast.FuncDecl:
			f.declare(info, dir, n)
		case *ast.ValueSpec:
			f.specWrites(at, n)
		case *ast.AssignStmt:
			f.assignWrites(at, n)
		case *ast.RangeStmt:
			f.escape(info, n.Key)
			f.escape(info, n.Value)
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				f.escape(info, n.X)
			}
		case *ast.CallExpr:
			if fn, isFunc := resolveObject(info, n.Fun).(*types.Func); isFunc {
				f.calls[fn.Origin()] = append(f.calls[fn.Origin()], flowCall{call: n, info: info, dir: dir})
			}
		}
		return true
	})
}

// declare records one function declaration and the positions of its parameters.
func (f *valueFlow) declare(info *types.Info, dir string, decl *ast.FuncDecl) {
	fn, isFunc := info.Defs[decl.Name].(*types.Func)
	if !isFunc || decl.Body == nil {
		return
	}
	f.decls[fn] = flowDecl{decl: decl, info: info, dir: dir}
	for _, one := range parametersOf(info, decl) {
		f.params[one.v] = flowParam{fn: fn, position: one.position}
	}
}

// declaredParameter is one parameter a declaration names, at its position; -1 is
// the receiver.
type declaredParameter struct {
	v        *types.Var
	position int
}

// parametersOf is every named parameter of one declaration, its receiver first.
func parametersOf(info *types.Info, decl *ast.FuncDecl) []declaredParameter {
	found := listParameters(info, decl.Recv, true)
	return append(found, listParameters(info, decl.Type.Params, false)...)
}

// listParameters is every named parameter of one parameter list, at its position,
// or at -1 for a receiver list.
func listParameters(info *types.Info, list *ast.FieldList, receiver bool) []declaredParameter {
	if list == nil {
		return nil
	}
	var found []declaredParameter
	position := 0
	for _, field := range list.List {
		if len(field.Names) == 0 {
			position++
		}
		for _, name := range field.Names {
			at := position
			if receiver {
				at = -1
			}
			if v, isVar := info.Defs[name].(*types.Var); isVar {
				found = append(found, declaredParameter{v: v, position: at})
			}
			position++
		}
	}
	return found
}

// specWrites records what one var specification writes.
func (f *valueFlow) specWrites(at flowAt, spec *ast.ValueSpec) {
	for i, name := range spec.Names {
		v, isVar := at.info.Defs[name].(*types.Var)
		if !isVar {
			continue
		}
		switch {
		case len(spec.Values) == 0:
			f.writes[v] = append(f.writes[v], flowWrite{index: -1, at: at})
		case len(spec.Values) == len(spec.Names):
			f.writes[v] = append(f.writes[v], flowWrite{expr: spec.Values[i], index: -1, at: at})
		case len(spec.Values) == 1:
			f.writes[v] = append(f.writes[v], flowWrite{expr: spec.Values[0], index: i, at: at})
		default:
			f.escaped[v] = true
		}
	}
}

// assignWrites records what one assignment writes to the variables it names. An
// operator assignment writes a value the flow cannot follow.
func (f *valueFlow) assignWrites(at flowAt, stmt *ast.AssignStmt) {
	for i, lhs := range stmt.Lhs {
		v := assignedVar(at.info, lhs)
		if v == nil {
			continue
		}
		switch {
		case stmt.Tok != token.DEFINE && stmt.Tok != token.ASSIGN:
			f.escaped[v] = true
		case len(stmt.Rhs) == len(stmt.Lhs):
			f.writes[v] = append(f.writes[v], flowWrite{expr: stmt.Rhs[i], index: -1, at: at})
		case len(stmt.Rhs) == 1:
			f.writes[v] = append(f.writes[v], flowWrite{expr: stmt.Rhs[0], index: i, at: at})
		default:
			f.escaped[v] = true
		}
	}
}

// assignedVar is the variable one assignment target names, a package-level one
// written through its package's name included.
func assignedVar(info *types.Info, lhs ast.Expr) *types.Var {
	var id *ast.Ident
	switch e := ast.Unparen(lhs).(type) {
	case *ast.Ident:
		id = e
	case *ast.SelectorExpr:
		if _, isPackage := info.Uses[identOf(e.X)].(*types.PkgName); !isPackage {
			return nil
		}
		id = e.Sel
	default:
		return nil
	}
	obj := info.Defs[id]
	if obj == nil {
		obj = info.Uses[id]
	}
	v, _ := obj.(*types.Var)
	return v
}

// identOf is the identifier e is, nil where it is none.
func identOf(e ast.Expr) *ast.Ident {
	id, _ := ast.Unparen(e).(*ast.Ident)
	return id
}

// escape marks the variable e names as written in a way the flow does not follow.
func (f *valueFlow) escape(info *types.Info, e ast.Expr) {
	if e == nil {
		return
	}
	if v := assignedVar(info, e); v != nil {
		f.escaped[v] = true
	}
}

// embeds records the //go:embed patterns of each package-level embed.FS variable
// one declaration declares.
func (f *valueFlow) embeds(info *types.Info, dir string, decl *ast.GenDecl) {
	if decl.Tok != token.VAR {
		return
	}
	for _, s := range decl.Specs {
		spec, isValue := s.(*ast.ValueSpec)
		if !isValue || len(spec.Names) != 1 {
			continue
		}
		v, isVar := info.Defs[spec.Names[0]].(*types.Var)
		if !isVar || !isEmbedFS(v.Type()) || v.Parent() != v.Pkg().Scope() {
			continue
		}
		doc := spec.Doc
		if doc == nil && len(decl.Specs) == 1 {
			doc = decl.Doc
		}
		f.embedDirs[v] = dir
		f.embedded[v] = embedPatterns(doc)
	}
}

// embedPatterns reads the patterns of every //go:embed directive of a comment
// group, a quoted pattern unquoted (https://pkg.go.dev/embed#hdr-Directives), and
// none where one directive's arguments do not parse, as the build then fails.
func embedPatterns(doc *ast.CommentGroup) []string {
	if doc == nil {
		return nil
	}
	var patterns []string
	for _, c := range doc.List {
		directive, found := ast.ParseDirective(c.Slash, c.Text)
		if !found || directive.Tool != "go" || directive.Name != "embed" {
			continue
		}
		args, err := directive.ParseArgs()
		if err != nil {
			return nil
		}
		for _, arg := range args {
			patterns = append(patterns, arg.Arg)
		}
	}
	return patterns
}

// isEmbedFS reports whether t is embed.FS.
func isEmbedFS(t types.Type) bool {
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "embed" && named.Obj().Name() == "FS"
}

// leaves is every expression e may take its value from, as index of a
// multi-value one, and false where the flow cannot bound them.
func (f *valueFlow) leaves(e ast.Expr, index int, at flowAt, depth int) ([]flowLeaf, bool) {
	e = ast.Unparen(e)
	if depth > maxFlowDepth {
		return nil, false
	}
	if tv, typed := at.info.Types[e]; typed && tv.Value != nil {
		return []flowLeaf{{expr: e, index: index, at: at}}, true
	}
	switch e := e.(type) {
	case *ast.Ident:
		v, isVar := at.info.Uses[e].(*types.Var)
		if !isVar {
			return []flowLeaf{{expr: e, index: index, at: at}}, true
		}
		return f.variable(v, e, index, at, depth)
	case *ast.SelectorExpr:
		if v, isVar := at.info.Uses[e.Sel].(*types.Var); isVar && v.Parent() == v.Pkg().Scope() {
			return f.variable(v, e, index, at, depth)
		}
	case *ast.CallExpr:
		if found, ok, followed := f.returns(e, index, at, depth); followed {
			return found, ok
		}
	}
	return []flowLeaf{{expr: e, index: index, at: at}}, true
}

// variable is [valueFlow.leaves] for a variable e names: an embedded file system is
// a leaf, a parameter takes what the call being followed binds it to or else what
// every call passes, and every variable takes every value written to it.
func (f *valueFlow) variable(v *types.Var, e ast.Expr, index int, at flowAt, depth int) ([]flowLeaf, bool) {
	if _, embeds := f.embedDirs[v]; embeds {
		return []flowLeaf{{expr: e, index: index, at: at}}, true
	}
	if f.escaped[v] || v.Origin() != v {
		return nil, false
	}
	var found []flowLeaf
	if param, isParam := f.params[v]; isParam {
		args, ok := f.arguments(v, param, at)
		if !ok {
			return nil, false
		}
		for _, arg := range args {
			more, ok := f.leaves(arg.expr, index, arg.at, depth+1)
			if !ok {
				return nil, false
			}
			found = append(found, more...)
		}
	} else if len(f.writes[v]) == 0 {
		return nil, false
	}
	written, ok := f.written(v, index, at, depth)
	return append(found, written...), ok
}

// written is what every value written to v may come from. A local variable is
// written in the function being followed, so its writes keep the call's bindings.
func (f *valueFlow) written(v *types.Var, index int, at flowAt, depth int) ([]flowLeaf, bool) {
	local := v.Pkg() == nil || v.Parent() != v.Pkg().Scope()
	var found []flowLeaf
	for _, w := range f.writes[v] {
		if w.expr == nil {
			continue
		}
		where := w.at
		if local {
			where.bound = at.bound
		}
		pick := index
		if w.index >= 0 {
			pick = w.index
		}
		more, ok := f.leaves(w.expr, pick, where, depth+1)
		if !ok {
			return nil, false
		}
		found = append(found, more...)
	}
	return found, true
}

// arguments is what a parameter may be passed: the argument the call being followed
// binds it to, else the argument of every call of its function where the program
// only ever calls that function by name. A method's parameters, which a call through
// an interface may pass, and a variadic one are bound only by the call followed.
func (f *valueFlow) arguments(v *types.Var, param flowParam, at flowAt) ([]flowBinding, bool) {
	if bound, isBound := at.bound[v]; isBound {
		return []flowBinding{bound}, true
	}
	signature := param.fn.Signature()
	if signature.Recv() != nil || signature.Variadic() || param.position < 0 {
		return nil, false
	}
	calls := f.calls[param.fn]
	if f.uses[param.fn] != len(calls) {
		return nil, false
	}
	found := make([]flowBinding, 0, len(calls))
	for _, c := range calls {
		if len(c.call.Args) != signature.Params().Len() || c.call.Ellipsis.IsValid() {
			return nil, false
		}
		found = append(found, flowBinding{expr: c.call.Args[param.position], at: flowAt{info: c.info, dir: c.dir}})
	}
	return found, true
}

// returns follows a call of a function the program declares to the values its
// return statements give, with the call's arguments bound to its parameters, and
// reports whether it followed one.
func (f *valueFlow) returns(call *ast.CallExpr, index int, at flowAt, depth int) (found []flowLeaf, ok, followed bool) {
	fn, isFunc := resolveObject(at.info, call.Fun).(*types.Func)
	if !isFunc {
		return nil, false, false
	}
	decl, declared := f.decls[fn.Origin()]
	if !declared {
		return nil, false, false
	}
	callee := flowAt{info: decl.info, dir: decl.dir, bound: f.bind(decl, call, at)}
	results := fn.Signature().Results().Len()
	if index < 0 {
		index = 0
	}
	ok = true
	ast.Inspect(decl.decl.Body, func(n ast.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			var more []flowLeaf
			switch {
			case len(n.Results) == results:
				more, ok = f.leaves(n.Results[index], -1, callee, depth+1)
			case len(n.Results) == 1 && results > 1:
				more, ok = f.leaves(n.Results[0], index, callee, depth+1)
			default:
				ok = false
			}
			found = append(found, more...)
		}
		return true
	})
	return found, ok, true
}

// bind maps the parameters of a declaration to the arguments one call passes.
func (*valueFlow) bind(decl flowDecl, call *ast.CallExpr, at flowAt) map[*types.Var]flowBinding {
	bound := make(map[*types.Var]flowBinding)
	sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	for _, one := range parametersOf(decl.info, decl.decl) {
		switch {
		case one.position < 0 && isSelector:
			bound[one.v] = flowBinding{expr: sel.X, at: at}
		case one.position >= 0 && !call.Ellipsis.IsValid() && one.position < len(call.Args) && !isVariadicAt(decl, one.position):
			bound[one.v] = flowBinding{expr: call.Args[one.position], at: at}
		}
	}
	return bound
}

// isVariadicAt reports whether position is a declaration's variadic parameter.
func isVariadicAt(decl flowDecl, position int) bool {
	fn, _ := decl.info.Defs[decl.decl.Name].(*types.Func)
	return fn != nil && fn.Signature().Variadic() && position == fn.Signature().Params().Len()-1
}

// constants is every constant string e may be, and false where one is no constant.
func (f *valueFlow) constants(e ast.Expr, at flowAt) ([]string, bool) {
	leaves, ok := f.leaves(e, -1, at, 0)
	if !ok {
		return nil, false
	}
	held := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		tv, typed := leaf.at.info.Types[leaf.expr]
		if !typed || tv.Value == nil || tv.Value.Kind() != constant.String {
			return nil, false
		}
		held = append(held, constant.StringVal(tv.Value))
	}
	return held, true
}

// fsRoot is a file system a value of the program may be: its directory on disk, and
// the files below it it holds.
type fsRoot struct {
	dir   string
	files []string
}

// match is the files of the root one fs.Glob pattern matches.
func (r fsRoot) match(pattern string) []string {
	var held []string
	for _, file := range r.files {
		rel, err := filepath.Rel(r.dir, file)
		if err != nil {
			continue
		}
		if matched, err := path.Match(pattern, filepath.ToSlash(rel)); err == nil && matched {
			held = append(held, file)
		}
	}
	return held
}

// named is the file of the root one name opens, nil where it holds none.
func (r fsRoot) named(name string) []string {
	return r.match(escapeGlob(name))
}

// escapeGlob quotes the characters path.Match reads as a pattern.
func escapeGlob(name string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`).Replace(name)
}

// maxRootFiles bounds the files one directory file system is read for.
const maxRootFiles = 2000

// roots is every file system e may be: a package-level embed.FS variable, a call of
// os.DirFS with a constant directory, or fs.Sub of one of them with a constant
// directory, and false where it may be anything else.
func (f *valueFlow) roots(e ast.Expr, at flowAt) ([]fsRoot, bool) {
	leaves, ok := f.leaves(e, -1, at, 0)
	if !ok {
		return nil, false
	}
	var found []fsRoot
	for _, leaf := range leaves {
		more, ok := f.rootOf(leaf)
		if !ok {
			return nil, false
		}
		found = append(found, more...)
	}
	return found, true
}

func (f *valueFlow) rootOf(leaf flowLeaf) ([]fsRoot, bool) {
	if v, isVar := resolveObject(leaf.at.info, leaf.expr).(*types.Var); isVar {
		if dir, embeds := f.embedDirs[v]; embeds {
			files, ok := embeddedFiles(f.root, dir, f.embedded[v])
			return []fsRoot{{dir: dir, files: files}}, ok
		}
	}
	call, fn := calledFunc(leaf)
	switch {
	case fn == nil:
		return nil, false
	case fn.Pkg().Path() == "os" && fn.Name() == "DirFS" && len(call.Args) == 1:
		return f.dirFS(call.Args[0], leaf.at)
	case fn.Pkg().Path() == ioFSPackage && fn.Name() == "Sub" && len(call.Args) == 2:
		return f.subFS(call.Args[0], call.Args[1], leaf.at)
	}
	return nil, false
}

// ioFSPackage is the file system interfaces' package.
const ioFSPackage = "io/fs"

// readFile is the name every file system reader of a whole file shares.
const readFile = "ReadFile"

// calledFunc is the call one leaf is and the package-level function or method it
// calls, nil where the leaf is no single-value call of one.
func calledFunc(leaf flowLeaf) (*ast.CallExpr, *types.Func) {
	call, isCall := leaf.expr.(*ast.CallExpr)
	if !isCall || leaf.index > 0 {
		return nil, nil
	}
	fn, _ := resolveObject(leaf.at.info, call.Fun).(*types.Func)
	if fn == nil || fn.Pkg() == nil {
		return nil, nil
	}
	return call, fn
}

// dirFS is every file system os.DirFS opens on each constant directory dir may be.
func (f *valueFlow) dirFS(dir ast.Expr, at flowAt) ([]fsRoot, bool) {
	dirs, ok := f.constants(dir, at)
	if !ok {
		return nil, false
	}
	found := make([]fsRoot, 0, len(dirs))
	for _, one := range dirs {
		root, ok := f.directory(at.dir, one)
		if !ok {
			return nil, false
		}
		found = append(found, root)
	}
	return found, true
}

// subFS is every file system fs.Sub gives below each constant directory of each
// file system the parent may be.
func (f *valueFlow) subFS(parent, dir ast.Expr, at flowAt) ([]fsRoot, bool) {
	parents, ok := f.roots(parent, at)
	subs, constants := f.constants(dir, at)
	if !ok || !constants {
		return nil, false
	}
	var found []fsRoot
	for _, one := range parents {
		for _, sub := range subs {
			found = append(found, one.sub(sub))
		}
	}
	return found, true
}

// sub is the part of the root below one slash-separated directory.
func (r fsRoot) sub(dir string) fsRoot {
	below := filepath.Join(r.dir, filepath.FromSlash(dir))
	held := fsRoot{dir: below}
	for _, file := range r.files {
		if within(below, file) {
			held.files = append(held.files, file)
		}
	}
	return held
}

// directory is the file system os.DirFS opens on one constant directory, read
// against the target root first and the calling file's directory second, as a
// program run from either opens it. It holds every regular file below the
// directory, and false where the directory is no directory of the target or holds
// more files than are read.
func (f *valueFlow) directory(callerDir, dir string) (fsRoot, bool) {
	for _, base := range []string{f.root, callerDir} {
		joined := filepath.Join(base, filepath.FromSlash(dir))
		if filepath.IsAbs(dir) {
			joined = filepath.Clean(dir)
		}
		if !within(f.root, joined) && joined != f.root {
			continue
		}
		files, err := regularFiles(joined, true)
		if err == nil {
			return fsRoot{dir: joined, files: files}, len(files) <= maxRootFiles
		}
	}
	return fsRoot{}, false
}

// embeddedFiles is the files a set of //go:embed patterns of a package in dir
// embeds: each file a pattern matches, and every file below a directory it
// matches but those whose name starts with . or _, which only the all: prefix
// keeps. A pattern that matches nothing fails the build, so it fails here, and so
// does a set holding more than maxRootFiles, which [regularFiles] stops reading.
func embeddedFiles(root, dir string, patterns []string) ([]string, bool) {
	if len(patterns) == 0 {
		return nil, false
	}
	var held []string
	for _, pattern := range patterns {
		files, ok := patternFiles(root, dir, pattern)
		if held = append(held, files...); !ok || len(held) > maxRootFiles {
			return nil, false
		}
	}
	return held, true
}

// patternFiles is the files one //go:embed pattern of a package in dir embeds,
// as [embeddedFiles] reads them, and false where it fails.
func patternFiles(root, dir, pattern string) ([]string, bool) {
	all := false
	if rest, found := strings.CutPrefix(pattern, "all:"); found {
		pattern, all = rest, true
	}
	matched, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pattern)))
	if err != nil || len(matched) == 0 {
		return nil, false
	}
	var held []string
	for _, one := range matched {
		if !within(root, one) {
			return nil, false
		}
		files, err := regularFiles(one, all)
		if err != nil {
			return nil, false
		}
		if held = append(held, files...); len(held) > maxRootFiles {
			return nil, false
		}
	}
	return held, true
}

// regularFiles is name itself where it is a regular file, else every regular file
// below it, skipping each file and directory below it whose name starts with . or
// _ unless hidden keeps them.
func regularFiles(name string, hidden bool) ([]string, error) {
	var held []string
	err := filepath.WalkDir(name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != name && !hidden && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			held = append(held, p)
		}
		if len(held) > maxRootFiles {
			return fs.SkipAll
		}
		return nil
	})
	return held, err
}

// textSource is one text a template parse may read: a constant, or any one of a
// set of files.
type textSource struct {
	text  string
	files []string
	file  bool
}

// texts is every text e may be: a constant string, or the content of a file read
// by os.ReadFile with a constant name, by fs.ReadFile or a ReadFile method of a
// file system [valueFlow.roots] bounds, or by io.ReadAll of a file such a file
// system's Open returns, each through a string conversion. A file system read
// with a name that is no constant may be any of its files.
func (f *valueFlow) texts(e ast.Expr, at flowAt) ([]textSource, bool) {
	leaves, ok := f.leaves(e, -1, at, 0)
	if !ok {
		return nil, false
	}
	var found []textSource
	for _, leaf := range leaves {
		more, ok := f.textOf(leaf)
		if !ok {
			return nil, false
		}
		found = append(found, more...)
	}
	return found, true
}

func (f *valueFlow) textOf(leaf flowLeaf) ([]textSource, bool) {
	if tv, typed := leaf.at.info.Types[leaf.expr]; typed && tv.Value != nil {
		if tv.Value.Kind() != constant.String {
			return nil, false
		}
		return []textSource{{text: constant.StringVal(tv.Value)}}, true
	}
	if operand := convertedOperand(leaf); operand != nil {
		return f.texts(operand, leaf.at)
	}
	return f.readText(leaf)
}

// convertedOperand is the operand of the conversion one leaf is, nil where it is
// none.
func convertedOperand(leaf flowLeaf) ast.Expr {
	call, isCall := leaf.expr.(*ast.CallExpr)
	if !isCall || leaf.index > 0 || len(call.Args) != 1 {
		return nil
	}
	if tv, typed := leaf.at.info.Types[call.Fun]; typed && tv.IsType() {
		return call.Args[0]
	}
	return nil
}

// readText is the text one call that reads a file gives.
func (f *valueFlow) readText(leaf flowLeaf) ([]textSource, bool) {
	call, fn := calledFunc(leaf)
	switch {
	case fn == nil:
		return nil, false
	case fn.Pkg().Path() == "io" && fn.Name() == "ReadAll" && len(call.Args) == 1:
		return f.opened(call.Args[0], leaf.at)
	case fn.Pkg().Path() == "os" && fn.Name() == readFile && len(call.Args) == 1:
		return f.osFile(call.Args[0], leaf.at)
	case fn.Pkg().Path() == ioFSPackage && fn.Name() == readFile && len(call.Args) == 2:
		return f.fileOf(call.Args[0], call.Args[1], leaf.at)
	case fn.Name() == readFile && fn.Signature().Recv() != nil && len(call.Args) == 1:
		return f.methodFile(call, leaf.at)
	}
	return nil, false
}

// osFile is the file os.ReadFile reads for each constant name may be, read against
// the target root and then the calling file's directory.
func (f *valueFlow) osFile(name ast.Expr, at flowAt) ([]textSource, bool) {
	names, ok := f.constants(name, at)
	if !ok {
		return nil, false
	}
	inv := &templateSet{root: f.root}
	found := make([]textSource, 0, len(names))
	for _, one := range names {
		found = append(found, textSource{files: inv.matching(at.dir, one, false), file: true})
	}
	return found, true
}

// opened is the text io.ReadAll reads of r: a file the Open method of a file system
// [valueFlow.roots] bounds returns.
func (f *valueFlow) opened(r ast.Expr, at flowAt) ([]textSource, bool) {
	leaves, ok := f.leaves(r, -1, at, 0)
	if !ok {
		return nil, false
	}
	var found []textSource
	for _, leaf := range leaves {
		call, isCall := leaf.expr.(*ast.CallExpr)
		if !isCall || leaf.index > 0 {
			return nil, false
		}
		fn, _ := resolveObject(leaf.at.info, call.Fun).(*types.Func)
		if fn == nil || fn.Name() != "Open" || fn.Signature().Recv() == nil || len(call.Args) != 1 {
			return nil, false
		}
		more, ok := f.methodFile(call, leaf.at)
		if !ok {
			return nil, false
		}
		found = append(found, more...)
	}
	return found, true
}

// methodFile is the file one Open or ReadFile method call of a file system names.
func (f *valueFlow) methodFile(call *ast.CallExpr, at flowAt) ([]textSource, bool) {
	sel, isSelector := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !isSelector || !isFileSystem(at.info.TypeOf(sel.X)) {
		return nil, false
	}
	return f.fileOf(sel.X, call.Args[0], at)
}

// isFileSystem reports whether t is embed.FS or an interface embedding io/fs.FS,
// whose Open and ReadFile read a file of the file system.
func isFileSystem(t types.Type) bool {
	if t == nil {
		return false
	}
	if isEmbedFS(t) {
		return true
	}
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == ioFSPackage &&
		(named.Obj().Name() == "FS" || named.Obj().Name() == "ReadFileFS")
}

// fileOf is the file name names in each file system fsys may be; a name that is no
// constant may be any of their files.
func (f *valueFlow) fileOf(fsys, name ast.Expr, at flowAt) ([]textSource, bool) {
	roots, ok := f.roots(fsys, at)
	if !ok {
		return nil, false
	}
	names, constants := f.constants(name, at)
	found := make([]textSource, 0, len(roots))
	for _, root := range roots {
		if !constants {
			found = append(found, textSource{files: root.files, file: true})
			continue
		}
		for _, one := range names {
			found = append(found, textSource{files: root.named(one), file: true})
		}
	}
	return found, true
}
