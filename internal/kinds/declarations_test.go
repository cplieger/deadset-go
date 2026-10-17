package kinds

import (
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

func TestTheUnusedKindsReportEveryDeadDeclarationOfTheFixtureAndNothingLive(t *testing.T) {
	result := analysisOf(t, "declarations-unused.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	for code, want := range map[vocab.Code][]string{
		vocab.UnusedExported:   {"Resolve", "Recurse"},
		vocab.UnusedUnexported: {"helper"},
		vocab.UnusedMember:     {"Counter.stale"},
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

	var reported []vocab.Code
	for i := range result.Findings {
		if result.Findings[i].Symbol.Name == "Recurse" {
			reported = append(reported, result.Findings[i].Code)
		}
	}
	if !slices.Equal(reported, []vocab.Code{vocab.UnusedExported}) {
		t.Fatalf("the pass reports Recurse under %v, want only %s: it is exported and its own call is not a reference to it",
			reported, vocab.UnusedExported)
	}
	found := findingOf(t, result.Findings, vocab.UnusedExported, "Recurse")
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

	for _, want := range []struct {
		code          vocab.Code
		name, message string
	}{
		{vocab.UnusedExported, "Resolve", "exported function has no reference in the target and none from any loaded consumer"},
		{vocab.UnusedUnexported, "helper", "unexported function has no reference in the target"},
		{vocab.UnusedMember, "Counter.stale", "unexported field has no reference in the target"},
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

	if got := namesUnder(before.Findings, vocab.TestOnlyUse); !slices.Equal(got, []string{"OnlyTested"}) {
		t.Errorf("the pass reports %s about %v, want [OnlyTested]", vocab.TestOnlyUse, got)
	}
	found := findingOf(t, before.Findings, vocab.TestOnlyUse, "OnlyTested")
	if !found.TestOnly {
		t.Error("the pass reports OnlyTested with test_only false, want true: every reference to it is from a test file")
	}
	if found.Message != "function is referenced only from test files and never from production code" {
		t.Errorf("the pass reports OnlyTested with the message %q, want the test-only one", found.Message)
	}
	if got := namesUnder(after.Findings, vocab.TestOnlyUse); len(got) != 0 {
		t.Errorf("with the reference moved to a production file the pass reports %s about %v, want nothing",
			vocab.TestOnlyUse, got)
	}
}

func TestATestWhoseEveryTargetIsDeadIsReportedInTheirComponent(t *testing.T) {
	result := analysisOf(t, "declarations-test-of-dead-code.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	if got := namesUnder(result.Findings, vocab.TestOfDeadCode); !slices.Equal(got, []string{"TestDeadOnly"}) {
		t.Fatalf("the pass reports %s about %v, want [TestDeadOnly]: TestMixed reaches a live declaration",
			vocab.TestOfDeadCode, got)
	}
	test := findingOf(t, result.Findings, vocab.TestOfDeadCode, "TestDeadOnly")
	if test.Message != testOfDeadCodeMessage {
		t.Errorf("the pass reports TestDeadOnly with the message %q, want the rule stated: %q",
			test.Message, testOfDeadCodeMessage)
	}
	for _, target := range []string{"DeadOne", "DeadTwo"} {
		found := findingOf(t, result.Findings, vocab.TestOnlyUse, target)
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

func TestTheMembersOfATestFileDeclarationATestReachesAreJudgedOnTheirOwn(t *testing.T) {
	result := analysisOf(t, "declarations-test-file-members.txtar", asApplication, Consumers{}).findings(t, everyKind)

	for code, want := range map[vocab.Code][]string{
		vocab.UnusedMember:        {"fixture.Spare", "fixture.stale"},
		vocab.UnusedUnexported:    {"entry", "fixture.helper"},
		vocab.UnreachableExport:   {"fixture.Label"},
		vocab.UnusedTypeParameter: {"first[U]"},
		vocab.UnusedEnumMember:    {"PhaseStop"},
	} {
		if got := namesUnder(result.Findings, code); !slices.Equal(got, want) {
			t.Errorf("the pass reports %s about %v, want %v: the test reaches their container, which holds none of them live",
				code, got, want)
		}
	}
	for i := range result.Findings {
		switch name := result.Findings[i].Symbol.Name; name {
		case "fixture", "fixture.Used", "first", "TestFixture", "entry.Name", "phase", "phaseStart":
			t.Errorf("the pass reports %s about %s, want nothing: a test reaches it, or it falls with its container",
				result.Findings[i].Code, name)
		}
	}
	entry := findingOf(t, result.Findings, vocab.UnusedUnexported, "entry")
	spare := findingOf(t, result.Findings, vocab.UnusedMember, "fixture.Spare")
	if entry.Component.ID != spare.Component.ID {
		t.Errorf("the pass puts entry in component %s and fixture.Spare in %s, want one component: only the field names the type",
			entry.Component.ID, spare.Component.ID)
	}
}

func TestAMethodOnlyADeadDeclarationConvertsFallsWithItAndNoExemptionRetainsIt(t *testing.T) {
	result := analysisOf(t, "declarations-dead-holder.txtar", asApplication, Consumers{}).findings(t, everyKind)

	spare := findingOf(t, result.Findings, vocab.UnusedUnexported, "spare")
	write := findingOf(t, result.Findings, vocab.UnreachableExport, "(*sink).Write")
	if write.Component.ID != spare.Component.ID {
		t.Errorf("the pass puts (*sink).Write in component %s and spare in %s, want one component: only spare's conversion keeps the method",
			write.Component.ID, spare.Component.ID)
	}
	if len(write.RetainedBy) != 0 {
		t.Errorf("the pass reports (*sink).Write retained by %v, want none: the conversion that satisfies io.Writer is in dead code",
			write.RetainedBy)
	}
}

func TestAConversionInLiveCodeRetainsAMethodADeadDeclarationConvertsFirst(t *testing.T) {
	result := analysisOf(t, "declarations-held-twice.txtar", asApplication, Consumers{}).findings(t, everyKind)

	findingOf(t, result.Findings, vocab.UnusedUnexported, "spare")
	for i := range result.Findings {
		if result.Findings[i].Symbol.Name == "(*sink).Write" {
			t.Errorf("the pass reports %s about (*sink).Write, want nothing: main converts the type to io.Writer",
				result.Findings[i].Code)
		}
	}
}

// An assertion is held by its own declaration as well as by the one converting the
// value, so a method whose only caller is an assertion it writes itself is dead
// under reachability, and the method main calls through the conversion is not.
func TestAnAssertionInsideTheMethodItRetainsHoldsNothing(t *testing.T) {
	result := analysisOf(t, "declarations-asserted-inside.txtar", asApplication, Consumers{}).findings(t, everyKind)

	closed := findingOf(t, result.Findings, vocab.UnreachableExport, "(*wrapper).Close")
	if closed.Relation != graph.Reachability {
		t.Errorf("the pass reports (*wrapper).Close under %v, want reachability: main holds the conversion and only Close holds the assertion",
			closed.Relation)
	}
	for i := range result.Findings {
		if result.Findings[i].Symbol.Name == "(*wrapper).Write" {
			t.Errorf("the pass reports %s about (*wrapper).Write, want nothing: main converts the wrapper to io.Writer and calls it",
				result.Findings[i].Code)
		}
	}
}

func TestAMethodAnErrorsAsTargetRequiresIsRetainedOnATypeUsedAsAnError(t *testing.T) {
	result := analysisOf(t, "interfaces-errors-as.txtar", asApplication, Consumers{}).findings(t, everyKind)

	var reported []string
	for i := range result.Findings {
		reported = append(reported, string(result.Findings[i].Code)+" "+result.Findings[i].Symbol.Name)
	}
	if want := []string{string(vocab.UnreachableExport) + " plain.Extra"}; !slices.Equal(reported, want) {
		t.Errorf("the pass reports %v, want %v: errors.As and errors.AsType assert the error to the interface whose method the classifier calls",
			reported, want)
	}
}

func TestADeprecatedDeclarationNothingReferencesIsReportedUnderTheDeprecatedKindAlone(t *testing.T) {
	result := analysisOf(t, "declarations-deprecated.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	want := []string{"Old", "Stale", "StaleToo", "Counter.Old"}
	if got := namesUnder(result.Findings, vocab.DeprecatedUnused); !slices.Equal(got, want) {
		t.Fatalf("the pass reports %s about %v, want %v: the second Old is the field",
			vocab.DeprecatedUnused, got, want)
	}
	for i := range result.Findings {
		if name := result.Findings[i].Symbol.Name; name == "Kept" || name == "Fresh" || name == "Live" {
			t.Errorf("the pass reports %s about %s, want nothing: it has a production reference",
				result.Findings[i].Code, name)
		}
		if strings.HasSuffix(result.Findings[i].Symbol.Name, "Old") && result.Findings[i].Code != vocab.DeprecatedUnused {
			t.Errorf("the pass reports Old under %s as well, want %s alone",
				result.Findings[i].Code, vocab.DeprecatedUnused)
		}
	}
	if got := findingOf(t, result.Findings, vocab.DeprecatedUnused, "Stale").Message; got != "deprecated variable has no production reference" {
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
		want             vocab.Code
	}{
		"counted as a test reference":       {production: 0, test: 1, want: vocab.TestOnlyUse},
		"counted as a production reference": {production: 1, test: 0, want: vocab.UnusedExported},
	} {
		t.Run(name, func(t *testing.T) {
			in := handInput(applicationConfig())
			in.Sweep.Candidates = []graph.Candidate{{
				ID:             exportedID,
				Relation:       graph.Reachability,
				ProductionRefs: counts.production,
				TestRefs:       counts.test,
				Configs:        inventory.ConfigSet(0b11),
			}}

			result := computed(t, in, declarationEmitters())

			if got := codesOf(result.Findings); !slices.Equal(got, []vocab.Code{counts.want}) {
				t.Errorf("a candidate with %d production and %d test references is reported under %v, want [%s]",
					counts.production, counts.test, got, counts.want)
			}
		})
	}
}

func TestAMemberWhoseContainerIsDeadIsNotReportedIndependently(t *testing.T) {
	const (
		typeID  inventory.SymbolID = "catalog.go:40:6"
		fieldID inventory.SymbolID = "catalog.go:41:2"
	)

	in := handInput(applicationConfig())
	in.Merged.Symbols = append(in.Merged.Symbols,
		inventory.Symbol{
			ID: typeID, Ref: "go://example.com/app#Counter", Name: "Counter",
			PkgPath: "example.com/app", Pos: token.Position{Filename: "catalog.go", Line: 40, Column: 6},
			EndLine: 43, Kind: inventory.KindType, Exported: true, Configs: inventory.ConfigSet(0b11),
		},
		inventory.Symbol{
			ID: fieldID, Ref: "go://example.com/app#Counter.stale", Name: "stale",
			PkgPath: "example.com/app", Pos: token.Position{Filename: "catalog.go", Line: 41, Column: 2},
			EndLine: 41, Kind: inventory.KindField, Parent: typeID, Configs: inventory.ConfigSet(0b11),
		},
	)
	in.Sweep.Candidates = []graph.Candidate{
		{ID: typeID, Relation: graph.ReferenceCounting, Configs: inventory.ConfigSet(0b11)},
		{ID: fieldID, Relation: graph.ReferenceCounting, Configs: inventory.ConfigSet(0b11)},
	}

	result := computed(t, in, declarationEmitters())

	if got := codesOf(result.Findings); !slices.Equal(got, []vocab.Code{vocab.UnusedExported}) {
		t.Errorf("the pass reports %v about a dead type and its dead field, want [%s] about the type alone",
			got, vocab.UnusedExported)
	}
}

func TestAKindReportsNothingWhereTheSeverityMapAllowsIt(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]vocab.Severity{
		string(vocab.UnusedExported):   vocab.Allow,
		string(vocab.UnusedUnexported): vocab.Allow,
	}
	in := inputOf(t, "declarations-unused.txtar", resolved, Consumers{})

	result := computed(t, in, declarationEmitters())

	if got := codesOf(result.Findings); !slices.Equal(got, []vocab.Code{vocab.UnusedMember}) {
		t.Errorf("with two kinds allowed the pass reports %v, want [%s]", got, vocab.UnusedMember)
	}
}

// A production sweep does not hold an exemption whose evidence a test file carries,
// so the fields of a struct the test of its own package is the only thing to marshal
// are candidates: the field the test writes reports as test-only use, and the field
// nothing writes, which the encoder reads no value of, as an unused member.
func TestTheMembersOfAStructOnlyATestFileMarshalsReportUnderAProductionSweep(t *testing.T) {
	result := analysisOf(t, "declarations-test-evidence.txtar", asApplication, Consumers{}).findings(t, everyKind)
	got := make(map[string]vocab.Code, len(result.Findings))
	for i := range result.Findings {
		got[result.Findings[i].Symbol.Ref] = result.Findings[i].Code
	}
	want := map[string]vocab.Code{
		"go://example.com/evidence#Fixture.Name":  vocab.TestOnlyUse,
		"go://example.com/evidence#Fixture.Extra": vocab.UnusedMember,
		// The struct itself is live, exported in a main package, and named only
		// from inside that package, which is the export-narrowing kind's subject
		// rather than this test's: the table is the whole pass over the fixture.
		"go://example.com/evidence#Fixture": vocab.UnnecessaryExport,
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

	if got := namesUnder(result.Findings, vocab.TestOnlyUse); !slices.Equal(got, []string{"OnlyTested"}) {
		t.Errorf("the pass over linux and windows reports %s about %v, want [OnlyTested]: linux imports the package from production code",
			vocab.TestOnlyUse, got)
	}
}

// A package carrying test evidence that one configuration imports from production
// code is production code under every order of the matrix, so a function of it only
// a test references is reported as test-only, and the test as a test of dead code,
// whichever configuration comes first.
func TestATestSupportClassOneConfigurationVetoesHoldsUnderEveryMatrixOrder(t *testing.T) {
	linux := load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}
	windows := load.Configuration{ID: "windows-amd64", OS: "windows", Arch: "amd64"}
	for name, order := range map[string][]load.Configuration{
		"linux first":   {linux, windows},
		"windows first": {windows, linux},
	} {
		t.Run(name, func(t *testing.T) {
			in := inputOf(t, "declarations-test-support-evidence-platform.txtar", applicationConfig(), Consumers{}, order...)
			result := computed(t, in, declarationEmitters())

			want := []string{string(vocab.TestOnlyUse) + " OnlyTested", string(vocab.TestOfDeadCode) + " TestOnlyTested"}
			if got := summary(result.Findings); !slices.Equal(got, want) {
				t.Errorf("the pass over %s reports %v, want %v: linux imports the package from production code", name, got, want)
			}
		})
	}
}

// Test-support code is test code: a declaration of it a test references is reported
// under no code at any minimum confidence, a deprecated one and an interface included,
// and a method of it nothing references is reported under the unused code its
// visibility selects, at certain.
func TestTestSupportCodeATestReferencesIsTestCode(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	in := inputOf(t, "declarations-test-support-possible.txtar", resolved, Consumers{})
	emitters := declarationEmitters()
	emitters[vocab.UnusedInterface] = unusedInterface
	result := computed(t, in, emitters)

	found := findingOf(t, result.Findings, vocab.UnusedExported, "Fake.Get")
	if found.Class != vocab.Certain || found.Confidence != vocab.Certain {
		t.Errorf("%s about Fake.Get has class %s and confidence %s, want certain and certain",
			vocab.UnusedExported, found.Class, found.Confidence)
	}
	if got := summary(result.Findings); len(got) != 1 {
		t.Errorf("the pass over declarations-test-support-possible.txtar reports %v, want the finding about Fake.Get alone", got)
	}
}

func TestCapNameLiteral_lowersADeclarationAStringLiteralSpellsToPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	result := computed(t, inputOf(t, "name-literal.txtar", resolved, Consumers{}), packageEmitters())

	cases := map[string]struct {
		code    vocab.Code
		class   vocab.Class
		literal int
	}{
		"spelled":     {code: vocab.UnusedUnexported, class: vocab.Possible, literal: 10},
		"Kind":        {code: vocab.UnnecessaryExport, class: vocab.Possible, literal: 10},
		"KindA":       {code: vocab.UnnecessaryExport, class: vocab.Possible, literal: 10},
		"selfSpelled": {code: vocab.UnusedUnexported, class: vocab.Certain},
		"testSpelled": {code: vocab.UnusedUnexported, class: vocab.Certain},
		"strings":     {code: vocab.UnusedUnexported, class: vocab.Certain},
		"Kind.Label":  {code: vocab.UnnecessaryExport, class: vocab.Certain},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			found := findingOf(t, result.Findings, want.code, name)
			if found.Class != want.class || found.Confidence != want.class {
				t.Errorf("%s %s has class %s and confidence %s, want %s for both", want.code, name, found.Class, found.Confidence, want.class)
			}
			switch at := found.Details.NameLiteral; {
			case want.literal == 0 && at != nil:
				t.Errorf("%s %s names the literal at %+v, want none", want.code, name, *at)
			case want.literal != 0 && (at == nil || at.Path != "main.go" || at.Line != want.literal):
				t.Errorf("%s %s names the literal at %+v, want main.go:%d", want.code, name, at, want.literal)
			}
		})
	}
}

// An application's package no build links is dead with every declaration of it, its
// init function included, at the possible class, because outside programs may import
// it, and an internal one at certain. A linked package's export keeps certain.
func TestUnlinkedApplicationPackageIsReportedAtPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	result := computed(t, inputOf(t, "declarations-unlinked-package.txtar", resolved, Consumers{}), declarationEmitters())

	for _, test := range []struct {
		code  vocab.Code
		name  string
		class vocab.Class
	}{
		{code: vocab.UnusedExported, name: "Spare", class: vocab.Certain},
		{code: vocab.UnusedExported, name: "Driver", class: vocab.Possible},
		{code: vocab.UnusedExported, name: "New", class: vocab.Possible},
		{code: vocab.UnusedUnexported, name: "register", class: vocab.Possible},
		{code: vocab.UnusedUnexported, name: "init", class: vocab.Possible},
		{code: vocab.UnusedUnexported, name: "load", class: vocab.Certain},
	} {
		t.Run(test.name, func(t *testing.T) {
			found := findingOf(t, result.Findings, test.code, test.name)
			if found.Confidence != test.class {
				t.Errorf("%s about %s has confidence %s, want %s", test.code, test.name, found.Confidence, test.class)
			}
		})
	}
}

// Production code only tests reach that carries no test evidence is reported, but
// only at possible: its test-only use, every finding that exists only because of it,
// the helper it keeps and the test of dead code that calls it, while a declaration of
// it nothing references keeps certain.
func TestProductionCodeOnlyTestsReachIsReportedAtPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	result := computed(t, inputOf(t, "declarations-test-reached.txtar", resolved, Consumers{}), declarationEmitters())

	cases := []struct {
		code              vocab.Code
		name              string
		class, confidence vocab.Class
	}{
		{vocab.TestOnlyUse, "Sample", vocab.Possible, vocab.Possible},
		{vocab.TestOnlyUse, "Seeded", vocab.Possible, vocab.Possible},
		{vocab.UnusedUnexported, "seed", vocab.Possible, vocab.Possible},
		{vocab.TestOfDeadCode, "Start", vocab.Certain, vocab.Possible},
		{vocab.UnusedExported, "Spare", vocab.Certain, vocab.Certain},
	}
	for _, want := range cases {
		t.Run(want.name, func(t *testing.T) {
			found := findingOf(t, result.Findings, want.code, want.name)
			if found.Class != want.class || found.Confidence != want.confidence {
				t.Errorf("%s %s has class %s and confidence %s, want %s and %s",
					want.code, want.name, found.Class, found.Confidence, want.class, want.confidence)
			}
		})
	}
	if got := summary(result.Findings); len(got) != len(cases) {
		t.Errorf("the pass reports %v, want the %d findings above alone", got, len(cases))
	}
}

