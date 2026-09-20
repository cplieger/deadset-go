package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/suppress"
	spec "github.com/cplieger/deadset-spec/v2"
	"golang.org/x/tools/txtar"
)

// Where the pinned Contract publishes the corpus, and what one fixture holds.
const (
	corpusDocument  = "corpus/corpus.json"
	corpusFixtures  = "corpus/fixtures"
	expectationFile = "expect.json"
	manifestSection = "fixture.json"
	goRendering     = "go.txtar"
)

// targetSection is the directory of a rendering that holds the target, which is what
// the run analyzes; every other directory of a rendering is a consumer.
const targetSection = "target"

// corpusLanguage is the language this analyzer answers the corpus for, which is the
// language it claims and the only renderings it loads.
const corpusLanguage = "go"

// The three values a fixture and every expectation inside it end as.
const (
	answerPass = "pass"
	answerGap  = "gap"
	answerFail = "fail"
)

// reportNone is the literal an expectation carries where the analyzer must report
// nothing at the resolved position.
const reportNone = "none"

// The two facts about the world a fixture declares, which is the closed set the
// expectation file's closed_world member draws from.
const (
	worldConsumers = "consumers"
	worldMatrix    = "matrix"
)

// staleSuppression is the issue kind that reports an entry matching no current
// finding, which the second phase must not produce for an entry the runner wrote.
const staleSuppression = "DS1703"

// goldenVariable is the gate that rewrites the committed conformance results, and
// goldenCommand is the invocation a failure names so a reader does not go looking for
// it.
const (
	goldenVariable = "UPDATE_GOLDEN"
	goldenCommand  = "UPDATE_GOLDEN=1 go test ./cmd/deadset-go/ -run TestConformanceCorpus"
)

// corpusExpectation is one row of a fixture's expectation file: the subject by its
// fixture-local logical name, and what an analyzer must report about it. A member the
// row leaves out states nothing and is not checked.
type corpusExpectation struct {
	Symbol            string         `json:"symbol"`
	Report            string         `json:"report"`
	SymbolKind        string         `json:"symbol_kind"`
	Confidence        string         `json:"confidence"`
	ReachabilityClass string         `json:"reachability_class"`
	LivenessRelation  string         `json:"liveness_relation"`
	Configurations    []string       `json:"configurations"`
	RetainedBy        []string       `json:"retained_by"`
	Details           *corpusDetails `json:"details"`
}

// corpusDetails are the details members an expectation pins, drawn from the closed
// set the corpus's expectation file declares: the members of a finding's details
// whose value is language-neutral, which is what an expectation binding to every
// rendering can state. A member the row leaves out states nothing and is not
// compared, so every member is optional here as it is there.
type corpusDetails struct {
	NarrowerVisibility string   `json:"narrower_visibility,omitempty"`
	ExcludedBy         string   `json:"excluded_by,omitempty"`
	DependencyClass    string   `json:"dependency_class,omitempty"`
	Replacement        string   `json:"replacement,omitempty"`
	Mechanism          string   `json:"mechanism,omitempty"`
	Edge               string   `json:"edge,omitempty"`
	Overlap            []string `json:"overlap,omitempty"`
}

// corpusFixtureFile is one fixture's expectation file: the renderings it has, the
// target kind and consumer set the runner configures, and the exhaustive expectation
// list.
type corpusFixtureFile struct {
	ConfiguredRoots map[string]string   `json:"configured_roots"`
	Name            string              `json:"name"`
	Languages       []string            `json:"languages"`
	TargetKind      string              `json:"target_kind"`
	Consumers       []string            `json:"consumers"`
	ClosedWorld     []string            `json:"closed_world"`
	Expect          []corpusExpectation `json:"expect"`
}

// corpusSubjectShapes is the corpus's own reading of the shape of every subject a
// finding is about: the kinds it names a part of one declaration and the kinds it
// names a row of a document. Every kind neither list names is a declaration, so the
// document carries the two exceptions rather than the rule.
type corpusSubjectShapes struct {
	Part []string `json:"part"`
	Row  []string `json:"row"`
}

// suppressible reports whether a suppression record can bind to a finding about one
// kind of subject, which is what decides whether the suppression phase applies to an
// expectation about it: a record binds to a declaration, so a declaration and a part
// of one are suppressible, a part through the declaration its own reference names,
// while a row reports a record of a document that no record binds to and whose remedy
// is the change the finding names or the severity the configuration gives its code.
//
// Only the row list decides it, because the two shapes a record reaches are the two a
// kind the lists leave out cannot be.
func (shapes *corpusSubjectShapes) suppressible(subjectKind string) bool {
	return !slices.Contains(shapes.Row, subjectKind)
}

// corpusManifest is one rendering's manifest: the build matrix a run derives from the
// rendering, and the file and line this rendering declares each logical name on.
type corpusManifest struct {
	Configurations []string              `json:"configurations"`
	Symbols        map[string]corpusSite `json:"symbols"`
}

// corpusSite is where one rendering declares one logical name, relative to the
// rendering root.
type corpusSite struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// conformanceResults is the per-fixture record the runner writes, in the field order
// the Contract's results schema declares, which is the order the digest is taken over.
type conformanceResults struct {
	CorpusVersion string             `json:"corpus_version"`
	Product       conformanceProduct `json:"product"`
	Result        string             `json:"result"`
	Totals        conformanceTotals  `json:"totals"`
	Fixtures      []fixtureAnswer    `json:"fixtures"`
}

// conformanceProduct is the product that ran and the language whose renderings it
// answered.
type conformanceProduct struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Language string `json:"language"`
}

// conformanceTotals is the fixture counts the printed result names, so a reader
// compares them without counting rows.
type conformanceTotals struct {
	Fixtures int `json:"fixtures"`
	Pass     int `json:"pass"`
	Gap      int `json:"gap"`
	Fail     int `json:"fail"`
}

// fixtureAnswer is one fixture's row: its result, one row per expectation, and every
// finding at a position no expectation resolves to.
type fixtureAnswer struct {
	Fixture      string              `json:"fixture"`
	Result       string              `json:"result"`
	Expectations []expectationAnswer `json:"expectations"`
	Unexpected   []unexpectedFinding `json:"unexpected"`
	Message      string              `json:"message,omitempty"`
}

// expectationAnswer is one expectation's row: what the analyzer reported at the
// resolved position, how the suppression phase went, and why the row is not a pass.
type expectationAnswer struct {
	Symbol      string             `json:"symbol"`
	Result      string             `json:"result"`
	Capability  string             `json:"capability,omitempty"`
	Actual      reportedAnswer     `json:"actual"`
	Suppression *suppressionAnswer `json:"suppression,omitempty"`
	Message     string             `json:"message,omitempty"`
}

// reportedAnswer is what the analyzer reported at one resolved position, in the
// expectation file's own vocabulary so the two are compared member by member. A
// member the expectation does not name is not written, because a runner answering a
// row that states nothing about a member says nothing about it either.
type reportedAnswer struct {
	Report            string         `json:"report"`
	SymbolKind        string         `json:"symbol_kind,omitempty"`
	Confidence        string         `json:"confidence,omitempty"`
	ReachabilityClass string         `json:"reachability_class,omitempty"`
	LivenessRelation  string         `json:"liveness_relation,omitempty"`
	Configurations    []string       `json:"configurations,omitempty"`
	RetainedBy        []string       `json:"retained_by,omitempty"`
	Details           *corpusDetails `json:"details,omitempty"`
}

// suppressionAnswer is the outcome of the second analysis: whether the position is
// silent and whether the entry the runner wrote was reported stale.
type suppressionAnswer struct {
	Suppressed bool `json:"suppressed"`
	Stale      bool `json:"stale"`
}

// unexpectedFinding is one finding the first analysis reported at a position no
// expectation of the fixture resolves to, named in the manifest's path form.
type unexpectedFinding struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Report string `json:"report"`
}

// ignoreDocument and ignoreEntry are the ignore document the suppression phase
// writes, in the form the suppression grammar publishes.
type ignoreDocument struct {
	Description string        `json:"description"`
	Ignore      []ignoreEntry `json:"ignore"`
}

