package graph

import (
	"slices"
	"testing"
)

func TestComponentsGroupACycleAndReportEveryMemberAsARoot(t *testing.T) {
	b := newGraphBuilder(t).add("alpha", "beta")
	b.ref("alpha", "beta")
	b.ref("beta", "alpha")
	r := b.graph().Sweep(Mode{})

	// Each holds the other live under reference counting and no root reaches
	// either, so both are candidates under reachability and the cycle is one
	// component. A cycle has no entry point, so every member no dead component
	// outside it references is a root of it.
	if want := []string{"alpha reachability", "beta reachability"}; !slices.Equal(b.candidates(r), want) {
		t.Errorf("Sweep over a cycle of two dead declarations returned %v, want %v", b.candidates(r), want)
	}
	want := []grouped{{members: "alpha beta", roots: "alpha beta", falls: "alpha beta", lines: 2}}
	if got := b.groups(r); !slices.Equal(got, want) {
		t.Errorf("Sweep over a cycle of two dead declarations returned components %+v, want %+v", got, want)
	}
}

// cascadeChain is one dead declaration reaching three further dead declarations,
// each of its own line span.
func cascadeChain(t *testing.T) *graphBuilder {
	t.Helper()
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "head", lines: 3})
	b.declare(handSymbol{name: "first", lines: 2})
	b.declare(handSymbol{name: "second", lines: 4})
	b.declare(handSymbol{name: "third", lines: 1})
	b.ref("head", "first")
	b.ref("first", "second")
	b.ref("second", "third")
	return b
}

func TestComponentsFallSetCoversWhatOnlyTheComponentReaches(t *testing.T) {
	b := cascadeChain(t)
	got := b.groups(b.graph().Sweep(Mode{}))

	// The count and the line total a report names at the root cover everything
	// the deletion removes, which is the component's own members plus every dead
	// symbol only this component reaches. A component another dead component
	// reaches is no root and carries its own members alone.
	want := []grouped{
		{members: "head", roots: "head", falls: "head first second third", lines: 10},
		{members: "first", falls: "first", lines: 2},
		{members: "second", falls: "second", lines: 4},
		{members: "third", falls: "third", lines: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a chain of four dead declarations returned components %+v, want %+v", got, want)
	}
}

func TestComponentsFallSetLeavesOutASymbolTwoComponentsReach(t *testing.T) {
	b := newGraphBuilder(t).add("leftRoot", "rightRoot", "shared")
	b.ref("leftRoot", "shared")
	b.ref("rightRoot", "shared")
	got := b.groups(b.graph().Sweep(Mode{}))

	// Deleting either root leaves the other reaching the shared declaration, so
	// it falls with neither and its own component is what reports it.
	want := []grouped{
		{members: "leftRoot", roots: "leftRoot", falls: "leftRoot", lines: 1},
		{members: "rightRoot", roots: "rightRoot", falls: "rightRoot", lines: 1},
		{members: "shared", falls: "shared", lines: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over two roots reaching one declaration returned components %+v, want %+v", got, want)
	}
}

func TestComponentsPlaceADeadMemberInsideItsDeadContainer(t *testing.T) {
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "box", kind: KindType, lines: 4})
	b.declare(handSymbol{name: "lid", kind: KindField, parent: "box"})
	b.declare(handSymbol{name: "side", kind: KindField, parent: "box"})
	b.declare(handSymbol{name: "open", kind: KindMethod, parent: "box", lines: 2})
	b.declare(handSymbol{name: "fetcher", kind: KindInterface, lines: 3})
	b.declare(handSymbol{name: "fetch", kind: KindInterfaceMethod, parent: "fetcher"})
	b.ref("open", "box")
	b.ref("open", "lid")
	got := b.groups(b.graph().Sweep(Mode{}))

	// A dead type's fields and methods and a dead interface's methods are members
	// of the container's component rather than components of their own, and the
	// container is the one root, because a deletion starts there and the members
	// fall with it.
	want := []grouped{
		{members: "box lid side open", roots: "box", falls: "box lid side open", lines: 8},
		{members: "fetcher fetch", roots: "fetcher", falls: "fetcher fetch", lines: 4},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a dead type and a dead interface returned components %+v, want %+v", got, want)
	}
}