// The helpers of a test file that only a test of production code only tests reach
// needs are at possible with that test, so the default level reports none of them.
func TestTestFileHelpersOfATestOfProductionCodeOnlyTestsReachAreAtPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	result := computed(t, inputOf(t, "declarations-test-reached-test-helpers.txtar", resolved, Consumers{}), declarationEmitters())

	for _, name := range []string{"helper", "base"} {
		t.Run(name, func(t *testing.T) {
			if found := findingOf(t, result.Findings, vocab.UnusedUnexported, name); found.Confidence != vocab.Possible {
				t.Errorf("%s %s has confidence %s, want possible", vocab.UnusedUnexported, name, found.Confidence)
			}
		})
	}
	if found := findingOf(t, result.Findings, vocab.TestOfDeadCode, "TestSample"); found.Confidence != vocab.Possible {
		t.Errorf("%s TestSample has confidence %s, want possible", vocab.TestOfDeadCode, found.Confidence)
	}

	byDefault := computed(t, inputOf(t, "declarations-test-reached-test-helpers.txtar", applicationConfig(), Consumers{}), declarationEmitters())
	if got := summary(byDefault.Findings); len(got) != 0 {
		t.Errorf("the default pass reports %v, want nothing", got)
	}
}

// A declaration only dead code references in production is a test-only use where a
// test of live code references it, and keeps its unreferenced kind where only a test
// of dead code does.
func TestADeclarationATestOfLiveCodeKeepsIsATestOnlyUse(t *testing.T) {
	result := computed(t, inputOf(t, "declarations-test-kept.txtar", applicationConfig(), Consumers{}), declarationEmitters())

	kept := findingOf(t, result.Findings, vocab.TestOnlyUse, "FixedNow")
	if want := "variable is referenced only from test files and from declarations that are themselves dead"; kept.Message != want {
		t.Errorf("DS1102 FixedNow says %q, want %q", kept.Message, want)
	}
	findingOf(t, result.Findings, vocab.UnusedExported, "Frozen")
	findingOf(t, result.Findings, vocab.TestOfDeadCode, "TestStale")
}

