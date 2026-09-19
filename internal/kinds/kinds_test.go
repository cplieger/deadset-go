package kinds

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	spec "github.com/cplieger/deadset-spec"
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
			Falls:          []graph.SymbolID{exportedID, helperID},
			Index:          0,
			DeletableLines: 24,
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
func oneFinding(in *Input, code string, id graph.SymbolID) Finding {
	found, held := in.finding(id, code, "the declaration has no reference in the target")
	if !held {
		panic("the hand-built inventory holds no " + string(id))
	}
	return found
}

func TestComputeFillsEveryFieldTheContractRequiresOfAFinding(t *testing.T) {
	in := handInput(applicationConfig())
	reported := oneFinding(in, unusedExportedCode, exportedID)
	reported.Relation = graph.ReferenceCounting

	result := computed(t, in, map[string]Emitter{unusedExportedCode: emitterOf(reported)})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() returned %d findings, want 1: %v", len(result.Findings), summary(result.Findings))
	}
	found := result.Findings[0]
	want := Finding{
		Code:            unusedExportedCode,
		Kind:            "unused-exported",
		Language:        "go",
		Position:        Position{Path: "catalog.go", Line: 14, Column: 6, EndLine: 31},
		Symbol:          Subject{Ref: exportedRef, Kind: "function", Name: "Resolve", SizeLines: 18},
		Class:           Certain,
		Confidence:      Certain,
		Relation:        graph.ReferenceCounting,
		Component:       Component{ID: "deadset-go/c-1", Root: true, SymbolCount: 2, DeletableLines: 24},
		RetainedBy:      []string{},
		Configurations:  []string{"linux-amd64", "linux-arm64"},
		ConsumersLoaded: []string{},
		Fixability:      "deletable",
		Severity:        config.Deny,
		Message:         reported.Message,
	}
	if diff := differences(found, want); diff != "" {
		t.Errorf("Compute() filled the finding as\n%s", diff)
	}
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
	if got.TestOnly != want.TestOnly {
		add("TestOnly", got.TestOnly, want.TestOnly)
	}
	if got.Generated != want.Generated {
		add("Generated", got.Generated, want.Generated)
	}
	if got.Component != want.Component {
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

func TestComputeSkipsTheEmitterOfAnAllowedKindAndACodeWithNoEmitter(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]config.Severity{unusedExportedCode: config.Allow}
	in := handInput(resolved)

	ran := false
	result := computed(t, in, map[string]Emitter{
		unusedExportedCode: func(*Input) ([]Finding, error) {
			ran = true
			return nil, nil
		},
	})

	if ran {
		t.Error("Compute() ran the emitter of a kind the severity map allows, want it skipped")
	}
	if len(result.Findings) != 0 {
		t.Errorf("Compute() = %v, want no finding: every other code has no emitter", summary(result.Findings))
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
			reported := oneFinding(in, unusedExportedCode, exportedID)
			reported.Message = message

			_, err := Compute(in, map[string]Emitter{unusedExportedCode: emitterOf(reported)})
			if !errors.Is(err, ErrEmitter) {
				t.Errorf("Compute() with a message %s = %v, want an ErrEmitter refusal", name, err)
			}
		})
	}
}

func TestComputeRefusesACodeThatIsNotTheEmittersOwn(t *testing.T) {
	in := handInput(applicationConfig())
	reported := oneFinding(in, unusedUnexportedCode, exportedID)

	_, err := Compute(in, map[string]Emitter{unusedExportedCode: emitterOf(reported)})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute() = %v, want an ErrEmitter refusal: the %s emitter returned a %s finding",
			err, unusedExportedCode, unusedUnexportedCode)
	}
}

func TestComputeRefusesAnEmitterRegisteredUnderACodeTheVocabularyDoesNotHold(t *testing.T) {
	const retired = "DS1202"

	in := handInput(applicationConfig())

	_, err := Compute(in, map[string]Emitter{retired: emitterOf()})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute() = %v, want an ErrEmitter refusal: %s names no live kind", err, retired)
	}
}

