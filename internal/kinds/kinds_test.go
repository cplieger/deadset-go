package kinds

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/graph"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The declarations every hand-built input of this file holds: one exported
// function, the unexported helper it would call, and one exported function of an
// internal package.
const (
	exportedID   graph.SymbolID = "catalog.go:14:6"
	helperID     graph.SymbolID = "catalog.go:31:6"
	internalID   graph.SymbolID = "internal/store/store.go:9:6"
	generatedID  graph.SymbolID = "wire.gen.go:12:6"
	exportedRef                 = "go://example.com/app#Resolve"
	helperRef                   = "go://example.com/app#resolve"
	internalRef                 = "go://example.com/app/internal/store#Open"
	generatedRef                = "go://example.com/app#Wired"
)

// handSpans are the lines the hand-built component's two declarations occupy, which
// share the line one ends and the other starts on.
var handSpans = []graph.Span{{Path: "catalog.go", First: 14, Last: 31}, {Path: "catalog.go", First: 31, Last: 36}}

// handInput is an input written by hand: one inventory, one sweep answer and one
// two-configuration matrix, so a test of the framework measures the framework
// rather than a load.
func handInput(resolved config.Config) *Input {
	symbols := []graph.Symbol{
		handSymbol(exportedID, exportedRef, "Resolve", "example.com/app", "catalog.go", 14, 31, true),
		handSymbol(helperID, helperRef, "resolve", "example.com/app", "catalog.go", 31, 36, false),
		handSymbol(internalID, internalRef, "Open", "example.com/app/internal/store", "internal/store/store.go", 9, 12, true),
		handSymbol(generatedID, generatedRef, "Wired", "example.com/app", "wire.gen.go", 12, 14, true),
	}
	merged := &graph.Merged{Symbols: symbols, Configurations: 2}
	swept := &graph.Result{
		Candidates: []graph.Candidate{
			{ID: exportedID, Relation: graph.ReferenceCounting, Configs: graph.ConfigSet(0b11)},
			{ID: helperID, Relation: graph.Reachability, ProductionRefs: 1, Configs: graph.ConfigSet(0b01)},
			{ID: generatedID, Relation: graph.ReferenceCounting, Configs: graph.ConfigSet(0b11)},
		},
		Components: []graph.Component{{
			Members:        []graph.SymbolID{exportedID, helperID},
			Roots:          []graph.SymbolID{exportedID},
			Spans:          handSpans,
			Index:          0,
			DeletableLines: 23,
		}},
	}
	refs := make(map[graph.SymbolID]string, len(symbols))
	for i := range symbols {
		refs[symbols[i].ID] = symbols[i].Ref
	}
	return &Input{
		Config:    &resolved,
		Merged:    merged,
		Sweep:     swept,
		Refs:      refs,
		Generated: func(path string) bool { return path == "wire.gen.go" },
		Matrix:    []string{"linux-amd64", "linux-arm64"},
	}
}

// handSymbol is one declaration of a hand-built inventory.
func handSymbol(id graph.SymbolID, ref, name, pkgPath, path string, line, endLine int, exported bool) graph.Symbol {
	return graph.Symbol{
		ID:       id,
		Ref:      ref,
		Name:     name,
		PkgPath:  pkgPath,
		Pos:      token.Position{Filename: path, Line: line, Column: 6},
		EndLine:  endLine,
		Kind:     graph.KindFunc,
		Exported: exported,
		Configs:  graph.ConfigSet(0b11),
	}
}

// emitterOf returns an emitter that reports exactly the findings it is given, which
// is how a test puts one shape in front of the framework.
func emitterOf(findings ...Finding) Emitter {
	return func(*Input) ([]Finding, error) { return findings, nil }
}

// oneFinding is the finding an emitter of code reports about one declaration.
func oneFinding(in *Input, code vocab.Code, id graph.SymbolID) Finding {
	found, held := in.finding(id, code, "the declaration has no reference in the target")
	if !held {
		panic("the hand-built inventory holds no " + string(id))
	}
	return found
}

