package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	spec "github.com/cplieger/deadset-spec"
	"golang.org/x/tools/txtar"
)

// consumerFixture is the corpus fixture whose two modules are a target and a
// consumer of it, and the path its Go rendering lives at.
const (
	corpusFixture  = "unused-exported-consumer"
	corpusArchive  = "corpus/fixtures/" + corpusFixture + "/go.txtar"
	fixtureTarget  = "target"
	fixtureConsume = "consumer"
)

// extractArchive writes one txtar archive's files into a temporary directory and
// returns that directory. The archive comes from the caller rather than from
// testdata, so the published corpus and this package's own fixtures extract
// through one function.
func extractArchive(t *testing.T, name string, body []byte) string {
	t.Helper()
	parsed := txtar.Parse(body)
	dir := t.TempDir()
	for _, f := range parsed.Files {
		at := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(at), 0o750); err != nil {
			t.Fatalf("Setup: create %s of %s: %v", filepath.Dir(at), name, err)
		}
		if err := os.WriteFile(at, f.Data, 0o600); err != nil {
			t.Fatalf("Setup: write %s of %s: %v", at, name, err)
		}
	}
	return dir
}

// loadTwoModules loads the target and the named consumer directories of an
// extracted archive under one configuration, and returns the result with the
// target root.
func loadTwoModules(t *testing.T, dir string, consumers ...string) (*load.Result, string) {
	t.Helper()
	doc := scope.Document{Target: scope.Module{Path: filepath.Join(dir, fixtureTarget)}}
	for _, name := range consumers {
		doc.Consumers = append(doc.Consumers,
			scope.Module{Path: filepath.Join(dir, name)})
	}
	result, err := load.Load(t.Context(), doc, load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Setup: load %s with consumers %v: %v", dir, consumers, err)
	}
	return &result, doc.Target.Path
}

// passes runs the enumeration, the reference pass and the root detection over one
// loaded configuration, which is the order the analysis runs them in.
func passes(t *testing.T, result *load.Result, root string) ([]Symbol, []Reference, []Root) {
	t.Helper()
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols() error: %v", err)
	}
	refs, _, err := References(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("References() error: %v", err)
	}
	roots, _, err := Roots(result, root, os.ReadFile, symbols, RootOptions{})
	if err != nil {
		t.Fatalf("Roots() error: %v", err)
	}
	return symbols, refs, roots
}

// named maps each symbol's identifier to the name a test reads it by.
func named(symbols []Symbol) map[SymbolID]string {
	names := make(map[SymbolID]string, len(symbols))
	for _, s := range symbols {
		names[s.ID] = s.Name
	}
	return names
}

// candidateNames lists the name of every candidate of one sweep, in the order the
// sweep returned them.
func candidateNames(names map[SymbolID]string, r Result) []string {
	found := make([]string, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		found = append(found, names[c.ID])
	}
	return found
}

func TestReferencesRecordsWhatALoadedConsumerReferences(t *testing.T) {
	dir := extractArchive(t, "consumer.txtar", mustReadTestdata(t, "consumer.txtar"))
	result, root := loadTwoModules(t, dir, fixtureConsume)
	symbols, refs, _ := passes(t, result, root)
	names := named(symbols)

	type made struct {
		consumer string
		from     string
		to       string
		file     string
		test     bool
	}
	var fromConsumer []made
	for _, r := range refs {
		if r.Consumer == "" {
			continue
		}
		fromConsumer = append(fromConsumer, made{
			consumer: r.Consumer,
			from:     names[r.From],
			to:       names[r.To],
			file:     r.Pos.Filename,
			test:     r.Test,
		})
	}

	// A consumer's reference carries the consumer's module path, no referencing
	// declaration, because the consumer's declarations are not the inventory's, and
	// a position relative to the consumer's own module root. Its Test flag is the
	// language's own rule applied to the consumer's file, as it is for the target.
	want := []made{
		{consumer: "example.com/consumer", to: "UsedByConsumer", file: "main.go"},
		{consumer: "example.com/consumer", to: "UsedByConsumerTest", file: "main_test.go", test: true},
	}
	if !slices.Equal(fromConsumer, want) {
		t.Errorf("References() recorded %+v from the consumer, want %+v", fromConsumer, want)
	}

	// The target's own references carry no consumer and stay relative to the
	// target root, so nothing of the target's rendering moved.
	for _, r := range refs {
		if r.Consumer != "" {
			continue
		}
		if r.From == "" {
			t.Errorf("References() recorded a reference to %s with no consumer and no referencing declaration", names[r.To])
		}
		if strings.HasPrefix(r.Pos.Filename, "..") {
			t.Errorf("References() rendered the target's own reference at %s, want a path inside the target", r.Pos.Filename)
		}
	}
}

