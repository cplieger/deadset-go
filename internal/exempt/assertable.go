package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
)

// assertable answers which interfaces a package outside the analysed program can
// assert on a value it holds, each package's answer computed once.
type assertable struct {
	in         *Program
	own        map[*types.Package][]*types.Interface // per reading of a package, as each states its types
	imported   map[*types.Package]importedSet
	assertions map[*types.Package][]*types.Interface
	selfNamed  map[string]map[string]bool // by package path, the names [assertable.assertsOwn] reads
}

// importedSet is what the packages one package imports at any depth can name: their
// exported interfaces, and whether a template engine is among them.
type importedSet struct {
	interfaces []*types.Interface
	templates  bool
}

// newAssertable starts the answer over one program.
func newAssertable(in *Program) *assertable {
	return &assertable{
		in:         in,
		own:        make(map[*types.Package][]*types.Interface),
		imported:   make(map[*types.Package]importedSet),
		assertions: make(map[*types.Package][]*types.Interface),
		selfNamed:  make(map[string]map[string]bool),
	}
}

// ownInterfaces is every interface one package declares, exported or not, and every
// interface type literal its source writes.
func (a *assertable) ownInterfaces(pkg *types.Package) []*types.Interface {
	if held, known := a.own[pkg]; known {
		return held
	}
	held := append(declaredInterfaces(pkg, true), a.literals(pkg)...)
	a.own[pkg] = held
	return held
}

// importedBy is what the packages one package imports at any depth can name, the
// package itself counted for the template engines alone.
func (a *assertable) importedBy(pkg *types.Package) importedSet {
	if held, known := a.imported[pkg]; known {
		return held
	}
	held := importedSet{templates: templateEngine(pkg.Path())}
	seen := map[string]bool{pkg.Path(): true}
	queue := slices.Clone(pkg.Imports())
	for _, one := range queue {
		seen[one.Path()] = true
	}
	for len(queue) > 0 {
		one := queue[0]
		queue = queue[1:]
		held.templates = held.templates || templateEngine(one.Path())
		held.interfaces = append(held.interfaces, declaredInterfaces(one, false)...)
		for _, imported := range one.Imports() {
			if !seen[imported.Path()] {
				seen[imported.Path()] = true
				queue = append(queue, imported)
			}
		}
	}
	a.imported[pkg] = held
	return held
}

// literals is every interface type literal one package's source writes that holds
// methods alone and that the loaded program can state ([load.Sources.Read]). A
// package-level type declaration is the scope's ([declaredInterfaces]), so its
// literal is not read twice.
func (a *assertable) literals(pkg *types.Package) []*types.Interface {
	source, ok := a.in.sources.Read(pkg)
	if !ok {
		return nil
	}
	local := localizer{pkg: pkg}
	var found []*types.Interface
	for _, file := range source.Files {
		declared := packageLevelTypes(file)
		ast.Inspect(file, func(n ast.Node) bool {
			literal, isInterface := n.(*ast.InterfaceType)
			if !isInterface || declared[literal] {
				return true
			}
			iface, checked := source.Info.TypeOf(literal).(*types.Interface)
			if !checked || !facts.HoldsValues(iface) {
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

// asserted is every interface one package outside the program asserts: the interface
// type of each type assertion and each type switch case its source writes, and the
// interface of each errors.As and errors.AsType call there, wherever it stands and
// whatever value it asserts on, read from its source ([load.Sources.Read]). A case
// naming a type that is no interface asserts nothing.
func (a *assertable) asserted(pkg *types.Package) []*types.Interface {
	if held, known := a.assertions[pkg]; known {
		return held
	}
	a.assertions[pkg] = nil
	source, ok := a.in.sources.Read(pkg)
	if !ok {
		return nil
	}
	local := localizer{pkg: pkg}
	var found []*types.Interface
	for _, file := range source.Files {
		for _, asserted := range assertedTypes(file, source.Info) {
			if iface, stated := local.assertedInterface(asserted); stated {
				found = append(found, iface)
			}
		}
	}
	a.assertions[pkg] = found
	return found
}

// assertsOwn reports whether the source of the package declaring one defined
// interface asserts that interface itself, by type assertion, type switch case,
// errors.As or errors.AsType ([assertedTypes]). Each package's source is read once,
// and only when asked.
func (a *assertable) assertsOwn(iface *types.Named) bool {
	pkg := iface.Obj().Pkg()
	names, known := a.selfNamed[pkg.Path()]
	if !known {
		names = a.ownAsserted(pkg)
		a.selfNamed[pkg.Path()] = names
	}
	return names[iface.Obj().Name()]
}

// ownAsserted is the name of every package-level type of one package outside the
// program that the package's own source asserts.
func (a *assertable) ownAsserted(pkg *types.Package) map[string]bool {
	names := make(map[string]bool)
	source, ok := a.in.sources.Read(pkg)
	if !ok {
		return names
	}
	for _, file := range source.Files {
		for _, asserted := range assertedTypes(file, source.Info) {
			named, isNamed := types.Unalias(asserted).(*types.Named)
			if !isNamed || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != pkg.Path() ||
				named.Obj().Parent() != named.Obj().Pkg().Scope() {
				continue
			}
			names[named.Obj().Name()] = true
		}
	}
	return names
}

// assertedTypes is the type every type assertion and every type switch case one file
// writes names, and the target type of every errors.As and errors.AsType call it
// writes. A case of an expression switch is no assertion.
func assertedTypes(file *ast.File, info *types.Info) []types.Type {
	var found []types.Type
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.TypeAssertExpr:
			if n.Type != nil {
				found = append(found, info.TypeOf(n.Type))
			}
		case *ast.TypeSwitchStmt:
			found = append(found, caseTypes(n, info)...)
		case *ast.CallExpr:
			if target := facts.ErrorsAsTarget(info, n); target != nil {
				found = append(found, target)
			}
		}
		return true
	})
	return found
}

// caseTypes is the type every case of one type switch names.
func caseTypes(n *ast.TypeSwitchStmt, info *types.Info) []types.Type {
	var found []types.Type
	for _, stmt := range n.Body.List {
		if clause, isCase := stmt.(*ast.CaseClause); isCase {
			for _, expr := range clause.List {
				found = append(found, info.TypeOf(expr))
			}
		}
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

// localizer states a type a source reading checked in the loaded program's types.
// The reading checks the package anew, so a type the package declares is a second
// object there, which no type of the program is identical to.
type localizer struct {
	pkg *types.Package
}

// of is t in the loaded program's types ([facts.Restated]), and false where it has
// no counterpart.
func (l localizer) of(t types.Type) (types.Type, bool) {
	return facts.Restated(t, func(p *types.Package) *types.Package {
		if p.Path() == l.pkg.Path() {
			return l.pkg
		}
		return p
	})
}

// iface is one interface type in the loaded program's types.
func (l localizer) iface(iface *types.Interface) (*types.Interface, bool) {
	stated, ok := l.of(iface)
	if !ok {
		return nil, false
	}
	loaded, _ := stated.(*types.Interface)
	return loaded, true
}

// assertedInterface is the interface an asserted type is in the loaded program's
// types, and false for a type that is no interface with methods.
func (l localizer) assertedInterface(t types.Type) (*types.Interface, bool) {
	if t == nil || !types.IsInterface(t) {
		return nil, false
	}
	loaded, stated := l.of(t)
	if !stated {
		return nil, false
	}
	iface, isInterface := loaded.Underlying().(*types.Interface)
	if !isInterface || !facts.HoldsValues(iface) {
		return nil, false
	}
	return iface, true
}
