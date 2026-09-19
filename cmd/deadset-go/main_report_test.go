package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/report"
	spec "github.com/cplieger/deadset-spec"
)

// The Contract page that publishes the expression a finding line is defined by, and
// how that expression starts, so a test compiles the Contract's own expression
// rather than a copy of it.
const (
	textLinePage     = "contract/grammar/text-line.md"
	expressionPrefix = "^(?<path>"
)

// corpusAnswered is the corpus record a test supplies. Nothing in this command can
// answer the corpus, so the values a report is required to carry come from here:
// what the analyzer states about itself is the caller's to supply, and every test
// below is the caller.
func corpusAnswered() *corpusRecord {
	return &corpusRecord{
		conformance: report.Conformance{
			CorpusVersion: "1.0.0",
			Result:        "pass",
			Digest:        "sha256:" + strings.Repeat("ab", 32),
		},
	}
}

// reportOfArchive is the envelope of one run over the module one archive declares,
// assembled once per archive: several tests read one report of the suppression fixture,
// and one assembly of a fixture module costs about a second under the race detector.
//
// A test whose target is a directory it built itself calls reportOfDir instead.
func reportOfArchive(t *testing.T, files map[string]string) report.Envelope {
	t.Helper()

	// A report names the target relative to the directory the run was invoked from
	// and can name no path outside it, so a run over a fixture is invoked from the
	// fixture. Every test that calls this is therefore sequential.
	t.Chdir(sharedModule(t, files))
	return cachedEnvelope(t.Context(), t, files)
}

// reportOfDir is the envelope of one run over dir, which is what every test below
// reads: the resolution a verb would build, the exemption options it would convert,
// and the assembly itself.
func reportOfDir(t *testing.T, dir string) report.Envelope {
	t.Helper()

	// A report names the target relative to the directory the run was invoked from
	// and can name no path outside it, so a run over a fixture is invoked from the
	// fixture. Every test that calls this is therefore sequential.
	t.Chdir(dir)
	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, exitClean)
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("Setup: exemptOptions(): %v", err)
	}
	envelope, err := reportOf(t.Context(), &resolved, &options, corpusAnswered())
	if err != nil {
		t.Fatalf("reportOf(%s) = error %v, want the report of the run", dir, err)
	}
	return envelope
}

// suppressedArchive is the fixture the suppression tests are driven against: the
// findings archive under one repository configuration, with an ignore document naming
// two of its dead declarations, one that matches a finding of the run and one that
// names a declaration nothing declares.
//
// The ignore document is part of the archive rather than written into the fixture
// afterwards, so that the archive names the whole of what the run reads and every test
// declaring it reads one analysis.
func suppressedArchive(document string) map[string]string {
	files := findingsArchive(document)
	files["deadset-ignore.json"] = `{
  "description": "One entry in effect and one that matches no current finding.",
  "ignore": [
    {
      "code": "DS1002",
      "symbol": "go://example.com/app#forgotten",
      "path": "app.go",
      "reason": "in effect: nothing reaches forgotten and the deletion is scheduled"
    },
    {
      "code": "DS1002",
      "symbol": "go://example.com/app#vanished",
      "path": "app.go",
      "reason": "stale: no declaration of this module answers the reference"
    }
  ]
}
`
	return files
}

func TestReportOfSuppressesTheFindingAnIgnoreEntryBindsAndCountsIt(t *testing.T) {
	envelope := reportOfArchive(t, suppressedArchive(`{"target": {"kind": "application"}}`))

	// The entry bound a declaration the sweep would otherwise have reported, so
	// the mark seeded the sweep and no finding names that declaration.
	for i := range envelope.Findings {
		if got := envelope.Findings[i].Symbol.Ref; got == "go://example.com/app#forgotten" {
			t.Errorf("the report holds a finding about %s under %s, want it suppressed by the ignore entry that binds it",
				got, envelope.Findings[i].Code)
		}
	}

	// Two entries, two reasons; one in effect, because the stale one is in effect
	// for nothing.
	if got := envelope.Totals.SuppressionsInEffect; got != 1 {
		t.Errorf("the report counts %d suppressions in effect, want 1 of the two entries", got)
	}
	if got := envelope.Totals.ReasonsRecorded; got != 2 {
		t.Errorf("the report counts %d reasons recorded, want 2: both entries carry one", got)
	}
}

