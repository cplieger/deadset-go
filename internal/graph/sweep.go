package graph

import (
	"go/token"
	"slices"
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
	Detail string // one short clause naming the evidence

	// Holder is the declaration whose code holds the evidence, where the class
	// retains as a use by that declaration: the symbol is then live only while
	// the holder is, and falls into the holder's dead component with it. A root
	// the exemption names is held by the holder instead. Empty, the exemption
	// holds the symbol live whatever else is.
	Holder SymbolID

	// Via is the interface method a retained method answers, where the class
	// retains through an interface the inventory declares.
	Via SymbolID

	Site token.Position // last, so the counted fields of a position end the value
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
	// tested is the test evidence [Matrix.Sweep] holds: each symbol it counts
	// seeds the reach that admits unreferenced test-file declarations, as an
	// exemption does.
	tested TestEvidence

	// Marked are the symbols a matched suppression names: each is live under both
	// relations and seeds reachability.
	Marked []SymbolID

	// Exempt are the exemptions the classes computed, one per symbol and class. An
	// exempt symbol is never a candidate and seeds reachability; every exemption of
	// a symbol stands, so an explanation names each class that held it.
	Exempt []Exemption

	// Uses are references the analysis infers rather than reads, each a use of
	// To by From, which every sweep counts as a reference From makes.
	Uses []Use

	Mode Mode
}

// Use is one inferred reference: From uses To.
type Use struct {
	From SymbolID
	To   SymbolID
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

	// UnreferencedTest reports a declaration of a test file that no root, and no
	// test of dead code, reaches through any reference, test files' included
	// ([sweep.admitUnreferencedTests]). Its relation counts every reference.
	UnreferencedTest bool
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
func (in *SweepInput) withoutExemptions() *SweepInput {
	out := *in
	out.Exempt = nil
	return &out
}

// withoutMarks is the input with the marks withdrawn, which the sweep that answers
// which marks took effect runs under.
func (in *SweepInput) withoutMarks() *SweepInput {
	out := *in
	out.Marked = nil
	return &out
}

// Sweep answers which symbols of one graph are dead under the input's mode, which
// relation found each, which dead component each belongs to, and which exemptions
// and marks held a symbol back. Which exemptions took effect, and which marks, is
// answered by one further sweep each with only that set withdrawn: withdrawing both
// at once would credit an exemption with a symbol a mark held back. The
// candidates, components and relations are the first sweep's.
func (g *Graph) Sweep(in *SweepInput) Result {
	swept := g.sweep(in)
	r := swept.result()
	if len(in.Exempt) > 0 {
		r.Retained = g.sweep(in.withoutExemptions()).retained(in.Exempt, swept)
	}
	if len(in.Marked) > 0 {
		r.Suppressed = g.sweep(in.withoutMarks()).suppressed(in.Marked)
	}
	return r
}

// sweep runs every pass of one input over the graph, in the order the answers
// depend on.
func (g *Graph) sweep(in *SweepInput) *sweep {
	g = g.using(in)
	s := &sweep{
		g:                g,
		in:               in,
		marked:           g.positions(in.Marked),
		exempt:           g.exempted(in.Exempt),
		called:           make([]bool, len(g.symbols)),
		live:             make([]RelationSet, len(g.symbols)),
		dead:             make([]bool, len(g.symbols)),
		testOfDeadCode:   make([]bool, len(g.symbols)),
		unreferencedTest: make([]bool, len(g.symbols)),
	}
	s.callers()
	s.referenceCounting()
	s.reachability()
	s.decide()
	s.admitTestsOfDeadCode()
	s.admitUnreferencedTests()
	return s
}

// using is the graph with the input's inferred references and the uses its held
// exemptions state added, and with every blank root such an exemption names left to
// its holder. The graph itself is unchanged, so one graph answers any number of
// inputs.
func (g *Graph) using(in *SweepInput) *Graph {
	uses := slices.Clone(in.Uses)
	held := make(map[int]bool)
	for _, e := range in.Exempt {
		if e.Holder != "" {
			uses = append(uses, Use{From: e.Holder, To: e.ID})
			if at := g.at(e.ID); at != outside {
				held[at] = true
			}
		}
	}
	if len(uses) == 0 {
		return g
	}
	derived := *g
	derived.out = make([][]edge, len(g.out))
	for i := range g.out {
		derived.out[i] = slices.Clip(g.out[i])
	}
	derived.made = slices.Clone(g.made)
	derived.rooted = slices.DeleteFunc(slices.Clone(g.rooted), func(r rooted) bool {
		return r.kind == RootBlank && held[r.at]
	})
	for _, use := range uses {
		from := g.at(use.From)
		if from == outside {
			continue
		}
		_, test := IsTestFile(g.symbols[from].Pos.Filename)
		derived.add(&Reference{From: use.From, To: use.To, Test: test})
	}
	return &derived
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
		if e.Holder == "" {
			ids = append(ids, e.ID)
		}
	}
	return g.positions(ids)
}

