package exempt

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// decoders answers which parameters of a function only decode into the value they
// are given: the body uses the parameter at least once, and every use is an
// argument, unchanged or converted to an interface type, of a decoding entry point
// or of a parameter that only decodes, applied until no further function joins. A
// function the program does not declare is read from the source its package was
// compiled from, for this question alone.
type decoders struct {
	flow     *encodingFlow
	answered map[*types.Func]map[int]reach
	pending  map[*types.Func]bool
	sources  map[string]*checkedSource
	declared map[*types.Func]programFunction // the program's and every checked source's
	fset     *token.FileSet
	context  build.Context // the configuration's, which selects a source's files
}

// checkedSource is one package outside the program, parsed and type-checked from
// its source directory, and false where that failed.
type checkedSource struct {
	decls map[string]programFunction // by the declaring file and line of the name
	ok    bool
}

// newDecoders starts the answer over one configuration.
func newDecoders(f *encodingFlow) *decoders {
	d := &decoders{
		flow:     f,
		answered: make(map[*types.Func]map[int]reach),
		pending:  make(map[*types.Func]bool),
		sources:  make(map[string]*checkedSource),
		declared: make(map[*types.Func]programFunction),
		fset:     token.NewFileSet(),
		context:  f.kept.in.Result.Configuration.Context(),
	}
	for _, one := range programFunctions(f.kept.in) {
		d.declared[one.fn.Origin()] = one
	}
	return d
}

// at reports whether the parameter at one position of fn only decodes, and what the
// decoding entry points it reaches retain.
func (d *decoders) at(fn *types.Func, param int) (reach, bool) {
	key := fn.Origin()
	if held, known := d.answered[key]; known {
		r, decodes := held[param]
		return r, decodes
	}
	if d.pending[key] {
		return reach{}, false
	}
	d.pending[key] = true
	held := d.compute(fn)
	delete(d.pending, key)
	d.answered[key] = held
	r, decodes := held[param]
	return r, decodes
}

// compute answers every parameter of one function.
func (d *decoders) compute(fn *types.Func) map[int]reach {
	decl, info := d.body(fn)
	if decl == nil || decl.Body == nil || info == nil {
		return nil
	}
	defined, ok := info.Defs[decl.Name].(*types.Func)
	if !ok {
		return nil
	}
	sig := defined.Signature()
	if sig.Params().Len() != fn.Signature().Params().Len() {
		return nil
	}
	held := make(map[int]reach)
	for i := range sig.Params().Len() {
		if r, decodes := d.decodesInto(info, decl.Body, sig.Params().At(i)); decodes {
			held[i] = r
		}
	}
	return held
}

// decodesInto reports whether every use of one parameter in a body is an argument a
// decoding entry point or a decoding parameter receives, and there is at least one.
func (d *decoders) decodesInto(info *types.Info, body *ast.BlockStmt, param *types.Var) (reach, bool) {
	uses := 0
	for _, obj := range info.Uses {
		if obj == param {
			uses++
		}
	}
	if uses == 0 {
		return reach{}, false
	}
	decoded := 0
	var held reach
	ast.Inspect(body, func(n ast.Node) bool {
		if call, isCall := n.(*ast.CallExpr); isCall {
			count, r := d.decodedArguments(info, call, param)
			decoded += count
			held = held.union(r)
		}
		return true
	})
	return held, decoded == uses
}

// decodedArguments counts the arguments of one call that pass the parameter to a
// callee parameter that decodes into it, and what those callee parameters retain.
func (d *decoders) decodedArguments(info *types.Info, call *ast.CallExpr, param *types.Var) (int, reach) {
	callee, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc {
		return 0, reach{}
	}
	decoded := 0
	var held reach
	for i, arg := range call.Args {
		if usedParameter(info, arg) != param {
			continue
		}
		at, supplies := parameterAt(callee.Signature(), i)
		if !supplies {
			continue
		}
		if r, decodes := d.receives(callee, at); decodes {
			decoded++
			held = held.union(r)
		}
	}
	return decoded, held
}

// receives reports whether one parameter of a callee decodes into its argument.
func (d *decoders) receives(callee *types.Func, at int) (reach, bool) {
	if dest, named := d.flow.encodingDestination(callee); named {
		if decodingOnly(dest.reach) {
			return dest.reach, true
		}
		return reach{}, false
	}
	return d.at(callee, at)
}

// decodingOnly reports whether a destination fills a value's fields without reading
// them and resolves only decoding methods.
func decodingOnly(r reach) bool {
	return r.methods&^reachDecoding == 0 && !r.fields && len(r.outside) == 0
}