func TestReportOfCarriesTheStaleEntryAsItsOwnRecord(t *testing.T) {
	envelope := reportOfArchive(t, suppressedArchive(`{"target": {"kind": "application"}}`))

	if len(envelope.StaleSuppressions) != 1 {
		t.Fatalf("the report holds %d stale suppressions, want 1: the entry naming a declaration nothing declares",
			len(envelope.StaleSuppressions))
	}
	stale := &envelope.StaleSuppressions[0]
	switch {
	case stale.Code != "DS1703":
		t.Errorf("the stale suppression is reported under %s, want DS1703", stale.Code)
	case stale.Mechanism != "ignore":
		t.Errorf("the stale suppression names mechanism %q, want %q", stale.Mechanism, "ignore")
	case stale.Entry.Symbol != "go://example.com/app#vanished":
		t.Errorf("the stale suppression carries entry symbol %q, want the reference the entry names",
			stale.Entry.Symbol)
	case stale.Entry.Reason == "":
		t.Errorf("the stale suppression carries no reason, want the one the entry was written with")
	case stale.Position.Path != "deadset-ignore.json":
		t.Errorf("the stale suppression is sited at %q, want the document that holds it", stale.Position.Path)
	}

	// A stale row is its own record and never a row of the finding list.
	for i := range envelope.Findings {
		if envelope.Findings[i].Code == "DS1703" {
			t.Errorf("the report holds a DS1703 row inside its finding list, want it in the stale-suppression array alone")
		}
	}
	if got := envelope.Totals.StaleSuppressions; got != 1 {
		t.Errorf("the report counts %d stale suppressions, want 1", got)
	}
}

func TestReportOfPublishesTheFindingADeclaredEdgeNamesInsideItsEvaluation(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	writeDocument(t, dir, "deadset-edges.json", `{
  "description": "One edge pairing a dead Go declaration with the TypeScript generated from it.",
  "edges": [
    {
      "id": "wire/forgotten",
      "because": "generated",
      "provides": "go://example.com/app#forgotten",
      "used_by": "ts://@example/app/src/wire.ts#forgotten"
    }
  ]
}
`)
	envelope := reportOfDir(t, dir)

	// One record for the own-language side and none for the other: a side no
	// analyzer evaluated is not absent, because nothing looked.
	if len(envelope.EdgeEvaluations) != 1 {
		t.Fatalf("the report holds %d edge evaluations, want 1 for the Go side of the one declared edge",
			len(envelope.EdgeEvaluations))
	}
	evaluated := &envelope.EdgeEvaluations[0]
	switch {
	case evaluated.Edge != "wire/forgotten":
		t.Errorf("the evaluation names edge %q, want %q", evaluated.Edge, "wire/forgotten")
	case evaluated.Side != "provides":
		t.Errorf("the evaluation names side %q, want %q", evaluated.Side, "provides")
	case evaluated.State != "dead":
		t.Errorf("the evaluation reports state %q, want %q: the declaration is one a finding is held for", evaluated.State, "dead")
	case evaluated.Finding == nil:
		t.Fatalf("the dead evaluation carries no pending finding, want the finding the run held for the declaration")
	case evaluated.Finding.Symbol.Ref != "go://example.com/app#forgotten":
		t.Errorf("the pending finding is about %q, want the declaration the edge names", evaluated.Finding.Symbol.Ref)
	}

	// The pending finding appears nowhere else: the report says the symbol is dead
	// on this side and says nothing about deleting it.
	for i := range envelope.Findings {
		if got := envelope.Findings[i].Symbol.Ref; got == "go://example.com/app#forgotten" {
			t.Errorf("the report holds a finding about %s under %s as well as inside the evaluation, want it published there alone",
				got, envelope.Findings[i].Code)
		}
	}
	if got := envelope.Totals.Pending; got != 1 {
		t.Errorf("the report counts %d pending findings, want 1", got)
	}
}

