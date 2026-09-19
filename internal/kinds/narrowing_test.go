package kinds

import (
	"go/token"
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// The declarations of narrowing-published.txtar a narrowing kind judges.
const (
	publishedHelper   = "go://example.com/app/catalog#Helper"
	publishedShared   = "go://example.com/app/catalog#Shared"
	publishedDo       = "go://example.com/app/catalog#Do"
	internalHelper    = "go://example.com/app/internal/store#Helper"
	internalOpen      = "go://example.com/app/internal/store#Open"
	commandRun        = "go://example.com/app/cmd/app#Run"
	commandUnused     = "go://example.com/app/cmd/app#Unused"
	internalUnused    = "go://example.com/app/internal/store#UnusedInternal"
	internalProbe     = "go://example.com/app/internal/store#Probe"
	internalTestProbe = "go://example.com/app/internal/store#TestProbe"
	internalForgotten = "go://example.com/app/internal/store#Forgotten"
	testPackageUnused = "go://example.com/app/catalog_test#UnusedInTest"
	serviceUse        = "go://example.com/app/svc#Use"
)

// narrowingInput builds what every kind reads from an inventory written by hand,
// through the merge and the matrix sweep a run uses, so a rule is measured over the
// production path from the inventory onward without a load.
//
// names is the package clause of each import path the inventory declares into, which
// is what tells a main package from an importable one.
func narrowingInput(
	sink narrowingSink,
	passes graph.Configured, names map[string]string, consumers Consumers,
) *Input {
	sink.Helper()

	merged, err := graph.Merge([]graph.Configured{passes})
	if err != nil {
		sink.Fatalf("Setup: graph.Merge: %v", err)
	}
	mode := graph.Mode{Production: true}
	swept := graph.NewMatrix(&merged).Sweep(graph.SweepInput{Mode: mode})
	refs := make(map[graph.SymbolID]string, len(merged.Symbols))
	for i := range merged.Symbols {
		refs[merged.Symbols[i].ID] = merged.Symbols[i].Ref
	}
	loaded := make([]*packages.Package, 0, len(names))
	for path, name := range names {
		loaded = append(loaded, &packages.Package{Name: name, PkgPath: path})
	}
	resolved := libraryConfig()
	return &Input{
		Config:    &resolved,
		Merged:    &merged,
		Sweep:     &swept,
		Refs:      refs,
		Matrix:    []string{fixtureConfiguration().ID},
		Per:       []Configured{{Result: &load.Result{Packages: loaded}}},
		Consumers: consumers,
		Mode:      mode,
	}
}

// narrowingSink is what a helper needs of the test that calls it: mark itself a
// helper, and end the test on a setup failure.
type narrowingSink interface {
	Helper()
	Fatalf(format string, args ...any)
}

// narrowedTo is the narrower visibility each finding names, by the symbol reference
// the finding is about.
func narrowedTo(found []Finding) map[string]string {
	by := make(map[string]string, len(found))
	for _, f := range found {
		by[f.Symbol.Ref] = f.Details.NarrowerVisibility
	}
	return by
}

// narrowedRefs is the symbol reference of every finding, in the order the kind
// answered.
func narrowedRefs(found []Finding) []string {
	refs := make([]string, 0, len(found))
	for _, f := range found {
		refs = append(refs, f.Symbol.Ref)
	}
	return refs
}

// narrowedIDOf answers the identifier of the declaration whose reference is ref.
func narrowedIDOf(t *testing.T, in *Input, ref string) graph.SymbolID {
	t.Helper()

	if id := in.index().byRef[ref]; id != "" {
		return id
	}
	t.Fatalf("Setup: the inventory holds no declaration whose reference is %q", ref)
	return ""
}

func TestUnnecessaryExportReportsAPublishedPackageOnlyUnderAClosedWorld(t *testing.T) {
	tests := []struct {
		name      string
		consumers Consumers
		want      map[string]string
	}{
		{
			name:      "no_consumer_information",
			consumers: Consumers{},
			want: map[string]string{
				internalHelper: visibilityPackage,
				commandRun:     visibilityPackage,
			},
		},
		{
			name:      "a_declared_consumer_that_did_not_load",
			consumers: Consumers{Declared: []string{theConsumer}, Complete: true},
			want: map[string]string{
				internalHelper: visibilityPackage,
				commandRun:     visibilityPackage,
			},
		},
		{
			name: "the_consumer_set_is_complete_and_every_consumer_loaded",
			consumers: Consumers{
				Declared: []string{theConsumer},
				Loaded:   []string{theConsumer},
				Complete: true,
			},
			want: map[string]string{
				publishedHelper: visibilityPackage,
				internalHelper:  visibilityPackage,
				commandRun:      visibilityPackage,
			},
		},
		{
			name:      "a_complete_consumer_set_that_declares_none",
			consumers: Consumers{Complete: true},
			want: map[string]string{
				publishedHelper: visibilityPackage,
				internalHelper:  visibilityPackage,
				commandRun:      visibilityPackage,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := inputOf(t, "narrowing-published.txtar", libraryConfig(), tc.consumers)
			found, err := UnnecessaryExport(in)
			if err != nil {
				t.Fatalf("UnnecessaryExport(%s) = error %v, want the findings", tc.name, err)
			}
			if got := narrowedTo(found); !maps.Equal(got, tc.want) {
				t.Errorf("UnnecessaryExport(%s) reported %v, want %v", tc.name, got, tc.want)
			}
			for _, f := range found {
				if f.Code != unnecessaryExportCode {
					t.Errorf("UnnecessaryExport(%s) reported %s under %q, want %q",
						tc.name, f.Symbol.Ref, f.Code, unnecessaryExportCode)
				}
			}
		})
	}
}

func TestUnnecessaryExportNamesTheDeclaringFileWhereEveryReferenceIsInIt(t *testing.T) {
	in := inputOf(t, "narrowing-file.txtar", libraryConfig(), Consumers{})
	found, err := UnnecessaryExport(in)
	if err != nil {
		t.Fatalf("UnnecessaryExport(narrowing-file) = error %v, want the findings", err)
	}
	want := map[string]string{
		"go://example.com/app#Helper": visibilityFile,
		"go://example.com/app#Beyond": visibilityPackage,
	}
	if got := narrowedTo(found); !maps.Equal(got, want) {
		t.Errorf("UnnecessaryExport(narrowing-file) reported %v, want %v", got, want)
	}
}

func TestUnnecessaryExposureReportsAModuleWideReferenceSetUnderAClosedWorld(t *testing.T) {
	tests := []struct {
		name      string
		consumers Consumers
		want      map[string]string
	}{
		{name: "no_consumer_information", consumers: Consumers{}, want: map[string]string{}},
		{
			name:      "the_consumer_set_is_complete",
			consumers: Consumers{Complete: true},
			want:      map[string]string{publishedShared: visibilityModule},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := inputOf(t, "narrowing-published.txtar", libraryConfig(), tc.consumers)
			found, err := UnnecessaryExposure(in)
			if err != nil {
				t.Fatalf("UnnecessaryExposure(%s) = error %v, want the findings", tc.name, err)
			}
			if got := narrowedTo(found); !maps.Equal(got, tc.want) {
				t.Errorf("UnnecessaryExposure(%s) reported %v, want %v", tc.name, got, tc.want)
			}
			if slices.Contains(narrowedRefs(found), internalOpen) {
				t.Errorf("UnnecessaryExposure(%s) reported %s, want no finding on a declaration "+
					"an internal element already guards", tc.name, internalOpen)
			}
		})
	}
}