func TestSweepHoldsWhatAConsumerReferencesLiveAndReportsTheRest(t *testing.T) {
	dir := extractArchive(t, "consumer.txtar", mustReadTestdata(t, "consumer.txtar"))

	cases := map[string]struct {
		consumers []string
		want      []string
	}{
		// With no consumer loaded, every export is unreferenced: the target is a
		// library whose callers are not in the graph, which is the answer a
		// consumer-less run gives. helper is reported too, under reachability: the
		// one declaration that references it is itself unreached.
		"no consumer": {
			want: []string{"UsedByConsumer", "UsedByConsumerTest", "DeadExport", "helper"},
		},
		// With the consumer loaded, the two exports it references are live and only
		// the one nothing references is reported. helper is live because the
		// consumer's call seeds the closure through UsedByConsumer.
		"the consumer": {
			consumers: []string{fixtureConsume},
			want:      []string{"DeadExport"},
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			result, root := loadTwoModules(t, dir, test.consumers...)
			symbols, refs, roots := passes(t, result, root)
			r := New(symbols, refs, roots).Sweep(Mode{})

			if got := candidateNames(named(symbols), r); !slices.Equal(got, test.want) {
				t.Errorf("Sweep(consumers %v) reported %v, want %v", test.consumers, got, test.want)
			}
		})
	}
}

func TestSweepHoldsAConsumersCallLiveUnderBothRelations(t *testing.T) {
	b := newGraphBuilder(t).add("called", "helper", "dead")
	b.ref("called", "helper")
	b.refFromConsumer(handConsumer, "called", false)
	r := b.graph().Sweep(Mode{})

	// A consumer's call is an actual caller outside the target, so it holds its
	// symbol live under both relations and seeds the closure from it, exactly as a
	// root of an actual caller does.
	for _, name := range []string{"called", "helper"} {
		set := r.LiveUnder[b.id(name)]
		if !set.Has(ReferenceCounting) || !set.Has(Reachability) {
			t.Errorf("LiveUnder[%s] holds %v, want both relations", name, relationsOf(set))
		}
	}
	if got, want := b.candidates(r), []string{"dead " + ReferenceCounting.String()}; !slices.Equal(got, want) {
		t.Errorf("Sweep reported %v, want %v", got, want)
	}
}

func TestSweepSeedsNothingFromAnOutsideReferenceNoConsumerMade(t *testing.T) {
	b := newGraphBuilder(t).add("referenced", "helper")
	b.ref("referenced", "helper")
	b.refFromOutside("referenced")
	r := b.graph().Sweep(Mode{})

	// A reference from a declaration the inventory does not hold and no consumer
	// made counts, so the symbol is live under reference counting; it names no
	// caller, so it seeds nothing and what it references is dead under
	// reachability. The contrast with a consumer's reference is the whole of what
	// loading a consumer buys.
	if set := r.LiveUnder[b.id("referenced")]; !set.Has(ReferenceCounting) || set.Has(Reachability) {
		t.Errorf("LiveUnder[referenced] holds %v, want reference counting alone", relationsOf(set))
	}
	want := []string{
		"referenced " + Reachability.String(),
		"helper " + Reachability.String(),
	}
	if got := b.candidates(r); !slices.Equal(got, want) {
		t.Errorf("Sweep reported %v, want %v", got, want)
	}
}

func TestSweepClassifiesAConsumersTestReferenceByTheMode(t *testing.T) {
	cases := map[string]struct {
		mode Mode
		// want is the candidate set, and the counts the candidate carries where it
		// is one.
		want           []string
		wantProduction int
		wantTest       int
	}{
		"every reference counted": {
			mode:     Mode{},
			want:     nil,
			wantTest: 1,
		},
		"a production sweep counts a consumer's test reference as a test reference": {
			mode:     Mode{Production: true},
			want:     []string{"called " + ReferenceCounting.String()},
			wantTest: 1,
		},
		"a production sweep counting a consumer's tests as production": {
			mode:           Mode{Production: true, ConsumerTestsProduction: true},
			want:           nil,
			wantProduction: 1,
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			b := newGraphBuilder(t).add("called")
			b.refFromConsumer(handConsumer, "called", true)
			r := b.graph().Sweep(test.mode)

			if got := b.candidates(r); !slices.Equal(got, test.want) {
				t.Errorf("Sweep(%+v) reported %v, want %v", test.mode, got, test.want)
			}
			for _, c := range r.Candidates {
				if c.ProductionRefs != test.wantProduction || c.TestRefs != test.wantTest {
					t.Errorf("Sweep(%+v) candidate %s counts %d production and %d test references, want %d and %d",
						test.mode, b.named[c.ID], c.ProductionRefs, c.TestRefs, test.wantProduction, test.wantTest)
				}
			}
		})
	}
}

func TestSweepCountsEachConsumersReferenceOfOneSymbol(t *testing.T) {
	b := newGraphBuilder(t).add("called")
	b.refFromConsumer(handConsumer, "called", false)
	b.refFromConsumer(handOtherConsumer, "called", false)
	g := b.graph()

	// Two consumers referencing one symbol are two references, which is what a
	// kind reading the counts reads.
	if got := g.made[g.at(b.id("called"))].production; got != 2 {
		t.Errorf("the graph counts %d production references to called, want 2", got)
	}
	if got := b.candidates(g.Sweep(Mode{})); len(got) != 0 {
		t.Errorf("Sweep reported %v, want nothing", got)
	}
}

