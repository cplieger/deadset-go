package report_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/report"
	"github.com/cplieger/deadset-go/internal/reporttest"
)

// TestTheDocumentRoundTrips pins that a report document read back and written again is
// the same bytes, which is what makes the envelope and the schema one shape rather than
// two.
func TestTheDocumentRoundTrips(t *testing.T) {
	in := report.FullInput()
	envelope := report.Built(t, &in)
	first := report.Rendered(t, "json", &envelope, report.Options{})

	read, err := reporttest.Read(first)
	if err != nil {
		t.Fatalf("reporttest.Read(a report document) = error %v, want the envelope it encodes", err)
	}
	second := report.Rendered(t, "json", &read, report.Options{})
	if !bytes.Equal(first, second) {
		t.Errorf("a report document written, read and written again differs:\n%s", report.Diff(string(first), string(second)))
	}
}

// TestTheDocumentRefusesAnUnknownMember pins that a member the schema does not declare
// is refused rather than dropped, so a document from another product or another schema
// version is never read as though the member were absent.
func TestTheDocumentRefusesAnUnknownMember(t *testing.T) {
	in := report.MinimalInput()
	envelope := report.Built(t, &in)
	document := string(report.Rendered(t, "json", &envelope, report.Options{}))

	cases := map[string]string{
		"a member of the envelope":       `{\n  "invented": 1,`,
		"a member a merged report holds": `{\n  "merged_from": [],`,
	}
	for name, member := range cases {
		t.Run(name, func(t *testing.T) {
			altered := strings.Replace(document, "{\n", strings.ReplaceAll(member, `\n`, "\n"), 1)
			if _, err := reporttest.Read([]byte(altered)); err == nil {
				t.Fatal("reporttest.Read(a document holding an undeclared member) = no error, want a refusal")
			}
		})
	}
}

// TestTheDocumentRefusesTrailingContent pins that bytes after the document are
// refused, so a run that wrote two documents is not read as its first.
func TestTheDocumentRefusesTrailingContent(t *testing.T) {
	in := report.MinimalInput()
	envelope := report.Built(t, &in)
	document := report.Rendered(t, "json", &envelope, report.Options{})

	for name, after := range map[string][]byte{
		"a second document": document,
		"a bracket":         []byte("]"),
		"a brace":           []byte("}"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := reporttest.Read(append(slices.Clone(document), after...)); err == nil {
				t.Errorf("reporttest.Read(a document followed by %s) = no error, want a refusal", name)
			}
		})
	}
}

// TestTheDocumentRefusesAnUnknownRelation pins that a liveness relation the vocabulary
// does not hold is refused, rather than read as the first of the two.
func TestTheDocumentRefusesAnUnknownRelation(t *testing.T) {
	in := report.FullInput()
	envelope := report.Built(t, &in)
	document := strings.Replace(string(report.Rendered(t, "json", &envelope, report.Options{})),
		`"liveness_relation": "reference-counting"`, `"liveness_relation": "guesswork"`, 1)

	_, err := reporttest.Read([]byte(document))
	if err == nil {
		t.Fatal("reporttest.Read(a document naming no relation) = no error, want a refusal")
	}
	if !strings.Contains(err.Error(), "guesswork") {
		t.Errorf("reporttest.Read(a document naming no relation) = error %q, want one naming the spelling", err)
	}
}

// TestTheLiveSubjectSurvivesTheRoundTrip pins that a document holding no liveness
// relation is read back as a finding about a live subject, so a rendering of the
// result omits the member again rather than inventing the relation it was read with.
func TestTheLiveSubjectSurvivesTheRoundTrip(t *testing.T) {
	live := report.FindingOf("DS1101", "unnecessary-export", "normalize.go", 12, 27,
		config.Warn, "narrowable", "exported function is referenced only inside the package that declares it")
	live.Live = true
	live.Details.NarrowerVisibility = "package"

	in := report.MinimalInput()
	in.Result.Findings = []kinds.Finding{live}
	envelope := report.Built(t, &in)

	read, err := reporttest.Read(report.Rendered(t, "json", &envelope, report.Options{}))
	if err != nil {
		t.Fatalf("reporttest.Read(a report document) = error %v, want the envelope it encodes", err)
	}
	if !read.Findings[0].Live {
		t.Error("a finding read from a document naming no relation is not about a live subject")
	}
}

