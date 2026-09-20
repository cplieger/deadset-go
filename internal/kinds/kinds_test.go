package kinds

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	spec "github.com/cplieger/deadset-spec/v2"
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
	if got.Live != want.Live {
		add("Live", got.Live, want.Live)
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

func TestComputeRefusesAFindingAboutADeclarationAnEmitterNamesByItsReferenceAlone(t *testing.T) {
	in := handInput(applicationConfig())
	reported := Finding{
		Code:     unusedExportedCode,
		Position: Position{Path: "catalog.go", Line: 14, Column: 6, EndLine: 31},
		Symbol:   Subject{Ref: exportedRef, Name: "Resolve", SizeLines: 18},
		Message:  "the declaration has no reference in the target",
	}

	_, err := Compute(in, map[string]Emitter{unusedExportedCode: emitterOf(reported)})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute(a finding naming %s by reference alone) = %v, want an ErrEmitter refusal: a report's own reference does not identify a declaration, because a blank declaration shares the reference of its container",
			exportedRef, err)
	}
}

func TestTheKeyOfEveryFindingAboutADeclarationIsThatDeclarationsIdentifier(t *testing.T) {
	t.Parallel()

	table := packageEmitters()
	measured := 0
	for _, archive := range fixtures(t) {
		in := inputOf(t, archive, applicationConfig(), Consumers{})

		result := computed(t, in, table)

		for i := range result.Findings {
			found := &result.Findings[i]
			if shapeOf(found.Symbol.Kind) != shapeDeclaration {
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

func TestComputeReportsEveryPositionOnceOverEveryKindOfThePackage(t *testing.T) {
	t.Parallel()

	emitters := packageEmitters()
	for _, archive := range fixtures(t) {
		t.Run(archive, func(t *testing.T) {
			t.Parallel()

			in := inputOf(t, archive, applicationConfig(), Consumers{})

			result, err := Compute(in, emitters)
			if err != nil {
				t.Fatalf("Compute(every kind of the package, %s) = error %v, want the findings of the pass: two kinds disagree on which code reports one subject",
					archive, err)
			}
			under := make(map[string]string, len(result.Findings))
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

// livenessAbsentKinds is the subject kinds the finding schema forbids a liveness
// relation on, read from the branch that states the rule by the members it is written
// with rather than by the position of that branch.
func livenessAbsentKinds(t *testing.T) []string {
	t.Helper()

	const schemaPath = "contract/finding.schema.json"
	body, err := spec.Contract.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", schemaPath, err)
	}
	type condition struct {
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
		var named []string
		for _, arm := range append(branch.If.AnyOf, branch.If.condition) {
			named = append(named, arm.Properties.Symbol.Properties.Kind.Enum...)
		}
		if len(named) == 0 {
			t.Fatalf("Setup: %s forbids a liveness relation under no subject kind, so this test pins nothing",
				schemaPath)
		}
		return named
	}
	t.Fatalf("Setup: %s states no branch forbidding a liveness relation", schemaPath)
	return nil
}

func TestTheShapeTableIsTheContractsLivenessAbsenceList(t *testing.T) {
	t.Parallel()

	absent := livenessAbsentKinds(t)
	want := make([]string, 0, len(absent))
	for _, kind := range absent {
		if kind != theMergesSubject {
			want = append(want, kind)
		}
	}
	slices.Sort(want)

	got := slices.Sorted(maps.Keys(shapes))

	if !slices.Equal(got, want) {
		t.Errorf("the shape table marks %v as a part or a row, want %v, the subject kinds the Contract carries no liveness relation on and this analyzer reports",
			got, want)
	}

	// The kind the list above drops is pinned in both directions, so a Contract that
	// stops forbidding the relation on it and a table that starts holding a shape for
	// it both land here rather than leaving the exemption unread.
	if !slices.Contains(absent, theMergesSubject) {
		t.Errorf("the Contract carries a liveness relation on the subject kind %q, and this table holds no shape for it because it carries none",
			theMergesSubject)
	}
	if got := shapeOf(theMergesSubject); got != shapeDeclaration {
		t.Errorf("the shape table reads the subject kind %q as %d, want the declaration shape, which is what a kind it does not name reads as",
			theMergesSubject, got)
	}
	for kind, held := range shapes {
		if held != shapePart && held != shapeRow {
			t.Errorf("the shape table gives %q the declaration shape, want a part or a row: the table holds the exception rather than the rule",
				kind)
		}
	}
	for _, word := range symbolKinds {
		if word == fileSubject {
			continue
		}
		if shapeOf(word) != shapeDeclaration {
			t.Errorf("a declaration of the inventory reported as subject kind %q reads as %d, want the declaration shape",
				word, shapeOf(word))
		}
	}
}

// theMergesSubject is the one subject kind the corpus's shape table names that this
// table does not: a cross-language edge is the subject of the kind the orchestrator's
// merge reports over the edge evaluations the analyzers publish, so no finding this
// analyzer produces carries it and its shape is not this table's to hold.
const theMergesSubject = "edge"

// corpusSubjectShapes reads the shape table the Contract's corpus publishes: the
// subject kinds it names a part of one declaration and the ones it names a row of a
// document, every other kind being a declaration.
func corpusSubjectShapes(t *testing.T) map[string]shape {
	t.Helper()

	const corpusPath = "corpus/corpus.json"
	body, err := spec.Corpus.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the corpus: %v", corpusPath, err)
	}
	var document struct {
		SubjectShapes struct {
			Part []string `json:"part"`
			Row  []string `json:"row"`
		} `json:"subject_shapes"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", corpusPath, err)
	}
	if len(document.SubjectShapes.Part) == 0 || len(document.SubjectShapes.Row) == 0 {
		t.Fatalf("Setup: %s names %d part kinds and %d row kinds, so this test pins nothing",
			corpusPath, len(document.SubjectShapes.Part), len(document.SubjectShapes.Row))
	}
	held := make(map[string]shape, len(document.SubjectShapes.Part)+len(document.SubjectShapes.Row))
	for _, kind := range document.SubjectShapes.Part {
		held[kind] = shapePart
	}
	for _, kind := range document.SubjectShapes.Row {
		held[kind] = shapeRow
	}
	return held
}

// TestTheShapeTableIsTheCorpusShapeTable holds this table against the reading the
// Contract's corpus publishes of the same fact.
//
// The corpus publishes the table because what a suppression record can bind to follows
// from it: a record binds to a declaration, a part is suppressed through the
// declaration its own reference names, and a row has no suppression at all. The corpus
// runner reads the corpus's copy so that an amendment moves it without an edit, which
// leaves the two readings to agree, and a drift between them would change what a corpus
// run measures without changing what any report says.
func TestTheShapeTableIsTheCorpusShapeTable(t *testing.T) {
	t.Parallel()

	published := corpusSubjectShapes(t)
	for kind, want := range published {
		if kind == theMergesSubject {
			continue
		}
		if got := shapeOf(kind); got != want {
			t.Errorf("the shape table reads the subject kind %q as %d, want %d as the corpus publishes it",
				kind, got, want)
		}
	}
	for kind, held := range shapes {
		if _, named := published[kind]; !named {
			t.Errorf("the shape table reads the subject kind %q as %d and the corpus names it neither a part nor a row, so the corpus reads it as a declaration",
				kind, held)
		}
	}

	// The one kind the first loop skips is pinned in both directions, so a corpus that
	// stops naming it a row and a table that starts holding a shape for it both land
	// here rather than leaving the exemption unread.
	if got, want := published[theMergesSubject], shapeRow; got != want {
		t.Errorf("the corpus reads the subject kind %q as %d, want %d, which is why this table holds no shape for it",
			theMergesSubject, got, want)
	}
	if got := shapeOf(theMergesSubject); got != shapeDeclaration {
		t.Errorf("the shape table reads the subject kind %q as %d, want the declaration shape, which is what a kind it does not name reads as: a kind this analyzer now reports is a kind the table owes a shape",
			theMergesSubject, got)
	}
}

func TestLivenessAbsentAnswersForEveryFindingTheContractForbidsTheRelationOn(t *testing.T) {
	t.Parallel()

	absent := livenessAbsentKinds(t)
	table := packageEmitters()
	measured := 0
	for _, archive := range fixtures(t) {
		in := inputOf(t, archive, applicationConfig(), Consumers{})

		for _, found := range computed(t, in, table).Findings {
			if !slices.Contains(absent, found.Symbol.Kind) {
				continue
			}
			measured++
			if !LivenessAbsent(&found) {
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
		Code:     unusedDependencyCode,
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

	result := computed(t, in, map[string]Emitter{unusedDependencyCode: emitterOf(requirementFinding())})

	if len(result.Findings) != 1 {
		t.Fatalf("Compute(%s over a requirement) returned %d findings, want 1: %v",
			unusedDependencyCode, len(result.Findings), summary(result.Findings))
	}
	reported := requirementFinding()
	want := Finding{
		Code:            unusedDependencyCode,
		Kind:            "unused-dependency",
		Language:        "go",
		Position:        reported.Position,
		Symbol:          reported.Symbol,
		Class:           Certain,
		Confidence:      Certain,
		Relation:        graph.ReferenceCounting,
		Live:            true,
		Component:       Component{ID: "deadset-go/c-2", Root: true, SymbolCount: 1},
		RetainedBy:      []string{},
		Configurations:  []string{"linux-amd64", "linux-arm64"},
		ConsumersLoaded: []string{},
		Fixability:      "manual",
		Severity:        config.Deny,
		Message:         reported.Message,
	}
	if diff := differences(result.Findings[0], want); diff != "" {
		t.Errorf("Compute(%s over a requirement) filled the finding as\n%s", unusedDependencyCode, diff)
	}
}

func TestComputeRefusesTwoFindingsAboutOneSubjectThatIsNoDeclaration(t *testing.T) {
	in := handInput(applicationConfig())
	twice := requirementFinding()

	_, err := Compute(in, map[string]Emitter{unusedDependencyCode: emitterOf(twice, twice)})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute(%s reporting one requirement twice) = %v, want an ErrEmitter refusal: %s is reported twice",
			unusedDependencyCode, err, requirementRef)
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
				Code:    unusedExportedCode,
				Symbol:  Subject{Ref: "go://example.com/app#Absent", Kind: subject, Name: "Absent", SizeLines: 1},
				Message: "the declaration has no reference in the target",
			}

			_, err := Compute(in, map[string]Emitter{unusedExportedCode: emitterOf(reported)})
			if !errors.Is(err, ErrEmitter) {
				t.Errorf("Compute(%s subject the inventory does not hold) = %v, want an ErrEmitter refusal",
					kind, err)
			}
		})
	}
}

func TestComputeRefusesARowThatNamesNoRecord(t *testing.T) {
	in := handInput(applicationConfig())
	unnamed := requirementFinding()
	unnamed.Symbol.Ref = ""

	_, err := Compute(in, map[string]Emitter{unusedDependencyCode: emitterOf(unnamed)})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute(%s naming no reference) = %v, want an ErrEmitter refusal: a %s row is found by the record it names, because its position names the document",
			unusedDependencyCode, err, dependencySubject)
	}
}

func TestComputeRefusesAPartOfADeclarationTheInventoryDoesNotHold(t *testing.T) {
	in := handInput(applicationConfig())
	orphan := findingAt(unusedParameterCode, Subject{
		Ref:       "go://example.com/app#Absent",
		Kind:      parameterSubject,
		Name:      "limit",
		SizeLines: 1,
	}, Position{Path: "catalog.go", Line: 14, Column: 20, EndLine: 14},
		"parameter limit is never read in the body")

	_, err := Compute(in, map[string]Emitter{unusedParameterCode: emitterOf(orphan)})
	if !errors.Is(err, ErrEmitter) {
		t.Errorf("Compute(a %s of a declaration the inventory does not hold) = %v, want an ErrEmitter refusal: a part belongs to a declaration and names it by reference",
			parameterSubject, err)
	}
}
