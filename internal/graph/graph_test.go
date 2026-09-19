package graph

import (
	"fmt"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// The files a hand-built graph declares its symbols in. The file is what decides
// whether the language's own rule classifies a declaration as a test
// declaration, so a hand-built graph carries the same flag a loaded one does.
const (
	handFile     = "hand.go"
	handTestFile = "hand_test.go"
	handPackage  = "example.com/hand"
)

// handSymbol is one symbol of a hand-built graph: what a sweep reads of a
// Symbol, being the file that declares it, its line span and its container.
type handSymbol struct {
	name   string // what a test names the symbol by; no sweep reads it
	parent string // the container's name, empty at package level
	lines  int    // the declaration's line span, one when zero
	kind   SymbolKind
	inTest bool
}

// graphBuilder assembles the symbols, the references and the roots of one graph
// by hand, so a sweep is exercised over a graph no load produced. Each
// declaration takes the next lines of its file in the order it was declared, and
// the graph holds them by site the way the enumeration does.
type graphBuilder struct {
	sink    failureSink
	ids     map[string]SymbolID
	named   map[SymbolID]string
	lines   map[string]int
	symbols []Symbol
	refs    []Reference
	roots   []Root
	outside int
}

// newGraphBuilder starts an empty graph, reporting a contradictory build through
// sink, which both the unit tests and the properties supply.
func newGraphBuilder(sink failureSink) *graphBuilder {
	return &graphBuilder{
		sink:  sink,
		ids:   make(map[string]SymbolID),
		named: make(map[SymbolID]string),
		lines: make(map[string]int),
	}
}

// declare keeps one declaration.
func (b *graphBuilder) declare(d handSymbol) *graphBuilder {
	b.sink.Helper()
	if _, held := b.ids[d.name]; held {
		b.sink.Fatalf("the hand-built graph declares %s twice", d.name)
	}

	file := handFile
	if d.inTest {
		file = handTestFile
	}
	line := b.lines[file] + 1
	b.lines[file] += max(1, d.lines)

	id := SymbolID(fmt.Sprintf("%s:%d:1", file, line))
	b.ids[d.name] = id
	b.named[id] = d.name
	b.symbols = append(b.symbols, Symbol{
		ID:      id,
		Ref:     "hand://" + d.name,
		Name:    d.name,
		PkgPath: handPackage,
		Parent:  b.parentOf(d),
		Pos:     token.Position{Filename: file, Line: line, Column: 1},
		EndLine: b.lines[file],
		Kind:    d.kind,
	})
	return b
}

// parentOf resolves a declaration's container, which the builder requires to be
// declared already so that a graph is written from the outside in.
func (b *graphBuilder) parentOf(d handSymbol) SymbolID {
	b.sink.Helper()
	if d.parent == "" {
		return ""
	}
	return b.id(d.parent)
}

// add declares one package-level production function per name.
func (b *graphBuilder) add(names ...string) *graphBuilder {
	b.sink.Helper()
	for _, name := range names {
		b.declare(handSymbol{name: name})
	}
	return b
}

// addTest declares one function per name in the test file.
func (b *graphBuilder) addTest(names ...string) *graphBuilder {
	b.sink.Helper()
	for _, name := range names {
		b.declare(handSymbol{name: name, inTest: true})
	}
	return b
}

// ref keeps one reference from one declaration to another. Test agrees with the
// referencing declaration's file, which is how the reference pass records it.
func (b *graphBuilder) ref(from, to string) *graphBuilder {
	b.sink.Helper()
	fromID := b.id(from)
	b.refs = append(b.refs, Reference{
		From: fromID,
		To:   b.id(to),
		Pos:  token.Position{Filename: strings.Split(string(fromID), ":")[0], Line: 1, Column: 1},
		Kind: RefCall,
		Test: strings.Contains(string(fromID), handTestFile),
	})
	return b
}

// The consumer modules a hand-built graph's outside references come from.
const (
	handConsumer      = "example.com/consumer"
	handOtherConsumer = "example.com/other"
)

// refFromConsumer keeps one reference to a declaration from a loaded consumer
// module, made by one of that consumer's production files or by one of its test
// files, which is what the reference pass records for a module outside the target.
func (b *graphBuilder) refFromConsumer(consumer, to string, test bool) *graphBuilder {
	b.sink.Helper()
	b.outside++
	file := "consumer.go"
	if test {
		file = "consumer_test.go"
	}
	b.refs = append(b.refs, Reference{
		To:       b.id(to),
		Consumer: consumer,
		Pos:      token.Position{Filename: file, Line: b.outside, Column: 1},
		Kind:     RefCall,
		Test:     test,
	})
	return b
}

// refFromOutside keeps one reference to a declaration from a declaration the
// inventory does not hold and no consumer made, which is the contrast to a
// consumer's reference: it counts, and it names no caller.
func (b *graphBuilder) refFromOutside(to string) *graphBuilder {
	b.sink.Helper()
	b.outside++
	b.refs = append(b.refs, Reference{
		From: SymbolID(fmt.Sprintf("../consumer/consumer.go:%d:1", b.outside)),
		To:   b.id(to),
		Kind: RefCall,
	})
	return b
}

// refToOutside keeps one reference from a declaration to a symbol the inventory
// does not hold.
func (b *graphBuilder) refToOutside(from string) *graphBuilder {
	b.sink.Helper()
	b.outside++
	b.refs = append(b.refs, Reference{
		From: b.id(from),
		To:   SymbolID(fmt.Sprintf("../consumer/consumer.go:%d:1", b.outside)),
		Kind: RefCall,
	})
	return b
}

// root keeps one root of the given kind on a declared symbol.
func (b *graphBuilder) root(name string, kind RootKind) *graphBuilder {
	b.sink.Helper()
	b.roots = append(b.roots, Root{ID: b.id(name), Kind: kind})
	return b
}

// rootOutside keeps one root on a symbol the inventory does not hold.
func (b *graphBuilder) rootOutside(kind RootKind) *graphBuilder {
	b.outside++
	b.roots = append(b.roots, Root{ID: SymbolID(fmt.Sprintf("absent.go:%d:1", b.outside)), Kind: kind})
	return b
}

// id returns the identifier of one declared symbol.
func (b *graphBuilder) id(name string) SymbolID {
	b.sink.Helper()
	id, held := b.ids[name]
	if !held {
		b.sink.Fatalf("the hand-built graph declares no symbol named %s", name)
	}
	return id
}

// names returns the names the builder gave a set of symbols, in the order given.
func (b *graphBuilder) names(ids []SymbolID) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, b.named[id])
	}
	return names
}

