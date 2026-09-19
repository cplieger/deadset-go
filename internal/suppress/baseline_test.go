package suppress

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// The analyzer identity every written row of these tests carries.
var identity = Provenance{Analyzer: "deadset-go", Version: "1.6.0"}

// writeBaselineFile writes one baseline document into a directory of its own and
// returns its path.
func writeBaselineFile(t *testing.T, document string) string {
	t.Helper()

	return writeDocument(t, BaselineFileName, document)
}

// baselineDocumentOf wraps one row, as the corpus writes it, in the document that
// carries it.
func baselineDocumentOf(row string) string {
	return "{\n  \"baseline\": [\n    " + row + "\n  ]\n}\n"
}

// baselineOutcomesOf reads one baseline document and names what the reader made of
// its one row.
func baselineOutcomesOf(t *testing.T, document string) []entryOutcome {
	t.Helper()

	records, refusals, err := Baseline(writeBaselineFile(t, document), nil)
	switch {
	case err != nil:
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("Baseline(%s) error = %v, want one satisfying errors.Is(err, ErrMalformed)", document, err)
		}
		return []entryOutcome{outcomeMalformed}
	case len(refusals) > 0:
		got := make([]entryOutcome, 0, len(refusals))
		for _, refusal := range refusals {
			got = append(got, entryOutcome(refusal.Reported))
		}
		return got
	case len(records) != 1:
		t.Fatalf("Baseline(%s) returned %d records, want one: %+v", document, len(records), records)
	}
	return []entryOutcome{outcomeRecord}
}

func TestBaselineDecidesEveryPublishedRowCase(t *testing.T) {
	for _, c := range readCorpus(t, "baseline-row") {
		t.Run(c.Rule+" "+string(c.Input), func(t *testing.T) {
			outcome, declared := entryOutcomes()[c.Rule]
			if !declared {
				t.Fatalf("the corpus names rule %s and no outcome is declared for it: %s", c.Rule, c.Reason)
			}
			want := wanted(&c, outcome)
			if got := baselineOutcomesOf(t, baselineDocumentOf(string(c.Input))); !slices.Equal(got, want) {
				t.Errorf("Baseline over the row %s = %v, want %v: %s", c.Input, got, want, c.Reason)
			}
		})
	}
}

func TestBaselineBindsARowAndCarriesItsProvenanceAsTheReason(t *testing.T) {
	_, _, symbols := loaded(t, "same-name.txtar")
	ref, path := boundSymbol(t, symbols)

	document := baselineDocumentOf(`{"code": "DS1002", "symbol": "` + ref + `", "path": "` + path +
		`", "reason": "recorded by deadset-go 1.6.0"}`)
	records, refusals, err := Baseline(writeBaselineFile(t, document), symbols)
	if err != nil {
		t.Fatalf("Baseline(a bound row) = _, _, %v, want the record it bound", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("Baseline(a bound row) refused %+v, want no refusal", refusals)
	}
	if len(records) != 1 {
		t.Fatalf("Baseline(a bound row) returned %d records, want 1: %+v", len(records), records)
	}
	one := records[0]
	switch {
	case one.Mechanism != MechanismBaseline:
		t.Errorf("Baseline(a bound row) carries mechanism %s, want %s", one.Mechanism, MechanismBaseline)
	case one.Bound == "":
		t.Errorf("Baseline(a bound row) bound nothing, want the declaration %s names", ref)
	case one.Reason != "recorded by deadset-go 1.6.0":
		t.Errorf("Baseline(a bound row) carries reason %q, want the provenance the row holds", one.Reason)
	case one.Site.Filename != BaselineFileName || one.Site.Line != 3 || one.Site.Column != 5:
		t.Errorf("Baseline(a bound row) sites the record at %s, want %s:3:5", one.Site, BaselineFileName)
	}
}

func TestBaselineAndTheIgnoreFileRefuseEachOtherMember(t *testing.T) {
	for name, held := range map[string]struct {
		document string
		read     func(path string) error
	}{
		"the ignore file carrying a baseline array": {
			document: `{"baseline": []}`,
			read: func(path string) error {
				_, _, err := IgnoreFile(path, nil)
				return err
			},
		},
		"the baseline carrying an ignore array": {
			document: `{"ignore": []}`,
			read: func(path string) error {
				_, _, err := Baseline(path, nil)
				return err
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "document.json")
			if err := os.WriteFile(path, []byte(held.document), 0o600); err != nil {
				t.Fatalf("Setup: write %s: %v", path, err)
			}
			if err := held.read(path); !errors.Is(err, ErrMalformed) {
				t.Errorf("reading %s = %v, want an error satisfying errors.Is(err, ErrMalformed): "+
					"each document has its own closed key list", name, err)
			}
		})
	}
}

