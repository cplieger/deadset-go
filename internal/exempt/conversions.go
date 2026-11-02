package exempt

import (
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
)

// concrete is the sites of a conversion set that convert a value of a concrete type.
func concrete(sites []facts.Conversion) []facts.Conversion {
	return slices.DeleteFunc(slices.Clone(sites), func(c facts.Conversion) bool { return types.IsInterface(c.From) })
}