func TestUnreachableExportReachesCertainWithNoConsumerInformation(t *testing.T) {
	in := inputOf(t, "narrowing-published.txtar", libraryConfig(), Consumers{})
	found, err := UnreachableExport(in)
	if err != nil {
		t.Fatalf("UnreachableExport(narrowing-published) = error %v, want the findings", err)
	}
	want := []string{commandUnused, internalUnused, internalForgotten, testPackageUnused}
	got := narrowedRefs(found)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("UnreachableExport(narrowing-published) reported %v, want %v", got, want)
	}
	for _, f := range found {
		if f.Code != unreachableExportCode {
			t.Errorf("UnreachableExport reported %s under %q, want %q",
				f.Symbol.Ref, f.Code, unreachableExportCode)
		}
		if f.Details.NarrowerVisibility != "" {
			t.Errorf("UnreachableExport reported %s naming the narrower visibility %q, want none",
				f.Symbol.Ref, f.Details.NarrowerVisibility)
		}
		if class := in.ClassOf(narrowedIDOf(t, in, f.Symbol.Ref)); class != Certain {
			t.Errorf("ClassOf(%s) = %q, want %q with no consumer information",
				f.Symbol.Ref, class, Certain)
		}
	}
}

func TestUnreachableExportSaysWhatTheAnalysisFoundAboutTheReferences(t *testing.T) {
	in := inputOf(t, "narrowing-published.txtar", libraryConfig(), Consumers{})
	found, err := UnreachableExport(in)
	if err != nil {
		t.Fatalf("UnreachableExport(narrowing-published) = error %v, want the findings", err)
	}
	want := map[string]string{
		commandUnused: "exported function has no reference in the target, and nothing outside " +
			"can import the package that declares it",
		internalForgotten: "exported function is referenced only from declarations that are " +
			"themselves dead, and nothing outside can import the package that declares it",
	}
	for _, f := range found {
		if message, pinned := want[f.Symbol.Ref]; pinned && f.Message != message {
			t.Errorf("UnreachableExport reports %s with the message %q, want %q",
				f.Symbol.Ref, f.Message, message)
		}
	}
}

