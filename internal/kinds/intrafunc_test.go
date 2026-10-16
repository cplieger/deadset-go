package kinds

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// intraFuncEmitters is the table of the intra-function group, which is what a pass
// over a fixture of these kinds computes with.
func intraFuncEmitters() map[vocab.Code]Emitter {
	return map[vocab.Code]Emitter{
		vocab.UnusedParameter:      unusedParameter,
		vocab.UnusedReceiver:       unusedReceiver,
		vocab.UnusedResult:         unusedResult,
		vocab.UnreachableStatement: unreachableStatement,
		vocab.DeadStore:            deadStore,
		vocab.UnreachableCase:      unreachableCase,
	}
}

// intraFuncFixtures is every fixture of this group, which is what the message rule
// is measured over.
func intraFuncFixtures(t *testing.T) []string {
	t.Helper()

	var held []string
	for _, archive := range fixtures(t) {
		if strings.HasPrefix(archive, "intrafunc-") {
			held = append(held, archive)
		}
	}
	if len(held) == 0 {
		t.Fatal("Setup: testdata holds no fixture of the intra-function group")
	}
	return held
}

// subjectsOf is the position, the subject kind, the subject name and the enclosing
// declaration's reference of every finding of one code, in the order the pass
// returned them, which is one line per subject a failure message can be read from.
func subjectsOf(findings []Finding, code vocab.Code) []string {
	var held []string
	for i := range findings {
		found := &findings[i]
		if found.Code != code {
			continue
		}
		held = append(held, fmt.Sprintf("%s:%d:%d %s %s of %s",
			found.Position.Path, found.Position.Line, found.Position.Column,
			found.Symbol.Kind, found.Symbol.Name, found.Symbol.Ref))
	}
	return held
}

func TestUnusedParameterReportsEveryParameterNoBodyReads(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-parameter.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{
		"main.go:4:24 parameter label of go://example.com/app#scaled(label)",
		"main.go:26:25 parameter label of go://example.com/app#dropped(label)",
	}
	if got := subjectsOf(result.Findings, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-parameter.txtar reports %v under %s, want %v",
			got, vocab.UnusedParameter, want)
	}
}

func TestUnusedReceiverReportsAReceiverNoBodyReads(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-receiver.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{"main.go:9:7 receiver c of go://example.com/app#counter.limit"}
	if got := subjectsOf(result.Findings, vocab.UnusedReceiver); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-receiver.txtar reports %v under %s, want %v",
			got, vocab.UnusedReceiver, want)
	}
}

func TestUnusedReceiverReportsAReceiverWhateverFixesTheSignature(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-receiver-fixed.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{
		"main.go:13:7 receiver f of go://example.com/app#fake.reset",
		"main.go:15:7 receiver f of go://example.com/app#fake.tick",
		"main.go:17:7 receiver f of go://example.com/app#fake.name",
	}
	if got := subjectsOf(result.Findings, vocab.UnusedReceiver); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-receiver-fixed.txtar reports %v under %s, want %v",
			got, vocab.UnusedReceiver, want)
	}
}

func TestUnusedParameterReportsALiteralOnlyCalledByName(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-literal.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{
		"main.go:8:17 parameter name of go://example.com/app#main(name)",
		"main.go:9:22 parameter name of go://example.com/app#main(name)",
	}
	if got := subjectsOf(result.Findings, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-literal.txtar reports %v under %s, want %v: only a literal every use calls is free",
			got, vocab.UnusedParameter, want)
	}
}

func TestUnusedResultReportsEveryResultEveryCallDiscards(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-result.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{
		"main.go:4:14 result result 1 of go://example.com/app#tally",
		"main.go:9:15 result result 1 of go://example.com/app#split",
	}
	if got := subjectsOf(result.Findings, vocab.UnusedResult); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-result.txtar reports %v under %s, want %v",
			got, vocab.UnusedResult, want)
	}
}

// A program run on its own calls what it names from outside the reference set, so no
// result of a function it names is discarded at every call site.
func TestUnusedResultSparesAFunctionAProgramNames(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-result-program.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{"tally/tally.go:5:14 result result 1 of go://example.com/app/tally#Spare"}
	if got := subjectsOf(result.Findings, vocab.UnusedResult); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-result-program.txtar reports %v under %s, want %v",
			got, vocab.UnusedResult, want)
	}
}

