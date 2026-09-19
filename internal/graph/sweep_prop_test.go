package graph

import (
	"fmt"
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// rootKinds are the kinds a drawn root takes, which is every kind the detection
// and the configuration produce, so a draw exercises the one kind that seeds
// reachability alone.
func rootKinds() []RootKind {
	return []RootKind{
		RootMain, RootInit, RootTest, RootLinkname, RootCgoExport,
		RootBlank, RootPublishedAPI, RootConfigured, RootPattern,
	}
}

// drawnRoot is one root a draw places on one declaration.
type drawnRoot struct {
	at   int
	kind RootKind
}

// drawnClasses are the class spellings a draw places on an exemption. They are
// not the vocabulary's: the sweep carries a class through and never reads one, so
// a draw of two arbitrary spellings is what pins that it stays opaque.
func drawnClasses() []string { return []string{"alpha-class", "beta-class"} }

// drawnExemption is one exemption a draw places on one declaration.
type drawnExemption struct {
	class string
	at    int
}

// drawnGraph is one graph a draw produced: a number of package-level
// declarations, the references between them, the roots and the exemptions.
type drawnGraph struct {
	edges   [][2]int
	roots   []drawnRoot
	exempt  []drawnExemption
	symbols int
}

// drawnGraphs draws a graph of one to eight declarations, up to twelve references
// between them, up to three roots and up to four exemptions. Each root's kind is
// drawn on its own so the published API is exercised beside the kinds that name a
// caller, and each exemption's class on its own so two exemptions can hold one
// declaration.
func drawnGraphs() *rapid.Generator[drawnGraph] {
	return rapid.Custom(func(t *rapid.T) drawnGraph {
		count := rapid.IntRange(1, 8).Draw(t, "the number of declarations")
		edges := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) [2]int {
			return [2]int{
				rapid.IntRange(0, count-1).Draw(t, "the referencing declaration"),
				rapid.IntRange(0, count-1).Draw(t, "the referenced declaration"),
			}
		}), 0, 12).Draw(t, "the references")
		roots := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) drawnRoot {
			return drawnRoot{
				at:   rapid.IntRange(0, count-1).Draw(t, "the rooted declaration"),
				kind: rapid.SampledFrom(rootKinds()).Draw(t, "the root's kind"),
			}
		}), 0, 3).Draw(t, "the roots")
		exempt := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) drawnExemption {
			return drawnExemption{
				at:    rapid.IntRange(0, count-1).Draw(t, "the exempt declaration"),
				class: rapid.SampledFrom(drawnClasses()).Draw(t, "the exemption's class"),
			}
		}), 0, 4).Draw(t, "the exemptions")
		return drawnGraph{symbols: count, edges: edges, roots: roots, exempt: exempt}
	})
}

// The two declarations every P7 draw plants: one nothing references, and one only
// that declaration references, which is the case the two relations answer
// differently.
const (
	plantedReferencer = "plantedReferencer"
	plantedTarget     = "plantedTarget"
)

// drawn names one drawn declaration.
func drawn(at int) string { return fmt.Sprintf("d%02d", at) }

// build assembles one drawn graph, planting the pair the relations disagree over
// and adding the roots a caller supplies on top of the drawn ones.
func (d drawnGraph) build(t *rapid.T, added []drawnRoot) *graphBuilder {
	b := newGraphBuilder(t)
	for at := range d.symbols {
		b.add(drawn(at))
	}
	b.add(plantedReferencer, plantedTarget)
	b.ref(plantedReferencer, plantedTarget)

	for _, e := range d.edges {
		b.ref(drawn(e[0]), drawn(e[1]))
	}
	for _, r := range slices.Concat(d.roots, added) {
		b.root(drawn(r.at), r.kind)
	}
	return b
}

// input is what one drawn graph is swept under: the drawn exemptions, each naming
// the declaration it holds, the class that holds it and a site of its own, so two
// exemptions of one declaration are two entries.
func (d drawnGraph) input(b *graphBuilder) SweepInput {
	exempt := make([]Exemption, 0, len(d.exempt))
	for i, e := range d.exempt {
		exempt = append(exempt, Exemption{
			ID:    b.id(drawn(e.at)),
			Class: e.class,
			Site:  token.Position{Filename: handFile, Line: i + 1, Column: 1},
		})
	}
	return SweepInput{Exempt: exempt}
}

