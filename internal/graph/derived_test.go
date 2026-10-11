package graph

import (
	"go/token"
	"go/types"
	"strconv"
	"testing"
)

// methods is an interface of methods taking and returning nothing, by name.
func methods(names ...string) *types.Interface {
	pkg := types.NewPackage("example.com/chains", "chains")
	funcs := make([]*types.Func, len(names))
	for i, name := range names {
		funcs[i] = types.NewFunc(token.NoPos, pkg, name, types.NewSignatureType(nil, nil, nil, nil, nil, false))
	}
	return types.NewInterfaceType(funcs, nil).Complete()
}

func TestChainsAnswersEveryChainBetweenTwoInterfaces(t *testing.T) {
	t.Parallel()

	wide, middle, narrow, other := methods("A", "B", "C"), methods("A", "B"), methods("A"), methods("Z")
	sites := []Conversion{
		{From: wide, To: middle, Site: 1},
		{From: middle, To: narrow, Site: 2},
		{From: wide, To: narrow, Site: 3},
		{From: wide, To: narrow, Site: 4},
	}
	byHolder := func(i int) string { return strconv.Itoa(i) }
	chains := NewChains(sites, byHolder)

	if got := chains.Routes(wide, narrow); len(got) != 3 {
		t.Errorf("Routes(wide, narrow) = %v, want three chains: through middle, and each direct site", got)
	}
	if got := chains.Routes(narrow, narrow); len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("Routes(narrow, narrow) = %v, want one empty chain", got)
	}
	if got := chains.Routes(narrow, wide); len(got) != 0 {
		t.Errorf("Routes(narrow, wide) = %v, want none: no site converts a narrow value", got)
	}
	if got := chains.Routes(other, narrow); len(got) != 0 {
		t.Errorf("Routes(other, narrow) = %v, want none", got)
	}

	oneHolder := NewChains(sites, func(int) string { return "one" })
	if got := oneHolder.Routes(wide, narrow); len(got) != 2 {
		t.Errorf("Routes(wide, narrow) with one holder = %v, want two: the two direct sites one declaration holds are one hop", got)
	}
}

func TestChainsAnswersOneEmptyChainPastTheBound(t *testing.T) {
	t.Parallel()

	from, to := methods("A", "B"), methods("A")
	sites := make([]Conversion, maxRoutes+1)
	for i := range sites {
		sites[i] = Conversion{From: from, To: to, Site: token.Pos(i + 1)}
	}
	chains := NewChains(sites, func(i int) string { return strconv.Itoa(i) })

	if got := chains.Routes(from, to); len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("Routes over %d chains = %d chains, want one empty chain: past the bound no site's holder is kept", len(sites), len(got))
	}
}