func TestUnreachableStatementReportsTheStatementAtThePassesOwnPosition(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-statement.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{"main.go:7:2 statement halted of go://example.com/app#halted"}
	if got := subjectsOf(result.Findings, vocab.UnreachableStatement); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-statement.txtar reports %v under %s, want %v",
			got, vocab.UnreachableStatement, want)
	}
}

func TestDeadStoreReportsEveryStoreNoReadReaches(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-store.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{
		"main.go:5:2 store count of go://example.com/app#overwritten",
		"main.go:14:2 store count of go://example.com/app#trailing",
		"main.go:28:2 store count of go://example.com/app#captured",
		"main.go:40:2 store seen of go://example.com/app#immediate",
		"signature.go:6:10 store err of go://example.com/app#scraped",
		"signature.go:8:10 store err of go://example.com/app#scraped",
		"signature.go:30:2 store n of go://example.com/app#rebound",
		"signature.go:39:2 store c of go://example.com/app#requeried",
	}
	if got := subjectsOf(result.Findings, vocab.DeadStore); !slices.Equal(got, want) {
		t.Fatalf("the pass over intrafunc-store.txtar reports %v under %s, want %v",
			got, vocab.DeadStore, want)
	}
	found := findingOf(t, result.Findings, vocab.DeadStore, "count")
	if len(found.Details.WritePositions) != 1 || found.Details.WritePositions[0] != found.Position {
		t.Errorf("%s about %s carries write positions %v, want the store's own position %v",
			vocab.DeadStore, found.Symbol.Name, found.Details.WritePositions, found.Position)
	}
}

func TestUnreachableCaseReportsACaseAnEarlierCaseAlwaysMatchesBefore(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-case.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{"main.go:19:2 case classify of go://example.com/app#classify"}
	if got := subjectsOf(result.Findings, vocab.UnreachableCase); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-case.txtar reports %v under %s, want %v",
			got, vocab.UnreachableCase, want)
	}
}

func TestTheSignatureKindsApplyEveryExemptionOfTheContractByName(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-exemptions.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{"main.go:39:23 parameter label of go://example.com/app#plain(label)"}
	if got := subjectsOf(result.Findings, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-exemptions.txtar reports %v under %s, want %v: a signature no caller outside the graph fixes is the only one reported",
			got, vocab.UnusedParameter, want)
	}
}

func TestUnusedParameterReportsNoParameterTheTestDriverFixes(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-test-driver.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)
	reported := subjectsOf(result.Findings, vocab.UnusedParameter)

	tests := []struct {
		name string
		at   string
	}{
		{name: "TestMain", at: "main_test.go:10:15 parameter m of go://example.com/app#TestMain(m)"},
		{name: "test", at: "main_test.go:14:18 parameter t of go://example.com/app#TestCompute(t)"},
		{name: "benchmark", at: "main_test.go:22:23 parameter b of go://example.com/app#BenchmarkCompute(b)"},
		{name: "fuzz_test", at: "main_test.go:26:18 parameter f of go://example.com/app#FuzzCompute(f)"},
		{name: "fuzz_target", at: "testsupport/testsupport.go:11:13 parameter t of go://example.com/app/testsupport#Target(t)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if slices.Contains(reported, test.at) {
				t.Errorf("the pass over intrafunc-test-driver.txtar reports %s under %s, a parameter whose signature the test driver fixes",
					test.at, vocab.UnusedParameter)
			}
		})
	}
}

func TestUnusedParameterReportsATestSupportFunctionTheTestDriverDoesNotRun(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-test-driver.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{"testsupport/testsupport.go:6:12 parameter t of go://example.com/app/testsupport#Check(t)"}
	if got := subjectsOf(result.Findings, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-test-driver.txtar reports %v under %s, want %v",
			got, vocab.UnusedParameter, want)
	}
}

