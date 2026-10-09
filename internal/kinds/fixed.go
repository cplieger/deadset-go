package kinds

import (
	"go/types"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

// fixedByOutsideInterface is every method of the target whose name and signature are
// those of a method of an exported interface type declared by a package outside the
// target that the program's import closure holds, computed once per pass. That
// interface fixes the method's signature and its exportedness whether or not the
// program converts the method's type to it, so the method is neither narrowed nor
// judged for its parameters and results.
func (in *Input) fixedByOutsideInterface() map[graph.SymbolID]bool {
	if in.fixed != nil {
		return in.fixed
	}
	in.fixed = make(map[graph.SymbolID]bool)
	for i := range in.Per {
		if one := &in.Per[i]; one.Result != nil && one.Resolve != nil {
			fixedIn(one, in.fixed)
		}
	}
	return in.fixed
}

// fixedIn adds the methods an outside interface fixes in one configuration to fixed.
func fixedIn(one *Configured, fixed map[graph.SymbolID]bool) {
	fixing := outsideInterfaceMethods(one.Result)
	if len(fixing) == 0 {
		return
	}
	for _, p := range one.Result.Packages {
		if p.TypesInfo == nil {
			continue
		}
		for _, object := range p.TypesInfo.Defs {
			if id, isFixed := fixedMethod(one.Resolve, object, fixing); isFixed {
				fixed[id] = true
			}
		}
	}
}

// fixedMethod reports whether one definition is a method of the inventory that a
// method of fixing, by name, matches in signature. Receivers are not compared,
// because [types.Identical] ignores a signature's receiver.
func fixedMethod(resolve *graph.Resolver, object types.Object, fixing map[string][]*types.Func) (graph.SymbolID, bool) {
	fn, isFunc := object.(*types.Func)
	if !isFunc || fn.Signature().Recv() == nil {
		return "", false
	}
	for _, method := range fixing[fn.Name()] {
		if types.Identical(fn.Signature(), method.Signature()) {
			return resolve.Object(fn)
		}
	}
	return "", false
}

// outsideInterfaceMethods indexes by name every exported method of every exported,
// non-generic interface type declared at package level by a package of the program's
// import closure that is not a target package, a loaded consumer's own among them.
func outsideInterfaceMethods(r *load.Result) map[string][]*types.Func {
	target := make(map[string]bool)
	var queue []*types.Package
	for _, p := range r.Packages {
		if p.Types != nil {
			target[p.PkgPath] = true
			queue = append(queue, p.Types)
		}
	}
	for i := range r.Consumers {
		for _, p := range r.Consumers[i].Packages {
			if p.Types != nil && !target[p.PkgPath] {
				queue = append(queue, p.Types)
			}
		}
	}
	seen := make(map[string]bool)
	found := make(map[string][]*types.Func)
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if seen[pkg.Path()] {
			continue
		}
		seen[pkg.Path()] = true
		queue = append(queue, pkg.Imports()...)
		if !target[pkg.Path()] {
			indexExportedInterfaces(pkg, found)
		}
	}
	return found
}

// indexExportedInterfaces adds the exported methods of one package's exported,
// non-generic interface types to found.
func indexExportedInterfaces(pkg *types.Package, found map[string][]*types.Func) {
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		typeName, isType := scope.Lookup(name).(*types.TypeName)
		if !isType || !typeName.Exported() || typeName.IsAlias() {
			continue
		}
		named, isNamed := typeName.Type().(*types.Named)
		if !isNamed || named.TypeParams().Len() > 0 {
			continue
		}
		iface, isInterface := named.Underlying().(*types.Interface)
		if !isInterface {
			continue
		}
		for method := range iface.Methods() {
			if method.Exported() {
				found[method.Name()] = append(found[method.Name()], method)
			}
		}
	}
}
