package facts

import (
	"go/types"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"golang.org/x/tools/go/packages"
)

// Instantiations is every type argument each type parameter takes in an
// instantiation the packages it was built from hold. A parameter is keyed by where
// it is written, so a consumer's reading of a target file instantiates the target's
// own parameter, and a generic method's receiver type parameter answers for its
// type's own.
type Instantiations struct {
	pos      *inventory.Positions
	args     map[inventory.Offset][]types.Type
	spelled  map[inventory.Offset]map[string]bool
	receiver map[inventory.Offset]inventory.Offset
}

// instantiationsOf collects the instantiations of every package of pkgs, whatever
// reading of the file set pos renders each belongs to.
func instantiationsOf(pkgs []*packages.Package, pos *inventory.Positions) *Instantiations {
	in := &Instantiations{
		pos:      pos,
		args:     make(map[inventory.Offset][]types.Type),
		spelled:  make(map[inventory.Offset]map[string]bool),
		receiver: make(map[inventory.Offset]inventory.Offset),
	}
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for ident, instance := range pkg.TypesInfo.Instances {
			params := typeParamsOf(pkg.TypesInfo.Uses[ident])
			for i := range min(params.Len(), instance.TypeArgs.Len()) {
				in.keepArg(pos.Offset(params.At(i).Obj().Pos()), instance.TypeArgs.At(i))
			}
		}
		for _, object := range pkg.TypesInfo.Defs {
			in.keepReceiver(object)
		}
	}
	return in
}

// keepArg records one type argument of the parameter held under key, once per
// spelling, since the variants that type-check one file instantiate it alike.
func (in *Instantiations) keepArg(key inventory.Offset, arg types.Type) {
	if in.spelled[key] == nil {
		in.spelled[key] = make(map[string]bool)
	}
	if spelling := resolve.Spell(arg, nil); !in.spelled[key][spelling] {
		in.spelled[key][spelling] = true
		in.args[key] = append(in.args[key], arg)
	}
}

// keepReceiver keys the receiver type parameters of one generic method to the type
// parameters of the type it is declared on.
func (in *Instantiations) keepReceiver(object types.Object) {
	method, isFunc := object.(*types.Func)
	if !isFunc {
		return
	}
	signature := method.Signature()
	declared := signature.RecvTypeParams()
	if declared.Len() == 0 {
		return
	}
	recv := types.Unalias(signature.Recv().Type())
	if pointer, isPointer := recv.(*types.Pointer); isPointer {
		recv = types.Unalias(pointer.Elem())
	}
	named, isNamed := recv.(*types.Named)
	if !isNamed {
		return
	}
	own := named.Origin().TypeParams()
	for i := range min(declared.Len(), own.Len()) {
		in.receiver[in.pos.Offset(declared.At(i).Obj().Pos())] = in.pos.Offset(own.At(i).Obj().Pos())
	}
}

// typeParamsOf is the type parameters of the generic declaration one instantiated
// identifier names.
func typeParamsOf(object types.Object) *types.TypeParamList {
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

// Declared is the key one type parameter's instantiations are held under.
func (in *Instantiations) Declared(param *types.TypeParam) inventory.Offset {
	key := in.pos.Offset(param.Obj().Pos())
	if own, isReceiver := in.receiver[key]; isReceiver {
		return own
	}
	return key
}

// Args is every type argument the parameter held under key takes, once per spelling.
func (in *Instantiations) Args(key inventory.Offset) []types.Type { return in.args[key] }
