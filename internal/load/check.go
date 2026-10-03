package load

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"golang.org/x/tools/go/packages"
)

// The parse modes of the two kinds of package a load checks. A package whose
// syntax the analysis walks keeps its comments, where directives and suppressions
// live; a package checked for its declarations alone keeps no syntax at all, and
// the per-file Go version its build constraint sets is recorded either way.
const (
	fullParse      = parser.AllErrors | parser.ParseComments
	signatureParse = parser.AllErrors | parser.SkipObjectResolution
)

// typeCheck type-checks every package reachable from roots, each after the
// packages it imports, and records what each reported in its Errors. A package of
// the main module, a package of module (the target module in a consumer's load,
// empty otherwise) and any package importing one of those are checked whole, their
// syntax and type information kept for the analysis to walk. Every other package is
// checked for its declarations alone, function bodies skipped and only its types
// kept. A type error in a package of the main module, or of module, is recorded in
// the package's TypeErrors alone and fails nothing, because the analysis skips the
// declaration that holds it; a type error in any other package checked for its
// declarations is no error of the program and is dropped.
func typeCheck(ctx context.Context, fset *token.FileSet, roots []*packages.Package, arch, module string) error {
	return check(ctx, fset, roots, arch, func(p *packages.Package) bool {
		return p.Module != nil && (module == "" && p.Module.Main || module != "" && p.Module.Path == module)
	}, true)
}

// typeCheckDeclarations type-checks every package reachable from roots for its
// declarations alone, none of them whole, as typeCheck checks a dependency.
func typeCheckDeclarations(ctx context.Context, fset *token.FileSet, roots []*packages.Package, arch string) error {
	return check(ctx, fset, roots, arch, func(*packages.Package) bool { return false }, false)
}

// check runs one load's checks: own names the packages whose type errors skip a
// declaration rather than fail the load, and mainWhole whether the main module's
// packages, and every package importing one, are checked whole.
func check(ctx context.Context, fset *token.FileSet, roots []*packages.Package, arch string,
	own func(*packages.Package) bool, mainWhole bool,
) error {
	sizes := types.SizesFor(gcCompiler, arch)
	if sizes == nil {
		return fmt.Errorf("no type sizes for architecture %q", arch)
	}
	c := &checker{
		ctx:    ctx,
		fset:   fset,
		sizes:  sizes,
		parsed: make(map[string]*parsedFile),
		cpu:    make(chan struct{}, runtime.GOMAXPROCS(0)),
	}
	c.run(schedule(roots, own, mainWhole))
	return ctx.Err()
}

// checkNode is one package of the import graph, with the packages that import it
// and the number of its own imports not yet checked.
type checkNode struct {
	pkg     *packages.Package
	preds   []*checkNode
	pending atomic.Int32
	whole   bool
	program bool // a type error in the package skips its declaration rather than failing the load
}

// schedule builds the graph typeCheck walks, in import order, deciding which
// packages are checked whole.
func schedule(roots []*packages.Package, own func(*packages.Package) bool, mainWhole bool) []*checkNode {
	nodes := make(map[*packages.Package]*checkNode)
	var order []*checkNode
	packages.Visit(roots, nil, func(p *packages.Package) {
		program := own(p)
		n := &checkNode{pkg: p, whole: program || mainWhole && p.Module != nil && p.Module.Main, program: program}
		seen := make(map[*checkNode]bool, len(p.Imports))
		for _, imported := range p.Imports {
			in := nodes[imported]
			if seen[in] {
				continue
			}
			seen[in] = true
			n.whole = n.whole || mainWhole && in.whole
			n.pending.Add(1)
			in.preds = append(in.preds, n)
		}
		nodes[p] = n
		order = append(order, n)
	})
	return order
}

// checker holds what one load's checks share: the file set, the type sizes of the
// configuration, the parse cache of the packages checked whole, and the bound on
// how many parses and checks run at once.
type checker struct {
	ctx    context.Context
	fset   *token.FileSet
	sizes  types.Sizes
	parsed map[string]*parsedFile
	cpu    chan struct{}
	mu     sync.Mutex
}

// parsedFile is one file's parse, shared by every package that compiles it.
type parsedFile struct {
	file  *ast.File
	err   error
	ready chan struct{}
}

// run checks every node once its imports are checked, starting from the packages
// that import nothing.
func (c *checker) run(nodes []*checkNode) {
	var wg sync.WaitGroup
	var start func(n *checkNode)
	start = func(n *checkNode) {
		wg.Go(func() {
			c.check(n.pkg, n.whole, n.program)
			for _, pred := range n.preds {
				if pred.pending.Add(-1) == 0 {
					start(pred)
				}
			}
		})
	}
	// The leaves are taken before any check starts: a running check brings its
	// importers' counts to zero, and those it starts itself.
	var leaves []*checkNode
	for _, n := range nodes {
		if n.pending.Load() == 0 {
			leaves = append(leaves, n)
		}
	}
	for _, n := range leaves {
		start(n)
	}
	wg.Wait()
}

