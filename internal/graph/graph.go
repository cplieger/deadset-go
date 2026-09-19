package graph

import "slices"

// outside is the position of a symbol the inventory does not hold.
const outside = -1

// edge is one reference as the sweep reads it: the position of the symbol
// referenced, and whether a test file made the reference.
type edge struct {
	to   int
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
	// by names the consumers that reference the symbol, each once, in the order
	// their module paths sort.
	by []string

	fromProductionFile bool
	fromTestFile       bool
}

// rooted is one root the inventory holds, at the position of the symbol it names.
type rooted struct {
	at   int
	kind RootKind
}

// Graph is one build configuration's symbols, the references between them and
// the roots that seed reachability, indexed for the sweep.
//
// A Graph is what the three passes before it produced and nothing else: it reads
// no file, renders no position and issues no query, so a sweep over it is a
// function of the values New was given. One Graph answers any number of sweeps.
type Graph struct {
	index    map[SymbolID]int
	symbols  []Symbol
	out      [][]edge // per symbol, the references the symbol makes
	made     []counts // per symbol, the references made to the symbol
	consumed []calls  // per symbol, how a loaded consumer references it
	parent   []int    // per symbol, the position of its container
	test     []bool   // per symbol, a test file declares it
	subject  []bool   // per symbol, the sweep judges its liveness
	rooted   []rooted
}

// New indexes the outputs of Symbols, References and Roots.
//
// Each reference is kept in the adjacency it can serve. One whose From is not a
// symbol of the inventory still counts toward the references made to its target,
// because reference counting asks whether any declaration of the loaded graph
// references the symbol and a declaration outside the inventory is one, a loaded
// consumer's being the case that arrives by design. One whose To is not a symbol
// is kept nowhere, and neither is a root naming no symbol, because the inventory
// is what decides which symbols the analysis reasons about.
//
// The symbols slice is held rather than copied, so a caller that changes it
// afterwards changes what every later sweep answers, and the order it arrives in,
// which is the order Symbols returns and so by site, is the order every set in a
// Result reads in.
func New(symbols []Symbol, refs []Reference, roots []Root) *Graph {
	g := &Graph{
		index:    make(map[SymbolID]int, len(symbols)),
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
		_, g.test[i] = IsTestFile(s.Pos.Filename)
		// A package and a file are not judged by either relation. Their liveness
		// is the subject of the file and package kinds, which read the load and
		// the file-to-package edges an import records rather than asking whether
		// anything references the file, which nothing ever does.
		g.subject[i] = s.Kind != KindPackage && s.Kind != KindFile
	}
	for i := range refs {
		g.add(&refs[i])
	}
	for i := range g.consumed {
		slices.Sort(g.consumed[i].by)
	}
	for _, r := range roots {
		if at := g.at(r.ID); at != outside {
			g.rooted = append(g.rooted, rooted{at: at, kind: r.Kind})
		}
	}
	return g
}

// add keeps one reference in the adjacency, in the count of references made to its
// target, and where a loaded consumer made it, in the record that a module outside
// the target calls the symbol.
func (g *Graph) add(r *Reference) {
	to := g.at(r.To)
	if to == outside {
		return
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
		if !slices.Contains(g.consumed[to].by, r.Consumer) {
			g.consumed[to].by = append(g.consumed[to].by, r.Consumer)
		}
	}
	if from := g.at(r.From); from != outside {
		g.out[from] = append(g.out[from], edge{to: to, test: r.Test})
	}
}

// at returns the position of the symbol id names, or outside when the inventory
// holds no such symbol. The empty identifier names none.
func (g *Graph) at(id SymbolID) int {
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

// ConsumersOf names the loaded consumers whose references reach the symbol id
// names, in the order their module paths sort, and nothing for a symbol no
// consumer references.
//
// It is the per-symbol half of the consumer answer: which of the consumers a run
// loaded actually use this declaration, where the run's loaded set says which were
// available to. A test reference is in the set whatever a mode counts, because the
// question is which module names the symbol.
func (g *Graph) ConsumersOf(id SymbolID) []string {
	at := g.at(id)
	if at == outside {
		return nil
	}
	return slices.Clone(g.consumed[at].by)
}

// consumedIn reports whether a loaded consumer's reference to the symbol at at is
// one the given mode counts, which is what makes the symbol called from outside the
// target.
func (g *Graph) consumedIn(at int, m Mode) bool {
	c := g.consumed[at]
	switch {
	case c.fromProductionFile:
		return true
	case !c.fromTestFile:
		return false
	case !m.Production:
		return true
	default:
		return m.ConsumerTestsProduction
	}
}

// span is the number of source lines one symbol occupies. A declaration occupies
// at least the line it starts on, so the span a report sums is never below one.
func span(s *Symbol) int {
	return max(1, s.EndLine-s.Pos.Line+1)
}