func TestComputeRefusesTwoFindingsAboutOneDeclaration(t *testing.T) {
	in := handInput(applicationConfig())

	_, err := Compute(in, map[string]Emitter{
		unusedExportedCode:   emitterOf(oneFinding(in, unusedExportedCode, exportedID)),
		testOnlyUseCode:      emitterOf(oneFinding(in, testOnlyUseCode, exportedID)),
		unusedUnexportedCode: emitterOf(oneFinding(in, unusedUnexportedCode, helperID)),
	})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute() = %v, want an ErrEmitter refusal: two kinds report %s", err, exportedRef)
	}
}

func TestComputeRefusesAFindingAboutASymbolAnExemptionRetained(t *testing.T) {
	in := handInput(applicationConfig())
	in.Sweep.Retained = []graph.Exemption{{
		ID:     exportedID,
		Class:  "interface-satisfaction",
		Detail: "implements an interface a value of its type is converted to",
	}}

	_, err := Compute(in, map[string]Emitter{
		unusedExportedCode: emitterOf(oneFinding(in, unusedExportedCode, exportedID)),
	})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute() = %v, want an ErrEmitter refusal: an exemption retained %s", err, exportedRef)
	}
}

func TestComputeRefusesAFindingAboutNoDeclarationOfTheInventory(t *testing.T) {
	in := handInput(applicationConfig())
	reported := Finding{
		Code:    unusedExportedCode,
		Symbol:  Subject{Ref: "go://example.com/app#Absent", Name: "Absent", SizeLines: 1},
		Message: "the declaration has no reference in the target",
	}

	_, err := Compute(in, map[string]Emitter{unusedExportedCode: emitterOf(reported)})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute() = %v, want an ErrEmitter refusal: the inventory holds no such declaration", err)
	}
}

func TestComputeResolvesAFindingByItsReferenceWhereAnEmitterNamesNoDeclaration(t *testing.T) {
	in := handInput(applicationConfig())
	reported := Finding{
		Code:     unusedExportedCode,
		Position: Position{Path: "catalog.go", Line: 14, Column: 6, EndLine: 31},
		Symbol:   Subject{Ref: exportedRef, Name: "Resolve", SizeLines: 18},
		Message:  "the declaration has no reference in the target",
	}

	result := computed(t, in, map[string]Emitter{unusedExportedCode: emitterOf(reported)})

	if len(result.Findings) != 1 || result.Findings[0].Component.ID != "deadset-go/c-1" {
		t.Errorf("Compute() = %v, want one finding completed from the inventory the reference names",
			summary(result.Findings))
	}
}

func TestComputeCapsTheConfidenceAtTheKindsCeilingAndTheMinimumConfidenceOmitsIt(t *testing.T) {
	in := handInput(applicationConfig())
	in.Config.Analysis.MinConfidence = config.Certain
	swap(t, []catalog.Row{{
		Code: unusedExportedCode, Name: "unused-exported", Languages: []string{"go"},
		DefaultSeverity: "deny", MaxClass: string(Probable), Fixability: "deletable", DefaultEnabled: true,
	}})

	result := computed(t, in, map[string]Emitter{
		unusedExportedCode: emitterOf(oneFinding(in, unusedExportedCode, exportedID)),
	})

	if len(result.Findings) != 0 {
		t.Errorf("Compute() = %v, want no finding: a probable confidence is below a certain minimum",
			summary(result.Findings))
	}
	if result.OmittedBelowMinConfidence != 1 {
		t.Errorf("Compute().OmittedBelowMinConfidence = %d, want 1", result.OmittedBelowMinConfidence)
	}
}

func TestComputeReportsTheCeilingAsTheConfidenceOfACertainClass(t *testing.T) {
	in := handInput(applicationConfig())
	swap(t, []catalog.Row{{
		Code: unusedExportedCode, Name: "unused-exported", Languages: []string{"go"},
		DefaultSeverity: "deny", MaxClass: string(Probable), Fixability: "deletable", DefaultEnabled: true,
	}})

	result := computed(t, in, map[string]Emitter{
		unusedExportedCode: emitterOf(oneFinding(in, unusedExportedCode, exportedID)),
	})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() = %v, want one finding", summary(result.Findings))
	}
	found := result.Findings[0]
	if found.Class != Certain || found.Confidence != Probable {
		t.Errorf("Compute() reported class %q and confidence %q, want %q and %q: the kind's ceiling caps the class",
			found.Class, found.Confidence, Certain, Probable)
	}
}

