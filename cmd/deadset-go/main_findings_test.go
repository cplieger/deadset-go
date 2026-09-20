package main

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/suppress"
)

// goKindsWithoutEmitter are the live Go kinds the emitters table holds no entry
// for, so no run of that table reports one. Each is named rather than counted, so
// the table's test says which kinds the table is silent about instead of allowing a
// kind to go missing unnoticed.
//
// DS1705 is the merge's, which is another product.
var goKindsWithoutEmitter = []string{
	"DS1705", // the stale cross-language edge, which the merge answers
}

// findingsArchive is the module the findings pass is driven against: an application
// whose entry point reaches one declaration and whose three others are dead in three
// different ways, every one of them unexported so that no visibility kind has a
// subject and the six unused-declaration kinds are what the pass answers.
//
// The declaration order is the order the canonical key puts the findings in, which
// is what lets the expectations below be written as a list.
func findingsArchive(document string) map[string]string {
	return map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { used() }\n\n" +
			"// used is what the entry point calls.\nfunc used() {}\n\n" +
			"// forgotten is what nothing names.\nfunc forgotten() {}\n\n" +
			"// probed is what one test names and no production declaration names.\nfunc probed() {}\n\n" +
			"// retired is what nothing names, and it says so.\n//\n" +
			"// Deprecated: nothing calls it.\nfunc retired() {}\n",
		"app_test.go": "package main\n\nimport \"testing\"\n\n" +
			"func TestProbed(t *testing.T) { probed() }\n",
		repositoryDocument: document,
	}
}

// findingsFixture writes that archive in a directory of the calling test's own, which
// is what a test that invokes a verb over the target or writes a document into it
// takes. A test that only reads one analysis of the archive calls cachedFindings
// instead and shares the analysis.
func findingsFixture(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, findingsArchive(document))
}

// findingsOfDir is the findings of one run over dir, which is what every test below
// reads: the resolution a verb would build, the exemption options it would convert,
// and the pass itself.
func findingsOfDir(t *testing.T, dir string) findingSet {
	t.Helper()

	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, exitClean)
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("Setup: exemptOptions(): %v", err)
	}
	set, err := findingsOf(t.Context(), &resolved, &options)
	if err != nil {
		t.Fatalf("findingsOf(%s) = error %v, want the findings of the run", dir, err)
	}
	return set
}

func TestEmittersNameEveryLiveGoKindOrSayWhichHasNoRule(t *testing.T) {
	t.Parallel()

	var published []string
	for _, row := range catalog.Kinds() {
		if !slices.Contains(row.Languages, string(language)) {
			continue
		}
		published = append(published, row.Code)
		_, wired := emitters[row.Code]
		named := slices.Contains(goKindsWithoutEmitter, row.Code)
		switch {
		case wired && named:
			t.Errorf("%s has an emitter and is named as having none", row.Code)
		case !wired && !named:
			t.Errorf("%s is a live Go kind with no emitter and is not named as one: a kind absent from both reports nothing and nothing says so", row.Code)
		}
	}

	if len(published) != len(emitters)+len(goKindsWithoutEmitter) {
		t.Errorf("the vocabulary publishes %d Go kinds, the table holds %d and %d are named without a rule",
			len(published), len(emitters), len(goKindsWithoutEmitter))
	}
	for code := range emitters {
		row, live := catalog.Kind(code)
		if !live {
			t.Errorf("the table registers an emitter under %s, which names no live kind", code)
			continue
		}
		if !slices.Contains(row.Languages, string(language)) {
			t.Errorf("the table registers an emitter under %s, which the vocabulary gives languages %v and not %s",
				code, row.Languages, language)
		}
	}
	for _, code := range goKindsWithoutEmitter {
		if _, live := catalog.Kind(code); !live {
			t.Errorf("%s is named as a live Go kind without a rule, and the vocabulary does not hold it", code)
		}
	}
}

