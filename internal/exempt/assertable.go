package exempt

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// sources parses and type-checks the packages outside the analysed program whose
// source a class reads, each once, under the configuration's build context.
type sources struct {
	fset    *token.FileSet
	checked map[string]*checkedSource // by directory
	context build.Context
}

// checkedSource is one package outside the program, parsed and type-checked from
// its source directory, and false where that failed.
type checkedSource struct {
	info  *types.Info
	decls map[string]programFunction // by the declaring file and line of the name, on first use
	files []*ast.File
	ok    bool
}

// newSources reads source under one input's configuration.
func newSources(in *Input) *sources {
	return &sources{
		fset:    token.NewFileSet(),
		checked: make(map[string]*checkedSource),
		context: in.Result.Configuration.Context(),
	}
}

// read parses and type-checks the package in one directory, its imports resolved
// to the packages the loaded program holds and otherwise to the toolchain's export
// data, so its objects are the program's own.
func (s *sources) read(pkg *types.Package, dir string) *checkedSource {
	if held, known := s.checked[dir]; known {
		return held
	}
	held := &checkedSource{}
	s.checked[dir] = held
	held.files = s.parseDir(pkg, dir)
	if len(held.files) == 0 {
		return held
	}
	held.info = checkFiles(pkg, s.fset, held.files)
	held.ok = true
	return held
}

// parseDir parses the files of one directory that build into the package under the
// configuration's build context, tests excluded.
func (s *sources) parseDir(pkg *types.Package, dir string) []*ast.File {
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
		if match, matchErr := s.context.MatchFile(dir, name); matchErr != nil || !match {
			continue
		}
		file, parseErr := parser.ParseFile(s.fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if parseErr == nil && file.Name.Name == pkg.Name() {
			files = append(files, file)
		}
	}
	return files
}

// assertable answers which interfaces a package outside the analysed program can
// assert on a value it holds, each package's answer computed once.
type assertable struct {
	in       *Input
	source   *sources
	own      map[string][]*types.Interface // by package path
	imported map[string]importedSet        // by package path
}

// importedSet is what the packages one package imports at any depth can name: their
// exported interfaces, and whether a template engine is among them.
type importedSet struct {
	interfaces []*types.Interface
	templates  bool
}

// assertableOf is the one answer over an input, made on first use.
func assertableOf(in *Input) *assertable {
	if in.assert == nil {
		in.assert = &assertable{
			in:       in,
			source:   newSources(in),
			own:      make(map[string][]*types.Interface),
			imported: make(map[string]importedSet),
		}
	}
	return in.assert
}

// ownInterfaces is every interface one package declares, exported or not, and every
// interface type literal its source writes.
func (a *assertable) ownInterfaces(pkg *types.Package) []*types.Interface {
	if held, known := a.own[pkg.Path()]; known {
		return held
	}
	held := append(declaredInterfaces(pkg, true), a.literals(pkg)...)
	a.own[pkg.Path()] = held
	return held
}

// importedBy is what the packages one package imports at any depth can name, the
// package itself counted for the template engines alone.
func (a *assertable) importedBy(pkg *types.Package) importedSet {
	if held, known := a.imported[pkg.Path()]; known {
		return held
	}
	held := importedSet{templates: templatePackages[pkg.Path()]}
	seen := map[string]bool{pkg.Path(): true}
	queue := slices.Clone(pkg.Imports())
	for _, one := range queue {
		seen[one.Path()] = true
	}
	for len(queue) > 0 {
		one := queue[0]
		queue = queue[1:]
		held.templates = held.templates || templatePackages[one.Path()]
		held.interfaces = append(held.interfaces, declaredInterfaces(one, false)...)
		for _, imported := range one.Imports() {
			if !seen[imported.Path()] {
				seen[imported.Path()] = true
				queue = append(queue, imported)
			}
		}
	}
	a.imported[pkg.Path()] = held
	return held
}

// literals is every interface type literal one package's source writes that holds
// methods alone and that the loaded program can state, read from the directory the
// package was compiled from. A package-level type declaration is the scope's
// ([declaredInterfaces]), so its literal is not read twice.
func (a *assertable) literals(pkg *types.Package) []*types.Interface {
	dir, located := packageDir(a.in.Result.Fset, pkg)
	if !located {
		return nil
	}
	source := a.source.read(pkg, dir)
	if !source.ok {
		return nil
	}
	local := localizer{pkg: pkg}
	var found []*types.Interface
	for _, file := range source.files {
		declared := packageLevelTypes(file)
		ast.Inspect(file, func(n ast.Node) bool {
			literal, isInterface := n.(*ast.InterfaceType)
			if !isInterface || declared[literal] {
				return true
			}
			iface, checked := source.info.TypeOf(literal).(*types.Interface)
			if !checked || !iface.IsMethodSet() || iface.NumMethods() == 0 {
				return true
			}
			if loaded, stated := local.iface(iface); stated {
				found = append(found, loaded)
			}
			return true
		})
	}
	return found
}

