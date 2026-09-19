package exempt

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	spec "github.com/cplieger/deadset-spec"
	"golang.org/x/tools/txtar"
)

// retainedBy runs the class over one input and returns the exemptions it produced.
func retainedBy(t *testing.T, in *Input, subject string) []graph.Exemption {
	t.Helper()
	found, err := InterfaceSatisfactionDetector(in)
	if err != nil {
		t.Fatalf("InterfaceSatisfactionDetector(%s) error: %v", subject, err)
	}
	return found
}

// retainedSet is the set of symbols one exemption slice names.
func retainedSet(exemptions []graph.Exemption) map[graph.SymbolID]bool {
	set := make(map[graph.SymbolID]bool, len(exemptions))
	for i := range exemptions {
		set[exemptions[i].ID] = true
	}
	return set
}

// refsOf names each symbol of a set by its stable reference, sorted, for a
// failure message.
func refsOf(in *Input, set map[graph.SymbolID]bool) []string {
	var named []string
	for i := range in.Symbols {
		if set[in.Symbols[i].ID] {
			named = append(named, in.Symbols[i].Ref)
		}
	}
	slices.Sort(named)
	return named
}

// symbolAtLine returns the symbol of the inventory declared on one line of one
// target-relative file.
func symbolAtLine(t *testing.T, in *Input, file string, line int) graph.Symbol {
	t.Helper()
	for i := range in.Symbols {
		s := &in.Symbols[i]
		if s.Pos.Filename == file && s.Pos.Line == line {
			return *s
		}
	}
	t.Fatalf("Setup: the inventory holds no symbol at %s:%d", file, line)
	return graph.Symbol{}
}

// symbolRef returns the symbol of the inventory carrying one stable reference.
// A test names its subject this way rather than by line, so a fixture edit above
// the subject does not move the expectation.
func symbolRef(t *testing.T, in *Input, ref string) graph.Symbol {
	t.Helper()
	for i := range in.Symbols {
		if in.Symbols[i].Ref == ref {
			return in.Symbols[i]
		}
	}
	t.Fatalf("Setup: the inventory holds no symbol referenced %s", ref)
	return graph.Symbol{}
}

// sweepWith indexes the graph of one input and sweeps it once with the given
// exemptions held back. The target of every fixture here is a library, so its
// published API is rooted.
func sweepWith(t *testing.T, in *Input, subject string, held []graph.Exemption) graph.Result {
	t.Helper()
	references, _, err := graph.References(in.Result, in.Root, in.Read, in.Symbols)
	if err != nil {
		t.Fatalf("Setup: graph.References(%s): %v", subject, err)
	}
	roots, _, err := graph.Roots(in.Result, in.Root, in.Read, in.Symbols, graph.RootOptions{PublishedAPI: true})
	if err != nil {
		t.Fatalf("Setup: graph.Roots(%s): %v", subject, err)
	}
	return graph.New(in.Symbols, references, roots).Sweep(graph.SweepInput{Exempt: held})
}

// candidateOf returns the candidate one sweep reported for a symbol.
func candidateOf(r graph.Result, id graph.SymbolID) (graph.Candidate, bool) {
	for _, c := range r.Candidates {
		if c.ID == id {
			return c, true
		}
	}
	return graph.Candidate{}, false
}

func TestInterfaceSatisfactionRetainsTheMethodAConversionReaches(t *testing.T) {
	in := inputOf(t, "unconverted-implementation.txtar", Options{})
	found := retainedBy(t, in, "unconverted-implementation.txtar")

	converted := symbolRef(t, in, "go://example.com/conservative#Converted.Count")
	unconverted := symbolRef(t, in, "go://example.com/conservative#Unconverted.Count")
	set := retainedSet(found)

	if !set[converted.ID] {
		t.Errorf("InterfaceSatisfactionDetector(unconverted-implementation.txtar) retained %v, want it to hold %s",
			refsOf(in, set), converted.Ref)
	}
	if set[unconverted.ID] {
		t.Errorf("InterfaceSatisfactionDetector(unconverted-implementation.txtar) retained %s, want it held back only for a type a conversion reaches",
			unconverted.Ref)
	}
}

