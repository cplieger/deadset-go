package graph

import "strconv"

// Relation names the liveness relation that decided a symbol. String returns the
// spelling a report carries.
type Relation uint8

// The two relations, which are different sets over one graph.
const (
	// ReferenceCounting holds a symbol live when one reference from any
	// declaration of the loaded graph names it, whatever the liveness of the
	// declaration that made the reference. It is not recursive.
	ReferenceCounting Relation = iota

	// Reachability holds a symbol live when a path of references reaches it from
	// the root seed, so a symbol only unreachable symbols reference is dead
	// under this relation and live under the other.
	Reachability
)

var relationNames = [...]string{
	ReferenceCounting: "reference-counting",
	Reachability:      "reachability",
}

// String returns the relation's spelling, and a numbered form for a value outside
// the set so a message never loses the number it was given.
func (r Relation) String() string {
	if int(r) >= len(relationNames) {
		return "Relation(" + strconv.Itoa(int(r)) + ")"
	}
	return relationNames[r]
}

// RelationSet is a set of relations, one bit per Relation.
type RelationSet uint8

// Has reports whether the set holds r.
func (s RelationSet) Has(r Relation) bool { return s&(1<<r) != 0 }

// with returns the set holding r as well.
func (s RelationSet) with(r Relation) RelationSet { return s | 1<<r }

// Mode is what one sweep counts.
type Mode struct {
	// Marked are the symbols a matched suppression names. A mark makes its
	// symbol live under both relations before either runs, and seeds
	// reachability, so nothing the marked symbol alone references cascades into
	// a candidate.
	Marked []SymbolID

	// Exempt are the symbols an exemption class retains. They are never
	// candidates, whatever either relation says, and each seeds reachability for
	// the same reason a mark does: the symbol is live by a mechanism the analysis
	// cannot see, so what it references is live too.
	Exempt []SymbolID

	// Production drops every reference a test file made from both relations and
	// leaves the test roots out of the reachability seed. A test root stays live
	// all the same, so a test function is never a candidate of a production
	// sweep and nothing it alone reaches is live.
	Production bool
}

// Candidate is one dead symbol, the relation that found it, and the reference
// counts a later kind assignment reads.
type Candidate struct {
	ID SymbolID

	// ProductionRefs and TestRefs count every reference made to the symbol,
	// whatever the mode: a production sweep decides liveness on ProductionRefs
	// alone and a kind reads both, so a symbol only tests reference is
	// distinguishable from one nothing references.
	ProductionRefs int
	TestRefs       int

	Relation Relation

	// TestOfDeadCode reports a test declaration admitted by the rule rather than
	// by a relation: the set of production declarations it references is
	// non-empty and every member of that set is a candidate.
	TestOfDeadCode bool
}

// Result is one sweep's answer.
type Result struct {
	// LiveUnder holds, per symbol, the relations that hold the symbol live, so
	// an explanation answers why a symbol is live or dead without a second
	// sweep. A symbol no relation holds live has no entry, which a lookup
	// answers as the empty set.
	LiveUnder map[SymbolID]RelationSet

	// Candidates are the dead symbols in the order the inventory holds them,
	// each carrying the relation that found it.
	Candidates []Candidate

	// Components are the dead components, roots first: each precedes every
	// component it reaches.
	Components []Component
}

// Sweep answers which symbols of one graph are dead under one mode, which
// relation found each, and which dead component each belongs to.
//
// The order of the work is the order the answers depend on: the marks and the
// called roots are live before either relation runs, reference counting and
// reachability are then computed over the whole graph, the candidate set is the
// symbols at least one relation does not hold live, the tests of dead code join
// that set, and the components are computed over the set that results.
func (g *Graph) Sweep(m Mode) Result {
	s := &sweep{
		g:              g,
		mode:           m,
		marked:         g.positions(m.Marked),
		exempt:         g.positions(m.Exempt),
		called:         make([]bool, len(g.symbols)),
		live:           make([]RelationSet, len(g.symbols)),
		dead:           make([]bool, len(g.symbols)),
		testOfDeadCode: make([]bool, len(g.symbols)),
	}
	s.callers()
	s.referenceCounting()
	s.reachability()
	s.decide()
	s.admitTestsOfDeadCode()
	return s.result()
}

// positions returns one flag per symbol, set for each symbol the identifiers
// name. An identifier the inventory does not hold names none.
func (g *Graph) positions(ids []SymbolID) []bool {
	flags := make([]bool, len(g.symbols))
	for _, id := range ids {
		if at := g.at(id); at != outside {
			flags[at] = true
		}
	}
	return flags
}

// sweep is one sweep's state over one graph.
type sweep struct {
	g              *Graph
	marked         []bool
	exempt         []bool
	called         []bool
	live           []RelationSet
	dead           []bool
	testOfDeadCode []bool
	mode           Mode
}