// packageLevelTypes is the type expression of every type one file declares at
// package level.
func packageLevelTypes(file *ast.File) map[ast.Expr]bool {
	held := make(map[ast.Expr]bool)
	for _, decl := range file.Decls {
		gen, isGen := decl.(*ast.GenDecl)
		if !isGen || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			if typeSpec, isType := spec.(*ast.TypeSpec); isType {
				held[typeSpec.Type] = true
			}
		}
	}
	return held
}

// packageDir is the directory one package outside the program was compiled from,
// read from the position of a declaration its scope holds.
func packageDir(fset *token.FileSet, pkg *types.Package) (string, bool) {
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		at := fset.Position(scope.Lookup(name).Pos())
		if at.IsValid() && at.Filename != "" {
			return filepath.Dir(at.Filename), true
		}
	}
	return "", false
}

// localizer rewrites a type a source reading checked into the loaded program's
// types. The reading checks the package anew, so a type the package declares is a
// second object there, which no type of the program is identical to; it is replaced
// by the loaded declaration of the same name. A type with no loaded counterpart, one
// declared in a function body or a type parameter, cannot be stated.
type localizer struct {
	pkg *types.Package
}

// of is t in the loaded program's types, and false where it has no counterpart.
func (l localizer) of(t types.Type) (types.Type, bool) {
	switch u := t.(type) {
	case *types.Basic:
		return u, true
	case *types.Alias:
		return l.of(types.Unalias(u))
	case *types.Named:
		return l.named(u)
	case *types.Pointer:
		elem, ok := l.of(u.Elem())
		return types.NewPointer(elem), ok
	case *types.Slice:
		elem, ok := l.of(u.Elem())
		return types.NewSlice(elem), ok
	case *types.Array:
		elem, ok := l.of(u.Elem())
		return types.NewArray(elem, u.Len()), ok
	case *types.Chan:
		elem, ok := l.of(u.Elem())
		return types.NewChan(u.Dir(), elem), ok
	case *types.Map:
		key, keyOK := l.of(u.Key())
		elem, elemOK := l.of(u.Elem())
		return types.NewMap(key, elem), keyOK && elemOK
	case *types.Signature:
		if sig, ok := l.signature(u); ok {
			return sig, true
		}
	case *types.Struct:
		return l.structure(u)
	case *types.Interface:
		if iface, ok := l.iface(u); ok {
			return iface, true
		}
	}
	return nil, false
}

// named is one defined type in the loaded program's types.
func (l localizer) named(n *types.Named) (types.Type, bool) {
	obj := n.Obj()
	if obj.Pkg() == nil || obj.Pkg() == l.pkg || obj.Pkg().Path() != l.pkg.Path() {
		return n, true
	}
	if obj.Parent() != obj.Pkg().Scope() {
		return nil, false
	}
	loaded, declared := l.pkg.Scope().Lookup(obj.Name()).(*types.TypeName)
	if !declared {
		return nil, false
	}
	args := n.TypeArgs()
	if args.Len() == 0 {
		return loaded.Type(), true
	}
	stated := make([]types.Type, args.Len())
	for i := range args.Len() {
		arg, ok := l.of(args.At(i))
		if !ok {
			return nil, false
		}
		stated[i] = arg
	}
	instance, err := types.Instantiate(nil, loaded.Type(), stated, false)
	return instance, err == nil
}

// signature is one function type in the loaded program's types.
func (l localizer) signature(sig *types.Signature) (*types.Signature, bool) {
	if sig.TypeParams().Len() > 0 {
		return nil, false
	}
	params, paramsOK := l.tuple(sig.Params())
	results, resultsOK := l.tuple(sig.Results())
	return types.NewSignatureType(nil, nil, nil, params, results, sig.Variadic()), paramsOK && resultsOK
}

// tuple is one parameter or result list in the loaded program's types.
func (l localizer) tuple(t *types.Tuple) (*types.Tuple, bool) {
	vars := make([]*types.Var, t.Len())
	for i := range t.Len() {
		v := t.At(i)
		stated, ok := l.of(v.Type())
		if !ok {
			return nil, false
		}
		vars[i] = types.NewParam(v.Pos(), v.Pkg(), v.Name(), stated)
	}
	return types.NewTuple(vars...), true
}

// structure is one struct type with no name in the loaded program's types.
func (l localizer) structure(st *types.Struct) (types.Type, bool) {
	fields := make([]*types.Var, st.NumFields())
	tags := make([]string, st.NumFields())
	for i := range st.NumFields() {
		field := st.Field(i)
		stated, ok := l.of(field.Type())
		if !ok {
			return nil, false
		}
		fields[i] = types.NewField(field.Pos(), field.Pkg(), field.Name(), stated, field.Embedded())
		tags[i] = st.Tag(i)
	}
	return types.NewStruct(fields, tags), true
}

// iface is one interface type in the loaded program's types, its methods those of
// its method set, embedded ones included.
func (l localizer) iface(iface *types.Interface) (*types.Interface, bool) {
	if !iface.IsMethodSet() {
		return nil, false
	}
	methods := make([]*types.Func, 0, iface.NumMethods())
	for m := range iface.Methods() {
		sig, ok := l.signature(m.Signature())
		if !ok {
			return nil, false
		}
		methods = append(methods, types.NewFunc(m.Pos(), l.pkg, m.Name(), sig))
	}
	return types.NewInterfaceType(methods, nil).Complete(), true
}