type ignoreEntry struct {
	Code   string `json:"code"`
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// site is one position as an expectation and a finding are compared at: the file the
// subject is declared in, relative to the target root, and the line. The column is
// not part of it, because a rendering's manifest binds a logical name to a line.
type site struct {
	File string
	Line int
}

// answered is what one analysis of one rendering reported: every finding, and the
// exemption classes the retained-symbol listing names at each position.
type answered struct {
	findings []kinds.Finding
	stale    []kinds.Finding
	retained map[site][]string
}

// TestConformanceCorpus is this analyzer's run of the Conformance Corpus: for every
// fixture of the pinned corpus carrying a Go rendering it extracts the rendering,
// analyzes it as it would a real repository, compares every expectation to what it
// reported, writes each reported finding into the rendering's ignore document and
// analyzes again, and records the whole run in the committed results document.
//
// The run goes through this package's own functions rather than through the built
// binary. Both are the same pipeline: the binary's analyze verb parses an invocation,
// resolves the configuration documents and calls the same pass this test calls, so
// what the two answer cannot differ. What differs is the cost, four modules analyzed
// twice each, and one thing the binary cannot do at all: a report names its target
// relative to the directory the run was invoked from and refuses a path outside it, so
// the binary would have to be run with a scratch directory as its working directory,
// which no test of this package may set while its siblings run. The binary's own
// conformance block is pinned by a test of its own below, over the documents this one
// writes.
func TestConformanceCorpus(t *testing.T) {
	published := publishedCorpusVersion(t)
	shapes := publishedSubjectShapes(t)
	declared := committedGaps(t)
	names := goFixtures(t)

	results := conformanceResults{
		CorpusVersion: published,
		Product:       conformanceProduct{Name: name, Version: version(), Language: corpusLanguage},
		Fixtures:      make([]fixtureAnswer, 0, len(names)),
	}
	for _, one := range names {
		var row fixtureAnswer
		t.Run(one, func(t *testing.T) {
			row = answerFixture(t, one, declared.Gaps, &shapes)
		})
		results.Fixtures = append(results.Fixtures, row)
	}

	results.Totals = conformanceTotals{Fixtures: len(results.Fixtures)}
	for _, row := range results.Fixtures {
		switch row.Result {
		case answerPass:
			results.Totals.Pass++
		case answerGap:
			results.Totals.Gap++
		default:
			results.Totals.Fail++
		}
	}
	results.Result = answerPass
	if results.Totals.Fail > 0 {
		results.Result = answerFail
	}

	for _, line := range printedResult(&results) {
		t.Log(line)
	}
	checkResultsGolden(t, encodeResults(t, &results))
}

// printedResult is the run's result as a reader reads it: one line naming the corpus,
// the fixture count and the three outcomes, then one line per gap and per failure
// naming the fixture, the logical symbol and what differed.
func printedResult(results *conformanceResults) []string {
	lines := []string{fmt.Sprintf("%s: corpus %s   fixtures %d  pass %d  gap %d  fail %d",
		results.Product.Name, results.CorpusVersion,
		results.Totals.Fixtures, results.Totals.Pass, results.Totals.Gap, results.Totals.Fail)}
	for i := range results.Fixtures {
		row := &results.Fixtures[i]
		if row.Message != "" {
			lines = append(lines, fmt.Sprintf("  %-4s %-34s %s", row.Result, row.Fixture, row.Message))
		}
		for j := range row.Expectations {
			held := &row.Expectations[j]
			if held.Result == answerPass {
				continue
			}
			lines = append(lines, fmt.Sprintf("  %-4s %-34s %-18s %s",
				held.Result, row.Fixture, held.Symbol, held.Message))
		}
		for _, one := range row.Unexpected {
			lines = append(lines, fmt.Sprintf("  fail %-34s %s:%d reported %s at a position no expectation resolves to",
				row.Fixture, one.File, one.Line, one.Report))
		}
	}
	return lines
}

// answerFixture runs one fixture's two phases and returns the row the results
// document carries for it.
//
// Every problem it meets is a row of the document as well as a test failure, because
// the document is the record the corpus repository's agreement check reads and a
// fixture missing from it is a silence rather than an answer.
func answerFixture(t *testing.T, fixtureName string, declared []conformanceGap,
	shapes *corpusSubjectShapes,
) fixtureAnswer {
	t.Helper()

	fixture, manifest, archive, err := readFixture(fixtureName)
	if err != nil {
		return failedFixture(t, fixtureName, err.Error())
	}

	// The results document orders a fixture's rows by logical name, so the
	// expectations are put in that order once and every row below follows it.
	slices.SortFunc(fixture.Expect, func(a, b corpusExpectation) int { return strings.Compare(a.Symbol, b.Symbol) })

	rendering := filepath.Join(t.TempDir(), "rendering")
	if err := extractRendering(rendering, archive); err != nil {
		return failedFixture(t, fixtureName, err.Error())
	}
	names := renderedNames(archive)
	before, err := renderedBytes(rendering, names)
	if err != nil {
		return failedFixture(t, fixtureName, err.Error())
	}
	sites, err := resolvedSites(&fixture, &manifest)
	if err != nil {
		return failedFixture(t, fixtureName, err.Error())
	}
	document, err := writeScope(filepath.Dir(rendering), rendering, fixture.Consumers)
	if err != nil {
		return failedFixture(t, fixtureName, err.Error())
	}
	run, err := corpusRunOf(rendering, document, &fixture, &manifest)
	if err != nil {
		return failedFixture(t, fixtureName, err.Error())
	}

	first, err := analyzeRendering(t.Context(), &run)
	if err != nil {
		return failedFixture(t, fixtureName, "the first analysis did not complete: "+err.Error())
	}

	row := fixtureAnswer{
		Fixture:      fixtureName,
		Expectations: make([]expectationAnswer, len(fixture.Expect)),
		Unexpected:   unexpectedFindings(&first, sites, fixture.ConfiguredRoots),
	}
	for i := range fixture.Expect {
		held := &fixture.Expect[i]
		actual, message := answerOf(held, sites[held.Symbol], &first, fixture.ConfiguredRoots[held.Symbol])
		row.Expectations[i] = expectationAnswer{Symbol: held.Symbol, Actual: actual, Message: message}
	}

	written, err := answerSuppression(t.Context(), &run, &fixture, sites, &first,
		row.Expectations, shapes)
	if err != nil {
		return failedFixture(t, fixtureName, "the second analysis did not complete: "+err.Error())
	}
	// The phase writes the ignore document itself, so the rendering is held to the
	// bytes it wrote there and to the archive's own bytes everywhere else.
	maps.Copy(before, written)
	if err := checkRenderingUnchanged(rendering, names, before); err != nil {
		return failedFixture(t, fixtureName, err.Error())
	}

	for i := range row.Expectations {
		rule(&fixture.Expect[i], &row.Expectations[i], declared, fixtureName)
	}
	row.Result = fixtureResultOf(&row)

	for i := range row.Expectations {
		if row.Expectations[i].Result == answerFail {
			t.Errorf("the corpus fixture %s expects %s and this analyzer answers otherwise: %s",
				fixtureName, row.Expectations[i].Symbol, row.Expectations[i].Message)
		}
	}
	for _, one := range row.Unexpected {
		t.Errorf("the corpus fixture %s reports %s at %s:%d, which no expectation resolves to: the expectation list is exhaustive",
			fixtureName, one.Report, one.File, one.Line)
	}
	return row
}

// rule decides one expectation's result: a pass when what the analyzer answered is
// what the corpus expects and the suppression phase held, a gap when a committed
// declared gap covers a capability the expectation exercises, and a fail otherwise.
func rule(expected *corpusExpectation, held *expectationAnswer, declared []conformanceGap, fixtureName string) {
	if held.Message == "" && held.Suppression != nil && (!held.Suppression.Suppressed || held.Suppression.Stale) {
		held.Message = fmt.Sprintf("the report phase agrees and the suppression phase does not: suppressed is %t and stale is %t",
			held.Suppression.Suppressed, held.Suppression.Stale)
	}
	if held.Message == "" {
		held.Result = answerPass
		return
	}
	if capability := coveringGap(declared, fixtureName, expected); capability != "" {
		held.Result, held.Capability = answerGap, capability
		return
	}
	held.Result = answerFail
}

// coveringGap is the capability a committed declared gap covers for one expectation,
// and the empty string where none does. A gap naming no symbol covers every
// expectation of its fixture that exercises the capability.
func coveringGap(declared []conformanceGap, fixtureName string, expected *corpusExpectation) string {
	exercised := exercisedCapabilities(expected)
	for _, gap := range declared {
		if gap.Fixture != fixtureName {
			continue
		}
		if gap.Symbol != "" && gap.Symbol != expected.Symbol {
			continue
		}
		if slices.Contains(exercised, gap.Capability) {
			return gap.Capability
		}
	}
	return ""
}

// exercisedCapabilities is what one expectation exercises: its report code where that
// is not the literal none, and every exemption class it names otherwise. An
// expectation reporting nothing and naming no class exercises no capability and admits
// no gap.
func exercisedCapabilities(expected *corpusExpectation) []string {
	if expected.Report != reportNone {
		return []string{expected.Report}
	}
	return expected.RetainedBy
}

// fixtureResultOf is one fixture's result: a fail where any expectation failed or any
// finding sits at a position no expectation resolves to, a gap where any expectation
// is a gap, and a pass otherwise.
func fixtureResultOf(row *fixtureAnswer) string {
	if len(row.Unexpected) > 0 {
		return answerFail
	}
	held := answerPass
	for i := range row.Expectations {
		switch row.Expectations[i].Result {
		case answerFail:
			return answerFail
		case answerGap:
			held = answerGap
		}
	}
	return held
}

// failedFixture is the row a fixture the runner could not complete carries, and the
// test failure that goes with it.
func failedFixture(t *testing.T, fixtureName, message string) fixtureAnswer {
	t.Helper()
	t.Errorf("the corpus fixture %s did not complete: %s", fixtureName, message)
	return fixtureAnswer{
		Fixture:      fixtureName,
		Result:       answerFail,
		Expectations: []expectationAnswer{},
		Unexpected:   []unexpectedFinding{},
		Message:      message,
	}
}

// answerOf is what the analyzer reported at one expectation's resolved position, and
// one line naming what differs from the expectation where anything does.
//
// Two expectations are answered from somewhere other than the findings at a position.
// A row naming a configured root is answered by the subject a finding names, because
// every unmatched root of a run is positioned in the configuration document the run
// read its roots from rather than in a file of the rendering, and root is the
// configured entry that row names where it names one. A row naming the
// stale-suppression code is answered from the records the report carries beside its
// findings, because that is the array such a row is a record of.
func answerOf(expected *corpusExpectation, at site, held *answered, root string) (reportedAnswer, string) {
	if root != "" {
		return answerRoot(expected, root, held)
	}
	if expected.Report == staleSuppression {
		return answerStale(expected, at, held)
	}
	found := findingsAt(held.findings, at)
	if expected.Report == reportNone {
		if len(found) > 0 {
			return answerFrom(expected, &found[0]),
				fmt.Sprintf("want no finding at %s:%d, got %s", at.File, at.Line, found[0].Code)
		}
		classes := held.retained[at]
		actual := reportedAnswer{Report: reportNone, RetainedBy: classes}
		if len(expected.RetainedBy) > 0 && !slices.Equal(classes, sorted(expected.RetainedBy)) {
			return actual, fmt.Sprintf("want the retained listing to name %v at %s:%d, got %v",
				expected.RetainedBy, at.File, at.Line, classes)
		}
		return actual, ""
	}

	switch len(found) {
	case 0:
		return reportedAnswer{Report: reportNone},
			fmt.Sprintf("want %s at %s:%d, got no finding", expected.Report, at.File, at.Line)
	case 1:
		actual := answerFrom(expected, &found[0])
		return actual, differences(expected, &actual)
	default:
		codes := make([]string, len(found))
		for i := range found {
			codes[i] = found[i].Code
		}
		return answerFrom(expected, &found[0]),
			fmt.Sprintf("want exactly one finding at %s:%d under %s, got %v", at.File, at.Line, expected.Report, codes)
	}
}

// answerRoot is what the analyzer reported about one configured root: the finding
// under the unmatched-root code whose subject is the entry the runner configured,
// wherever the analyzer positioned it.
//
// The position answers nothing here. A configured root is written in a configuration
// document as a member of an array whose entries carry no line, so every unmatched root
// of a run renders at one position, and the entry the finding names is the only thing
// that tells two of them apart. A row whose report is none states that the entry
// matched a symbol, so the answer is that no finding names it.
func answerRoot(expected *corpusExpectation, entry string, held *answered) (reportedAnswer, string) {
	var found []kinds.Finding
	for i := range held.findings {
		if held.findings[i].Code == unmatchedRoot && held.findings[i].Symbol.Ref == entry {
			found = append(found, held.findings[i])
		}
	}

	if expected.Report == reportNone {
		if len(found) > 0 {
			return answerFrom(expected, &found[0]),
				fmt.Sprintf("want no %s naming %s, got one: the entry matches a symbol", unmatchedRoot, entry)
		}
		return reportedAnswer{Report: reportNone}, ""
	}
	switch len(found) {
	case 0:
		return reportedAnswer{Report: reportNone},
			fmt.Sprintf("want %s naming %s, got no finding under it", expected.Report, entry)
	case 1:
		actual := answerFrom(expected, &found[0])
		return actual, differences(expected, &actual)
	default:
		return answerFrom(expected, &found[0]),
			fmt.Sprintf("want exactly one %s naming %s, got %d", unmatchedRoot, entry, len(found))
	}
}

// answerStale is the record the report carries at one expectation's resolved position
// in the array it holds its stale suppressions in.
//
// The record's own position is the suppression's site, so the row resolves through the
// manifest like any other; what differs is the array, because a stale suppression is a
// record of the report rather than one of its findings. The row's confidence and
// subject kind are answered from the record this analyzer produced rather than from the
// fixed values a reader renders it with, so a record that claimed anything else would
// fail the corpus rather than pass it unread.
func answerStale(expected *corpusExpectation, at site, held *answered) (reportedAnswer, string) {
	records := findingsAt(held.stale, at)
	switch len(records) {
	case 0:
		return reportedAnswer{Report: reportNone},
			fmt.Sprintf("want a %s record at %s:%d, got none", staleSuppression, at.File, at.Line)
	case 1:
		actual := answerFrom(expected, &records[0])
		return actual, differences(expected, &actual)
	default:
		return answerFrom(expected, &records[0]),
			fmt.Sprintf("want exactly one %s record at %s:%d, got %d", staleSuppression, at.File, at.Line, len(records))
	}
}

// answerFrom is one finding in the expectation file's vocabulary, carrying the members
// the expectation names and no others.
func answerFrom(expected *corpusExpectation, found *kinds.Finding) reportedAnswer {
	actual := reportedAnswer{Report: found.Code, Confidence: string(found.Confidence)}
	if expected.SymbolKind != "" {
		actual.SymbolKind = found.Symbol.Kind
	}
	if expected.ReachabilityClass != "" {
		actual.ReachabilityClass = string(found.Class)
	}
	if expected.LivenessRelation != "" && !found.Live {
		actual.LivenessRelation = found.Relation.String()
	}
	if len(expected.Configurations) > 0 {
		actual.Configurations = slices.Clone(found.Configurations)
	}
	actual.Details = detailsOf(expected.Details, &found.Details)
	return actual
}

// detailsOf is the finding's details in the expectation file's vocabulary, carrying
// the members one expectation pins and no others, and nothing at all where the
// expectation pins none: a row pins what it names, so a member it omits is answered
// by nothing and compared by nothing.
//
// A member the row names and the finding does not carry answers as the empty value,
// which the comparison names as a mismatch rather than passing over. The
// cross-language edge is one such member always: the kind that reports an edge is the
// orchestrator's merge rather than any analyzer's pass, so a finding of this analyzer
// carries no edge and a row naming one cannot be answered.
func detailsOf(expected *corpusDetails, found *kinds.Details) *corpusDetails {
	if expected == nil {
		return nil
	}
	var actual corpusDetails
	if expected.NarrowerVisibility != "" {
		actual.NarrowerVisibility = found.NarrowerVisibility
	}
	if expected.ExcludedBy != "" {
		actual.ExcludedBy = found.ExcludedBy
	}
	if expected.DependencyClass != "" {
		actual.DependencyClass = found.DependencyClass
	}
	if expected.Replacement != "" {
		actual.Replacement = found.Replacement
	}
	if expected.Mechanism != "" {
		actual.Mechanism = found.Mechanism
	}
	if len(expected.Overlap) > 0 {
		actual.Overlap = slices.Clone(found.Overlap)
	}
	if actual.empty() {
		return nil
	}
	return &actual
}

// empty reports whether these details name no member, which a runner omits rather
// than writes: the results document admits details naming at least one member and a
// row whose every pinned member the finding leaves empty is a mismatch the message
// names.
func (d *corpusDetails) empty() bool {
	return d.NarrowerVisibility == "" && d.ExcludedBy == "" && d.DependencyClass == "" &&
		d.Replacement == "" && d.Mechanism == "" && d.Edge == "" && len(d.Overlap) == 0
}

// differences is one line naming every member the expectation states and the answer
// does not match, and the empty string where the two agree.
func differences(expected *corpusExpectation, actual *reportedAnswer) string {
	var held []string
	for _, one := range []struct{ member, want, got string }{
		{"report", expected.Report, actual.Report},
		{"confidence", expected.Confidence, actual.Confidence},
		{"symbol_kind", expected.SymbolKind, actual.SymbolKind},
		{"reachability_class", expected.ReachabilityClass, actual.ReachabilityClass},
		{"liveness_relation", expected.LivenessRelation, actual.LivenessRelation},
	} {
		if one.want != "" && one.want != one.got {
			held = append(held, fmt.Sprintf("%s want %q got %q", one.member, one.want, one.got))
		}
	}
	if len(expected.Configurations) > 0 && !slices.Equal(expected.Configurations, actual.Configurations) {
		held = append(held, fmt.Sprintf("configurations want %v got %v", expected.Configurations, actual.Configurations))
	}
	if expected.Details != nil {
		held = append(held, detailsDifferences(expected.Details, actual.Details)...)
	}
	return strings.Join(held, "; ")
}

// detailsDifferences is one entry per details member the expectation pins that the
// answer does not carry equal, which is what makes the details the thing that
// distinguishes two findings of one code rather than a decoration.
func detailsDifferences(expected, actual *corpusDetails) []string {
	if actual == nil {
		actual = &corpusDetails{}
	}
	var held []string
	for _, one := range []struct{ member, want, got string }{
		{"details.narrower_visibility", expected.NarrowerVisibility, actual.NarrowerVisibility},
		{"details.excluded_by", expected.ExcludedBy, actual.ExcludedBy},
		{"details.dependency_class", expected.DependencyClass, actual.DependencyClass},
		{"details.replacement", expected.Replacement, actual.Replacement},
		{"details.mechanism", expected.Mechanism, actual.Mechanism},
		{"details.edge", expected.Edge, actual.Edge},
	} {
		if one.want != "" && one.want != one.got {
			held = append(held, fmt.Sprintf("%s want %q got %q", one.member, one.want, one.got))
		}
	}
	if len(expected.Overlap) > 0 && !slices.Equal(expected.Overlap, actual.Overlap) {
		held = append(held, fmt.Sprintf("details.overlap want %v got %v", expected.Overlap, actual.Overlap))
	}
	return held
}

// findingsAt is every finding the analysis reported at one position, in the order the
// report lists them.
func findingsAt(findings []kinds.Finding, at site) []kinds.Finding {
	var held []kinds.Finding
	for i := range findings {
		if findings[i].Position.Path == at.File && findings[i].Position.Line == at.Line {
			held = append(held, findings[i])
		}
	}
	return held
}

// unexpectedFindings is everything the analyzer reported that no expectation of the
// fixture accounts for, ordered by file, then line, then code. The expectation list is
// exhaustive for the target, so each one fails the fixture.
//
// The three arms the fixture is answered from are as exhaustive as one another. A
// finding is accounted for by an expectation resolving to its position; a record of the
// report's stale-suppression array counts as a finding at its own position, which is
// the suppression's site; and a finding whose subject is a configured root counts by
// the entry its subject names, because every such finding of a run shares one position.
func unexpectedFindings(held *answered, sites map[string]site,
	roots map[string]string,
) []unexpectedFinding {
	expected := make(map[site]bool, len(sites))
	for _, at := range sites {
		expected[at] = true
	}
	configured := make(map[string]bool, len(roots))
	for _, entry := range roots {
		configured[entry] = true
	}

	reported := slices.Concat(held.findings, held.stale)
	unaccounted := make([]unexpectedFinding, 0, len(reported))
	for i := range reported {
		found := &reported[i]
		if found.Code == unmatchedRoot {
			if configured[found.Symbol.Ref] {
				continue
			}
			unaccounted = append(unaccounted, unexpectedFinding{
				File:   path.Join(targetSection, found.Position.Path),
				Line:   found.Position.Line,
				Report: found.Code,
			})
			continue
		}
		at := site{File: found.Position.Path, Line: found.Position.Line}
		if expected[at] {
			continue
		}
		unaccounted = append(unaccounted, unexpectedFinding{
			File:   path.Join(targetSection, at.File),
			Line:   at.Line,
			Report: found.Code,
		})
	}
	slices.SortFunc(unaccounted, func(a, b unexpectedFinding) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Report, b.Report)
	})
	return unaccounted
}

