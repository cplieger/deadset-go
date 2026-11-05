package facts

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

// readUnsafeConversion records the layout reads of one conversion between a pointer
// and unsafe.Pointer, in either direction, at the conversion: memory read through
// either names no field, so every field the pointed-to type lays out is used there.
func (p *referencePass) readUnsafeConversion(call *ast.CallExpr, encl inventory.SymbolID) {
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
func (p *referencePass) readHostLayout(t *ast.StructType, owner inventory.SymbolID) {
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
	read   map[inventory.SymbolID]bool
	params map[inventory.Offset]bool
	encl   inventory.SymbolID
	pos    token.Pos
}

func newLayoutRead(encl inventory.SymbolID, pos token.Pos) *layoutRead {
	return &layoutRead{read: make(map[inventory.SymbolID]bool), params: make(map[inventory.Offset]bool), encl: encl, pos: pos}
}

// readLayout records one read of every field t lays out: every field of a struct
// and every element of an array, at any depth, and a type parameter laid out as
// each type argument it takes. A field holding a pointer, a slice, a map, a channel,
// a function or an interface is read and laid out no further.
func (p *referencePass) readLayout(t types.Type, site *layoutRead) {
	t = types.Unalias(t)
	if param, isParam := t.(*types.TypeParam); isParam {
		key := p.instances.Declared(param)
		if site.params[key] {
			return
		}
		site.params[key] = true
		for _, arg := range p.instances.Args(key) {
			p.readLayout(arg, site)
		}
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for field := range u.Fields() {
			if to, ok := p.resolve.Object(field.Origin()); ok && !site.read[to] {
				site.read[to] = true
				p.add(site.encl, to, site.pos, RefRead, viaLayout)
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