// A parameter and a receiver are dead by the body that never reads them, so a
// published declaration of a library is reported whatever the run knows about the
// library's consumers: no caller outside the graph can make a body read one. The
// signature an interface the receiver's type reaches fixes is retained by the
// exemption class that records that reach, which is what keeps the satisfying
// method's parameter out of the answer.
func TestTheSignatureKindsReportAPublishedDeclarationOfALibrary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup setup
	}{
		{name: "a library whose consumer set is not declared complete", setup: asLibrary},
		{name: "an application, whose every caller is in the graph", setup: asApplication},
	}
	wantParameters := []string{
		"api/api.go:4:24 parameter label of go://example.com/app/api#Scaled(label)",
		"api/api.go:9:24 parameter label of go://example.com/app/api#scaled(label)",
		"api/writer.go:15:20 parameter limit of go://example.com/app/api#Sink.Held(limit)",
	}
	wantReceivers := []string{"api/writer.go:15:7 receiver s of go://example.com/app/api#Sink.Held"}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := analysisOf(t, "intrafunc-published.txtar", test.setup, Consumers{}).findings(t, intraFunctionKinds)

			kind := test.setup.resolved().Target.Kind.Of(vocab.Go)
			if got := subjectsOf(result.Findings, vocab.UnusedParameter); !slices.Equal(got, wantParameters) {
				t.Errorf("the pass over intrafunc-published.txtar as %s reports %v under %s, want %v",
					kind, got, vocab.UnusedParameter, wantParameters)
			}
			if got := subjectsOf(result.Findings, vocab.UnusedReceiver); !slices.Equal(got, wantReceivers) {
				t.Errorf("the pass over intrafunc-published.txtar as %s reports %v under %s, want %v",
					kind, got, vocab.UnusedReceiver, wantReceivers)
			}
		})
	}
}

// An edit to a parameter of a published declaration is an edit to every call site,
// including the ones outside the graph, which is what the vocabulary's fixability for
// the kind says; deleting a receiver name changes no signature, which is what the
// receiver kind's says.
func TestTheSignatureKindsCarryTheFixabilityOfTheirOwnEditOnAPublishedDeclaration(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-published.txtar", asLibrary, Consumers{}).findings(t, intraFunctionKinds)

	for code, want := range map[vocab.Code]string{
		vocab.UnusedParameter: "manual",
		vocab.UnusedReceiver:  "deletable",
	} {
		got := fixabilitiesOf(result.Findings, code, "go://example.com/app/api#")
		if len(got) == 0 {
			t.Errorf("the pass over intrafunc-published.txtar reports nothing under %s about the published package", code)
		}
		for _, one := range got {
			if one != want {
				t.Errorf("the pass over intrafunc-published.txtar reports a published %s finding with fixability %q, want %q",
					code, one, want)
			}
		}
	}
}

// fixabilitiesOf is the fixability of every finding of one code whose subject's
// reference begins with prefix.
func fixabilitiesOf(findings []Finding, code vocab.Code, prefix string) []string {
	var held []string
	for i := range findings {
		found := &findings[i]
		if found.Code == code && strings.HasPrefix(found.Symbol.Ref, prefix) {
			held = append(held, string(found.Fixability))
		}
	}
	return held
}

func TestTheUnusedResultKindReportsOnlyWhereEveryCallSiteIsLoaded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup setup
		want  []string
	}{
		{
			name:  "a library whose consumer set is not declared complete",
			setup: asLibrary,
			want:  []string{"api/api.go:9:14 result result 1 of go://example.com/app/api#tally"},
		},
		{
			name:  "an application, whose every caller is in the graph",
			setup: asApplication,
			want: []string{
				"api/api.go:4:14 result result 1 of go://example.com/app/api#Tally",
				"api/api.go:9:14 result result 1 of go://example.com/app/api#tally",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := analysisOf(t, "intrafunc-outofgraph.txtar", test.setup, Consumers{}).findings(t, intraFunctionKinds)

			if got := subjectsOf(result.Findings, vocab.UnusedResult); !slices.Equal(got, test.want) {
				t.Errorf("the pass over intrafunc-outofgraph.txtar as %s reports %v under %s, want %v",
					test.setup.resolved().Target.Kind.Of(vocab.Go), got, vocab.UnusedResult, test.want)
			}
		})
	}
}

