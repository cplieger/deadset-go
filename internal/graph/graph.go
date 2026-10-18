// Package graph merges the configurations of a matrix and sweeps it for the
// declarations nothing keeps live.
package graph

import (
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// outside is the position of a symbol the inventory does not hold.
const outside = -1

// edge is one reference as the sweep reads it: the position of the symbol
// referenced, and whether a test file made the reference.
type edge struct {
	to   int
	gate int // one past the index of the gate in [Graph.gates] the edge waits on, 0 for none
	test bool
}

// counts is how many references one symbol carries, split the way a kind reads
// them.
type counts struct {
	production int

	// test counts every reference a test file made, a loaded consumer's test
	// files included.
	test int

	// consumerTest counts the references a loaded consumer's test files made,
	// which is the part of test a mode may classify as production.
	consumerTest int
}

// calls is how the modules outside the target reference one symbol: from a
// consumer's production files, from a consumer's test files, or from neither.
type calls struct {
	fromProductionFile bool
	fromTestFile       bool
}

// rooted is one root the inventory holds, at the position of the symbol it names.
type rooted struct {
	at   int
	kind roots.RootKind
}

// Graph is one build configuration's symbols, the references between them and
// the roots that seed reachability, indexed for the sweep.
//
// A Graph is what the three passes before it produced and nothing else: it reads
// no file, renders no position and issues no query, so a sweep over it is a
// function of the values New was given. One Graph answers any number of sweeps.
type Graph struct {
	index    map[inventory.SymbolID]int
	symbols  []inventory.Symbol
	out      [][]edge // per symbol, the references the symbol makes
	made     []counts // per symbol, the references made to the symbol
	consumed []calls  // per symbol, how a loaded consumer references it
	parent   []int    // per symbol, the position of its container
	test     []bool   // per symbol, test code declares it: a test file or a test-support package
	subject  []bool   // per symbol, the sweep judges its liveness
	rooted   []rooted
	gates    [][]int // per gate, the positions of the symbols that must all be reached before an edge waiting on it is followed
}

// New indexes the outputs of inventory.Of, References and Roots. A reference from
// outside the inventory, a consumer's, still counts toward its target's references;
// one to a symbol outside it, and a root naming none, are kept nowhere. The symbols
// slice is held, not copied, and its order (by site) is the order every set of a
// Result reads in.
func New(symbols []inventory.Symbol, refs []facts.Reference, seeds []roots.Root) *Graph {
	g := &Graph{
		index:    make(map[inventory.SymbolID]int, len(symbols)),
		symbols:  symbols,
		out:      make([][]edge, len(symbols)),
		made:     make([]counts, len(symbols)),
		consumed: make([]calls, len(symbols)),
		parent:   make([]int, len(symbols)),
		test:     make([]bool, len(symbols)),
		subject:  make([]bool, len(symbols)),
	}
	for i := range symbols {
		g.index[symbols[i].ID] = i
	}
	for i := range symbols {
		s := &symbols[i]
		g.parent[i] = g.at(s.Parent)
		g.test[i] = s.InTestFile || s.TestSupport
		// A package and a file are not judged by either relation. Their liveness
		// is the subject of the file and package kinds, which read the load and
		// the file-to-package edges an import records rather than asking whether
		// anything references the file, which nothing ever does.
		g.subject[i] = s.Kind != inventory.KindPackage && s.Kind != inventory.KindFile
	}
	for i := range refs {
		g.add(&refs[i])
	}
	for _, r := range seeds {
		if at := g.at(r.ID); at != outside {
			g.rooted = append(g.rooted, rooted{at: at, kind: r.Kind})
		}
	}
	return g
}

// add keeps one reference in the adjacency, in the count of references made to its
// target, and where a loaded consumer made it, in the record that a module outside
// the target calls the symbol. It reports whether it appended an edge to From's
// adjacency.
func (g *Graph) add(r *facts.Reference) bool {
	to := g.at(r.To)
	if to == outside {
		return false
	}
	if r.Test {
		g.made[to].test++
	} else {
		g.made[to].production++
	}
	if r.Consumer != "" {
		if r.Test {
			g.made[to].consumerTest++
			g.consumed[to].fromTestFile = true
		} else {
			g.consumed[to].fromProductionFile = true
		}
	}
	from := g.at(r.From)
	if from == outside {
		return false
	}
	g.out[from] = append(g.out[from], edge{to: to, test: r.Test})
	return true
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
// symbol at at. A production sweep counts none that a test file made, except the
// ones a loaded consumer's test files made where the mode classifies those as
// production references.
func (g *Graph) references(at int, m Mode) int {
	c := g.made[at]
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
// A kind reads both numbers, so the split is the mode's classification rather than
// the mode's filter.
func (g *Graph) counted(at int, m Mode) (production, test int) {
	c := g.made[at]
	if m.ConsumerTestsProduction {
		return c.production + c.consumerTest, c.test - c.consumerTest
	}
	return c.production, c.test
}

// consumedIn reports whether a loaded consumer's reference to the symbol at at is
// one the given mode counts, which is what makes the symbol called from outside the
// target.
func (g *Graph) consumedIn(at int, m Mode) bool {
	c := g.consumed[at]
	return c.fromProductionFile || (c.fromTestFile && m.countsConsumerTests())
}

// countsConsumerTests reports whether the mode counts a loaded consumer's test-file
// reference as a call from outside the target.
func (m Mode) countsConsumerTests() bool {
	return !m.Production || m.ConsumerTestsProduction
}