func TestComputeFillsEveryFieldTheContractRequiresOfAFinding(t *testing.T) {
	in := handInput(applicationConfig())
	reported := oneFinding(in, vocab.UnusedExported, exportedID)
	reported.Relation = graph.ReferenceCounting

	result := computed(t, in, map[vocab.Code]Emitter{vocab.UnusedExported: emitterOf(reported)})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() returned %d findings, want 1: %v", len(result.Findings), summary(result.Findings))
	}
	found := result.Findings[0]
	want := Finding{
		Code:            vocab.UnusedExported,
		Kind:            "unused-exported",
		Language:        "go",
		Position:        Position{Path: "catalog.go", Line: 14, Column: 6, EndLine: 31},
		Symbol:          Subject{Ref: exportedRef, Kind: "function", Name: "Resolve", SizeLines: 18},
		Class:           vocab.Certain,
		Confidence:      vocab.Certain,
		Relation:        graph.ReferenceCounting,
		Component:       findingComponent{ID: "deadset-go/c-1", SymbolCount: 2, DeletableLines: 23, Root: true},
		RetainedBy:      []string{},
		Configurations:  []string{"linux-amd64", "linux-arm64"},
		ConsumersLoaded: []string{},
		Fixability:      "deletable",
		Severity:        vocab.Deny,
		Message:         reported.Message,
	}
	if diff := differences(found, want); diff != "" {
		t.Errorf("Compute() filled the finding as\n%s", diff)
	}
}

// sameComponent reports whether two components agree member by member.
func sameComponent(a, b *findingComponent) bool {
	return a.ID == b.ID && a.Root == b.Root && a.SymbolCount == b.SymbolCount &&
		a.DeletableLines == b.DeletableLines
}

// differences names every field of a finding that is not what the Contract requires
// of it, so one failure lists every field at once.
func differences(got, want Finding) string {
	var lines []string
	add := func(field string, gotValue, wantValue any) {
		lines = append(lines, "  "+field+" = "+render(gotValue)+", want "+render(wantValue))
	}
	if got.Kind != want.Kind {
		add("Kind", got.Kind, want.Kind)
	}
	if got.Language != want.Language {
		add("Language", got.Language, want.Language)
	}
	if got.Position != want.Position {
		add("Position", got.Position, want.Position)
	}
	if got.Symbol != want.Symbol {
		add("Symbol", got.Symbol, want.Symbol)
	}
	if got.Class != want.Class {
		add("Class", got.Class, want.Class)
	}
	if got.Confidence != want.Confidence {
		add("Confidence", got.Confidence, want.Confidence)
	}
	if got.Relation != want.Relation {
		add("Relation", got.Relation, want.Relation)
	}
	if got.Live != want.Live {
		add("Live", got.Live, want.Live)
	}
	if got.TestOnly != want.TestOnly {
		add("TestOnly", got.TestOnly, want.TestOnly)
	}
	if got.Generated != want.Generated {
		add("Generated", got.Generated, want.Generated)
	}
	if !sameComponent(&got.Component, &want.Component) {
		add("Component", got.Component, want.Component)
	}
	if !slices.Equal(got.RetainedBy, want.RetainedBy) {
		add("RetainedBy", got.RetainedBy, want.RetainedBy)
	}
	if !slices.Equal(got.Configurations, want.Configurations) {
		add("Configurations", got.Configurations, want.Configurations)
	}
	if !slices.Equal(got.ConsumersLoaded, want.ConsumersLoaded) {
		add("ConsumersLoaded", got.ConsumersLoaded, want.ConsumersLoaded)
	}
	if got.Fixability != want.Fixability {
		add("Fixability", got.Fixability, want.Fixability)
	}
	if got.Severity != want.Severity {
		add("Severity", got.Severity, want.Severity)
	}
	if got.Message != want.Message {
		add("Message", got.Message, want.Message)
	}
	return strings.Join(lines, "\n")
}

// render is one value in a failure message.
func render(value any) string {
	if text, ok := value.(string); ok {
		return "\"" + text + "\""
	}
	return fmt.Sprintf("%+v", value)
}

func TestComputeReportsNoFindingOfAnAllowedKindAndNoneOfACodeWithNoEmitter(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]vocab.Severity{string(vocab.UnusedExported): vocab.Allow}
	in := handInput(resolved)

	result := computed(t, in, map[vocab.Code]Emitter{
		vocab.UnusedExported: emitterOf(oneFinding(in, vocab.UnusedExported, exportedID)),
	})

	if len(result.Findings) != 0 {
		t.Errorf("Compute() = %v, want no finding: a kind at allow is withheld and every other code has no emitter",
			summary(result.Findings))
	}
}

