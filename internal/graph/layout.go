package graph

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// instantiations is every type argument each type parameter of one configuration
// takes in an instantiation the configuration holds. A parameter is keyed by the
// position it is declared at, so the variants that type-check one file share it, and
// a method's receiver type parameter answers for its type's own parameter.
type instantiations struct {
	args     map[token.Pos][]types.Type
	spelled  map[token.Pos]map[string]bool
	receiver map[token.Pos]token.Pos
}

// instantiationsOf collects the instantiations of every package of one walk.
func instantiationsOf(pkgs []*packages.Package) *instantiations {
	in := &instantiations{
		args:     make(map[token.Pos][]types.Type),
		spelled:  make(map[token.Pos]map[string]bool),
		receiver: make(map[token.Pos]token.Pos),
	}
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for ident, instance := range pkg.TypesInfo.Instances {
			params := typeParamsOf(pkg.TypesInfo.Uses[ident])
			for i := range min(params.Len(), instance.TypeArgs.Len()) {
				in.keepArg(params.At(i).Obj().Pos(), instance.TypeArgs.At(i))
			}
		}
		for _, object := range pkg.TypesInfo.Defs {
			in.keepReceiver(object)
		}
	}
	return in
}

// keepArg records one type argument of the parameter declared at key, once per
// spelling, since the variants that type-check one file instantiate it alike.
func (in *instantiations) keepArg(key token.Pos, arg types.Type) {
	if in.spelled[key] == nil {
		in.spelled[key] = make(map[string]bool)
	}
	if spelling := types.TypeString(arg, nil); !in.spelled[key][spelling] {
		in.spelled[key][spelling] = true
		in.args[key] = append(in.args[key], arg)
	}
}

// keepReceiver keys the receiver type parameters of one generic method to the type
// parameters of the type it is declared on.
func (in *instantiations) keepReceiver(object types.Object) {
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
		in.receiver[declared.At(i).Obj().Pos()] = own.At(i).Obj().Pos()
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

// declared is the key one type parameter's instantiations are held under.
func (in *instantiations) declared(param *types.TypeParam) token.Pos {
	key := param.Obj().Pos()
	if own, isReceiver := in.receiver[key]; isReceiver {
		return own
	}
	return key
}

// readUnsafeConversion records the layout reads of one conversion between a pointer
// and unsafe.Pointer, in either direction, at the conversion: memory read through
// either names no field, so every field the pointed-to type lays out is used there.
func (p *referencePass) readUnsafeConversion(call *ast.CallExpr, encl SymbolID) {
	if len(call.Args) != 1 {
		return
	}
	target, held := p.info.Types[call.Fun]
	if !held || !target.IsType() {
		return
	}
	// An operand without a type is a C value, which names no Go type to read;
	// converted to a Go pointer it can only have been an unsafe.Pointer.
	from := p.info.TypeOf(call.Args[0])
	unknown := typeUnknown(from)
	var converted types.Type
	switch {
	case isUnsafePointer(target.Type) && !unknown:
		converted = from
	case unknown, isUnsafePointer(from):
		converted = target.Type
	default:
		return
	}
	pointer, isPointer := types.Unalias(converted).Underlying().(*types.Pointer)
	if !isPointer {
		return
	}
	p.readLayout(pointer.Elem(), newLayoutRead(encl, call.Pos()))
}

// readHostLayout records the layout reads of one struct type that declares a field
// of type structs.HostLayout, at that field: the layout is fixed for code outside the
// program, which names none of the fields.
func (p *referencePass) readHostLayout(t *ast.StructType, owner SymbolID) {
	declared, isStruct := p.info.TypeOf(t).(*types.Struct)
	if !isStruct {
		return
	}
	for i, field := range t.Fields.List {
		if i < declared.NumFields() && isHostLayout(declared.Field(i).Type()) {
			p.readLayout(declared, newLayoutRead(owner, field.Pos()))
			return
		}
	}
}

// layoutRead is one site's walk over a laid-out type: where it is recorded, and the
// fields and type parameters it has reached, so each field is read once per site.
type layoutRead struct {
	read   map[SymbolID]bool
	params map[token.Pos]bool
	encl   SymbolID
	pos    token.Pos
}

func newLayoutRead(encl SymbolID, pos token.Pos) *layoutRead {
	return &layoutRead{read: make(map[SymbolID]bool), params: make(map[token.Pos]bool), encl: encl, pos: pos}
}

// readLayout records one read of every field t lays out: every field of a struct
// and every element of an array, at any depth, and a type parameter laid out as
// each type argument it takes. A field holding a pointer, a slice, a map, a channel,
// a function or an interface is read and laid out no further.
func (p *referencePass) readLayout(t types.Type, site *layoutRead) {
	t = types.Unalias(t)
	if param, isParam := t.(*types.TypeParam); isParam {
		key := p.instances.declared(param)
		if site.params[key] {
			return
		}
		site.params[key] = true
		for _, arg := range p.instances.args[key] {
			p.readLayout(arg, site)
		}
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for field := range u.Fields() {
			if to, ok := p.symbolOf(field.Origin()); ok && !site.read[to] {
				site.read[to] = true
				p.add(site.encl, to, site.pos, RefRead)
			}
			p.readLayout(field.Type(), site)
		}
	case *types.Array:
		p.readLayout(u.Elem(), site)
	}
}

// isUnsafePointer reports whether a type is unsafe.Pointer or a type defined over it.
func isUnsafePointer(t types.Type) bool {
	if t == nil {
		return false
	}
	basic, isBasic := types.Unalias(t).Underlying().(*types.Basic)
	return isBasic && basic.Kind() == types.UnsafePointer
}

// typeUnknown reports whether the type checker recorded no type for an expression,
// or recorded the invalid type, which an identifier declared from such an
// expression holds.
func typeUnknown(t types.Type) bool {
	if t == nil {
		return true
	}
	basic, isBasic := t.(*types.Basic)
	return isBasic && basic.Kind() == types.Invalid
}

// isHostLayout reports whether a field's type is structs.HostLayout.
func isHostLayout(t types.Type) bool {
	named, isNamed := types.Unalias(t).(*types.Named)
	if !isNamed {
		return false
	}
	name := named.Obj()
	return name.Pkg() != nil && name.Pkg().Path() == "structs" && name.Name() == "HostLayout"
}
