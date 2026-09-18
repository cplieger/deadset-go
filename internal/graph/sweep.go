package graph

import (
	"go/token"
	"strconv"
)

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

// Exemption is one symbol an exemption class holds back, and why.
//
// Class is the spelling the vocabulary gives the class. Site is where the
// evidence was found, rendered the way every other position of the graph is:
// target-relative with the solidus as separator, and a column counting UTF-16
// code units. A class whose evidence is a type relation rather than a source
// site leaves Site and Detail empty; a class whose evidence is a text match
// records both, because they are what a maintainer goes and looks at.
type Exemption struct {
	ID     SymbolID
	Class  string
	Detail string         // one short clause naming the evidence
	Site   token.Position // last, so the counted fields of a position end the value
}

// Mode is what one sweep counts.
type Mode struct {
	// Marked are the symbols a matched suppression names. A mark makes its
	// symbol live under both relations before either runs, and seeds
	// reachability, so nothing the marked symbol alone references cascades into
	// a candidate.
	Marked []SymbolID

	// Exempt are the exemptions an exemption class computed, one per symbol and
	// class. An exempt symbol is never a candidate, whatever either relation
	// says, and each seeds reachability for the same reason a mark does: the
	// symbol is live by a mechanism the analysis cannot see, so what it
	// references is live too. Two exemptions of one symbol both stand, so an
	// explanation names every class that held it.
	Exempt []Exemption

	// Production drops every reference a test file made from the reference count
	// and leaves the test roots out of the reachability seed. A test root stays
	// live all the same, so a test function is never a candidate of a production
	// sweep and nothing it alone reaches is live.
	//
	// A mark and an exemption are the exception, and they are the only one: each
	// seeds a production sweep whatever file declares the symbol, and the closure
	// from that seed follows every reference it finds. A mechanism the analysis
	// cannot see is what holds the symbol live, so the references it makes are
	// uses that mechanism makes, and a symbol below a retained test declaration
	// is not dead code. The symbol the retained declaration references directly
	// is still counted on its production references alone, which is what leaves a
	// symbol only tests reference reported whatever retains the test.
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

	// Configs is the set of build configurations the candidate is dead in, which
	// over a matrix is every configuration it exists in and over one
	// configuration's graph is empty, because such a graph is not keyed by a
	// matrix. It is what a finding names the configurations it holds under from.
	Configs ConfigSet

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

	// Retained are the exemptions that held back a symbol this sweep would
	// otherwise have reported, in the order the inventory holds the symbols they
	// name, and in the order Mode.Exempt gave them for one symbol. An exemption
	// on a symbol some relation holds live anyway is not here, because nothing
	// was held back; it is still in Mode.Exempt, which is what an explanation of
	// one symbol reads. An exemption naming no symbol of the inventory is here
	// under no circumstances.
	Retained []Exemption
}

// Sweep answers which symbols of one graph are dead under one mode, which
// relation found each, which dead component each belongs to, and which
// exemptions held a symbol back.
//
// The order of the work is the order the answers depend on: the marks and the
// called roots are live before either relation runs, reference counting and
// reachability are then computed over the whole graph, the candidate set is the
// symbols at least one relation does not hold live, the tests of dead code join
// that set, and the components are computed over the set that results.
//
// Which exemptions took effect is decided by sweeping twice, because the sweep is
// what makes them take effect: the second sweep drops the exemptions from both
// the seed and the candidate removal, and its candidate set is what the run would
// have reported without them. A mode carrying no exemption sweeps once.
func (g *Graph) Sweep(m Mode) Result {
	r := g.sweep(m).result()
	if len(m.Exempt) == 0 {
		return r
	}
	r.Retained = g.sweep(Mode{Marked: m.Marked, Production: m.Production}).retained(m.Exempt)
	return r
}

// sweep runs every pass of one mode over the graph, in the order the answers
// depend on.
func (g *Graph) sweep(m Mode) *sweep {
	s := &sweep{
		g:              g,
		mode:           m,
		marked:         g.positions(m.Marked),
		exempt:         g.exempted(m.Exempt),
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
	return s
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

// exempted returns one flag per symbol, set for each symbol an exemption names.
// Two exemptions of one symbol set one flag: the sweep reads whether a symbol is
// held back, and which classes held it is the retained record's answer.
func (g *Graph) exempted(exempt []Exemption) []bool {
	ids := make([]SymbolID, 0, len(exempt))
	for _, e := range exempt {
		ids = append(ids, e.ID)
	}
	return g.positions(ids)
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

// retained lists the exemptions that name a symbol this sweep judged a candidate,
// which is what a sweep without them answers, in the order the inventory holds
// those symbols.
func (s *sweep) retained(exempt []Exemption) []Exemption {
	held := make(map[SymbolID][]Exemption)
	for _, e := range exempt {
		if at := s.g.at(e.ID); at != outside && s.dead[at] {
			held[e.ID] = append(held[e.ID], e)
		}
	}
	if len(held) == 0 {
		return nil
	}
	found := make([]Exemption, 0, len(exempt))
	for i := range s.g.symbols {
		found = append(found, held[s.g.symbols[i].ID]...)
	}
	return found
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

// reachability holds a symbol live when a seed reaches it, and holds every caller
// live whether or not a seed reaches it.
//
// There are two seeds because they reach through different reference sets. A
// symbol a mark or an exemption holds is live by a mechanism the analysis cannot
// see, so every reference it makes is one that mechanism makes and no mode
// withholds it; a root reaches through the references the mode counts. The held
// seed runs first, which is what makes the wider rule transitive: a symbol its
// closure reached is already expanded under that rule by the time the roots reach
// it.
func (s *sweep) reachability() {
	reached := make([]bool, len(s.g.symbols))
	s.walk(reached, s.heldSeed(reached), true)
	s.walk(reached, s.rootSeed(reached), false)
	for i := range s.g.symbols {
		if reached[i] || s.called[i] {
			s.live[i] = s.live[i].with(Reachability)
		}
	}
}

// heldSeed is every mark and every exemption, because a symbol either holds is
// live by a mechanism the analysis cannot see and keeps what it references alive.
// A production sweep withdraws neither, so an exempt test declaration seeds one as
// well.
func (s *sweep) heldSeed(reached []bool) []int {
	queue := make([]int, 0, len(s.g.symbols))
	for i := range s.g.symbols {
		if s.marked[i] || s.exempt[i] {
			reached[i] = true
			queue = append(queue, i)
		}
	}
	return queue
}

// rootSeed is every root, except that a production sweep leaves the test roots
// out. A test function is run by the test binary and is not a candidate, and
// nothing it alone reaches is live.
func (s *sweep) rootSeed(reached []bool) []int {
	queue := make([]int, 0, len(s.g.rooted))
	for _, r := range s.g.rooted {
		if (s.mode.Production && r.kind == RootTest) || reached[r.at] {
			continue
		}
		reached[r.at] = true
		queue = append(queue, r.at)
	}
	return queue
}

// walk follows the references out of every symbol of one seed, and out of every
// symbol those reach. A production sweep drops the references a test file made,
// except out of the held seed's closure, where every reference is a use the
// holding mechanism makes.
func (s *sweep) walk(reached []bool, queue []int, held bool) {
	for len(queue) > 0 {
		at := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, e := range s.g.out[at] {
			if reached[e.to] || (!held && s.mode.Production && e.test) {
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
	r.Components = s.g.componentsOf(s.dead, s.testOfDeadCode)
	return r
}
