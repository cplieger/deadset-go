package exempt

import (
	"go/ast"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"golang.org/x/tools/go/packages"
)

// bindings is every type with no type parameter in it that a type parameter t holds
// stands for in an instantiation the program holds, through the type parameters of
// other generic declarations it is passed on to as a type argument.
func bindings(in *facts.Instantiations, t types.Type) []types.Type {
	var found []types.Type
	seen := make(map[inventory.Offset]bool)
	var expand func(types.Type)
	expand = func(t types.Type) {
		for _, param := range typeParamsIn(t) {
			key := in.Declared(param)
			if seen[key] {
				continue
			}
			seen[key] = true
			for _, arg := range in.Args(key) {
				if len(typeParamsIn(arg)) == 0 {
					found = append(found, arg)
				}
				expand(arg)
			}
		}
	}
	expand(t)
	return found
}

// typeParamsIn is every type parameter one type is built from: itself, or one under
// a pointer, a slice, an array, a map, a channel, a type argument or a field of a
// struct type with no name.
func typeParamsIn(t types.Type) []*types.TypeParam {
	var found []*types.TypeParam
	seen := make(map[types.Type]bool)
	stack := []types.Type{t}
	for len(stack) > 0 {
		cur := types.Unalias(stack[len(stack)-1])
		stack = stack[:len(stack)-1]
		if cur == nil || seen[cur] {
			continue
		}
		seen[cur] = true
		switch u := cur.(type) {
		case *types.TypeParam:
			found = append(found, u)
		case *types.Named:
			for arg := range u.TypeArgs().Types() {
				stack = append(stack, arg)
			}
		case *types.Pointer:
			stack = append(stack, u.Elem())
		case *types.Slice:
			stack = append(stack, u.Elem())
		case *types.Array:
			stack = append(stack, u.Elem())
		case *types.Chan:
			stack = append(stack, u.Elem())
		case *types.Map:
			stack = append(stack, u.Key(), u.Elem())
		case *types.Struct:
			for field := range u.Fields() {
				stack = append(stack, field.Type())
			}
		}
	}
	return found
}

// typeArgumentArrival is what the type arguments of one instantiation carry: every
// one of a generic that reaches a destination by its declaration alone, a generic
// function of a destination package, and a generic function, method or type of a
// package outside the program whose own imports name a standard decoder, which
// reaches a decoding entry point of each such decoder whether or not the generic's
// body decodes it; and otherwise the ones [encodingFlow.madeOutside] fills.
func (f *encodingFlow) typeArgumentArrival(info *types.Info, id *ast.Ident, imports map[string][]string) (arrival, bool) {
	d, reaches := f.typeArgumentDestination(info.Uses[id], imports)
	if !reaches {
		return f.madeOutside(info, id)
	}
	return arrival{d: d, types: slices.Collect(info.Instances[id].TypeArgs.Types()), at: id.Pos()}, true
}

// madeOutside is the type arguments of one instantiated generic function outside the
// program that bind a type parameter its results mention and none of its parameters
// does, recorded as filled: no argument hands the value over, so the callee makes
// it, filling it where the analysis cannot see, as a decoding client does.
func (f *encodingFlow) madeOutside(info *types.Info, id *ast.Ident) (arrival, bool) {
	fn, isFunc := info.Uses[id].(*types.Func)
	if !isFunc || fn.Pkg() == nil || f.own[fn.Pkg().Path()] || namedDestination(fn.Pkg().Path()) {
		return arrival{}, false
	}
	sig := fn.Signature()
	passed := make(map[*types.TypeParam]bool)
	for param := range sig.Params().Variables() {
		for _, tp := range typeParamsIn(param.Type()) {
			passed[tp] = true
		}
	}
	made := make(map[*types.TypeParam]bool)
	for result := range sig.Results().Variables() {
		for _, tp := range typeParamsIn(result.Type()) {
			made[tp] = !passed[tp]
		}
	}
	a := arrival{d: destination{detail: "made by " + fn.FullName(), reach: reach{}.filling()}, at: id.Pos()}
	args := info.Instances[id].TypeArgs
	for i := range sig.TypeParams().Len() {
		if i < args.Len() && made[sig.TypeParams().At(i)] {
			a.types = append(a.types, args.At(i))
		}
	}
	return a, len(a.types) > 0
}

// typeArgumentDestination is what a type argument of one instantiated generic
// reaches, and false for a generic that reaches no destination by its declaration.
func (f *encodingFlow) typeArgumentDestination(object types.Object, imports map[string][]string) (destination, bool) {
	if object == nil || object.Pkg() == nil {
		return destination{}, false
	}
	if fn, isFunc := object.(*types.Func); isFunc {
		if d, reaches := f.encodingDestination(fn); reaches {
			d.detail = "passed as a type argument to " + fn.FullName()
			return d, true
		}
	}
	pkg := object.Pkg()
	if f.own[pkg.Path()] {
		return destination{}, false
	}
	var decoded reach
	var decodes bool
	for _, path := range imports[pkg.Path()] {
		if row := destinations[path]; row.methods&reachDecoding != 0 {
			decoded = decoded.union(entryPoint(&row, "Decode", f.strict[path]))
			decodes = true
		}
	}
	if !decodes {
		return destination{}, false
	}
	return destination{detail: "passed as a type argument to " + pkg.Path() + "." + object.Name(), reach: decoded}, true
}

// sourceImports is, for every package the program's import graph holds, the paths
// its own import declarations name, in path order.
func sourceImports(pkgs []*packages.Package) map[string][]string {
	held := make(map[string][]string)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if _, seen := held[p.PkgPath]; seen {
			return
		}
		imports := make([]string, 0, len(p.Imports))
		for _, imported := range p.Imports {
			imports = append(imports, imported.PkgPath)
		}
		slices.Sort(imports)
		held[p.PkgPath] = imports
	})
	return held
}
