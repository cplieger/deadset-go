package graph

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// unreachableChain is the graph both relations disagree over: one declaration a
// root reaches, one nothing references, and one only the unreferenced declaration
// references.
func unreachableChain(t *testing.T) *graphBuilder {
	t.Helper()
	b := newGraphBuilder(t).add("entry", "reached", "unreferenced", "referencedByDeadCode")
	b.root("entry", RootMain)
	b.ref("entry", "reached")
	b.ref("unreferenced", "referencedByDeadCode")
	return b
}

func TestSweepNamesTheRelationThatFoundEachCandidate(t *testing.T) {
	b := unreachableChain(t)
	got := b.verdicts(b.graph().Sweep(Mode{}))

	// The chain is the case the two relations answer differently: one reference
	// from a declaration nothing reaches holds its target live under reference
	// counting, and no path from a root reaches it.
	want := []verdict{
		{name: "unreferenced", relation: ReferenceCounting},
		{name: "referencedByDeadCode", relation: Reachability, productionRefs: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep over the unreachable chain returned %+v, want %+v", got, want)
	}
}

func TestSweepNamesTheRelationsHoldingEachSymbolLive(t *testing.T) {
	b := unreachableChain(t)
	live := b.graph().Sweep(Mode{}).LiveUnder

	cases := map[string]struct {
		wantCounted, wantReached bool
	}{
		"entry":                {wantCounted: true, wantReached: true},
		"reached":              {wantCounted: true, wantReached: true},
		"unreferenced":         {},
		"referencedByDeadCode": {wantCounted: true},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			set := live[b.id(name)]
			if got := set.Has(ReferenceCounting); got != test.wantCounted {
				t.Errorf("Sweep().LiveUnder[%s].Has(%s) = %t, want %t", name, ReferenceCounting, got, test.wantCounted)
			}
			if got := set.Has(Reachability); got != test.wantReached {
				t.Errorf("Sweep().LiveUnder[%s].Has(%s) = %t, want %t", name, Reachability, got, test.wantReached)
			}
		})
	}
}

func TestSweepHoldsAMarkedSymbolLiveUnderBothRelationsWithWhatItReferences(t *testing.T) {
	b := unreachableChain(t)
	r := b.graph().Sweep(Mode{Marked: []SymbolID{b.id("unreferenced")}})

	// A mark seeds reachability as well as holding its own symbol live, so
	// nothing the marked declaration alone references is reported. A mark that
	// only withheld the finding would leave the symbol below it reported, which
	// is the report a maintainer cannot suppress.
	if got := b.candidates(r); len(got) != 0 {
		t.Errorf("Sweep over the unreachable chain with the unreferenced declaration marked returned %v, want no candidate", got)
	}
	set := r.LiveUnder[b.id("unreferenced")]
	if !set.Has(ReferenceCounting) || !set.Has(Reachability) {
		t.Errorf("Sweep().LiveUnder[unreferenced] holds %v, want both relations", set)
	}
}

func TestSweepReportsAPublishedRootUnderReferenceCountingAndHoldsItsClosureLive(t *testing.T) {
	b := newGraphBuilder(t).add("Exported", "helper")
	b.root("Exported", RootPublishedAPI)
	b.ref("Exported", "helper")
	r := b.graph().Sweep(Mode{})

	// The published API is the one root kind that hypothesises its caller: the
	// exported symbol nothing references is a candidate under reference
	// counting, while reachability keeps its closure out of the dead set.
	if want := []string{"Exported reference-counting"}; !slices.Equal(b.candidates(r), want) {
		t.Errorf("Sweep over a published root returned %v, want %v", b.candidates(r), want)
	}
	want := []grouped{{members: "Exported", roots: "Exported", falls: "Exported", lines: 1}}
	if got := b.groups(r); !slices.Equal(got, want) {
		t.Errorf("Sweep over a published root returned components %+v, want %+v", got, want)
	}
}

func TestSweepHoldsEveryRootThatNamesACallerLiveUnderBothRelations(t *testing.T) {
	cases := []struct {
		kind RootKind
		want []string
	}{
		{kind: RootMain},
		{kind: RootInit},
		{kind: RootTest},
		{kind: RootLinkname},
		{kind: RootCgoExport},
		{kind: RootBlank},
		{kind: RootConfigured},
		{kind: RootPattern},
		{kind: RootPublishedAPI, want: []string{"entry reference-counting"}},
	}
	for _, test := range cases {
		t.Run(test.kind.String(), func(t *testing.T) {
			b := newGraphBuilder(t).add("entry")
			b.root("entry", test.kind)
			if got := b.candidates(b.graph().Sweep(Mode{})); !slices.Equal(got, test.want) {
				t.Errorf("Sweep over one %s root returned %v, want %v", test.kind, got, test.want)
			}
		})
	}
}

