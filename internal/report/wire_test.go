package report

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// TestTheDocumentRoundTrips pins that a report document read back and written again is
// the same bytes, which is what makes the envelope and the schema one shape rather than
// two.
func TestTheDocumentRoundTrips(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	first := rendered(t, "json", &envelope, Options{})

	var read Envelope
	if err := json.Unmarshal(first, &read); err != nil {
		t.Fatalf("json.Unmarshal(a report document) = error %v, want the envelope it encodes", err)
	}
	second := rendered(t, "json", &read, Options{})
	if !bytes.Equal(first, second) {
		t.Errorf("a report document written, read and written again differs:\n%s", diff(string(first), string(second)))
	}
}

// TestTheDocumentRefusesAnUnknownMember pins that a member the schema does not declare
// is refused rather than dropped, so a document from another product or another schema
// version is never read as though the member were absent.
func TestTheDocumentRefusesAnUnknownMember(t *testing.T) {
	in := minimalInput()
	envelope := built(t, &in)
	document := string(rendered(t, "json", &envelope, Options{}))

	cases := map[string]string{
		"a member of the envelope":       `{\n  "invented": 1,`,
		"a member a merged report holds": `{\n  "merged_from": [],`,
	}
	for name, member := range cases {
		t.Run(name, func(t *testing.T) {
			altered := strings.Replace(document, "{\n", strings.ReplaceAll(member, `\n`, "\n"), 1)
			var read Envelope
			if err := json.Unmarshal([]byte(altered), &read); err == nil {
				t.Fatal("json.Unmarshal(a document holding an undeclared member) = no error, want a refusal")
			}
		})
	}
}

// TestTheDocumentRefusesAnUnknownRelation pins that a liveness relation the vocabulary
// does not hold is refused, rather than read as the first of the two.
func TestTheDocumentRefusesAnUnknownRelation(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	document := strings.Replace(string(rendered(t, "json", &envelope, Options{})),
		`"liveness_relation": "reference-counting"`, `"liveness_relation": "guesswork"`, 1)

	var read Envelope
	err := json.Unmarshal([]byte(document), &read)
	if err == nil {
		t.Fatal("json.Unmarshal(a document naming no relation) = no error, want a refusal")
	}
	if !strings.Contains(err.Error(), "guesswork") {
		t.Errorf("json.Unmarshal(a document naming no relation) = error %q, want one naming the spelling", err)
	}
}

// TestTheDocumentWritesEveryRequiredMember pins that every member the schema requires
// is in the document whatever the run answered, because a reader never distinguishes an
// absent array from an empty one.
func TestTheDocumentWritesEveryRequiredMember(t *testing.T) {
	in := minimalInput()
	envelope := built(t, &in)

	var document map[string]json.RawMessage
	if err := json.Unmarshal(rendered(t, "json", &envelope, Options{}), &document); err != nil {
		t.Fatalf("json.Unmarshal(a report document) = error %v, want its members", err)
	}
	want := []string{
		"schema_version", "contract_version", "analyzer", "target", "configurations",
		"configurations_not_built", "consumers", "findings", "edge_evaluations",
		"stale_suppressions", "declared_gaps", "excluded_by_cgo", "test_file_rules", "totals",
	}
	for _, member := range want {
		if _, held := document[member]; !held {
			t.Errorf("the document holds no %q", member)
		}
	}
	if len(document) != len(want) {
		t.Errorf("the document holds %d members, want the %d the schema declares", len(document), len(want))
	}
	for _, member := range []string{"findings", "excluded_by_cgo", "declared_gaps", "configurations_not_built"} {
		if got := string(document[member]); got != "[]" {
			t.Errorf("the document writes %q as %s, want an empty array", member, got)
		}
	}
}

// TestTheDocumentWritesAnEmptyArrayForANilOne pins that a finding whose lists are unset
// writes them as empty arrays, because a null there is a value the schema refuses.
func TestTheDocumentWritesAnEmptyArrayForANilOne(t *testing.T) {
	bare := kinds.Finding{
		Code: "DS1001", Kind: "unused-exported", Language: "go",
		Position: kinds.Position{Path: "catalog.go", Line: 1, Column: 1, EndLine: 1},
		Symbol:   kinds.Subject{Ref: "go://example.com/app#F", Kind: "function", Name: "F", SizeLines: 1},
		Message:  "exported function has no reference in the target",
	}
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{bare}
	envelope := built(t, &in)

	document := string(rendered(t, "json", &envelope, Options{}))
	for _, member := range []string{"retained_by", "configurations", "consumers_loaded"} {
		if !strings.Contains(document, `"`+member+`": []`) {
			t.Errorf("the document writes %q as something other than an empty array:\n%s", member, document)
		}
	}
	if !strings.Contains(document, `"details": {}`) {
		t.Errorf("the document writes an unset details as something other than an empty object:\n%s", document)
	}
}

