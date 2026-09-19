package report

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// sarifOf renders one envelope as SARIF and decodes the log, so a test reads the
// document rather than its text.
func sarifOf(t *testing.T, e *Envelope, opts Options) sarifLog {
	t.Helper()

	var log sarifLog
	if err := json.Unmarshal(rendered(t, "sarif", e, opts), &log); err != nil {
		t.Fatalf("json.Unmarshal(a SARIF log) = error %v, want the log", err)
	}
	return log
}

// TestTheSarifLogNamesTheFormatAndTheRun pins the fixed values of the document and the
// run: the schema, the version, the unit a column counts, the declared base identifier,
// the automation identifier a consumer tells one language's alerts by, and the totals
// the document carries for a reader of the file.
func TestTheSarifLogNamesTheFormatAndTheRun(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	log := sarifOf(t, &envelope, Options{Read: lines()})

	switch {
	case log.Schema != sarifSchema:
		t.Errorf("$schema = %q, want %q", log.Schema, sarifSchema)
	case log.Version != sarifVersion:
		t.Errorf("version = %q, want %q", log.Version, sarifVersion)
	case len(log.Runs) != 1:
		t.Fatalf("the log holds %d runs, want the one an analyzer writes", len(log.Runs))
	}
	run := &log.Runs[0]
	switch {
	case run.Tool.Driver.Name != testAnalyzerName:
		t.Errorf("tool.driver.name = %q, want %q", run.Tool.Driver.Name, testAnalyzerName)
	case run.Tool.Driver.SemanticVersion != testAnalyzerVersion:
		t.Errorf("tool.driver.semanticVersion = %q, want %q",
			run.Tool.Driver.SemanticVersion, testAnalyzerVersion)
	case run.ColumnKind != sarifColumnKind:
		t.Errorf("columnKind = %q, want %q", run.ColumnKind, sarifColumnKind)
	case run.AutomationDetails.ID != "deadset/go/":
		t.Errorf("automationDetails.id = %q, want %q", run.AutomationDetails.ID, "deadset/go/")
	case run.Properties.Totals.Findings != envelope.Totals.Findings:
		t.Errorf("properties.totals.findings = %d, want the report's %d",
			run.Properties.Totals.Findings, envelope.Totals.Findings)
	}
	if _, held := run.OriginalURIBaseIDs[sarifURIBaseID]; !held {
		t.Errorf("originalUriBaseIds declares %v, want %q", run.OriginalURIBaseIDs, sarifURIBaseID)
	}
}

