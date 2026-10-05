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
	// ReferenceCounting holds a symbol live when any declaration of the loaded
	// graph references it, live or not. It is not recursive.
	ReferenceCounting Relation = iota

	// Reachability holds a symbol live when a path of references reaches it from
	// the root seed, so a symbol only unreachable symbols reference is dead here.
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

// Exemption is one symbol an exemption class holds back, and why. Class is the
// vocabulary's spelling of the class. Site is the evidence's position, rendered as
// every graph position is: target-relative, solidus-separated, UTF-16 columns. A
// class whose evidence is a type relation rather than a source site leaves Site
// and Detail empty.
type Exemption struct {
	ID     SymbolID
	Class  string
	Detail string         // one short clause naming the evidence
	Site   token.Position // last, so the counted fields of a position end the value
}

// Mode is the run's reference mode: which references a sweep counts, and how a
// reference from a loaded consumer's test file is classified. The composition root
// decides it once per run and hands it to every stage that reads it, so no two
// stages of one run answer about different reference sets.
type Mode struct {
	// Production drops the references test files made and leaves the test roots
	// out of the reachability seed; a test root itself stays live. A mark or an
	// exemption still seeds a closure over every reference ([sweep.walk]), but the
	// symbol a retained test declaration names directly counts production
	// references alone.
	Production bool

	// ConsumerTestsProduction classifies a reference from a loaded consumer's test
	// file as production rather than test. It is a classification, not a filter: it
	// decides the split a candidate reports as well as what a production sweep counts.
	ConsumerTestsProduction bool
}

// SweepInput is what one sweep runs over besides the graph: the symbols a
// mechanism outside the reference graph holds live, and the mode.
type SweepInput struct {
	// Marked are the symbols a matched suppression names: each is live under both
	// relations and seeds reachability.
	Marked []SymbolID

	// Exempt are the exemptions the classes computed, one per symbol and class. An
	// exempt symbol is never a candidate and seeds reachability; every exemption of
	// a symbol stands, so an explanation names each class that held it.
	Exempt []Exemption

	Mode Mode
}

// Candidate is one dead symbol, the relation that found it, and the reference
// counts a later kind assignment reads.
type Candidate struct {
	ID SymbolID

	// ProductionRefs and TestRefs count every reference to the symbol, split as the
	// mode classifies them, whatever the mode counts: a kind reads both to tell a
	// symbol only tests reference from one nothing references.
	ProductionRefs int
	TestRefs       int

	// Configs is the set of build configurations the candidate is dead in: every
	// configuration it exists in over a matrix, empty over one configuration's graph.
	Configs ConfigSet

	Relation Relation

	// TestOfDeadCode reports a test declaration [sweep.admitTestsOfDeadCode]
	// admitted rather than a relation.
	TestOfDeadCode bool
}

// Result is one sweep's answer.
type Result struct {
	// LiveUnder holds, per symbol, the relations that hold it live; a symbol with
	// no entry is live under none.
	LiveUnder map[SymbolID]RelationSet

	// Candidates are the dead symbols in the order the inventory holds them,
	// each carrying the relation that found it.
	Candidates []Candidate

	// Components are the dead components, roots first: each precedes every
	// component it reaches.
	Components []Component

	// Retained are the exemptions that held back a symbol this sweep would
	// otherwise report, in inventory order and then input order. An exemption on a
	// symbol live anyway, or naming no inventory symbol, is not here.
	Retained []Exemption

	// Suppressed are the marks that held back a symbol, in inventory order and each
	// once; a mark on a symbol live anyway, exempt, or outside the inventory is
	// stale and not here. Over a matrix it is the union of the configurations.
	Suppressed []SymbolID
}

// withoutExemptions is the input with the exemptions withdrawn, which the sweep
// that answers which exemptions took effect runs under.
func (in SweepInput) withoutExemptions() SweepInput {
	in.Exempt = nil
	return in
}

// withoutMarks is the input with the marks withdrawn, which the sweep that answers
// which marks took effect runs under.
func (in SweepInput) withoutMarks() SweepInput {
	in.Marked = nil
	return in
}

// Sweep answers which symbols of one graph are dead under the input's mode, which
// relation found each, which dead component each belongs to, and which exemptions
// and marks held a symbol back. Which exemptions took effect, and which marks, is
// answered by one further sweep each with only that set withdrawn: withdrawing both
// at once would credit an exemption with a symbol a mark held back. The
// candidates, components and relations are the first sweep's.
func (g *Graph) Sweep(in SweepInput) Result {
	r := g.sweep(in).result()
	if len(in.Exempt) > 0 {
		r.Retained = g.sweep(in.withoutExemptions()).retained(in.Exempt)
	}
	if len(in.Marked) > 0 {
		r.Suppressed = g.sweep(in.withoutMarks()).suppressed(in.Marked)
	}
	return r
}