func TestFindingsOfReportsEveryDeadDeclarationOfAModuleOnceAndInTheCanonicalOrder(t *testing.T) {
	t.Parallel()

	set := cachedFindings(t.Context(), t, findingsArchive(`{"target": {"kind": "application"}}`))

	// app.go before app_test.go, and within a file by line: the unreferenced
	// declaration, the one only a test names, the deprecated one, then the test
	// whose every production reference is dead.
	want := []string{
		"app.go\t9\tDS1002\tgo://example.com/app#forgotten",
		"app.go\t12\tDS1004\tgo://example.com/app#probed",
		"app.go\t17\tDS1006\tgo://example.com/app#retired",
		"app_test.go\t5\tDS1005\tgo://example.com/app#TestProbed",
	}
	if got := reported(set.result.Findings); !slices.Equal(got, want) {
		t.Errorf("findingsOf() reported\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(set.loaded.unmatched) != 0 {
		t.Errorf("findingsOf() reported %d configured strings as naming nothing, want 0: the fixture configures no root pattern",
			len(set.loaded.unmatched))
	}

	// One symbol is reported once, which is what the framework refuses to break
	// and what the whole table over one real module is the measurement of.
	seen := make(map[string]string, len(set.result.Findings))
	for _, found := range set.result.Findings {
		if first, twice := seen[found.Symbol.Ref]; twice {
			t.Errorf("findingsOf() reported %s under %s and under %s", found.Symbol.Ref, first, found.Code)
		}
		seen[found.Symbol.Ref] = found.Code
	}
}

func TestFindingsOfCompletesAFindingWithEveryFieldTheContractRequires(t *testing.T) {
	t.Parallel()

	set := cachedFindings(t.Context(), t, findingsArchive(`{"target": {"kind": "application"}}`))
	found := findingUnder(t, set.result.Findings, "DS1002")

	// The kind fills the code, the position, the subject, the relation and the
	// message; the framework fills everything else, and the two together are what
	// the report schema requires of one row.
	if got, want := found.Kind, "unused-unexported"; got != want {
		t.Errorf("the finding's issue kind is %q, want %q as the vocabulary names the code", got, want)
	}
	if got, want := found.Language, string(config.GoLanguage); got != want {
		t.Errorf("the finding's language is %q, want %q", got, want)
	}
	if got, want := found.Position, (kinds.Position{Path: "app.go", Line: 9, Column: 6, EndLine: 9}); got != want {
		t.Errorf("the finding's position is %+v, want %+v", got, want)
	}
	if got, want := found.Symbol, (kinds.Subject{
		Ref:       "go://example.com/app#forgotten",
		Kind:      "function",
		Name:      "forgotten",
		SizeLines: 1,
	}); got != want {
		t.Errorf("the finding's symbol is %+v, want %+v", got, want)
	}
	if got, want := found.Class, kinds.Certain; got != want {
		t.Errorf("the finding's reachability class is %q, want %q: an unexported declaration of any target is certain", got, want)
	}
	if got, want := found.Confidence, kinds.Certain; got != want {
		t.Errorf("the finding's confidence is %q, want %q: the kind's ceiling is certain", got, want)
	}
	if got, want := found.Relation, graph.ReferenceCounting; got != want {
		t.Errorf("the finding's liveness relation is %q, want %q: nothing references the declaration at all", got, want)
	}
	if found.TestOnly {
		t.Error("the finding is test-only, want production: no test references the declaration either")
	}
	if found.Generated {
		t.Error("the finding is about generated source, want hand-written: the fixture holds no generation marker")
	}
	if len(found.RetainedBy) != 0 {
		t.Errorf("the finding names %v as retaining it, want nothing: no finding carries a retention", found.RetainedBy)
	}
	if got, want := found.Configurations, []string{load.HostConfiguration().ID}; !slices.Equal(got, want) {
		t.Errorf("the finding holds under configurations %v, want %v: the matrix the tree implies is the host's", got, want)
	}
	if len(found.ConsumersLoaded) != 0 {
		t.Errorf("the finding names %v as loaded consumers, want none: the scope declares no consumer", found.ConsumersLoaded)
	}
	if got, want := found.Message, "unexported function has no reference in the target"; got != want {
		t.Errorf("the finding says %q, want %q", got, want)
	}
	if got := found.Details; !reflect.DeepEqual(got, kinds.Details{}) {
		t.Errorf("the finding carries details %+v, want none: an unreferenced declaration has no per-kind member", got)
	}

	// The vocabulary owns the severity and the fixability of every kind, so the
	// two are read from it rather than restated here.
	row, live := catalog.Kind("DS1002")
	if !live {
		t.Fatal("Setup: the vocabulary does not hold DS1002")
	}
	if got, want := string(found.Severity), row.DefaultSeverity; got != want {
		t.Errorf("the finding's severity is %q, want %q as the vocabulary sets it", got, want)
	}
	if got, want := found.Fixability, row.Fixability; got != want {
		t.Errorf("the finding's fixability is %q, want %q as the vocabulary sets it", got, want)
	}

	// The component is minted by the run, so its number is the run's; what the
	// finding says about it is that the declaration is a root of a component of
	// one symbol and one deletable line.
	if got := found.Component.ID; !strings.HasPrefix(got, name+"/c-") {
		t.Errorf("the finding's component is %q, want an identifier this analyzer minted", got)
	}
	if got, want := found.Component.Root, true; got != want {
		t.Errorf("the finding's component root is %t, want %t", got, want)
	}
	if got, want := found.Component.SymbolCount, 1; got != want {
		t.Errorf("the finding's component holds %d symbols, want %d", got, want)
	}
	if got, want := found.Component.DeletableLines, 1; got != want {
		t.Errorf("the finding's component holds %d deletable lines, want %d", got, want)
	}
}

func TestFindingsOfReportsNothingAboutAModuleWhereEveryDeclarationIsLive(t *testing.T) {
	t.Parallel()

	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { used() }\n\n" +
			"// used is what the entry point calls.\nfunc used() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	set := findingsOfDir(t, dir)
	if len(set.result.Findings) != 0 {
		t.Errorf("findingsOf() reported\n%s\nwant nothing: the entry point reaches every declaration",
			strings.Join(reported(set.result.Findings), "\n"))
	}
}

func TestFindingsOfDropsTheKindTheConfigurationAllows(t *testing.T) {
	t.Parallel()

	set := cachedFindings(t.Context(), t,
		findingsArchive(`{"target": {"kind": "application"}, "severity": {"DS1002": "allow"}}`))

	// The kind the configuration allows reports nothing and no other kind claims
	// its subject, because a severity decides what is reported and never which
	// kind a declaration belongs to.
	for _, found := range set.result.Findings {
		if found.Code == "DS1002" {
			t.Errorf("findingsOf() reported %s under DS1002, which the configuration allows", found.Symbol.Ref)
		}
		if found.Symbol.Ref == "go://example.com/app#forgotten" {
			t.Errorf("findingsOf() reported the allowed kind's subject under %s", found.Code)
		}
	}
	if got, want := len(set.result.Findings), 3; got != want {
		t.Errorf("findingsOf() reported %d findings, want %d:\n%s", got, want, strings.Join(reported(set.result.Findings), "\n"))
	}
}

func TestFindingsOfFailures(t *testing.T) {
	t.Parallel()

	codes := contractExitCodes(t)
	tests := []struct {
		name  string
		files map[string]string
		want  int
	}{
		{
			name: "a_target_that_does_not_type-check_is_a_failure",
			files: map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
				"app.go":           "package main\n\nfunc main() { missing() }\n",
				repositoryDocument: `{"target": {"kind": "application"}}`,
			},
			want: codes["failure"],
		},
		{
			name: "a_configured_template_directory_the_target_does_not_hold_is_a_usage_error",
			files: map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
				"app.go":           "package main\n\nfunc main() {}\n",
				repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"template_dirs": ["absent"]}}`,
			},
			want: codes["usage"],
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := writeModule(t, tc.files)
			resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
			if code != exitClean {
				t.Fatalf("Setup: resolve the configuration = %d, want %d", code, exitClean)
			}
			options, err := exemptOptions(&resolved.config)
			if err != nil {
				t.Fatalf("Setup: exemptOptions(): %v", err)
			}

			set, err := findingsOf(t.Context(), &resolved, &options)
			if err == nil {
				t.Fatalf("findingsOf() = %d findings, <nil>, want an error", len(set.result.Findings))
			}
			if got := exitCodeFor(err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", err, got, tc.want)
			}
		})
	}
}

func TestFindingsOfRefusesATableRegisteredUnderACodeTheVocabularyDoesNotHold(t *testing.T) {
	t.Parallel()

	// The pass a defect in a kind's table produces is a failure rather than a
	// finding list, which is the exit code an emitter's own error takes as well.
	in := &kinds.Input{Config: &config.Config{}}
	_, err := kinds.Compute(in, map[string]kinds.Emitter{"DS1400": kinds.UnusedExported})
	if err == nil {
		t.Fatal("kinds.Compute(a table naming a retired code) = _, <nil>, want an error")
	}
	if got, want := exitCodeFor(err), exitFailure; got != want {
		t.Errorf("exitCodeFor(%v) = %d, want %d", err, got, want)
	}

	_, err = kinds.Compute(nil, emitters)
	if err == nil {
		t.Fatal("kinds.Compute(no input) = _, <nil>, want an error")
	}
	if got, want := exitCodeFor(err), exitFailure; got != want {
		t.Errorf("exitCodeFor(%v) = %d, want %d", err, got, want)
	}
}

func TestGeneratedPathsNamesTheGeneratedFilesOfTheRun(t *testing.T) {
	t.Parallel()

	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { used() }\n\n" +
			"// used is what the entry point calls.\nfunc used() {}\n",
		"wire.go": "// Code generated by a generator. DO NOT EDIT.\n\npackage main\n\n" +
			"// Forgotten is what nothing names.\nfunc Forgotten() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"generated_files": "include"}}`,
	})

	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration = %d, want %d", code, exitClean)
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("Setup: exemptOptions(): %v", err)
	}
	analyzed, err := analysisOf(t.Context(), &resolved, &options, modeOf(&resolved.config, modeProduction))
	if err != nil {
		t.Fatalf("Setup: analysisOf(): %v", err)
	}

	paths, err := generatedPaths(analyzed.per)
	if err != nil {
		t.Fatalf("generatedPaths() = _, %v, want the generated files of the run", err)
	}
	if !paths["wire.go"] {
		t.Errorf("generatedPaths() = %v, want it to name wire.go, which carries the generation marker", paths)
	}
	if paths["app.go"] {
		t.Errorf("generatedPaths() = %v, want it not to name app.go, which carries none", paths)
	}
}