// TestTheRuleListIsTheWholeVocabulary pins that the rules are every live kind of the
// run's language in bytewise order whatever the run reported, so a rule's index is
// stable across runs and configurations.
func TestTheRuleListIsTheWholeVocabulary(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	run := &sarifOf(t, &envelope, Options{Read: lines()}).Runs[0]

	var want []string
	for _, row := range catalog.Kinds() {
		if slices.Contains(row.Languages, "go") {
			want = append(want, row.Code)
		}
	}
	slices.Sort(want)
	got := make([]string, 0, len(run.Tool.Driver.Rules))
	for _, rule := range run.Tool.Driver.Rules {
		got = append(got, rule.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the rules are %v, want every live Go kind in bytewise order %v", got, want)
	}
	for i := range run.Results {
		result := &run.Results[i]
		if got := run.Tool.Driver.Rules[result.RuleIndex].ID; got != result.RuleID {
			t.Errorf("the result for %s points at rule %s", result.RuleID, got)
		}
	}
}

// TestTheRuleCarriesTheVocabularysDefaults pins the members of a rule this analyzer
// reads from the vocabulary: the kind's name, the level its default severity maps to,
// the precision its confidence ceiling maps to, and the problem severity a consumer
// combines with that precision.
func TestTheRuleCarriesTheVocabularysDefaults(t *testing.T) {
	in := minimalInput()
	envelope := built(t, &in)
	run := &sarifOf(t, &envelope, Options{Read: lines()}).Runs[0]

	for _, rule := range run.Tool.Driver.Rules {
		row, live := catalog.Kind(rule.ID)
		if !live {
			t.Errorf("the rules name %s, which the vocabulary holds no live row for", rule.ID)
			continue
		}
		want := sarifRule{
			ID:                   row.Code,
			Name:                 row.Name,
			DefaultConfiguration: sarifConfiguration{Level: levelOf(config.Severity(row.DefaultSeverity))},
			Properties: sarifRuleProperties{
				Precision: precisionOf(row.MaxClass),
				Problem:   sarifProblem{Severity: problemOf(config.Severity(row.DefaultSeverity))},
			},
		}
		if rule != want {
			t.Errorf("the rule for %s = %+v, want %+v", rule.ID, rule, want)
		}
	}
}

// TestOneResultPerRecord pins that every finding and every stale suppression is a
// result, in the order the report lists them, and that a finding a suppression held back
// is absent from the document while the totals still count it.
func TestOneResultPerRecord(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	run := &sarifOf(t, &envelope, Options{Read: lines()}).Runs[0]

	if want := len(envelope.Findings) + len(envelope.StaleSuppressions); len(run.Results) != want {
		t.Fatalf("the run holds %d results, want %d", len(run.Results), want)
	}
	for i := range envelope.Findings {
		if got := run.Results[i].RuleID; got != envelope.Findings[i].Code {
			t.Errorf("result %d reports %s, want the report's %s", i, got, envelope.Findings[i].Code)
		}
	}
	last := run.Results[len(run.Results)-1]
	if last.RuleID != staleSuppressionCode || last.Level != levelError {
		t.Errorf("the last result is %s at %s, want %s at %s",
			last.RuleID, last.Level, staleSuppressionCode, levelError)
	}
	if run.Properties.Totals.SuppressionsInEffect != 4 {
		t.Errorf("properties.totals.suppressions_in_effect = %d, want the count of what the document omits",
			run.Properties.Totals.SuppressionsInEffect)
	}
	if strings.Contains(string(rendered(t, "sarif", &envelope, Options{Read: lines()})), `"suppressions":`) {
		t.Error("the document carries a suppressions property, which a consumer would publish as an open alert")
	}
}

// TestTheResultLocatesAndDescribesTheFinding pins the location, the level, the field bag
// and the two fingerprints of one result.
func TestTheResultLocatesAndDescribesTheFinding(t *testing.T) {
	in := fullInput()
	withoutStaleSuppressions(&in)
	in.Result.Findings = in.Result.Findings[:1]
	in.EdgeEvaluations = nil
	envelope := built(t, &in)
	result := &sarifOf(t, &envelope, Options{Read: lines()}).Runs[0].Results[0]

	at := &result.Locations[0].PhysicalLocation
	switch {
	case at.ArtifactLocation.URI != "go.mod":
		t.Errorf("artifactLocation.uri = %q, want the target-relative path", at.ArtifactLocation.URI)
	case at.ArtifactLocation.URIBaseID != sarifURIBaseID:
		t.Errorf("artifactLocation.uriBaseId = %q, want %q", at.ArtifactLocation.URIBaseID, sarifURIBaseID)
	case at.Region != (sarifRegion{StartLine: 12, StartColumn: 2, EndLine: 12}):
		t.Errorf("region = %+v, want the finding's position with no end column", at.Region)
	case result.Level != levelError:
		t.Errorf("level = %q, want the level a deny finding maps to", result.Level)
	case result.Properties == nil:
		t.Fatal("the result carries no field bag, so a consumer of the document reads none of the finding")
	case result.Properties.Details.DependencyClass != "require":
		t.Errorf("properties.details.dependency_class = %q, want the finding's",
			result.Properties.Details.DependencyClass)
	}
	if result.PartialFingerprints.PrimaryLocationLineHash == "" ||
		result.PartialFingerprints.DeadsetSymbolRef == "" {
		t.Errorf("partialFingerprints = %+v, want both keys", result.PartialFingerprints)
	}
}

// TestTheUriEncodesEachSegment pins that a path a relative reference cannot carry
// verbatim is percent-encoded segment by segment, and that the link in the message
// renders the path a reader recognises.
func TestTheUriEncodesEachSegment(t *testing.T) {
	found := findingOf("DS1301", "write-only-symbol", "odd dir/a b.go", 4, 8,
		config.Deny, "deletable", "the field is written and never read")
	found.Details.WritePositions = []kinds.Position{{Path: "odd dir/a b.go", Line: 9, Column: 3, EndLine: 9}}
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{found}
	envelope := built(t, &in)
	result := &sarifOf(t, &envelope, Options{Read: lines()}).Runs[0].Results[0]

	if got := result.Locations[0].PhysicalLocation.ArtifactLocation.URI; got != "odd%20dir/a%20b.go" {
		t.Errorf("artifactLocation.uri = %q, want each segment percent-encoded", got)
	}
	want := "the field is written and never read (see [write odd dir/a b.go:9:3](1))"
	if result.Message.Text != want {
		t.Errorf("message.text = %q, want %q", result.Message.Text, want)
	}
}

// TestTheRelatedLocationsAreNumberedInTheMappingsOrder pins the order and the labels of
// the positions a finding names beyond its own, and the links that reach them.
func TestTheRelatedLocationsAreNumberedInTheMappingsOrder(t *testing.T) {
	in := fullInput()
	withoutStaleSuppressions(&in)
	in.EdgeEvaluations = nil
	envelope := built(t, &in)
	run := &sarifOf(t, &envelope, Options{Read: lines()}).Runs[0]

	for i := range run.Results {
		result := &run.Results[i]
		for at := range result.RelatedLocations {
			if got := result.RelatedLocations[at].ID; got != at+1 {
				t.Errorf("related location %d of %s carries id %d", at, result.RuleID, got)
			}
		}
		if len(result.RelatedLocations) == 0 {
			continue
		}
		label := result.RelatedLocations[0].Message.Text
		if result.RuleID == "DS1203" && label != labelImplementation {
			t.Errorf("the first related location of %s is labelled %q, want %q",
				result.RuleID, label, labelImplementation)
		}
		if result.RuleID == "DS1301" && label != labelWrite {
			t.Errorf("the first related location of %s is labelled %q, want %q",
				result.RuleID, label, labelWrite)
		}
		if !strings.Contains(result.Message.Text, "(see [") {
			t.Errorf("%s names related locations and links to none: %q", result.RuleID, result.Message.Text)
		}
	}
}

// TestTheRelatedLocationsAreBounded pins the hundred a result carries, because a consumer
// rejects a result naming more.
func TestTheRelatedLocationsAreBounded(t *testing.T) {
	found := findingOf("DS1301", "write-only-symbol", "catalog.go", 4, 8,
		config.Deny, "deletable", "the field is written and never read")
	for i := range 150 {
		found.Details.WritePositions = append(found.Details.WritePositions,
			kinds.Position{Path: "catalog.go", Line: 10 + i, Column: 3, EndLine: 10 + i})
	}
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{found}
	envelope := built(t, &in)

	if got := len(sarifOf(t, &envelope, Options{Read: lines()}).Runs[0].Results[0].RelatedLocations); got != maxRelatedLocations {
		t.Errorf("the result carries %d related locations, want at most %d", got, maxRelatedLocations)
	}
	if got := len(envelope.Findings[0].Details.WritePositions); got != 150 {
		t.Errorf("the report holds %d write positions, want every one of the 150", got)
	}
}

// TestTheSymbolFingerprintSurvivesALineMove pins the key a baseline joins on: the same
// code and the same symbol at another line digest identically, while the line hash does
// not.
func TestTheSymbolFingerprintSurvivesALineMove(t *testing.T) {
	first := findingOf("DS1002", "unused-unexported", "catalog.go", 12, 16,
		config.Deny, "deletable", "the function has no reference in the target")
	moved := first
	moved.Position.Line, moved.Position.EndLine = 212, 216

	in := minimalInput()
	in.Result.Findings = []kinds.Finding{first}
	before := built(t, &in)
	in.Result.Findings = []kinds.Finding{moved}
	after := built(t, &in)

	one := sarifOf(t, &before, Options{Read: lines()}).Runs[0].Results[0].PartialFingerprints
	two := sarifOf(t, &after, Options{Read: lines()}).Runs[0].Results[0].PartialFingerprints
	if one.DeadsetSymbolRef != two.DeadsetSymbolRef {
		t.Errorf("the symbol fingerprint moved with the line: %q became %q",
			one.DeadsetSymbolRef, two.DeadsetSymbolRef)
	}
	if one.PrimaryLocationLineHash == two.PrimaryLocationLineHash {
		t.Errorf("the line fingerprint did not move with the line: both are %q",
			one.PrimaryLocationLineHash)
	}
}

// TestTheSarifRenderingNeedsAReader pins that a rendering with no reader for the source
// lines is refused rather than written with a key a consumer would recompute differently.
func TestTheSarifRenderingNeedsAReader(t *testing.T) {
	in := minimalInput()
	envelope := built(t, &in)

	err := SARIF(&strings.Builder{}, &envelope, Options{})
	if !errors.Is(err, ErrOptions) {
		t.Errorf("SARIF(no reader) = error %v, want one carrying ErrOptions", err)
	}
}

// TestTheSarifRenderingFailsOnAnUnreadableLine pins the two ways a line fingerprint
// cannot be computed, each of which fails the rendering.
func TestTheSarifRenderingFailsOnAnUnreadableLine(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{findingOf("DS1002", "unused-unexported", "gone.go", 4000, 4000,
		config.Deny, "deletable", "the function has no reference in the target")}
	envelope := built(t, &in)

	if err := SARIF(&strings.Builder{}, &envelope, Options{Read: lines()}); err == nil {
		t.Error("SARIF(a finding past the end of its file) = no error, want the failure")
	}
	absent := func(string) ([]byte, error) { return nil, errRefused }
	if err := SARIF(&strings.Builder{}, &envelope, Options{Read: absent}); !errors.Is(err, errRefused) {
		t.Errorf("SARIF(a file it cannot read) = error %v, want the reader's own", err)
	}
}
