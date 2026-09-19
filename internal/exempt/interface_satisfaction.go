package exempt

import (
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
)

// InterfaceSatisfactionDetector retains every method that satisfies an interface
// a value of the method's receiver type reaches.
//
// The conversion set is the whole evidence: for each site where a value of a
// concrete type reaches a position typed as an interface, types.Implements
// decides satisfaction, and the method of the type answering each method the
// interface requires is retained at that site. A type whose values never reach an
// interface retains nothing, whatever it happens to implement, because no caller
// can dispatch to it through an interface the program never builds.
//
// A method satisfying two interfaces at two sites is retained twice, once per
// site, so a maintainer reading the retained set is shown every conversion that
// depends on the method rather than one of them.
//
// A satisfaction assertion is itself a use of the interface it names, which the
// interface kinds judge separately: the assertion retains the methods here and is
// still reported when nothing else uses the interface as a type.
func InterfaceSatisfactionDetector(in *Input) ([]graph.Exemption, error) {
	sites, err := conversionSites(in)
	if err != nil {
		return nil, err
	}

	var retained []graph.Exemption
	for i := range sites {
		c := &sites[i]
		if !types.Implements(c.From, c.To) {
			continue
		}
		methods := satisfying(c.From, c.To)
		if len(methods) == 0 {
			continue
		}
		site, err := in.Resolve.Render(c.Site)
		if err != nil {
			return nil, err
		}
		for _, m := range methods {
			id, inventoried := in.Resolve.Object(m)
			if !inventoried {
				continue
			}
			retained = append(retained, graph.Exemption{
				ID:     id,
				Class:  string(InterfaceSatisfaction),
				Site:   site,
				Detail: "satisfies " + c.name,
			})
		}
	}

	slices.SortFunc(retained, byHeldSymbol)
	return slices.CompactFunc(retained, sameExemption), nil
}

// satisfying returns the method of from that answers each method to requires, in
// the order to declares them. A method promoted from an embedded field is the
// method of the type embedded, which is the declaration a deletion would remove.
func satisfying(from types.Type, to *types.Interface) []types.Object {
	set := types.NewMethodSet(from)
	methods := make([]types.Object, 0, to.NumMethods())
	for m := range to.Methods() {
		sel := set.Lookup(m.Pkg(), m.Name())
		if sel == nil {
			continue
		}
		methods = append(methods, sel.Obj())
	}
	return methods
}

// byHeldSymbol orders two exemptions by site, then by the symbol retained and the
// detail recorded.
//
//nolint:gocritic // slices.SortFunc fixes a comparator's parameters to values.
func byHeldSymbol(a, b graph.Exemption) int {
	if c := graph.ByPosition(a.Site, b.Site); c != 0 {
		return c
	}
	if c := strings.Compare(string(a.ID), string(b.ID)); c != 0 {
		return c
	}
	return strings.Compare(a.Detail, b.Detail)
}

// sameExemption reports whether two exemptions state the same fact.
//
//nolint:gocritic // slices.CompactFunc fixes a comparator's parameters to values.
func sameExemption(a, b graph.Exemption) bool {
	return a.ID == b.ID && a.Class == b.Class && a.Site == b.Site && a.Detail == b.Detail
}