// exemptNames names every declaration a draw placed an exemption on.
func (d drawnGraph) exemptNames() map[string]bool {
	names := make(map[string]bool, len(d.exempt))
	for _, e := range d.exempt {
		names[drawn(e.at)] = true
	}
	return names
}

// liveness answers both relations the slow way: reference counting by counting
// the references made to each declaration, and reachability by repeating one pass
// over the reference list until the pass adds nothing.
//
// It is written over the names a draw produced rather than over the indexed
// adjacency the sweep builds, so an agreement between the two is not one
// implementation checking itself.
func (d drawnGraph) liveness(added []drawnRoot) (referenced, counted, reached map[string]bool) {
	referenced, counted, reached = map[string]bool{}, map[string]bool{}, map[string]bool{}
	edges := [][2]string{{plantedReferencer, plantedTarget}}
	for _, e := range d.edges {
		edges = append(edges, [2]string{drawn(e[0]), drawn(e[1])})
	}
	for _, e := range edges {
		referenced[e[1]] = true
		counted[e[1]] = true
	}
	for _, r := range slices.Concat(d.roots, added) {
		reached[drawn(r.at)] = true
		if r.kind != RootPublishedAPI {
			counted[drawn(r.at)] = true
		}
	}
	// An exemption seeds the closure, because the symbol it holds is live by a
	// mechanism the analysis cannot see and what it references runs. It does not
	// join the counted set: nothing in the graph references the held symbol, so
	// reference counting does not hold it live and an explanation of it reads the
	// retained record rather than the relations.
	for name := range d.exemptNames() {
		reached[name] = true
	}
	for changed := true; changed; {
		changed = false
		for _, e := range edges {
			if reached[e[0]] && !reached[e[1]] {
				reached[e[1]] = true
				changed = true
			}
		}
	}
	return referenced, counted, reached
}

// Property dead-code-suite/P7: every candidate records the liveness relation that
// found it, reference counting where the references made to the symbol are none
// and reachability otherwise; an exemption seeds the closure, so no symbol an
// exempt declaration reaches is reported under reachability; and a root changes no
// reference-counting verdict except on the symbol it names.
//
// The graph is built by hand rather than loaded, so a draw costs no package load
// and shrinks to the one declaration that carries a failure.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a
// deeper local run.
func TestProperty07TheRelationRecordedIsTheOneThatFoundTheCandidate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := drawnGraphs().Draw(t, "the graph")
		b := d.build(t, nil)
		r := b.graph().Sweep(d.input(b))

		referenced, counted, reached := d.liveness(nil)
		exempt := d.exemptNames()
		want := []string{}
		for _, name := range b.names(namesOf(b.symbols)) {
			if exempt[name] || (counted[name] && reached[name]) {
				continue
			}
			relation := Reachability
			if !referenced[name] {
				relation = ReferenceCounting
			}
			want = append(want, name+" "+relation.String())
		}
		if got := b.candidates(r); !slices.Equal(got, want) {
			t.Fatalf("Sweep over the drawn graph reported %v, want %v\n%s", got, want, describeDrawn(d))
		}

		// The planted pair is the case Requirement 5.2 names: one reference from
		// a declaration nothing reaches holds the target live under reference
		// counting and leaves it dead under reachability.
		planted := []string{
			plantedReferencer + " " + ReferenceCounting.String(),
			plantedTarget + " " + Reachability.String(),
		}
		for _, line := range planted {
			if !slices.Contains(b.candidates(r), line) {
				t.Fatalf("Sweep over the drawn graph reported %v, want it to hold %q\n%s", b.candidates(r), line, describeDrawn(d))
			}
		}

		// Adding a root changes reachability and, for every kind but the
		// published API, holds its own symbol live under reference counting. No
		// other symbol's reference-counting verdict moves.
		added := drawnRoot{
			at:   rapid.IntRange(0, d.symbols-1).Draw(t, "the declaration a root is added to"),
			kind: rapid.SampledFrom(rootKinds()).Draw(t, "the added root's kind"),
		}
		second := d.build(t, []drawnRoot{added})
		after := second.graph().Sweep(d.input(second))
		for _, name := range b.names(namesOf(b.symbols)) {
			before := r.LiveUnder[b.id(name)].Has(ReferenceCounting)
			now := after.LiveUnder[second.id(name)].Has(ReferenceCounting)
			caller := name == drawn(added.at) && added.kind != RootPublishedAPI
			switch {
			case caller && !now:
				t.Fatalf("adding a %s root to %s left it dead under %s\n%s", added.kind, name, ReferenceCounting, describeDrawn(d))
			case !caller && before != now:
				t.Fatalf("adding a %s root to %s moved %s under %s from %t to %t\n%s",
					added.kind, drawn(added.at), name, ReferenceCounting, before, now, describeDrawn(d))
			}
		}
	})
}

