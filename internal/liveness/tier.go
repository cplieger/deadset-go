package liveness

import (
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// tier is how strongly something holds a symbol live, strongest first: the path a
// symbol answers is the one its strongest tier's pass reached it by.
type tier uint8

// The tiers. An exemption outranks a mark, so a mark on a symbol an exemption holds
// holds nothing back.
const (
	unreached     tier = iota
	callerTier         // a root or a loaded consumer's reference reaches it
	exemptionTier      // only an exemption, or a use one states, reaches it
	markTier           // only a suppression mark reaches it
	tierBound          // one past the weakest tier: a view that counts every tier
)

// The seed a tier pass reached a symbol at, where it was reached from no other: a
// root by its place among the roots, or one of these.
const (
	seedConsumer  = -1
	seedExemption = -2
	seedMark      = -3
)

// labels is one labelling of the symbols: the strongest tier reaching each, and per
// tier pass the symbol each was reached from and the hop or seed that reached it.
type labels struct {
	tier []tier
	from [tierBound - 1][]int
	via  [tierBound - 1][]int
}

// provenance is how the sweep reached each symbol, labelled under the mode.
type provenance struct {
	g *Graph
	p *SweepPolicy

	// counting labels the symbols by every reference, a test file's included,
	// under a production mode, so a path answers for a symbol only tests reach;
	// nil under any other mode, whose labels already count every reference.
	counting *labels

	labels
}

// settle labels the symbols under the mode, and under a production mode once more
// by every reference.
func (s *sweep) settle() *provenance {
	pr := &provenance{g: s.g, p: s.p, labels: s.label(s.p.Mode)}
	if s.p.Mode.Production {
		counting := s.label(facts.Mode{ConsumerTestsProduction: s.p.Mode.ConsumerTestsProduction})
		pr.counting = &counting
	}
	return pr
}

// label runs one reachability pass per tier under one mode, each over the seeds
// and edges of its own tier and every stronger one, and settles each symbol at the
// first that reaches it. A pass that adds no seed and no edge repeats the one
// before it.
func (s *sweep) label(m facts.Mode) labels {
	l := labels{tier: make([]tier, len(s.g.symbols))}
	heldEdges := anyHeldUse(s.g)
	for t := callerTier; t < tierBound; t++ {
		if t > callerTier && !s.addsAt(t, heldEdges) {
			l.from[t-1], l.via[t-1] = l.from[t-2], l.via[t-2]
			continue
		}
		var marked []bool
		if t >= markTier {
			marked = s.marked
		}
		reached, from, via := s.pass(t >= exemptionTier, marked, false, m)
		l.from[t-1], l.via[t-1] = from, via
		for i := range reached {
			if reached[i] && l.tier[i] == unreached {
				l.tier[i] = t
			}
		}
	}
	return l
}

// anyHeldUse reports whether the graph holds an exemption's use.
func anyHeldUse(g *Graph) bool {
	for i := range g.out {
		for _, e := range g.out[i] {
			if e.held {
				return true
			}
		}
	}
	return false
}

// addsAt reports whether the pass of tier t has a seed or an edge the pass before
// it lacks.
func (s *sweep) addsAt(t tier, heldEdges bool) bool {
	switch t {
	case exemptionTier:
		return heldEdges || anyOf(s.exempt)
	case markTier:
		return anyOf(s.marked)
	}
	return true
}

// anyOf reports whether a flag is set.
func anyOf(flags []bool) bool {
	for _, f := range flags {
		if f {
			return true
		}
	}
	return false
}

// pass is one reachability under one mode, seeded by the exemptions and their uses
// where exemptions is set, by the marks marked flags, and by the yielded roots
// where yielded is set, with the symbol and hop or seed each was reached by. The held
// seeds (exemptions, then marks) reach through every reference, the caller seeds
// through those the mode counts; the held seeds run first, so a symbol their
// closure reached is already expanded under the wider rule when the callers reach
// it.
func (s *sweep) pass(exemptions bool, marked []bool, yielded bool, m facts.Mode) (reached []bool, from, via []int) {
	n := len(s.g.symbols)
	reached = make([]bool, n)
	from, via = make([]int, n), make([]int, n)
	record := func(at, by, hop int) { from[at], via[at] = by, hop }
	edges := func(e edge) bool { return e.held && !exemptions }
	var queue []int
	for i := range s.g.symbols {
		switch {
		case exemptions && s.exempt[i]:
			record(i, -1, seedExemption)
		case i < len(marked) && marked[i]:
			record(i, -1, seedMark)
		default:
			continue
		}
		reached[i] = true
		queue = append(queue, i)
	}
	closure(s.g.out, s.g.gates, reached, queue, edges, record)
	queue = queue[:0]
	s.g.callerSeeds(m, reached, yielded, func(at, root int) {
		record(at, -1, root)
		queue = append(queue, at)
	})
	closure(s.g.out, s.g.gates, reached, queue, func(e edge) bool { return edges(e) || !follows(m, e.test) }, record)
	// A test root a production mode does not seed is still live: it runs.
	for i, r := range s.g.rooted {
		if (s.called[r.at] || (yielded && s.yielded[r.at])) && !reached[r.at] {
			reached[r.at] = true
			record(r.at, -1, i)
		}
	}
	return reached, from, via
}

// reached reports whether a held mechanism the view counts, or a caller, reaches
// the symbol at i.
func (pr *provenance) reached(i int, v *view) bool {
	switch {
	case !v.exemptions:
		return v.reached[i]
	case len(v.marked) > 0:
		return pr.tier[i] != unreached
	}
	return pr.tier[i] == callerTier || pr.tier[i] == exemptionTier
}

// callerSeeds visits, in order, every root and then every symbol a loaded consumer
// references in a way the mode counts, except the test roots under a production
// mode, a yielded root unless yielded is set, and the symbols already reached, and
// marks each reached. root is the seed's place among the roots, seedConsumer for a
// consumer's seed.
func (g *Graph) callerSeeds(m facts.Mode, reached []bool, yielded bool, visit func(at, root int)) {
	for i, r := range g.rooted {
		if (m.Production && r.root.Kind == roots.RootTest) || (r.yielded && !yielded) || reached[r.at] {
			continue
		}
		reached[r.at] = true
		visit(r.at, i)
	}
	for i := range g.symbols {
		if reached[i] || !g.consumedIn(i, m) {
			continue
		}
		reached[i] = true
		visit(i, seedConsumer)
	}
}

// follows reports whether the mode's caller reachability follows a reference a test
// file made or not: a production mode skips the test file's.
func follows(m facts.Mode, test bool) bool {
	return !m.Production || !test
}

// closure follows, breadth first, every edge of out from the queue's symbols that
// skip does not refuse, an edge waiting on a gate of gates only once every symbol of
// the gate is reached, and records each symbol it reaches with the symbol and hop
// that reached it. Every holder of a gate has an edge waiting on it, and a symbol is
// reached before it is expanded, so the last holder expanded finds the gate open.
func closure(out [][]edge, gates [][]int, reached []bool, queue []int, skip func(e edge) bool, record func(at, by, hop int)) {
	for head := 0; head < len(queue); head++ {
		at := queue[head]
		for _, e := range out[at] {
			if reached[e.to] || skip(e) || (e.gate > 0 && !open(gates[e.gate-1], reached)) {
				continue
			}
			reached[e.to] = true
			if record != nil {
				record(e.to, at, e.hop)
			}
			queue = append(queue, e.to)
		}
	}
}

// open reports whether every symbol a gate waits on is reached.
func open(gate []int, reached []bool) bool {
	for _, at := range gate {
		if !reached[at] {
			return false
		}
	}
	return true
}

// testUsed is [Result.TestUsed], one gated pass over the dead subgraph of the full
// view. It seeds the dead production declarations of packages only tests reach that
// a test references, and follows every reference and containment into a dead
// declaration that is no test of dead code. A test of dead code waits on a gate of
// its dead production targets, so it is reached once every one of them is, and its
// own references are followed from there.
func (s *sweep) testUsed(v *view) map[inventory.SymbolID]bool {
	out, gates := s.testUseEdges(v)
	reached := make([]bool, len(s.g.symbols))
	var queue []int
	for i := range s.g.symbols {
		if deadProduction(s.g, v, i) && s.g.testReferenced(i) && s.g.tests.Reached[s.g.symbols[i].Package] {
			reached[i] = true
			queue = append(queue, i)
		}
	}
	closure(out, gates, reached, queue, func(edge) bool { return false }, nil)
	used := make(map[inventory.SymbolID]bool)
	for i := range reached {
		if reached[i] {
			used[s.g.symbols[i].ID] = true
		}
	}
	return used
}

// testUseEdges is the edges [sweep.testUsed] follows and the gates its tests of dead
// code wait on.
func (s *sweep) testUseEdges(v *view) (out [][]edge, gates [][]int) {
	out = s.deadEdges(v)
	for i := range s.g.symbols {
		if !v.testOfDeadCode[i] {
			continue
		}
		targets := s.deadTargets(v, i)
		if len(targets) == 0 {
			continue
		}
		gates = append(gates, targets)
		for _, target := range targets {
			out[target] = append(out[target], edge{to: i, gate: len(gates)})
		}
	}
	return out, gates
}

// deadEdges is, per symbol, a reference or containment into each dead declaration
// of the view that is no test of dead code.
func (s *sweep) deadEdges(v *view) [][]edge {
	out := make([][]edge, len(s.g.symbols))
	follow := func(from, to int) {
		if v.dead[to] && !v.testOfDeadCode[to] {
			out[from] = append(out[from], edge{to: to})
		}
	}
	for i := range s.g.symbols {
		if p := s.g.parent[i]; p != outside {
			follow(p, i)
		}
		for _, e := range s.g.out[i] {
			if s.g.hops[e.hop].Reference != nil {
				follow(i, e.to)
			}
		}
	}
	return out
}

// deadTargets is, once each, the dead production declarations the references of
// the symbol at i name.
func (s *sweep) deadTargets(v *view, i int) []int {
	var targets []int
	for _, e := range s.g.out[i] {
		if s.g.hops[e.hop].Reference != nil && deadProduction(s.g, v, e.to) && !slices.Contains(targets, e.to) {
			targets = append(targets, e.to)
		}
	}
	return targets
}

// deadProduction reports whether the view judges the symbol at i a dead production
// declaration: a candidate outside the test files that is no test of dead code.
func deadProduction(g *Graph, v *view, i int) bool {
	return v.dead[i] && !v.testOfDeadCode[i] && !g.symbols[i].InTestFile
}

// Hop is one edge a path follows, From's reference to or use of To: a reference, a
// use the analysis infers, or a use an exemption states.
type Hop struct {
	// Reference is the reference followed, nil for a use.
	Reference *facts.Reference

	// Exemption is the exemption whose holder From makes the use, nil for a
	// reference and for an inferred use.
	Exemption *exempt.Exemption

	From, To inventory.SymbolID
}

// Path is how the sweep reached one symbol at the tier that holds it: the seed the
// path starts at and the hops it follows. Exactly one of Root, Exemption and Mark
// names the seed, except where a loaded consumer's reference seeds it, which is
// then the first hop.
type Path struct {
	Root      *roots.Root
	Exemption *exempt.Exemption
	Mark      inventory.SymbolID
	Hops      []Hop
}

// Path is the path the sweep reached the symbol by at the tier that holds it, and
// false where nothing reaches it. Under a production mode, where the mode's own
// labelling leaves the symbol unreached, Path answers from the labelling that
// counts every reference, a test file's included. Each tier pass expands its held
// seeds, then the roots in the merge's order and the loaded consumers, breadth
// first, so one tree answers the same path on every run.
func (r *Result) Path(to inventory.SymbolID) (Path, bool) {
	pr := r.paths
	if pr == nil {
		return Path{}, false
	}
	at := pr.g.at(to)
	if at == outside {
		return Path{}, false
	}
	l := &pr.labels
	if l.tier[at] == unreached && pr.counting != nil {
		l = pr.counting
	}
	if l.tier[at] == unreached {
		return Path{}, false
	}
	from, via := l.from[l.tier[at]-1], l.via[l.tier[at]-1]
	var hops []Hop
	for ; from[at] >= 0; at = from[at] {
		hops = append(hops, pr.g.hops[via[at]])
	}
	reverse(hops)
	path := Path{Hops: hops}
	seed := pr.g.symbols[at].ID
	switch kind := via[at]; {
	case kind >= 0:
		root := pr.g.rooted[kind].root
		path.Root = &root
	case kind == seedExemption:
		path.Exemption = pr.exemptionOf(seed)
	case kind == seedMark:
		path.Mark = seed
	default:
		path.Hops = append([]Hop{pr.consumerHop(at)}, hops...)
	}
	return path, true
}

// reverse reverses hops in place.
func reverse(hops []Hop) {
	for i, j := 0, len(hops)-1; i < j; i, j = i+1, j-1 {
		hops[i], hops[j] = hops[j], hops[i]
	}
}

// exemptionOf is the first exemption of the policy that seeds the symbol.
func (pr *provenance) exemptionOf(id inventory.SymbolID) *exempt.Exemption {
	for i := range pr.p.Exempt {
		if e := &pr.p.Exempt[i]; e.ID == id && e.Holder == "" {
			return e
		}
	}
	return nil
}

// consumerHop is the reference of a loaded consumer that made the symbol at at a
// seed: its first from a production file, else its first from a test file, which
// a seed has only where the mode counts it.
func (pr *provenance) consumerHop(at int) Hop {
	c := pr.g.consumed[at]
	first := c.production
	if first == 0 {
		first = c.test
	}
	r := &pr.g.refs[first-1]
	return Hop{Reference: r, From: r.From, To: r.To}
}
