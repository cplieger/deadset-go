package suppress

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// documentOf wraps one entry, as the corpus writes it, in the document that
// carries it, so a case of either kind is read through the production path.
func documentOf(entry string) string {
	return "{\n  \"ignore\": [\n    " + entry + "\n  ]\n}\n"
}

// entryOutcome is what a reader makes of one entry.
type entryOutcome string

const (
	outcomeRecord    entryOutcome = "a record"
	outcomeNoReason  entryOutcome = "DS1701"
	outcomeUnscoped  entryOutcome = "DS1702"
	outcomeMalformed entryOutcome = "malformed"
)

// entryOutcomes maps one rule of the corpus vocabulary to what a reader makes of
// a case under it: the outcome for an accepted case and the outcome for a refused
// one.
func entryOutcomes() map[string][2]entryOutcome {
	return map[string][2]entryOutcome{
		"entry-shape":     {outcomeRecord, outcomeMalformed},
		"row-shape":       {outcomeRecord, outcomeMalformed},
		"reason-required": {outcomeRecord, outcomeNoReason},
		"path-required":   {outcomeRecord, outcomeUnscoped},
		"path-form":       {outcomeRecord, outcomeMalformed},
		"symbol-form":     {outcomeRecord, outcomeMalformed},
		"code-form":       {outcomeRecord, outcomeMalformed},
		"closed-keys":     {outcomeRecord, outcomeMalformed},
		"value-type":      {outcomeRecord, outcomeMalformed},
	}
}

// outcomeOf reads one document and names what the reader made of its one entry.
func outcomeOf(t *testing.T, document string) entryOutcome {
	t.Helper()
	records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), nil)
	switch {
	case err != nil:
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("IgnoreFile(%s) error = %v, want one satisfying errors.Is(err, ErrMalformed)", document, err)
		}
		return outcomeMalformed
	case len(refusals) == 1:
		return entryOutcome(refusals[0].Reported)
	case len(refusals) > 1:
		t.Fatalf("IgnoreFile(%s) returned %d refusals, want at most one: %+v", document, len(refusals), refusals)
	case len(records) != 1:
		t.Fatalf("IgnoreFile(%s) returned %d records, want one: %+v", document, len(records), records)
	}
	return outcomeRecord
}

func TestIgnoreFileDecidesEveryPublishedEntryCase(t *testing.T) {
	for _, kind := range []string{"ignore-entry", "baseline-row"} {
		for _, c := range readCorpus(t, kind) {
			t.Run(kind+" "+c.Rule+" "+string(c.Input), func(t *testing.T) {
				outcome, declared := entryOutcomes()[c.Rule]
				if !declared {
					t.Fatalf("the corpus names rule %s and no outcome is declared for it: %s", c.Rule, c.Reason)
				}
				want := outcome[1]
				if c.Accepted {
					want = outcome[0]
				}
				if got := outcomeOf(t, documentOf(string(c.Input))); got != want {
					t.Errorf("IgnoreFile over the %s case %s = %s, want %s: %s", kind, c.Input, got, want, c.Reason)
				}
			})
		}
	}
}

func TestIgnoreFileHasAnArmForEveryPublishedRuleAndNoneTheCorpusLacks(t *testing.T) {
	exercised := make(map[string]bool)
	for _, kind := range []string{"ignore-entry", "baseline-row"} {
		for _, c := range readCorpus(t, kind) {
			exercised[c.Rule] = true
		}
	}
	declared := entryOutcomes()
	for rule := range exercised {
		if _, ok := declared[rule]; !ok {
			t.Errorf("the published corpus exercises rule %s and no outcome is declared for it", rule)
		}
	}
	for rule := range declared {
		if !exercised[rule] {
			t.Errorf("an outcome is declared for rule %s, which the published corpus does not exercise", rule)
		}
	}
}

func TestIgnoreFileCarriesTheReasonAndTheCodeOfABoundEntry(t *testing.T) {
	const document = `{
  "description": "Adjudications for the fixture.",
  "ignore": [
    {
      "code": "DS1001",
      "symbol": "go://example.com/samename#Helper",
      "path": "catalog.go",
      "reason": "Kept for the promise the next major breaks."
    }
  ]
}
`
	_, _, symbols := loaded(t, "same-name.txtar")
	records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), symbols)
	if err != nil {
		t.Fatalf("IgnoreFile error: %v", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("IgnoreFile returned %d refusals, want none: %+v", len(refusals), refusals)
	}
	if len(records) != 1 {
		t.Fatalf("IgnoreFile returned %d records, want 1: %+v", len(records), records)
	}

	want := Record{
		Code:      "DS1001",
		Symbol:    "go://example.com/samename#Helper",
		Path:      "catalog.go",
		Reason:    "Kept for the promise the next major breaks.",
		Bound:     "catalog.go:6:6",
		Site:      records[0].Site,
		Mechanism: MechanismIgnore,
	}
	if records[0] != want {
		t.Errorf("IgnoreFile record = %+v, want %+v", records[0], want)
	}
}