// namesOf lists the identifiers of a symbol set in the order it holds them.
func namesOf(symbols []Symbol) []SymbolID {
	ids := make([]SymbolID, 0, len(symbols))
	for _, s := range symbols {
		ids = append(ids, s.ID)
	}
	return ids
}

// describeDrawn prints one drawn graph so a failure carries the graph rather than
// only its size.
func describeDrawn(d drawnGraph) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d declarations\n", d.symbols)
	for _, e := range d.edges {
		fmt.Fprintf(&b, "reference %s -> %s\n", drawn(e[0]), drawn(e[1]))
	}
	for _, r := range d.roots {
		fmt.Fprintf(&b, "root %s %s\n", drawn(r.at), r.kind)
	}
	for _, e := range d.exempt {
		fmt.Fprintf(&b, "exemption %s %s\n", drawn(e.at), e.class)
	}
	return b.String()
}

// The shapes a P6 draw plants, each rendered with names of its own so one draw
// holds several without their references crossing.
const (
	shapeChain     = "chain"
	shapeCycle     = "cycle"
	shapeContainer = "nested members"
	shapeDeadTest  = "a test of dead code"
	shapeLiveTest  = "a test of live code"
)

// drawnShape is one structure a draw plants.
type drawnShape struct {
	kind string
	size int
}

// drawnShapes draws one to four structures, each of one to four declarations.
func drawnShapes() *rapid.Generator[[]drawnShape] {
	one := rapid.Custom(func(t *rapid.T) drawnShape {
		return drawnShape{
			kind: rapid.SampledFrom([]string{shapeChain, shapeCycle, shapeContainer, shapeDeadTest, shapeLiveTest}).
				Draw(t, "the shape"),
			size: rapid.IntRange(1, 4).Draw(t, "the shape's declarations"),
		}
	})
	return rapid.SliceOfN(one, 1, 4)
}

// plant renders one shape into a graph, naming every declaration for the shape's
// position so two shapes of one draw never share a name. It returns the name of
// the test declaration the shape's own structure admits as a test of dead code,
// and the empty string where the shape admits none, so the oracle decides the
// rule from what was drawn rather than from the sweep's answer.
func plant(b *graphBuilder, at int, shape drawnShape) string {
	name := func(i int) string { return fmt.Sprintf("s%02d_%02d", at, i) }
	span := func(i int) int { return 1 + i%3 }

	switch shape.kind {
	case shapeChain:
		for i := range shape.size {
			b.declare(handSymbol{name: name(i), lines: span(i)})
			if i > 0 {
				b.ref(name(i-1), name(i))
			}
		}
	case shapeCycle:
		for i := range shape.size {
			b.declare(handSymbol{name: name(i), lines: span(i)})
		}
		for i := range shape.size {
			b.ref(name(i), name((i+1)%shape.size))
		}
	case shapeContainer:
		for i := range shape.size {
			d := handSymbol{name: name(i), lines: span(i), kind: KindType}
			if i > 0 {
				d.parent, d.kind = name(i-1), KindField
			}
			b.declare(d)
		}
	case shapeDeadTest:
		// Nothing outside the shape names these declarations and no root reaches
		// them, so a production sweep counts none of the test's own references and
		// every target is dead: the rule admits the test whatever the size drawn.
		for i := range shape.size {
			b.declare(handSymbol{name: name(i), lines: span(i)})
		}
		b.declare(handSymbol{name: name(shape.size), inTest: true, lines: 2})
		b.root(name(shape.size), RootTest)
		for i := range shape.size {
			b.ref(name(shape.size), name(i))
		}
		return name(shape.size)
	case shapeLiveTest:
		// Every target carries one reference from the shape's own root, so every
		// target is live and the rule admits nothing.
		b.declare(handSymbol{name: name(0), lines: 2})
		b.root(name(0), RootMain)
		for i := 1; i <= shape.size; i++ {
			b.declare(handSymbol{name: name(i), lines: span(i)})
			b.ref(name(0), name(i))
		}
		b.declare(handSymbol{name: name(shape.size + 1), inTest: true, lines: 2})
		b.root(name(shape.size+1), RootTest)
		for i := 1; i <= shape.size; i++ {
			b.ref(name(shape.size+1), name(i))
		}
	}
	return ""
}

