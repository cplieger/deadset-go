package testsupport

import (
	"fmt"
	"reflect"
)

// Sink is what a helper needs of the test that calls it: mark itself a helper, and
// end that test with a diagnostic. *testing.T and a property's *rapid.T both
// satisfy it.
type Sink interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Detached is a copy of value that shares no memory a test could write through:
// every pointer, slice, map and interface it holds is copied in turn, and two
// pointers to one value stay two pointers to one copy. A value it cannot copy ends
// the test through sink: a channel, an unsafe pointer, or an unexported field
// holding any reference, which reflection cannot set and a copy by value would
// share.
func Detached[V any](sink Sink, value V) V {
	sink.Helper()

	copied, err := copier{seen: make(map[copiedAt]reflect.Value)}.of(reflect.ValueOf(&value).Elem())
	if err != nil {
		sink.Fatalf("Setup: copy a shared value: %v", err)
		var none V
		return none
	}
	held, _ := reflect.TypeAssert[V](copied)
	return held
}

// copiedAt is one pointer already copied, so two pointers to one value stay two
// pointers to one copy.
type copiedAt struct {
	of reflect.Type
	at uintptr
}

// copier is one deep copy in progress.
type copier struct {
	seen map[copiedAt]reflect.Value
}

// of is the copy of one value.
func (c copier) of(v reflect.Value) (reflect.Value, error) {
	switch v.Kind() {
	case reflect.Pointer:
		return c.pointer(v)
	case reflect.Slice:
		if v.IsNil() {
			return v, nil
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		return out, c.elements(v, out)
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		return out, c.elements(v, out)
	case reflect.Map:
		return c.mapOf(v)
	case reflect.Interface:
		if v.IsNil() {
			return v, nil
		}
		inner, err := c.of(v.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(inner)
		return out, nil
	case reflect.Struct:
		return c.structOf(v)
	case reflect.Chan, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("a value of type %s cannot be copied", v.Type())
	default:
		return v, nil
	}
}

// pointer is the copy of one pointer and of what it points to.
func (c copier) pointer(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return v, nil
	}
	at := copiedAt{of: v.Type(), at: v.Pointer()}
	if done, held := c.seen[at]; held {
		return done, nil
	}
	out := reflect.New(v.Type().Elem())
	c.seen[at] = out
	inner, err := c.of(v.Elem())
	if err != nil {
		return reflect.Value{}, err
	}
	out.Elem().Set(inner)
	return out, nil
}

// elements copies every element of a slice or an array into another.
func (c copier) elements(from, into reflect.Value) error {
	for i := range from.Len() {
		inner, err := c.of(from.Index(i))
		if err != nil {
			return err
		}
		into.Index(i).Set(inner)
	}
	return nil
}

// mapOf is the copy of one map, its keys and values copied in turn.
func (c copier) mapOf(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return v, nil
	}
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	for iter := v.MapRange(); iter.Next(); {
		key, err := c.of(iter.Key())
		if err != nil {
			return reflect.Value{}, err
		}
		value, err := c.of(iter.Value())
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetMapIndex(key, value)
	}
	return out, nil
}

// structOf is the copy of one struct: an exported field is copied in turn, and an
// unexported one, which reflection cannot set, is taken by value and must hold no
// reference.
func (c copier) structOf(v reflect.Value) (reflect.Value, error) {
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	for i := range v.NumField() {
		field := v.Type().Field(i)
		if !field.IsExported() {
			if !flat(field.Type) {
				return reflect.Value{}, fmt.Errorf("the unexported field %s.%s holds a reference a copy would share",
					v.Type(), field.Name)
			}
			continue
		}
		inner, err := c.of(v.Field(i))
		if err != nil {
			return reflect.Value{}, err
		}
		out.Field(i).Set(inner)
	}
	return out, nil
}

// flat reports whether a value of one type holds no pointer, slice, map, interface or
// channel, so copying it by value shares nothing.
func flat(of reflect.Type) bool {
	switch of.Kind() {
	case reflect.Array:
		return flat(of.Elem())
	case reflect.Struct:
		for field := range of.Fields() {
			if !flat(field.Type) {
				return false
			}
		}
		return true
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface, reflect.Chan, reflect.UnsafePointer:
		return false
	default:
		return true
	}
}