func TestBaselineOfAnAbsentDocumentIsAnEmptyOne(t *testing.T) {
	records, refusals, err := Baseline(filepath.Join(t.TempDir(), BaselineFileName), nil)
	if err != nil {
		t.Fatalf("Baseline(an absent document) = _, _, %v, want no error", err)
	}
	if records != nil || refusals != nil {
		t.Errorf("Baseline(an absent document) = %+v, %+v, want neither a record nor a refusal", records, refusals)
	}
}

func TestWriteBaselineRecordsEveryFindingWithItsProvenance(t *testing.T) {
	findings := []Recorded{
		{Code: "DS1002", Symbol: "go://example.com/app#forgotten", Path: "app.go"},
		{Code: "DS1001", Symbol: "go://example.com/app#Exported", Path: "app.go"},
	}
	var written bytes.Buffer
	if err := WriteBaseline(&written, findings, identity); err != nil {
		t.Fatalf("WriteBaseline(two findings) = %v, want the document", err)
	}

	const want = `{
  "description": "Recorded findings: a run fails only on a finding this document does not hold. Every reason is the analyzer that recorded the row, never an adjudication.",
  "baseline": [
    {
      "code": "DS1002",
      "symbol": "go://example.com/app#forgotten",
      "path": "app.go",
      "reason": "recorded by deadset-go 1.6.0"
    },
    {
      "code": "DS1001",
      "symbol": "go://example.com/app#Exported",
      "path": "app.go",
      "reason": "recorded by deadset-go 1.6.0"
    }
  ]
}
`
	if got := written.String(); got != want {
		t.Errorf("WriteBaseline(two findings) wrote\n%s\nwant\n%s", got, want)
	}
}

func TestWriteBaselineOfNoFindingIsAnEmptyArray(t *testing.T) {
	var written bytes.Buffer
	if err := WriteBaseline(&written, nil, identity); err != nil {
		t.Fatalf("WriteBaseline(no finding) = %v, want the document", err)
	}
	if got := written.String(); !bytes.Contains([]byte(got), []byte(`"baseline": []`)) {
		t.Errorf("WriteBaseline(no finding) wrote %s, want an empty baseline array rather than a null", got)
	}
}

func TestWriteBaselineIsTheSameBytesForTheSameReport(t *testing.T) {
	findings := []Recorded{{Code: "DS1002", Symbol: "go://example.com/app#forgotten", Path: "app.go"}}
	var first, second bytes.Buffer
	for _, into := range []*bytes.Buffer{&first, &second} {
		if err := WriteBaseline(into, findings, identity); err != nil {
			t.Fatalf("WriteBaseline(one finding) = %v, want the document", err)
		}
	}
	if first.String() != second.String() {
		t.Errorf("WriteBaseline wrote\n%s\nand then\n%s\nwant one document written twice", first.String(), second.String())
	}
}

func TestWriteBaselineLeavesAReferenceAsTheGrammarSpellsIt(t *testing.T) {
	// A TypeScript reference carries a type parameter in angle brackets, which is
	// what a JSON encoder escapes for HTML and what a byte comparison against the
	// grammar's spelling would then fail on.
	findings := []Recorded{{
		Code:   "DS1003",
		Symbol: "ts://@example/app/src/wire.ts#Codec.decode<T>",
		Path:   "src/wire.ts",
	}}
	var written bytes.Buffer
	if err := WriteBaseline(&written, findings, identity); err != nil {
		t.Fatalf("WriteBaseline(a TypeScript reference) = %v, want the document", err)
	}
	if !bytes.Contains(written.Bytes(), []byte("ts://@example/app/src/wire.ts#Codec.decode<T>")) {
		t.Errorf("WriteBaseline(a TypeScript reference) wrote %s, want the reference as the grammar spells it",
			written.String())
	}
}

