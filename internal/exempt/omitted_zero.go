package exempt

import (
	"go/types"
	"reflect"
	"slices"
	"strings"
)

// omitsZero reports whether every encoder of k leaves a field of type t holding its
// zero value out of its output by the field's tag, each by its own rule. Zero keys
// name no encoder known by its tag, which is taken to emit the field.
func (k tagKeys) omitsZero(t types.Type, tag string) bool {
	if k == 0 {
		return false
	}
	for key, name := range tagNames {
		if k&key == 0 {
			continue
		}
		opts := tagOptions(tag, name)
		zero, empty := slices.Contains(opts, "omitzero"), slices.Contains(opts, "omitempty")
		var omitted bool
		switch key {
		case jsonKey:
			omitted = zero && zeroOmitted(t) || empty && emptyByKind(t)
		case jsonV2Key:
			omitted = zero && zeroOmitted(t) || empty && encodesEmpty(t)
		case xmlKey:
			omitted = empty && emptyByKind(t)
		}
		if !omitted {
			return false
		}
	}
	return true
}

// tagOptions are the options a struct tag gives under one key, after its name.
func tagOptions(tag, name string) []string {
	_, options, _ := strings.Cut(reflect.StructTag(tag).Get(name), ",")
	return strings.Split(options, ",")
}

// emptyByKind reports whether the omitempty of encoding/json and encoding/xml leaves
// out the zero value of t, which they judge by its kind before calling any method:
// false, 0, a nil pointer or interface, and an array, slice, map or string of length
// zero (https://pkg.go.dev/encoding/json#Marshal, https://pkg.go.dev/encoding/xml#Marshal).
func emptyByKind(t types.Type) bool {
	if typeParameter(t) {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsString) != 0
	case *types.Slice, *types.Map, *types.Pointer, *types.Interface:
		return true
	case *types.Array:
		return u.Len() == 0
	}
	return false
}

// encodesEmpty reports whether the omitempty of encoding/json/v2 leaves out the zero
// value of t, which it does where the value encodes as null, "", {} or []: a nil
// pointer or interface, on which it calls no method, and a string, slice, map or
// array of length zero whose type has no JSON encoding method, false and 0 being
// encoded (https://pkg.go.dev/encoding/json/v2#Marshal).
func encodesEmpty(t types.Type) bool {
	if typeParameter(t) {
		return false
	}
	var empty bool
	switch u := t.Underlying().(type) {
	case *types.Pointer, *types.Interface:
		return true
	case *types.Basic:
		empty = u.Info()&types.IsString != 0
	case *types.Slice, *types.Map:
		empty = true
	case *types.Array:
		empty = u.Len() == 0
	}
	return empty && !jsonMarshaler(t)
}

// jsonMarshaler reports whether t or a pointer to it has a method a JSON encoder
// encodes it by ([methodsResolvedByName]).
func jsonMarshaler(t types.Type) bool {
	for m := range types.NewMethodSet(types.NewPointer(t)).Methods() {
		if methodsResolvedByName[m.Obj().Name()]&reachJSONEncode != 0 {
			return true
		}
	}
	return false
}

// zeroOmitted reports whether the omitzero of both JSON encoders leaves out the zero
// value of t. They leave out a nil pointer or interface whatever its methods, call
// t's method IsZero() bool where t has one, whose answer the analysis does not
// know, and test the Go zero value otherwise (https://pkg.go.dev/encoding/json#Marshal).
func zeroOmitted(t types.Type) bool {
	if typeParameter(t) {
		return false
	}
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Interface:
		return true
	}
	return zeroMethod(t) == nil
}

// keepZeroMethod keeps the IsZero() bool method of the type of a field an encoder
// emits where that encoder calls it: both JSON encoders do for a field whose json
// tag carries omitzero, while encoding/json and encoding/xml decide omitempty by
// kind and call no method. An opaque destination ([reach.opaque]) may hand the
// value to a JSON encoder or to a YAML one calling it under omitempty
// (https://pkg.go.dev/gopkg.in/yaml.v3#Marshal), so it keeps the method too.
func (f *encodingFlow) keepZeroMethod(t types.Type, tag string, site *evidence, d *destination) {
	if !d.reach.emits {
		return
	}
	json := (d.reach.opaque || d.reach.encoders&reachJSONEncode != 0) && slices.Contains(tagOptions(tag, "json"), "omitzero")
	yaml := d.reach.opaque && slices.Contains(tagOptions(tag, "yaml"), "omitempty")
	if !json && !yaml {
		return
	}
	if fn := zeroMethod(t); fn != nil {
		f.kept.record(fn.Origin(), site, d.detail)
	}
}

// zeroMethod is the method IsZero() bool of t or a pointer to it, the one the JSON
// encoders look for, nil for an interface or a type with none.
func zeroMethod(t types.Type) *types.Func {
	if types.IsInterface(t) || typeParameter(t) {
		return nil
	}
	set := types.NewMethodSet(t)
	if _, isPointer := t.Underlying().(*types.Pointer); !isPointer {
		set = types.NewMethodSet(types.NewPointer(t))
	}
	sel := set.Lookup(nil, "IsZero")
	if sel == nil {
		return nil
	}
	fn, isFunc := sel.Obj().(*types.Func)
	if !isFunc {
		return nil
	}
	if sig := fn.Signature(); sig.Params().Len() != 0 || sig.Results().Len() != 1 ||
		!types.Identical(sig.Results().At(0).Type(), types.Typ[types.Bool]) {
		return nil
	}
	return fn
}

// typeParameter reports whether t is a type parameter, whose zero value and methods
// each instantiation decides.
func typeParameter(t types.Type) bool {
	_, param := types.Unalias(t).(*types.TypeParam)
	return param
}
