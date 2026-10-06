package exempt

import (
	"fmt"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
)

// Conversions returns every site in the loaded configuration where a value of a
// concrete type reaches a position typed as an interface, in the order
// [graph.Conversions] returns them. A site converting an interface value is the
// interface kinds' subject rather than an exemption's, so it is not here.
//
// The set narrows interface satisfaction to the types a program converts. Three
// classes read it: interface satisfaction, errors duck typing and the format verbs.
// It is computed once per input, and a caller reads the slice without changing it.
func Conversions(in *Input) ([]graph.Conversion, error) {
	if in == nil || in.Result == nil || in.Result.Fset == nil {
		return nil, fmt.Errorf("%w: no file set", graph.ErrIncompleteLoad)
	}
	if in.conversions == nil {
		sites := graph.Conversions(in.Result.Fset, in.Result.Packages)
		sites = slices.DeleteFunc(sites, func(c graph.Conversion) bool { return types.IsInterface(c.From) })
		in.conversions = &sites
	}
	return *in.conversions, nil
}