func TestComputeReturnsTheFindingsInTheCanonicalOrder(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, map[string]Emitter{
		unusedExportedCode: emitterOf(
			oneFinding(in, unusedExportedCode, internalID),
			oneFinding(in, unusedExportedCode, generatedID),
			oneFinding(in, unusedExportedCode, exportedID),
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

	result := computed(t, in, map[string]Emitter{
		unusedExportedCode: emitterOf(oneFinding(in, unusedExportedCode, internalID)),
	})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() = %v, want one finding", summary(result.Findings))
	}
	want := Component{ID: "deadset-go/c-2", Root: true, SymbolCount: 1}
	if got := result.Findings[0].Component; got != want {
		t.Errorf("Compute() gave a live subject the component %+v, want %+v", got, want)
	}
}

func TestComputeMarksAFindingInAGeneratedFileAsOneNoMechanicalEditActsOn(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, map[string]Emitter{
		unusedExportedCode: emitterOf(oneFinding(in, unusedExportedCode, generatedID)),
	})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute() = %v, want one finding", summary(result.Findings))
	}
	found := result.Findings[0]
	if !found.Generated || found.Fixability != generatedFixability {
		t.Errorf("Compute() reported generated=%t and fixability %q for a generated file, want true and %q",
			found.Generated, found.Fixability, generatedFixability)
	}
}

func TestComputeRefusesAPassWithNoResolvedConfiguration(t *testing.T) {
	if _, err := Compute(&Input{}, nil); !errors.Is(err, ErrInput) {
		t.Errorf("Compute() with no configuration = %v, want an ErrInput refusal", err)
	}
}

// swap puts one vocabulary in front of the framework for the length of one test,
// which is how a ceiling below certain is measured: no shipped kind declares one.
func swap(t *testing.T, rows []catalog.Row) {
	t.Helper()

	before := kindsOfCatalog
	kindsOfCatalog = func() []catalog.Row { return rows }
	t.Cleanup(func() { kindsOfCatalog = before })
}

func TestComputeReportsEveryDeclarationOnceOverEveryKindOfThePackage(t *testing.T) {
	t.Parallel()

	emitters := packageEmitters()
	for _, archive := range fixtures(t) {
		t.Run(archive, func(t *testing.T) {
			t.Parallel()

			in := inputOf(t, archive, applicationConfig(), Consumers{})

			result, err := Compute(in, emitters)
			if err != nil {
				t.Fatalf("Compute(every kind of the package, %s) = error %v, want the findings of the pass: two kinds disagree on which code a declaration is reported under",
					archive, err)
			}
			under := make(map[string]string, len(result.Findings))
			for i := range result.Findings {
				found := &result.Findings[i]
				if first, twice := under[found.Symbol.Ref]; twice {
					t.Errorf("the pass over %s reports %s under both %s and %s, want one code",
						archive, found.Symbol.Ref, first, found.Code)
					continue
				}
				under[found.Symbol.Ref] = found.Code
			}
		})
	}
}

func TestTheKindsOfThePackageAgreeOnWhichCodeReportsADeclarationNothingReferences(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"go://example.com/app/api#Reader":      unusedInterfaceCode,
		"go://example.com/app/api#Store.Flush": uncalledInterfaceMethodCode,
		"go://example.com/app/api#Quiet":       enumMemberCode,
		"go://example.com/app/api#Convert[T]":  typeParameterCode,
	}

	in := inputOf(t, "precedence-importable.txtar", applicationConfig(), Consumers{})

	result, err := Compute(in, packageEmitters())
	if err != nil {
		t.Fatalf("Compute(every kind of the package, precedence-importable.txtar) = error %v, want the findings of the pass: an unused-declaration kind takes a subject a more specific kind reports",
			err)
	}
	under := make(map[string]string, len(result.Findings))
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