// Under an internal tree a declaration a test of live code keeps is the test-only
// use alone, and one only a test of dead code reads is the unreachable export.
func TestADeclarationATestOfLiveCodeKeepsIsNoUnreachableExport(t *testing.T) {
	emitters := declarationEmitters()
	emitters[vocab.UnreachableExport] = unreachableExport
	result := computed(t, inputOf(t, "declarations-test-kept-internal.txtar", applicationConfig(), Consumers{}), emitters)

	findingOf(t, result.Findings, vocab.TestOnlyUse, "FixedNow")
	findingOf(t, result.Findings, vocab.UnreachableExport, "Frozen")
}

// Every kind a test's use of production code only tests reach decides reports that
// declaration at possible, and a declaration nothing references at certain.
func TestEveryKindOfProductionCodeOnlyTestsReachIsAtPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	emitters := declarationEmitters()
	emitters[vocab.UnusedInterface] = unusedInterface
	result := computed(t, inputOf(t, "declarations-test-reached-kinds.txtar", resolved, Consumers{}), emitters)

	cases := []struct {
		code  vocab.Code
		name  string
		class vocab.Class
	}{
		{vocab.UnusedInterface, "Doer", vocab.Possible},
		{vocab.DeprecatedUnused, "Old", vocab.Possible},
		{vocab.UnusedUnexported, "helper", vocab.Possible},
		{vocab.UnusedExported, "Spare", vocab.Certain},
	}
	for _, want := range cases {
		t.Run(want.name, func(t *testing.T) {
			found := findingOf(t, result.Findings, want.code, want.name)
			if found.Class != want.class || found.Confidence != want.class {
				t.Errorf("%s %s has class %s and confidence %s, want %s",
					want.code, want.name, found.Class, found.Confidence, want.class)
			}
		})
	}
}