// testOnlyUse is the graph a production sweep answers differently: a declaration
// production code reaches, a declaration only a test reaches, and a test that
// references both, so the rule admitting a test of dead code leaves the test out.
func testOnlyUse(t *testing.T) *graphBuilder {
	t.Helper()
	b := newGraphBuilder(t).add("entry", "usedByProduction", "usedByTestOnly").addTest("TestBoth")
	b.root("entry", RootMain)
	b.root("TestBoth", RootTest)
	b.ref("entry", "usedByProduction")
	b.ref("TestBoth", "usedByProduction")
	b.ref("TestBoth", "usedByTestOnly")
	return b
}

func TestSweepCountsEveryReferenceAndSeedsEveryRoot(t *testing.T) {
	b := testOnlyUse(t)
	if got := b.candidates(b.graph().Sweep(Mode{})); len(got) != 0 {
		t.Errorf("Sweep counting every reference returned %v, want no candidate", got)
	}
}

func TestSweepUnderProductionModeCountsNoTestReferenceAndSeedsNoTestRoot(t *testing.T) {
	b := testOnlyUse(t)
	got := b.verdicts(b.graph().Sweep(Mode{Production: true}))

	// The test root leaves the seed and stays live, so the test function is not a
	// candidate and the declaration only it reaches is dead under both relations.
	want := []verdict{{name: "usedByTestOnly", relation: ReferenceCounting, testRefs: 1}}
	if !slices.Equal(got, want) {
		t.Errorf("Sweep under production mode returned %+v, want %+v", got, want)
	}
}

func TestSweepAdmitsATestOfDeadCodeAndDeclinesATestOfLiveCode(t *testing.T) {
	b := newGraphBuilder(t).add("entry", "live", "deadOne", "deadTwo").addTest("TestDead", "TestLive")
	b.root("entry", RootMain)
	b.root("TestDead", RootTest)
	b.root("TestLive", RootTest)
	b.ref("entry", "live")
	b.ref("TestDead", "deadOne")
	b.ref("TestDead", "deadTwo")
	b.ref("TestLive", "live")
	b.ref("TestLive", "deadOne")
	r := b.graph().Sweep(Mode{Production: true})

	want := []verdict{
		{name: "deadOne", relation: ReferenceCounting, testRefs: 2},
		{name: "deadTwo", relation: ReferenceCounting, testRefs: 1},
		{name: "TestDead", relation: ReferenceCounting, testOfDeadCode: true},
	}
	if got := b.verdicts(r); !slices.Equal(got, want) {
		t.Errorf("Sweep over two tests of one dead declaration returned %+v, want %+v", got, want)
	}
	// The admitted test lands in the component of the declarations it exercises,
	// so one deletion removes the test and its subjects together.
	components := []grouped{{
		members: "deadOne deadTwo TestDead",
		roots:   "deadOne deadTwo TestDead",
		falls:   "deadOne deadTwo TestDead",
		lines:   3,
	}}
	if got := b.groups(r); !slices.Equal(got, components) {
		t.Errorf("Sweep over a test of dead code returned components %+v, want %+v", got, components)
	}
}

func TestSweepJudgesNoPackageAndNoFile(t *testing.T) {
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "package", kind: KindPackage})
	b.declare(handSymbol{name: "file", kind: KindFile, parent: "package"})
	b.declare(handSymbol{name: "declared", parent: "package"})
	r := b.graph().Sweep(Mode{})

	// Nothing references a file, and an import names a package at the import
	// spec rather than at the package clause, so both would be candidates of
	// every run. Their liveness is the subject of the file and package kinds.
	if want := []string{"declared reference-counting"}; !slices.Equal(b.candidates(r), want) {
		t.Errorf("Sweep over a package, a file and one declaration returned %v, want %v", b.candidates(r), want)
	}
	want := []grouped{{members: "declared", roots: "declared", falls: "declared", lines: 1}}
	if got := b.groups(r); !slices.Equal(got, want) {
		t.Errorf("Sweep over a package, a file and one declaration returned components %+v, want %+v", got, want)
	}
}

// retainedMethod is the graph the exemption rule turns on: a method an exemption
// class retains, the type its receiver names, and a private helper nothing but
// that method's body references.
func retainedMethod(t *testing.T) *graphBuilder {
	t.Helper()
	b := newGraphBuilder(t)
	b.declare(handSymbol{name: "Stringer", kind: KindType})
	b.declare(handSymbol{name: "String", kind: KindMethod, parent: "Stringer"})
	b.add("helper")
	b.ref("String", "Stringer")
	b.ref("String", "helper")
	return b
}

// retainedBy names every exemption of one sweep's retained record: the symbol
// held back and the class that held it.
func (b *graphBuilder) retainedBy(r Result) []string {
	found := make([]string, 0, len(r.Retained))
	for _, e := range r.Retained {
		found = append(found, b.named[e.ID]+" "+e.Class)
	}
	return found
}