// Property dead-code-suite/P6: every dead symbol lands in exactly one component,
// each component is reported at its roots, and the set that falls with a
// component is the one the condensation over the components decides.
//
// The oracle is written over the names a draw produced and answers mutual
// reachability by a transitive closure rather than by Tarjan's algorithm, so an
// agreement between the two is not one implementation checking itself.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a
// deeper local run.
func TestProperty06EveryDeadSymbolLandsInOneComponentReportedAtItsRoots(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		shapes := drawnShapes().Draw(t, "the shapes")
		b := newGraphBuilder(t)
		admitted := map[string]bool{}
		for at, shape := range shapes {
			if name := plant(b, at, shape); name != "" {
				admitted[name] = true
			}
		}
		g := b.graph()
		r := g.Sweep(SweepInput{Mode: Mode{Production: true}})

		dead := make([]string, 0, len(r.Candidates))
		reported := map[string]bool{}
		for _, c := range r.Candidates {
			dead = append(dead, b.named[c.ID])
			if c.TestOfDeadCode {
				reported[b.named[c.ID]] = true
			}
		}
		if !maps.Equal(reported, admitted) {
			t.Fatalf("Sweep admitted %v as tests of dead code, want %v\n%s",
				slices.Sorted(maps.Keys(reported)), slices.Sorted(maps.Keys(admitted)), b.describe(r))
		}
		held := map[string]int{}
		for _, c := range r.Components {
			for _, member := range c.Members {
				held[b.named[member]]++
			}
		}
		for _, name := range dead {
			if held[name] != 1 {
				t.Fatalf("%s is a member of %d components, want exactly 1\n%s", name, held[name], b.describe(r))
			}
		}
		if len(held) != len(dead) {
			t.Fatalf("the components hold %d symbols and the sweep reported %d candidates\n%s", len(held), len(dead), b.describe(r))
		}

		want := b.slowComponents(g, r, dead, admitted)
		if got := b.groups(r); !slices.Equal(got, want) {
			t.Fatalf("Sweep returned components\n%+v\nwant\n%+v\n%s", got, want, b.describe(r))
		}
		for i, c := range r.Components {
			if c.Index != i {
				t.Fatalf("Components[%d].Index = %d, want %d\n%s", i, c.Index, i, b.describe(r))
			}
			if lines := b.lineTotal(g, c.Falls); lines != c.DeletableLines {
				t.Fatalf("Components[%d].DeletableLines = %d, want %d over %v\n%s",
					i, c.DeletableLines, lines, b.names(c.Falls), b.describe(r))
			}
		}
	})
}

// describe prints one sweep's answer for a failure message.
func (b *graphBuilder) describe(r Result) string {
	var out strings.Builder
	for _, s := range b.symbols {
		fmt.Fprintf(&out, "declaration %s parent=%s lines=%d\n", b.named[s.ID], b.named[s.Parent], span(&s))
	}
	for _, ref := range b.refs {
		fmt.Fprintf(&out, "reference %s -> %s test=%t\n", b.named[ref.From], b.named[ref.To], ref.Test)
	}
	for _, root := range b.roots {
		fmt.Fprintf(&out, "root %s %s\n", b.named[root.ID], root.Kind)
	}
	for _, c := range b.verdicts(r) {
		fmt.Fprintf(&out, "candidate %+v\n", c)
	}
	return out.String()
}

// lineTotal sums the line spans of a set of symbols.
func (b *graphBuilder) lineTotal(g *Graph, ids []SymbolID) int {
	total := 0
	for _, id := range ids {
		symbol := g.symbols[g.at(id)]
		total += span(&symbol)
	}
	return total
}