func TestWriteBaselineRefusesADocumentWhoseRowsWouldCarryNoReason(t *testing.T) {
	for name, held := range map[string]struct {
		findings   []Recorded
		provenance Provenance
	}{
		"an identity with no analyzer name": {
			findings:   []Recorded{{Code: "DS1001", Symbol: "go://example.com/app#A", Path: "app.go"}},
			provenance: Provenance{Version: "1.6.0"},
		},
		"an identity with no version": {
			findings:   []Recorded{{Code: "DS1001", Symbol: "go://example.com/app#A", Path: "app.go"}},
			provenance: Provenance{Analyzer: "deadset-go"},
		},
		"a finding naming no code": {
			findings:   []Recorded{{Symbol: "go://example.com/app#A", Path: "app.go"}},
			provenance: identity,
		},
		"a finding naming no symbol": {
			findings:   []Recorded{{Code: "DS1001", Path: "app.go"}},
			provenance: identity,
		},
		"a finding naming no path": {
			findings:   []Recorded{{Code: "DS1001", Symbol: "go://example.com/app#A"}},
			provenance: identity,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var written bytes.Buffer
			err := WriteBaseline(&written, held.findings, held.provenance)
			if !errors.Is(err, ErrProvenance) {
				t.Fatalf("WriteBaseline(%s) = %v, want an error satisfying errors.Is(err, ErrProvenance)", name, err)
			}
			if written.Len() != 0 {
				t.Errorf("WriteBaseline(%s) wrote %s, want nothing", name, written.String())
			}
		})
	}
}

func TestWriteBaselineReadBackSuppressesExactlyTheRecordedFindings(t *testing.T) {
	_, _, symbols := loaded(t, "same-name.txtar")
	ref, path := boundSymbol(t, symbols)

	var written bytes.Buffer
	findings := []Recorded{{Code: "DS1002", Symbol: ref, Path: path}}
	if err := WriteBaseline(&written, findings, identity); err != nil {
		t.Fatalf("WriteBaseline(one finding) = %v, want the document", err)
	}

	records, refusals, err := Baseline(writeBaselineFile(t, written.String()), symbols)
	if err != nil {
		t.Fatalf("Baseline(the document WriteBaseline wrote) = _, _, %v, want the record it recorded", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("Baseline(the document WriteBaseline wrote) refused %+v, want no refusal: "+
			"every written row carries a reason", refusals)
	}
	if len(records) != 1 {
		t.Fatalf("Baseline(the document WriteBaseline wrote) returned %d records, want 1: %+v", len(records), records)
	}
	one := records[0]
	switch {
	case one.Code != "DS1002" || one.Symbol != ref || one.Path != path:
		t.Errorf("Baseline(the document WriteBaseline wrote) read %+v, want the finding that was recorded", one)
	case one.Bound == "":
		t.Errorf("Baseline(the document WriteBaseline wrote) bound nothing, want the declaration %s names", ref)
	case one.Reason != identity.reason():
		t.Errorf("Baseline(the document WriteBaseline wrote) carries reason %q, want %q", one.Reason, identity.reason())
	}
}

// boundSymbol is the reference and path of one declaration of a loaded fixture that
// an entry or a row can name, which is the first function the inventory holds.
func boundSymbol(t *testing.T, symbols []graph.Symbol) (ref, path string) {
	t.Helper()

	for i := range symbols {
		if symbols[i].Kind == graph.KindFunc {
			return symbols[i].Ref, symbols[i].Pos.Filename
		}
	}
	t.Fatal("Setup: the fixture holds no function for a suppression to name")
	return "", ""
}