// exemption is one exemption on a declared symbol, carrying a class spelling and
// no site, which is what a class deriving its evidence from a type relation
// records.
func (b *graphBuilder) exemption(name, class string) Exemption {
	b.sink.Helper()
	return Exemption{ID: b.id(name), Class: class}
}

func TestSweepReportsNoExemptSymbolAndHoldsWhatItReferencesLive(t *testing.T) {
	b := retainedMethod(t)
	r := b.graph().Sweep(Mode{Exempt: []Exemption{b.exemption("String", "format-verb-contract")}})

	// An exemption seeds reachability the way a mark does, because the retained
	// method is live by a mechanism the analysis cannot see and the helper its
	// body is the only reference to therefore runs as well. An exemption that
	// only withheld its own symbol would report that helper.
	if got := b.candidates(r); len(got) != 0 {
		t.Errorf("Sweep with the retained method exempt returned %v, want no candidate", got)
	}
	if set := r.LiveUnder[b.id("helper")]; !set.Has(Reachability) {
		t.Errorf("Sweep().LiveUnder[helper] holds %v, want it to hold %s", set, Reachability)
	}
	// The record holds the exemption that held a symbol back and nothing else:
	// the helper is live, and no exemption names it, so a maintainer reading the
	// record is not told the helper was retained.
	want := []string{"String format-verb-contract"}
	if got := b.retainedBy(r); !slices.Equal(got, want) {
		t.Errorf("Sweep with the retained method exempt recorded %v as retained, want %v", got, want)
	}
}

func TestSweepRecordsEveryExemptionThatHeldASymbolBackAndNoOther(t *testing.T) {
	b := newGraphBuilder(t).add("entry", "unreferenced", "alsoUnreferenced")
	b.root("entry", RootMain)
	r := b.graph().Sweep(Mode{Exempt: []Exemption{
		b.exemption("alsoUnreferenced", "enum-group"),
		b.exemption("entry", "generated-file"),
		b.exemption("unreferenced", "format-verb-contract"),
		b.exemption("unreferenced", "generated-file"),
	}})

	// The entry point is live under both relations whatever any exemption says, so
	// its exemption held nothing back and the record leaves it out; the two
	// declarations nothing references were candidates, so every exemption naming
	// one is in the record. The record reads in the order the inventory holds the
	// symbols, and two classes holding one symbol both stand, so an explanation
	// of that symbol names both.
	want := []string{
		"unreferenced format-verb-contract",
		"unreferenced generated-file",
		"alsoUnreferenced enum-group",
	}
	if got := b.retainedBy(r); !slices.Equal(got, want) {
		t.Errorf("Sweep over three exempt declarations recorded %v as retained, want %v", got, want)
	}
	if got := b.candidates(r); len(got) != 0 {
		t.Errorf("Sweep over three exempt declarations returned %v, want no candidate", got)
	}
}

func TestSweepRecordsNoExemptionNamingASymbolTheInventoryDoesNotHold(t *testing.T) {
	b := newGraphBuilder(t).add("unreferenced")
	r := b.graph().Sweep(Mode{Exempt: []Exemption{
		{ID: SymbolID("absent.go:1:1"), Class: "reflective-lookup"},
	}})

	// A class that resolved a name to a declaration outside the inventory holds
	// nothing back, so the record is empty and the declaration nothing references
	// is still reported.
	if got := b.retainedBy(r); len(got) != 0 {
		t.Errorf("Sweep over an exemption naming no symbol recorded %v as retained, want none", got)
	}
	if want := []string{"unreferenced reference-counting"}; !slices.Equal(b.candidates(r), want) {
		t.Errorf("Sweep over an exemption naming no symbol returned %v, want %v", b.candidates(r), want)
	}
}

// retainedTestDeclaration is the graph a production sweep answers differently
// once an exemption retains a test declaration: the retained declaration, the
// production declaration only it references, and the declaration below that one,
// which carries a production reference and so can only be reported under
// reachability.
func retainedTestDeclaration(t *testing.T) *graphBuilder {
	t.Helper()
	b := newGraphBuilder(t).add("calledByTheTest", "belowIt").addTest("Hook")
	b.ref("Hook", "calledByTheTest")
	b.ref("calledByTheTest", "belowIt")
	return b
}

