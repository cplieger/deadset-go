package graph

import (
	"cmp"
	"slices"
	"strconv"
)

// unvisited is the index of a symbol the component walk has not reached.
const unvisited = -1

// Component is one strongly connected component of the dead subgraph, with its
// place in the acyclic graph over those components.
type Component struct {
	// Members are the component's own symbols, by site. A dead member of a dead
	// container is one of them rather than a component of its own.
	Members []SymbolID

	// Roots are the members no other dead component references, which are the
	// symbols a deletion starts at. A cycle has as many roots as it has members
	// no dead component outside it references.
	Roots []SymbolID

	// Falls are the symbols one deletion of this component removes: its members,
	// and every dead symbol only this component reaches. A dead symbol two
	// components both reach falls with neither and is reported by its own
	// component.
	Falls []SymbolID

	// Index is the component's position in the order the sweep returns, which is
	// what a report mints an identifier from.
	Index int

	// DeletableLines is the number of source lines the deletion removes, summed
	// over Falls.
	DeletableLines int
}

// Cascade is how much of a dead component a report carries. String returns the
// spelling a report and a message carry.
type Cascade uint8

// The two cascade output modes.
const (
	// CascadeRoots names each component by its roots and by the count of symbols
	// that fall with it.
	CascadeRoots Cascade = iota

	// CascadeFull names every member of the component as well.
	CascadeFull
)

var cascadeNames = [...]string{
	CascadeRoots: "roots",
	CascadeFull:  "full",
}

// String returns the mode's spelling, and a numbered form for a value outside the
// set so a message never loses the number it was given.
func (c Cascade) String() string {
	if int(c) >= len(cascadeNames) {
		return "Cascade(" + strconv.Itoa(int(c)) + ")"
	}
	return cascadeNames[c]
}

// Listing is what a report carries for one dead component under one cascade mode.
type Listing struct {
	// Roots are the component's roots, which the default mode reports.
	Roots []SymbolID

	// Members is every symbol of the component under CascadeFull, and empty
	// otherwise.
	Members []SymbolID

	// SymbolCount and DeletableLines are what falls with the component, under
	// either mode.
	SymbolCount    int
	DeletableLines int
}

// List returns what a report carries for the component under mode: its roots and
// the size of what falls with it, and every member where the mode lists a
// component in full.
func (c *Component) List(mode Cascade) Listing {
	l := Listing{Roots: c.Roots, SymbolCount: len(c.Falls), DeletableLines: c.DeletableLines}
	if mode == CascadeFull {
		l.Members = c.Members
	}
	return l
}

// componentsOf groups the symbols dead marks into strongly connected components,
// orders the components so that each precedes every component it reaches, and
// computes what falls with each. Both arguments carry one flag per symbol of the
// graph, in the graph's own order: which symbols are dead, and which of those a
// sweep admitted by the test-of-dead-code rule.
//
// The dead set arrives rather than being read from a sweep, because over a matrix
// of build configurations the set is the intersection of what several sweeps
// answered while the edges are this graph's, which is every configuration's: a
// report lists one dead component once, and a reference under any configuration is
// a reference, so a member or an edge one configuration alone holds belongs to the
// one component the same as any other.
func (g *Graph) componentsOf(dead, testOfDeadCode []bool) []Component {
	at, adj := g.deadSubgraph(dead, testOfDeadCode)
	if len(at) == 0 {
		return nil
	}
	compOf, members := connected(adj)
	edges, into := condense(adj, compOf, len(members))
	falls := fallSets(edges, members, into)
	predecessor := external(adj, compOf)

	components := make([]Component, len(members))
	for index, c := range order(edges, members) {
		component := Component{Index: index, Members: g.identify(at, members[c]), Falls: g.identify(at, falls[c])}
		for _, member := range members[c] {
			// A dead member of a dead container is never a root: the container
			// is the site the deletion starts at and the member falls with it.
			contained := g.parent[at[member]] != outside && dead[g.parent[at[member]]]
			if !predecessor[member] && !contained {
				component.Roots = append(component.Roots, g.symbols[at[member]].ID)
			}
		}
		for _, fell := range falls[c] {
			component.DeletableLines += span(&g.symbols[at[fell]])
		}
		components[index] = component
	}
	return components
}