func TestUnreachableExportLeavesADeclarationOutsideItsPopulationAlone(t *testing.T) {
	in := inputOf(t, "narrowing-published.txtar", libraryConfig(), Consumers{})
	found, err := UnreachableExport(in)
	if err != nil {
		t.Fatalf("UnreachableExport(narrowing-published) = error %v, want the findings", err)
	}
	outside := []string{
		publishedHelper, // a declaration its own package references
		internalHelper,  // the same, under an internal tree
		commandRun,      // the same, in a main package
		publishedDo,     // a candidate of a package an importer outside the module can name
	}
	for _, ref := range outside {
		if slices.Contains(narrowedRefs(found), ref) {
			t.Errorf("UnreachableExport reported %s, want no finding on it", ref)
		}
	}
}

func TestUnreachableExportLeavesACandidateATestFileReferencesToTheTestOnlyKind(t *testing.T) {
	in := inputOf(t, "narrowing-published.txtar", libraryConfig(), Consumers{})
	result, err := Compute(in, map[string]Emitter{
		testOnlyUseCode:       TestOnlyUse,
		unreachableExportCode: UnreachableExport,
	})
	if err != nil {
		t.Fatalf("Compute(the test-only and the unreachable-export kinds) = error %v, "+
			"want the findings", err)
	}
	want := map[string]string{
		internalProbe:     testOnlyUseCode,
		commandUnused:     unreachableExportCode,
		internalUnused:    unreachableExportCode,
		internalForgotten: unreachableExportCode,
		testPackageUnused: unreachableExportCode,
	}
	got := make(map[string]string, len(result.Findings))
	for _, f := range result.Findings {
		got[f.Symbol.Ref] = f.Code
	}
	if !maps.Equal(got, want) {
		t.Errorf("Compute(the test-only and the unreachable-export kinds) reported %v, want %v",
			got, want)
	}
}

// The three narrowing kinds and the six unused-declaration kinds report each
// declaration once, so a declaration of a package nothing outside can import is the
// unreachable-export kind's and never the unused-exported kind's. The resolved
// configuration declares the consumer set complete as well, because the
// unused-exported kind is allowed for a library whose published API may have
// references the analysis cannot see, and an allowed kind cannot disagree.
func TestNarrowingAndTheUnusedDeclarationKindsReportEachDeclarationOnce(t *testing.T) {
	resolved := libraryConfig()
	resolved.Consumers.Complete = true
	in := inputOf(t, "narrowing-published.txtar", resolved, Consumers{Complete: true})
	emitters := declarationEmitters()
	emitters[unnecessaryExportCode] = UnnecessaryExport
	emitters[unnecessaryExposureCode] = UnnecessaryExposure
	emitters[unreachableExportCode] = UnreachableExport

	result := computed(t, in, emitters)
	want := map[string]string{
		publishedDo:       unusedExportedCode,
		publishedHelper:   unnecessaryExportCode,
		publishedShared:   unnecessaryExposureCode,
		serviceUse:        unusedExportedCode,
		commandRun:        unnecessaryExportCode,
		commandUnused:     unreachableExportCode,
		internalHelper:    unnecessaryExportCode,
		internalUnused:    unreachableExportCode,
		internalForgotten: unreachableExportCode,
		internalProbe:     testOnlyUseCode,
		internalTestProbe: testOfDeadCodeCode,
		testPackageUnused: unreachableExportCode,
	}
	got := make(map[string]string, len(result.Findings))
	for _, f := range result.Findings {
		got[f.Symbol.Ref] = f.Code
	}
	if !maps.Equal(got, want) {
		t.Errorf("Compute(the narrowing and the unused-declaration kinds) reported %v, want %v",
			got, want)
	}
}