// answerSuppression runs the second phase: every finding the first analysis reported
// at an expectation's position that a suppression record can bind to is written into
// the rendering's ignore document, the rendering is analyzed again, and each of those
// expectations records whether its position fell silent and whether the entry was
// reported stale.
//
// The entries are written together and the rendering analyzed once, because the phase
// asks what the documented form does to the findings it names rather than what one
// entry does in isolation.
//
// Which findings a record can bind to is the corpus's shape table, read from the
// pinned corpus rather than listed here, so an amendment naming a further kind of
// document row moves this phase without an edit. An expectation the phase skips
// records no suppression answer, which is what the results document then says about
// it. An expectation naming a configured root is skipped before that table is read,
// because it resolves to no position of the rendering at all and a record binds to a
// declaration.
//
// The entries are added to the rendering's own ignore document where it carries one,
// because a fixture about suppression writes its own records and its expectations are
// written against them. The phase is the one writer of that file, so it returns what
// it wrote and the caller holds the rendering to those bytes: what the two analyses
// must leave alone is every file of the rendering, this one at the bytes the phase
// gave it.
func answerSuppression(ctx context.Context, run *corpusRun, fixture *corpusFixtureFile,
	sites map[string]site, first *answered, rows []expectationAnswer, shapes *corpusSubjectShapes,
) (map[string]string, error) {
	entries := make([]ignoreEntry, 0, len(fixture.Expect))
	covered := make(map[string]ignoreEntry, len(fixture.Expect))
	for i := range fixture.Expect {
		held := &fixture.Expect[i]
		if _, configured := fixture.ConfiguredRoots[held.Symbol]; held.Report == reportNone || configured {
			continue
		}
		found := findingsAt(first.findings, sites[held.Symbol])
		if len(found) == 0 || !shapes.suppressible(found[0].Symbol.Kind) {
			continue
		}
		entry := ignoreEntry{
			Code:   found[0].Code,
			Symbol: found[0].Symbol.Ref,
			Path:   found[0].Position.Path,
			Reason: "Written by the conformance run to check that the finding it names is suppressed.",
		}
		entries = append(entries, entry)
		covered[held.Symbol] = entry
	}
	if len(entries) == 0 {
		return nil, nil
	}

	rendered := path.Join(targetSection, suppress.IgnoreFileName)
	held := filepath.Join(run.rendering, targetSection, suppress.IgnoreFileName)
	document, err := ignoreDocumentAt(held)
	if err != nil {
		return nil, err
	}
	document.Ignore = append(document.Ignore, entries...)
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the ignore document: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(held, body, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", held, err)
	}
	digest := sha256.Sum256(body)
	written := map[string]string{rendered: hex.EncodeToString(digest[:])}

	second, err := analyzeRendering(ctx, run)
	if err != nil {
		return written, err
	}
	for i := range rows {
		entry, held := covered[rows[i].Symbol]
		if !held {
			continue
		}
		rows[i].Suppression = &suppressionAnswer{
			Suppressed: len(findingsUnder(second.findings, sites[rows[i].Symbol], entry.Code)) == 0,
			Stale:      staleFor(second.stale, entry),
		}
	}
	return written, nil
}

