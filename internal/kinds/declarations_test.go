package kinds

import (
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

func TestTheUnusedKindsReportEveryDeadDeclarationOfTheFixtureAndNothingLive(t *testing.T) {
	result := analysisOf(t, "declarations-unused.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	for code, want := range map[string][]string{
		unusedExportedCode:   {"Resolve", "Recurse"},
		unusedUnexportedCode: {"helper"},
		unusedMemberCode:     {"Counter.stale"},
	} {
		if got := namesUnder(result.Findings, code); !slices.Equal(got, want) {
			t.Errorf("the pass reports %s about %v, want %v", code, got, want)
		}
	}
	for _, live := range []string{"Used", "usedHelper", "Counter", "Total", "Live", "main"} {
		for i := range result.Findings {
			if result.Findings[i].Symbol.Name == live {
				t.Errorf("the pass reports %s about %s, want nothing: the declaration is live",
					result.Findings[i].Code, live)
			}
		}
	}
}

func TestASelfReferencingDeclarationIsReportedOnceUnderTheCodeItsVisibilitySelects(t *testing.T) {
	result := analysisOf(t, "declarations-unused.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	var reported []string
	for i := range result.Findings {
		if result.Findings[i].Symbol.Name == "Recurse" {
			reported = append(reported, result.Findings[i].Code)
		}
	}
	if !slices.Equal(reported, []string{unusedExportedCode}) {
		t.Fatalf("the pass reports Recurse under %v, want only %s: it is exported and its own call is not a reference to it",
			reported, unusedExportedCode)
	}
	found := findingOf(t, result.Findings, unusedExportedCode, "Recurse")
	if found.Relation != graph.Reachability {
		t.Errorf("the pass reports Recurse under the %s relation, want %s: its own call is a reference",
			found.Relation, graph.Reachability)
	}
	if found.Message != "exported function is referenced only from declarations that are themselves dead" {
		t.Errorf("the pass reports Recurse with the message %q, want the one the reachability relation gives",
			found.Message)
	}
}

func TestTheUnusedMessagesSayWhatTheAnalysisFound(t *testing.T) {
	result := analysisOf(t, "declarations-unused.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	for _, want := range []struct{ code, name, message string }{
		{unusedExportedCode, "Resolve", "exported function has no reference in the target and none from any loaded consumer"},
		{unusedUnexportedCode, "helper", "unexported function has no reference in the target"},
		{unusedMemberCode, "Counter.stale", "unexported field has no reference in the target"},
	} {
		if got := findingOf(t, result.Findings, want.code, want.name).Message; got != want.message {
			t.Errorf("the pass reports %s about %s with the message %q, want %q",
				want.code, want.name, got, want.message)
		}
	}
}

func TestATestOnlyReferenceIsItsOwnKindAndAProductionReferenceRemovesTheFinding(t *testing.T) {
	before := analysisOf(t, "declarations-test-only.txtar", asApplication, Consumers{}).findings(t, declarationKinds)
	after := analysisOf(t, "declarations-test-only-moved.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	if got := namesUnder(before.Findings, testOnlyUseCode); !slices.Equal(got, []string{"OnlyTested"}) {
		t.Errorf("the pass reports %s about %v, want [OnlyTested]", testOnlyUseCode, got)
	}
	found := findingOf(t, before.Findings, testOnlyUseCode, "OnlyTested")
	if !found.TestOnly {
		t.Error("the pass reports OnlyTested with test_only false, want true: every reference to it is from a test file")
	}
	if found.Message != "function is referenced only from test files and never from production code" {
		t.Errorf("the pass reports OnlyTested with the message %q, want the test-only one", found.Message)
	}
	if got := namesUnder(after.Findings, testOnlyUseCode); len(got) != 0 {
		t.Errorf("with the reference moved to a production file the pass reports %s about %v, want nothing",
			testOnlyUseCode, got)
	}
}

func TestATestWhoseEveryTargetIsDeadIsReportedInTheirComponent(t *testing.T) {
	result := analysisOf(t, "declarations-test-of-dead-code.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	if got := namesUnder(result.Findings, testOfDeadCodeCode); !slices.Equal(got, []string{"TestDeadOnly"}) {
		t.Fatalf("the pass reports %s about %v, want [TestDeadOnly]: TestMixed reaches a live declaration",
			testOfDeadCodeCode, got)
	}
	test := findingOf(t, result.Findings, testOfDeadCodeCode, "TestDeadOnly")
	if test.Message != testOfDeadCodeMessage {
		t.Errorf("the pass reports TestDeadOnly with the message %q, want the rule stated: %q",
			test.Message, testOfDeadCodeMessage)
	}
	for _, target := range []string{"DeadOne", "DeadTwo"} {
		found := findingOf(t, result.Findings, testOnlyUseCode, target)
		if found.Component.ID != test.Component.ID {
			t.Errorf("the pass puts %s in component %s and its test in %s, want one component",
				target, found.Component.ID, test.Component.ID)
		}
	}
	for i := range result.Findings {
		switch result.Findings[i].Symbol.Name {
		case "TestMixed", "Live":
			t.Errorf("the pass reports %s about %s, want nothing: it reaches a live declaration",
				result.Findings[i].Code, result.Findings[i].Symbol.Name)
		case "assertSum":
			t.Errorf("the pass reports %s about the test helper, want nothing: a production sweep drops the only references a test declaration can have",
				result.Findings[i].Code)
		}
	}
}

func TestADeprecatedDeclarationNothingReferencesIsReportedUnderTheDeprecatedKindAlone(t *testing.T) {
	result := analysisOf(t, "declarations-deprecated.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	want := []string{"Old", "Stale", "StaleToo", "Counter.Old"}
	if got := namesUnder(result.Findings, deprecatedAndUnusedCode); !slices.Equal(got, want) {
		t.Fatalf("the pass reports %s about %v, want %v: the second Old is the field",
			deprecatedAndUnusedCode, got, want)
	}
	for i := range result.Findings {
		if name := result.Findings[i].Symbol.Name; name == "Kept" || name == "Fresh" || name == "Live" {
			t.Errorf("the pass reports %s about %s, want nothing: it has a production reference",
				result.Findings[i].Code, name)
		}
		if strings.HasSuffix(result.Findings[i].Symbol.Name, "Old") && result.Findings[i].Code != deprecatedAndUnusedCode {
			t.Errorf("the pass reports Old under %s as well, want %s alone",
				result.Findings[i].Code, deprecatedAndUnusedCode)
		}
	}
	if got := findingOf(t, result.Findings, deprecatedAndUnusedCode, "Stale").Message; got != "deprecated variable has no production reference" {
		t.Errorf("the pass reports Stale with the message %q, want the deprecated one", got)
	}
}

func TestAnExportedDeclarationOfAnUnimportablePackageIsLeftToTheUnreachableExportKind(t *testing.T) {
	result := analysisOf(t, "class-visibility.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	for i := range result.Findings {
		if result.Findings[i].Symbol.Name == "Dropped" {
			t.Errorf("the pass reports %s about Dropped, want nothing: nothing outside the module can import its package",
				result.Findings[i].Code)
		}
	}
}

func TestAConsumersTestReferenceDecidesTheKindByHowTheReferencePassCountedIt(t *testing.T) {
	for name, counts := range map[string]struct {
		production, test int
		want             string
	}{
		"counted as a test reference":       {production: 0, test: 1, want: testOnlyUseCode},
		"counted as a production reference": {production: 1, test: 0, want: unusedExportedCode},
	} {
		t.Run(name, func(t *testing.T) {
			in := handInput(applicationConfig())
			in.Sweep.Candidates = []graph.Candidate{{
				ID:             exportedID,
				Relation:       graph.Reachability,
				ProductionRefs: counts.production,
				TestRefs:       counts.test,
				Configs:        graph.ConfigSet(0b11),
			}}

			result := computed(t, in, declarationEmitters())

			if got := codesOf(result.Findings); !slices.Equal(got, []string{counts.want}) {
				t.Errorf("a candidate with %d production and %d test references is reported under %v, want [%s]",
					counts.production, counts.test, got, counts.want)
			}
		})
	}
}

func TestAMemberWhoseContainerIsDeadIsNotReportedIndependently(t *testing.T) {
	const (
		typeID  graph.SymbolID = "catalog.go:40:6"
		fieldID graph.SymbolID = "catalog.go:41:2"
	)

	in := handInput(applicationConfig())
	in.Merged.Symbols = append(in.Merged.Symbols,
		graph.Symbol{
			ID: typeID, Ref: "go://example.com/app#Counter", Name: "Counter",
			PkgPath: "example.com/app", Pos: token.Position{Filename: "catalog.go", Line: 40, Column: 6},
			EndLine: 43, Kind: graph.KindType, Exported: true, Configs: graph.ConfigSet(0b11),
		},
		graph.Symbol{
			ID: fieldID, Ref: "go://example.com/app#Counter.stale", Name: "stale",
			PkgPath: "example.com/app", Pos: token.Position{Filename: "catalog.go", Line: 41, Column: 2},
			EndLine: 41, Kind: graph.KindField, Parent: typeID, Configs: graph.ConfigSet(0b11),
		},
	)
	in.Sweep.Candidates = []graph.Candidate{
		{ID: typeID, Relation: graph.ReferenceCounting, Configs: graph.ConfigSet(0b11)},
		{ID: fieldID, Relation: graph.ReferenceCounting, Configs: graph.ConfigSet(0b11)},
	}

	result := computed(t, in, declarationEmitters())

	if got := codesOf(result.Findings); !slices.Equal(got, []string{unusedExportedCode}) {
		t.Errorf("the pass reports %v about a dead type and its dead field, want [%s] about the type alone",
			got, unusedExportedCode)
	}
}

func TestAKindReportsNothingWhereTheSeverityMapAllowsIt(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]config.Severity{
		unusedExportedCode:   config.Allow,
		unusedUnexportedCode: config.Allow,
	}
	in := inputOf(t, "declarations-unused.txtar", resolved, Consumers{})

	result := computed(t, in, declarationEmitters())

	if got := codesOf(result.Findings); !slices.Equal(got, []string{unusedMemberCode}) {
		t.Errorf("with two kinds allowed the pass reports %v, want [%s]", got, unusedMemberCode)
	}
}

// A production sweep does not hold an exemption whose evidence a test file carries,
// so the fields of a struct the test of its own package is the only thing to marshal
// are candidates, and the evidence counts as a test reference to each, so they report
// as test-only use. The exemption class that would retain them fires on the same
// fixture in the plain mode, which is where the two modes differ.
func TestTheMembersOfAStructOnlyATestFileMarshalsReportUnderAProductionSweep(t *testing.T) {
	result := analysisOf(t, "declarations-test-evidence.txtar", asApplication, Consumers{}).findings(t, everyKind)
	got := make(map[string]string, len(result.Findings))
	for i := range result.Findings {
		got[result.Findings[i].Symbol.Ref] = result.Findings[i].Code
	}
	want := map[string]string{
		"go://example.com/evidence#Fixture.Name":  testOnlyUseCode,
		"go://example.com/evidence#Fixture.Extra": testOnlyUseCode,
		// The struct itself is live, exported in a main package, and named only
		// from inside that package, which is the export-narrowing kind's subject
		// rather than this test's: the table is the whole pass over the fixture.
		"go://example.com/evidence#Fixture": unnecessaryExportCode,
	}
	if !maps.Equal(got, want) {
		t.Errorf("the pass over declarations-test-evidence reported %v, want %v", got, want)
	}
}

// A package one configuration imports from production code is production code,
// whatever another configuration that compiles none of its production importers
// says, so a function of it only a test references is reported as test-only.
func TestAPackageAProductionFileImportsUnderOneConfigurationIsNotTestSupport(t *testing.T) {
	in := inputOf(t, "declarations-test-support-platform.txtar", applicationConfig(), Consumers{},
		load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"},
		load.Configuration{ID: "windows-amd64", OS: "windows", Arch: "amd64"})

	result := computed(t, in, declarationEmitters())

	if got := namesUnder(result.Findings, testOnlyUseCode); !slices.Equal(got, []string{"OnlyTested"}) {
		t.Errorf("the pass over linux and windows reports %s about %v, want [OnlyTested]: linux imports the package from production code",
			testOnlyUseCode, got)
	}
}

// A declaration of test-support code a test references is reported under the test-only
// kind at the possible class, a deprecated one included, and an interface under the
// unused-interface kind instead. Each finding is in a component minted for it, holding
// the subject and the dead members that fall with it and deleting no line.
func TestTestSupportCodeATestReferencesIsReportedAtPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = config.Possible
	in := inputOf(t, "declarations-test-support-possible.txtar", resolved, Consumers{})
	emitters := declarationEmitters()
	emitters[unusedInterfaceCode] = UnusedInterface
	result := computed(t, in, emitters)

	for _, test := range []struct {
		code, name string
		members    int
	}{
		{code: testOnlyUseCode, name: "Old", members: 1},
		{code: testOnlyUseCode, name: "Fake", members: 3},
		{code: unusedInterfaceCode, name: "Sink", members: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			found := findingOf(t, result.Findings, test.code, test.name)
			if found.Class != Possible || found.Confidence != Possible {
				t.Errorf("%s about %s has class %s and confidence %s, want possible and possible",
					test.code, test.name, found.Class, found.Confidence)
			}
			if c := found.Component; !c.Root || c.SymbolCount != test.members || c.DeletableLines != 0 {
				t.Errorf("%s about %s has component root=%t symbols=%d lines=%d, want root=true symbols=%d lines=0",
					test.code, test.name, c.Root, c.SymbolCount, c.DeletableLines, test.members)
			}
		})
	}
	if got := summary(result.Findings); len(got) != 3 {
		t.Errorf("the pass over declarations-test-support-possible.txtar reports %v, want the three findings above alone", got)
	}
}