func TestReferencesAndSweepOverTheCorpusConsumerFixture(t *testing.T) {
	body, err := spec.Corpus.ReadFile(corpusArchive)
	if err != nil {
		t.Fatalf("Setup: read %s from the corpus: %v", corpusArchive, err)
	}
	dir := extractArchive(t, corpusFixture, body)
	result, root := loadTwoModules(t, dir, fixtureConsume)
	symbols, refs, roots := passes(t, result, root)
	names := named(symbols)

	r := New(symbols, refs, roots).Sweep(Mode{})

	// The fixture's expectation: the export nothing references is reported under
	// reference counting, and the export the consumer references is not reported at
	// all.
	if got, want := candidateNames(names, r), []string{"DeadExport"}; !slices.Equal(got, want) {
		t.Errorf("Sweep over the %s fixture reported %v, want %v", corpusFixture, got, want)
	}
	for _, c := range r.Candidates {
		if names[c.ID] == "DeadExport" && c.Relation != ReferenceCounting {
			t.Errorf("DeadExport was found under %s, want %s", c.Relation, ReferenceCounting)
		}
	}

	// The consumer's reference is recorded against the symbol the fixture says it
	// uses, and the fixture's manifest is what says which line declares it.
	found := ""
	for _, ref := range refs {
		if ref.Consumer != "" && names[ref.To] == "UsedByConsumer" {
			found = ref.Consumer
		}
	}
	if found != "example.com/consumer" {
		t.Errorf("the reference to UsedByConsumer carries consumer %q, want %q", found, "example.com/consumer")
	}
	if set := r.LiveUnder[idOf(names, "UsedByConsumer")]; !set.Has(ReferenceCounting) || !set.Has(Reachability) {
		t.Errorf("LiveUnder[UsedByConsumer] holds %v, want both relations", relationsOf(set))
	}
}

func TestReferencesAndSweepOverATargetOnlyDocumentAreUnchanged(t *testing.T) {
	dir := extract(t, "tested-module.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, refs, _ := passes(t, result, root)
	got := renderRun(symbols, refs, New(symbols, refs, nil).Sweep(Mode{Production: true}))
	golden := filepath.Join("testdata", "target-only.golden")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", golden, err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("Setup: read %s (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run TestReferencesAndSweepOverATargetOnlyDocumentAreUnchanged): %v", golden, err)
	}
	if got != string(want) {
		t.Errorf("a target-only document's references and sweep moved (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run TestReferencesAndSweepOverATargetOnlyDocumentAreUnchanged)\n--- want\n%s\n+++ got\n%s", want, got)
	}
}

// renderRun prints one run's references and one sweep's answer, field by field, so
// a golden comparison fails on any change to either. It names no field the
// consumer half added, so the same rendering is what the stage before consumers
// produced.
func renderRun(symbols []Symbol, refs []Reference, r Result) string {
	names := named(symbols)
	var b strings.Builder
	for _, ref := range refs {
		fmt.Fprintf(&b, "reference\t%s\t%s\t%s\t%s:%d:%d\ttest=%t\n",
			names[ref.From], names[ref.To], ref.Kind,
			ref.Pos.Filename, ref.Pos.Line, ref.Pos.Column, ref.Test)
	}
	for _, c := range r.Candidates {
		fmt.Fprintf(&b, "candidate\t%s\t%s\tproduction=%d\ttest=%d\ttestOfDeadCode=%t\n",
			names[c.ID], c.Relation, c.ProductionRefs, c.TestRefs, c.TestOfDeadCode)
	}
	for _, c := range r.Components {
		fmt.Fprintf(&b, "component\t%d\tmembers=%s\troots=%s\tfalls=%s\tlines=%d\n",
			c.Index, strings.Join(namesIn(names, c.Members), " "),
			strings.Join(namesIn(names, c.Roots), " "),
			strings.Join(namesIn(names, c.Falls), " "), c.DeletableLines)
	}
	for _, s := range symbols {
		if set := r.LiveUnder[s.ID]; set != 0 {
			fmt.Fprintf(&b, "live\t%s\t%s\n", s.Name, strings.Join(relationsOf(set), " "))
		}
	}
	return b.String()
}

// namesIn lists the names of a symbol set, in the order given.
func namesIn(names map[SymbolID]string, ids []SymbolID) []string {
	found := make([]string, 0, len(ids))
	for _, id := range ids {
		found = append(found, names[id])
	}
	return found
}

// relationsOf spells the relations one set holds, in the order they are declared.
func relationsOf(set RelationSet) []string {
	var held []string
	for _, relation := range []Relation{ReferenceCounting, Reachability} {
		if set.Has(relation) {
			held = append(held, relation.String())
		}
	}
	return held
}

// idOf returns the identifier of the one symbol of that name, and the empty
// identifier when the inventory holds none.
func idOf(names map[SymbolID]string, name string) SymbolID {
	for id, held := range names {
		if held == name {
			return id
		}
	}
	return ""
}

// mustReadTestdata returns the bytes of one archive under testdata.
func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("Setup: read testdata/%s: %v", name, err)
	}
	return body
}