// sweep runs every pass of one input over the graph, in the order the answers
// depend on.
func (g *Graph) sweep(in SweepInput) *sweep {
	s := &sweep{
		g:              g,
		in:             in,
		marked:         g.positions(in.Marked),
		exempt:         g.exempted(in.Exempt),
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

// exempted returns one flag per symbol an exemption names; which classes held it
// is [sweep.retained]'s answer.
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
	in             SweepInput
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

// suppressed lists the marks that name a symbol this sweep, run without them,
// judged a candidate, in inventory order and each once. Any other mark held
// nothing back: its symbol is outside the inventory, live, or exempt.
func (s *sweep) suppressed(marked []SymbolID) []SymbolID {
	held := make(map[SymbolID]bool, len(marked))
	for _, id := range marked {
		if at := s.g.at(id); at != outside && s.dead[at] {
			held[id] = true
		}
	}
	if len(held) == 0 {
		return nil
	}
	found := make([]SymbolID, 0, len(held))
	for i := range s.g.symbols {
		if id := s.g.symbols[i].ID; held[id] {
			found = append(found, id)
			delete(held, id)
		}
	}
	return found
}

// callers keeps every symbol an actual caller reaches, live under both relations:
// a root of every kind but the published API, which only hypothesises a caller,
// and a symbol a loaded consumer references, a caller the analysis saw in the
// consumer's source. A published symbol is still a reachability seed, so it is a
// candidate only when nothing in the loaded graph references it.
func (s *sweep) callers() {
	for _, r := range s.g.rooted {
		if r.kind != RootPublishedAPI {
			s.called[r.at] = true
		}
	}
	for i := range s.g.symbols {
		if s.g.consumedIn(i, s.in.Mode) {
			s.called[i] = true
		}
	}
}

// referenceCounting holds a symbol live when the mode's reference set holds one
// reference to it, and holds every mark and every caller live before it counts.
func (s *sweep) referenceCounting() {
	for i := range s.g.symbols {
		if s.marked[i] || s.called[i] || s.g.references(i, s.in.Mode) > 0 {
			s.live[i] = s.live[i].with(ReferenceCounting)
		}
	}
}

// reachability holds live every caller and every symbol a seed reaches. The held
// seed reaches through every reference, the caller seed through those the mode
// counts; the held seed runs first, so a symbol its closure reached is already
// expanded under the wider rule when the callers reach it.
func (s *sweep) reachability() {
	reached := make([]bool, len(s.g.symbols))
	s.walk(reached, s.heldSeed(reached), true)
	s.walk(reached, s.callerSeed(reached), false)
	for i := range s.g.symbols {
		if reached[i] || s.called[i] {
			s.live[i] = s.live[i].with(Reachability)
		}
	}
}

// heldSeed is every mark and every exemption, test declarations included: a
// mechanism the analysis cannot see holds each, so every reference it makes is a
// use that mechanism makes.
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

// callerSeed is every root and every symbol a loaded consumer references, except
// the test roots under a production sweep. Which consumer references count is the
// mode's.
func (s *sweep) callerSeed(reached []bool) []int {
	queue := make([]int, 0, len(s.g.rooted))
	for _, r := range s.g.rooted {
		if (s.in.Mode.Production && r.kind == RootTest) || reached[r.at] {
			continue
		}
		reached[r.at] = true
		queue = append(queue, r.at)
	}
	for i := range s.g.symbols {
		if reached[i] || !s.g.consumedIn(i, s.in.Mode) {
			continue
		}
		reached[i] = true
		queue = append(queue, i)
	}
	return queue
}

// walk follows the references out of a seed's closure. Outside the held seed's
// closure a production sweep skips the references a test file made.
func (s *sweep) walk(reached []bool, queue []int, held bool) {
	for len(queue) > 0 {
		at := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, e := range s.g.out[at] {
			if reached[e.to] || (!held && s.in.Mode.Production && e.test) {
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

// admitTestsOfDeadCode adds every test declaration whose referenced production
// declarations are non-empty and all candidates. It runs after the relations
// because a test is a root both hold live, and it reads the references the test
// makes rather than those the mode counts: a production sweep counts none, so the
// mode's set would leave the rule unable to fire in the only mode where it can.
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
		if s.g.references(i, s.in.Mode) == 0 {
			relation = ReferenceCounting
		}
		production, test := s.g.counted(i, s.in.Mode)
		r.Candidates = append(r.Candidates, Candidate{
			ID:             s.g.symbols[i].ID,
			ProductionRefs: production,
			TestRefs:       test,
			Relation:       relation,
			TestOfDeadCode: s.testOfDeadCode[i],
		})
	}
	r.Components = s.g.componentsOf(s.dead, s.testOfDeadCode)
	return r
}

// TestEvidence counts, per symbol, the exemptions whose evidence a test file
// carries, which a production run does not hold: each is a test reference to its
// symbol, as a reference from a test file is, and holds nothing live.
type TestEvidence map[SymbolID]int

// TestReferencesOf counts, per symbol, the distinct facts test evidence states
// about it: one per class and detail, however many configurations or sites found
// it.
func TestReferencesOf(evidence []Exemption) TestEvidence {
	type fact struct {
		id     SymbolID
		class  string
		detail string
	}
	seen := make(map[fact]bool, len(evidence))
	counts := make(map[SymbolID]int, len(evidence))
	for _, e := range evidence {
		if f := (fact{id: e.ID, class: e.Class, detail: e.Detail}); !seen[f] {
			seen[f] = true
			counts[e.ID]++
		}
	}
	return counts
}
