package facts

import (
	"go/token"
	"go/types"
	"strconv"
	"testing"
)

// bareMethods is an interface of methods taking and returning nothing, by name.
func bareMethods(names ...string) *types.Interface {
	pkg := types.NewPackage("example.com/chains", "chains")
	funcs := make([]*types.Func, len(names))
	for i, name := range names {
		funcs[i] = types.NewFunc(token.NoPos, pkg, name, types.NewSignatureType(nil, nil, nil, nil, nil, false))
	}
	return types.NewInterfaceType(funcs, nil).Complete()
}

func TestChainsAnswersOneEmptyChainPastTheBound(t *testing.T) {
	t.Parallel()

	from, to := bareMethods("A", "B"), bareMethods("A")
	sites := make([]Conversion, maxRoutes+1)
	for i := range sites {
		sites[i] = Conversion{From: from, To: to, Site: token.Pos(i + 1)}
	}
	chains := NewChains(sites, func(i int) string { return strconv.Itoa(i) })

	if got := chains.Routes(from, to); len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("Routes over %d chains = %d chains, want one empty chain: past the bound no site's holder is kept", len(sites), len(got))
	}
}