// slowComponents answers the component pass the slow way, from the graph's own
// inputs: two dead declarations share a component when each reaches the other
// through the dead subgraph, a component is a root of the condensation when no
// other component reaches it, and a component falls with the one root of the
// condensation that reaches it, if exactly one does.
func (b *graphBuilder) slowComponents(g *Graph, r Result, dead []string, admitted map[string]bool) []grouped {
	edges := b.slowSubgraph(dead, admitted)
	reaches := closureByName(edges, dead)

	var groups [][]string
	of := map[string]int{}
	for _, name := range dead {
		if _, held := of[name]; held {
			continue
		}
		group := []string{}
		for _, other := range dead {
			if _, held := of[other]; !held && reaches[name][other] && reaches[other][name] {
				of[other] = len(groups)
				group = append(group, other)
			}
		}
		groups = append(groups, group)
	}

	between := make([]map[int]bool, len(groups))
	into := make([]int, len(groups))
	for i := range groups {
		between[i] = map[int]bool{}
	}
	for from, targets := range edges {
		for _, to := range targets {
			if of[from] == of[to] || between[of[from]][of[to]] {
				continue
			}
			between[of[from]][of[to]] = true
			into[of[to]]++
		}
	}

	owners := make([]int, len(groups))
	for i := range groups {
		if into[i] != 0 {
			continue
		}
		for j := range groups {
			if reachesGroup(between, i, j) {
				owners[j]++
			}
		}
	}

	found := make([]grouped, 0, len(groups))
	for i, members := range groups {
		falls := slices.Clone(members)
		var roots []string
		for _, member := range members {
			container := b.named[g.symbols[g.at(b.id(member))].Parent]
			if !b.referencedFromOutside(edges, of, member) && (container == "" || of[container] != of[member]) {
				roots = append(roots, member)
			}
		}
		if into[i] == 0 {
			for j, other := range groups {
				if j != i && reachesGroup(between, i, j) && owners[j] == 1 {
					falls = append(falls, other...)
				}
			}
		}
		found = append(found, grouped{
			members: strings.Join(b.bySite(members), " "),
			roots:   strings.Join(b.bySite(roots), " "),
			falls:   strings.Join(b.bySite(falls), " "),
			lines:   b.lineTotal(g, b.identifiers(falls)),
		})
	}
	return b.orderBySweep(r, found, of)
}

// slowSubgraph is the dead subgraph the oracle reads: every reference one dead
// declaration makes to another, the container relation both ways, and the edge
// back from every target of a test the drawn shapes admit.
func (b *graphBuilder) slowSubgraph(dead []string, admitted map[string]bool) map[string][]string {
	isDead := map[string]bool{}
	for _, name := range dead {
		isDead[name] = true
	}
	tests := map[string]bool{}
	for _, s := range b.symbols {
		if _, test := IsTestFile(s.Pos.Filename); test {
			tests[b.named[s.ID]] = true
		}
	}

	edges := map[string][]string{}
	for _, ref := range b.refs {
		from, to := b.named[ref.From], b.named[ref.To]
		if !isDead[from] || !isDead[to] {
			continue
		}
		edges[from] = append(edges[from], to)
		if admitted[from] && !tests[to] {
			edges[to] = append(edges[to], from)
		}
	}
	for _, s := range b.symbols {
		child, container := b.named[s.ID], b.named[s.Parent]
		if container == "" || !isDead[child] || !isDead[container] {
			continue
		}
		edges[child] = append(edges[child], container)
		edges[container] = append(edges[container], child)
	}
	return edges
}

// referencedFromOutside reports whether a dead component other than the member's
// own references it.
func (b *graphBuilder) referencedFromOutside(edges map[string][]string, of map[string]int, member string) bool {
	for from, targets := range edges {
		for _, to := range targets {
			if to == member && of[from] != of[member] {
				return true
			}
		}
	}
	return false
}

// closureByName answers which dead declarations each dead declaration reaches,
// itself included, by repeating one pass over the edges until it adds nothing.
func closureByName(edges map[string][]string, dead []string) map[string]map[string]bool {
	reaches := make(map[string]map[string]bool, len(dead))
	for _, name := range dead {
		reaches[name] = map[string]bool{name: true}
	}
	for changed := true; changed; {
		changed = false
		for _, name := range dead {
			for _, at := range slices.Sorted(maps.Keys(reaches[name])) {
				for _, to := range edges[at] {
					if !reaches[name][to] {
						reaches[name][to] = true
						changed = true
					}
				}
			}
		}
	}
	return reaches
}

// reachesGroup reports whether one component reaches another, itself included.
func reachesGroup(between []map[int]bool, from, to int) bool {
	if from == to {
		return true
	}
	seen := map[int]bool{from: true}
	stack := []int{from}
	for len(stack) > 0 {
		at := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for next := range between[at] {
			if next == to {
				return true
			}
			if !seen[next] {
				seen[next] = true
				stack = append(stack, next)
			}
		}
	}
	return false
}