func TestIgnoreFileBindsOnCodeSymbolAndPathTogether(t *testing.T) {
	_, _, symbols := loaded(t, "same-name.txtar")

	// The premise: three declarations of the fixture are named Helper, in two
	// files of one package and in a package of its own.
	named := 0
	for _, s := range symbols {
		if s.Name == "Helper" || strings.HasSuffix(s.Name, ".Helper") {
			named++
		}
	}
	if named != 3 {
		t.Fatalf("Setup: the fixture holds %d declarations named Helper, want 3", named)
	}

	cases := []struct {
		name   string
		symbol string
		path   string
		want   graph.SymbolID
	}{
		{"the function the entry names", "go://example.com/samename#Helper", "catalog.go", "catalog.go:6:6"},
		{"the method of another file", "go://example.com/samename#Queue.Helper", "other.go", "other.go:7:16"},
		{"the function of another package", "go://example.com/samename/queue#Helper", "queue/queue.go", "queue/queue.go:4:6"},
		{"the right symbol under the wrong path", "go://example.com/samename#Helper", "other.go", ""},
		{"the right symbol under another package's path", "go://example.com/samename#Helper", "queue/queue.go", ""},
		{"a name with no reference of its own", "go://example.com/samename#Absent", "catalog.go", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			document := fmt.Sprintf(`{"ignore": [{"code": "DS1001", "symbol": %q, "path": %q, "reason": "one entry names one symbol."}]}`,
				c.symbol, c.path)
			records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), symbols)
			if err != nil {
				t.Fatalf("IgnoreFile(%s, %s) error: %v", c.symbol, c.path, err)
			}
			if len(refusals) != 0 {
				t.Fatalf("IgnoreFile(%s, %s) returned %d refusals, want none: %+v", c.symbol, c.path, len(refusals), refusals)
			}
			if len(records) != 1 {
				t.Fatalf("IgnoreFile(%s, %s) returned %d records, want 1: %+v", c.symbol, c.path, len(records), records)
			}
			if got := records[0].Bound; got != c.want {
				t.Errorf("IgnoreFile(%s, %s) bound %q, want %q", c.symbol, c.path, got, c.want)
			}
		})
	}
}

func TestIgnoreFileRefusesAnEntryNamingASymbolAndNoPath(t *testing.T) {
	const document = `{"ignore": [{"code": "DS1001", "symbol": "go://example.com/samename#Helper", "reason": "no path at all."}]}`
	_, _, symbols := loaded(t, "same-name.txtar")

	records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), symbols)
	if err != nil {
		t.Fatalf("IgnoreFile error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("IgnoreFile returned %d records for an unscoped entry, want none: %+v", len(records), records)
	}
	if len(refusals) != 1 {
		t.Fatalf("IgnoreFile returned %d refusals, want 1: %+v", len(refusals), refusals)
	}
	want := Refusal{
		Reported:  codeUnscoped,
		Code:      "DS1001",
		Symbol:    "go://example.com/samename#Helper",
		Reason:    "no path at all.",
		Site:      refusals[0].Site,
		Mechanism: MechanismIgnore,
	}
	if refusals[0] != want {
		t.Errorf("IgnoreFile refusal = %+v, want %+v", refusals[0], want)
	}
}

func TestIgnoreFileRefusesAnEntryLackingBothItsReasonAndItsPath(t *testing.T) {
	const document = `{"ignore": [{"code": "DS1001", "symbol": "go://example.com/app#Catalog"}]}`
	records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), nil)
	if err != nil {
		t.Fatalf("IgnoreFile error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("IgnoreFile returned %d records, want none: %+v", len(records), records)
	}
	reported := make([]string, 0, len(refusals))
	for _, r := range refusals {
		reported = append(reported, r.Reported)
	}
	if want := []string{codeNoReason, codeUnscoped}; !slices.Equal(reported, want) {
		t.Errorf("IgnoreFile reported %v, want %v: each rule reports the field it is about", reported, want)
	}
}

