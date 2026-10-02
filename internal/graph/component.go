package graph

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// unvisited is the index of a symbol the component walk has not reached.
const unvisited = -1

// Component is one dead component: its root members and every dead symbol that
// falls with them, which is what one deletion removes.
//
// The dead symbols form cycles, a dead member and its dead container counting as
// referencing each other and a symbol on no cycle being a cycle of one. A root
// cycle is one no dead symbol outside it references. A symbol that only dead
// symbols reference falls with the root cycles that reach it, and a symbol two root
// cycles both reach is dead only through the two of them together, so a component
// holds every root cycle such a symbol links and every symbol they reach: deleting
// any part of it alone leaves a reference to what was deleted.
type Component struct {
	// Members are every symbol of the component, by site.
	Members []SymbolID

	// Roots are the members of the component's root cycles whose container is not
	// itself dead, by site: the symbols a deletion starts at. Every component has
	// at least one, because a member's dead container sits on the member's own
	// cycle.
	Roots []SymbolID

	// Spans are the lines each member occupies, in the order of Members.
	Spans []Span

	// Index is the component's position in the order the sweep returns, which is
	// what a report mints an identifier from.
	Index int

	// DeletableLines is the number of distinct source lines the deletion removes:
	// the lines Spans covers, a line two of them share counted once.
	DeletableLines int
}

// Span is the run of source lines one declaration occupies: its file, and its
// first and last line, both counted.
type Span struct {
	Path  string
	First int
	Last  int
}

// spanOf is the run of source lines one symbol occupies. A declaration occupies at
// least the line it starts on, so a span never ends above the line it starts on.
func spanOf(s *Symbol) Span {
	return Span{Path: s.Pos.Filename, First: s.Pos.Line, Last: max(s.Pos.Line, s.EndLine)}
}

// DistinctLines is the number of source lines a set of spans covers, a line two of
// them share counted once: a field's line inside its struct is one line whichever
// of the two a deletion is counted from.
func DistinctLines(spans []Span) int {
	ordered := slices.Clone(spans)
	slices.SortFunc(ordered, func(a, b Span) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.First, b.First))
	})

	total := 0
	var covered Span
	for i, s := range ordered {
		if i == 0 || s.Path != covered.Path || s.First > covered.Last {
			total += s.Last - s.First + 1
			covered = s
			continue
		}
		if s.Last > covered.Last {
			total += s.Last - covered.Last
			covered.Last = s.Last
		}
	}
	return total
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

	// SymbolCount and DeletableLines are the component's size, under either mode.
	SymbolCount    int
	DeletableLines int
}

// List returns what a report carries for the component under mode: its roots and
// its size, and every member where the mode lists a component in full.
func (c *Component) List(mode Cascade) Listing {
	l := Listing{Roots: c.Roots, SymbolCount: len(c.Members), DeletableLines: c.DeletableLines}
	if mode == CascadeFull {
		l.Members = c.Members
	}
	return l
}

// componentsOf groups the symbols dead marks into dead components, orders the
// components by their first root cycle, and computes each one's size. Both
// arguments carry one flag per symbol of the graph, in the graph's own order: which
// symbols are dead, and which of those a sweep admitted by the test-of-dead-code
// rule.
//
// The dead set arrives rather than being read from a sweep, because over a matrix
// of build configurations the set is the intersection of what several sweeps
// answered while the edges are this graph's, which is every configuration's: a
// report lists one dead component once, and a reference under any configuration is
// a reference, so a member or an edge one configuration alone holds belongs to the
// one component the same as any other.
func (g *Graph) componentsOf(dead, testOfDeadCode []bool) []Component {
	at, adj, place := g.deadSubgraph(dead, testOfDeadCode)
	if len(at) == 0 {
		return nil
	}
	cycleOf, cycles := connected(adj)
	edges, into := condense(adj, cycleOf, len(cycles))
	clusterOf, count := clusters(adj, cycleOf, order(edges, cycles), len(cycles))

	components := make([]Component, count)
	for index := range components {
		components[index].Index = index
	}
	for position, i := range at {
		cycle := cycleOf[position]
		component := &components[clusterOf[cycle]]
		id := g.symbols[i].ID
		component.Members = append(component.Members, id)
		component.Spans = append(component.Spans, spanOf(&g.symbols[i]))
		// A dead member of a dead container is never a root: the container is
		// the site the deletion starts at and the member falls with it.
		contained := g.parent[i] != outside && place[g.parent[i]] != outside
		if into[cycle] == 0 && !contained {
			component.Roots = append(component.Roots, id)
		}
	}
	for index := range components {
		components[index].DeletableLines = DistinctLines(components[index].Spans)
	}
	return components
}