func TestReportOfNamesTheDependencyADeletionOrphans(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n\n" +
			"require example.com/dep v1.0.0\n\nreplace example.com/dep => ./dep\n",
		"app.go": "package main\n\nimport \"example.com/dep/one\"\n\nfunc main() {}\n\n" +
			"// forgotten is what nothing names, and it holds the target's only use of\n" +
			"// the module it imports.\nfunc forgotten() string { return one.Name() }\n",
		"dep/go.mod":       "module example.com/dep\n\ngo 1.27.1\n",
		"dep/one/one.go":   "package one\n\n// Name is the one declaration this module provides.\nfunc Name() string { return \"dep\" }\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
	envelope := reportOfDir(t, dir)

	found := findingUnder(t, envelope.Findings, "DS1002")
	want := []string{"example.com/dep"}
	if got := found.Details.RemovesLastUseOf; !slices.Equal(got, want) {
		t.Errorf("the finding about %s names %v as the dependencies its deletion orphans, want %v",
			found.Symbol.Ref, got, want)
	}
}

func TestReportOfReportsAConfiguredRootThatNamesNothing(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}, "roots": {"patterns": ["go://example.com/app#Absent"]}}`)
	envelope := reportOfDir(t, dir)

	found := findingUnder(t, envelope.Findings, unmatchedRoot)
	switch {
	case found.Symbol.Ref != "go://example.com/app#Absent":
		t.Errorf("%s names %q, want the configured string as the configuration spells it", unmatchedRoot, found.Symbol.Ref)
	case found.Position.Path != repositoryDocument:
		t.Errorf("%s is sited at %q, want %q: a configured root has no line of its own",
			unmatchedRoot, found.Position.Path, repositoryDocument)
	}
}

func TestReportOfNamesEveryMemberTheContractRequiresOfTheRun(t *testing.T) {
	envelope := reportOfArchive(t, findingsArchive(`{"target": {"kind": "application"}}`))

	contractVersion, schemaVersions := contractVersions(t)
	switch {
	case envelope.SchemaVersion != report.SchemaVersion:
		t.Errorf("the report names schema version %q, want %q", envelope.SchemaVersion, report.SchemaVersion)
	case !slices.Contains(schemaVersions, envelope.SchemaVersion):
		t.Errorf("the report names schema version %q, want one of the Contract's %v", envelope.SchemaVersion, schemaVersions)
	case envelope.ContractVersion != contractVersion:
		t.Errorf("the report names contract version %q, want the pinned Contract's %q", envelope.ContractVersion, contractVersion)
	case envelope.Analyzer.Name != name:
		t.Errorf("the report names analyzer %q, want %q", envelope.Analyzer.Name, name)
	case envelope.Analyzer.Version != version():
		t.Errorf("the report names analyzer version %q, want %q", envelope.Analyzer.Version, version())
	case !slices.Equal(envelope.Analyzer.SchemaVersionsAccepted, schemaVersionsAccepted):
		t.Errorf("the report accepts schema versions %v, want %v",
			envelope.Analyzer.SchemaVersionsAccepted, schemaVersionsAccepted)
	case envelope.Target.Kind != "application":
		t.Errorf("the report declares target kind %q, want %q", envelope.Target.Kind, "application")
	case envelope.Target.Identity != "example.com/app":
		t.Errorf("the report names target identity %q, want %q", envelope.Target.Identity, "example.com/app")
	case envelope.Target.Root != ".":
		t.Errorf("the report names target root %q, want %q: the run was invoked from the target",
			envelope.Target.Root, ".")
	}

	// The matrix the run derived, named as the report names it.
	if len(envelope.Configurations) != 1 {
		t.Fatalf("the report names %d build configurations, want the one the derivation answered for the fixture",
			len(envelope.Configurations))
	}
	held := &envelope.Configurations[0]
	if held.ID == "" || held.OS == "" || held.Arch == "" {
		t.Errorf("the report names configuration %+v, want an identifier, an operating system and an architecture", *held)
	}

	// The fixture declares no consumer and holds no cgo file, so both are empty
	// and the declared count agrees with the lists.
	if want := len(envelope.Consumers.Loaded) + len(envelope.Consumers.Unavailable); envelope.Consumers.Declared != want {
		t.Errorf("the report declares %d consumers and lists %d", envelope.Consumers.Declared, want)
	}
	if len(envelope.ExcludedByCgo) != 0 {
		t.Errorf("the report names %d files excluded by cgo, want none: the fixture imports no C", len(envelope.ExcludedByCgo))
	}

	// One test-file rule, matching the fixture's one test file.
	if len(envelope.TestFileRules) != 1 || envelope.TestFileRules[0].Matched != 1 {
		t.Errorf("the report names test-file rules %+v, want one rule matching the fixture's one test file",
			envelope.TestFileRules)
	}
}

// mixedModule is the fixture the round trip is driven against: a main package
// holding a finding about a live declaration beside findings about dead ones, so the
// document exercises both halves of the rule that a finding about a live subject
// names no liveness relation.
func mixedModule(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { println(Narrower()) }\n\n" +
			"// Narrower is exported and referenced only inside the package that declares\n" +
			"// it, so it is live and the export is what the run reports.\n" +
			"func Narrower() string { return \"n\" }\n\n" +
			"// forgotten is what nothing names.\nfunc forgotten() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
}

func TestReportOfRoundTripsThroughTheJSONDocumentItWrites(t *testing.T) {
	envelope := reportOfDir(t, mixedModule(t))

	// Both halves of the presence rule are in the document, or the round trip
	// measures one of them.
	var live, dead int
	for i := range envelope.Findings {
		if envelope.Findings[i].Live {
			live++
			continue
		}
		dead++
	}
	if live == 0 || dead == 0 {
		t.Fatalf("the report holds %d findings about live subjects and %d about dead ones, want at least one of each: %v",
			live, dead, findingCodes(envelope.Findings))
	}

	var written bytes.Buffer
	if err := report.JSON(&written, &envelope, report.Options{}); err != nil {
		t.Fatalf("report.JSON() = %v, want the document of the envelope", err)
	}

	// The decode is strict: a member the envelope does not declare is a member this
	// analyzer should not be writing, whatever the schema would accept.
	decoder := json.NewDecoder(bytes.NewReader(written.Bytes()))
	decoder.DisallowUnknownFields()
	var decoded report.Envelope
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode the document this run wrote: %v\n%s", err, written.String())
	}
	if decoder.More() {
		t.Errorf("the run wrote more than one JSON document")
	}

	var again bytes.Buffer
	if err := report.JSON(&again, &decoded, report.Options{}); err != nil {
		t.Fatalf("report.JSON() over the decoded envelope = %v", err)
	}
	if !bytes.Equal(written.Bytes(), again.Bytes()) {
		t.Errorf("the document does not round-trip:\n--- written\n%s\n+++ read back and written again\n%s",
			written.String(), again.String())
	}
}

func TestReportOfWritesEveryTextLineThePublishedExpressionDefines(t *testing.T) {
	envelope := reportOfArchive(t, suppressedArchive(`{"target": {"kind": "application"}}`))

	var written bytes.Buffer
	if err := report.Text(&written, &envelope, report.Options{}); err != nil {
		t.Fatalf("report.Text() = %v, want the text report of the envelope", err)
	}
	lines := strings.Split(strings.TrimRight(written.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("the text report wrote %d lines, want the findings of the run and a summary:\n%s",
			len(lines), written.String())
	}

	expression := publishedFindingLine(t)
	for i, line := range lines[:len(lines)-1] {
		if !expression.MatchString(line) {
			t.Errorf("line %d does not match the expression the Contract publishes: %q", i+1, line)
		}
	}
	if want := len(envelope.Findings) + len(envelope.StaleSuppressions); len(lines)-1 != want {
		t.Errorf("the text report wrote %d lines before its summary, want %d", len(lines)-1, want)
	}
}

// publishedFindingLine is the expression the Contract publishes for a finding line.
func publishedFindingLine(t *testing.T) *regexp.Regexp {
	t.Helper()

	body, err := spec.Contract.ReadFile(textLinePage)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", textLinePage, err)
	}
	for line := range strings.Lines(string(body)) {
		held := strings.TrimRight(line, "\n")
		if !strings.HasPrefix(held, expressionPrefix) {
			continue
		}
		compiled, err := regexp.Compile(held)
		if err != nil {
			t.Fatalf("Setup: compile the published expression %q: %v", held, err)
		}
		return compiled
	}
	t.Fatalf("Setup: %s publishes no expression starting %q", textLinePage, expressionPrefix)
	return nil
}

func TestReportOfRefusesAnEnvelopeNamingNoConformanceResult(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	t.Chdir(dir)
	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, exitClean)
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("Setup: exemptOptions(): %v", err)
	}

	_, err = reportOf(t.Context(), &resolved, &options, &corpusRecord{})
	if err == nil {
		t.Fatalf("reportOf() with no corpus record wrote a report, want a refusal: a merge refuses a report carrying no conformance result")
	}
	if !errors.Is(err, report.ErrInput) {
		t.Errorf("reportOf() with no corpus record = %v, want a %v", err, report.ErrInput)
	}
	if !strings.Contains(err.Error(), "conformance") {
		t.Errorf("reportOf() with no corpus record = %v, want an error naming the conformance result", err)
	}
}

func TestReportOfRefusesATargetTheRunDirectoryDoesNotContain(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	// The run is invoked from a directory of its own, which does not contain the
	// target: the report format admits no path that climbs out of the run
	// directory, so the run fails rather than writing a document no reader admits.
	t.Chdir(t.TempDir())

	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, exitClean)
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("Setup: exemptOptions(): %v", err)
	}

	_, err = reportOf(t.Context(), &resolved, &options, corpusAnswered())
	if !errors.Is(err, errReportPath) {
		t.Fatalf("reportOf() over a target outside the run directory = %v, want a %v", err, errReportPath)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("reportOf() = %v, want an error naming the target %s", err, dir)
	}

	// The findings pass itself is unaffected: what a report can name is a question
	// about the document, not about the analysis.
	if _, err := findingsOf(t.Context(), &resolved, &options); err != nil {
		t.Errorf("findingsOf() over the same target = %v, want the findings of the run", err)
	}
}

// findingCodes is the codes of a finding list, so a failure names what the run
// reported rather than printing every finding whole.
func findingCodes(findings []kinds.Finding) []string {
	codes := make([]string, len(findings))
	for i := range findings {
		codes[i] = findings[i].Code
	}
	return codes
}

// livenessAbsent is what contract/finding.schema.json forbids a liveness relation
// on: the subject kinds of a finding about something that is not a declaration, and
// the codes whose subject the analysis holds live. A finding matching either half
// carries no relation and every other finding carries one.
type livenessAbsent struct {
	subjectKinds []string
	codes        []string
}

// forbids reports whether the schema forbids the relation on a finding about one
// subject kind under one code, which is the rule read from the schema rather than a
// copy of it.
func (a *livenessAbsent) forbids(subjectKind, code string) bool {
	return slices.Contains(a.subjectKinds, subjectKind) || slices.Contains(a.codes, code)
}

// livenessAbsentRule reads both halves of the rule from the branch of the schema
// that states it, so the test compares the analyzer against the Contract's own
// condition rather than against a copy of it.
func livenessAbsentRule(t *testing.T) livenessAbsent {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/finding.schema.json")
	if err != nil {
		t.Fatalf("Setup: read contract/finding.schema.json: %v", err)
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
	var document struct {
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
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/finding.schema.json: %v", err)
	}

	for _, branch := range document.AllOf {
		if !slices.Contains(branch.Then.Not.Required, "liveness_relation") {
			continue
		}
		var rule livenessAbsent
		// The condition is an alternation of one arm per half of the rule, and a
		// branch stating one half alone spells that arm inline.
		for _, arm := range append(branch.If.AnyOf, branch.If.condition) {
			rule.codes = append(rule.codes, arm.Properties.Code.Enum...)
			rule.subjectKinds = append(rule.subjectKinds, arm.Properties.Symbol.Properties.Kind.Enum...)
		}
		if len(rule.codes) == 0 && len(rule.subjectKinds) == 0 {
			t.Fatalf("Setup: contract/finding.schema.json forbids liveness_relation under no code and no subject kind, so this test pins nothing")
		}
		return rule
	}
	t.Fatalf("Setup: contract/finding.schema.json states no branch forbidding liveness_relation")
	return livenessAbsent{}
}

// livenessModule is the fixture the liveness-relation agreement is measured over: a
// module reporting a code of the Contract's closed list beside codes outside it, a
// configured root that matches nothing, and an ignore entry the reader refuses, so
// the finding list holds a subject of every shape the run can report.
func livenessModule(t *testing.T) string {
	t.Helper()

	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { println(Narrower()) }\n\n" +
			"// Narrower is exported and referenced only inside the package that declares\n" +
			"// it, so it is live and the export is what the run reports.\n" +
			"func Narrower() string { return \"n\" }\n\n" +
			"// forgotten is what nothing names.\nfunc forgotten() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}, "roots": {"patterns": ["go://example.com/app#Absent"]}}`,
	})
	writeDocument(t, dir, "deadset-ignore.json", `{
  "description": "One entry the reader refuses for carrying no reason.",
  "ignore": [
    {
      "code": "DS1002",
      "symbol": "go://example.com/app#forgotten",
      "path": "app.go",
      "reason": ""
    }
  ]
}
`)
	return dir
}

