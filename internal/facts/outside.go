package facts

import (
	"cmp"
	"go/types"
	"slices"
)

// OutsideMethod is one exported method of an exported, non-generic interface type,
// a constraint's among them ([HoldsValues]), that a package of the program's import
// closure declares and the target does not.
// Consumer marks a method a loaded consumer's own package declares, which is
// outside the target and inside the program the analysis reads.
type OutsideMethod struct {
	Interface *types.Interface
	Method    *types.Func
	Consumer  bool
}

// outside is the import closure of the program's packages: the packages outside
// the program, and the interface methods declared outside the target, by name.
type outside struct {
	methods  map[string][]OutsideMethod
	packages []*types.Package
}

// Outside is every package of the import closure of the program's packages that is
// none of them, ordered by path.
func (x *Index) Outside() []*types.Package { return x.outside.packages }

// OutsideMethods is every [OutsideMethod] named name.
func (x *Index) OutsideMethods(name string) []OutsideMethod { return x.outside.methods[name] }

// outsideOf walks the import closure of x's program. Each module of the target
// type-checks its imports anew, and a type is identical only to its own reading's,
// so every reading of a path is a package of its own here.
func outsideOf(x *Index) outside {
	target := make(map[string]bool)
	for _, p := range x.packages {
		if p.Types != nil {
			target[p.PkgPath] = true
		}
	}
	program := make(map[string]bool)
	var queue []*types.Package
	for _, p := range x.Program() {
		if p.Types != nil {
			program[p.PkgPath] = true
			queue = append(queue, p.Types)
		}
	}
	found := outside{methods: make(map[string][]OutsideMethod)}
	seen := make(map[*types.Package]bool)
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		queue = append(queue, pkg.Imports()...)
		if !program[pkg.Path()] {
			found.packages = append(found.packages, pkg)
		}
		if !target[pkg.Path()] {
			found.index(pkg, program[pkg.Path()])
		}
	}
	slices.SortStableFunc(found.packages, func(a, b *types.Package) int { return cmp.Compare(a.Path(), b.Path()) })
	return found
}

// index adds the exported methods of one package's exported interface types.
func (o *outside) index(pkg *types.Package, consumer bool) {
	for _, named := range DeclaredInterfaces(pkg) {
		if !named.Obj().Exported() {
			continue
		}
		iface, _ := named.Underlying().(*types.Interface)
		for method := range iface.Methods() {
			if method.Exported() {
				o.methods[method.Name()] = append(o.methods[method.Name()], OutsideMethod{Interface: iface, Method: method, Consumer: consumer})
			}
		}
	}
}