// identify names the symbols at a set of positions in the dead subgraph.
func (g *Graph) identify(at, positions []int) []SymbolID {
	ids := make([]SymbolID, 0, len(positions))
	for _, p := range positions {
		ids = append(ids, g.symbols[at[p]].ID)
	}
	return ids
}

// deadSubgraph indexes the dead symbols and the edges between them: every
// reference one dead symbol makes to another, one edge each way between a dead
// member and its dead container, and the edge back from each target of an admitted
// test.
//
// The container edges are what place a dead type's fields and methods, and a dead
// interface's methods, inside the container's component rather than in components
// of their own. One direction alone would not: a member its container only points
// at is a component the container reaches rather than a member of it. The edge back
// from an admitted test's target is the same mechanism for the same reason: a
// subject never references its test, so nothing else would close the cycle that
// puts a test of dead code in the component of the code it exercises.
//
// The references are the ones the graph holds rather than the ones the mode
// counts. Only an edge between two dead symbols is here, a reference a test file
// made comes from a test declaration, and a dead test declaration's references are
// the cascade the run is asked for: what falls with it when it is deleted.
func (g *Graph) deadSubgraph(dead, testOfDeadCode []bool) (at []int, adj [][]int) {
	position := make([]int, len(g.symbols))
	for i := range g.symbols {
		position[i] = outside
		if dead[i] {
			position[i] = len(at)
			at = append(at, i)
		}
	}

	adj = make([][]int, len(at))
	for from, i := range at {
		g.referenceEdges(adj, position, from, i, testOfDeadCode)
		if p := g.parent[i]; p != outside && dead[p] {
			adj[from] = append(adj[from], position[p])
			adj[position[p]] = append(adj[position[p]], from)
		}
	}
	return at, adj
}

// referenceEdges adds the references the dead symbol at one position makes to
// other dead symbols, and the edge back from every production declaration an
// admitted test references.
func (g *Graph) referenceEdges(adj [][]int, position []int, from, at int, testOfDeadCode []bool) {
	for _, e := range g.out[at] {
		to := position[e.to]
		if to == outside {
			continue
		}
		adj[from] = append(adj[from], to)
		if testOfDeadCode[at] && !g.test[e.to] && g.subject[e.to] {
			adj[to] = append(adj[to], from)
		}
	}
}

// connected returns each dead symbol's component and each component's members by
// site, found by Tarjan's algorithm in one pass over the subgraph.
func connected(adj [][]int) (compOf []int, members [][]int) {
	t := &tarjan{
		adj:     adj,
		index:   make([]int, len(adj)),
		low:     make([]int, len(adj)),
		onStack: make([]bool, len(adj)),
	}
	for i := range t.index {
		t.index[i] = unvisited
	}
	for at := range adj {
		if t.index[at] == unvisited {
			t.connect(at)
		}
	}

	compOf = make([]int, len(adj))
	for c, group := range t.found {
		for _, at := range group {
			compOf[at] = c
		}
	}
	return compOf, t.found
}

// tarjan is the state of one run of Tarjan's algorithm over the dead subgraph.
// The components it finds are closed in an order in which every component follows
// the components it reaches.
type tarjan struct {
	adj     [][]int
	index   []int // per symbol, the order the walk reached it
	low     []int // per symbol, the lowest index its own walk reaches
	onStack []bool
	stack   []int
	found   [][]int
	next    int
}

