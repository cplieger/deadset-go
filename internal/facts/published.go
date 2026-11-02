package facts

import (
	"go/types"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"golang.org/x/tools/go/packages"
)

// Published is what one configuration's published API reaches, which
// grammar/analysis/confidence.md states (confidence.3): the type of each exported
// declaration of a package of the published API, and in turn the type of each
// parameter, result, constraint, type argument, exported or embedded field and
// exported method of a type it reaches. It is the analysis's one published reach.
type Published struct {
	target     map[string]bool // the target's packages, by [resolve.Resolver.PackageKey]
	ifaces     map[*types.Interface]bool
	reached    map[string]bool // by [resolve.Resolver.TypeKey]
	named      map[*types.TypeName]bool
	resolve    *resolve.Resolver
	packages   []*packages.Package
	interfaces []*types.Interface
	names      []*types.TypeName
}

// Published is what the configuration's published API reaches.
func (x *Index) Published() *Published { return x.published }

// Packages is every package of the published API, in the walk order, each once.
func (p *Published) Packages() []*packages.Package { return p.packages }

// Target reports whether a package is one of the target's.
func (p *Published) Target(pkg *types.Package) bool {
	return pkg != nil && p.target[p.resolve.PackageKey(pkg)]
}

// Interfaces is every interface type the published API reaches, in the order it
// reaches them, each once.
func (p *Published) Interfaces() []*types.Interface { return p.interfaces }

// Names is every defined type and alias the published API reaches, in the order it
// reaches them, each once.
func (p *Published) Names() []*types.TypeName { return p.names }

// publishedOf reads the published API of one configuration's target.
func publishedOf(x *Index, rs *resolve.Resolver) *Published {
	p := &Published{
		target:  make(map[string]bool),
		reached: make(map[string]bool),
		named:   make(map[*types.TypeName]bool),
		ifaces:  make(map[*types.Interface]bool),
		resolve: rs,
	}
	for _, pkg := range x.packages {
		if pkg.Types == nil || pkg.Module == nil || !pkg.Module.Main || pkg.ForTest != "" || p.target[rs.PackageKey(pkg.Types)] {
			continue
		}
		p.target[rs.PackageKey(pkg.Types)] = true
		if load.Published(pkg.PkgPath, pkg.Name == "main") {
			p.packages = append(p.packages, pkg)
		}
	}
	for _, pkg := range p.packages {
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			if object := scope.Lookup(name); object.Exported() {
				p.reach(object.Type())
			}
		}
	}
	return p
}

// reach adds one type and what it reaches.
func (p *Published) reach(t types.Type) {
	if t == nil {
		return
	}
	if alias, isAlias := t.(*types.Alias); isAlias {
		p.name(alias.Obj())
	}
	t = types.Unalias(t)
	key := p.resolve.TypeKey(t)
	if p.reached[key] {
		return
	}
	p.reached[key] = true
	switch u := t.(type) {
	case *types.Named:
		p.name(u.Obj())
		p.reachNamed(u)
	case interface{ Elem() types.Type }:
		if m, isMap := u.(*types.Map); isMap {
			p.reach(m.Key())
		}
		p.reach(u.Elem())
	case *types.Signature:
		p.reachSignature(u)
	case *types.Struct:
		for field := range u.Fields() {
			if field.Exported() || field.Embedded() {
				p.reach(field.Type())
			}
		}
	case *types.Interface:
		p.iface(u)
		for embedded := range u.EmbeddedTypes() {
			p.reach(embedded)
		}
	}
}

// name records one defined type or alias the published API reaches.
func (p *Published) name(name *types.TypeName) {
	if !p.named[name] {
		p.named[name] = true
		p.names = append(p.names, name)
	}
}

// iface records one interface type the published API reaches.
func (p *Published) iface(u *types.Interface) {
	if !p.ifaces[u] {
		p.ifaces[u] = true
		p.interfaces = append(p.interfaces, u)
	}
}

// reachNamed reaches what a defined type holds: its type arguments, its exported
// methods' signatures, a promoted one included, and its underlying type. A defined
// interface is recorded whether or not an identical one was reached, since each
// declares methods of its own.
func (p *Published) reachNamed(u *types.Named) {
	if iface, isInterface := u.Underlying().(*types.Interface); isInterface {
		p.iface(iface)
	}
	for arg := range u.TypeArgs().Types() {
		p.reach(arg)
	}
	set := types.NewMethodSet(types.NewPointer(u))
	if types.IsInterface(u) {
		set = types.NewMethodSet(u)
	}
	for selection := range set.Methods() {
		if selection.Obj().Exported() {
			p.reach(selection.Obj().Type())
		}
	}
	p.reach(u.Underlying())
}

// reachSignature reaches the parameters, results and constraints of one function
// type.
func (p *Published) reachSignature(u *types.Signature) {
	for v := range u.Params().Variables() {
		p.reach(v.Type())
	}
	for v := range u.Results().Variables() {
		p.reach(v.Type())
	}
	for tp := range u.TypeParams().TypeParams() {
		p.reach(tp.Constraint())
	}
}