// bySite orders a set of names the way the graph holds the symbols they name.
func (b *graphBuilder) bySite(names []string) []string {
	ordered := make([]string, 0, len(names))
	for _, s := range b.sorted() {
		if slices.Contains(names, b.named[s.ID]) {
			ordered = append(ordered, b.named[s.ID])
		}
	}
	return ordered
}

// identifiers names the symbols a set of names identifies.
func (b *graphBuilder) identifiers(names []string) []SymbolID {
	ids := make([]SymbolID, 0, len(names))
	for _, name := range names {
		ids = append(ids, b.ids[name])
	}
	return ids
}

// sorted returns the symbols in the order the graph holds them.
func (b *graphBuilder) sorted() []Symbol {
	symbols := slices.Clone(b.symbols)
	slices.SortFunc(symbols, bySite)
	return symbols
}

// orderBySweep puts the oracle's components in the order the sweep returned its
// own, so a comparison is over the contents rather than over the order, which the
// unit tests pin on their own.
func (b *graphBuilder) orderBySweep(r Result, found []grouped, of map[string]int) []grouped {
	ordered := make([]grouped, 0, len(found))
	for _, c := range r.Components {
		ordered = append(ordered, found[of[b.named[c.Members[0]]]])
	}
	return ordered
}

// drawnConsumerRef is one reference a drawn consumer module makes to one drawn
// declaration.
type drawnConsumerRef struct {
	consumer int  // the consumer that makes it, by place in the drawn set
	at       int  // the declaration it references
	test     bool // one of the consumer's test files makes it
}

// drawnLoad is one run a draw produced: the declarations of the target, the
// references between them, the number of consumers loaded, the references those
// consumers make, and what the run's mode counts.
type drawnLoad struct {
	edges     [][2]int
	consumed  []drawnConsumerRef
	symbols   int
	consumers int
	mode      Mode
}

// drawnConsumer names one consumer of a drawn run as a module path, which is the
// identifier a reference carries.
func drawnConsumer(at int) string { return fmt.Sprintf("example.com/consumer%02d", at) }

// drawnLoads draws one to six declarations, up to eight references between them,
// one to three consumers, up to six references from those consumers, and the mode
// the sweep runs in. The mode is drawn because what a consumer's test reference
// counts as is the mode's, and the property holds under every combination.
func drawnLoads() *rapid.Generator[drawnLoad] {
	return rapid.Custom(func(t *rapid.T) drawnLoad {
		count := rapid.IntRange(1, 6).Draw(t, "the number of declarations")
		consumers := rapid.IntRange(1, 3).Draw(t, "the number of consumers")
		edges := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) [2]int {
			return [2]int{
				rapid.IntRange(0, count-1).Draw(t, "the referencing declaration"),
				rapid.IntRange(0, count-1).Draw(t, "the referenced declaration"),
			}
		}), 0, 8).Draw(t, "the target's own references")
		consumed := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) drawnConsumerRef {
			return drawnConsumerRef{
				consumer: rapid.IntRange(0, consumers-1).Draw(t, "the consumer that references"),
				at:       rapid.IntRange(0, count-1).Draw(t, "the declaration a consumer references"),
				test:     rapid.Bool().Draw(t, "the reference is in a consumer's test file"),
			}
		}), 0, 6).Draw(t, "the consumers' references")
		return drawnLoad{
			symbols:   count,
			consumers: consumers,
			edges:     edges,
			consumed:  consumed,
			mode: Mode{
				Production:              rapid.Bool().Draw(t, "the sweep counts production references alone"),
				ConsumerTestsProduction: rapid.Bool().Draw(t, "a consumer's test reference counts as production"),
			},
		}
	})
}

// build assembles one drawn run: the declarations of the target with the
// references between them, then the references each drawn consumer makes.
func (d drawnLoad) build(t *rapid.T) *graphBuilder {
	b := newGraphBuilder(t)
	for at := range d.symbols {
		b.add(drawn(at))
	}
	for _, e := range d.edges {
		b.ref(drawn(e[0]), drawn(e[1]))
	}
	for _, c := range d.consumed {
		b.refFromConsumer(drawnConsumer(c.consumer), drawn(c.at), c.test)
	}
	return b
}

