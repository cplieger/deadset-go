package roots

import (
	"go/ast"
	"go/types"
	"path"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/conventions"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"golang.org/x/tools/go/packages"
)

// convention is one tool's root rule: the kind of root it reports and the rule
// that names what it reads.
type convention struct {
	rule conventions.Rule
	kind RootKind
}

// conventionRows are the Go tools whose conventions root what no reference
// reaches, each under the tool's name and where its convention is documented.
var conventionRows = [...]convention{
	// go-swagger: https://goswagger.io/go-swagger/use/spec/
	{
		kind: RootSwagger,
		rule: &conventions.DocDirective{Prefix: "swagger:", Of: conventions.Declarations},
	},
	// swag: https://github.com/swaggo/swag#api-operation
	{
		kind: RootSwagRoute,
		rule: &conventions.DocDirective{Prefix: "@Router", Of: conventions.Functions, Separated: true},
	},
	// stringer: https://pkg.go.dev/golang.org/x/tools/cmd/stringer
	{
		kind: RootGenerate,
		rule: &conventions.GenerateInputs{Programs: []string{"stringer"}, Select: conventions.TypeFlag},
	},
	// enumer: https://github.com/dmarkham/enumer
	{
		kind: RootGenerate,
		rule: &conventions.GenerateInputs{Programs: []string{"enumer"}, Select: conventions.TypeFlag},
	},
	// mockgen: https://github.com/uber-go/mock
	{
		kind: RootGenerate,
		rule: &conventions.GenerateInputs{Programs: []string{"mockgen"}, Select: conventions.SourceOrNames, BooleanFlags: []string{
			"typed", "version", "debug_parser", "write_package_comment", "write_source_comment", "write_generate_directive",
		}},
	},
	// ent: https://entgo.io/docs/code-gen
	{
		kind: RootGenerate,
		rule: &conventions.PackageGenerator{Programs: []string{"ent", "entc"}, Subcommand: "generate"},
	},
	// testify-suite: https://pkg.go.dev/github.com/stretchr/testify/suite
	{
		kind: RootTest,
		rule: &conventions.SuiteRunner{Pkg: "github.com/stretchr/testify/suite", Func: "Run", Arg: 1, Prefixes: []string{testPrefix}},
	},
	// gocheck-suite: https://pkg.go.dev/gopkg.in/check.v1
	{
		kind: RootTest,
		rule: &conventions.SuiteRunner{
			Pkg: "gopkg.in/check.v1", Func: "Suite", Arg: 0, Prefixes: []string{testPrefix, benchmarkPrefix},
			Names: []string{"SetUpSuite", "SetUpTest", "TearDownTest", "TearDownSuite"},
		},
	},
	// go-fuzz: https://github.com/dvyukov/go-fuzz
	{
		kind: RootFuzz,
		rule: &conventions.TaggedEntry{Tag: "gofuzz", Prefix: "Fuzz"},
	},
}

// conventionRoots roots what every convention row's rule names in one file.
func (d *rootDetection) conventionRoots(p *packages.Package, f *ast.File) {
	file := d.pos.Fset.Position(f.FileStart).Filename
	directives := slices.Collect(conventions.Directives(f, file, f.Name.Name))
	for i := range conventionRows {
		row := &conventionRows[i]
		switch rule := row.rule.(type) {
		case *conventions.DocDirective:
			d.docDirective(rule, row.kind, f)
		case *conventions.GenerateInputs:
			d.generateDirectives(rule, row.kind, p, file, directives)
		case *conventions.PackageGenerator:
			for _, words := range directives {
				d.packageArguments(file, rule.Packages(words))
			}
		case *conventions.SuiteRunner:
			d.suiteMethods(rule, row.kind, p, f)
		case *conventions.TaggedEntry:
			d.taggedEntries(rule, row.kind, p, f)
		}
	}
}

// generateDirectives roots what each go:generate directive of the file at file
// names as the input of the rule's generator.
func (d *rootDetection) generateDirectives(rule *conventions.GenerateInputs, kind RootKind, p *packages.Package, file string, directives [][]string) {
	if p.Types == nil {
		return
	}
	for _, words := range directives {
		if named, ok := rule.Inputs(words); ok {
			d.generatorInputs(p, file, named, kind)
		}
	}
}

// taggedEntries roots every entry point of the rule that f declares, where a build
// that sets the rule's tag may compile f.
func (d *rootDetection) taggedEntries(rule *conventions.TaggedEntry, kind RootKind, p *packages.Package, f *ast.File) {
	if p.TypesInfo == nil || !rule.Compiles(f) {
		return
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && rule.Entry(p.TypesInfo, fn) {
			d.addAt(fn.Name.Pos(), kind)
		}
	}
}

// docDirective roots every declaration of f a doc-comment directive names.
func (d *rootDetection) docDirective(rule *conventions.DocDirective, kind RootKind, f *ast.File) {
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if rule.Holds(decl.Doc) {
				d.addAt(decl.Name.Pos(), kind)
			}
		case *ast.GenDecl:
			if rule.Of != conventions.Declarations {
				continue
			}
			for _, spec := range decl.Specs {
				d.directiveSpec(spec, kind, rule.Holds(decl.Doc) || rule.Holds(specDoc(spec)))
			}
		}
	}
}

