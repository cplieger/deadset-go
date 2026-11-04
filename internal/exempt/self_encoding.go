package exempt

import (
	"fmt"
	"go/types"
	"slices"
)

// selfEncoder is one method an encoder calls instead of walking fields, with the
// parameter and result counts its signature has, the last result an error. some is
// whether only some encoders of the direction call it, the others never.
type selfEncoder struct {
	name            string
	params, results int
	some            bool
}

// selfEncodingKey is one walked type and the encoding directions of its walk.
type selfEncodingKey struct {
	t          types.Type
	directions methodReach
}

// selfEncodedTypes is what a walk by standard encoders reads of one type: the types
// it reads, and by spelling the ones among them that encode themselves in every
// direction of the walk, which the walk does not enter.
type selfEncodedTypes struct {
	encoded map[string]bool
	found   []*types.Named
}

// selfEncodedReach is [encodingFlow.encoderReach] for destinations that are standard
// encoders of the directions given, read once per type by [encodingFlow.index] after
// the type's own walk.
func (f *encodingFlow) selfEncodedReach(t types.Type, directions methodReach) selfEncodedTypes {
	encoded := make(map[string]bool)
	for key, met := range f.addressing(t) {
		if encodesItself(met, directions) {
			encoded[key] = true
		}
	}
	if len(encoded) == 0 {
		return selfEncodedTypes{found: f.walked[t]}
	}
	found := f.encodedTypes(t)
	seen := make(map[string]bool, len(found))
	for _, named := range found {
		seen[types.TypeString(named, nil)] = true
	}
	for at := 0; at < len(found); at++ {
		if encoded[types.TypeString(found[at], nil)] {
			continue
		}
		for _, next := range f.membersReached(found[at]) {
			if key := types.TypeString(next, nil); !seen[key] {
				seen[key] = true
				found = append(found, next)
			}
		}
	}
	return selfEncodedTypes{found: found, encoded: encoded}
}

// meeting is one defined type a walk of a value meets, and whether it meets a value of
// it that is not addressable, so that a method with a pointer receiver is not called.
type meeting struct {
	named *types.Named
	plain bool
}

// addressing is every defined type a walk of a value of t meets, by its spelling, the
// walk entering every field as [encodingFlow.encodedTypes] does. A value is
// addressable where it is met through a pointer, as a slice element, or as an array
// element or a field of an addressable value, and not as the value itself, a map key
// or value, or a value an interface holds (https://pkg.go.dev/reflect#Value.CanAddr).
func (f *encodingFlow) addressing(t types.Type) map[string]*meeting {
	met := make(map[string]*meeting)
	seen := make(map[string]bool)
	stack := []walkStep{{t: t}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		key := fmt.Sprint(cur.addressable, types.TypeString(cur.t, nil))
		if cur.t == nil || seen[key] {
			continue
		}
		seen[key] = true
		if named, isNamed := types.Unalias(cur.t).(*types.Named); isNamed {
			spelled := types.TypeString(named, nil)
			if met[spelled] == nil {
				met[spelled] = &meeting{named: named}
			}
			met[spelled].plain = met[spelled].plain || !cur.addressable
		}
		stack = append(stack, f.beneath(cur)...)
	}
	return met
}

// walkStep is one type [encodingFlow.addressing] meets, and whether the value met is
// addressable.
type walkStep struct {
	t           types.Type
	addressable bool
}

// beneath is the steps one step of [encodingFlow.addressing] leads to.
func (f *encodingFlow) beneath(cur walkStep) []walkStep {
	switch u := types.Unalias(cur.t).(type) {
	case *types.Named:
		if !types.IsInterface(u) {
			return []walkStep{{t: u.Underlying(), addressable: cur.addressable}}
		}
	case *types.Pointer:
		return []walkStep{{t: u.Elem(), addressable: true}}
	case *types.Slice:
		return []walkStep{{t: u.Elem(), addressable: true}}
	case *types.Array:
		return []walkStep{{t: u.Elem(), addressable: cur.addressable}}
	case *types.Map:
		return []walkStep{{t: u.Key()}, {t: u.Elem()}}
	case *types.Struct:
		var next []walkStep
		for field := range u.Fields() {
			for _, stored := range f.graph.stored.of(field) {
				next = append(next, walkStep{t: stored})
			}
			if !types.IsInterface(field.Type()) {
				next = append(next, walkStep{t: field.Type(), addressable: cur.addressable})
			}
		}
		return next
	}
	return nil
}

// keepPromoting keeps each embedded field through which a type that encodes itself
// has the encoding method an encoder calls, the first of its direction's [selfEncoders]
// the type has, because calling the method reads that field. A method only some
// encoders call ([selfEncoder.some]) leaves the others to go on to the next.
func (f *encodingFlow) keepPromoting(named *types.Named, site evidence, d *destination) {
	set := types.NewMethodSet(types.NewPointer(named))
	for direction, methods := range selfEncoders {
		if d.reach.encoders&direction == 0 {
			continue
		}
		for _, m := range methods {
			sel := declares(set, m)
			if sel == nil {
				continue
			}
			if len(sel.Index()) > 1 {
				f.keepEmbedded(named, sel.Index()[:len(sel.Index())-1], site, d)
			}
			if !m.some {
				break
			}
		}
	}
}

// keepEmbedded keeps each embedded field a path of field indices crosses from t.
func (f *encodingFlow) keepEmbedded(t types.Type, path []int, site evidence, d *destination) {
	for _, i := range path {
		if pointer, isPointer := t.Underlying().(*types.Pointer); isPointer {
			t = pointer.Elem()
		}
		st, isStruct := t.Underlying().(*types.Struct)
		if !isStruct {
			return
		}
		f.kept.record(st.Field(i), site, d.detail)
		t = st.Field(i).Type()
	}
}

// encodesItself reports whether a type met by a walk encodes itself in every one of
// the directions: its own method set holds a method of [selfEncoders] for the
// direction, or a pointer's does where every value of it the walk meets is addressable.
func encodesItself(met *meeting, directions methodReach) bool {
	for direction, methods := range selfEncoders {
		if directions&direction == 0 {
			continue
		}
		if !declaresAny(met.named, methods) && (met.plain || !declaresAny(types.NewPointer(met.named), methods)) {
			return false
		}
	}
	return true
}

// declaresAny reports whether the method set of t holds one of the methods with the
// signature its row gives.
func declaresAny(t types.Type, methods []selfEncoder) bool {
	set := types.NewMethodSet(t)
	return slices.ContainsFunc(methods, func(m selfEncoder) bool { return declares(set, m) != nil })
}

// declares is the selection of m in a method set where it has the signature m's row
// gives, nil where it has not.
func declares(set *types.MethodSet, m selfEncoder) *types.Selection {
	selection := set.Lookup(nil, m.name)
	if selection == nil {
		return nil
	}
	sig, isSignature := selection.Type().(*types.Signature)
	if !isSignature || sig.Params().Len() != m.params || sig.Results().Len() != m.results ||
		!types.Identical(sig.Results().At(m.results-1).Type(), types.Universe.Lookup("error").Type()) {
		return nil
	}
	return selection
}