// TestTheDocumentOmitsTheLivenessRelationOfALiveSubject pins that the member is
// absent on a finding about a subject the sweep judged live and present on every
// other, because the presence of the member is the claim that a relation decided the
// subject.
func TestTheDocumentOmitsTheLivenessRelationOfALiveSubject(t *testing.T) {
	live := findingOf("DS1101", "unnecessary-export", "normalize.go", 12, 27,
		config.Warn, "narrowable", "exported function is referenced only inside the package that declares it")
	live.Live = true
	live.Details.NarrowerVisibility = "package"
	decided := findingOf("DS1002", "unused-unexported", "catalog.go", 4, 8,
		config.Deny, "deletable", "the function has no reference in the target")

	in := minimalInput()
	in.Result.Findings = []kinds.Finding{live, decided}
	envelope := built(t, &in)
	document := string(rendered(t, "json", &envelope, Options{}))

	if got := strings.Count(document, `"liveness_relation"`); got != 1 {
		t.Errorf("the document names the liveness relation %d times, want once for the one finding a relation decided:\n%s",
			got, document)
	}
	if !strings.Contains(document, `"liveness_relation": "reference-counting"`) {
		t.Errorf("the document names no relation for the finding a relation decided:\n%s", document)
	}
}

// TestTheLiveSubjectSurvivesTheRoundTrip pins that a document holding no liveness
// relation is read back as a finding about a live subject, so a rendering of the
// result omits the member again rather than inventing the relation it was read with.
func TestTheLiveSubjectSurvivesTheRoundTrip(t *testing.T) {
	live := findingOf("DS1101", "unnecessary-export", "normalize.go", 12, 27,
		config.Warn, "narrowable", "exported function is referenced only inside the package that declares it")
	live.Live = true
	live.Details.NarrowerVisibility = "package"

	in := minimalInput()
	in.Result.Findings = []kinds.Finding{live}
	envelope := built(t, &in)

	var read Envelope
	if err := json.Unmarshal(rendered(t, "json", &envelope, Options{}), &read); err != nil {
		t.Fatalf("json.Unmarshal(a report document) = error %v, want the envelope it encodes", err)
	}
	if !read.Findings[0].Live {
		t.Error("a finding read from a document naming no relation is not about a live subject")
	}
}

// TestTheDocumentWritesEveryDependencyADeletionOrphans pins that the member is the
// whole set rather than one of it, because one deletion can remove the last use of
// several dependencies and a reader acts on each.
func TestTheDocumentWritesEveryDependencyADeletionOrphans(t *testing.T) {
	several := findingOf("DS1002", "unused-unexported", "catalog.go", 4, 8,
		config.Deny, "deletable", "the function has no reference in the target")
	several.Details.RemovesLastUseOf = []string{"example.com/left", "example.com/right"}

	in := minimalInput()
	in.Result.Findings = []kinds.Finding{several}
	envelope := built(t, &in)
	document := string(rendered(t, "json", &envelope, Options{}))

	for _, named := range []string{"example.com/left", "example.com/right"} {
		if !strings.Contains(document, `"`+named+`"`) {
			t.Errorf("the document does not name %s among the dependencies the deletion orphans:\n%s",
				named, document)
		}
	}
	if strings.Contains(document, `"removes_last_use_of": "`) {
		t.Errorf("the document writes the member as one dependency rather than as the set:\n%s", document)
	}
}

// TestTheDocumentOmitsAnEmptyDependencySet pins that a finding whose deletion orphans
// nothing carries no member at all, because the schema admits no empty array there.
func TestTheDocumentOmitsAnEmptyDependencySet(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{findingOf("DS1002", "unused-unexported", "catalog.go", 4, 8,
		config.Deny, "deletable", "the function has no reference in the target")}
	envelope := built(t, &in)

	if document := string(rendered(t, "json", &envelope, Options{})); strings.Contains(document, "removes_last_use_of") {
		t.Errorf("the document names the member for a deletion that orphans nothing:\n%s", document)
	}
}

