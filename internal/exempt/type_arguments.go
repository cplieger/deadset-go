package exempt

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"

	"golang.org/x/tools/go/packages"
)

// standardDecoder reports whether one package is a standard decoder, whose decoding
// entry points a type argument of a generic of a package importing it reaches.
func standardDecoder(path string) bool {
	return destinationPackages[path]&reachDecoding != 0
}

// typeArguments is every type argument each type parameter of the program takes in
// an instantiation the program holds, a parameter keyed by the position it is
// declared at so the variants that type-check one file share it, and a generic
// method's receiver type parameter answering for its type's own.
type typeArguments struct {
	args     map[token.Pos][]types.Type
	receiver map[token.Pos]token.Pos
}

// typeArgumentsOf collects the instantiations of every package of the program.
func typeArgumentsOf(pkgs []*packages.Package) *typeArguments {
	held := &typeArguments{args: make(map[token.Pos][]types.Type), receiver: make(map[token.Pos]token.Pos)}
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		for id, instance := range p.TypesInfo.Instances {
			params := typeParameters(p.TypesInfo.Uses[id])
			for i := range min(params.Len(), instance.TypeArgs.Len()) {
				key := params.At(i).Obj().Pos()
				held.args[key] = append(held.args[key], instance.TypeArgs.At(i))
			}
		}
		for _, object := range p.TypesInfo.Defs {
			held.keepReceiver(object)
		}
	}
	return held
}

// keepReceiver keys the receiver type parameters of one generic method to the type
// parameters of the type it is declared on.
func (a *typeArguments) keepReceiver(object types.Object) {
	method, isFunc := object.(*types.Func)
	if !isFunc || method.Signature().RecvTypeParams().Len() == 0 {
		return
	}
	recv := types.Unalias(method.Signature().Recv().Type())
	if pointer, isPointer := recv.(*types.Pointer); isPointer {
		recv = types.Unalias(pointer.Elem())
	}
	named, isNamed := recv.(*types.Named)
	if !isNamed {
		return
	}
	declared, own := method.Signature().RecvTypeParams(), named.Origin().TypeParams()
	for i := range min(declared.Len(), own.Len()) {
		a.receiver[declared.At(i).Obj().Pos()] = own.At(i).Obj().Pos()
	}
}

// typeParameters is the type parameters of the generic declaration one instantiated
// identifier names.
func typeParameters(object types.Object) *types.TypeParamList {
	switch t := object.(type) {
	case *types.Func:
		return t.Signature().TypeParams()
	case *types.TypeName:
		switch declared := t.Type().(type) {
		case *types.Named:
			return declared.TypeParams()
		case *types.Alias:
			return declared.TypeParams()
		}
	}
	return nil
}

// bindings is every type with no type parameter in it that a type parameter t holds
// stands for in an instantiation the program holds, through the type parameters of
// other generic declarations it is passed on to as a type argument.
func (a *typeArguments) bindings(t types.Type) []types.Type {
	var found []types.Type
	seen := make(map[token.Pos]bool)
	var expand func(types.Type)
	expand = func(t types.Type) {
		for _, param := range typeParamsIn(t) {
			key := param.Obj().Pos()
			if own, isReceiver := a.receiver[key]; isReceiver {
				key = own
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			for _, arg := range a.args[key] {
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

// walkTypeArguments records every type argument the program passes to a generic
// that reaches a destination by its declaration alone: a generic function of a
// destination package, and a generic function, method or type of a package outside
// the program whose own imports name a standard decoder, which reaches a decoding
// entry point of each such decoder whether or not the generic's body decodes it.
func (f *encodingFlow) walkTypeArguments() error {
	own := make(map[string]bool)
	pkgs := f.kept.in.Facts.Program()
	for _, p := range pkgs {
		own[p.PkgPath] = true
	}
	imports := sourceImports(pkgs)
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		if err := f.packageTypeArguments(p.TypesInfo, own, imports); err != nil {
			return err
		}
	}
	return nil
}

// packageTypeArguments records the type arguments of one package's instantiations,
// in position order.
func (f *encodingFlow) packageTypeArguments(info *types.Info, own map[string]bool, imports map[string][]string) error {
	ids := slices.SortedFunc(maps.Keys(info.Instances), func(a, b *ast.Ident) int { return cmp.Compare(a.Pos(), b.Pos()) })
	for _, id := range ids {
		d, reaches := f.typeArgumentDestination(info.Uses[id], own, imports)
		if !reaches {
			if err := f.madeOutside(info, id, own); err != nil {
				return err
			}
			continue
		}
		for arg := range info.Instances[id].TypeArgs.Types() {
			if err := f.retain(arg, id.Pos(), &d); err != nil {
				return err
			}
		}
	}
	return nil
}

// madeOutside records as filled the type arguments of one instantiated generic
// function outside the program that bind a type parameter its results mention and
// none of its parameters does: no argument hands the value over, so the callee makes
// it, filling it where the analysis cannot see, as a decoding client does.
func (f *encodingFlow) madeOutside(info *types.Info, id *ast.Ident, own map[string]bool) error {
	fn, isFunc := info.Uses[id].(*types.Func)
	if !isFunc || fn.Pkg() == nil || own[fn.Pkg().Path()] || namedDestinations[fn.Pkg().Path()] {
		return nil
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
	d := destination{detail: "made by " + fn.FullName(), reach: reach{}.filling()}
	args := info.Instances[id].TypeArgs
	for i := range sig.TypeParams().Len() {
		if i < args.Len() && made[sig.TypeParams().At(i)] {
			if err := f.retain(args.At(i), id.Pos(), &d); err != nil {
				return err
			}
		}
	}
	return nil
}

// typeArgumentDestination is what a type argument of one instantiated generic
// reaches, and false for a generic that reaches no destination by its declaration.
func (f *encodingFlow) typeArgumentDestination(object types.Object, own map[string]bool,
	imports map[string][]string,
) (destination, bool) {
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
	if own[pkg.Path()] {
		return destination{}, false
	}
	var decoded reach
	var decodes bool
	for _, path := range imports[pkg.Path()] {
		if standardDecoder(path) {
			decoded = decoded.union(entryPoint(path, "Decode", destinationPackages[path], f.unknownMembers))
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
		paths := make([]string, 0, len(p.Imports))
		for _, imported := range p.Imports {
			paths = append(paths, imported.PkgPath)
		}
		slices.Sort(paths)
		held[p.PkgPath] = paths
	})
	return held
}