// ignoreDocumentAt is the ignore document one rendering carries, and an empty one
// under the phase's own description where the rendering carries none.
func ignoreDocumentAt(held string) (ignoreDocument, error) {
	body, err := os.ReadFile(held)
	if errors.Is(err, os.ErrNotExist) {
		return ignoreDocument{
			Description: "The adjudications the conformance run writes for its suppression phase.",
		}, nil
	}
	if err != nil {
		return ignoreDocument{}, fmt.Errorf("read %s: %w", held, err)
	}
	var document ignoreDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return ignoreDocument{}, fmt.Errorf("decode %s: %w", held, err)
	}
	return document, nil
}

// findingsUnder is every finding at one position under one code.
func findingsUnder(findings []kinds.Finding, at site, code string) []kinds.Finding {
	var held []kinds.Finding
	for _, found := range findingsAt(findings, at) {
		if found.Code == code {
			held = append(held, found)
		}
	}
	return held
}

// staleFor reports whether the run held a stale-suppression record for one entry,
// read from the array the report carries those records in.
func staleFor(records []kinds.Finding, entry ignoreEntry) bool {
	for i := range records {
		found := &records[i]
		if found.Details.Entry == nil {
			continue
		}
		if found.Details.Entry.Code == entry.Code && found.Details.Entry.Symbol == entry.Symbol {
			return true
		}
	}
	return false
}