// callers keeps every root that names a caller the analysis cannot see in the
// source: the runtime, the test binary, the linker, C code, a blank declaration's
// initializer, or the maintainer's assertion of one in the configuration. Each is
// live under both relations.
//
// The published API of a library is the one root kind that only hypothesises a
// caller, so it is not a caller here: its closure is live under reachability
// while the exported symbol itself is a candidate when nothing in the loaded
// graph references it.
func (s *sweep) callers() {
	for _, r := range s.g.rooted {
		if r.kind != RootPublishedAPI {
			s.called[r.at] = true
		}
	}
}

// referenceCounting holds a symbol live when the mode's reference set holds one
// reference to it, and holds every mark and every caller live before it counts.
func (s *sweep) referenceCounting() {
	for i := range s.g.symbols {
		if s.marked[i] || s.called[i] || s.g.references(i, s.mode.Production) > 0 {
			s.live[i] = s.live[i].with(ReferenceCounting)
		}
	}
}

// reachability holds a symbol live when the seed reaches it through the
// references the mode counts, and holds every caller live whether or not the seed
// reaches it.
func (s *sweep) reachability() {
	reached, queue := s.seed()
	s.walk(reached, queue)
	for i := range s.g.symbols {
		if reached[i] || s.called[i] {
			s.live[i] = s.live[i].with(Reachability)
		}
	}
}

// seed is where the closure starts: every mark and every exemption, because a
// symbol either retains is live by a mechanism the analysis cannot see and keeps
// what it references alive, and every root, except that a production sweep leaves
// the test roots out. A test function is run by the test binary and is not a
// candidate, and nothing it alone reaches is live.
func (s *sweep) seed() (reached []bool, queue []int) {
	reached = make([]bool, len(s.g.symbols))
	queue = make([]int, 0, len(s.g.symbols))
	for i := range s.g.symbols {
		if s.marked[i] || s.exempt[i] {
			reached[i] = true
			queue = append(queue, i)
		}
	}
	for _, r := range s.g.rooted {
		if (s.mode.Production && r.kind == RootTest) || reached[r.at] {
			continue
		}
		reached[r.at] = true
		queue = append(queue, r.at)
	}
	return reached, queue
}

// walk follows the references the mode counts out of every symbol the seed
// reached, and out of every symbol those reach.
func (s *sweep) walk(reached []bool, queue []int) {
	for len(queue) > 0 {
		at := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, e := range s.g.out[at] {
			if (s.mode.Production && e.test) || reached[e.to] {
				continue
			}
			reached[e.to] = true
			queue = append(queue, e.to)
		}
	}
}

// decide keeps as a candidate every symbol the sweep judges that at least one
// relation does not hold live, and that no exemption retains.
func (s *sweep) decide() {
	for i := range s.g.symbols {
		if !s.g.subject[i] || s.exempt[i] {
			continue
		}
		if s.live[i].Has(ReferenceCounting) && s.live[i].Has(Reachability) {
			continue
		}
		s.dead[i] = true
	}
}

// admitTestsOfDeadCode adds to the candidate set every test declaration whose
// referenced set of production declarations is non-empty and wholly dead.
//
// The rule is the whole mechanism, and it is applied after the relations rather
// than through them: a test declaration is a root, so both relations hold it
// live, and its subject never references it, so no cycle exists for the component
// pass to find. A test that references one live production declaration is never
// admitted, whatever the number of dead ones it also references.
//
// The set is read from the references the test makes rather than from the ones
// the mode counts. A production sweep counts none of them, which is what makes
// the targets dead in the first place, so reading the mode's set here would leave
// the rule unable to fire in the only mode where it can.
func (s *sweep) admitTestsOfDeadCode() {
	for i := range s.g.symbols {
		if !s.g.test[i] || !s.g.subject[i] || s.exempt[i] || s.marked[i] {
			continue
		}
		if targets, live := s.targetsOf(i); targets > 0 && live == 0 {
			s.dead[i] = true
			s.testOfDeadCode[i] = true
		}
	}
}

// targetsOf counts the production declarations one test declaration references,
// and how many of them are not candidates.
func (s *sweep) targetsOf(at int) (targets, live int) {
	for _, e := range s.g.out[at] {
		if s.g.test[e.to] || !s.g.subject[e.to] {
			continue
		}
		targets++
		if !s.dead[e.to] {
			live++
		}
	}
	return targets, live
}

// result collects the candidates in the order the inventory holds the symbols,
// whichever step admitted each, and the components over the set that results.
func (s *sweep) result() Result {
	r := Result{LiveUnder: make(map[SymbolID]RelationSet)}
	for i := range s.g.symbols {
		if set := s.live[i]; set != 0 {
			r.LiveUnder[s.g.symbols[i].ID] = set
		}
		if !s.dead[i] {
			continue
		}
		relation := Reachability
		if s.g.references(i, s.mode.Production) == 0 {
			relation = ReferenceCounting
		}
		r.Candidates = append(r.Candidates, Candidate{
			ID:             s.g.symbols[i].ID,
			ProductionRefs: s.g.made[i].production,
			TestRefs:       s.g.made[i].test,
			Relation:       relation,
			TestOfDeadCode: s.testOfDeadCode[i],
		})
	}
	r.Components = s.components()
	return r
}
