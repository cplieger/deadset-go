package kinds

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/suppress"
	spec "github.com/cplieger/deadset-spec"
)

// intraFuncEmitters is the table of the intra-function group, which is what a pass
// over a fixture of these kinds computes with.
func intraFuncEmitters() map[string]Emitter {
	return map[string]Emitter{
		unusedParameterCode:      UnusedParameter,
		unusedReceiverCode:       UnusedReceiver,
		unusedResultCode:         UnusedResult,
		unreachableStatementCode: UnreachableStatement,
		deadStoreCode:            DeadStore,
		unreachableCaseCode:      UnreachableCase,
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
func subjectsOf(findings []Finding, code string) []string {
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

	in := inputOf(t, "intrafunc-parameter.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, intraFuncEmitters())

	want := []string{"main.go:4:24 parameter label of go://example.com/app#scaled"}
	if got := subjectsOf(result.Findings, unusedParameterCode); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-parameter.txtar reports %v under %s, want %v",
			got, unusedParameterCode, want)
	}
}

func TestUnusedReceiverReportsAReceiverNoBodyReads(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "intrafunc-receiver.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, intraFuncEmitters())

	want := []string{"main.go:9:7 receiver c of go://example.com/app#counter.limit"}
	if got := subjectsOf(result.Findings, unusedReceiverCode); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-receiver.txtar reports %v under %s, want %v",
			got, unusedReceiverCode, want)
	}
}

func TestUnusedResultReportsEveryResultEveryCallDiscards(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "intrafunc-result.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, intraFuncEmitters())

	want := []string{
		"main.go:4:14 result result 1 of go://example.com/app#tally",
		"main.go:9:15 result result 1 of go://example.com/app#split",
	}
	if got := subjectsOf(result.Findings, unusedResultCode); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-result.txtar reports %v under %s, want %v",
			got, unusedResultCode, want)
	}
}

func TestUnreachableStatementReportsTheStatementAtThePassesOwnPosition(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "intrafunc-statement.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, intraFuncEmitters())

	want := []string{"main.go:7:2 statement halted of go://example.com/app#halted"}
	if got := subjectsOf(result.Findings, unreachableStatementCode); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-statement.txtar reports %v under %s, want %v",
			got, unreachableStatementCode, want)
	}
}

func TestDeadStoreReportsEveryStoreNoReadReaches(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "intrafunc-store.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, intraFuncEmitters())

	want := []string{
		"main.go:5:2 store count of go://example.com/app#overwritten",
		"main.go:14:2 store count of go://example.com/app#trailing",
	}
	if got := subjectsOf(result.Findings, deadStoreCode); !slices.Equal(got, want) {
		t.Fatalf("the pass over intrafunc-store.txtar reports %v under %s, want %v",
			got, deadStoreCode, want)
	}
	found := findingOf(t, result.Findings, deadStoreCode, "count")
	if len(found.Details.WritePositions) != 1 || found.Details.WritePositions[0] != found.Position {
		t.Errorf("%s about %s carries write positions %v, want the store's own position %v",
			deadStoreCode, found.Symbol.Name, found.Details.WritePositions, found.Position)
	}
}

func TestUnreachableCaseReportsACaseAnEarlierCaseAlwaysMatchesBefore(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "intrafunc-case.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, intraFuncEmitters())

	want := []string{"main.go:19:2 case classify of go://example.com/app#classify"}
	if got := subjectsOf(result.Findings, unreachableCaseCode); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-case.txtar reports %v under %s, want %v",
			got, unreachableCaseCode, want)
	}
}

func TestTheSignatureKindsApplyEveryExemptionOfTheContractByName(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "intrafunc-exemptions.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, intraFuncEmitters())

	want := []string{"main.go:39:23 parameter label of go://example.com/app#plain"}
	if got := subjectsOf(result.Findings, unusedParameterCode); !slices.Equal(got, want) {
		t.Errorf("the pass over intrafunc-exemptions.txtar reports %v under %s, want %v: a signature no caller outside the graph fixes is the only one reported",
			got, unusedParameterCode, want)
	}
}

func TestTheUnusedParameterKindReportsAPublishedSignatureOnlyUnderAClosedWorld(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resolved config.Config
		want     []string
	}{
		{
			name:     "a library whose consumer set is not declared complete",
			resolved: libraryConfig(),
			want:     []string{"api/api.go:9:24 parameter label of go://example.com/app/api#scaled"},
		},
		{
			name:     "an application, whose every caller is in the graph",
			resolved: applicationConfig(),
			want: []string{
				"api/api.go:4:24 parameter label of go://example.com/app/api#Scaled",
				"api/api.go:9:24 parameter label of go://example.com/app/api#scaled",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			in := inputOf(t, "intrafunc-published.txtar", test.resolved, Consumers{})

			result := computed(t, in, intraFuncEmitters())

			if got := subjectsOf(result.Findings, unusedParameterCode); !slices.Equal(got, test.want) {
				t.Errorf("the pass over intrafunc-published.txtar as %s reports %v under %s, want %v",
					test.resolved.Target.Kind, got, unusedParameterCode, test.want)
			}
		})
	}
}