func TestASuppressionBoundToADeclarationSilencesTheFindingAboutItsPart(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "intrafunc-suppressed.txtar", applicationConfig(), Consumers{})
	marks, refusals, err := suppress.Inline(in.Per[0].Result, in.Per[0].Resolve, in.Per[0].Symbols)
	if err != nil {
		t.Fatalf("Setup: suppress.Inline(intrafunc-suppressed.txtar): %v", err)
	}
	if len(refusals) != 0 || len(marks) != 1 || marks[0].Bound == "" {
		t.Fatalf("Setup: the fixture's directive read as %v with refusals %v, want one bound record",
			marks, refusals)
	}

	before := computed(t, in, intraFuncEmitters())
	in.Marks = marks
	after := computed(t, in, intraFuncEmitters())

	wantBefore := []string{
		"main.go:4:24 parameter label of go://example.com/app#scaled(label)",
		"main.go:9:25 parameter label of go://example.com/app#tripled(label)",
	}
	if got := subjectsOf(before.Findings, vocab.UnusedParameter); !slices.Equal(got, wantBefore) {
		t.Fatalf("the pass with no suppression reports %v under %s, want %v",
			got, vocab.UnusedParameter, wantBefore)
	}
	wantAfter := wantBefore[1:]
	if got := subjectsOf(after.Findings, vocab.UnusedParameter); !slices.Equal(got, wantAfter) {
		t.Errorf("the pass with the fixture's directive reports %v under %s, want %v",
			got, vocab.UnusedParameter, wantAfter)
	}
}

func TestTheIntraFunctionGroupIsEnabledByDefaultAndSilencedByCodeAndByFamily(t *testing.T) {
	t.Parallel()

	everyCode := []vocab.Code{
		vocab.UnusedParameter, vocab.UnusedReceiver, vocab.UnusedResult,
		vocab.UnreachableStatement, vocab.DeadStore, vocab.UnreachableCase,
	}
	tests := []struct {
		name     string
		severity map[string]vocab.Severity
		want     []vocab.Code
	}{
		{
			name: "a configuration with no severity section",
			want: everyCode,
		},
		{
			name:     "one code of the group silenced",
			severity: map[string]vocab.Severity{string(vocab.DeadStore): vocab.Allow},
			want: []vocab.Code{
				vocab.UnusedParameter, vocab.UnusedReceiver, vocab.UnusedResult,
				vocab.UnreachableStatement, vocab.UnreachableCase,
			},
		},
		{
			name:     "the family prefix silenced",
			severity: map[string]vocab.Severity{"DS14": vocab.Allow},
			want:     nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolved := applicationConfig()
			maps.Copy(resolved.Severity, test.severity)
			in := inputOf(t, "intrafunc-group.txtar", resolved, Consumers{})

			result := computed(t, in, intraFuncEmitters())

			if got := codesOf(result.Findings); !slices.Equal(got, test.want) {
				t.Errorf("the pass over intrafunc-group.txtar reports %v, want %v: the pass reports %v",
					got, test.want, summary(result.Findings))
			}
		})
	}
}

func TestEveryIntraFunctionFindingCarriesTheOverlapTheVocabularyCarriesAndNamesItNowhereElse(t *testing.T) {
	t.Parallel()

	published := overlapVocabulary(t)
	for _, archive := range intraFuncFixtures(t) {
		t.Run(archive, func(t *testing.T) {
			t.Parallel()

			found := analysisOf(t, archive, asApplication, Consumers{}).findings(t, intraFunctionKinds).Findings

			if len(found) == 0 {
				t.Fatalf("the kinds report nothing about %s, so the overlap rule is measured over nothing",
					archive)
			}
			for i := range found {
				one := &found[i]
				want := published[one.Code]
				if len(want) == 0 {
					t.Errorf("the vocabulary publishes no Go overlap for %s, which every finding of the kind carries",
						one.Code)
					continue
				}
				if !slices.Equal(one.Details.Overlap, want) {
					t.Errorf("%s about %s carries the overlap %v, want %v, the list the vocabulary publishes",
						one.Code, one.Symbol.Name, one.Details.Overlap, want)
				}
				for _, named := range want {
					if strings.Contains(one.Message, named) {
						t.Errorf("%s about %s carries the message %q, which names %q: the overlap is the member's, and one fact has one member",
							one.Code, one.Symbol.Name, one.Message, named)
					}
				}
			}
		})
	}
}

