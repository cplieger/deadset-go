package facts

import "go/types"

// Restated is t stated in the packages reading answers for the packages its types
// name: each defined type, method and field is replaced by its counterpart of the
// same name in reading's package, through every type constructor and the type
// arguments of an instance. Two readings of one package declare distinct types under
// one path, which no type of the other is identical to. t itself comes back where
// reading changes no package it names and t names no alias. False where a type t
// names has no counterpart: one declared in a function body, a name the other
// reading does not declare, a type parameter, or an interface holding type terms.
func Restated(t types.Type, reading func(*types.Package) *types.Package) (types.Type, bool) {
	return restater(reading).of(t)
}

type restater func(*types.Package) *types.Package

func (r restater) pkg(p *types.Package) *types.Package {
	if p == nil {
		return nil
	}
	return r(p)
}

func (r restater) of(t types.Type) (types.Type, bool) {
	switch u := t.(type) {
	case *types.Basic:
		return u, true
	case *types.Alias:
		return r.of(types.Unalias(u))
	case *types.Named:
		return r.named(u)
	case *types.Signature:
		return r.signature(u)
	case *types.Struct:
		return r.structure(u)
	case *types.Interface:
		return r.iface(u)
	}
	return r.composite(t)
}

// composite restates a pointer, slice, array or channel type, the type itself where
// its element is unchanged. A map is mapping's.
func (r restater) composite(t types.Type) (types.Type, bool) {
	var elem types.Type
	var build func(types.Type) types.Type
	switch u := t.(type) {
	case *types.Pointer:
		elem, build = u.Elem(), func(e types.Type) types.Type { return types.NewPointer(e) }
	case *types.Slice:
		elem, build = u.Elem(), func(e types.Type) types.Type { return types.NewSlice(e) }
	case *types.Array:
		elem, build = u.Elem(), func(e types.Type) types.Type { return types.NewArray(e, u.Len()) }
	case *types.Chan:
		elem, build = u.Elem(), func(e types.Type) types.Type { return types.NewChan(u.Dir(), e) }
	case *types.Map:
		return r.mapping(u)
	default:
		return nil, false
	}
	stated, ok := r.of(elem)
	if !ok {
		return nil, false
	}
	if stated == elem {
		return t, true
	}
	return build(stated), true
}

func (r restater) mapping(m *types.Map) (types.Type, bool) {
	key, keyOK := r.of(m.Key())
	elem, elemOK := r.of(m.Elem())
	if !keyOK || !elemOK {
		return nil, false
	}
	if key == m.Key() && elem == m.Elem() {
		return m, true
	}
	return types.NewMap(key, elem), true
}

func (r restater) named(n *types.Named) (types.Type, bool) {
	obj := n.Obj()
	origin := n.Origin()
	if into := r.pkg(obj.Pkg()); into != obj.Pkg() {
		if obj.Parent() != obj.Pkg().Scope() {
			return nil, false
		}
		same, declared := into.Scope().Lookup(obj.Name()).(*types.TypeName)
		if !declared {
			return nil, false
		}
		origin, _ = types.Unalias(same.Type()).(*types.Named)
		if origin == nil {
			return nil, false
		}
	}
	args := n.TypeArgs()
	if args.Len() == 0 {
		if origin == n.Origin() {
			return n, true
		}
		return origin, true
	}
	stated := make([]types.Type, args.Len())
	changed := origin != n.Origin()
	for i := range args.Len() {
		arg, ok := r.of(args.At(i))
		if !ok {
			return nil, false
		}
		stated[i] = arg
		changed = changed || arg != args.At(i)
	}
	if !changed {
		return n, true
	}
	instance, err := types.Instantiate(nil, origin, stated, false)
	return instance, err == nil
}

func (r restater) signature(sig *types.Signature) (types.Type, bool) {
	if sig.TypeParams().Len() > 0 {
		return nil, false
	}
	params, paramsSame, paramsOK := r.tuple(sig.Params())
	results, resultsSame, resultsOK := r.tuple(sig.Results())
	if !paramsOK || !resultsOK {
		return nil, false
	}
	if paramsSame && resultsSame {
		return sig, true
	}
	return types.NewSignatureType(nil, nil, nil, params, results, sig.Variadic()), true
}

// tuple is one parameter or result list restated, and whether nothing in it changed.
func (r restater) tuple(t *types.Tuple) (*types.Tuple, bool, bool) {
	vars := make([]*types.Var, t.Len())
	same := true
	for i := range t.Len() {
		v := t.At(i)
		stated, ok := r.of(v.Type())
		if !ok {
			return nil, false, false
		}
		pkg := r.pkg(v.Pkg())
		same = same && stated == v.Type() && pkg == v.Pkg()
		vars[i] = types.NewParam(v.Pos(), pkg, v.Name(), stated)
	}
	if same {
		return t, true, true
	}
	return types.NewTuple(vars...), false, true
}

func (r restater) structure(st *types.Struct) (types.Type, bool) {
	fields := make([]*types.Var, st.NumFields())
	tags := make([]string, st.NumFields())
	same := true
	for i := range st.NumFields() {
		field := st.Field(i)
		stated, ok := r.of(field.Type())
		if !ok {
			return nil, false
		}
		pkg := r.pkg(field.Pkg())
		same = same && stated == field.Type() && pkg == field.Pkg()
		fields[i] = types.NewField(field.Pos(), pkg, field.Name(), stated, field.Embedded())
		tags[i] = st.Tag(i)
	}
	if same {
		return st, true
	}
	return types.NewStruct(fields, tags), true
}

// iface is one interface restated as the methods of its method set, embedded ones
// included. A method's package is restated into the target reading too, because an
// unexported method's identity is its package's.
func (r restater) iface(iface *types.Interface) (types.Type, bool) {
	if !iface.IsMethodSet() {
		return nil, false
	}
	methods := make([]*types.Func, 0, iface.NumMethods())
	same := true
	for m := range iface.Methods() {
		stated, ok := r.signature(m.Signature())
		if !ok {
			return nil, false
		}
		sig, _ := stated.(*types.Signature)
		pkg := r.pkg(m.Pkg())
		same = same && sig == m.Signature() && pkg == m.Pkg()
		unbound := types.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic())
		methods = append(methods, types.NewFunc(m.Pos(), pkg, m.Name(), unbound))
	}
	if same {
		return iface, true
	}
	return types.NewInterfaceType(methods, nil).Complete(), true
}