// corpusRun is how the runner invokes one fixture's analysis: the extracted
// rendering, the scope document its declared consumers need, and the configuration
// the corpus states the run is configured with.
//
// It is resolved once per fixture and both phases analyze under it, so the two
// analyses are one run configuration by construction rather than by two calls that
// happen to agree.
type corpusRun struct {
	rendering string
	scope     string
	config    config.Config
}

// corpusRunOf is the run one fixture is answered under, as the corpus states a run is
// configured: the expectation file's target kind, the consumers the scope document
// names, the world its closed_world member declares, and every other setting at its
// default, so a rendering carrying build constraints and declaring nothing is analyzed
// under the matrix the analyzer derives from it.
//
// A fixture that declares the consumer set complete is analyzed under a closed
// published API; one that declares the matrix complete is analyzed over the
// configurations its manifest names, because a matrix the analyzer derived is never a
// complete one and a run configured with the declaration alone would leave the kinds
// that need it reporting nothing. The member names no other fact and a run sets no
// other key from it.
func corpusRunOf(rendering, document string, fixture *corpusFixtureFile,
	manifest *corpusManifest,
) (corpusRun, error) {
	held := corpusRun{rendering: rendering, scope: document, config: config.Default()}
	switch config.TargetKind(fixture.TargetKind) {
	case config.Application, config.Library:
		held.config.Target.Kind = config.TargetKind(fixture.TargetKind)
	default:
		return corpusRun{}, fmt.Errorf("the expectation file names the target kind %q", fixture.TargetKind)
	}

	// The configured roots the fixture names, in the order of the logical names that
	// name them, so two runs over one fixture configure one list.
	for _, logical := range slices.Sorted(maps.Keys(fixture.ConfiguredRoots)) {
		held.config.Roots.Patterns = append(held.config.Roots.Patterns, fixture.ConfiguredRoots[logical])
	}

	for _, fact := range fixture.ClosedWorld {
		switch fact {
		case worldConsumers:
			held.config.Consumers.Complete = true
		case worldMatrix:
			declared, err := declaredMatrix(manifest.Configurations)
			if err != nil {
				return corpusRun{}, err
			}
			held.config.Analysis.Matrix.Complete = true
			held.config.Analysis.Configurations = declared
		default:
			return corpusRun{}, fmt.Errorf("the expectation file declares the closed-world fact %q, "+
				"and the corpus draws that member from %q and %q", fact, worldConsumers, worldMatrix)
		}
	}
	return held, nil
}

// declaredMatrix is the build matrix a rendering's manifest declares, one
// configuration per identifier it names, spelled as an identifier spells it: the
// operating system, the architecture and any tags joined by hyphens.
func declaredMatrix(identifiers []string) ([]config.Configuration, error) {
	declared := make([]config.Configuration, 0, len(identifiers))
	for _, one := range identifiers {
		parts := strings.Split(one, "-")
		if len(parts) < 2 {
			return nil, fmt.Errorf("the manifest declares the configuration %q, "+
				"and an identifier names an operating system and an architecture", one)
		}
		declared = append(declared, config.Configuration{
			ID: one, OS: parts[0], Arch: parts[1], Tags: parts[2:],
		})
	}
	return declared, nil
}

// analyzeRendering analyzes one extracted rendering under the run the corpus states,
// and holds what it answered in the two arrays a report carries.
//
// A stale suppression is answered by the findings pass under its own code and is a
// record of the report's own array rather than one of its findings, which is the move
// the report assembly makes; the corpus answers a row naming that code from that
// array, so the run splits the pass the same way here. The rule is the code and
// nothing else, which is why the split is one line rather than a second reading of the
// assembly.
func analyzeRendering(ctx context.Context, run *corpusRun) (answered, error) {
	resolved := resolution{
		target: filepath.Join(run.rendering, targetSection),
		scope:  run.scope,
		config: run.config,
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		return answered{}, err
	}
	set, err := findingsOf(ctx, &resolved, &options)
	if err != nil {
		return answered{}, err
	}
	held := answered{retained: retainedAt(&set)}
	for _, found := range set.result.Findings {
		if found.Code == staleSuppression {
			held.stale = append(held.stale, found)
			continue
		}
		held.findings = append(held.findings, found)
	}
	return held, nil
}

// retainedAt is the exemption classes the retained-symbol listing names at each
// position, which is what an expectation reporting nothing and naming classes is
// answered from.
func retainedAt(set *findingSet) map[site][]string {
	positions := make(map[graph.SymbolID]token.Position, len(set.loaded.merged.Symbols))
	for i := range set.loaded.merged.Symbols {
		positions[set.loaded.merged.Symbols[i].ID] = set.loaded.merged.Symbols[i].Pos
	}
	held := make(map[site][]string)
	for _, one := range set.swept.Retained {
		at, known := positions[one.ID]
		if !known {
			continue
		}
		key := site{File: at.Filename, Line: at.Line}
		if !slices.Contains(held[key], one.Class) {
			held[key] = append(held[key], one.Class)
		}
	}
	for key := range held {
		slices.Sort(held[key])
	}
	return held
}

// readFixture reads one fixture of the pinned corpus: its expectation file, the
// manifest of its Go rendering, and the rendering itself.
func readFixture(fixtureName string) (corpusFixtureFile, corpusManifest, *txtar.Archive, error) {
	var fixture corpusFixtureFile
	body, err := spec.Corpus.ReadFile(path.Join(corpusFixtures, fixtureName, expectationFile))
	if err != nil {
		return fixture, corpusManifest{}, nil, err
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		return fixture, corpusManifest{}, nil, fmt.Errorf("decode %s: %w", expectationFile, err)
	}

	body, err = spec.Corpus.ReadFile(path.Join(corpusFixtures, fixtureName, goRendering))
	if err != nil {
		return fixture, corpusManifest{}, nil, fmt.Errorf("the expectation file lists %s and the fixture has no %s: "+
			"a fixture listing a language and holding no rendering for it fails the corpus", corpusLanguage, goRendering)
	}
	archive := txtar.Parse(body)

	for _, file := range archive.Files {
		if file.Name != manifestSection {
			continue
		}
		var manifest corpusManifest
		if err := json.Unmarshal(file.Data, &manifest); err != nil {
			return fixture, manifest, nil, fmt.Errorf("decode %s: %w", manifestSection, err)
		}
		return fixture, manifest, archive, nil
	}
	return fixture, corpusManifest{}, nil, fmt.Errorf("the rendering carries no %s section", manifestSection)
}

// resolvedSites binds every logical name the expectation file uses to the position the
// rendering declares it at, relative to the target root, which is what a finding's own
// position is relative to.
//
// A name the expectation file's configured-roots member declares is resolved through
// that member instead: it names a row of the configuration the runner writes rather
// than a declaration of the target, so no manifest carries it and it has no position
// to resolve to. A name both documents carry is a defect in the corpus, because the
// two readings answer the expectation differently and nothing says which one holds.
func resolvedSites(fixture *corpusFixtureFile, manifest *corpusManifest) (map[string]site, error) {
	sites := make(map[string]site, len(fixture.Expect))
	for i := range fixture.Expect {
		logical := fixture.Expect[i].Symbol
		held, known := manifest.Symbols[logical]
		if _, configured := fixture.ConfiguredRoots[logical]; configured {
			if known {
				return nil, fmt.Errorf("%s is named by both the configured roots and the manifest, which is a defect in the corpus",
					logical)
			}
			continue
		}
		if !known || held.File == "" || held.Line == 0 {
			return nil, fmt.Errorf("the manifest carries no file and line for %s, which is a defect in the corpus", logical)
		}
		within, inside := strings.CutPrefix(held.File, targetSection+"/")
		if !inside {
			return nil, fmt.Errorf("the manifest declares %s in %s, and the run analyzes %s/ alone",
				logical, held.File, targetSection)
		}
		sites[logical] = site{File: within, Line: held.Line}
	}
	return sites, nil
}

// extractRendering writes one rendering into dir, which is the scratch copy the run
// analyzes.
func extractRendering(dir string, archive *txtar.Archive) error {
	for _, file := range archive.Files {
		held := filepath.Join(dir, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(held), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(held), err)
		}
		if err := os.WriteFile(held, file.Data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", held, err)
		}
	}
	return nil
}

// renderedNames is every file the rendering publishes, in the order the archive holds
// them.
func renderedNames(archive *txtar.Archive) []string {
	names := make([]string, len(archive.Files))
	for i := range archive.Files {
		names[i] = archive.Files[i].Name
	}
	return names
}

