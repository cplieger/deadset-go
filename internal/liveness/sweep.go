package liveness

import (
	"slices"
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/vocab"
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

// SweepPolicy is what one sweep seeds, counts and follows besides the graph: the
// symbols a mechanism outside the reference graph holds live, the test evidence, the
// declarations outside programs may call, and the mode.
type SweepPolicy struct {
	// Unseen are the production declarations programs outside the analysis may
	// call, whose reachability class is possible: each counts as a live target of
	// every test that references it, so such a test is no test of dead code
	// ([grammar/analysis/tests.md] tests.6).
	Unseen map[inventory.SymbolID]bool

	// Marked are the symbols a matched suppression names: each is live under both
	// relations and seeds reachability at the mark tier.
	Marked []inventory.SymbolID

	// Exempt are the exemptions the classes computed, one per symbol and class,
	// under every configuration. An exempt symbol is never a candidate and seeds
	// reachability at the exemption tier; an exemption with a holder is a use by
	// each holder at that tier. A duplicate, one symbol, class, detail and holders,
	// is one retained record, at the first entry's site.
	Exempt []exempt.Exemption

	// TestEvidence is the exemption evidence a test file carries, which the mode
	// does not hold: each distinct fact is a test reference to its symbol, and
	// seeds the reach that admits unreferenced test-file declarations, as an
	// exemption does.
	TestEvidence []exempt.Exemption

	Mode facts.Mode
}

// Candidate is one dead symbol, the relation that found it, and the reference
// counts a later kind assignment reads.
type Candidate struct {
	ID inventory.SymbolID

	// ProductionRefs and TestRefs count every reference to the symbol, split as the
	// mode classifies them, whatever the mode counts: a kind reads both to tell a
	// symbol only tests reference from one nothing references.
	ProductionRefs int
	TestRefs       int

	// Configs is the set of build configurations the candidate exists in, every
	// one of which it is dead in, and empty over one configuration's graph.
	Configs inventory.ConfigSet

	Relation Relation

	// TestOfDeadCode reports a test declaration [sweep.admit] admitted rather than
	// a relation.
	TestOfDeadCode bool

	// UnreferencedTest reports a declaration of a test file that no root, and no
	// test of dead code, reaches through any reference, test files' included
	// ([sweep.unreferenced]). Its relation counts every reference.
	UnreferencedTest bool

	// KeptByTests reports that test code the run counting test references reaches,
	// and that is no test of dead code, references the symbol: a test that stays
	// keeps it.
	KeptByTests bool
}

// Result is one sweep's answer.
type Result struct {
	paths *provenance

	// TestUsed are the candidates whose findings are possible because production
	// code only tests reach is: each such declaration a test references, each dead
	// production declaration one reaches through a reference or as its container,
	// and each test of dead code whose every dead target is one, with every
	// candidate that test reaches. No member references a candidate outside the
	// set, so reporting the rest of a component without it deletes nothing a
	// withheld member needs ([grammar/analysis/confidence.md] confidence.21).
	TestUsed map[inventory.SymbolID]bool

	// Carried holds, per declaration every counted reference to which has
	// carriers ([facts.Reference.Carriers]), each distinct chain of carriers those
	// references hold, innermost first: what deleting them with their writes also
	// deletes, where one of each chain is a write-only subject.
	Carried map[inventory.SymbolID][][]inventory.SymbolID

	// LiveUnder holds, per symbol, the relations that hold it live; a symbol with
	// no entry is live under none.
	LiveUnder map[inventory.SymbolID]RelationSet

	// Candidates are the dead symbols in the order the inventory holds them,
	// each carrying the relation that found it.
	Candidates []Candidate

	// Components are the dead components in sweep order: each precedes every
	// component it reaches.
	Components []Component

	// Retained are the exemptions that held back a symbol this sweep would
	// otherwise report, in inventory order and then policy order. An exemption on a
	// symbol live anyway, or naming no inventory symbol, is not here.
	Retained []exempt.Exemption

	// Suppressed are the marks that held back a symbol, in inventory order and each
	// once; a mark on a symbol live anyway, exempt, or outside the inventory is
	// stale and not here.
	Suppressed []inventory.SymbolID
}

// Sweep answers which symbols of the graph are dead under the policy, which
// relation found each, which dead component each belongs to, which exemptions and
// marks held a symbol back, and how each live symbol is reached.
//
// Reachability is settled tier by tier ([tier]). What the marks held back is what
// the view without them judges dead, read from the tiers; what the exemptions held
// back is what the view counting only the marks in effect judges dead, which one
// more reachability pass answers.
func (g *Graph) Sweep(p *SweepPolicy) Result {
	g = g.using(p)
	s := &sweep{
		g:       g,
		p:       p,
		tested:  testReferencesOf(p.TestEvidence),
		marked:  g.positions(p.Marked),
		exempt:  g.exempted(p.Exempt),
		called:  make([]bool, len(g.symbols)),
		yielded: make([]bool, len(g.symbols)),
	}
	s.callers()
	s.paths = s.settle()
	all := s.judge(&view{exemptions: true, marked: s.marked})
	r := s.result(all)
	if len(p.Marked) > 0 {
		r.Suppressed = s.suppressed(s.judge(&view{exemptions: true}))
	}
	if len(p.Exempt) > 0 {
		// A mark the exemptions made stale leaves its symbol to them, so the view
		// an exemption held back from counts the marks in effect alone.
		without := &view{marked: g.positions(r.Suppressed)}
		without.reached, _, _ = s.pass(false, without.marked, true, p.Mode)
		r.Retained = s.retained(s.judge(without), all)
	}
	return r
}

// WithoutUncalled is the exemptions with every interface-satisfaction retention
// through an interface method the graph's implementation uses come from withdrawn:
// the implementation falls with that method rather than being held by it.
func (g *Graph) WithoutUncalled(exemptions []exempt.Exemption) []exempt.Exemption {
	return slices.DeleteFunc(slices.Clone(exemptions), func(e exempt.Exemption) bool {
		return e.Class == string(vocab.InterfaceSatisfaction) && e.Via != "" && g.uncalled[e.Via]
	})
}

// using is the graph with the uses the policy's held exemptions state added, and
// with every blank root such an exemption names yielded to its holder. The graph
// itself is unchanged, so one graph answers any number of policies.
func (g *Graph) using(p *SweepPolicy) *Graph {
	if !slices.ContainsFunc(p.Exempt, func(e exempt.Exemption) bool { return e.Holder != "" }) {
		return g
	}
	derived := *g
	derived.out = make([][]edge, len(g.out))
	for i := range g.out {
		derived.out[i] = slices.Clip(g.out[i])
	}
	derived.made = slices.Clone(g.made)
	derived.heldMade = slices.Clone(g.heldMade)
	derived.hops = slices.Clip(g.hops)
	derived.gates = slices.Clip(g.gates)
	held := make(map[int]bool)
	for ix := range p.Exempt {
		e := &p.Exempt[ix]
		if e.Holder == "" {
			continue
		}
		gate := 0
		if len(e.CoHolders) > 0 {
			derived.gates = append(derived.gates, g.gateOf(e))
			gate = len(derived.gates)
		}
		for _, holder := range append([]inventory.SymbolID{e.Holder}, e.CoHolders...) {
			derived.holderUse(e, holder, gate)
		}
		if at := g.at(e.ID); at != outside {
			held[at] = true
		}
	}
	derived.rooted = slices.Clone(g.rooted)
	for i := range derived.rooted {
		r := &derived.rooted[i]
		r.yielded = r.root.Kind == roots.RootBlank && held[r.at]
	}
	return &derived
}

// holderUse adds one exemption's use by one of its holders, waiting on the gate
// given, and counts it apart from the references so the caller tier does not.
func (g *Graph) holderUse(e *exempt.Exemption, holder inventory.SymbolID, gate int) {
	from, to := g.at(holder), g.at(e.ID)
	if from == outside || to == outside {
		return
	}
	test := g.symbols[from].InTestFile
	// An unappended use leaves another edge last, so only an edge this call
	// appended may be marked.
	if !g.add(Hop{From: holder, To: e.ID, Exemption: e}, test) {
		return
	}
	if test {
		g.heldMade[to].test++
	} else {
		g.heldMade[to].production++
	}
	last := &g.out[from][len(g.out[from])-1]
	last.held, last.gate = true, gate
}

// gateOf is the positions of the declarations holding one exemption, each held in
// the inventory. A holder outside it is no declaration a sweep decides, so it
// never closes the gate.
func (g *Graph) gateOf(e *exempt.Exemption) []int {
	var held []int
	for _, holder := range append([]inventory.SymbolID{e.Holder}, e.CoHolders...) {
		if at := g.at(holder); at != outside {
			held = append(held, at)
		}
	}
	return held
}

// positions returns one flag per symbol, set for each symbol the identifiers
// name. An identifier the inventory does not hold names none.
func (g *Graph) positions(ids []inventory.SymbolID) []bool {
	flags := make([]bool, len(g.symbols))
	for _, id := range ids {
		if at := g.at(id); at != outside {
			flags[at] = true
		}
	}
	return flags
}

// exempted returns one flag per symbol an exemption with no holder names.
func (g *Graph) exempted(exemptions []exempt.Exemption) []bool {
	ids := make([]inventory.SymbolID, 0, len(exemptions))
	for ix := range exemptions {
		if e := &exemptions[ix]; e.Holder == "" {
			ids = append(ids, e.ID)
		}
	}
	return g.positions(ids)
}

// sweep is one sweep's state over one graph.
type sweep struct {
	p      *SweepPolicy
	g      *Graph
	paths  *provenance
	tested map[inventory.SymbolID]int // per symbol, the distinct facts the test evidence states
	marked []bool
	exempt []bool
	called []bool
	// yielded marks the symbols a yielded root calls, which hold only once the
	// exemptions are withdrawn.
	yielded []bool
}

// callers keeps every symbol an actual caller reaches, live under both relations:
// a root of every kind but the published API, which only hypothesises a caller,
// and a symbol a loaded consumer references, a caller the analysis saw in the
// consumer's source. A published symbol is still a reachability seed, so it is a
// candidate only when nothing in the loaded graph references it.
func (s *sweep) callers() {
	for _, r := range s.g.rooted {
		switch {
		case r.yielded:
			s.yielded[r.at] = true
		case r.root.Kind != roots.RootPublishedAPI:
			s.called[r.at] = true
		}
	}
	for i := range s.g.symbols {
		if s.g.consumedIn(i, s.p.Mode) {
			s.called[i] = true
		}
	}
}

// view is what the sweep judges dead counting the held mechanisms it names: the
// full view counts the exemptions and every mark, the view a mark held back from
// counts the exemptions alone, and the view an exemption held back from counts the
// marks in effect alone, with what they and the callers reach.
type view struct {
	dead             []bool
	testOfDeadCode   []bool
	unreferencedTest []bool
	keptByTests      []bool
	marked           []bool // per symbol, a mark the view counts names it; nil for none
	reached          []bool // what the view reaches where it counts no exemption
	exemptions       bool
}

// judge decides every symbol under the view: the relations, then the tests of dead
// code, then the unreferenced test-file declarations, then the test-support code a
// reference keeps. Each step reads the one before it, which is the order the rules
// depend on.
func (s *sweep) judge(v *view) *view {
	n := len(s.g.symbols)
	v.dead, v.testOfDeadCode, v.unreferencedTest, v.keptByTests = make([]bool, n), make([]bool, n), make([]bool, n), make([]bool, n)
	for i := range s.g.symbols {
		v.dead[i] = s.relationDead(i, v)
	}
	s.admit(v)
	s.unreferenced(v)
	s.keepReferencedSupport(v)
	return v
}

// held reports whether the view counts an exemption or a mark that names the
// symbol at i.
func (s *sweep) held(i int, v *view) bool {
	return (v.exemptions && s.exempt[i]) || (i < len(v.marked) && v.marked[i])
}

// relationDead reports whether the sweep judges the symbol at i dead in the view
// under at least one relation and held back by no exemption.
func (s *sweep) relationDead(i int, v *view) bool {
	if !s.g.subject[i] || (v.exemptions && s.exempt[i]) {
		return false
	}
	if !v.exemptions && s.yielded[i] {
		return false
	}
	return !s.paths.reached(i, v) || !s.counted(i, v)
}

// counted reports whether reference counting holds the symbol at i live in the
// view: a caller or a counted reference holds it in every view, an exemption's use
// where the view counts exemptions, and a mark where it counts marks.
func (s *sweep) counted(i int, v *view) bool {
	n := s.g.references(i, s.p.Mode)
	return s.called[i] || n > s.g.heldMade[i].under(s.p.Mode) || (v.exemptions && n > 0) || (i < len(v.marked) && v.marked[i])
}

// admit adds every test declaration whose referenced production declarations are
// non-empty and all dead. It reads the references the test makes rather than those
// the mode counts: a production sweep counts none, so the mode's set would leave
// the rule unable to fire in the only mode where it can.
func (s *sweep) admit(v *view) {
	shared := s.sharedSupport(v)
	for i := range s.g.symbols {
		if !s.g.test[i] || !s.g.subject[i] || s.held(i, v) {
			continue
		}
		if targets, live := s.targetsOf(i, v, shared); targets > 0 && live == 0 {
			v.dead[i] = true
			v.testOfDeadCode[i] = true
		}
	}
}

// unreferenced adds every declaration of a test file that the run counting test
// references holds live under neither relation: no root reaches it. The roots are
// every root, test roots included, every mark, exemption, test evidence and consumer
// reference the view counts, but not a test of dead code, which is reported, so a
// test-file declaration only reported declarations reach falls with them.
func (s *sweep) unreferenced(v *view) {
	reached := s.reachedCountingTests(v)
	s.markKeptByTests(v, reached)
	for i := range s.g.symbols {
		if !s.g.subject[i] || !s.g.symbols[i].InTestFile || reached[i] || v.testOfDeadCode[i] || s.held(i, v) {
			continue
		}
		v.dead[i] = true
		v.unreferencedTest[i] = true
	}
}

// markKeptByTests marks every symbol a test reference of reached test code names,
// a test of dead code's excepted.
func (s *sweep) markKeptByTests(v *view, reached []bool) {
	for i := range s.g.symbols {
		if !s.g.test[i] || !reached[i] || v.testOfDeadCode[i] {
			continue
		}
		for _, e := range s.g.out[i] {
			if e.test {
				v.keptByTests[e.to] = true
			}
		}
	}
}

// keepReferencedSupport holds live every declaration of test-support code that a
// reference or test evidence names and that is no test of dead code: test-support
// code is test code, and a reference from test code keeps it live.
func (s *sweep) keepReferencedSupport(v *view) {
	for i := range s.g.symbols {
		referenced := s.g.made[i].test+s.g.made[i].production > 0 || s.tested[s.g.symbols[i].ID] > 0
		if v.dead[i] && !v.testOfDeadCode[i] && s.supportCode(i) && referenced {
			v.dead[i] = false
		}
	}
}

// reachedCountingTests marks every symbol the roots [sweep.unreferenced] names reach
// over every edge the view counts.
func (s *sweep) reachedCountingTests(v *view) []bool {
	reached := make([]bool, len(s.g.symbols))
	queue := make([]int, 0, len(s.g.rooted))
	seed := func(i int) {
		if !reached[i] && !v.testOfDeadCode[i] {
			reached[i] = true
			queue = append(queue, i)
		}
	}
	for _, r := range s.g.rooted {
		if !r.yielded || !v.exemptions {
			seed(r.at)
		}
	}
	for i := range s.g.symbols {
		if s.held(i, v) || s.tested[s.g.symbols[i].ID] > 0 || s.g.consumedIn(i, s.p.Mode) {
			seed(i)
		}
	}
	closure(s.g.out, s.g.gates, reached, queue, func(e edge) bool {
		return v.testOfDeadCode[e.to] || (e.held && !v.exemptions)
	}, nil)
	return reached
}

// sharedSupport marks every declaration of test-support code that a test referencing
// a live target symbol also references: it counts as a live target symbol of every
// test that references it.
func (s *sweep) sharedSupport(v *view) []bool {
	shared := make([]bool, len(s.g.symbols))
	for i := range s.g.symbols {
		if !s.g.test[i] {
			continue
		}
		if _, live := s.targetsOf(i, v, nil); live == 0 {
			continue
		}
		for _, e := range s.g.out[i] {
			if s.supportCode(e.to) {
				shared[e.to] = true
			}
		}
	}
	return shared
}

// supportCode reports whether one symbol is a declaration of test-support code
// rather than of a test file.
func (s *sweep) supportCode(at int) bool {
	return s.g.symbols[at].TestSupport && !s.g.symbols[at].InTestFile
}

// targetsOf counts the target symbols one test declaration references, and how many
// of them are not dead in the view: its production declarations, and each
// declaration of test-support code shared marks, which counts as live.
func (s *sweep) targetsOf(at int, v *view, shared []bool) (targets, live int) {
	for _, e := range s.g.out[at] {
		if e.to < len(shared) && shared[e.to] {
			targets++
			live++
			continue
		}
		if s.g.test[e.to] || !s.g.subject[e.to] {
			continue
		}
		targets++
		if !v.dead[e.to] || s.p.Unseen[s.g.symbols[e.to].ID] {
			live++
		}
	}
	return targets, live
}

// result collects the candidates of the full view in the order the inventory holds
// the symbols, whichever step admitted each, and the components over the set that
// results.
func (s *sweep) result(v *view) Result {
	r := Result{LiveUnder: make(map[inventory.SymbolID]RelationSet), paths: s.paths}
	reachable := make([]bool, len(s.g.symbols))
	for i := range s.g.symbols {
		var set RelationSet
		if s.counted(i, v) {
			set = set.with(ReferenceCounting)
		}
		if reachable[i] = s.paths.reached(i, v); reachable[i] {
			set = set.with(Reachability)
		}
		if set != 0 {
			r.LiveUnder[s.g.symbols[i].ID] = set
		}
		if !v.dead[i] {
			continue
		}
		production, test := s.g.counted(i, s.p.Mode)
		relation := Reachability
		if s.g.references(i, s.p.Mode) == 0 && (!v.unreferencedTest[i] || production+test == 0) {
			relation = ReferenceCounting
		}
		r.Candidates = append(r.Candidates, Candidate{
			ID:               s.g.symbols[i].ID,
			ProductionRefs:   production,
			TestRefs:         test + s.tested[s.g.symbols[i].ID],
			Configs:          s.g.symbols[i].Configs,
			Relation:         relation,
			TestOfDeadCode:   v.testOfDeadCode[i],
			UnreferencedTest: v.unreferencedTest[i],
			KeptByTests:      v.keptByTests[i],
		})
	}
	r.Components = s.g.componentsOf(v.dead, v.testOfDeadCode, v.unreferencedTest, reachable)
	r.TestUsed = s.testUsed(v)
	r.Carried = s.carried()
	return r
}

// carried is [Result.Carried], read from every configuration's own references: a
// declaration one counted reference without a carrier names carries nothing.
func (s *sweep) carried() map[inventory.SymbolID][][]inventory.SymbolID {
	carried := make(map[inventory.SymbolID][][]inventory.SymbolID)
	free := make(map[inventory.SymbolID]bool)
	for i := range s.g.carrying {
		r := &s.g.carrying[i]
		switch {
		case s.p.Mode.Uncounted(r), free[r.To]:
		case len(r.Carriers) == 0:
			free[r.To] = true
			delete(carried, r.To)
		case !slices.ContainsFunc(carried[r.To], func(chain []inventory.SymbolID) bool { return slices.Equal(chain, r.Carriers) }):
			carried[r.To] = append(carried[r.To], r.Carriers)
		}
	}
	return carried
}

// TestReferenced reports whether a test file references the declaration.
func (r *Result) TestReferenced(id inventory.SymbolID) bool {
	if r.paths == nil {
		return false
	}
	at := r.paths.g.at(id)
	return at != outside && r.paths.g.testReferenced(at)
}

// suppressed lists the marks that name a symbol the view without them judges dead,
// in inventory order and each once. Any other mark held nothing back: its symbol is
// outside the inventory, live, or exempt.
func (s *sweep) suppressed(without *view) []inventory.SymbolID {
	var found []inventory.SymbolID
	for i := range s.g.symbols {
		if s.marked[i] && without.dead[i] {
			found = append(found, s.g.symbols[i].ID)
		}
	}
	return found
}

// retained lists the exemptions that name a symbol the view without them judges
// dead and the full view does not, each symbol, class, detail and holders once at
// the first entry's site, in inventory order and then policy order. An exemption
// whose holder is dead held nothing back.
func (s *sweep) retained(without, all *view) []exempt.Exemption {
	held := make(map[inventory.SymbolID][]exempt.Exemption)
	kept := make(map[exempt.Fact]bool)
	for ix := range s.p.Exempt {
		e := &s.p.Exempt[ix]
		at := s.g.at(e.ID)
		if at == outside || !without.dead[at] || all.dead[at] || !s.holds(e, all) || kept[e.Fact()] {
			continue
		}
		kept[e.Fact()] = true
		held[e.ID] = append(held[e.ID], *e)
	}
	if len(held) == 0 {
		return nil
	}
	var found []exempt.Exemption
	for i := range s.g.symbols {
		found = append(found, held[s.g.symbols[i].ID]...)
	}
	return found
}

// holds reports whether one exemption's holder, if it has one, and each of its
// co-holders is live. A holder reachability holds live counts though reference
// counting finds it dead: a published declaration nothing loaded references still
// runs when an outside program calls it, and so does what it holds.
func (s *sweep) holds(e *exempt.Exemption, all *view) bool {
	for _, holder := range append([]inventory.SymbolID{e.Holder}, e.CoHolders...) {
		if at := s.g.at(holder); holder != "" && at != outside && all.dead[at] && !s.paths.reached(at, all) {
			return false
		}
	}
	return true
}

// testReferencesOf counts, per symbol, the distinct facts test evidence states
// about it: one per class and detail, however many configurations or sites found
// it. Each is a test reference to its symbol, as a reference from a test file is,
// and holds nothing live.
func testReferencesOf(evidence []exempt.Exemption) map[inventory.SymbolID]int {
	type fact struct {
		id     inventory.SymbolID
		class  string
		detail string
	}
	seen := make(map[fact]bool, len(evidence))
	counts := make(map[inventory.SymbolID]int, len(evidence))
	for ix := range evidence {
		e := &evidence[ix]
		if f := (fact{id: e.ID, class: e.Class, detail: e.Detail}); !seen[f] {
			seen[f] = true
			counts[e.ID]++
		}
	}
	return counts
}