// TestTheDocumentWritesTheSuppressionRecordAFindingReports pins the two members a
// finding about a suppression record carries, and that a member the record lacks is
// absent rather than empty: the reason-free and the unscoped kinds report exactly the
// absence, and the schema admits no empty spelling of either member.
func TestTheDocumentWritesTheSuppressionRecordAFindingReports(t *testing.T) {
	reasonFree := report.FindingOf("DS1701", "suppression-without-reason", "deadset-ignore.json", 7, 7,
		config.Deny, "manual", "the ignore entry carries no reason")
	reasonFree.Live = true
	reasonFree.Symbol.Kind = "suppression"
	reasonFree.Details.Mechanism = "ignore"
	reasonFree.Details.Entry = &kinds.Entry{
		Code:   "DS1001",
		Symbol: "go://example.com/app#Catalog.legacyAlias",
		Path:   "catalog.go",
	}

	unscoped := report.FindingOf("DS1702", "unscoped-suppression", "deadset-ignore.json", 9, 9,
		config.Deny, "manual", "the ignore entry names no path")
	unscoped.Live = true
	unscoped.Symbol.Kind = "suppression"
	unscoped.Details.Mechanism = "ignore"
	unscoped.Details.Entry = &kinds.Entry{Code: "DS1001", Reason: "removal is breaking"}

	in := report.MinimalInput()
	in.Result.Findings = []kinds.Finding{reasonFree, unscoped}
	envelope := report.Built(t, &in)
	document := string(report.Rendered(t, "json", &envelope, report.Options{}))

	for _, member := range []string{
		`"mechanism": "ignore"`,
		`"code": "DS1001"`,
		`"symbol": "go://example.com/app#Catalog.legacyAlias"`,
		`"path": "catalog.go"`,
		`"reason": "removal is breaking"`,
	} {
		if !strings.Contains(document, member) {
			t.Errorf("the document carries no %s among the records the findings report:\n%s", member, document)
		}
	}
	if strings.Contains(document, `"reason": ""`) {
		t.Errorf("the document writes an empty reason for the record the reason-free kind reports:\n%s", document)
	}
	if strings.Contains(document, `"path": ""`) {
		t.Errorf("the document writes an empty path for the record the unscoped kind reports:\n%s", document)
	}

	read, err := reporttest.Read([]byte(document))
	if err != nil {
		t.Fatalf("reporttest.Read(the document this run wrote) = error %v, want the envelope it encodes", err)
	}
	if len(read.Findings) != 2 {
		t.Fatalf("the document read back carries %d findings, want the two the run reported", len(read.Findings))
	}
	back := findingUnder(t, read.Findings, "DS1701")
	if held := back.Details.Entry; held == nil || held.Code != "DS1001" || held.Reason != "" {
		t.Errorf("the record read back for %s is %+v, want the record the run reported", back.Code, held)
	}
	if got := back.Details.Mechanism; got != "ignore" {
		t.Errorf("the mechanism read back for %s is %q, want %q", back.Code, got, "ignore")
	}
}

// TestTheDocumentWritesTheOverlapAnIntraFunctionFindingCarries pins that the external
// rules reporting the same kind reach the document, and that a kind the vocabulary
// lists none for carries no member at all, because the schema admits no empty array
// there and forbids the member outside the intra-function group.
func TestTheDocumentWritesTheOverlapAnIntraFunctionFindingCarries(t *testing.T) {
	part := report.FindingOf("DS1801", "unused-parameter", "normalize.go", 12, 12,
		config.Warn, "deletable", "parameter limit is never read in the body")
	part.Live = true
	part.Symbol.Kind = "parameter"
	part.Details.Overlap = []string{"revive unused-parameter", "gopls unusedparams", "unparam"}

	declaration := report.FindingOf("DS1002", "unused-unexported", "catalog.go", 4, 8,
		config.Deny, "deletable", "the function has no reference in the target")

	in := report.MinimalInput()
	in.Result.Findings = []kinds.Finding{part, declaration}
	envelope := report.Built(t, &in)
	document := string(report.Rendered(t, "json", &envelope, report.Options{}))

	for _, named := range part.Details.Overlap {
		if !strings.Contains(document, `"`+named+`"`) {
			t.Errorf("the document does not name %s among the rules that report the same kind:\n%s", named, document)
		}
	}
	if got, want := strings.Count(document, `"overlap"`), 1; got != want {
		t.Errorf("the document names the overlap member %d times, want %d: the kind the vocabulary lists none for carries none:\n%s",
			got, want, document)
	}

	read, err := reporttest.Read([]byte(document))
	if err != nil {
		t.Fatalf("reporttest.Read(the document this run wrote) = error %v, want the envelope it encodes", err)
	}
	back := findingUnder(t, read.Findings, "DS1801")
	if got := back.Details.Overlap; !slices.Equal(got, part.Details.Overlap) {
		t.Errorf("the overlap read back for %s is %v, want %v", back.Code, got, part.Details.Overlap)
	}
}

// findingUnder is the one finding of a list reported under one code, which is how a
// test names its subject where the canonical order decides the positions.
func findingUnder(t *testing.T, findings []kinds.Finding, code string) *kinds.Finding {
	t.Helper()

	at := slices.IndexFunc(findings, func(found kinds.Finding) bool { return found.Code == code })
	if at < 0 {
		t.Fatalf("no finding of the list is reported under %s, want the one the run reported", code)
	}
	return &findings[at]
}