// renderedBytes is the digest of every named file of the rendering, which is what the
// report-only guarantee is checked against.
func renderedBytes(dir string, names []string) (map[string]string, error) {
	held := make(map[string]string, len(names))
	for _, one := range names {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(one)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", one, err)
		}
		digest := sha256.Sum256(body)
		held[one] = hex.EncodeToString(digest[:])
	}
	return held, nil
}

// checkRenderingUnchanged refuses a run that edited a file of the rendering. The
// ignore document the suppression phase writes is the runner's own and is not one of
// them, and no analysis writes anything at all.
func checkRenderingUnchanged(dir string, names []string, before map[string]string) error {
	after, err := renderedBytes(dir, names)
	if err != nil {
		return err
	}
	for _, one := range names {
		if after[one] != before[one] {
			return fmt.Errorf("the run changed %s, and analysis edits no file of the target", one)
		}
	}
	return nil
}

// writeScope writes the scope document one fixture's run reads, and returns the empty
// string for a fixture that declares no consumer, which is the target analyzed alone.
//
// The document is written outside the rendering and names absolute paths, so the
// rendering holds no file the corpus did not publish and the report-only check reads a
// tree the runner added nothing to.
func writeScope(dir, rendering string, consumers []string) (string, error) {
	if len(consumers) == 0 {
		return "", nil
	}
	document := struct {
		Target    map[string]string   `json:"target"`
		Consumers []map[string]string `json:"consumers"`
	}{
		Target:    map[string]string{"path": filepath.Join(rendering, targetSection), "role": "target"},
		Consumers: make([]map[string]string, 0, len(consumers)),
	}
	for _, one := range consumers {
		document.Consumers = append(document.Consumers,
			map[string]string{"path": filepath.Join(rendering, one), "role": "consumer"})
	}
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render the scope document: %w", err)
	}
	held := filepath.Join(dir, "scope.json")
	if err := os.WriteFile(held, append(body, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", held, err)
	}
	return held, nil
}

// goFixtures is every fixture of the pinned corpus whose expectation file lists this
// analyzer's language, in the order the corpus holds them, which is by name.
func goFixtures(t *testing.T) []string {
	t.Helper()

	entries, err := spec.Corpus.ReadDir(corpusFixtures)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", corpusFixtures, err)
	}
	var held []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		body, err := spec.Corpus.ReadFile(path.Join(corpusFixtures, entry.Name(), expectationFile))
		if err != nil {
			t.Fatalf("Setup: read the expectation file of %s: %v", entry.Name(), err)
		}
		var fixture corpusFixtureFile
		if err := json.Unmarshal(body, &fixture); err != nil {
			t.Fatalf("Setup: decode the expectation file of %s: %v", entry.Name(), err)
		}
		if slices.Contains(fixture.Languages, corpusLanguage) {
			held = append(held, entry.Name())
		}
	}
	if len(held) == 0 {
		t.Fatalf("Setup: the pinned corpus holds no fixture listing %s", corpusLanguage)
	}
	slices.Sort(held)
	return held
}

// publishedCorpusVersion is the corpus version the pinned Contract publishes, which
// is the version a run answers and the version the committed documents name.
func publishedCorpusVersion(t *testing.T) string {
	t.Helper()

	body, err := spec.Corpus.ReadFile(corpusDocument)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", corpusDocument, err)
	}
	var document struct {
		CorpusVersion string `json:"corpus_version"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", corpusDocument, err)
	}
	if document.CorpusVersion == "" {
		t.Fatalf("Setup: %s names no corpus version", corpusDocument)
	}
	return document.CorpusVersion
}

// publishedSubjectShapes is the shape table the pinned Contract's corpus publishes,
// which is what decides the expectations the suppression phase applies to.
func publishedSubjectShapes(t *testing.T) corpusSubjectShapes {
	t.Helper()

	body, err := spec.Corpus.ReadFile(corpusDocument)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", corpusDocument, err)
	}
	var document struct {
		SubjectShapes corpusSubjectShapes `json:"subject_shapes"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", corpusDocument, err)
	}
	if len(document.SubjectShapes.Part) == 0 || len(document.SubjectShapes.Row) == 0 {
		t.Fatalf("Setup: %s names %d part kinds and %d row kinds, want a shape table with both",
			corpusDocument, len(document.SubjectShapes.Part), len(document.SubjectShapes.Row))
	}
	return document.SubjectShapes
}

// committedGaps is the declared-gap document this analyzer commits, decoded from the
// file on disk rather than from the embedded copy, so the document a reader opens is
// the document the run reads.
func committedGaps(t *testing.T) gapsDocument {
	t.Helper()

	var document gapsDocument
	if err := json.Unmarshal(readCommitted(t, conformanceGapsFile), &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", conformanceGapsFile, err)
	}
	return document
}

// readCommitted reads one of the two committed conformance documents from the working
// tree. A test binary runs in its own package's directory, so the documents sit two
// levels above this one.
func readCommitted(t *testing.T, held string) []byte {
	t.Helper()

	body, err := os.ReadFile(committedPath(held))
	if err != nil {
		t.Fatalf("Setup: read %s: %v", held, err)
	}
	return body
}

// committedPath is one committed conformance document as this package's tests reach it.
func committedPath(held string) string {
	return filepath.Join("..", "..", held)
}

// encodeResults renders the results document the way the digest is taken over it: the
// members in the order the schema declares them, two spaces of indentation and one
// closing newline, so two runs over an unchanged analyzer and corpus write the same
// bytes.
func encodeResults(t *testing.T, results *conformanceResults) []byte {
	t.Helper()

	body, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("render the conformance results: %v", err)
	}
	return append(body, '\n')
}

// checkResultsGolden compares the results document the run produced against the
// committed copy, and rewrites the committed copy instead where the gate is open.
//
// A committed record that no longer matches the run is a red test rather than a
// silence, because the digest of that record is what every report this analyzer writes
// states about itself.
func checkResultsGolden(t *testing.T, got []byte) {
	t.Helper()

	if os.Getenv(goldenVariable) == "1" {
		if err := os.WriteFile(committedPath(conformanceResultsFile), got, documentMode); err != nil {
			t.Fatalf("write %s: %v", conformanceResultsFile, err)
		}
		return
	}
	want := readCommitted(t, conformanceResultsFile)
	if string(got) != string(want) {
		t.Errorf("the conformance results of this run differ from the committed %s (regenerate with %s):\n%s",
			conformanceResultsFile, goldenCommand, diffLines(string(want), string(got)))
	}
}

// diffLines is a line-by-line report of the first differences between two documents,
// for a failure that has to say what moved.
func diffLines(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	var held strings.Builder
	for i := range max(len(wantLines), len(gotLines)) {
		at, to := "", ""
		if i < len(wantLines) {
			at = wantLines[i]
		}
		if i < len(gotLines) {
			to = gotLines[i]
		}
		if at == to {
			continue
		}
		fmt.Fprintf(&held, "line %d:\n--- want %q\n+++ got  %q\n", i+1, at, to)
		if held.Len() > 2000 {
			held.WriteString("...\n")
			break
		}
	}
	return held.String()
}

// sorted is one class list in the order the runner answers with, so a comparison does
// not turn on the order a document happens to list them in.
func sorted(held []string) []string {
	clone := slices.Clone(held)
	slices.Sort(clone)
	return clone
}

// TestCorpusAnswerReadsTheCommittedConformanceDocuments pins the record every document
// this analyzer writes states about itself to the two documents committed beside this
// source: the corpus version the pinned Contract publishes, the result the run
// recorded, the digest of the results document's own bytes, and the declared gaps.
func TestCorpusAnswerReadsTheCommittedConformanceDocuments(t *testing.T) {
	t.Parallel()

	answered, err := corpusAnswer()
	if err != nil {
		t.Fatalf("corpusAnswer() = error %v, want the committed conformance record", err)
	}

	published := publishedCorpusVersion(t)
	if got := answered.conformance.CorpusVersion; got != published {
		t.Errorf("this analyzer answers corpus version %q, want %q as the pinned Contract's corpus publishes it",
			got, published)
	}

	results := readCommitted(t, conformanceResultsFile)
	var document struct {
		CorpusVersion string `json:"corpus_version"`
		Result        string `json:"result"`
	}
	if err := json.Unmarshal(results, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", conformanceResultsFile, err)
	}
	if got := answered.conformance.Result; got != document.Result {
		t.Errorf("this analyzer states the corpus result %q, want %q as %s records it",
			got, document.Result, conformanceResultsFile)
	}
	if got := answered.conformance.CorpusVersion; got != document.CorpusVersion {
		t.Errorf("this analyzer answers corpus version %q, want %q as %s records it",
			got, document.CorpusVersion, conformanceResultsFile)
	}

	digest := sha256.Sum256(results)
	if want := "sha256:" + hex.EncodeToString(digest[:]); answered.conformance.Digest != want {
		t.Errorf("this analyzer states the digest %q, want %q, the digest of the committed %s",
			answered.conformance.Digest, want, conformanceResultsFile)
	}

	declared := committedGaps(t)
	if got, want := len(answered.gaps), len(declared.Gaps); got != want {
		t.Fatalf("this analyzer declares %d gaps, want %d as %s declares them", got, want, conformanceGapsFile)
	}
	for i, gap := range declared.Gaps {
		held := answered.gaps[i]
		if held.Fixture != gap.Fixture || held.Symbol != gap.Symbol ||
			held.Capability != gap.Capability || held.Reason != gap.Reason {
			t.Errorf("this analyzer declares the gap %+v, want %+v as %s declares it", held, gap, conformanceGapsFile)
		}
	}
}