func TestComponentsOrderPlacesEachComponentBeforeTheOnesItReaches(t *testing.T) {
	// The declaration that is reached is written first, so the order the sweep
	// returns is the one the references decide rather than the one the sites do.
	b := newGraphBuilder(t).add("tail", "head")
	b.ref("head", "tail")
	got := b.groups(b.graph().Sweep(Mode{}))

	// The components read from the root down while each component's own symbol
	// sets stay ordered by site, so what falls with the root reads in file order.
	want := []grouped{
		{members: "head", roots: "head", falls: "tail head", lines: 2},
		{members: "tail", falls: "tail", lines: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over one declaration reaching a declaration written above it returned components %+v, want %+v", got, want)
	}
}

func TestComponentsOrderIsTheSameOnEveryCall(t *testing.T) {
	b := cascadeChain(t)
	b.ref("third", "head")
	g := b.graph()

	first, second := g.Sweep(Mode{}), g.Sweep(Mode{})
	if got, want := b.groups(first), b.groups(second); !slices.Equal(got, want) {
		t.Errorf("Sweep returned a different order on the second call\n--- first\n%+v\n+++ second\n%+v", got, want)
	}
	for i, c := range first.Components {
		if c.Index != i {
			t.Errorf("Sweep().Components[%d].Index = %d, want %d", i, c.Index, i)
		}
	}
	// The reference back from the last declaration makes the whole chain one
	// component, which is the shape a report's identifier counter is minted over.
	want := []grouped{{members: "head first second third", roots: "head first second third", falls: "head first second third", lines: 10}}
	if got := b.groups(first); !slices.Equal(got, want) {
		t.Errorf("Sweep over a chain closed into a cycle returned components %+v, want %+v", got, want)
	}
}

func TestComponentListNamesEveryMemberOnlyWhereTheModeIsFull(t *testing.T) {
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "box", kind: KindType, lines: 2})
	b.declare(handSymbol{name: "lid", kind: KindField, parent: "box"})
	components := b.graph().Sweep(Mode{}).Components
	if len(components) != 1 {
		t.Fatalf("Sweep over a dead type and its field returned %d components, want 1", len(components))
	}

	cases := map[string]struct {
		mode        Cascade
		wantMembers []string
	}{
		"the default mode": {mode: CascadeRoots},
		"the full mode":    {mode: CascadeFull, wantMembers: []string{"box", "lid"}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			list := components[0].List(test.mode)
			if got := b.names(list.Members); !slices.Equal(got, test.wantMembers) {
				t.Errorf("Component.List(%s).Members = %v, want %v", test.mode, got, test.wantMembers)
			}
			if got := b.names(list.Roots); !slices.Equal(got, []string{"box"}) {
				t.Errorf("Component.List(%s).Roots = %v, want [box]", test.mode, got)
			}
			if list.SymbolCount != 2 || list.DeletableLines != 3 {
				t.Errorf("Component.List(%s) counts %d symbols over %d lines, want 2 over 3",
					test.mode, list.SymbolCount, list.DeletableLines)
			}
		})
	}
}

func TestCascadeStringNamesEveryMode(t *testing.T) {
	cases := []struct {
		mode Cascade
		want string
	}{
		{CascadeRoots, "roots"},
		{CascadeFull, "full"},
		{Cascade(200), "Cascade(200)"},
	}
	for _, test := range cases {
		t.Run(test.want, func(t *testing.T) {
			if got := test.mode.String(); got != test.want {
				t.Errorf("Cascade(%d).String() = %q, want %q", test.mode, got, test.want)
			}
		})
	}
}