func TestUnusedParameterReportsOneFindingPerParameterOfOneSignature(t *testing.T) {
	t.Parallel()

	found := analysisOf(t, "intrafunc-twoparts.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds).Findings

	want := []string{
		"main.go:9:25 parameter low of go://example.com/app#counter.limit(low)",
		"main.go:9:30 parameter high of go://example.com/app#counter.limit(high)",
	}
	if got := subjectsOf(found, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("the kind reports %v under %s, want %v: each part of a signature is a subject of its own",
			got, vocab.UnusedParameter, want)
	}
	wantReceiver := []string{"main.go:9:7 receiver c of go://example.com/app#counter.limit"}
	if got := subjectsOf(found, vocab.UnusedReceiver); !slices.Equal(got, wantReceiver) {
		t.Errorf("the kind reports %v under %s, want %v",
			got, vocab.UnusedReceiver, wantReceiver)
	}
}

// overlapVocabulary is the overlap list the vocabulary publishes per code, for the
// language this analyzer reports.
func overlapVocabulary(t *testing.T) map[vocab.Code][]string {
	t.Helper()

	const kindsPath = "contract/kinds.json"
	body, err := spec.Contract.ReadFile(kindsPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", kindsPath, err)
	}
	var document struct {
		Kinds []struct {
			Code    vocab.Code          `json:"code"`
			Overlap map[string][]string `json:"overlap"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", kindsPath, err)
	}
	published := make(map[vocab.Code][]string)
	for _, row := range document.Kinds {
		if named := row.Overlap[string(language)]; len(named) > 0 {
			published[row.Code] = named
		}
	}
	if len(published) == 0 {
		t.Fatalf("Setup: %s publishes no overlap for %s, so this test pins nothing", kindsPath, language)
	}
	return published
}

func TestEveryKindTheVocabularyPublishesAnOverlapForIsAKindOfThisGroup(t *testing.T) {
	t.Parallel()

	published := overlapVocabulary(t)
	table := intraFuncEmitters()
	for code := range published {
		if _, emitted := table[code]; !emitted {
			t.Errorf("the vocabulary publishes a Go overlap for %s, which this group emits nothing under: the member is carried by the kinds an external rule also reports",
				code)
		}
	}
	for code := range table {
		if len(published[code]) == 0 {
			t.Errorf("the vocabulary publishes no Go overlap for %s, which the schema requires the member on", code)
		}
	}
}

func TestEveryIntraFunctionSubjectIsAPartOfTheContractsSubjectVocabulary(t *testing.T) {
	t.Parallel()

	const schemaPath = "contract/finding.schema.json"
	body, err := spec.Contract.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", schemaPath, err)
	}
	var schema struct {
		Properties struct {
			Symbol struct {
				Properties struct {
					Kind struct {
						Enum []string `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"symbol"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatalf("Setup: decode %s: %v", schemaPath, err)
	}
	vocabulary := schema.Properties.Symbol.Properties.Kind.Enum
	if len(vocabulary) == 0 {
		t.Fatalf("Setup: %s declares no subject vocabulary, so this test pins nothing", schemaPath)
	}
	for _, word := range []string{
		parameterSubject, receiverSubject, resultSubject,
		statementSubject, caseSubject, storeSubject,
	} {
		if !slices.Contains(vocabulary, word) {
			t.Errorf("the subject %q is outside the vocabulary %s declares: %v",
				word, schemaPath, vocabulary)
		}
	}
}

// A function another file of its package declares for another build
// configuration has the signature every configuration's callers call, so a
// parameter one platform's body leaves unread is that platform's answer and no
// finding. The same body declared once is reported, and so is an unread receiver
// of a method declared per platform, whose name is no part of the signature.
func TestUnusedParameterReportsNothingAboutAFunctionDeclaredOncePerPlatform(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-platform-twins.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	for code, want := range map[vocab.Code][]string{
		vocab.UnusedParameter: {"main.go:9:24 parameter mode of go://example.com/app#solo(mode)"},
		vocab.UnusedReceiver:  {"open_linux.go:5:7 receiver d of go://example.com/app#disk.free"},
	} {
		if got := subjectsOf(result.Findings, code); !slices.Equal(got, want) {
			t.Errorf("the pass over intrafunc-platform-twins.txtar reports %v under %s, want %v", got, code, want)
		}
	}
}

func TestAMethodATypeErrorRootsHasNoSignatureFreeToChange(t *testing.T) {
	result := analysisOf(t, "intrafunc-type-error.txtar", asApplication, Consumers{}).findings(t, everyKind)

	var reported []string
	for i := range result.Findings {
		if result.Findings[i].Code == vocab.UnusedParameter {
			reported = append(reported, result.Findings[i].Symbol.Ref+" "+result.Findings[i].Symbol.Name)
		}
	}
	want := []string{"go://example.com/app#plain.Use(x) x"}
	if !slices.Equal(reported, want) {
		t.Errorf("the pass reports %s about %v, want %v: the interface the skipped open returns into may fix Filter's signature",
			vocab.UnusedParameter, reported, want)
	}
}

func TestDeadStoreReportsStoresTheBodysOwnGraphDoesNotOrder(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-store-flow.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{
		"main.go:8:2 store n of go://example.com/app#",
		"main.go:8:5 store err of go://example.com/app#",
		"main.go:14:2 store n of go://example.com/app#handler",
		"main.go:21:3 store x of go://example.com/app#masked",
		"main.go:35:6 store err of go://example.com/app#handle",
		"main.go:47:2 store total of go://example.com/app#looped",
		"main.go:64:2 store k of go://example.com/app#counted",
	}
	if got := subjectsOf(result.Findings, vocab.DeadStore); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-store-flow.txtar reports %v under %s, want %v",
			got, vocab.DeadStore, want)
	}
}

