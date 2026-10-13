package exempt

import (
	"fmt"
	"go/types"
	"slices"
)

// selfEncoders are, per encoding direction, the methods by which a type encodes
// itself, so the encoder calls the method and walks no field of the type
// (https://pkg.go.dev/encoding/json#Marshal, https://pkg.go.dev/encoding/xml#Marshal,
// https://pkg.go.dev/encoding/gob).
var selfEncoders = map[methodReach][]selfEncoder{
	reachJSONEncode: {{marshalJSON, 0, 2}, {"MarshalJSONTo", 1, 1}, {marshalText, 0, 2}},
	reachXMLEncode:  {{"MarshalXML", 2, 1}, {marshalText, 0, 2}},
	reachGobEncode:  {{"GobEncode", 0, 2}, {"MarshalBinary", 0, 2}},
}

// selfEncoder is one method an encoder calls instead of walking fields, with the
// parameter and result counts its signature has, the last result an error.
type selfEncoder struct {
	name            string
	params, results int
}

// selfEncodedReach is [encodingFlow.encoderReach] for destinations that are standard
// encoders of the directions given, with the types that encode themselves in every one
// of them, which the walk does not enter.
func (f *encodingFlow) selfEncodedReach(t types.Type, directions methodReach) (found []*types.Named, encoded map[string]bool) {
	key := selfEncodingKey{t: t, directions: directions}
	if held, known := f.selfEncoded[key]; known {
		return held.found, held.encoded
	}
	found, encoded = f.computeSelfEncodedReach(t, directions)
	if f.selfEncoded == nil {
		f.selfEncoded = make(map[selfEncodingKey]selfEncodedTypes)
	}
	f.selfEncoded[key] = selfEncodedTypes{found: found, encoded: encoded}
	return found, encoded
}

// selfEncodingKey is one walked type and the encoding directions of its walk.
type selfEncodingKey struct {
	t          types.Type
	directions methodReach
}

// selfEncodedTypes is one answer of [encodingFlow.selfEncodedReach].
type selfEncodedTypes struct {
	encoded map[string]bool
	found   []*types.Named
}

// computeSelfEncodedReach answers [encodingFlow.selfEncodedReach] once.
func (f *encodingFlow) computeSelfEncodedReach(t types.Type, directions methodReach) (found []*types.Named, encoded map[string]bool) {
	encoded = make(map[string]bool)
	for key, met := range f.addressing(t) {
		if encodesItself(met, directions) {
			encoded[key] = true
		}
	}
	if len(encoded) == 0 {
		return f.encoderReach(t), nil
	}
	found = f.encodedTypes(t)
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
	return found, encoded
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
			for _, stored := range f.stored.of(field) {
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
	return slices.ContainsFunc(methods, func(m selfEncoder) bool {
		selection := set.Lookup(nil, m.name)
		if selection == nil {
			return false
		}
		sig, isSignature := selection.Type().(*types.Signature)
		return isSignature && sig.Params().Len() == m.params && sig.Results().Len() == m.results &&
			types.Identical(sig.Results().At(m.results-1).Type(), types.Universe.Lookup("error").Type())
	})
}