func TestComputeRefusesAMessageAReporterCannotCarry(t *testing.T) {
	for name, message := range map[string]string{
		"ending in a full stop": "the declaration has no reference.",
		"holding a line feed":   "the declaration has no reference\nanywhere",
		"holding no text":       "",
	} {
		t.Run(name, func(t *testing.T) {
			in := handInput(applicationConfig())
			reported := oneFinding(in, vocab.UnusedExported, exportedID)
			reported.Message = message

			_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedExported: emitterOf(reported)})
			if !errors.Is(err, errEmitter) {
				t.Errorf("Compute() with a message %s = %v, want an errEmitter refusal", name, err)
			}
		})
	}
}

func TestComputeRefusesACodeThatIsNotTheEmittersOwn(t *testing.T) {
	in := handInput(applicationConfig())
	reported := oneFinding(in, vocab.UnusedUnexported, exportedID)

	_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedExported: emitterOf(reported)})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute() = %v, want an errEmitter refusal: the %s emitter returned a %s finding",
			err, vocab.UnusedExported, vocab.UnusedUnexported)
	}
}

func TestComputeRefusesAnEmitterRegisteredUnderACodeTheVocabularyDoesNotHold(t *testing.T) {
	const retired = "DS1299"

	in := handInput(applicationConfig())

	_, err := Compute(in, map[vocab.Code]Emitter{retired: emitterOf()})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute() = %v, want an errEmitter refusal: %s names no live kind", err, retired)
	}
}

func TestComputeRefusesTwoFindingsAboutOneDeclaration(t *testing.T) {
	in := handInput(applicationConfig())

	_, err := Compute(in, map[vocab.Code]Emitter{
		vocab.UnusedExported:   emitterOf(oneFinding(in, vocab.UnusedExported, exportedID)),
		vocab.TestOnlyUse:      emitterOf(oneFinding(in, vocab.TestOnlyUse, exportedID)),
		vocab.UnusedUnexported: emitterOf(oneFinding(in, vocab.UnusedUnexported, helperID)),
	})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute() = %v, want an errEmitter refusal: two kinds report %s", err, exportedRef)
	}
}

func TestComputeRefusesAFindingAboutASymbolAnExemptionRetained(t *testing.T) {
	in := handInput(applicationConfig())
	in.Sweep.Retained = []graph.Exemption{{
		ID:     exportedID,
		Class:  "interface-satisfaction",
		Detail: "implements an interface a value of its type is converted to",
	}}

	_, err := Compute(in, map[vocab.Code]Emitter{
		vocab.UnusedExported: emitterOf(oneFinding(in, vocab.UnusedExported, exportedID)),
	})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute() = %v, want an errEmitter refusal: an exemption retained %s", err, exportedRef)
	}
}

func TestComputeRefusesAFindingAboutNoDeclarationOfTheInventory(t *testing.T) {
	in := handInput(applicationConfig())
	reported := Finding{
		Code:    vocab.UnusedExported,
		Symbol:  Subject{Ref: "go://example.com/app#Absent", Name: "Absent", SizeLines: 1},
		Message: "the declaration has no reference in the target",
	}

	_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedExported: emitterOf(reported)})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute() = %v, want an errEmitter refusal: the inventory holds no such declaration", err)
	}
}

func TestComputeRefusesAFindingAboutADeclarationAnEmitterNamesByItsReferenceAlone(t *testing.T) {
	in := handInput(applicationConfig())
	reported := Finding{
		Code:     vocab.UnusedExported,
		Position: Position{Path: "catalog.go", Line: 14, Column: 6, EndLine: 31},
		Symbol:   Subject{Ref: exportedRef, Name: "Resolve", SizeLines: 18},
		Message:  "the declaration has no reference in the target",
	}

	_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedExported: emitterOf(reported)})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute(a finding naming %s by reference alone) = %v, want an errEmitter refusal: a report's own reference does not identify a declaration, because a blank declaration shares the reference of its container",
			exportedRef, err)
	}
}

