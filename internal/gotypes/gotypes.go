// Package gotypes holds the questions about go/types values that more than one
// stage of the analysis asks.
package gotypes

import "go/types"

// Underlying returns the underlying type of t, and nil for a nil t.
func Underlying(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}

// TupleTypes lists the types one tuple holds.
func TupleTypes(t *types.Tuple) []types.Type {
	if t == nil {
		return nil
	}
	out := make([]types.Type, 0, t.Len())
	for v := range t.Variables() {
		out = append(out, v.Type())
	}
	return out
}

// ParameterType returns the type of the parameter the argument at index reaches.
// An argument past the last parameter of a variadic signature reaches the
// element type of that parameter, unless the call spreads a slice into it, where
// it reaches the slice.
func ParameterType(sig *types.Signature, index int, spread bool) types.Type {
	params := sig.Params()
	if params == nil || params.Len() == 0 {
		return nil
	}
	last := params.Len() - 1
	if index < last {
		return params.At(index).Type()
	}
	final := params.At(last).Type()
	if !sig.Variadic() {
		if index > last {
			return nil
		}
		return final
	}
	if spread {
		return final
	}
	if slice, ok := final.(*types.Slice); ok {
		return slice.Elem()
	}
	return final
}