func TestSweepUnderProductionModeSeedsAnExemptTestDeclaration(t *testing.T) {
	b := retainedTestDeclaration(t)
	r := b.graph().Sweep(Mode{
		Production: true,
		Exempt:     []Exemption{b.exemption("Hook", "linkname-cgo-asm-plugin")},
	})

	// The exemption stands for a caller the analysis cannot see, so the retained
	// test declaration seeds the closure of a production sweep and the production
	// declarations it reaches run. The declaration it references directly is still
	// counted on its production references, which are none, so it stays the
	// test-only population; the one below it carries a production reference and is
	// live under both relations rather than reported as a cascade.
	want := []verdict{{name: "calledByTheTest", relation: ReferenceCounting, testRefs: 1}}
	if got := b.verdicts(r); !slices.Equal(got, want) {
		t.Errorf("Sweep under production mode with the test declaration exempt returned %+v, want %+v", got, want)
	}
	for _, name := range []string{"Hook", "calledByTheTest", "belowIt"} {
		if set := r.LiveUnder[b.id(name)]; !set.Has(Reachability) {
			t.Errorf("Sweep().LiveUnder[%s] holds %v, want it to hold %s", name, set, Reachability)
		}
	}
}

func TestSweepUnderProductionModeKeepsNothingATestFileRootReferences(t *testing.T) {
	b := retainedTestDeclaration(t)
	b.root("Hook", RootBlank)
	r := b.graph().Sweep(Mode{Production: true})

	// A blank declaration in a test file is a root of a kind a production sweep
	// keeps, so it is live and seeds the closure; what it references it references
	// from a test file, and the mode counts none of those, so the closure stops
	// there and the declaration below is reported under reachability. A mark and
	// an exemption are the only things that widen a production closure, because
	// they stand for a mechanism outside the analysis rather than for a caller the
	// mode has already decided not to count.
	want := []verdict{
		{name: "calledByTheTest", relation: ReferenceCounting, testRefs: 1},
		{name: "belowIt", relation: Reachability, productionRefs: 1},
		{name: "Hook", relation: ReferenceCounting, testOfDeadCode: true},
	}
	if got := b.verdicts(r); !slices.Equal(got, want) {
		t.Errorf("Sweep under production mode over a blank root in a test file returned %+v, want %+v", got, want)
	}
}

func TestSweepUnderProductionModeWithoutTheExemptionReachesNothingTheTestReferences(t *testing.T) {
	b := retainedTestDeclaration(t)
	r := b.graph().Sweep(Mode{Production: true})

	// The same graph with nothing retaining the test declaration: no seed reaches
	// it, so the declaration below the one it calls is dead under reachability and
	// reported, which is the finding the exemption exists to withdraw.
	want := []verdict{
		{name: "calledByTheTest", relation: ReferenceCounting, testRefs: 1},
		{name: "belowIt", relation: Reachability, productionRefs: 1},
		{name: "Hook", relation: ReferenceCounting, testOfDeadCode: true},
	}
	if got := b.verdicts(r); !slices.Equal(got, want) {
		t.Errorf("Sweep under production mode over the same graph returned %+v, want %+v", got, want)
	}
}

func TestSweepWithoutTheExemptionReportsWhatTheRetainedMethodReferences(t *testing.T) {
	b := retainedMethod(t)

	// The same graph without the exemption: nothing seeds the closure, so the
	// helper is dead under reachability and reported.
	want := []string{"Stringer reachability", "String reference-counting", "helper reachability"}
	if got := b.candidates(b.graph().Sweep(Mode{})); !slices.Equal(got, want) {
		t.Errorf("Sweep over the retained method without the exemption returned %v, want %v", got, want)
	}
}

func TestSweepUnderProductionModeReadsTheReferencingFileAndNotTheTargets(t *testing.T) {
	b := newGraphBuilder(t).add("entry").addTest("reachedFromProduction")
	b.root("entry", RootMain)
	b.ref("entry", "reachedFromProduction")

	// Production mode drops a reference a test file made, which is decided by the
	// file the reference is written in. A production declaration referencing a
	// test declaration is therefore walked; the Go rule that a test declaration
	// is invisible to the package's production files is what makes the case
	// unreachable through a load rather than anything the mode does.
	if got := b.candidates(b.graph().Sweep(Mode{Production: true})); len(got) != 0 {
		t.Errorf("Sweep under production mode returned %v, want no candidate: the reference comes from a production file", got)
	}
}