// usedParameter is the variable an argument names, unchanged or converted to an
// interface type, and nil for any other argument.
func usedParameter(info *types.Info, arg ast.Expr) *types.Var {
	arg = ast.Unparen(arg)
	if call, isCall := arg.(*ast.CallExpr); isCall && len(call.Args) == 1 {
		if tv, held := info.Types[call.Fun]; held && tv.IsType() && types.IsInterface(tv.Type) {
			arg = ast.Unparen(call.Args[0])
		}
	}
	ident, isIdent := arg.(*ast.Ident)
	if !isIdent {
		return nil
	}
	v, _ := info.Uses[ident].(*types.Var)
	return v
}

// body is the declaration and type information of one function: the program's own
// where it declares the function, and otherwise the source its package was compiled
// from.
func (d *decoders) body(fn *types.Func) (*ast.FuncDecl, *types.Info) {
	if one, held := d.declared[fn.Origin()]; held {
		return one.decl, one.info
	}
	if fn.Pkg() == nil {
		return nil, nil
	}
	at := d.flow.kept.in.Result.Fset.Position(fn.Pos())
	if !at.IsValid() || at.Filename == "" {
		return nil, nil
	}
	source := d.source(fn.Pkg(), filepath.Dir(at.Filename))
	if !source.ok {
		return nil, nil
	}
	one, held := source.decls[declKey(filepath.Base(at.Filename), at.Line)]
	if !held {
		return nil, nil
	}
	return one.decl, one.info
}

// source parses and type-checks the package in one directory, its imports resolved
// to the packages the loaded program holds and otherwise to the toolchain's export
// data, so its objects are the program's own.
func (d *decoders) source(pkg *types.Package, dir string) *checkedSource {
	if held, known := d.sources[dir]; known {
		return held
	}
	held := &checkedSource{decls: make(map[string]programFunction)}
	d.sources[dir] = held
	files := d.parseDir(pkg, dir)
	if len(files) == 0 {
		return held
	}
	info := checkFiles(pkg, d.fset, files)
	for _, file := range files {
		for _, decl := range file.Decls {
			d.index(held, info, decl)
		}
	}
	held.ok = true
	return held
}

// parseDir parses the files of one directory that build into the package under the
// configuration's build context, tests excluded.
func (d *decoders) parseDir(pkg *types.Package, dir string) []*ast.File {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if match, matchErr := d.context.MatchFile(dir, name); matchErr != nil || !match {
			continue
		}
		file, parseErr := parser.ParseFile(d.fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if parseErr == nil && file.Name.Name == pkg.Name() {
			files = append(files, file)
		}
	}
	return files
}

// checkFiles type-checks one package's files, its imports resolved to the packages
// the loaded program holds and otherwise to the toolchain's export data. A type error
// leaves what it could not check out of the answer.
func checkFiles(pkg *types.Package, fset *token.FileSet, files []*ast.File) *types.Info {
	imports := make(map[string]*types.Package, len(pkg.Imports()))
	for _, imported := range pkg.Imports() {
		imports[imported.Path()] = imported
	}
	fallback := importer.Default()
	info := &types.Info{
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
		Types: make(map[ast.Expr]types.TypeAndValue),
	}
	config := types.Config{
		Importer: importerFunc(func(path string) (*types.Package, error) {
			if one, found := imports[path]; found {
				return one, nil
			}
			return fallback.Import(path)
		}),
		Error: func(error) {},
	}
	_, _ = config.Check(pkg.Path(), fset, files, info)
	return info
}

// index records one checked function declaration of a source under its file and
// line, and as a function the program can read the body of.
func (d *decoders) index(held *checkedSource, info *types.Info, decl ast.Decl) {
	fd, isFunc := decl.(*ast.FuncDecl)
	if !isFunc {
		return
	}
	fn, defines := info.Defs[fd.Name].(*types.Func)
	if !defines {
		return
	}
	one := programFunction{info: info, decl: fd, fn: fn}
	d.declared[fn] = one
	at := d.fset.Position(fd.Name.Pos())
	held.decls[declKey(filepath.Base(at.Filename), at.Line)] = one
}

// declKey names a declaration by its file and the line of its name, which is what an
// object read from export data carries.
func declKey(file string, line int) string {
	return file + ":" + strconv.Itoa(line)
}

// importerFunc adapts a function to the types.Importer interface.
type importerFunc func(path string) (*types.Package, error)

// Import resolves one import path.
func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