// The unreachable-export kind is the unused-exported answer for a package nothing
// outside can import, so it reports what that kind reports and yields every subject a
// more specific kind claims. Over a package under an internal tree, that is the four
// subjects of the interface and the read-and-write kinds.
//
// The two narrowing findings beside them are the same package's live exported
// declarations nothing outside it names, which the export-narrowing kind reports
// because an internal tree is closed world: they are not the subject of this test and
// they are here because the table is the whole package's.
func TestUnreachableExportYieldsEverySubjectAMoreSpecificKindReports(t *testing.T) {
	want := map[string]string{
		"go://example.com/app/internal/api#Reader":      unusedInterfaceCode,
		"go://example.com/app/internal/api#Store.Flush": uncalledInterfaceMethodCode,
		"go://example.com/app/internal/api#Quiet":       enumMemberCode,
		"go://example.com/app/internal/api#Convert[T]":  typeParameterCode,
		"go://example.com/app/internal/api#Forgotten":   unreachableExportCode,
		"go://example.com/app/internal/api#Level":       unnecessaryExportCode,
		"go://example.com/app/internal/api#Loud":        unnecessaryExportCode,
	}

	in := inputOf(t, "narrowing-unimportable.txtar", applicationConfig(), Consumers{})
	result := computed(t, in, packageEmitters())
	got := make(map[string]string, len(result.Findings))
	for _, f := range result.Findings {
		got[f.Symbol.Ref] = f.Code
	}
	if !maps.Equal(got, want) {
		t.Errorf("Compute(every kind of the package and the unreachable-export kind) reported %v, want %v",
			got, want)
	}
}

func TestNarrowingLeavesADeclarationAnExemptionHoldsLiveAlone(t *testing.T) {
	in := inputOf(t, "narrowing-published.txtar", libraryConfig(), Consumers{})
	exempted := narrowedIDOf(t, in, internalHelper)
	exemptions := []graph.Exemption{{ID: exempted, Class: "interface-satisfaction"}}
	in.Exempt = exemptions

	found, err := UnnecessaryExport(in)
	if err != nil {
		t.Fatalf("UnnecessaryExport(an exempt declaration) = error %v, want the findings", err)
	}
	if slices.Contains(narrowedRefs(found), internalHelper) {
		t.Errorf("UnnecessaryExport(an exempt declaration) reported %s, want no finding on a "+
			"declaration an exemption class holds live", internalHelper)
	}
}

// A declaration the counted references store into and never read is the write-only
// kind's subject, which claims it is deletable. That is the more specific answer about
// it than a narrower visibility, so a narrowing yields it whatever its reference
// spread says.
func TestNarrowingYieldsADeclarationNothingReadsToTheWriteOnlyKind(t *testing.T) {
	const (
		file    = "answer.go"
		pkgPath = "example.com/app"
		fieldOf = "go://example.com/app#Answer.Status"
	)
	answer := graph.Symbol{
		ID:       graph.SymbolID(file + ":3:6"),
		Ref:      "go://example.com/app#Answer",
		Name:     "Answer",
		PkgPath:  pkgPath,
		Pos:      token.Position{Filename: file, Line: 3, Column: 6},
		EndLine:  5,
		Kind:     graph.KindType,
		Exported: true,
	}
	status := graph.Symbol{
		ID:       graph.SymbolID(file + ":4:2"),
		Ref:      fieldOf,
		Name:     "Status",
		PkgPath:  pkgPath,
		Pos:      token.Position{Filename: file, Line: 4, Column: 2},
		EndLine:  4,
		Kind:     graph.KindField,
		Parent:   answer.ID,
		Exported: true,
	}
	fill := graph.Symbol{
		ID:       graph.SymbolID(file + ":7:6"),
		Ref:      "go://example.com/app#Fill",
		Name:     "Fill",
		PkgPath:  pkgPath,
		Pos:      token.Position{Filename: file, Line: 7, Column: 6},
		EndLine:  9,
		Kind:     graph.KindFunc,
		Exported: true,
	}
	in := narrowingInput(t, graph.Configured{
		Symbols: []graph.Symbol{answer, status, fill},
		References: []graph.Reference{
			{
				From: fill.ID, To: status.ID, Kind: graph.RefWrite,
				Pos: token.Position{Filename: file, Line: 8, Column: 3},
			},
			{
				From: fill.ID, To: answer.ID, Kind: graph.RefTypeUse,
				Pos: token.Position{Filename: file, Line: 8, Column: 12},
			},
		},
		Roots: []graph.Root{{ID: fill.ID, Kind: graph.RootMain}},
	}, map[string]string{pkgPath: mainPackageName}, Consumers{})

	narrowed, err := UnnecessaryExport(in)
	if err != nil {
		t.Fatalf("UnnecessaryExport(a written declaration nothing reads) = error %v, want the findings", err)
	}
	if slices.Contains(narrowedRefs(narrowed), fieldOf) {
		t.Errorf("UnnecessaryExport reported %s, want no finding on a declaration the write-only "+
			"kind reports", fieldOf)
	}
	written, err := WriteOnlySymbol(in)
	if err != nil {
		t.Fatalf("WriteOnlySymbol(a written declaration nothing reads) = error %v, want the findings", err)
	}
	if !slices.Contains(narrowedRefs(written), fieldOf) {
		t.Errorf("WriteOnlySymbol dropped %s, so the narrowing kind yielded it to nobody", fieldOf)
	}
}