func TestInterfaceSatisfactionNamesTheInterfaceAndTheSiteItWasFoundAt(t *testing.T) {
	in := inputOf(t, "unconverted-implementation.txtar", Options{})
	found := retainedBy(t, in, "unconverted-implementation.txtar")

	if len(found) != 1 {
		t.Fatalf("InterfaceSatisfactionDetector(unconverted-implementation.txtar) returned %d exemptions, want 1", len(found))
	}

	// The site is the position of the operand that reaches the parameter, which is
	// the composite literal on the line Sum is declared on.
	got := found[0]
	want := graph.Exemption{
		ID:     symbolRef(t, in, "go://example.com/conservative#Converted.Count").ID,
		Class:  "interface-satisfaction",
		Site:   token.Position{Filename: "counters.go", Offset: got.Site.Offset, Line: 24, Column: 31},
		Detail: "satisfies conservative.Counter",
	}
	if got != want {
		t.Errorf("InterfaceSatisfactionDetector(unconverted-implementation.txtar) = %+v, want %+v", got, want)
	}
}

func TestInterfaceSatisfactionRetainsNothingWhereNoConversionReachesTheType(t *testing.T) {
	in := inputOf(t, "no-conversion.txtar", Options{})
	found := retainedBy(t, in, "no-conversion.txtar")
	if len(found) != 0 {
		t.Fatalf("InterfaceSatisfactionDetector(no-conversion.txtar) = %+v, want no exemption", found)
	}

	// With nothing retained, the method the removed conversion used to reach is
	// reported, and so is the exported function nothing references.
	write := symbolRef(t, in, "go://example.com/target#Sink.Write")
	dead := symbolRef(t, in, "go://example.com/target#DeadExport")
	r := sweepWith(t, in, "no-conversion.txtar", found)
	for _, want := range []graph.Symbol{write, dead} {
		got, reported := candidateOf(r, want.ID)
		if !reported {
			t.Errorf("Sweep(no-conversion.txtar) reported %v, want it to report %s",
				reportedRefs(in, r), want.Ref)
			continue
		}
		if got.Relation != graph.ReferenceCounting {
			t.Errorf("Sweep(no-conversion.txtar) reported %s under %s, want reference-counting",
				want.Ref, got.Relation)
		}
	}
}

func TestInterfaceSatisfactionRetainsEveryMethodOfAMultiMethodInterfaceAtEverySite(t *testing.T) {
	in := inputOf(t, "conversion-shapes.txtar", Options{})
	found := retainedBy(t, in, "conversion-shapes.txtar")

	// Counter declares one method, and two types answer it, so every exemption
	// names one of the two Count methods and each is held at more than one site.
	value := symbolRef(t, in, "go://example.com/shapes#Holder.Count")
	pointer := symbolRef(t, in, "go://example.com/shapes#Pointed.Count")
	sites := make(map[graph.SymbolID]int)
	for i := range found {
		sites[found[i].ID]++
		if found[i].ID != value.ID && found[i].ID != pointer.ID {
			t.Errorf("InterfaceSatisfactionDetector(conversion-shapes.txtar) retained %s, want only the two Count methods",
				found[i].ID)
		}
	}
	for _, want := range []graph.Symbol{value, pointer} {
		if sites[want.ID] < 2 {
			t.Errorf("InterfaceSatisfactionDetector(conversion-shapes.txtar) retained %s at %d sites, want it retained once per conversion",
				want.Ref, sites[want.ID])
		}
	}
}

func TestInterfaceSatisfactionRetainsStrictlyLessThanTheConservativeRule(t *testing.T) {
	in := inputOf(t, "unconverted-implementation.txtar", Options{})
	narrow := retainedSet(retainedBy(t, in, "unconverted-implementation.txtar"))
	conservative := conservativeRetained(t, in)

	for id := range narrow {
		if !conservative[id] {
			t.Errorf("the conversion set retained %s and the conservative rule did not; want the conversion set to be a subset", id)
		}
	}
	if len(narrow) >= len(conservative) {
		t.Fatalf("the conversion set retained %v and the conservative rule retained %v; want the conversion set to retain strictly less",
			refsOf(in, narrow), refsOf(in, conservative))
	}

	// The difference is the whole point of the class: a type that answers an
	// interface's method set and reaches no position of that interface.
	unconverted := symbolRef(t, in, "go://example.com/conservative#Unconverted.Count")
	if narrow[unconverted.ID] || !conservative[unconverted.ID] {
		t.Errorf("the difference between the two rules is %v, want it to be %s",
			differenceOf(in, conservative, narrow), unconverted.Ref)
	}
}