// A field of production code only tests reach that only a test reads is under the
// same tier, while a field of the command a test alone reads keeps its class.
func TestAFieldOfProductionCodeOnlyTestsReachThatOnlyTestsReadIsPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	emitters := declarationEmitters()
	emitters[vocab.WriteOnlySymbol] = writeOnlySymbol
	result := computed(t, inputOf(t, "declarations-test-reached-field-read-by-tests.txtar", resolved, Consumers{}), emitters)

	cases := []struct {
		name  string
		class vocab.Class
	}{
		{"Volume.Initialized", vocab.Possible},
		{"Disk.Ready", vocab.Certain},
	}
	for _, want := range cases {
		t.Run(want.name, func(t *testing.T) {
			found := findingOf(t, result.Findings, vocab.WriteOnlySymbol, want.name)
			if found.Class != want.class || found.Confidence != want.class {
				t.Errorf("%s %s has class %s and confidence %s, want %s",
					vocab.WriteOnlySymbol, want.name, found.Class, found.Confidence, want.class)
			}
		})
	}
}

// A dead root beside production code only tests reach keeps its own class, and so
// does what only it reaches, though it shares a dead declaration with that code.
func TestADeadRootBesideProductionCodeOnlyTestsReachKeepsItsClass(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	result := computed(t, inputOf(t, "declarations-test-reached-beside-a-dead-root.txtar", resolved, Consumers{}), declarationEmitters())

	cases := []struct {
		code              vocab.Code
		name              string
		class, confidence vocab.Class
	}{
		{vocab.UnusedExported, "Legacy", vocab.Certain, vocab.Certain},
		{vocab.UnusedUnexported, "only", vocab.Certain, vocab.Certain},
		{vocab.UnusedExported, "Shared", vocab.Possible, vocab.Possible},
		{vocab.UnusedUnexported, "shared", vocab.Possible, vocab.Possible},
		{vocab.TestOnlyUse, "Make", vocab.Possible, vocab.Possible},
		{vocab.TestOfDeadCode, "TestMake", vocab.Certain, vocab.Possible},
	}
	for _, want := range cases {
		t.Run(want.name, func(t *testing.T) {
			found := findingOf(t, result.Findings, want.code, want.name)
			if found.Class != want.class || found.Confidence != want.confidence {
				t.Errorf("%s %s has class %s and confidence %s, want %s and %s",
					want.code, want.name, found.Class, found.Confidence, want.class, want.confidence)
			}
		})
	}
}