// TestTheDeclaredGapsNameTheProductAndTheCorpusTheyAnswer pins the committed
// declared-gap document to the analyzer and the corpus it is about: a gap declared
// against another corpus version says nothing about this one, and a gap naming a
// fixture or a capability the corpus does not hold covers nothing.
func TestTheDeclaredGapsNameTheProductAndTheCorpusTheyAnswer(t *testing.T) {
	t.Parallel()

	var document struct {
		CorpusVersion string `json:"corpus_version"`
		Product       struct {
			Name string `json:"name"`
		} `json:"product"`
		Gaps []conformanceGap `json:"gaps"`
	}
	if err := json.Unmarshal(readCommitted(t, conformanceGapsFile), &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", conformanceGapsFile, err)
	}
	if got, want := document.Product.Name, name; got != want {
		t.Errorf("%s names the product %q, want %q as a report names this analyzer", conformanceGapsFile, got, want)
	}
	if got, want := document.CorpusVersion, publishedCorpusVersion(t); got != want {
		t.Errorf("%s declares gaps against corpus %q, want %q as the pinned Contract's corpus publishes it",
			conformanceGapsFile, got, want)
	}

	for _, gap := range document.Gaps {
		fixture, _, _, err := readFixture(gap.Fixture)
		if err != nil {
			t.Errorf("%s declares a gap for the fixture %s, which the corpus does not hold: %v",
				conformanceGapsFile, gap.Fixture, err)
			continue
		}
		exercised := false
		for i := range fixture.Expect {
			held := &fixture.Expect[i]
			if gap.Symbol != "" && gap.Symbol != held.Symbol {
				continue
			}
			if slices.Contains(exercisedCapabilities(held), gap.Capability) {
				exercised = true
			}
		}
		if !exercised {
			t.Errorf("%s declares a gap on %s in %s, and no expectation it names exercises that capability: the gap covers nothing",
				conformanceGapsFile, gap.Capability, gap.Fixture)
		}
	}
}