func TestFindingsOfSaysAFindingAboutGeneratedSourceIsGenerated(t *testing.T) {
	t.Parallel()

	// The configuration includes generated source, so the class that would
	// otherwise hold a generated declaration back retains nothing and the
	// declaration is reported, carrying the fact that a generator wrote it.
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { used() }\n\n" +
			"// used is what the entry point calls.\nfunc used() {}\n",
		"wire.go": "// Code generated by a generator. DO NOT EDIT.\n\npackage main\n\n" +
			"// forgotten is what nothing names.\nfunc forgotten() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"generated_files": "include"}}`,
	})

	set := findingsOfDir(t, dir)
	found := findingUnder(t, set.result.Findings, "DS1002")
	if got, want := found.Symbol.Ref, "go://example.com/app#forgotten"; got != want {
		t.Fatalf("the pass reports %s under DS1002, want %s", got, want)
	}
	if !found.Generated {
		t.Errorf("the finding about %s is not marked generated, want generated: %s carries the generation marker",
			found.Symbol.Ref, found.Position.Path)
	}
	if got, want := found.Fixability, "none"; got != want {
		t.Errorf("the finding's fixability is %q, want %q: a generated file is fixed by its generator", got, want)
	}
}

func TestConsumersOfNamesWhatTheRunLoaded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		complete bool
		per      []configured
		want     kinds.Consumers
	}{
		{
			name: "a_run_that_loaded_no_consumer",
			want: kinds.Consumers{},
		},
		{
			name:     "a_run_whose_configuration_declares_the_set_complete",
			complete: true,
			per: []configured{{result: load.Result{Consumers: []load.Consumer{
				{ID: "example.com/one"}, {ID: "example.com/two"},
			}}}},
			want: kinds.Consumers{
				Declared: []string{"example.com/one", "example.com/two"},
				Loaded:   []string{"example.com/one", "example.com/two"},
				Complete: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.Consumers.Complete = tc.complete
			got := consumersOf(&cfg, tc.per)
			if !slices.Equal(got.Declared, tc.want.Declared) || !slices.Equal(got.Loaded, tc.want.Loaded) ||
				got.Complete != tc.want.Complete {
				t.Errorf("consumersOf() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// reported is one line per finding, in the order the pass returned them: the file
// and line, the code and the symbol.
func reported(findings []kinds.Finding) []string {
	lines := make([]string, len(findings))
	for i, found := range findings {
		lines[i] = fmt.Sprintf("%s\t%d\t%s\t%s", found.Position.Path, found.Position.Line, found.Code, found.Symbol.Ref)
	}
	return lines
}

// findingUnder is the one finding a code reports, and a failure where the pass
// reported none or more than one.
func findingUnder(t *testing.T, findings []kinds.Finding, code string) kinds.Finding {
	t.Helper()

	var held []kinds.Finding
	for _, found := range findings {
		if found.Code == code {
			held = append(held, found)
		}
	}
	if len(held) != 1 {
		t.Fatalf("the pass reports %d findings under %s, want 1:\n%s", len(held), code, strings.Join(reported(findings), "\n"))
	}
	return held[0]
}

// testEvidenceModule is the fixture the exemption mode is measured over: a field
// production writes and never reads, whose only marshalling is in a test file. The
// encoding class holds it under a plain sweep and holds nothing under a production
// one, so the two verbs answer differently about one declaration.
func testEvidenceModule(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { println(record().Name) }\n\n" +
			"// Record is what the program fills.\ntype Record struct {\n" +
			"\t// Name is what the entry point reads.\n\tName string `json:\"name\"`\n\n" +
			"\t// Tally is written here and read by nothing this module compiles into its\n" +
			"\t// program, so only the test that marshals a Record uses it.\n" +
			"\tTally int `json:\"tally\"`\n}\n\n" +
			"// record is the value the entry point prints.\nfunc record() Record {\n" +
			"\tr := Record{Name: \"n\"}\n\tr.Tally = 1\n\treturn r\n}\n",
		"app_test.go": "package main\n\nimport (\n\t\"encoding/json\"\n\t\"testing\"\n)\n\n" +
			"func TestRecordMarshals(t *testing.T) {\n" +
			"\tif _, err := json.Marshal(record()); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
}

func TestAnalysisOfComputesItsExemptionsUnderTheModeItSweepsIn(t *testing.T) {
	dir := testEvidenceModule(t)
	const subject = "go://example.com/app#Record.Tally"

	resolved, code := resolve("print-retained", printRetainedUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, exitClean)
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("Setup: exemptOptions(): %v", err)
	}

	// The plain mode counts the test's marshalling as the use it is evidence of, so
	// the class holds the field; the production mode holds nothing by it.
	for _, test := range []struct {
		named string
		want  string
	}{
		{named: modePlain, want: string(exempt.EncodingReflection)},
		{named: modeProduction, want: ""},
	} {
		mode := modeOf(&resolved.config, test.named)
		analyzed, analysisErr := analysisOf(t.Context(), &resolved, &options, mode)
		if analysisErr != nil {
			t.Fatalf("analysisOf(%s) = %v, want the analysis of the run", test.named, analysisErr)
		}
		var got string
		for i := range analyzed.exemptions {
			if analyzed.stages.refs[analyzed.exemptions[i].ID] == subject {
				got = analyzed.exemptions[i].Class
				break
			}
		}
		if got != test.want {
			t.Errorf("analysisOf(%s) retains %s by %q, want %q: the only marshalling of the value is written in a test file",
				test.named, subject, got, test.want)
		}
	}

	// What the mode decides is what the run reports: the field is a candidate of the
	// production sweep and a kind names it, retained by nothing.
	set := findingsOfDir(t, dir)
	var reported *kinds.Finding
	for i := range set.result.Findings {
		if set.result.Findings[i].Symbol.Ref == subject {
			reported = &set.result.Findings[i]
			break
		}
	}
	if reported == nil {
		t.Fatalf("the findings pass reports nothing about %s, want a finding\nreported: %v",
			subject, findingCodes(set.result.Findings))
	}
	if len(reported.RetainedBy) != 0 {
		t.Errorf("the finding about %s names %v as what retained it, want nothing", subject, reported.RetainedBy)
	}
}

func TestTheSuppressionRecordsAreReadInTheOrderTheGrammarResolvesThem(t *testing.T) {
	t.Parallel()

	// The records the run reads decide which of several adjudications of one finding
	// is in effect, so the order they are read in is the grammar's: every inline
	// directive by position, then the ignore file in document order, then the
	// baseline. The fixture writes each mechanism twice, with the directive of the
	// last file below every entry of the ignore document in any file order.
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { used() }\n\n" +
			"// used is what the entry point calls.\nfunc used() {}\n\n" +
			"//deadset:ignore DS1002 -- the first directive by position\nfunc first() {}\n",
		"zz.go": "package main\n\n//deadset:ignore DS1002 -- the last directive by position\nfunc last() {}\n",
		suppress.IgnoreFileName: `{
  "description": "Two entries in document order.",
  "ignore": [
    { "code": "DS1002", "symbol": "go://example.com/app#first", "path": "app.go", "reason": "the first entry" },
    { "code": "DS1002", "symbol": "go://example.com/app#last", "path": "zz.go", "reason": "the second entry" }
  ]
}
`,
		suppress.BaselineFileName: `{
  "description": "One recorded row.",
  "baseline": [
    { "code": "DS1002", "symbol": "go://example.com/app#first", "path": "app.go", "reason": "recorded by deadset-go 1.15.0" }
  ]
}
`,
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration = %d, want %d", code, exitClean)
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("Setup: exemptOptions(): %v", err)
	}
	analyzed, err := analysisOf(t.Context(), &resolved, &options, modeOf(&resolved.config, modeProduction))
	if err != nil {
		t.Fatalf("Setup: analysisOf(): %v", err)
	}

	read := make([]string, 0, len(analyzed.marks))
	for i := range analyzed.marks {
		mark := &analyzed.marks[i]
		read = append(read, fmt.Sprintf("%s %s:%d", mark.Mechanism, mark.Site.Filename, mark.Site.Line))
	}
	want := []string{
		"inline app.go:8",
		"inline zz.go:3",
		"ignore " + suppress.IgnoreFileName + ":4",
		"ignore " + suppress.IgnoreFileName + ":5",
		"baseline " + suppress.BaselineFileName + ":4",
	}
	if !slices.Equal(read, want) {
		t.Errorf("the run read the records as %v, want %v", read, want)
	}
}