// check parses and type-checks one package whose imports are already checked.
func (c *checker) check(p *packages.Package, whole, program bool) {
	p.Fset = c.fset
	p.TypesSizes = c.sizes
	if p.PkgPath == "unsafe" {
		p.Types = types.Unsafe
		p.Syntax = []*ast.File{}
		p.TypesInfo = newTypesInfo()
		return
	}
	p.Types = types.NewPackage(p.PkgPath, p.Name)
	if c.ctx.Err() != nil {
		return
	}

	files, errs := c.parseFiles(p.CompiledGoFiles, whole)
	for _, err := range errs {
		appendError(p, err)
	}
	var info *types.Info
	if whole {
		info = newTypesInfo()
		p.Syntax = files
		p.TypesInfo = info
	}
	conf := &types.Config{
		Importer:         importer{pkg: p},
		IgnoreFuncBodies: !whole,
		Error:            typeErrorSink(p, whole, program),
		Sizes:            c.sizes,
	}
	if p.Module != nil && p.Module.GoVersion != "" {
		conf.GoVersion = "go" + p.Module.GoVersion
	}

	c.cpu <- struct{}{}
	err := types.NewChecker(conf, c.fset, p.Types, info).Files(files)
	<-c.cpu
	if _, typed := err.(types.Error); err != nil && len(p.Errors) == 0 && !typed {
		appendError(p, err)
	}
	p.IllTyped = len(p.Errors) > 0
	for _, imported := range p.Imports {
		p.IllTyped = p.IllTyped || imported.IllTyped
	}
}

// typeErrorSink is where one package's checker sends its errors: a type error of a
// package the program holds is recorded to skip a declaration, one of a package
// checked for its declarations alone is dropped, and every other error is the
// package's.
func typeErrorSink(p *packages.Package, whole, program bool) func(error) {
	return func(err error) {
		if typed, ok := err.(types.Error); ok && (program || !whole) {
			if program {
				p.TypeErrors = append(p.TypeErrors, typed)
			}
			return
		}
		appendError(p, err)
	}
}

// parseFiles parses one package's files, and returns the trees of the files that
// parsed at least in part together with every error.
func (c *checker) parseFiles(names []string, whole bool) ([]*ast.File, []error) {
	files := make([]*ast.File, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { files[i], errs[i] = c.parse(name, whole) })
	}
	wg.Wait()

	parsed := make([]*ast.File, 0, len(files))
	for _, f := range files {
		if f != nil {
			parsed = append(parsed, f)
		}
	}
	failed := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err)
		}
	}
	return parsed, failed
}

// parse reads one file. A file of a package checked whole is parsed once per load
// and shared by every variant that compiles it, so one source site is one
// position. A file of a package checked for its declarations is parsed afresh and
// its tree dropped with the check: only such a package compiles it.
func (c *checker) parse(name string, whole bool) (*ast.File, error) {
	if !whole {
		return c.parseOnce(name, signatureParse)
	}
	c.mu.Lock()
	v, held := c.parsed[name]
	if !held {
		v = &parsedFile{ready: make(chan struct{})}
		c.parsed[name] = v
	}
	c.mu.Unlock()
	if held {
		<-v.ready
		return v.file, v.err
	}
	v.file, v.err = c.parseOnce(name, fullParse)
	close(v.ready)
	return v.file, v.err
}

// parseOnce reads and parses one file under mode.
func (c *checker) parseOnce(name string, mode parser.Mode) (*ast.File, error) {
	src, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	c.cpu <- struct{}{}
	defer func() { <-c.cpu }()
	return parser.ParseFile(c.fset, name, src, mode)
}

// importer resolves the imports of one package from the packages its own import
// map names, which are checked before it is.
type importer struct {
	pkg *packages.Package
}

// Import resolves one import path.
func (im importer) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	imported := im.pkg.Imports[path]
	if imported == nil {
		return nil, fmt.Errorf("no metadata for %s", path)
	}
	if imported.Types == nil || !imported.Types.Complete() {
		return nil, fmt.Errorf("no complete types for %s", path)
	}
	return imported.Types, nil
}

// appendError records one parse or type error on p in the form the toolchain's
// own loader gives it, so a diagnostic reads the same whichever reported it.
func appendError(p *packages.Package, err error) {
	switch err := err.(type) {
	case *os.PathError:
		p.Errors = append(p.Errors, packages.Error{Pos: err.Path + ":1", Msg: err.Err.Error(), Kind: packages.ParseError})
	case scanner.ErrorList:
		for _, e := range err {
			p.Errors = append(p.Errors, packages.Error{Pos: e.Pos.String(), Msg: e.Msg, Kind: packages.ParseError})
		}
	case types.Error:
		p.TypeErrors = append(p.TypeErrors, err)
		p.Errors = append(p.Errors, packages.Error{Pos: err.Fset.Position(err.Pos).String(), Msg: err.Msg, Kind: packages.TypeError})
	default:
		p.Errors = append(p.Errors, packages.Error{Pos: "-", Msg: err.Error(), Kind: packages.UnknownError})
	}
}

// newTypesInfo is the type information a package checked whole records.
func newTypesInfo() *types.Info {
	return &types.Info{
		Types:        make(map[ast.Expr]types.TypeAndValue),
		Defs:         make(map[*ast.Ident]types.Object),
		Uses:         make(map[*ast.Ident]types.Object),
		Implicits:    make(map[ast.Node]types.Object),
		Instances:    make(map[*ast.Ident]types.Instance),
		Scopes:       make(map[ast.Node]*types.Scope),
		Selections:   make(map[*ast.SelectorExpr]*types.Selection),
		FileVersions: make(map[*ast.File]string),
	}
}