func TestTheKeyOfEveryFindingAboutADeclarationIsThatDeclarationsIdentifier(t *testing.T) {
	t.Parallel()

	measured := 0
	for _, archive := range fixtures(t) {
		result := analysisOf(t, archive, asApplication, Consumers{}).findings(t, everyKind)

		for i := range result.Findings {
			found := &result.Findings[i]
			if found.shape() != shapeDeclaration {
				continue
			}
			measured++
			if got, want := key(found), string(found.id); got != want {
				t.Errorf("the key of %s about %s is %q, want %q, the identifier the analysis enumerated the declaration under",
					found.Code, found.Symbol.Ref, got, want)
			}
		}
	}
	if measured == 0 {
		t.Fatal("no fixture reports a finding about a declaration, so the identity of one is measured over nothing")
	}
}

func TestComputeCapsTheConfidenceAtTheKindsCeilingAndTheMinimumConfidenceOmitsIt(t *testing.T) {
	in := handInput(applicationConfig())
	in.Config.Analysis.MinConfidence = vocab.Certain
	swap(t, []vocab.Kind{{
		Code: vocab.UnusedExported, Name: "unused-exported", Languages: []vocab.Language{vocab.Go},
		DefaultSeverity: vocab.Deny, MaxClass: vocab.Probable, Fixability: vocab.Deletable, DefaultEnabled: true,
	}})

	result := computed(t, in, map[vocab.Code]Emitter{
		vocab.UnusedExported: emitterOf(oneFinding(in, vocab.UnusedExported, exportedID)),
	})

	if len(result.Findings) != 0 {
		t.Errorf("Compute() = %v, want no finding: a probable confidence is below a certain minimum",
			summary(result.Findings))
	}
}

func TestComputeReportsTheCeilingAsTheConfidenceOfACertainClass(t *testing.T) {
	in := handInput(applicationConfig())
	swap(t, []vocab.Kind{{
		Code: vocab.UnusedExported, Name: "unused-exported", Languages: []vocab.Language{vocab.Go},
		DefaultSeverity: vocab.Deny, MaxClass: vocab.Probable, Fixability: vocab.Deletable, DefaultEnabled: true,
	}})

	result := computed(t, in, map[vocab.Code]Emitter{
		vocab.UnusedExported: emitterOf(oneFinding(in, vocab.UnusedExported, exportedID)),
	})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() = %v, want one finding", summary(result.Findings))
	}
	found := result.Findings[0]
	if found.Class != vocab.Certain || found.Confidence != vocab.Probable {
		t.Errorf("Compute() reported class %q and confidence %q, want %q and %q: the kind's ceiling caps the class",
			found.Class, found.Confidence, vocab.Certain, vocab.Probable)
	}
}

func TestComputeReturnsTheFindingsInTheCanonicalOrder(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, map[vocab.Code]Emitter{
		vocab.UnusedExported: emitterOf(
			oneFinding(in, vocab.UnusedExported, internalID),
			oneFinding(in, vocab.UnusedExported, generatedID),
			oneFinding(in, vocab.UnusedExported, exportedID),
		),
	})

	want := []string{"catalog.go", "internal/store/store.go", "wire.gen.go"}
	got := make([]string, len(result.Findings))
	for i := range result.Findings {
		got[i] = result.Findings[i].Position.Path
	}
	if !slices.Equal(got, want) {
		t.Errorf("Compute() ordered the findings by path %v, want %v", got, want)
	}
}

func TestComputeMintsAComponentForASubjectNoDeadComponentHolds(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, map[vocab.Code]Emitter{
		vocab.UnusedExported: emitterOf(oneFinding(in, vocab.UnusedExported, internalID)),
	})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() = %v, want one finding", summary(result.Findings))
	}
	want := findingComponent{ID: "deadset-go/c-2", Root: true, SymbolCount: 1}
	if got := result.Findings[0].Component; !sameComponent(&got, &want) {
		t.Errorf("Compute() gave a live subject the component %+v, want %+v", got, want)
	}
}