// sweep is one sweep's state over one graph.
type sweep struct {
	in               *SweepInput
	g                *Graph
	marked           []bool
	exempt           []bool
	called           []bool
	live             []RelationSet
	dead             []bool
	testOfDeadCode   []bool
	unreferencedTest []bool
}

// retained lists the exemptions that name a symbol this sweep judged a candidate,
// which is what a sweep without them answers, and the sweep with them did not, in
// the order the inventory holds those symbols. An exemption whose holder is dead
// held nothing back.
func (s *sweep) retained(exempt []Exemption, with *sweep) []Exemption {
	held := make(map[SymbolID][]Exemption)
	for _, e := range exempt {
		if at := s.g.at(e.ID); at != outside && s.dead[at] && !with.dead[at] && with.holds(&e) {
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

// holds reports whether one exemption's holder, if it has one, is live.
func (s *sweep) holds(e *Exemption) bool {
	at := s.g.at(e.Holder)
	return e.Holder == "" || at == outside || !s.dead[at]
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

// admitUnreferencedTests adds every declaration of a test file that the run counting
// test references holds live under neither relation: no reference names it, or no
// root reaches it. The roots are every root, test roots included, every mark,
// exemption, test evidence and consumer reference, but not a test of dead code,
// which is reported, so a test-file declaration only reported declarations reach
// falls with them.
func (s *sweep) admitUnreferencedTests() {
	reached := s.reachedCountingTests()
	for i := range s.g.symbols {
		if !s.g.subject[i] || s.exempt[i] || s.marked[i] || s.testOfDeadCode[i] || reached[i] {
			continue
		}
		if _, inTestFile := IsTestFile(s.g.symbols[i].Pos.Filename); !inTestFile {
			continue
		}
		s.dead[i] = true
		s.unreferencedTest[i] = true
	}
}

// reachedCountingTests marks every symbol the roots [sweep.admitUnreferencedTests]
// names reach over every edge.
func (s *sweep) reachedCountingTests() []bool {
	reached := make([]bool, len(s.g.symbols))
	queue := make([]int, 0, len(s.g.rooted))
	seed := func(i int) {
		if !reached[i] && !s.testOfDeadCode[i] {
			reached[i] = true
			queue = append(queue, i)
		}
	}
	for _, r := range s.g.rooted {
		seed(r.at)
	}
	for i := range s.g.symbols {
		if s.marked[i] || s.exempt[i] || s.in.tested[s.g.symbols[i].ID] > 0 || s.g.consumedIn(i, s.in.Mode) {
			seed(i)
		}
	}
	for len(queue) > 0 {
		at := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, e := range s.g.out[at] {
			seed(e.to)
		}
	}
	return reached
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
		production, test := s.g.counted(i, s.in.Mode)
		relation := Reachability
		if s.g.references(i, s.in.Mode) == 0 && (!s.unreferencedTest[i] || production+test == 0) {
			relation = ReferenceCounting
		}
		r.Candidates = append(r.Candidates, Candidate{
			ID:               s.g.symbols[i].ID,
			ProductionRefs:   production,
			TestRefs:         test,
			Relation:         relation,
			TestOfDeadCode:   s.testOfDeadCode[i],
			UnreferencedTest: s.unreferencedTest[i],
		})
	}
	r.Components = s.g.componentsOf(s.dead, s.testOfDeadCode, s.unreferencedTest)
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