// counts answers what the mode counts the slow way, over the names a draw
// produced: how many references each declaration carries, which declarations a
// consumer's counted reference calls, and which consumers reference each
// declaration whatever the mode.
func (d drawnLoad) counts() (referenced map[string]int, called map[string]bool, by map[string][]string) {
	referenced, called, by = map[string]int{}, map[string]bool{}, map[string][]string{}
	for _, e := range d.edges {
		referenced[drawn(e[1])]++
	}
	for _, c := range d.consumed {
		name := drawn(c.at)
		module := drawnConsumer(c.consumer)
		if !slices.Contains(by[name], module) {
			by[name] = append(by[name], module)
		}
		if c.test && d.mode.Production && !d.mode.ConsumerTestsProduction {
			continue
		}
		referenced[name]++
		called[name] = true
	}
	for name := range by {
		slices.Sort(by[name])
	}
	return referenced, called, by
}

// reachable answers reachability the slow way: every declaration a consumer's
// counted reference calls, and everything those reach through the target's own
// references, by repeating one pass until it adds nothing.
func (d drawnLoad) reachable(called map[string]bool) map[string]bool {
	reached := maps.Clone(called)
	for changed := true; changed; {
		changed = false
		for _, e := range d.edges {
			if reached[drawn(e[0])] && !reached[drawn(e[1])] {
				reached[drawn(e[1])] = true
				changed = true
			}
		}
	}
	return reached
}

// describeLoad prints one drawn run so a failure carries the run rather than only
// its size.
func describeLoad(d drawnLoad) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d declarations, %d consumers, production=%t consumerTestsProduction=%t\n",
		d.symbols, d.consumers, d.mode.Production, d.mode.ConsumerTestsProduction)
	for _, e := range d.edges {
		fmt.Fprintf(&b, "reference %s -> %s\n", drawn(e[0]), drawn(e[1]))
	}
	for _, c := range d.consumed {
		fmt.Fprintf(&b, "consumer %s -> %s test=%t\n", drawnConsumer(c.consumer), drawn(c.at), c.test)
	}
	return b.String()
}

// Property dead-code-suite/P3: a reference from any loaded module prevents the
// finding. A declaration the mode counts one reference to is live under reference
// counting whichever module made it, a declaration a loaded consumer calls is live
// under both relations and keeps what it references live, a declaration no counted
// reference names is a candidate, and the consumers a declaration's references come
// from are exactly the ones that referenced it.
//
// The graph is built by hand rather than loaded, so a draw costs no package load
// and shrinks to the one declaration that carries a failure.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a
// deeper local run.
func TestProperty03AReferenceFromAnyLoadedModulePreventsTheFinding(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := drawnLoads().Draw(t, "the run")
		b := d.build(t)
		g := b.graph()
		r := g.Sweep(SweepInput{Mode: d.mode})

		referenced, called, by := d.counts()
		reached := d.reachable(called)

		want := []string{}
		for _, name := range b.names(namesOf(b.symbols)) {
			if referenced[name] > 0 && reached[name] {
				continue
			}
			relation := Reachability
			if referenced[name] == 0 {
				relation = ReferenceCounting
			}
			want = append(want, name+" "+relation.String())
		}
		if got := b.candidates(r); !slices.Equal(got, want) {
			t.Fatalf("Sweep over the drawn run reported %v, want %v\n%s", got, want, describeLoad(d))
		}

		for _, name := range b.names(namesOf(b.symbols)) {
			set := r.LiveUnder[b.id(name)]
			switch {
			case referenced[name] > 0 && !set.Has(ReferenceCounting):
				t.Fatalf("%s carries %d counted references and is dead under %s\n%s",
					name, referenced[name], ReferenceCounting, describeLoad(d))
			case called[name] && !set.Has(Reachability):
				t.Fatalf("%s is called by a loaded consumer and is dead under %s\n%s",
					name, Reachability, describeLoad(d))
			case referenced[name] == 0 && set.Has(ReferenceCounting):
				t.Fatalf("%s carries no counted reference and is live under %s\n%s",
					name, ReferenceCounting, describeLoad(d))
			}
			if got := g.ConsumersOf(b.id(name)); !slices.Equal(got, by[name]) {
				t.Fatalf("ConsumersOf(%s) = %v, want %v\n%s", name, got, by[name], describeLoad(d))
			}
		}
	})
}