// TestTheDocumentNamesThePositionedSymbolsDisplayName pins that a symbol a finding
// names beside its subject carries the name a text line renders, so a reader of a
// related location reads the same name as a reader of a finding line.
func TestTheDocumentNamesThePositionedSymbolsDisplayName(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	document := string(rendered(t, "json", &envelope, Options{}))

	if !strings.Contains(document, `"ref": "go://example.com/app#memoryStore.Purge"`) {
		t.Fatalf("the document names no implementation:\n%s", document)
	}
	if !strings.Contains(document, `"name": "(*memoryStore).Purge"`) {
		t.Errorf("the document carries no display name for the implementation it names:\n%s", document)
	}
}

// TestTheDocumentNamesTheConsumerRole pins the one value the schema admits for the role
// of an entry of either consumer list.
func TestTheDocumentNamesTheConsumerRole(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	document := string(rendered(t, "json", &envelope, Options{}))

	if got := strings.Count(document, `"role": "consumer"`); got != 2 {
		t.Errorf("the document names the consumer role %d times, want one per consumer entry", got)
	}
}

// TestThePendingFindingIsCarriedByItsEvaluation pins that a pending finding lives in
// the edge evaluation and appears in no finding list, so the report encodes the pending
// state once.
func TestThePendingFindingIsCarriedByItsEvaluation(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)

	held := slices.ContainsFunc(envelope.Findings, func(found kinds.Finding) bool {
		return found.Symbol.Ref == "go://example.com/app#unused-exported"
	})
	if held {
		t.Error("the finding list holds the pending finding, which belongs to its edge evaluation alone")
	}
	if envelope.EdgeEvaluations[0].Finding == nil {
		t.Fatal("the dead edge evaluation carries no pending finding")
	}
	if envelope.Totals.Pending != 1 {
		t.Errorf("Totals.Pending = %d, want one per dead evaluation", envelope.Totals.Pending)
	}
}

// TestTheDocumentWritesTheSuppressionRecordAFindingReports pins the two members a
// finding about a suppression record carries, and that a member the record lacks is
// absent rather than empty: the reason-free and the unscoped kinds report exactly the
// absence, and the schema admits no empty spelling of either member.
func TestTheDocumentWritesTheSuppressionRecordAFindingReports(t *testing.T) {
	reasonFree := findingOf("DS1701", "suppression-without-reason", "deadset-ignore.json", 7, 7,
		config.Deny, "manual", "the ignore entry carries no reason")
	reasonFree.Live = true
	reasonFree.Symbol.Kind = "suppression"
	reasonFree.Details.Mechanism = "ignore"
	reasonFree.Details.Entry = &kinds.Entry{
		Code:   "DS1001",
		Symbol: "go://example.com/app#Catalog.legacyAlias",
		Path:   "catalog.go",
	}

	unscoped := findingOf("DS1702", "unscoped-suppression", "deadset-ignore.json", 9, 9,
		config.Deny, "manual", "the ignore entry names no path")
	unscoped.Live = true
	unscoped.Symbol.Kind = "suppression"
	unscoped.Details.Mechanism = "ignore"
	unscoped.Details.Entry = &kinds.Entry{Code: "DS1001", Reason: "removal is breaking"}

	in := minimalInput()
	in.Result.Findings = []kinds.Finding{reasonFree, unscoped}
	envelope := built(t, &in)
	document := string(rendered(t, "json", &envelope, Options{}))

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

	var read Envelope
	if err := json.Unmarshal([]byte(document), &read); err != nil {
		t.Fatalf("json.Unmarshal(the document this run wrote) = error %v, want the envelope it encodes", err)
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
	part := findingOf("DS1801", "unused-parameter", "normalize.go", 12, 12,
		config.Warn, "deletable", "parameter limit is never read in the body")
	part.Live = true
	part.Symbol.Kind = "parameter"
	part.Details.Overlap = []string{"revive unused-parameter", "gopls unusedparams", "unparam"}

	declaration := findingOf("DS1002", "unused-unexported", "catalog.go", 4, 8,
		config.Deny, "deletable", "the function has no reference in the target")

	in := minimalInput()
	in.Result.Findings = []kinds.Finding{part, declaration}
	envelope := built(t, &in)
	document := string(rendered(t, "json", &envelope, Options{}))

	for _, named := range part.Details.Overlap {
		if !strings.Contains(document, `"`+named+`"`) {
			t.Errorf("the document does not name %s among the rules that report the same kind:\n%s", named, document)
		}
	}
	if got, want := strings.Count(document, `"overlap"`), 1; got != want {
		t.Errorf("the document names the overlap member %d times, want %d: the kind the vocabulary lists none for carries none:\n%s",
			got, want, document)
	}

	var read Envelope
	if err := json.Unmarshal([]byte(document), &read); err != nil {
		t.Fatalf("json.Unmarshal(the document this run wrote) = error %v, want the envelope it encodes", err)
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