// connect walks one dead symbol, keeps the lowest index the walk below it reaches,
// and closes a component at every symbol whose walk reaches nothing above it.
func (t *tarjan) connect(at int) {
	t.index[at], t.low[at] = t.next, t.next
	t.next++
	t.stack = append(t.stack, at)
	t.onStack[at] = true

	for _, to := range t.adj[at] {
		switch {
		case t.index[to] == unvisited:
			t.connect(to)
			t.low[at] = min(t.low[at], t.low[to])
		case t.onStack[to]:
			t.low[at] = min(t.low[at], t.index[to])
		}
	}

	if t.low[at] != t.index[at] {
		return
	}
	var group []int
	for {
		top := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.onStack[top] = false
		group = append(group, top)
		if top == at {
			break
		}
	}
	slices.Sort(group)
	t.found = append(t.found, group)
}

// condense returns, per component, the components it reaches in one step, and how
// many components reach it, counting each pair once. The result is acyclic,
// because an edge inside a component is not an edge between two.
func condense(adj [][]int, compOf []int, groups int) (edges [][]int, into []int) {
	edges = make([][]int, groups)
	into = make([]int, groups)
	seen := make(map[[2]int]struct{})
	for from, targets := range adj {
		for _, to := range targets {
			pair := [2]int{compOf[from], compOf[to]}
			if _, held := seen[pair]; held || pair[0] == pair[1] {
				continue
			}
			seen[pair] = struct{}{}
			edges[pair[0]] = append(edges[pair[0]], pair[1])
			into[pair[1]]++
		}
	}
	return edges, into
}

// external reports, per dead symbol, whether a dead component other than its own
// references it, which is what makes a member of a component a root or not.
func external(adj [][]int, compOf []int) []bool {
	referenced := make([]bool, len(adj))
	for from, targets := range adj {
		for _, to := range targets {
			if compOf[from] != compOf[to] {
				referenced[to] = true
			}
		}
	}
	return referenced
}

// order returns the components in the order a report is worked in: each component
// precedes every component it reaches, and two components neither of which
// reaches the other are ordered by the site of the first member of each, so one
// graph yields one order.
func order(edges, members [][]int) []int {
	sequence := make([]int, 0, len(edges))
	for c := range slices.Backward(edges) {
		sequence = append(sequence, c)
	}

	// The components are closed in an order in which every component follows the
	// ones it reaches, so reading it backwards reaches every component after the
	// components that reach it, which is what makes one pass enough.
	depth := make([]int, len(edges))
	for _, from := range sequence {
		for _, to := range edges[from] {
			depth[to] = max(depth[to], depth[from]+1)
		}
	}

	slices.SortFunc(sequence, func(a, b int) int {
		return cmp.Or(cmp.Compare(depth[a], depth[b]), cmp.Compare(members[a][0], members[b][0]))
	})
	return sequence
}

// fallSets returns, per component, the dead symbols one deletion of that
// component removes.
//
// A component reached from more than one root of the condensation falls with
// neither, because deleting either root leaves the other reaching it; it is
// reported by its own component instead. A component that is not a root of the
// condensation carries its own members alone, because whatever it reaches is
// reached through the root above it as well.
func fallSets(edges, members [][]int, into []int) [][]int {
	owners := rootOwners(edges, into)
	sets := make([][]int, len(edges))
	for c := range edges {
		set := slices.Clone(members[c])
		if into[c] == 0 {
			for d, reached := range closure(edges, c) {
				if reached && d != c && owners[d] == 1 {
					set = append(set, members[d]...)
				}
			}
		}
		slices.Sort(set)
		sets[c] = set
	}
	return sets
}

// rootOwners counts, per component, how many roots of the condensation reach it.
// Every component is reached by at least one, because a component no other
// component reaches is a root itself.
func rootOwners(edges [][]int, into []int) []int {
	owners := make([]int, len(edges))
	for c := range edges {
		if into[c] != 0 {
			continue
		}
		for d, reached := range closure(edges, c) {
			if reached {
				owners[d]++
			}
		}
	}
	return owners
}

// closure returns the components one component reaches, itself included.
func closure(edges [][]int, from int) []bool {
	reached := make([]bool, len(edges))
	reached[from] = true
	stack := []int{from}
	for len(stack) > 0 {
		at := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, to := range edges[at] {
			if !reached[to] {
				reached[to] = true
				stack = append(stack, to)
			}
		}
	}
	return reached
}