func TestThePassesOverAModuleThatHasTestFiles(t *testing.T) {
	const pkg = "go://example.com/tested#"
	s := sweepOf(t, "tested-module.txtar", RootOptions{PublishedAPI: true})

	t.Run("every test function is a root", func(t *testing.T) {
		var found []string
		for _, root := range s.roots {
			if root.Kind == RootTest {
				found = append(found, s.byID[root.ID].Ref)
			}
		}
		slices.Sort(found)
		want := []string{
			"go://example.com/tested#TestNormalize",
			"go://example.com/tested_test#TestResolve",
		}
		if !slices.Equal(found, want) {
			t.Errorf("Roots(tested-module.txtar) named %v under %s, want %v", found, RootTest, want)
		}
	})

	t.Run("a reference a test file makes carries the test flag", func(t *testing.T) {
		// Both test files reference a declaration of the production file, and the
		// production file references one declaration of its own, so the flag
		// separates the two populations rather than standing for the whole set.
		byName := make(map[string][]bool)
		for _, ref := range s.refs {
			name := s.name(ref.To, pkg)
			byName[name] = append(byName[name], ref.Test)
		}
		cases := map[string][]bool{
			"Resolve":   {true},
			"normalize": {false, true},
		}
		for name, want := range cases {
			if got := byName[name]; !slices.Equal(got, want) {
				t.Errorf("References(tested-module.txtar) to %s carried Test %v, want %v", name, got, want)
			}
		}
	})

	t.Run("every symbol renders under the target root", func(t *testing.T) {
		for _, symbol := range s.symbols {
			path := symbol.Pos.Filename
			if filepath.IsAbs(path) || !filepath.IsLocal(filepath.FromSlash(path)) {
				t.Errorf("Symbols(tested-module.txtar)[%s].Pos.Filename = %q, want a path under the target root",
					symbol.ID, path)
			}
		}
	})

	t.Run("the sweep reports the declaration nothing references", func(t *testing.T) {
		// The published entry point and the declaration it reaches are live, the
		// test functions are roots, and the one declaration no file names is the
		// whole candidate set.
		want := []string{"orphan reference-counting"}
		if got := s.candidatesUnder(pkg, s.graph.Sweep(Mode{})); !slices.Equal(got, want) {
			t.Errorf("Sweep(tested-module.txtar) reported %v under %s, want %v", got, pkg, want)
		}
	})
}

func TestRelationStringNamesEveryRelation(t *testing.T) {
	cases := []struct {
		relation Relation
		want     string
	}{
		{ReferenceCounting, "reference-counting"},
		{Reachability, "reachability"},
		{Relation(200), "Relation(200)"},
	}
	for _, test := range cases {
		t.Run(test.want, func(t *testing.T) {
			if got := test.relation.String(); got != test.want {
				t.Errorf("Relation(%d).String() = %q, want %q", test.relation, got, test.want)
			}
		})
	}
}

// swept is one archive's load, enumeration, reference set, root set and the graph
// the sweep answers over.
type swept struct {
	graph   *Graph
	byID    map[SymbolID]Symbol
	byRef   map[string]SymbolID
	symbols []Symbol
	refs    []Reference
	roots   []Root
}

// sweepOf extracts one archive, loads it for one configuration and runs the three
// passes the sweep reads, so a verdict is measured over the production pipeline
// rather than over a graph written by hand.
func sweepOf(t *testing.T, archive string, opts RootOptions) *swept {
	t.Helper()

	dir := extract(t, archive)
	result, target := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, target, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(%s): %v", archive, err)
	}
	refs, _, err := References(result, target, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: References(%s): %v", archive, err)
	}
	roots, _, err := Roots(result, target, os.ReadFile, symbols, opts)
	if err != nil {
		t.Fatalf("Setup: Roots(%s): %v", archive, err)
	}

	s := &swept{
		graph:   New(symbols, refs, roots),
		byID:    make(map[SymbolID]Symbol, len(symbols)),
		byRef:   make(map[string]SymbolID, len(symbols)),
		symbols: symbols,
		refs:    refs,
		roots:   roots,
	}
	for _, symbol := range symbols {
		s.byID[symbol.ID] = symbol
		// A blank declaration carries its container's reference rather than one
		// of its own, so no reference names it.
		if !symbol.Blank {
			s.byRef[symbol.Ref] = symbol.ID
		}
	}
	return s
}

// id returns the identifier of the symbol one reference names.
func (s *swept) id(t *testing.T, ref string) SymbolID {
	t.Helper()
	id, held := s.byRef[ref]
	if !held {
		t.Fatalf("the enumeration holds no symbol whose reference is %s", ref)
	}
	return id
}

// name is how a failure message and a table name one symbol: its reference
// without the part every symbol of the package shares.
func (s *swept) name(id SymbolID, prefix string) string {
	return strings.TrimPrefix(s.byID[id].Ref, prefix)
}

// names names a set of symbols in the order given.
func (s *swept) names(ids []SymbolID, prefix string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, s.name(id, prefix))
	}
	return names
}

// candidatesUnder names every candidate whose reference carries prefix, each with
// the relation that found it and, where the rule admitted it, the rule.
func (s *swept) candidatesUnder(prefix string, r Result) []string {
	var found []string
	for _, c := range r.Candidates {
		if !strings.HasPrefix(s.byID[c.ID].Ref, prefix) {
			continue
		}
		line := s.name(c.ID, prefix) + " " + c.Relation.String()
		if c.TestOfDeadCode {
			line += " test-of-dead-code"
		}
		found = append(found, line)
	}
	return found
}