func specDoc(spec ast.Spec) *ast.CommentGroup {
	switch spec := spec.(type) {
	case *ast.TypeSpec:
		return spec.Doc
	case *ast.ValueSpec:
		return spec.Doc
	}
	return nil
}

// directiveSpec roots one specification a directive names, and every field of a
// struct type it declares.
func (d *rootDetection) directiveSpec(spec ast.Spec, kind RootKind, named bool) {
	if !named {
		return
	}
	switch spec := spec.(type) {
	case *ast.TypeSpec:
		d.addAt(spec.Name.Pos(), kind)
		d.directiveFields(spec.Type, kind)
	case *ast.ValueSpec:
		for _, name := range spec.Names {
			d.addAt(name.Pos(), kind)
		}
	}
}

// directiveFields roots every field of a struct type expression, through anonymous
// struct types.
func (d *rootDetection) directiveFields(expr ast.Expr, kind RootKind) {
	structure, ok := expr.(*ast.StructType)
	if !ok {
		return
	}
	for _, field := range structure.Fields.List {
		for _, name := range field.Names {
			d.addAt(name.Pos(), kind)
		}
		d.directiveFields(field.Type, kind)
	}
}

// generatorInputs roots what one directive of the file at file names as a
// generator's input, and every method an input interface declares, which the
// generator reads as well: deleting one breaks go generate, whatever reads its output.
func (d *rootDetection) generatorInputs(p *packages.Package, file string, named conventions.Named, kind RootKind) {
	var objs []types.Object
	switch {
	case named.File != "":
		objs = d.interfacesOf(p, filepath.Join(filepath.Dir(file), named.File))
	case named.Package == ".":
		objs = lookup([]*types.Package{p.Types}, named.Names)
	default:
		scopes := make([]*types.Package, 0, len(d.byPath[named.Package]))
		for _, variant := range d.byPath[named.Package] {
			scopes = append(scopes, variant.Types)
		}
		objs = lookup(scopes, named.Names)
	}
	for _, obj := range objs {
		d.addObject(obj, kind)
		if iface, ok := obj.Type().Underlying().(*types.Interface); ok {
			for method := range iface.ExplicitMethods() {
				d.addObject(method, kind)
			}
		}
	}
}

// lookup is each name in the first scope that declares it.
func lookup(scopes []*types.Package, names []string) []types.Object {
	var held []types.Object
	for _, name := range names {
		for _, scope := range scopes {
			if obj := scope.Scope().Lookup(name); obj != nil {
				held = append(held, obj)
				break
			}
		}
	}
	return held
}

// interfacesOf is every interface type the file of p at that path declares.
func (d *rootDetection) interfacesOf(p *packages.Package, file string) []types.Object {
	var held []types.Object
	for _, f := range p.Syntax {
		if filepath.Clean(d.pos.Fset.Position(f.FileStart).Filename) != filepath.Clean(file) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if _, isInterface := spec.Type.(*ast.InterfaceType); isInterface && p.TypesInfo.Defs[spec.Name] != nil {
				held = append(held, p.TypesInfo.Defs[spec.Name])
			}
			return true
		})
	}
	return held
}

// packageArguments records the directory of every package below the target that
// one directive of the file at file passes by a relative path, the directive's own
// directory excepted.
func (d *rootDetection) packageArguments(file string, relative []string) {
	for _, arg := range relative {
		dir := filepath.Join(filepath.Dir(file), filepath.FromSlash(arg))
		if below := d.pos.Relative(dir); below != "" && dir != filepath.Dir(file) {
			d.inputDirs[below] = true
		}
	}
}

// generatorPackageInputs roots every declaration a package packageArguments
// recorded exports to a generator: an exported declaration outside a test file
// whose every container is exported too, a type parameter excepted.
func (d *rootDetection) generatorPackageInputs(symbols []inventory.Symbol) {
	if len(d.inputDirs) == 0 {
		return
	}
	for i := range symbols {
		s := &symbols[i]
		if d.inputDirs[path.Dir(s.Pos.Filename)] && s.Exported && s.Kind != inventory.KindTypeParam && !s.InTestFile && d.exportedContainers(s.Parent) {
			d.add(s.ID, RootGenerate, "")
		}
	}
}

// suiteMethods roots, as kind, the methods a suite runner calls on each value of a
// static type other than an interface that a call in f passes it as the suite.
func (d *rootDetection) suiteMethods(rule *conventions.SuiteRunner, kind RootKind, p *packages.Package, f *ast.File) {
	if p.TypesInfo == nil {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || rule.Arg >= len(call.Args) || !rule.Runs(callee(p.TypesInfo, call)) {
			return true
		}
		suite := p.TypesInfo.TypeOf(call.Args[rule.Arg])
		if suite == nil || types.IsInterface(suite) {
			return true
		}
		for selection := range types.NewMethodSet(suite).Methods() {
			if method := selection.Obj(); method.Exported() && rule.Calls(method.Name()) {
				d.addObject(method, kind)
			}
		}
		return true
	})
}

// callee is the function a call names, or a function of no package.
func callee(info *types.Info, call *ast.CallExpr) *types.Func {
	var name *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.SelectorExpr:
		name = fun.Sel
	case *ast.Ident:
		name = fun
	}
	if fn, ok := info.Uses[name].(*types.Func); ok {
		return fn
	}
	return types.NewFunc(0, nil, "", nil)
}
