package graph

import (
	"slices"
	"testing"
)

func TestComponentsGroupACycleAndReportEveryMemberAsARoot(t *testing.T) {
	b := newGraphBuilder(t).add("alpha", "beta")
	b.ref("alpha", "beta")
	b.ref("beta", "alpha")
	r := b.graph().Sweep(&SweepInput{})

	// Each holds the other live under reference counting and no root reaches
	// either, so both are candidates under reachability and the cycle is one
	// component. A cycle has no entry point, so every member no dead component
	// outside it references is a root of it.
	if want := []string{"alpha reachability", "beta reachability"}; !slices.Equal(b.candidates(r), want) {
		t.Errorf("Sweep over a cycle of two dead declarations returned %v, want %v", b.candidates(r), want)
	}
	want := []grouped{{members: "alpha beta", roots: "alpha beta", lines: 2}}
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
	got := b.groups(b.graph().Sweep(&SweepInput{}))

	// The three declarations the head reaches are dead only through it, so they
	// fall with it and are members of its component: every finding the one
	// deletion removes names one component, and the head is its one root.
	want := []grouped{
		{members: "head first second third", roots: "head", lines: 10},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a chain of four dead declarations returned components %+v, want %+v", got, want)
	}
}

func TestComponentsJoinTwoRootsThatReachOneDeclaration(t *testing.T) {
	b := newGraphBuilder(t).add("leftRoot", "rightRoot", "shared")
	b.ref("leftRoot", "shared")
	b.ref("rightRoot", "shared")
	got := b.groups(b.graph().Sweep(&SweepInput{}))

	// Deleting either root alone leaves the other referencing the shared
	// declaration, which is dead only through the two of them, so the three are
	// one component with two roots and no finding names a component without one.
	want := []grouped{
		{members: "leftRoot rightRoot shared", roots: "leftRoot rightRoot", lines: 3},
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
	got := b.groups(b.graph().Sweep(&SweepInput{}))

	// A dead type's fields and methods and a dead interface's methods are members
	// of the container's component rather than components of their own, and the
	// container is the one root, because a deletion starts there and the members
	// fall with it.
	want := []grouped{
		{members: "box lid side open", roots: "box", lines: 8},
		{members: "fetcher fetch", roots: "fetcher", lines: 4},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a dead type and a dead interface returned components %+v, want %+v", got, want)
	}
}

func TestComponentsCountALineTwoFallingDeclarationsShareOnce(t *testing.T) {
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "box", kind: KindType, lines: 4})
	b.declare(handSymbol{name: "lid", kind: KindField, parent: "box", nested: true})
	b.declare(handSymbol{name: "side", kind: KindField, parent: "box", nested: true, lines: 2})
	b.declare(handSymbol{name: "open", kind: KindMethod, parent: "box", lines: 2})
	got := b.groups(b.graph().Sweep(&SweepInput{}))

	// The struct runs from its first line to its closing brace and its fields'
	// lines are inside that run, so the deletion removes the struct's four lines
	// and the method's two: six, where adding the spans of the four falling
	// declarations together would say nine.
	want := []grouped{{members: "box lid side open", roots: "box", lines: 6}}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a dead struct whose fields lie inside it returned components %+v, want %+v", got, want)
	}
}

func TestDistinctLinesCountsALineSeveralSpansCoverOnce(t *testing.T) {
	cases := map[string]struct {
		spans []Span
		want  int
	}{
		"no span": {want: 0},
		"one span": {
			spans: []Span{{Path: "a.go", First: 3, Last: 5}},
			want:  3,
		},
		"a span inside another": {
			spans: []Span{{Path: "a.go", First: 3, Last: 9}, {Path: "a.go", First: 4, Last: 6}},
			want:  7,
		},
		"two spans that overlap": {
			spans: []Span{{Path: "a.go", First: 5, Last: 8}, {Path: "a.go", First: 3, Last: 6}},
			want:  6,
		},
		"two spans that meet without sharing a line": {
			spans: []Span{{Path: "a.go", First: 3, Last: 4}, {Path: "a.go", First: 5, Last: 6}},
			want:  4,
		},
		"one span twice": {
			spans: []Span{{Path: "a.go", First: 7, Last: 7}, {Path: "a.go", First: 7, Last: 7}},
			want:  1,
		},
		"the same lines of two files": {
			spans: []Span{{Path: "a.go", First: 3, Last: 5}, {Path: "b.go", First: 3, Last: 5}},
			want:  6,
		},
		"a span that ends before the run it follows": {
			spans: []Span{
				{Path: "a.go", First: 1, Last: 10},
				{Path: "a.go", First: 2, Last: 3},
				{Path: "a.go", First: 9, Last: 12},
			},
			want: 12,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DistinctLines(test.spans); got != test.want {
				t.Errorf("DistinctLines(%+v) = %d, want %d", test.spans, got, test.want)
			}
		})
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
	got := b.groups(b.graph().Sweep(&SweepInput{}))

	// The cycle and the chain are two components, each holding its own members by
	// site, and the component whose root cycle comes first in site order comes
	// first.
	want := []grouped{
		{members: "earlyTail earlyHead", roots: "earlyTail earlyHead", lines: 2},
		{members: "lateTail lateHead", roots: "lateHead", lines: 2},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over one declaration reaching a declaration written above it returned components %+v, want %+v", got, want)
	}
}

func TestComponentsOrderIsTheSameOnEveryCall(t *testing.T) {
	b := cascadeChain(t)
	b.ref("third", "head")
	g := b.graph()

	first, second := g.Sweep(&SweepInput{}), g.Sweep(&SweepInput{})
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
	want := []grouped{{members: "head first second third", roots: "head first second third", lines: 10}}
	if got := b.groups(first); !slices.Equal(got, want) {
		t.Errorf("Sweep over a chain closed into a cycle returned components %+v, want %+v", got, want)
	}
}

func TestComponentListNamesEveryMemberOnlyWhereTheModeIsFull(t *testing.T) {
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "box", kind: KindType, lines: 2})
	b.declare(handSymbol{name: "lid", kind: KindField, parent: "box"})
	components := b.graph().Sweep(&SweepInput{}).Components
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

func TestComponentsMarkNoMemberOfAReachedCycleAsARoot(t *testing.T) {
	b := newGraphBuilder(t).add("caller", "ping", "pong")
	b.ref("caller", "ping")
	b.ref("ping", "pong")
	b.ref("pong", "ping")
	got := b.groups(b.graph().Sweep(&SweepInput{}))

	// Only ping is referenced from outside the cycle, and pong is not, yet the
	// cycle as a whole is referenced: deleting pong alone leaves ping
	// referencing it, so neither is a root and the caller is the one root.
	want := []grouped{{members: "caller ping pong", roots: "caller", lines: 3}}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a dead caller of a dead cycle returned components %+v, want %+v", got, want)
	}
}