// TestTheBinaryReportsTheConformanceRecordItCommitted is the end-to-end pin: the
// analyzer built the way a published build is built writes the committed conformance
// record into the report it produces, and states the digest of the committed results
// document rather than a value stated beside it.
//
// It runs the real process, because the block a reader and an orchestrator act on is
// the one in the document the binary wrote.
func TestTheBinaryReportsTheConformanceRecordItCommitted(t *testing.T) {
	digest := sha256.Sum256(readCommitted(t, conformanceResultsFile))
	var committed struct {
		CorpusVersion string `json:"corpus_version"`
		Result        string `json:"result"`
	}
	if err := json.Unmarshal(readCommitted(t, conformanceResultsFile), &committed); err != nil {
		t.Fatalf("Setup: decode %s: %v", conformanceResultsFile, err)
	}

	base := t.TempDir()
	dir := filepath.Join(base, "app")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", dir, err)
	}
	if err := writeFiles(dir, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"app.go":           "// Command app is the target of a conformance-block assertion.\npackage main\n\nfunc main() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}); err != nil {
		t.Fatalf("Setup: write the target module: %v", err)
	}

	binary := analyzerBinary(t)
	command := exec.CommandContext(t.Context(), binary, "analyze", "--target=./app", "--report=report.json")
	command.Dir = base
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("%s analyze over a clean module = %v, want the clean code\nstderr: %s", binary, err, stderr.String())
	}

	body, err := os.ReadFile(filepath.Join(base, "report.json"))
	if err != nil {
		t.Fatalf("read the report the run wrote: %v", err)
	}
	var document struct {
		Analyzer struct {
			Conformance conformanceBlock `json:"conformance"`
		} `json:"analyzer"`
		DeclaredGaps []conformanceGap `json:"declared_gaps"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the report the run wrote: %v", err)
	}

	want := conformanceBlock{
		CorpusVersion: committed.CorpusVersion,
		Result:        committed.Result,
		Digest:        "sha256:" + hex.EncodeToString(digest[:]),
	}
	if document.Analyzer.Conformance != want {
		t.Errorf("the report states the conformance block %+v, want %+v as the committed documents record it",
			document.Analyzer.Conformance, want)
	}
	if got, want := len(document.DeclaredGaps), len(committedGaps(t).Gaps); got != want {
		t.Errorf("the report names %d declared gaps, want %d as %s declares them", got, want, conformanceGapsFile)
	}
}

// TestAnswerFromRendersEveryMemberAnExpectationNames pins the vocabulary the runner
// answers in, member by member, over a finding carrying every one of them.
//
// The corpus decides which members its own fixtures name, and today no fixture with a
// Go rendering names the liveness relation on a row this analyzer reports, so the
// rendering of each member is pinned here rather than by whichever fixtures the pin
// happens to reach. A member the expectation does not name is not answered at all,
// which is the other half of the rule.
// TestTheCorpusRunDeclaresTheWorldTheFixtureNames pins what the runner configures
// from an expectation file's closed-world member, which is the one thing the corpus
// asks a runner to translate rather than to copy.
//
// It is a test of its own rather than a reading of the corpus run, because a fixture
// answers under its own declaration alone: a fixture that declares nothing cannot say
// what a declaration would have changed, and a fixture whose kind this analyzer
// declines is answered as a gap whatever the runner configured.
func TestTheCorpusRunDeclaresTheWorldTheFixtureNames(t *testing.T) {
	t.Parallel()

	manifest := corpusManifest{Configurations: []string{"linux-amd64", "linux-arm64-netgo"}}
	declared := []config.Configuration{
		{ID: "linux-amd64", OS: "linux", Arch: "amd64", Tags: []string{}},
		{ID: "linux-arm64-netgo", OS: "linux", Arch: "arm64", Tags: []string{"netgo"}},
	}

	tests := []struct {
		name           string
		world          []string
		consumers      bool
		matrix         bool
		configurations []config.Configuration
	}{
		{name: "an_open_world", world: nil, configurations: []config.Configuration{}},
		{
			name:           "a_declared_consumer_set",
			world:          []string{worldConsumers},
			consumers:      true,
			configurations: []config.Configuration{},
		},
		{
			name:           "a_declared_matrix",
			world:          []string{worldMatrix},
			matrix:         true,
			configurations: declared,
		},
		{
			name:           "both_facts",
			world:          []string{worldConsumers, worldMatrix},
			consumers:      true,
			matrix:         true,
			configurations: declared,
		},
	}

	for _, one := range tests {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			fixture := corpusFixtureFile{TargetKind: string(config.Library), ClosedWorld: one.world}
			run, err := corpusRunOf("rendering", "", &fixture, &manifest)
			if err != nil {
				t.Fatalf("corpusRunOf(a fixture declaring %v) = error %v, want the run it is answered under",
					one.world, err)
			}
			if got := run.config.Consumers.Complete; got != one.consumers {
				t.Errorf("corpusRunOf(a fixture declaring %v) set consumers.complete to %t, want %t",
					one.world, got, one.consumers)
			}
			if got := run.config.Analysis.Matrix.Complete; got != one.matrix {
				t.Errorf("corpusRunOf(a fixture declaring %v) set analysis.matrix.complete to %t, want %t",
					one.world, got, one.matrix)
			}
			if got := run.config.Analysis.Configurations; !slices.EqualFunc(got, one.configurations, sameConfiguration) {
				t.Errorf("corpusRunOf(a fixture declaring %v) set analysis.configurations to %v, want %v",
					one.world, got, one.configurations)
			}
		})
	}
}

// sameConfiguration reports whether two configurations of a declared matrix are the
// one configuration, every member included.
func sameConfiguration(a, b config.Configuration) bool {
	return a.ID == b.ID && a.OS == b.OS && a.Arch == b.Arch && slices.Equal(a.Tags, b.Tags)
}

// TestTheCorpusRunRefusesAWorldTheCorpusDoesNotCarry pins that a fact outside the
// closed set the corpus declares, and an identifier that names no architecture, are
// refused rather than configured as nothing: either is a corpus the runner cannot
// answer, and a run configured from a member it did not understand would report a
// kind's silence as this analyzer's answer.
func TestTheCorpusRunRefusesAWorldTheCorpusDoesNotCarry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fixture  corpusFixtureFile
		manifest corpusManifest
		want     string
	}{
		{
			name:    "a_fact_outside_the_closed_set",
			fixture: corpusFixtureFile{TargetKind: string(config.Library), ClosedWorld: []string{"platforms"}},
			want:    `"platforms"`,
		},
		{
			name:     "an_identifier_naming_no_architecture",
			fixture:  corpusFixtureFile{TargetKind: string(config.Library), ClosedWorld: []string{worldMatrix}},
			manifest: corpusManifest{Configurations: []string{"linux"}},
			want:     `"linux"`,
		},
	}

	for _, one := range tests {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			_, err := corpusRunOf("rendering", "", &one.fixture, &one.manifest)
			if err == nil {
				t.Fatalf("corpusRunOf(%s) = no error, want a refusal", one.name)
			}
			if !strings.Contains(err.Error(), one.want) {
				t.Errorf("corpusRunOf(%s) = error %q, want one naming %s", one.name, err, one.want)
			}
		})
	}
}

// TestIgnoreDocumentAtKeepsTheRenderingsOwnRecords pins what the suppression phase
// writes over: a fixture about suppression carries its own records and its
// expectations are written against them, so the phase adds its entries to that
// document rather than replacing it, and it writes a document of its own only where
// the rendering carries none.
func TestIgnoreDocumentAtKeepsTheRenderingsOwnRecords(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	held := filepath.Join(dir, suppress.IgnoreFileName)

	absent, err := ignoreDocumentAt(held)
	if err != nil {
		t.Fatalf("ignoreDocumentAt(a rendering carrying no ignore document) = error %v, want a document of the phase's own", err)
	}
	if absent.Description == "" || len(absent.Ignore) != 0 {
		t.Errorf("ignoreDocumentAt(no document) = %+v, want the phase's description and no entry", absent)
	}

	own := ignoreDocument{
		Description: "One entry the fixture writes itself.",
		Ignore: []ignoreEntry{{
			Code:   "DS1002",
			Symbol: "go://example.com/app#dropped",
			Path:   "main.go",
			Reason: "in effect: nothing reaches dropped",
		}},
	}
	body, err := json.MarshalIndent(own, "", "  ")
	if err != nil {
		t.Fatalf("Setup: render the fixture's own document: %v", err)
	}
	if err := os.WriteFile(held, append(body, '\n'), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", held, err)
	}

	read, err := ignoreDocumentAt(held)
	if err != nil {
		t.Fatalf("ignoreDocumentAt(the fixture's own document) = error %v, want the document", err)
	}
	if read.Description != own.Description || len(read.Ignore) != 1 || read.Ignore[0] != own.Ignore[0] {
		t.Errorf("ignoreDocumentAt(the fixture's own document) = %+v, want %+v", read, own)
	}
}

func TestAnswerFromRendersEveryMemberAnExpectationNames(t *testing.T) {
	t.Parallel()

	found := kinds.Finding{
		Code:           "DS1101",
		Symbol:         kinds.Subject{Kind: "function"},
		Class:          kinds.Certain,
		Confidence:     kinds.Probable,
		Relation:       graph.ReferenceCounting,
		Configurations: []string{"linux-amd64"},
		Details: kinds.Details{
			NarrowerVisibility: "file",
			Overlap:            []string{"revive unused-parameter"},
		},
	}

	tests := []struct {
		name     string
		expected corpusExpectation
		want     reportedAnswer
	}{
		{
			name:     "a_row_naming_the_code_and_the_confidence_alone",
			expected: corpusExpectation{Report: "DS1101", Confidence: "probable"},
			want:     reportedAnswer{Report: "DS1101", Confidence: "probable"},
		},
		{
			name: "a_row_naming_one_details_member",
			expected: corpusExpectation{
				Report: "DS1101", Confidence: "probable",
				Details: &corpusDetails{NarrowerVisibility: "file"},
			},
			want: reportedAnswer{
				Report: "DS1101", Confidence: "probable",
				Details: &corpusDetails{NarrowerVisibility: "file"},
			},
		},
		{
			name: "a_row_naming_every_member",
			expected: corpusExpectation{
				Report: "DS1101", Confidence: "probable", SymbolKind: "function",
				ReachabilityClass: "certain", LivenessRelation: "reference-counting",
				Configurations: []string{"linux-amd64"},
				Details: &corpusDetails{
					NarrowerVisibility: "file",
					Overlap:            []string{"revive unused-parameter"},
				},
			},
			want: reportedAnswer{
				Report: "DS1101", Confidence: "probable", SymbolKind: "function",
				ReachabilityClass: "certain", LivenessRelation: "reference-counting",
				Configurations: []string{"linux-amd64"},
				Details: &corpusDetails{
					NarrowerVisibility: "file",
					Overlap:            []string{"revive unused-parameter"},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := answerFrom(&tc.expected, &found)
			if got.Report != tc.want.Report || got.Confidence != tc.want.Confidence ||
				got.SymbolKind != tc.want.SymbolKind || got.ReachabilityClass != tc.want.ReachabilityClass ||
				got.LivenessRelation != tc.want.LivenessRelation ||
				!slices.Equal(got.Configurations, tc.want.Configurations) ||
				!sameDetails(got.Details, tc.want.Details) {
				t.Errorf("answerFrom(%+v) = %+v, want %+v", tc.expected, got, tc.want)
			}
			if message := differences(&tc.expected, &got); message != "" {
				t.Errorf("differences(%+v) = %q, want no difference", tc.expected, message)
			}
		})
	}
}

// sameDetails reports whether two answers carry the same details members, absence
// included: an answer that carries none where one is wanted is what a row pinning a
// member the finding does not hold produces.
func sameDetails(got, want *corpusDetails) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.NarrowerVisibility == want.NarrowerVisibility && got.ExcludedBy == want.ExcludedBy &&
		got.DependencyClass == want.DependencyClass && got.Replacement == want.Replacement &&
		got.Mechanism == want.Mechanism && got.Edge == want.Edge &&
		slices.Equal(got.Overlap, want.Overlap)
}

// TestDifferencesNamesEveryMemberThatDisagrees pins the failure a mismatch produces:
// one line naming each member the expectation states and the answer does not match, so
// a reader of a failed run corrects the analyzer rather than hunting for the member.
func TestDifferencesNamesEveryMemberThatDisagrees(t *testing.T) {
	t.Parallel()

	expected := corpusExpectation{
		Report: "DS1001", Confidence: "certain", SymbolKind: "function",
		ReachabilityClass: "certain", LivenessRelation: "reference-counting",
		Configurations: []string{"linux-amd64"},
		Details: &corpusDetails{
			NarrowerVisibility: "file",
			ExcludedBy:         "handwritten",
			DependencyClass:    "require",
			Replacement:        "golang.org/x/sync v0.17.0",
			Mechanism:          "inline",
			Edge:               "wire/plan",
			Overlap:            []string{"unparam"},
		},
	}
	actual := reportedAnswer{
		Report: "DS1002", Confidence: "probable", SymbolKind: "method",
		ReachabilityClass: "possible", LivenessRelation: "reachability",
		Configurations: []string{"linux-arm64"},
		Details: &corpusDetails{
			NarrowerVisibility: "package",
			ExcludedBy:         "generated",
			DependencyClass:    "dependency",
			Replacement:        "golang.org/x/sync v0.18.0",
			Mechanism:          "ignore",
			Overlap:            []string{"revive unused-parameter"},
		},
	}

	members := []string{
		"report", "confidence", "symbol_kind", "reachability_class", "liveness_relation", "configurations",
		"details.narrower_visibility", "details.excluded_by", "details.dependency_class",
		"details.replacement", "details.mechanism", "details.edge", "details.overlap",
	}
	got := differences(&expected, &actual)
	for _, member := range members {
		if !strings.Contains(got, member) {
			t.Errorf("differences() = %q, want it to name %s", got, member)
		}
	}
}