func TestComputeMarksAFindingInAGeneratedFileAsOneNoMechanicalEditActsOn(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, map[vocab.Code]Emitter{
		vocab.UnusedExported: emitterOf(oneFinding(in, vocab.UnusedExported, generatedID)),
	})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() = %v, want one finding", summary(result.Findings))
	}
	found := result.Findings[0]
	if !found.Generated || found.Fixability != vocab.None {
		t.Errorf("Compute() reported generated=%t and fixability %q for a generated file, want true and %q",
			found.Generated, found.Fixability, vocab.None)
	}
}

func TestComputeRefusesAPassWithNoResolvedConfiguration(t *testing.T) {
	if _, err := Compute(&Input{}, nil); !errors.Is(err, errInput) {
		t.Errorf("Compute() with no configuration = %v, want an errInput refusal", err)
	}
}

// swap puts one vocabulary in front of the framework for the length of one test,
// which is how a ceiling below certain is measured: no shipped kind declares one.
func swap(t *testing.T, rows []vocab.Kind) {
	t.Helper()

	before := vocabularyKinds
	vocabularyKinds = func() []vocab.Kind { return rows }
	t.Cleanup(func() { vocabularyKinds = before })
}

func TestComputeReportsEveryPositionOnceOverEveryKindOfThePackage(t *testing.T) {
	t.Parallel()

	for _, archive := range fixtures(t) {
		t.Run(archive, func(t *testing.T) {
			t.Parallel()

			result, err := analysisOf(t, archive, asApplication, Consumers{}).compute(t, everyKind)
			if err != nil {
				t.Fatalf("Compute(every kind of the package, %s) = error %v, want the findings of the pass: two kinds disagree on which code reports one subject",
					archive, err)
			}
			under := make(map[string]vocab.Code, len(result.Findings))
			for i := range result.Findings {
				found := &result.Findings[i]
				at := key(found)
				if first, twice := under[at]; twice {
					t.Errorf("the pass over %s reports %s under both %s and %s, want one code",
						archive, at, first, found.Code)
					continue
				}
				under[at] = found.Code
			}
		})
	}
}

func TestTheKindsOfThePackageAgreeOnWhichCodeReportsADeclarationNothingReferences(t *testing.T) {
	t.Parallel()

	want := map[string]vocab.Code{
		"go://example.com/app/api#Reader":      vocab.UnusedInterface,
		"go://example.com/app/api#Store.Flush": vocab.UncalledInterfaceMethod,
		"go://example.com/app/api#Quiet":       vocab.UnusedEnumMember,
		"go://example.com/app/api#Convert[T]":  vocab.UnusedTypeParameter,
	}

	result, err := analysisOf(t, "precedence-importable.txtar", asApplication, Consumers{}).compute(t, everyKind)
	if err != nil {
		t.Fatalf("Compute(every kind of the package, precedence-importable.txtar) = error %v, want the findings of the pass: an unused-declaration kind takes a subject a more specific kind reports",
			err)
	}
	under := make(map[string]vocab.Code, len(result.Findings))
	for i := range result.Findings {
		under[result.Findings[i].Symbol.Ref] = result.Findings[i].Code
	}
	for ref, code := range want {
		if got := under[ref]; got != code {
			t.Errorf("the pass over precedence-importable.txtar reports %s under %q, want %q: the pass reports %v",
				ref, got, code, summary(result.Findings))
		}
	}
}