// livenessWritten is which findings of one envelope's document carry a liveness
// relation, keyed by the code and the subject reference of each, which is what a
// reader of the document sees rather than what the finding holds in memory.
func livenessWritten(t *testing.T, envelope *report.Envelope) map[string]bool {
	t.Helper()

	var written bytes.Buffer
	if err := report.JSON(&written, envelope, report.Options{}); err != nil {
		t.Fatalf("report.JSON() = %v, want the document of the envelope", err)
	}
	var document struct {
		Findings []struct {
			Code             string `json:"code"`
			LivenessRelation string `json:"liveness_relation"`
			Symbol           struct {
				Ref string `json:"ref"`
			} `json:"symbol"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(written.Bytes(), &document); err != nil {
		t.Fatalf("decode the document this run wrote: %v\n%s", err, written.String())
	}

	carried := make(map[string]bool, len(document.Findings))
	for _, found := range document.Findings {
		carried[found.Code+" "+found.Symbol.Ref] = found.LivenessRelation != ""
	}
	return carried
}

func TestReportOfOmitsTheLivenessRelationWhereTheContractForbidsIt(t *testing.T) {
	envelope := reportOfDir(t, livenessModule(t))

	rule := livenessAbsentRule(t)
	written := livenessWritten(t, &envelope)

	// No arm of the rule is measured by a finding list that holds none of its
	// subjects, so the fixture's population is checked before the rule is: a finding
	// whose subject is not a declaration, a finding under a code of the list, and a
	// finding a relation decided.
	var byKind, byCode, decided int
	for i := range envelope.Findings {
		found := &envelope.Findings[i]
		switch {
		case slices.Contains(rule.subjectKinds, found.Symbol.Kind):
			byKind++
		case slices.Contains(rule.codes, found.Code):
			byCode++
		default:
			decided++
		}
	}
	if byKind == 0 || byCode == 0 || decided == 0 {
		t.Fatalf("the run reports %d findings whose subject is not a declaration, %d under the Contract's code list and %d a relation decided, want at least one of each: %v",
			byKind, byCode, decided, findingCodes(envelope.Findings))
	}

	for i := range envelope.Findings {
		found := &envelope.Findings[i]
		key := found.Code + " " + found.Symbol.Ref

		// The document says what the finding holds: the member is written exactly
		// where a relation decided the subject.
		if got, want := written[key], !kinds.LivenessAbsent(found); got != want {
			t.Errorf("the document carries a liveness relation for %s about %s: %v, want %v",
				found.Code, found.Symbol.Ref, got, want)
		}

		// The analyzer and the Contract answer the same question about every finding
		// the run produced: the one predicate the analyzer answers it with holds
		// exactly where the schema forbids the relation, under either arm of the rule.
		if got, want := kinds.LivenessAbsent(found), rule.forbids(found.Symbol.Kind, found.Code); got != want {
			t.Errorf("kinds.LivenessAbsent(%s about the %s %s) = %v, want %v: the Contract forbids the relation on the subject kinds %v and the codes %v",
				found.Code, found.Symbol.Kind, found.Symbol.Ref, got, want,
				rule.subjectKinds, rule.codes)
		}
	}
}