// A test of dead code that also tests a dead declaration outside production code
// only tests reach keeps its own confidence, so it is never withheld while that
// declaration, which it references, is reported.
func TestATestOfDeadCodeBesideADeadRootKeepsItsConfidence(t *testing.T) {
	result := computed(t, inputOf(t, "declarations-test-reached-and-dead-root-tested.txtar", applicationConfig(), Consumers{}), declarationEmitters())

	if found := findingOf(t, result.Findings, vocab.TestOfDeadCode, "TestBoth"); found.Confidence != vocab.Certain {
		t.Errorf("DS1103 TestBoth has confidence %s, want certain", found.Confidence)
	}
}

// Under the default minimum a dead root beside production code only tests reach is
// reported with what only it reaches, and the rest of its component is withheld.
func TestADeadRootBesideProductionCodeOnlyTestsReachIsReportedByDefault(t *testing.T) {
	result := computed(t, inputOf(t, "declarations-test-reached-beside-a-dead-root.txtar", applicationConfig(), Consumers{}), declarationEmitters())

	if got, want := summary(result.Findings), []string{string(vocab.UnusedExported) + " Legacy", string(vocab.UnusedUnexported) + " only"}; !slices.Equal(got, want) {
		t.Errorf("the pass reports %v, want %v", got, want)
	}
}

// Under the default minimum the findings of production code only tests reach are
// withheld, and the one about a declaration nothing references is reported.
func TestProductionCodeOnlyTestsReachIsWithheldByDefault(t *testing.T) {
	result := computed(t, inputOf(t, "declarations-test-reached.txtar", applicationConfig(), Consumers{}), declarationEmitters())

	if got, want := summary(result.Findings), []string{string(vocab.UnusedExported) + " Spare"}; !slices.Equal(got, want) {
		t.Errorf("the pass reports %v, want %v", got, want)
	}
	if len(result.Withheld) != 4 {
		t.Errorf("the pass withholds %v, want the four findings of the component Sample and Seeded root", summary(result.Withheld))
	}
}