// graph indexes what the builder holds, ordering the symbols by site the way the
// enumeration returns them.
func (b *graphBuilder) graph() *Graph {
	symbols := slices.Clone(b.symbols)
	slices.SortFunc(symbols, bySite)
	return New(symbols, b.refs, b.roots)
}

// verdict is one candidate as a test reads it.
type verdict struct {
	name           string
	relation       Relation
	productionRefs int
	testRefs       int
	testOfDeadCode bool
}

// verdicts names every candidate of one sweep, in the order the sweep returned
// them.
func (b *graphBuilder) verdicts(r Result) []verdict {
	found := make([]verdict, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		found = append(found, verdict{
			name:           b.named[c.ID],
			relation:       c.Relation,
			productionRefs: c.ProductionRefs,
			testRefs:       c.TestRefs,
			testOfDeadCode: c.TestOfDeadCode,
		})
	}
	return found
}

// candidates names every candidate of one sweep, without the counts.
func (b *graphBuilder) candidates(r Result) []string {
	names := make([]string, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		names = append(names, b.named[c.ID]+" "+c.Relation.String())
	}
	return names
}

// grouped is one dead component as a test reads it: the names of its members, of
// its roots and of what falls with it, and the lines the deletion removes.
type grouped struct {
	members string
	roots   string
	falls   string
	lines   int
}

// groups names every component of one sweep, in the order the sweep returned
// them.
func (b *graphBuilder) groups(r Result) []grouped {
	found := make([]grouped, 0, len(r.Components))
	for _, c := range r.Components {
		found = append(found, grouped{
			members: strings.Join(b.names(c.Members), " "),
			roots:   strings.Join(b.names(c.Roots), " "),
			falls:   strings.Join(b.names(c.Falls), " "),
			lines:   c.DeletableLines,
		})
	}
	return found
}

func TestNewCountsAReferenceWhoseSourceIsOutsideTheInventory(t *testing.T) {
	b := newGraphBuilder(t).add("consumed", "internal")
	b.refFromOutside("consumed")
	b.ref("consumed", "internal")
	got := b.verdicts(b.graph().Sweep(Mode{}))

	// A declaration outside the inventory is a declaration of the loaded graph,
	// so the reference it makes holds its target live under reference counting
	// and leaves it dead under reachability.
	want := []verdict{
		{name: "consumed", relation: Reachability, productionRefs: 1},
		{name: "internal", relation: Reachability, productionRefs: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a graph holding one reference from outside the inventory returned %+v, want %+v", got, want)
	}
}

func TestNewSplitsTheReferenceCountsByTheFileThatMadeThem(t *testing.T) {
	b := newGraphBuilder(t).add("subject", "caller", "entry").addTest("exercise")
	b.ref("caller", "subject")
	b.ref("exercise", "subject")
	b.ref("exercise", "subject")
	// The test declaration references one live symbol as well, so the rule that
	// admits a test of dead code leaves it out of this graph's answer.
	b.ref("exercise", "entry")
	b.root("entry", RootMain)
	got := b.verdicts(b.graph().Sweep(Mode{}))

	want := []verdict{
		{name: "subject", relation: Reachability, productionRefs: 1, testRefs: 2},
		{name: "caller", relation: ReferenceCounting},
		{name: "exercise", relation: ReferenceCounting},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over a graph holding one production and two test references returned %+v, want %+v", got, want)
	}
}

func TestNewKeepsNoRootAndNoReferenceNamingNoSymbol(t *testing.T) {
	b := newGraphBuilder(t).add("alone")
	b.rootOutside(RootMain)
	b.refToOutside("alone")
	got := b.candidates(b.graph().Sweep(Mode{}))

	// A root the inventory does not hold seeds nothing, and a reference to a
	// symbol it does not hold is a reference to nothing the sweep reasons about,
	// so the one declaration is dead under both relations.
	if want := []string{"alone reference-counting"}; !slices.Equal(got, want) {
		t.Errorf("Sweep over a graph whose root and whose reference name no symbol returned %v, want %v", got, want)
	}
}

func TestSpanCountsTheLinesOneDeclarationOccupies(t *testing.T) {
	cases := map[string]struct {
		symbol Symbol
		want   int
	}{
		"one line":                   {symbol: Symbol{Pos: token.Position{Line: 10}, EndLine: 10}, want: 1},
		"three lines":                {symbol: Symbol{Pos: token.Position{Line: 10}, EndLine: 12}, want: 3},
		"an end line that is unset":  {symbol: Symbol{Pos: token.Position{Line: 10}}, want: 1},
		"an end line above the line": {symbol: Symbol{Pos: token.Position{Line: 10}, EndLine: 9}, want: 1},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := span(&test.symbol); got != test.want {
				t.Errorf("span(%d to %d) = %d, want %d", test.symbol.Pos.Line, test.symbol.EndLine, got, test.want)
			}
		})
	}
}
