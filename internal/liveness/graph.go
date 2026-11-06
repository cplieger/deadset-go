// Package liveness merges the configurations of a matrix into one graph and sweeps
// it for the declarations nothing keeps live, settling each declaration at the
// strongest tier that holds it and recording the path that does.
package liveness

import (
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// outside is the position of a symbol the inventory does not hold.
const outside = -1

// edge is one reference or use as the sweep reads it: the position of the symbol
// it names, whether a test file made it, and the hop a path through it follows.
type edge struct {
	to   int
	gate int // one past the index of the gate in [Graph.gates] the edge waits on, 0 for none
	hop  int // the index of the edge's hop in [Graph.hops]
	test bool
	held bool // an exemption's use by its holder, which the exemption tier follows
}

// counts is how many references one symbol carries, split the way a kind reads
// them.
type counts struct {
	production int

	// test counts every reference test code made, a loaded consumer's test
	// files included.
	test int

	// consumerTest counts the references a consumer build's test files made,
	// which is the part of test a mode may classify as production.
	consumerTest int
}

// calls is how the code outside the inventory's declarations references one
// symbol: per kind of file, one past the place in [Graph.refs] of the first
// reference such a file makes to it, 0 for none.
type calls struct {
	production   int // a consumer build's file other than a test file
	consumerTest int // a consumer build's test file
	testBuild    int // a file of a [load.TestBuild] package
}

// rooted is one root the inventory holds, at the position of the symbol it names.
// A blank root an exemption names is yielded to the exemption's holders: it seeds
// nothing until the exemptions are withdrawn.
type rooted struct {
	root    roots.Root
	at      int
	yielded bool
}

// Graph is one matrix's symbols, the references every configuration makes between
// them and the roots that seed reachability, indexed for the sweep.
//
// A Graph reads no file, renders no position and issues no query, so a sweep over
// it is a function of the values it was built from. One Graph answers any number
// of sweeps.
type Graph struct {
	index    map[inventory.SymbolID]int
	tests    roots.TestClasses
	symbols  []inventory.Symbol
	hops     []Hop
	out      [][]edge // per symbol, the references the symbol makes
	made     []counts // per symbol, the references made to the symbol
	heldMade []counts // per symbol, the part of made an exemption's holders make
	consumed []calls  // per symbol, how a loaded consumer references it
	parents  [][]int  // per symbol, the positions of its containers the inventory holds
	test     []bool   // per symbol, test code declares it: a test file or a test-support package
	subject  []bool   // per symbol, the sweep judges its liveness
	rooted   []rooted
	gates    [][]int // per gate, the positions of the symbols that must all be reached before an edge waiting on it is followed
	refs     []facts.Reference

	// carrying is every configuration's own references, each with its own carriers,
	// which [Result.Carried] reads: an edge of refs merges a reference several
	// configurations make whatever carriers each gives it.
	carrying []facts.Reference
}

// New indexes one configuration's symbols, references and roots. A reference from
// outside the inventory, a consumer's, still counts toward its target's references;
// one to a symbol outside it, and a root naming none, are kept nowhere. The symbols
// slice is held, not copied, and its order (by site) is the order every set of a
// Result reads in.
func New(symbols []inventory.Symbol, refs []facts.Reference, seeds []roots.Root) *Graph {
	g := &Graph{
		index:    make(map[inventory.SymbolID]int, len(symbols)),
		symbols:  symbols,
		refs:     refs,
		carrying: refs,
		out:      make([][]edge, len(symbols)),
		made:     make([]counts, len(symbols)),
		heldMade: make([]counts, len(symbols)),
		consumed: make([]calls, len(symbols)),
		parents:  make([][]int, len(symbols)),
		test:     make([]bool, len(symbols)),
		subject:  make([]bool, len(symbols)),
	}
	for i := range symbols {
		g.index[symbols[i].ID] = i
	}
	for i := range symbols {
		s := &symbols[i]
		for _, id := range s.Parents {
			if at := g.at(id); at != outside {
				g.parents[i] = append(g.parents[i], at)
			}
		}
		g.test[i] = s.InTestFile || s.TestSupport
		// A package and a file are not judged by either relation. Their liveness
		// is the subject of the file and package kinds, which read the load and
		// the file-to-package edges an import records rather than asking whether
		// anything references the file, which nothing ever does.
		g.subject[i] = s.Kind != inventory.KindPackage && s.Kind != inventory.KindFile
	}
	for i := range refs {
		g.add(Hop{Reference: &refs[i], From: refs[i].From, To: refs[i].To}, refs[i].Test)
		g.consume(i)
	}
	for _, r := range seeds {
		if at := g.at(r.ID); at != outside {
			g.rooted = append(g.rooted, rooted{at: at, root: r})
		}
	}
	return g
}

// Of indexes one merged matrix as one graph over every configuration's references
// and roots: a declaration is live where any configuration's references keep it,
// so the graph holds them all at once ([grammar/analysis/matrix.md] matrix.4 and
// matrix.5).
//
// A declaration whose set names no configuration of the matrix is judged by
// neither relation: dead in every configuration it exists in would be vacuously
// true of it.
func Of(m *Merged) *Graph {
	g := New(m.Symbols, distinct(m.References), m.Roots)
	g.carrying = m.References
	g.tests = m.Tests
	for i := range g.symbols {
		if !slices.ContainsFunc(g.symbols[i].Configs.Indexes(), func(c int) bool { return c < m.Configurations }) {
			g.subject[i] = false
		}
	}
	for _, use := range m.Uncalled.Uses {
		g.add(Hop{From: use.From, To: use.To}, false)
	}
	return g
}

// add keeps one hop in the adjacency and in the count of references made to its
// target. It reports whether it appended an edge to the hop's source.
func (g *Graph) add(hop Hop, test bool) bool {
	to := g.at(hop.To)
	if to == outside {
		return false
	}
	if test {
		g.made[to].test++
	} else {
		g.made[to].production++
	}
	if r := hop.Reference; r != nil && r.Build == load.ConsumerBuild && r.Test {
		g.made[to].consumerTest++
	}
	from := g.at(hop.From)
	if from == outside {
		return false
	}
	g.hops = append(g.hops, hop)
	g.out[from] = append(g.out[from], edge{to: to, test: test, hop: len(g.hops) - 1})
	return true
}

// consume records the reference at i in [Graph.refs], where a loaded consumer made
// it, as the first such call of its target if it is.
func (g *Graph) consume(i int) {
	r, to := &g.refs[i], g.at(g.refs[i].To)
	if r.Build == load.TargetBuild || to == outside {
		return
	}
	first := &g.consumed[to].production
	switch {
	case r.Build == load.TestBuild:
		first = &g.consumed[to].testBuild
	case r.Test:
		first = &g.consumed[to].consumerTest
	}
	if *first == 0 {
		*first = i + 1
	}
}

// at returns the position of the symbol id names, or outside when the inventory
// holds no such symbol. The empty identifier names none.
func (g *Graph) at(id inventory.SymbolID) int {
	if i, held := g.index[id]; held {
		return i
	}
	return outside
}

// references reports how many references a sweep in the given mode counts to the
// symbol at at, an exemption's uses included.
func (g *Graph) references(at int, m facts.Mode) int { return g.made[at].under(m) }

// under is how many of the counted references a sweep in the given mode counts. A
// production sweep counts none that a test file made, except the ones a loaded
// consumer's test files made where the mode classifies those as production
// references.
func (c counts) under(m facts.Mode) int {
	if !m.Production {
		return c.production + c.test
	}
	if m.ConsumerTestsProduction {
		return c.production + c.consumerTest
	}
	return c.production
}

// counted splits the references made to the symbol at at the way the mode
// classifies them, whatever the mode counts: a reference from a loaded consumer's
// test file is a test reference, and a production reference where the mode says so.
func (g *Graph) counted(at int, m facts.Mode) (production, test int) {
	c := g.made[at]
	if m.ConsumerTestsProduction {
		return c.production + c.consumerTest, c.test - c.consumerTest
	}
	return c.production, c.test
}

// testReferenced reports whether a test file references the symbol at at, an
// exemption's use by a holder of a test file aside.
func (g *Graph) testReferenced(at int) bool {
	return g.made[at].test > g.heldMade[at].test
}

// consumedIn reports whether a loaded consumer's reference to the symbol at at is
// one the given mode counts, which is what makes the symbol called from code the
// inventory does not declare.
func (g *Graph) consumedIn(at int, m facts.Mode) bool {
	return g.consumedFirst(at, m) > 0
}

// consumedFirst is one past the place in [Graph.refs] of the first reference of
// the kinds [calls] holds, in its order, that the mode counts to the symbol at at,
// 0 for none.
func (g *Graph) consumedFirst(at int, m facts.Mode) int {
	c := g.consumed[at]
	for _, first := range [...]int{c.production, c.consumerTest, c.testBuild} {
		if first > 0 && !m.Uncounted(&g.refs[first-1]) {
			return first
		}
	}
	return 0
}