func TestTheUnusedResultKindReportsOnlyWhereEveryCallSiteIsLoaded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resolved config.Config
		want     []string
	}{
		{
			name:     "a library whose consumer set is not declared complete",
			resolved: libraryConfig(),
			want:     []string{"api/api.go:9:14 result result 1 of go://example.com/app/api#tally"},
		},
		{
			name:     "an application, whose every caller is in the graph",
			resolved: applicationConfig(),
			want: []string{
				"api/api.go:4:14 result result 1 of go://example.com/app/api#Tally",
				"api/api.go:9:14 result result 1 of go://example.com/app/api#tally",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			in := inputOf(t, "intrafunc-outofgraph.txtar", test.resolved, Consumers{})

			result := computed(t, in, intraFuncEmitters())

			if got := subjectsOf(result.Findings, unusedResultCode); !slices.Equal(got, test.want) {
				t.Errorf("the pass over intrafunc-outofgraph.txtar as %s reports %v under %s, want %v",
					test.resolved.Target.Kind, got, unusedResultCode, test.want)
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
		"main.go:4:24 parameter label of go://example.com/app#scaled",
		"main.go:9:25 parameter label of go://example.com/app#tripled",
	}
	if got := subjectsOf(before.Findings, unusedParameterCode); !slices.Equal(got, wantBefore) {
		t.Fatalf("the pass with no suppression reports %v under %s, want %v",
			got, unusedParameterCode, wantBefore)
	}
	wantAfter := wantBefore[1:]
	if got := subjectsOf(after.Findings, unusedParameterCode); !slices.Equal(got, wantAfter) {
		t.Errorf("the pass with the fixture's directive reports %v under %s, want %v",
			got, unusedParameterCode, wantAfter)
	}
}

func TestTheIntraFunctionGroupIsEnabledByDefaultAndSilencedByCodeAndByFamily(t *testing.T) {
	t.Parallel()

	everyCode := []string{
		unusedParameterCode, unusedReceiverCode, unusedResultCode,
		unreachableStatementCode, deadStoreCode, unreachableCaseCode,
	}
	tests := []struct {
		name     string
		severity map[string]config.Severity
		want     []string
	}{
		{
			name: "a configuration with no severity section",
			want: everyCode,
		},
		{
			name:     "one code of the group silenced",
			severity: map[string]config.Severity{deadStoreCode: config.Allow},
			want: []string{
				unusedParameterCode, unusedReceiverCode, unusedResultCode,
				unreachableStatementCode, unreachableCaseCode,
			},
		},
		{
			name:     "the family prefix silenced",
			severity: map[string]config.Severity{"DS18": config.Allow},
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

			in := inputOf(t, archive, applicationConfig(), Consumers{})

			found := computed(t, in, intraFuncEmitters()).Findings

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

	in := inputOf(t, "intrafunc-twoparts.txtar", applicationConfig(), Consumers{})

	found := computed(t, in, intraFuncEmitters()).Findings

	want := []string{
		"main.go:9:25 parameter low of go://example.com/app#counter.limit",
		"main.go:9:30 parameter high of go://example.com/app#counter.limit",
	}
	if got := subjectsOf(found, unusedParameterCode); !slices.Equal(got, want) {
		t.Errorf("the kind reports %v under %s, want %v: each part of a signature is a subject of its own",
			got, unusedParameterCode, want)
	}
	wantReceiver := []string{"main.go:9:7 receiver c of go://example.com/app#counter.limit"}
	if got := subjectsOf(found, unusedReceiverCode); !slices.Equal(got, wantReceiver) {
		t.Errorf("the kind reports %v under %s, want %v",
			got, unusedReceiverCode, wantReceiver)
	}
}

// overlapVocabulary is the overlap list the vocabulary publishes per code, for the
// language this analyzer reports.
func overlapVocabulary(t *testing.T) map[string][]string {
	t.Helper()

	const kindsPath = "contract/kinds.json"
	body, err := spec.Contract.ReadFile(kindsPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", kindsPath, err)
	}
	var document struct {
		Kinds []struct {
			Code    string              `json:"code"`
			Overlap map[string][]string `json:"overlap"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", kindsPath, err)
	}
	published := make(map[string][]string)
	for _, row := range document.Kinds {
		if named := row.Overlap[language]; len(named) > 0 {
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

// intraFuncModules are the real modules the intra-function kinds are measured over:
// three libraries analyzed with no consumer information, and one application.
var intraFuncModules = []struct {
	dir  string
	kind config.TargetKind
}{
	{"/workspace/webhttp", config.Library},
	{"/workspace/toolbelt", config.Library},
	{"/workspace/subflux", config.Application},
	{"/workspace/deadset-go", config.Application},
}

// TestMeasureTheIntraFunctionKindsOverRealModules prints what every kind of the
// group reports over real modules: the count per code and every finding, so a report
// carries a row per subject a hand classification can be read against.
func TestMeasureTheIntraFunctionKindsOverRealModules(t *testing.T) {
	if !measuring(t) {
		return
	}
	for _, one := range intraFuncModules {
		t.Run(one.dir, func(t *testing.T) {
			if _, err := os.Stat(one.dir); err != nil {
				t.Skipf("%s is not on this machine: %v", one.dir, err)
			}
			resolved := config.Default()
			resolved.Target.Kind = one.kind
			in := inputOfDir(t, one.dir, resolved, Consumers{})

			found := computed(t, in, intraFuncEmitters()).Findings

			t.Logf("%s (%s): %d findings of the intra-function group", one.dir, one.kind, len(found))
			for _, line := range tallied(found, func(f *Finding) string { return f.Code }) {
				t.Logf("   %s", line)
			}
			for i := range found {
				f := &found[i]
				t.Logf("   %s %s:%d:%d %s %s of %s", f.Code, f.Position.Path, f.Position.Line,
					f.Position.Column, f.Symbol.Kind, f.Symbol.Name, f.Symbol.Ref)
			}
		})
	}
}
