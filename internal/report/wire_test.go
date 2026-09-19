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
		"schema_version", "contract_version", "analyzer", "target", "configurations", "consumers",
		"findings", "edge_evaluations", "stale_suppressions", "declared_gaps", "excluded_by_cgo",
		"test_file_rules", "totals",
	}
	for _, member := range want {
		if _, held := document[member]; !held {
			t.Errorf("the document holds no %q", member)
		}
	}
	if len(document) != len(want) {
		t.Errorf("the document holds %d members, want the %d the schema declares", len(document), len(want))
	}
	for _, member := range []string{"findings", "excluded_by_cgo", "declared_gaps"} {
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