// groupsUnder names every component holding a symbol whose reference carries
// prefix.
func (s *swept) groupsUnder(prefix string, r Result) []grouped {
	var found []grouped
	for _, c := range r.Components {
		if !strings.HasPrefix(s.byID[c.Members[0]].Ref, prefix) {
			continue
		}
		found = append(found, grouped{
			members: strings.Join(s.names(c.Members, prefix), " "),
			roots:   strings.Join(s.names(c.Roots, prefix), " "),
			falls:   strings.Join(s.names(c.Falls, prefix), " "),
			lines:   c.DeletableLines,
		})
	}
	return found
}

func TestSweepOverTheLoadedGraphNamesTheRelationThatFoundEachCandidate(t *testing.T) {
	const pkg = "go://example.com/sweep#"
	s := sweepOf(t, "sweep.txtar", RootOptions{PublishedAPI: true})
	r := s.graph.Sweep(Mode{})

	// The published entry point is a candidate under reference counting while the
	// declaration it reaches is live under both relations, so a library's own API
	// is reported without its transitive closure becoming a dead component. The
	// pair below it is the case the relations answer differently.
	want := []string{
		"Published reference-counting",
		"unreferenced reference-counting",
		"referencedByDeadCode reachability",
	}
	if got := s.candidatesUnder(pkg, r); !slices.Equal(got, want) {
		t.Errorf("Sweep(sweep.txtar) reported %v under %s, want %v", got, pkg, want)
	}
	components := []grouped{
		{members: "Published", roots: "Published", falls: "Published", lines: 1},
		{members: "unreferenced", roots: "unreferenced", falls: "unreferenced referencedByDeadCode", lines: 2},
		{members: "referencedByDeadCode", falls: "referencedByDeadCode", lines: 1},
	}
	if got := s.groupsUnder(pkg, r); !slices.Equal(got, components) {
		t.Errorf("Sweep(sweep.txtar) returned components %+v under %s, want %+v", got, pkg, components)
	}
}

func TestSweepOverTheLoadedGraphReportsNothingUnderAMarkedDeclaration(t *testing.T) {
	const pkg = "go://example.com/sweep#"
	s := sweepOf(t, "sweep.txtar", RootOptions{PublishedAPI: true})
	r := s.graph.Sweep(Mode{Marked: []SymbolID{s.id(t, pkg+"unreferenced")}})

	// The mark holds the declaration live under both relations and seeds
	// reachability from it, so the declaration only it references is not reported
	// either.
	want := []string{"Published reference-counting"}
	if got := s.candidatesUnder(pkg, r); !slices.Equal(got, want) {
		t.Errorf("Sweep(sweep.txtar) with unreferenced marked reported %v under %s, want %v", got, pkg, want)
	}
}

func TestSweepOverTheLoadedGraphGroupsACycleAndACascade(t *testing.T) {
	s := sweepOf(t, "sweep.txtar", RootOptions{PublishedAPI: true})
	r := s.graph.Sweep(Mode{})

	cases := map[string]struct {
		prefix         string
		wantCandidates []string
		wantComponents []grouped
	}{
		"a cycle of mutually referencing declarations": {
			prefix:         "go://example.com/sweep/cycle#",
			wantCandidates: []string{"alpha reachability", "beta reachability"},
			wantComponents: []grouped{{members: "alpha beta", roots: "alpha beta", falls: "alpha beta", lines: 2}},
		},
		"one declaration reaching three further dead ones": {
			prefix: "go://example.com/sweep/cascade#",
			wantCandidates: []string{
				"head reference-counting",
				"first reachability",
				"second reachability",
				"third reachability",
			},
			wantComponents: []grouped{
				{members: "head", roots: "head", falls: "head first second third", lines: 12},
				{members: "first", falls: "first", lines: 3},
				{members: "second", falls: "second", lines: 3},
				{members: "third", falls: "third", lines: 3},
			},
		},
		"a dead type and a dead interface with their members": {
			prefix: "go://example.com/sweep/members#",
			wantCandidates: []string{
				"box reachability",
				"box.lid reachability",
				"box.side reference-counting",
				"box.open reference-counting",
				"fetcher reference-counting",
				"fetcher.fetch reference-counting",
				"fetcher.shut reference-counting",
			},
			wantComponents: []grouped{
				{members: "box box.lid box.side box.open", roots: "box", falls: "box box.lid box.side box.open", lines: 7},
				{members: "fetcher fetcher.fetch fetcher.shut", roots: "fetcher", falls: "fetcher fetcher.fetch fetcher.shut", lines: 6},
			},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := s.candidatesUnder(test.prefix, r); !slices.Equal(got, test.wantCandidates) {
				t.Errorf("Sweep(sweep.txtar) reported %v under %s, want %v", got, test.prefix, test.wantCandidates)
			}
			if got := s.groupsUnder(test.prefix, r); !slices.Equal(got, test.wantComponents) {
				t.Errorf("Sweep(sweep.txtar) returned components %+v under %s, want %+v", got, test.prefix, test.wantComponents)
			}
		})
	}
}