func TestNarrowingLeavesADeclarationReferencedFromOutsideTheModuleAlone(t *testing.T) {
	const (
		ref     = "go://example.com/app/catalog#Shared"
		file    = "catalog/api.go"
		pkgPath = "example.com/app/catalog"
	)
	subject := graph.Symbol{
		ID:       graph.SymbolID(file + ":3:6"),
		Ref:      ref,
		Name:     "Shared",
		PkgPath:  pkgPath,
		Pos:      token.Position{Filename: file, Line: 3, Column: 6},
		EndLine:  3,
		Kind:     graph.KindFunc,
		Exported: true,
	}
	tests := []struct {
		name     string
		from     graph.SymbolID
		consumer string
		want     map[string]string
	}{
		{
			name: "a_reference_the_inventory_does_not_hold",
			from: "consumer/use.go:5:2",
			want: map[string]string{},
		},
		{
			// A loaded consumer's reference names no declaration of the
			// inventory, because a run enumerates the target's declarations
			// alone, and it names the module that made it.
			name:     "a_reference_a_loaded_consumer_made",
			consumer: theConsumer,
			want:     map[string]string{},
		},
		{
			name: "a_reference_of_the_declaring_file",
			from: subject.ID,
			want: map[string]string{ref: visibilityFile},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := narrowingInput(t, graph.Configured{
				Symbols: []graph.Symbol{subject},
				References: []graph.Reference{{
					From:     tc.from,
					To:       subject.ID,
					Consumer: tc.consumer,
					Pos:      token.Position{Filename: file, Line: 4, Column: 9},
					Kind:     graph.RefCall,
				}},
				Roots: []graph.Root{{ID: subject.ID, Kind: graph.RootPublishedAPI}},
			}, map[string]string{pkgPath: "catalog"}, Consumers{Complete: true})

			exported, err := UnnecessaryExport(in)
			if err != nil {
				t.Fatalf("UnnecessaryExport(%s) = error %v, want the findings", tc.name, err)
			}
			exposed, err := UnnecessaryExposure(in)
			if err != nil {
				t.Fatalf("UnnecessaryExposure(%s) = error %v, want the findings", tc.name, err)
			}
			if got := narrowedTo(append(exported, exposed...)); !maps.Equal(got, tc.want) {
				t.Errorf("the narrowing kinds over %s reported %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// The framework fills the same fields for a narrowing finding as for any other, and
// the confidence of one is the reachability class: a declaration of a package
// nothing outside can import is certain whatever the run knows about consumers,
// while a published one is certain only where the run loaded every consumer the
// scope declared. A consumer set declared complete and declaring none is what opens
// the two narrowing kinds on the published package, and it is not consumer
// information, so those findings are possible.
func TestNarrowingReportsEveryFindingTheFrameworkAccepts(t *testing.T) {
	loaded := Consumers{Declared: []string{theConsumer}, Loaded: []string{theConsumer}, Complete: true}
	tests := []struct {
		name      string
		consumers Consumers
		published string
	}{
		{name: "a_complete_consumer_set_that_declares_none", consumers: Consumers{Complete: true}, published: "possible"},
		{name: "a_declared_consumer_that_loaded", consumers: loaded, published: "certain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := inputOf(t, "narrowing-published.txtar", libraryConfig(), tc.consumers)
			result, err := Compute(in, map[string]Emitter{
				unnecessaryExportCode:   UnnecessaryExport,
				unnecessaryExposureCode: UnnecessaryExposure,
				unreachableExportCode:   UnreachableExport,
			})
			if err != nil {
				t.Fatalf("Compute(the three narrowing kinds) = error %v, want the findings", err)
			}
			want := map[string]string{
				publishedHelper:   unnecessaryExportCode + " warn narrowable " + tc.published,
				internalHelper:    unnecessaryExportCode + " warn narrowable certain",
				commandRun:        unnecessaryExportCode + " warn narrowable certain",
				publishedShared:   unnecessaryExposureCode + " warn narrowable " + tc.published,
				commandUnused:     unreachableExportCode + " deny deletable certain",
				internalUnused:    unreachableExportCode + " deny deletable certain",
				internalForgotten: unreachableExportCode + " deny deletable certain",
				testPackageUnused: unreachableExportCode + " deny deletable certain",
			}
			got := make(map[string]string, len(result.Findings))
			for _, f := range result.Findings {
				got[f.Symbol.Ref] = f.Code + " " + string(f.Severity) + " " +
					f.Fixability + " " + string(f.Confidence)
			}
			if !maps.Equal(got, want) {
				t.Errorf("Compute(the three narrowing kinds) reported %v, want %v", got, want)
			}
		})
	}
}

// A narrowing finding's subject is a live declaration, so no liveness relation
// decided it and the finding says so; an unreachable-export finding's subject is a
// candidate, so it carries the relation that found it. The framework decides both
// from the sweep, which is why one pass over one fixture measures the two.
func TestANarrowingFindingCarriesNoLivenessRelationAndAnUnreachableExportCarriesTheCandidates(t *testing.T) {
	in := inputOf(t, "narrowing-published.txtar", libraryConfig(), Consumers{Complete: true})
	result, err := Compute(in, map[string]Emitter{
		unnecessaryExportCode:   UnnecessaryExport,
		unnecessaryExposureCode: UnnecessaryExposure,
		unreachableExportCode:   UnreachableExport,
	})
	if err != nil {
		t.Fatalf("Compute(the three narrowing kinds) = error %v, want the findings", err)
	}
	want := map[string]bool{
		publishedHelper:   true,
		internalHelper:    true,
		commandRun:        true,
		publishedShared:   true,
		commandUnused:     false,
		internalUnused:    false,
		internalForgotten: false,
		testPackageUnused: false,
	}
	live := make(map[string]bool, len(result.Findings))
	for _, f := range result.Findings {
		live[f.Symbol.Ref] = f.Live
	}
	if !maps.Equal(live, want) {
		t.Errorf("Compute(the three narrowing kinds) reported the live subjects %v, want %v", live, want)
	}
	forgotten := findingOf(t, result.Findings, unreachableExportCode, "Forgotten")
	if forgotten.Relation != graph.Reachability {
		t.Errorf("Compute() reports %s with the relation %q, want %q: the candidate was found by reachability",
			internalForgotten, forgotten.Relation, graph.Reachability)
	}
}

// Every finding of a kind of this file names the language and the vocabulary's own
// name for the kind, which the framework fills rather than the emitter.
func TestNarrowingCarriesTheVocabularyNameAndTheLanguage(t *testing.T) {
	in := inputOf(t, "narrowing-published.txtar", libraryConfig(), Consumers{Complete: true})
	result, err := Compute(in, map[string]Emitter{
		unnecessaryExportCode:   UnnecessaryExport,
		unnecessaryExposureCode: UnnecessaryExposure,
		unreachableExportCode:   UnreachableExport,
	})
	if err != nil {
		t.Fatalf("Compute(the three narrowing kinds) = error %v, want the findings", err)
	}
	names := map[string]string{
		unnecessaryExportCode:   "unnecessary-export",
		unnecessaryExposureCode: "unnecessary-exposure",
		unreachableExportCode:   "unreachable-export",
	}
	for _, f := range result.Findings {
		if f.Kind != names[f.Code] {
			t.Errorf("Compute() reported %s under the name %q, want %q",
				f.Symbol.Ref, f.Kind, names[f.Code])
		}
		if f.Language != "go" {
			t.Errorf("Compute() reported %s in the language %q, want %q", f.Symbol.Ref, f.Language, "go")
		}
	}
}

// The declarations of narrowing-members.txtar the two narrowing kinds judge.
const (
	storeFill        = "go://example.com/app/internal/store#Fill"
	storeDrain       = "go://example.com/app/internal/store#Drain"
	storeSink        = "go://example.com/app/internal/store#Sink"
	storeSinkWrite   = "go://example.com/app/internal/store#Sink.Write"
	storeBuffer      = "go://example.com/app/internal/store#Buffer"
	storeBufferData  = "go://example.com/app/internal/store#Buffer.Data"
	storeBufferWrite = "go://example.com/app/internal/store#Buffer.Write"
	storePipe        = "go://example.com/app/internal/store#Pipe"
	storePipeWrite   = "go://example.com/app/internal/store#Pipe.Write"
	sharedRecord     = "go://example.com/app/shared#Record"
	sharedRecordName = "go://example.com/app/shared#Record.Name"
	sharedLabel      = "go://example.com/app/shared#Label"
)

// Neither narrowing kind reports a method an interface declares, a field of a
// struct, or a method that satisfies an interface the target names as a type. Each
// exclusion is measured beside the declarations of the same fixture the kinds do
// report, so an empty population cannot pass for an exclusion.
func TestNarrowingReportsNoInterfaceMethodNoFieldAndNoInterfaceSatisfyingMethod(t *testing.T) {
	in := inputOf(t, "narrowing-members.txtar", applicationConfig(), Consumers{Complete: true})
	found, err := UnnecessaryExport(in)
	if err != nil {
		t.Fatalf("UnnecessaryExport(narrowing-members) = error %v, want the findings", err)
	}
	exposed, err := UnnecessaryExposure(in)
	if err != nil {
		t.Fatalf("UnnecessaryExposure(narrowing-members) = error %v, want the findings", err)
	}
	want := map[string]string{
		storeFill:    visibilityPackage,
		storeDrain:   visibilityPackage,
		storeSink:    visibilityFile,
		storeBuffer:  visibilityFile,
		storePipe:    visibilityPackage,
		sharedRecord: visibilityModule,
		sharedLabel:  visibilityModule,
	}
	if got := narrowedTo(append(found, exposed...)); !maps.Equal(got, want) {
		t.Errorf("the narrowing kinds over narrowing-members reported %v, want %v", got, want)
	}
}

// The two rules that exclude a method are not one rule: the exemption class holds a
// method the program converts to an interface, and the kinds' own rule holds a
// method that satisfies an interface the target names as a type whether or not
// anything is converted to it. This pins which rule answers for each of the
// fixture's two satisfying methods, so a later change cannot delete one rule and
// keep both methods excluded by accident.
func TestNarrowingExcludesASatisfyingMethodTheExemptionClassDoesNotHold(t *testing.T) {
	in := inputOf(t, "narrowing-members.txtar", applicationConfig(), Consumers{Complete: true})

	exempted := make(map[string]bool, len(in.Exempt))
	for _, exemption := range in.Exempt {
		if exemption.Class == string(exempt.InterfaceSatisfaction) {
			exempted[in.Refs[exemption.ID]] = true
		}
	}
	if !exempted[storePipeWrite] {
		t.Errorf("the interface-satisfaction class does not hold %s, which the conversion at the call of Drain retains: it holds %v",
			storePipeWrite, slices.Sorted(maps.Keys(exempted)))
	}
	if exempted[storeBufferWrite] {
		t.Errorf("the interface-satisfaction class holds %s, which nothing converts to the interface, so this test no longer measures the kinds' own rule",
			storeBufferWrite)
	}

	found, err := UnnecessaryExport(in)
	if err != nil {
		t.Fatalf("UnnecessaryExport(narrowing-members) = error %v, want the findings", err)
	}
	for _, ref := range []string{storeBufferWrite, storePipeWrite} {
		if slices.Contains(narrowedRefs(found), ref) {
			t.Errorf("UnnecessaryExport reported %s, want no finding on a method that satisfies an interface the target names as a type", ref)
		}
	}
}