// reportedRefs names every candidate one sweep reported, for a failure message.
func reportedRefs(in *Input, r graph.Result) []string {
	set := make(map[graph.SymbolID]bool, len(r.Candidates))
	for _, c := range r.Candidates {
		set[c.ID] = true
	}
	return refsOf(in, set)
}

// differenceOf names the symbols the first set holds and the second does not.
func differenceOf(in *Input, wide, narrow map[graph.SymbolID]bool) []string {
	only := make(map[graph.SymbolID]bool)
	for id := range wide {
		if !narrow[id] {
			only[id] = true
		}
	}
	return refsOf(in, only)
}

// conservativeRetained is the retained set of the conservative rule this class
// narrows: every method of every named type of the program that answers any
// interface the program mentions, on either method set, whether or not a value of
// the type ever reaches that interface. It is the test's oracle for what the
// conversion set removes and no production code implements it.
func conservativeRetained(t *testing.T, in *Input) map[graph.SymbolID]bool {
	t.Helper()
	interfaces := mentionedInterfaces(in)
	set := make(map[graph.SymbolID]bool)
	for _, named := range namedTypes(in) {
		for _, subject := range []types.Type{named, types.NewPointer(named)} {
			methods := types.NewMethodSet(subject)
			for _, iface := range interfaces {
				if iface.NumMethods() == 0 || !types.Implements(subject, iface) {
					continue
				}
				for m := range iface.Methods() {
					sel := methods.Lookup(m.Pkg(), m.Name())
					if sel == nil {
						continue
					}
					if id, held := in.Resolve.Object(sel.Obj()); held {
						set[id] = true
					}
				}
			}
		}
	}
	return set
}

// namedTypes lists every defined non-interface type the loaded packages declare.
func namedTypes(in *Input) []*types.Named {
	var declared []*types.Named
	for _, p := range in.Result.Packages {
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			if _, iface := named.Underlying().(*types.Interface); !iface {
				declared = append(declared, named)
			}
		}
	}
	return declared
}

// mentionedInterfaces lists every interface type the loaded syntax names, which is
// the population the conservative rule tests every type against.
func mentionedInterfaces(in *Input) []*types.Interface {
	var mentioned []*types.Interface
	for _, p := range in.Result.Packages {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				e, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				tv, recorded := p.TypesInfo.Types[e]
				if !recorded || !tv.IsType() {
					return true
				}
				if iface, ok := tv.Type.Underlying().(*types.Interface); ok {
					mentioned = append(mentioned, iface)
				}
				return true
			})
		}
	}
	return mentioned
}

// corpusExpectation is the part of one corpus expectation file this test reads.
type corpusExpectation struct {
	Name       string      `json:"name"`
	TargetKind string      `json:"target_kind"`
	Consumers  []string    `json:"consumers"`
	Expect     []corpusRow `json:"expect"`
}

// corpusRow is one subject of one corpus expectation.
type corpusRow struct {
	Symbol     string   `json:"symbol"`
	Report     string   `json:"report"`
	Relation   string   `json:"liveness_relation"`
	RetainedBy []string `json:"retained_by"`
}

// corpusManifest maps one fixture's logical symbol names to positions.
type corpusManifest struct {
	Symbols map[string]corpusSite `json:"symbols"`
}

// corpusSite is where one logical symbol of a fixture is written.
type corpusSite struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