func TestSweepOverTheLoadedGraphAdmitsATestOfDeadCode(t *testing.T) {
	const pkg = "go://example.com/sweep/subject#"
	s := sweepOf(t, "sweep.txtar", RootOptions{PublishedAPI: true})
	r := s.graph.Sweep(Mode{Production: true})

	// The rule needs a mode that counts no test reference, because a test's own
	// reference is what would otherwise hold its subject live. The test that
	// references one live declaration is not reported whatever the number of dead
	// ones it also references.
	want := []string{
		"deadOne reference-counting",
		"deadTwo reference-counting",
		"TestDeadOnly reference-counting test-of-dead-code",
	}
	if got := s.candidatesUnder(pkg, r); !slices.Equal(got, want) {
		t.Errorf("Sweep(sweep.txtar) under production mode reported %v under %s, want %v", got, pkg, want)
	}
	components := []grouped{{
		members: "deadOne deadTwo TestDeadOnly",
		roots:   "deadOne deadTwo TestDeadOnly",
		falls:   "deadOne deadTwo TestDeadOnly",
		lines:   7,
	}}
	if got := s.groupsUnder(pkg, r); !slices.Equal(got, components) {
		t.Errorf("Sweep(sweep.txtar) under production mode returned components %+v under %s, want %+v", got, pkg, components)
	}
}

// verdictOf reports whether one symbol is live under reachability and, where the
// sweep reported it, the relation that found it.
func (s *swept) verdictOf(t *testing.T, r Result, ref string) (reachable bool, candidate string) {
	t.Helper()
	id := s.id(t, ref)
	for _, c := range r.Candidates {
		if c.ID == id {
			candidate = c.Relation.String()
		}
	}
	return r.LiveUnder[id].Has(Reachability), candidate
}

func TestSweepOverEveryRootClassHoldsTheRootLiveAndReportsItsLookAlike(t *testing.T) {
	const pkg = "go://example.com/roots#"
	s := sweepOf(t, "roots.txtar", RootOptions{PublishedAPI: true})
	r := s.graph.Sweep(Mode{})

	// One pair per detected root class: the declaration the class roots, and the
	// declaration beside it that carries everything about it except what makes it
	// a root. The root is live under reachability and the look-alike is reported,
	// with one exception the fixture's shape decides rather than the sweep: the
	// unexported function paired with the published one is referenced BY it, so
	// both relations hold it live.
	cases := []struct {
		ref       string
		candidate string
		reachable bool
	}{
		{ref: "go://example.com/roots/cmd/app#main", reachable: true},
		{ref: "go://example.com/roots/notmain#main", candidate: "reference-counting"},

		{ref: pkg + "init", reachable: true},

		{ref: pkg + "TestMain", reachable: true},
		{ref: pkg + "TestPublished", reachable: true},
		{ref: pkg + "BenchmarkPublished", reachable: true},
		{ref: pkg + "FuzzPublished", reachable: true},
		{ref: pkg + "ExampleWithOutput", reachable: true},
		{ref: "go://example.com/roots_test#TestExternal", reachable: true},
		{ref: pkg + "ExampleTakesAnArgument", candidate: "reference-counting"},
		{ref: pkg + "TestingLower", candidate: "reference-counting"},
		{ref: pkg + "HelperExported", candidate: "reference-counting"},

		{ref: pkg + "pushed", reachable: true},
		{ref: pkg + "aliased", reachable: true},
		{ref: pkg + "counted", reachable: true},
		{ref: pkg + "notLinked", candidate: "reference-counting"},
		{ref: pkg + "unsafeless", candidate: "reference-counting"},

		// The file importing "C" is excluded from the load, so the cgo-export
		// class holds no symbol and its look-alike is a published root nothing
		// references.
		{ref: "go://example.com/roots/cgoexport#Plain", candidate: "reference-counting", reachable: true},

		{ref: pkg + "Sentinel", reachable: true},
		{ref: pkg + "Named", candidate: "reference-counting", reachable: true},

		{ref: pkg + "Published", reachable: true},
		{ref: pkg + "unpublished", reachable: true},
		{ref: pkg + "Catalog.Resolve", candidate: "reference-counting", reachable: true},
		{ref: pkg + "Catalog.fill", candidate: "reference-counting"},
		{ref: pkg + "Catalog.size", candidate: "reachability"},
		{ref: pkg + "Fetcher.Fetch", candidate: "reference-counting", reachable: true},
		{ref: pkg + "Fetcher.fetch", candidate: "reference-counting"},
		{ref: "go://example.com/roots/cmd/app#Exported", candidate: "reference-counting"},
		{ref: "go://example.com/roots/internal/hidden#Exported", candidate: "reference-counting"},
		{ref: "go://example.com/roots_test#ExportedFromAnExternalTestPackage", candidate: "reference-counting"},
	}
	for _, test := range cases {
		t.Run(test.ref, func(t *testing.T) {
			reachable, candidate := s.verdictOf(t, r, test.ref)
			if reachable != test.reachable {
				t.Errorf("Sweep(roots.txtar).LiveUnder[%s].Has(reachability) = %t, want %t", test.ref, reachable, test.reachable)
			}
			if candidate != test.candidate {
				t.Errorf("Sweep(roots.txtar) reported %s under %q, want %q", test.ref, candidate, test.candidate)
			}
		})
	}
}