// livenessAbsentKinds is the subject kinds the finding schema forbids a liveness
// relation on, read from the branch that states the rule by the members it is
// written with rather than by the position of that branch.
func livenessAbsentKinds(t *testing.T) []string {
	t.Helper()

	const schemaPath = "contract/finding.schema.json"
	body, err := spec.Contract.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", schemaPath, err)
	}
	type condition struct {
		Properties struct {
			Code struct {
				Enum []string `json:"enum"`
			} `json:"code"`
			Symbol struct {
				Properties struct {
					Kind struct {
						Enum []string `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"symbol"`
		} `json:"properties"`
	}
	var schema struct {
		AllOf []struct {
			If struct {
				condition
				AnyOf []condition `json:"anyOf"`
			} `json:"if"`
			Then struct {
				Not struct {
					Required []string `json:"required"`
				} `json:"not"`
			} `json:"then"`
		} `json:"allOf"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatalf("Setup: decode %s: %v", schemaPath, err)
	}
	for _, branch := range schema.AllOf {
		if !slices.Contains(branch.Then.Not.Required, "liveness_relation") {
			continue
		}
		var subjectKinds, codes []string
		for _, arm := range append(branch.If.AnyOf, branch.If.condition) {
			subjectKinds = append(subjectKinds, arm.Properties.Symbol.Properties.Kind.Enum...)
			codes = append(codes, arm.Properties.Code.Enum...)
		}
		if len(subjectKinds) == 0 || len(codes) == 0 {
			t.Fatalf("Setup: %s forbids a liveness relation under subject kinds %v and codes %v, want both arms, so this test pins nothing",
				schemaPath, subjectKinds, codes)
		}
		return subjectKinds
	}
	t.Fatalf("Setup: %s states no branch forbidding a liveness relation", schemaPath)
	return nil
}

func TestLivenessAbsentAnswersForEveryFindingTheContractForbidsTheRelationOn(t *testing.T) {
	t.Parallel()

	absent := livenessAbsentKinds(t)
	measured := 0
	for _, archive := range fixtures(t) {
		for _, found := range analysisOf(t, archive, asApplication, Consumers{}).findings(t, everyKind).Findings {
			if !slices.Contains(absent, found.Symbol.Kind) {
				continue
			}
			measured++
			if !livenessAbsent(&found) {
				t.Errorf("LivenessAbsent(%s about the %s %s) = false, want true: the Contract forbids the relation on that subject kind",
					found.Code, found.Symbol.Kind, found.Symbol.Ref)
			}
			if !found.Live {
				t.Errorf("%s about the %s %s carries the relation %s, want none",
					found.Code, found.Symbol.Kind, found.Symbol.Ref, found.Relation)
			}
		}
	}
	if measured == 0 {
		t.Fatal("no fixture reports a finding about a part or a row, so the rule is measured over nothing")
	}
}

func TestEveryKindOfDeclarationMapsOntoTheContractsSubjectVocabulary(t *testing.T) {
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

	for kind, word := range symbolKinds {
		if !slices.Contains(vocabulary, word) {
			t.Errorf("a %s declaration is reported as subject kind %q, which %s does not declare",
				kind, word, schemaPath)
		}
	}
	for kind := graph.KindFunc; kind <= graph.KindFile; kind++ {
		if kind == graph.KindPackage {
			continue
		}
		if _, held := symbolKinds[kind]; !held {
			t.Errorf("a %s declaration maps onto no subject kind of %s, so a finding about one carries none",
				kind, schemaPath)
		}
	}
}

// The subject of a finding whose kind the inventory cannot hold a declaration for,
// which is a direct requirement of the target's module file.
const (
	requirementRef  = "go://example.com/app#golang.org/x/text:require"
	requirementName = "golang.org/x/text"
)

// requirementFinding is the finding the unused-dependency emitter reports about one
// requirement: a subject the inventory holds no declaration for, named by its
// reference, with the build configurations the kind claims it under.
func requirementFinding() Finding {
	return Finding{
		Code:     vocab.UnusedDependency,
		Position: Position{Path: "go.mod", Line: 7, Column: 2, EndLine: 7},
		Symbol: Subject{
			Ref:       requirementRef,
			Kind:      dependencySubject,
			Name:      requirementName,
			SizeLines: 1,
		},
		Configurations: []string{"linux-amd64", "linux-arm64"},
		Message:        "no package of the target imports a package this required module provides",
		Details:        Details{DependencyClass: requireSection},
	}
}

func TestComputeCompletesAFindingWhoseSubjectIsNoDeclarationOfTheInventory(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, map[vocab.Code]Emitter{vocab.UnusedDependency: emitterOf(requirementFinding())})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute(%s over a requirement) returned %d findings, want 1: %v",
			vocab.UnusedDependency, len(result.Findings), summary(result.Findings))
	}
	reported := requirementFinding()
	want := Finding{
		Code:            vocab.UnusedDependency,
		Kind:            "unused-dependency",
		Language:        "go",
		Position:        reported.Position,
		Symbol:          reported.Symbol,
		Class:           vocab.Certain,
		Confidence:      vocab.Certain,
		Relation:        graph.ReferenceCounting,
		Live:            true,
		Component:       findingComponent{ID: "deadset-go/c-2", Root: true, SymbolCount: 1},
		RetainedBy:      []string{},
		Configurations:  []string{"linux-amd64", "linux-arm64"},
		ConsumersLoaded: []string{},
		Fixability:      "manual",
		Severity:        vocab.Deny,
		Message:         reported.Message,
	}
	if diff := differences(result.Findings[0], want); diff != "" {
		t.Errorf("Compute(%s over a requirement) filled the finding as\n%s", vocab.UnusedDependency, diff)
	}
}

func TestComputeRefusesTwoFindingsAboutOneSubjectThatIsNoDeclaration(t *testing.T) {
	in := handInput(applicationConfig())
	twice := requirementFinding()

	_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedDependency: emitterOf(twice, twice)})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute(%s reporting one requirement twice) = %v, want an errEmitter refusal: %s is reported twice",
			vocab.UnusedDependency, err, requirementRef)
	}
}

func TestComputeRefusesAFindingAboutADeclarationKindTheInventoryDoesNotHold(t *testing.T) {
	for kind, subject := range map[string]string{
		"a function": symbolKinds[graph.KindFunc],
		"a field":    symbolKinds[graph.KindField],
		"no kind":    "",
	} {
		t.Run(kind, func(t *testing.T) {
			in := handInput(applicationConfig())
			reported := Finding{
				Code:    vocab.UnusedExported,
				Symbol:  Subject{Ref: "go://example.com/app#Absent", Kind: subject, Name: "Absent", SizeLines: 1},
				Message: "the declaration has no reference in the target",
			}

			_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedExported: emitterOf(reported)})
			if !errors.Is(err, errEmitter) {
				t.Errorf("Compute(%s subject the inventory does not hold) = %v, want an errEmitter refusal",
					kind, err)
			}
		})
	}
}

func TestComputeRefusesARowThatNamesNoRecord(t *testing.T) {
	in := handInput(applicationConfig())
	unnamed := requirementFinding()
	unnamed.Symbol.Ref = ""

	_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedDependency: emitterOf(unnamed)})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute(%s naming no reference) = %v, want an errEmitter refusal: a %s row is found by the record it names, because its position names the document",
			vocab.UnusedDependency, err, dependencySubject)
	}
}

func TestComputeRefusesAPartOfADeclarationTheInventoryDoesNotHold(t *testing.T) {
	in := handInput(applicationConfig())
	orphan := findingAt(vocab.UnusedParameter, Subject{
		Ref:       "go://example.com/app#Absent",
		Kind:      parameterSubject,
		Name:      "limit",
		SizeLines: 1,
	}, Position{Path: "catalog.go", Line: 14, Column: 20, EndLine: 14},
		"parameter limit is never read in the body")

	_, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedParameter: emitterOf(orphan)})
	if !errors.Is(err, errEmitter) {
		t.Errorf("Compute(a %s of a declaration the inventory does not hold) = %v, want an errEmitter refusal: a part belongs to a declaration and names it by reference",
			parameterSubject, err)
	}
}

func TestASuppressionWithholdsAFindingReportedInAnotherFileThanTheDeclarationItNames(t *testing.T) {
	in := suppressed(t, "interfaces-assertion-elsewhere.txtar", applicationConfig())
	emitters := map[vocab.Code]Emitter{
		vocab.UnusedSatisfactionAssertion: unusedSatisfactionAssertion,
		vocab.StaleSuppression:            staleSuppressions,
	}

	// The premise the entry is written against: the reference it names is the
	// interface's, declared in iface.go, and the path it names is the file the
	// assertion is written in, so the pair resolves to no one declaration and the
	// record marks nothing live.
	if len(in.Marks) != 1 {
		t.Fatalf("Setup: the fixture's ignore document read as %d records, want 1: %+v", len(in.Marks), in.Marks)
	}
	if in.Marks[0].Bound != "" {
		t.Fatalf("Setup: the entry bound %s, want no declaration: the interface is declared in another file than the assertion",
			in.Marks[0].Bound)
	}

	// The pass with the entry withdrawn, which is the run a maintainer makes after
	// deleting it: it is what says the withheld finding exists, and where.
	bare := suppressed(t, "interfaces-assertion-elsewhere.txtar", applicationConfig())
	bare.Marks = nil
	assertion := findingOf(t, computed(t, bare, emitters).Findings, vocab.UnusedSatisfactionAssertion, "encoder")
	if assertion.Symbol.Ref != "go://example.com/app#encoder" {
		t.Errorf("the assertion finding names %q, want the interface's reference", assertion.Symbol.Ref)
	}
	if assertion.Position.Path != "assert.go" {
		t.Errorf("the assertion finding is reported at %s, want assert.go: the position is the assertion's own",
			assertion.Position.Path)
	}

	// The same pass with the entry: the finding is withheld and the record that
	// withheld it is in effect, so nothing is reported at all.
	if got := summary(computed(t, in, emitters).Findings); len(got) != 0 {
		t.Errorf("the pass with the fixture's entry reports %v, want nothing: the entry withholds the assertion", got)
	}
	if inEffect, reasons := Totals(in); inEffect != 1 || reasons != 1 {
		t.Errorf("Totals() = %d in effect and %d reasons, want 1 and 1", inEffect, reasons)
	}
}

func TestASuppressionWithholdsNoFindingReportedInAFileTheRecordDoesNotName(t *testing.T) {
	in := suppressed(t, "interfaces-assertion-elsewhere.txtar", applicationConfig())
	if len(in.Marks) != 1 {
		t.Fatalf("Setup: the fixture's ignore document read as %d records, want 1: %+v", len(in.Marks), in.Marks)
	}
	// The entry as a maintainer would misfile it: the right reference and the file
	// the interface is declared in rather than the one the finding is reported in.
	in.Marks[0].Path = "iface.go"

	found := computed(t, in, map[vocab.Code]Emitter{
		vocab.UnusedSatisfactionAssertion: unusedSatisfactionAssertion,
		vocab.StaleSuppression:            staleSuppressions,
	}).Findings

	want := []string{
		"DS1203 encoder",
		"DS9003 go://example.com/app#encoder",
	}
	if got := summary(found); !slices.Equal(got, want) {
		t.Errorf("the pass with the entry naming another file reports %v, want %v: an entry matches the path the finding carries",
			got, want)
	}
}

// goKindsWithoutEmitter are the live Go kinds Emitters holds no entry
// for, so no run of that table reports one. Each is named rather than counted, so
// the table's test says which kinds the table is silent about instead of allowing a
// kind to go missing unnoticed.
//
// DS9005 is the merge's, which is another product.
var goKindsWithoutEmitter = []vocab.Code{
	"DS9005", // the stale cross-language edge, which the merge answers
}

func TestEmittersNameEveryLiveGoKindOrSayWhichHasNoRule(t *testing.T) {
	t.Parallel()

	var published []vocab.Code
	for _, row := range vocab.Kinds() {
		if !row.AppliesTo(vocab.Go) {
			continue
		}
		published = append(published, row.Code)
		_, wired := Emitters[row.Code]
		named := slices.Contains(goKindsWithoutEmitter, row.Code)
		switch {
		case wired && named:
			t.Errorf("%s has an emitter and is named as having none", row.Code)
		case !wired && !named:
			t.Errorf("%s is a live Go kind with no emitter and is not named as one: a kind absent from both reports nothing and nothing says so", row.Code)
		}
	}

	if len(published) != len(Emitters)+len(goKindsWithoutEmitter) {
		t.Errorf("the vocabulary publishes %d Go kinds, the table holds %d and %d are named without a rule",
			len(published), len(Emitters), len(goKindsWithoutEmitter))
	}
	for code := range Emitters {
		row, live := vocab.KindOf(code)
		if !live {
			t.Errorf("the table registers an emitter under %s, which names no live kind", code)
			continue
		}
		if !row.AppliesTo(vocab.Go) {
			t.Errorf("the table registers an emitter under %s, which the vocabulary gives languages %v and not %s",
				code, row.Languages, vocab.Go)
		}
	}
	for _, code := range goKindsWithoutEmitter {
		if _, live := vocab.KindOf(code); !live {
			t.Errorf("%s is named as a live Go kind without a rule, and the vocabulary does not hold it", code)
		}
	}
}