func TestIgnoreFileNamesThePositionOfTheBraceThatOpensEachEntry(t *testing.T) {
	// The first entry's reason carries a character outside ASCII and one outside
	// the basic multilingual plane, and the second entry opens on the same line,
	// so the column it renders at differs in all three units: 96 counting UTF-16
	// code units, 100 counting bytes and 95 counting code points.
	const document = `{
  "ignore": [
    {"code": "DS1001", "symbol": "go://example.com/app#A", "path": "a.go", "reason": "éé🙂."}, {"code": "DS1002", "symbol": "go://example.com/app#B", "path": "b.go", "reason": "two."},
        {"code": "DS1003", "symbol": "go://example.com/app#C", "path": "c.go", "reason": "three."}
  ]
}
`
	records, _, err := IgnoreFile(writeIgnoreFile(t, document), nil)
	if err != nil {
		t.Fatalf("IgnoreFile error: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("IgnoreFile returned %d records, want 3: %+v", len(records), records)
	}

	cases := []struct {
		code string
		line int
		col  int
	}{
		{"DS1001", 3, 5},
		{"DS1002", 3, 96},
		{"DS1003", 4, 9},
	}
	for i, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			at := records[i].Site
			if at.Filename != IgnoreFileName || at.Line != c.line || at.Column != c.col {
				t.Errorf("IgnoreFile record %s sits at %s, want %s:%d:%d", c.code, at, IgnoreFileName, c.line, c.col)
			}
		})
	}
}

func TestIgnoreFileAbsentIsAnEmptyOne(t *testing.T) {
	records, refusals, err := IgnoreFile(filepath.Join(t.TempDir(), IgnoreFileName), nil)
	if err != nil {
		t.Fatalf("IgnoreFile over an absent document error = %v, want none", err)
	}
	if records != nil || refusals != nil {
		t.Errorf("IgnoreFile over an absent document returned %d records and %d refusals, want none of either", len(records), len(refusals))
	}
}

func TestIgnoreFileRefusesTheDocumentsTheGrammarRefuses(t *testing.T) {
	cases := map[string]string{
		"a member written twice in one entry": `{"ignore": [{"code": "DS1001", "code": "DS1002", "symbol": "go://example.com/app#A", "path": "a.go", "reason": "twice."}]}`,
		"a member written twice at the top":   `{"ignore": [], "ignore": []}`,
		"a key outside the closed list":       `{"ignore": [], "baseline": []}`,
		"a key outside an entry's list":       `{"ignore": [{"code": "DS1001", "line": 214, "symbol": "go://example.com/app#A", "path": "a.go", "reason": "keyed."}]}`,
		"no ignore array":                     `{"description": "nothing to adjudicate."}`,
		"an ignore array of the wrong type":   `{"ignore": {"code": "DS1001"}}`,
		"a second value after the document":   `{"ignore": []} {"ignore": []}`,
		"a document that is not an object":    `[{"code": "DS1001"}]`,
		"a comment, which strict JSON has no": "{\n  // the reason lives in the entry\n  \"ignore\": []\n}",
		"a trailing comma":                    `{"ignore": [],}`,
		"an entry that is not an object":      `{"ignore": ["DS1001"]}`,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), nil)
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("IgnoreFile(%s) error = %v, want one satisfying errors.Is(err, ErrMalformed)", document, err)
			}
			if records != nil || refusals != nil {
				t.Errorf("IgnoreFile(%s) returned %d records and %d refusals with its error, want none of either", document, len(records), len(refusals))
			}
			var malformed *MalformedError
			if !errors.As(err, &malformed) {
				t.Fatalf("IgnoreFile(%s) error = %v, want one errors.As reads as *MalformedError", document, err)
			}
			if malformed.Site.Filename != IgnoreFileName || malformed.Mechanism != MechanismIgnore {
				t.Errorf("IgnoreFile(%s) refused at %s under mechanism %s, want %s and %s",
					document, malformed.Site, malformed.Mechanism, IgnoreFileName, MechanismIgnore)
			}
		})
	}
}

func TestIgnoreFileRefusesADocumentAboveTheSizeBound(t *testing.T) {
	document := `{"description": "` + strings.Repeat("a", maxDocumentBytes) + `", "ignore": []}`
	_, _, err := IgnoreFile(writeIgnoreFile(t, document), nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("IgnoreFile over a document of %d bytes error = %v, want one satisfying errors.Is(err, ErrTooLarge)", len(document), err)
	}
}

func TestIgnoreFileReportsADocumentItCannotRead(t *testing.T) {
	_, _, err := IgnoreFile(t.TempDir(), nil)
	if err == nil {
		t.Fatal("IgnoreFile over a directory error = nil, want one naming the path")
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrMalformed) {
		t.Errorf("IgnoreFile over a directory error = %v, want neither an absent document nor a malformed one", err)
	}
}