func TestUnusedParameterReportsALiteralCalledWhereItIsWritten(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "intrafunc-literal-callee.txtar", asApplication, Consumers{}).findings(t, intraFunctionKinds)

	want := []string{
		"main.go:5:14 parameter n of go://example.com/app#(n)",
		"main.go:9:10 parameter i of go://example.com/app#main(i)",
		"main.go:11:13 parameter label of go://example.com/app#main(label)",
		"main.go:12:7 parameter n of go://example.com/app#main(n)",
	}
	if got := subjectsOf(result.Findings, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-literal-callee.txtar reports %v under %s, want %v",
			got, vocab.UnusedParameter, want)
	}
}

func TestUnusedUnexportedReportsALocalConstantOrTypeItsBodyNeverReferences(t *testing.T) {
	t.Parallel()

	result := analysisOf(t, "declarations-locals.txtar", asApplication, Consumers{}).findings(t, declarationKinds)

	want := []string{
		"main.go:4:8 constant width of go://example.com/app#",
		"main.go:10:8 constant unused of go://example.com/app#shapes",
		"main.go:11:7 type label of go://example.com/app#shapes",
		"main.go:14:7 type node of go://example.com/app#shapes",
		"main.go:16:8 constant derived of go://example.com/app#shapes",
	}
	if got := subjectsOf(result.Findings, vocab.UnusedUnexported); !slices.Equal(got, want) {
		t.Fatalf("the pass over declarations-locals.txtar reports %v under %s, want %v",
			got, vocab.UnusedUnexported, want)
	}
	found := findingOf(t, result.Findings, vocab.UnusedUnexported, "unused")
	if found.Live || found.Relation != graph.ReferenceCounting || found.Confidence != vocab.Certain {
		t.Errorf("%s about the local constant unused carries live %t, relation %s, confidence %s, want a reference-counting finding at %s",
			vocab.UnusedUnexported, found.Live, found.Relation, found.Confidence, vocab.Certain)
	}
}
