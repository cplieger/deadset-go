package graph

import (
	"go/ast"
	"go/token"
	"go/types"
)

// readStructConversion records the field reads of one conversion between two struct
// types, or between two pointer types that are not named types and whose bases are
// struct types. The conversion compiles only while the fields type identity compares
// stay identical, so no field of either side can be deleted alone
// (https://go.dev/ref/spec#Conversions). A type parameter operand stands for each type
// argument an instantiation gives it, and a type parameter destination for the struct
// type its constraint's core type is.
func (p *referencePass) readStructConversion(call *ast.CallExpr, encl SymbolID) {
	if len(call.Args) != 1 {
		return
	}
	target, held := p.info.Types[call.Fun]
	if !held || !target.IsType() {
		return
	}
	from := p.info.TypeOf(call.Args[0])
	if from == nil {
		return
	}
	site := &identityRead{read: make(map[SymbolID]bool), encl: encl, pos: call.Pos()}
	to := coreOf(target.Type)
	for _, operand := range p.operandTypes(from, make(map[token.Pos]bool)) {
		a, b, convertible := convertedStructs(operand, to)
		if convertible {
			p.readIdentity(a, b, site)
		}
	}
}

// identityRead is one conversion's walk: where it is recorded, and the fields it has
// read, so each field is read once per conversion.
type identityRead struct {
	read map[SymbolID]bool
	encl SymbolID
	pos  token.Pos
}

// operandTypes is the types one conversion operand stands for: itself, or for a type
// parameter every type argument the program's instantiations give it, through the
// type parameters of other generic declarations.
func (p *referencePass) operandTypes(t types.Type, seen map[token.Pos]bool) []types.Type {
	param, isParam := types.Unalias(t).(*types.TypeParam)
	if !isParam {
		return []types.Type{t}
	}
	key := p.instances.declared(param)
	if seen[key] {
		return nil
	}
	seen[key] = true
	var held []types.Type
	for _, arg := range p.instances.args[key] {
		held = append(held, p.operandTypes(arg, seen)...)
	}
	return held
}

// coreOf is the type a type parameter destination converts to, its constraint's
// single underlying type, and the type itself for anything else. A constraint whose
// terms hold several underlying types has no core type and converts no struct.
func coreOf(t types.Type) types.Type {
	param, isParam := types.Unalias(t).(*types.TypeParam)
	if !isParam {
		return t
	}
	iface, isInterface := param.Constraint().Underlying().(*types.Interface)
	if !isInterface {
		return nil
	}
	var core types.Type
	for embedded := range iface.EmbeddedTypes() {
		var terms []types.Type
		if union, isUnion := embedded.(*types.Union); isUnion {
			for term := range union.Terms() {
				terms = append(terms, term.Type())
			}
		} else {
			terms = append(terms, embedded)
		}
		for _, term := range terms {
			under := term.Underlying()
			if core != nil && !types.Identical(core, under) {
				return nil
			}
			core = under
		}
	}
	return core
}

// convertedStructs is the two struct types one conversion pairs: the underlying
// structs of two struct types, or the bases' structs of two unnamed pointers.
func convertedStructs(from, to types.Type) (a, b *types.Struct, convertible bool) {
	if from == nil || to == nil {
		return nil, nil, false
	}
	from, to = types.Unalias(from), types.Unalias(to)
	fromPointer, fromIsPointer := from.(*types.Pointer)
	toPointer, toIsPointer := to.(*types.Pointer)
	if fromIsPointer && toIsPointer {
		from, to = fromPointer.Elem(), toPointer.Elem()
	}
	a, aIsStruct := from.Underlying().(*types.Struct)
	b, bIsStruct := to.Underlying().(*types.Struct)
	return a, b, aIsStruct && bIsStruct
}

// readIdentity records a read of every field of each pair of distinct struct type
// literals the identity of a and b compares in turn, through the field, element, key
// and base types of the literals it reaches. It stops at a defined type, which is
// identical to itself whatever fields it declares, and at one type met on both sides,
// whose fields no pairing constrains. The inventory holds no field of a struct
// literal under a type argument, a signature or an interface, so the walk does not
// enter those either; a change that enumerates them must extend it.
func (p *referencePass) readIdentity(a, b types.Type, site *identityRead) {
	a, b = types.Unalias(a), types.Unalias(b)
	if a == b {
		return
	}
	x, xIsStruct := a.(*types.Struct)
	y, yIsStruct := b.(*types.Struct)
	if xIsStruct && yIsStruct && x.NumFields() == y.NumFields() {
		for i := range x.NumFields() {
			p.readIdentityField(x.Field(i), site)
			p.readIdentityField(y.Field(i), site)
			p.readIdentity(x.Field(i).Type(), y.Field(i).Type(), site)
		}
		return
	}
	for _, pair := range componentPairs(a, b) {
		p.readIdentity(pair[0], pair[1], site)
	}
}

// componentPairs is the component types identity compares between two type literals
// of one constructor: the elements of two pointers, slices, arrays or channels, and
// the keys and the elements of two maps.
func componentPairs(a, b types.Type) [][2]types.Type {
	switch x := a.(type) {
	case *types.Pointer:
		if y, ok := b.(*types.Pointer); ok {
			return [][2]types.Type{{x.Elem(), y.Elem()}}
		}
	case *types.Slice:
		if y, ok := b.(*types.Slice); ok {
			return [][2]types.Type{{x.Elem(), y.Elem()}}
		}
	case *types.Array:
		if y, ok := b.(*types.Array); ok {
			return [][2]types.Type{{x.Elem(), y.Elem()}}
		}
	case *types.Chan:
		if y, ok := b.(*types.Chan); ok {
			return [][2]types.Type{{x.Elem(), y.Elem()}}
		}
	case *types.Map:
		if y, ok := b.(*types.Map); ok {
			return [][2]types.Type{{x.Key(), y.Key()}, {x.Elem(), y.Elem()}}
		}
	}
	return nil
}

// readIdentityField records one read of a field at the conversion.
func (p *referencePass) readIdentityField(field *types.Var, site *identityRead) {
	if to, ok := p.symbolOf(field.Origin()); ok && !site.read[to] {
		site.read[to] = true
		p.add(site.encl, to, site.pos, RefRead)
	}
}