func TestInterfaceSatisfactionAnswersTheCorpusConversionFixture(t *testing.T) {
	const fixture = "interface-satisfaction-conversion"
	expectation := corpusExpectationOf(t, fixture)
	archive := corpusArchiveOf(t, fixture)
	manifest := corpusManifestOf(t, archive)

	// The fixture declares a consumer module and the loader refuses a scope that
	// names one, so the target section is loaded alone. Every expectation row of
	// this fixture is decided inside the target.
	in := inputForDir(t, writeUnder(t, archive.Files, "target/"), fixture)
	found := retainedBy(t, in, fixture)
	retained := retainedSet(found)
	r := sweepWith(t, in, fixture, found)

	for _, row := range expectation.Expect {
		t.Run(row.Symbol, func(t *testing.T) {
			subject := corpusSymbol(t, in, manifest, row.Symbol)
			candidate, reported := candidateOf(r, subject.ID)

			if row.Report == "none" {
				if reported {
					t.Errorf("Sweep(%s) reported %s under %s, want it held back by %v",
						fixture, row.Symbol, candidate.Relation, row.RetainedBy)
				}
				assertRetainedBy(t, fixture, row, found, retained, subject)
				return
			}
			if !reported {
				t.Fatalf("Sweep(%s) reported %v, want it to report %s as %s",
					fixture, reportedRefs(in, r), row.Symbol, row.Report)
			}
			if candidate.Relation.String() != row.Relation {
				t.Errorf("Sweep(%s) reported %s under %s, want %s",
					fixture, row.Symbol, candidate.Relation, row.Relation)
			}
		})
	}
}

// assertRetainedBy checks that one expectation's classes are the classes the
// detector recorded for its subject.
func assertRetainedBy(t *testing.T, fixture string, row corpusRow, found []graph.Exemption,
	retained map[graph.SymbolID]bool, subject graph.Symbol,
) {
	t.Helper()
	if !slices.Contains(row.RetainedBy, string(InterfaceSatisfaction)) {
		return
	}
	if !retained[subject.ID] {
		t.Fatalf("InterfaceSatisfactionDetector(%s) retained nothing at %s, want %s held by %v",
			fixture, subject.Ref, row.Symbol, row.RetainedBy)
	}
	for i := range found {
		if found[i].ID == subject.ID && found[i].Class != string(InterfaceSatisfaction) {
			t.Errorf("InterfaceSatisfactionDetector(%s) recorded the class %q for %s, want %q",
				fixture, found[i].Class, row.Symbol, InterfaceSatisfaction)
		}
	}
}

// corpusSymbol resolves one logical symbol name of a corpus fixture to the symbol
// of the inventory at the position the manifest names. A manifest path is relative
// to the rendering root and the target is loaded on its own, so the target
// directory is stripped from it.
func corpusSymbol(t *testing.T, in *Input, manifest corpusManifest, name string) graph.Symbol {
	t.Helper()
	at, held := manifest.Symbols[name]
	if !held || at.File == "" || at.Line == 0 {
		t.Fatalf("Setup: the fixture manifest carries no file and line for %s", name)
	}
	file, inside := relativeTo(at.File, "target/")
	if !inside {
		t.Fatalf("Setup: %s is outside the target of the fixture", at.File)
	}
	return symbolAtLine(t, in, file, at.Line)
}

// corpusArchiveOf reads one corpus fixture's Go rendering from the pinned corpus.
func corpusArchiveOf(t *testing.T, fixture string) *txtar.Archive {
	t.Helper()
	data, err := spec.Corpus.ReadFile("corpus/fixtures/" + fixture + "/go.txtar")
	if err != nil {
		t.Fatalf("Setup: read the Go rendering of %s: %v", fixture, err)
	}
	return txtar.Parse(data)
}

// corpusExpectationOf reads one corpus fixture's expectation file.
func corpusExpectationOf(t *testing.T, fixture string) corpusExpectation {
	t.Helper()
	data, err := spec.Corpus.ReadFile("corpus/fixtures/" + fixture + "/expect.json")
	if err != nil {
		t.Fatalf("Setup: read the expectation of %s: %v", fixture, err)
	}
	var expectation corpusExpectation
	if err := json.Unmarshal(data, &expectation); err != nil {
		t.Fatalf("Setup: decode the expectation of %s: %v", fixture, err)
	}
	if len(expectation.Expect) == 0 {
		t.Fatalf("Setup: the expectation of %s carries no subject", fixture)
	}
	return expectation
}

// corpusManifestOf reads one fixture rendering's manifest section.
func corpusManifestOf(t *testing.T, archive *txtar.Archive) corpusManifest {
	t.Helper()
	for _, f := range archive.Files {
		if f.Name != "fixture.json" {
			continue
		}
		var manifest corpusManifest
		if err := json.Unmarshal(f.Data, &manifest); err != nil {
			t.Fatalf("Setup: decode the fixture manifest: %v", err)
		}
		return manifest
	}
	t.Fatalf("Setup: the rendering carries no fixture.json")
	return corpusManifest{}
}
