package graph

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
	test       int
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
	index   map[SymbolID]int
	symbols []Symbol
	out     [][]edge // per symbol, the references the symbol makes
	made    []counts // per symbol, the references made to the symbol
	parent  []int    // per symbol, the position of its container
	test    []bool   // per symbol, a test file declares it
	subject []bool   // per symbol, the sweep judges its liveness
	rooted  []rooted
}

// New indexes the outputs of Symbols, References and Roots.
//
// Each reference is kept in the adjacency it can serve. One whose From is not a
// symbol of the inventory still counts toward the references made to its target,
// because reference counting asks whether any declaration of the loaded graph
// references the symbol and a declaration outside the inventory is one. One whose
// To is not a symbol is kept nowhere, and neither is a root naming no symbol,
// because the inventory is what decides which symbols the analysis reasons about.
//
// The symbols slice is held rather than copied, so a caller that changes it
// afterwards changes what every later sweep answers, and the order it arrives in,
// which is the order Symbols returns and so by site, is the order every set in a
// Result reads in.
func New(symbols []Symbol, refs []Reference, roots []Root) *Graph {
	g := &Graph{
		index:   make(map[SymbolID]int, len(symbols)),
		symbols: symbols,
		out:     make([][]edge, len(symbols)),
		made:    make([]counts, len(symbols)),
		parent:  make([]int, len(symbols)),
		test:    make([]bool, len(symbols)),
		subject: make([]bool, len(symbols)),
	}
	for i := range symbols {
		g.index[symbols[i].ID] = i
	}
	for i := range symbols {
		s := &symbols[i]
		g.parent[i] = g.at(s.Parent)
		_, g.test[i] = IsTestFile(s.Pos.Filename)
		// A package and a file carry no reference of their own: an import names
		// the package's own identifier at the import spec rather than at the
		// package clause, and nothing names a file at all. Their liveness is the
		// subject of the file and package kinds, which read the load rather than
		// the reference set, so the sweep does not judge them.
		g.subject[i] = s.Kind != KindPackage && s.Kind != KindFile
	}
	for i := range refs {
		g.add(&refs[i])
	}
	for _, r := range roots {
		if at := g.at(r.ID); at != outside {
			g.rooted = append(g.rooted, rooted{at: at, kind: r.Kind})
		}
	}
	return g
}

// add keeps one reference in the adjacency and in the count of references made to
// its target.
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
// symbol at at. A production sweep counts none that a test file made.
func (g *Graph) references(at int, production bool) int {
	if production {
		return g.made[at].production
	}
	return g.made[at].production + g.made[at].test
}

// span is the number of source lines one symbol occupies. A declaration occupies
// at least the line it starts on, so the span a report sums is never below one.
func span(s *Symbol) int {
	return max(1, s.EndLine-s.Pos.Line+1)
}