// clusters numbers the dead components: two cycles joined by a reference belong to
// one, because the root cycles reaching either reach both. A component is numbered
// by the place its first cycle takes in the order the cycles are worked in, which
// puts every root cycle ahead of the cycles it reaches.
func clusters(adj [][]int, cycleOf, sequence []int, cycles int) (clusterOf []int, count int) {
	parent := make([]int, cycles)
	for c := range parent {
		parent[c] = c
	}
	find := func(c int) int {
		for parent[c] != c {
			parent[c] = parent[parent[c]]
			c = parent[c]
		}
		return c
	}
	for from, targets := range adj {
		for _, to := range targets {
			parent[find(cycleOf[from])] = find(cycleOf[to])
		}
	}

	numbered := make(map[int]int, cycles)
	clusterOf = make([]int, cycles)
	for _, c := range sequence {
		root := find(c)
		number, held := numbered[root]
		if !held {
			number = len(numbered)
			numbered[root] = number
		}
		clusterOf[c] = number
	}
	return clusterOf, len(numbered)
}

// deadSubgraph indexes the dead symbols a component holds and the edges between
// them: every reference one of them makes to another, one edge each way between a
// member and its container, and the edge back from each target of an admitted test.
// It returns each symbol's place in the subgraph as well, outside for one it leaves
// out.
//
// A declaration of a test file is held only where the sweep admitted it as a test of
// dead code. Any other dead test-file declaration belongs to no component, so its
// references join no two components and make no production declaration a non-root:
// a cluster only tests reach is rooted at a declaration outside the test files.
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
func (g *Graph) deadSubgraph(dead, testOfDeadCode []bool) (at []int, adj [][]int, position []int) {
	position = make([]int, len(g.symbols))
	for i := range g.symbols {
		position[i] = outside
		if dead[i] && (!g.test[i] || testOfDeadCode[i]) {
			position[i] = len(at)
			at = append(at, i)
		}
	}

	adj = make([][]int, len(at))
	for from, i := range at {
		g.referenceEdges(adj, position, from, i, testOfDeadCode)
		if p := g.parent[i]; p != outside && position[p] != outside {
			adj[from] = append(adj[from], position[p])
			adj[position[p]] = append(adj[position[p]], from)
		}
	}
	return at, adj, position
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

// connected returns each dead symbol's cycle and each cycle's members by site,
// found by Tarjan's algorithm in one pass over the subgraph.
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
// The cycles it finds are closed in an order in which every cycle follows the
// cycles it reaches.
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
// and closes a cycle at every symbol whose walk reaches nothing above it.
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

// condense returns, per cycle, the cycles it reaches in one step, and how many
// cycles reach it, counting each pair once. The result is acyclic, because an
// edge inside a cycle is not an edge between two.
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

// order returns the cycles in the order a report is worked in: each cycle precedes
// every cycle it reaches, and two cycles neither of which reaches the other are
// ordered by the site of the first member of each, so one graph yields one order.
func order(edges, members [][]int) []int {
	sequence := make([]int, 0, len(edges))
	for c := range slices.Backward(edges) {
		sequence = append(sequence, c)
	}

	// The cycles are closed in an order in which every cycle follows the ones it
	// reaches, so reading it backwards reaches every cycle after the cycles that
	// reach it, which is what makes one pass enough.
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
