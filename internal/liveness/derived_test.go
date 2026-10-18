package liveness

import (
	"go/token"
	"go/types"
	"strconv"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/facts"
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
	sites := []facts.Conversion{
		{From: wide, To: middle, Site: 1},
		{From: middle, To: narrow, Site: 2},
		{From: wide, To: narrow, Site: 3},
		{From: wide, To: narrow, Site: 4},
	}
	byHolder := func(i int) string { return strconv.Itoa(i) }
	chains := facts.NewChains(sites, byHolder)

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

	oneHolder := facts.NewChains(sites, func(int) string { return "one" })
	if got := oneHolder.Routes(wide, narrow); len(got) != 2 {
		t.Errorf("Routes(wide, narrow) with one holder = %v, want two: the two direct sites one declaration holds are one hop", got)
	}
}
