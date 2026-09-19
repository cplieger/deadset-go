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

// Mode is the run's reference mode: which of the references the graph holds a
// sweep counts, and how a reference a loaded consumer's test file made is
// classified.
//
// It is one value: the composition root decides it once per run and hands it to
// every stage that reads the mode, the exemption classes and the kinds included, so
// that no stage of a run derives the mode again and no two stages of one run can
// answer about different reference sets.
type Mode struct {
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

	// ConsumerTestsProduction classifies a reference from a loaded consumer's test
	// file as a production reference rather than as the test reference it is by
	// default. It is a classification and not a filter, so it decides the split a
	// candidate reports as well as what a production sweep counts: a symbol only a
	// consumer's tests reach is test-only use by default and referenced code under
	// this mode.
	ConsumerTestsProduction bool
}

// SweepInput is what one sweep runs over besides the graph: the symbols a
// mechanism outside the reference graph holds live, and the mode.
type SweepInput struct {
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

	// Mode is the run's reference mode, which the composition root decided once
	// for every stage of the run.
	Mode Mode
}

// Candidate is one dead symbol, the relation that found it, and the reference
// counts a later kind assignment reads.
type Candidate struct {
	ID SymbolID

	// ProductionRefs and TestRefs count every reference made to the symbol,
	// whatever the mode counts, split the way the mode classifies them: a
	// production sweep decides liveness on ProductionRefs alone and a kind reads
	// both, so a symbol only tests reference is distinguishable from one nothing
	// references. A reference a loaded consumer's test file made is a test
	// reference, and a production one where the mode classifies it so.
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
	// name, and in the order SweepInput.Exempt gave them for one symbol. An
	// exemption on a symbol some relation holds live anyway is not here, because
	// nothing was held back; it is still in SweepInput.Exempt, which is what an
	// explanation of one symbol reads. An exemption naming no symbol of the
	// inventory is here under no circumstances.
	Retained []Exemption

	// Suppressed lists the marks that held a symbol back: the sweep runs once
	// more with the same exemptions and no mark, and every mark whose symbol
	// that pass judged a candidate is here, in the order the inventory holds
	// those symbols, each once. A mark on a symbol live anyway, and a mark on
	// one an exemption retains, is not here: that mark is in effect for nothing,
	// which is what a stale suppression is. A mark naming no symbol of the
	// inventory held nothing back either, so it is not here.
	//
	// Over a matrix the answer is the union of the configurations, for the
	// reason the Retained record is one per symbol and class whatever the number
	// of configurations: a suppression that is needed anywhere is not stale.
	Suppressed []SymbolID
}

// withoutExemptions is the input with the exemptions withdrawn and everything else
// as the run gave it, which is what the sweep that answers which exemptions took
// effect runs under.
func (in SweepInput) withoutExemptions() SweepInput {
	in.Exempt = nil
	return in
}

// withoutMarks is the input with the marks withdrawn and everything else as the run
// gave it, which is what the sweep that answers which marks took effect runs
// under.
func (in SweepInput) withoutMarks() SweepInput {
	in.Marked = nil
	return in
}

// Sweep answers which symbols of one graph are dead under the input's mode, which
// relation found each, which dead component each belongs to, and which
// exemptions held a symbol back.
//
// The order of the work is the order the answers depend on: the callers are live
// before either relation runs, reference counting and reachability are then
// computed over the whole graph, the candidate set is the symbols at least one
// relation does not hold live, the tests of dead code join that set, and the
// components are computed over the set that results.
//
// A caller is a root of every kind but the published API, and a symbol a loaded
// consumer references. The two are one rule read twice: a root of an actual caller
// and a consumer's call each name a use the target's own source does not hold, so
// each holds its symbol live under both relations and seeds the closure from it.
//
// Which exemptions took effect, and which marks did, is decided by sweeping
// again, because the sweep is what makes each take effect: a further sweep drops
// the one under question and keeps everything else the input carries, and its
// candidate set is what the run would have reported without it. An input carrying
// no exemption and no mark sweeps once.
//
// The two questions are symmetric and are answered by two passes rather than one,
// because withdrawing both at once would credit an exemption with holding back a
// symbol a mark held back and the other way about. Neither pass is the run's
// answer: the candidates, the components and the relations are the first sweep's.
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

// suppressed lists the marks that name a symbol this sweep judged a candidate,
// which is what a sweep without them answers, in the order the inventory holds
// those symbols and each once.
//
// A mark the inventory holds no symbol for names nothing to hold back, and a mark
// on a symbol this sweep did not judge a candidate held nothing back: the symbol
// is live by a relation, or an exemption this sweep still carries retains it. Both
// are marks in effect for nothing.
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

// callers keeps every symbol an actual caller reaches, which is of two kinds.
//
// A root that names a caller the analysis cannot see in the source: the runtime,
// the test binary, the linker, C code, a blank declaration's initializer, or the
// maintainer's assertion of one in the configuration. The published API of a
// library is the one root kind that only hypothesises a caller, so it is not a
// caller here: its closure is live under reachability while the exported symbol
// itself is a candidate when nothing in the loaded graph references it.
//
// And a symbol a loaded consumer references. That caller the analysis did see, in
// the consumer's own source, which is the whole point of loading the consumer: a
// declared consumer is a module whose references count, so a symbol it uses is live
// under both relations exactly as a root of an actual caller is, and what that
// symbol references is live with it.
//
// Each is live under both relations.
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

// reachability holds a symbol live when a seed reaches it, and holds every caller
// live whether or not a seed reaches it.
//
// There are two seeds because they reach through different reference sets. A
// symbol a mark or an exemption holds is live by a mechanism the analysis cannot
// see, so every reference it makes is one that mechanism makes and no mode
// withholds it; a root and a consumer's call reach through the references the mode
// counts. The held seed runs first, which is what makes the wider rule transitive:
// a symbol its closure reached is already expanded under that rule by the time the
// callers reach it.
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

// callerSeed is every root and every symbol a loaded consumer references, except
// that a production sweep leaves the test roots out. A test function is run by the
// test binary and is not a candidate, and nothing it alone reaches is live.
//
// A consumer's reference seeds the closure because the consumer is a caller: what
// the symbol it calls references is reached through that call. Which of a
// consumer's references count is the mode's, so a production sweep seeds from a
// consumer's test file only where the mode classifies such a reference as a
// production one.
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

// walk follows the references out of every symbol of one seed, and out of every
// symbol those reach. A production sweep drops the references a test file made,
// except out of the held seed's closure, where every reference is a use the
// holding mechanism makes.
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
