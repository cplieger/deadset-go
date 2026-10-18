package liveness

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

func TestComponentsGroupACycleAndReportEveryMemberAsARoot(t *testing.T) {
	b := newGraphBuilder(t).add("alpha", "beta")
	b.ref("alpha", "beta")
	b.ref("beta", "alpha")
	r := b.graph().Sweep(&SweepPolicy{})

	// Each holds the other live under reference counting and no root reaches
	// either, so both are candidates under reachability and the cycle is one
	// component. A cycle has no entry point, so every member no dead component
	// outside it references is a root of it.
	if want := []string{"alpha reachability", "beta reachability"}; !slices.Equal(b.candidates(r), want) {
		t.Errorf("Sweep over a cycle of two dead declarations returned %v, want %v", b.candidates(r), want)
	}
	want := []grouped{{members: "alpha beta", roots: "alpha beta"}}
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

func TestComponentsHoldEverySymbolThatFallsWithTheRoot(t *testing.T) {
	b := cascadeChain(t)
	got := b.groups(b.graph().Sweep(&SweepPolicy{}))

	// The three declarations the head reaches are dead only through it, so they
	// fall with it and are members of its component: every finding the one
	// deletion removes names one component, and the head is its one root.
	want := []grouped{
		{members: "head first second third", roots: "head"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a chain of four dead declarations returned components %+v, want %+v", got, want)
	}
}

func TestComponentsJoinTwoRootsThatReachOneDeclaration(t *testing.T) {
	b := newGraphBuilder(t).add("leftRoot", "rightRoot", "shared")
	b.ref("leftRoot", "shared")
	b.ref("rightRoot", "shared")
	got := b.groups(b.graph().Sweep(&SweepPolicy{}))

	// Deleting either root alone leaves the other referencing the shared
	// declaration, which is dead only through the two of them, so the three are
	// one component with two roots and no finding names a component without one.
	want := []grouped{
		{members: "leftRoot rightRoot shared", roots: "leftRoot rightRoot"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over two roots reaching one declaration returned components %+v, want %+v", got, want)
	}
}

func TestComponentsPlaceADeadMemberInsideItsDeadContainer(t *testing.T) {
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "box", kind: inventory.KindType, lines: 4})
	b.declare(handSymbol{name: "lid", kind: inventory.KindField, parent: "box"})
	b.declare(handSymbol{name: "side", kind: inventory.KindField, parent: "box"})
	b.declare(handSymbol{name: "open", kind: inventory.KindMethod, parent: "box", lines: 2})
	b.declare(handSymbol{name: "fetcher", kind: inventory.KindInterface, lines: 3})
	b.declare(handSymbol{name: "fetch", kind: inventory.KindInterfaceMethod, parent: "fetcher"})
	b.ref("open", "box")
	b.ref("open", "lid")
	got := b.groups(b.graph().Sweep(&SweepPolicy{}))

	// A dead type's fields and methods and a dead interface's methods are members
	// of the container's component rather than components of their own, and the
	// container is the one root, because a deletion starts there and the members
	// fall with it.
	want := []grouped{
		{members: "box lid side open", roots: "box"},
		{members: "fetcher fetch", roots: "fetcher"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a dead type and a dead interface returned components %+v, want %+v", got, want)
	}
}

func TestComponentsOrderPlacesEachComponentAtItsFirstRoot(t *testing.T) {
	// Each component's reached declaration is written first, so the order the
	// sweep returns is the one the roots decide rather than the one the sites do.
	b := newGraphBuilder(t).add("lateTail", "earlyTail", "lateHead", "earlyHead")
	b.ref("lateHead", "lateTail")
	b.ref("earlyHead", "earlyTail")
	b.ref("earlyTail", "earlyHead")
	b.ref("lateHead", "lateTail")
	got := b.groups(b.graph().Sweep(&SweepPolicy{}))

	// The cycle and the chain are two components, each holding its own members by
	// site, and the component whose root cycle comes first in site order comes
	// first.
	want := []grouped{
		{members: "earlyTail earlyHead", roots: "earlyTail earlyHead"},
		{members: "lateTail lateHead", roots: "lateHead"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over one declaration reaching a declaration written above it returned components %+v, want %+v", got, want)
	}
}

func TestComponentsOrderIsTheSameOnEveryCall(t *testing.T) {
	b := cascadeChain(t)
	b.ref("third", "head")
	g := b.graph()

	first, second := g.Sweep(&SweepPolicy{}), g.Sweep(&SweepPolicy{})
	if got, want := b.groups(first), b.groups(second); !slices.Equal(got, want) {
		t.Errorf("Sweep returned a different order on the second call\n--- first\n%+v\n+++ second\n%+v", got, want)
	}
	// The reference back from the last declaration makes the whole chain one
	// component, which is the shape a report's identifier counter is minted over.
	want := []grouped{{members: "head first second third", roots: "head first second third"}}
	if got := b.groups(first); !slices.Equal(got, want) {
		t.Errorf("Sweep over a chain closed into a cycle returned components %+v, want %+v", got, want)
	}
}

func TestComponentsMarkNoMemberOfAReachedCycleAsARoot(t *testing.T) {
	b := newGraphBuilder(t).add("caller", "ping", "pong")
	b.ref("caller", "ping")
	b.ref("ping", "pong")
	b.ref("pong", "ping")
	got := b.groups(b.graph().Sweep(&SweepPolicy{}))

	// Only ping is referenced from outside the cycle, and pong is not, yet the
	// cycle as a whole is referenced: deleting pong alone leaves ping
	// referencing it, so neither is a root and the caller is the one root.
	want := []grouped{{members: "caller ping pong", roots: "caller"}}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a dead caller of a dead cycle returned components %+v, want %+v", got, want)
	}
}