func TestSweepWithoutThePublishedSeedReportsEveryExportedSymbolNothingReferences(t *testing.T) {
	const pkg = "go://example.com/roots#"
	s := sweepOf(t, "roots.txtar", RootOptions{})
	r := s.graph.Sweep(Mode{})

	// The published API is the one seed the target's kind decides, and it is the
	// one root kind that changes a verdict under reachability alone: withdrawing
	// it leaves every reference-counting verdict of the fixture as it was.
	cases := []struct {
		ref       string
		candidate string
		reachable bool
	}{
		{ref: pkg + "Catalog.Resolve", candidate: "reference-counting"},
		{ref: pkg + "Fetcher.Fetch", candidate: "reference-counting"},
		{ref: pkg + "Named", candidate: "reference-counting"},
		{ref: "go://example.com/roots/cgoexport#Plain", candidate: "reference-counting"},
		// Nothing withdraws a verdict the test binary's own roots decide.
		{ref: pkg + "Published", reachable: true},
		{ref: pkg + "unpublished", reachable: true},
	}
	for _, test := range cases {
		t.Run(test.ref, func(t *testing.T) {
			reachable, candidate := s.verdictOf(t, r, test.ref)
			if reachable != test.reachable {
				t.Errorf("Sweep(roots.txtar) without the published seed: LiveUnder[%s].Has(reachability) = %t, want %t",
					test.ref, reachable, test.reachable)
			}
			if candidate != test.candidate {
				t.Errorf("Sweep(roots.txtar) without the published seed reported %s under %q, want %q",
					test.ref, candidate, test.candidate)
			}
		})
	}
}

func TestSweepOverAConfiguredRootHoldsItLiveAndReportsTheDeclarationsNothingNames(t *testing.T) {
	const pkg = "go://example.com/patterns#"
	s := sweepOf(t, "root-patterns.txtar", RootOptions{Patterns: []string{pkg + "Alpha"}})
	r := s.graph.Sweep(Mode{})

	// A configured root is the maintainer's assertion of a caller, so it is live
	// under both relations, and reporting it would contradict the configuration
	// that exists to prevent the report.
	want := []string{"Box reachability", "Box.Lid reachability", "Box.Open reference-counting"}
	if got := s.candidatesUnder(pkg, r); !slices.Equal(got, want) {
		t.Errorf("Sweep(root-patterns.txtar) reported %v under %s, want %v", got, pkg, want)
	}
	components := []grouped{{
		members: "Box Box.Lid Box.Open",
		roots:   "Box",
		falls:   "Box Box.Lid Box.Open",
		lines:   5,
	}}
	if got := s.groupsUnder(pkg, r); !slices.Equal(got, components) {
		t.Errorf("Sweep(root-patterns.txtar) returned components %+v under %s, want %+v", got, pkg, components)
	}
	if got := s.candidatesUnder("go://example.com/patterns/inner#", r); !slices.Equal(got, []string{"Gamma reference-counting"}) {
		t.Errorf("Sweep(root-patterns.txtar) reported %v under the nested package, want [Gamma reference-counting]", got)
	}
}

func TestSweepOverAPackageWithATestVariantLeavesAFunctionOutOfTheTypesComponent(t *testing.T) {
	const pkg = "go://example.com/receivers#"
	s := sweepOf(t, "receivers.txtar", RootOptions{})
	r := s.graph.Sweep(Mode{Production: true})

	// A package-level function's container is the package, which no sweep judges,
	// so the function is a component of its own however dead the type beside it
	// is. A receiver recorded against the wrong symbol puts the function inside
	// the type's component, and a report then tells a maintainer that deleting the
	// type removes the function.
	want := []grouped{
		{
			members: "Catalog Catalog.entries Catalog.Resolve TestResolve",
			roots:   "Catalog TestResolve",
			falls:   "Catalog Catalog.entries Catalog.Resolve TestResolve",
			lines:   11,
		},
		{members: "Normalize", roots: "Normalize", falls: "Normalize", lines: 1},
	}
	if got := s.groupsUnder(pkg, r); !slices.Equal(got, want) {
		t.Errorf("Sweep(receivers.txtar) returned components %+v under %s, want %+v", got, pkg, want)
	}
}
