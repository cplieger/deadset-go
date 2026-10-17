package suppress

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
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
	outcomeNoReason  entryOutcome = "DS9001"
	outcomeUnscoped  entryOutcome = "DS9002"
	outcomeMalformed entryOutcome = "malformed"

	// outcomeOtherScheme is a well-formed record naming another language's
	// scheme, which this reader binds, counts and reports nowhere.
	outcomeOtherScheme entryOutcome = "another scheme's record"
)

// entryOutcomes maps one rule of the corpus vocabulary to what a reader makes of
// a case under it: the outcome for an accepted case and the outcome for a refused
// one.
func entryOutcomes() map[string][2]entryOutcome {
	return map[string][2]entryOutcome{
		"entry-shape":     {outcomeRecord, outcomeMalformed},
		"row-shape":       {outcomeRecord, outcomeMalformed},
		"reason-required": {outcomeRecord, outcomeNoReason},
		"reason-text":     {outcomeRecord, outcomeNoReason},
		"path-required":   {outcomeRecord, outcomeUnscoped},
		"path-form":       {outcomeRecord, outcomeMalformed},
		"symbol-form":     {outcomeRecord, outcomeMalformed},
		"code-form":       {outcomeRecord, outcomeMalformed},
		"closed-keys":     {outcomeRecord, outcomeMalformed},
		"value-type":      {outcomeRecord, outcomeMalformed},
	}
}

// outcomesOf reads one document and names what the reader made of its one entry:
// one outcome per finding the entry produced, in the order the rules apply, and the
// one outcome of an entry that produced none.
func outcomesOf(t *testing.T, document string) []entryOutcome {
	t.Helper()
	records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), nil)
	switch {
	case err != nil:
		if !errors.Is(err, core.ErrMalformed) {
			t.Fatalf("IgnoreFile(%s) error = %v, want one satisfying errors.Is(err, core.ErrMalformed)", document, err)
		}
		return []entryOutcome{outcomeMalformed}
	case len(refusals) > 0:
		got := make([]entryOutcome, 0, len(refusals))
		for _, refusal := range refusals {
			got = append(got, entryOutcome(refusal.Reported))
		}
		return got
	case len(records) == 0:
		return []entryOutcome{outcomeOtherScheme}
	case len(records) != 1:
		t.Fatalf("IgnoreFile(%s) returned %d records, want one: %+v", document, len(records), records)
	}
	return []entryOutcome{outcomeRecord}
}

// wanted is the outcomes one corpus case declares: the code of every finding it
// names, and the rule's own outcome where it names none. A case naming another
// language's scheme that is not malformed is that scheme's analyzer's to report.
func wanted(c *corpusCase, outcome [2]entryOutcome) []entryOutcome {
	held := wantedOf(c, outcome)
	if !slices.Equal(held, []entryOutcome{outcomeMalformed}) && otherScheme(c.Input) {
		return []entryOutcome{outcomeOtherScheme}
	}
	return held
}

// otherScheme reports whether one case's record names a reference of a scheme
// other than this analyzer's.
func otherScheme(input []byte) bool {
	var record struct {
		Symbol string `json:"symbol"`
	}
	return json.Unmarshal(input, &record) == nil && record.Symbol != "" && !strings.HasPrefix(record.Symbol, OwnScheme)
}

// wantedOf is the outcomes one corpus case declares, read as the analyzer of its
// own language reads them.
func wantedOf(c *corpusCase, outcome [2]entryOutcome) []entryOutcome {
	if len(c.Reports) > 0 {
		reported := make([]entryOutcome, 0, len(c.Reports))
		for _, code := range c.Reports {
			reported = append(reported, entryOutcome(code))
		}
		return reported
	}
	if c.Accepted {
		return []entryOutcome{outcome[0]}
	}
	return []entryOutcome{outcome[1]}
}

func TestIgnoreFileDecidesEveryPublishedEntryCase(t *testing.T) {
	for _, kind := range []string{"ignore-entry", "baseline-row"} {
		for _, c := range readCorpus(t, kind) {
			t.Run(kind+" "+c.Rule+" "+string(c.Input), func(t *testing.T) {
				outcome, declared := entryOutcomes()[c.Rule]
				if !declared {
					t.Fatalf("the corpus names rule %s and no outcome is declared for it: %s", c.Rule, c.Reason)
				}
				want := wanted(&c, outcome)
				if got := outcomesOf(t, documentOf(string(c.Input))); !slices.Equal(got, want) {
					t.Errorf("IgnoreFile over the %s case %s = %v, want %v: %s", kind, c.Input, got, want, c.Reason)
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
		want   inventory.SymbolID
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
		Reported:  vocab.UnscopedIgnoreEntry,
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
	reported := make([]vocab.Code, 0, len(refusals))
	for _, r := range refusals {
		reported = append(reported, r.Reported)
	}
	if want := []vocab.Code{vocab.SuppressionWithoutReason, vocab.UnscopedIgnoreEntry}; !slices.Equal(reported, want) {
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
		code vocab.Code
		line int
		col  int
	}{
		{"DS1001", 3, 5},
		{"DS1002", 3, 96},
		{"DS1003", 4, 9},
	}
	for i, c := range cases {
		t.Run(string(c.code), func(t *testing.T) {
			at := records[i].Site
			if at.Filename != core.IgnoreFileName || at.Line != c.line || at.Column != c.col {
				t.Errorf("IgnoreFile record %s sits at %s, want %s:%d:%d", c.code, at, core.IgnoreFileName, c.line, c.col)
			}
		})
	}
}

func TestIgnoreFileAbsentIsAnEmptyOne(t *testing.T) {
	records, refusals, err := IgnoreFile(filepath.Join(t.TempDir(), core.IgnoreFileName), nil)
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
		"a bracket after the document":        `{"ignore": []}]`,
		"a brace after the document":          `{"ignore": []}}`,
		"a document that is not an object":    `[{"code": "DS1001"}]`,
		"a comment, which strict JSON has no": "{\n  // the reason lives in the entry\n  \"ignore\": []\n}",
		"a trailing comma":                    `{"ignore": [],}`,
		"an entry that is not an object":      `{"ignore": ["DS1001"]}`,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), nil)
			if !errors.Is(err, core.ErrMalformed) {
				t.Fatalf("IgnoreFile(%s) error = %v, want one satisfying errors.Is(err, core.ErrMalformed)", document, err)
			}
			if records != nil || refusals != nil {
				t.Errorf("IgnoreFile(%s) returned %d records and %d refusals with its error, want none of either", document, len(records), len(refusals))
			}
			var malformed *core.MalformedError
			if !errors.As(err, &malformed) {
				t.Fatalf("IgnoreFile(%s) error = %v, want one errors.As reads as *core.MalformedError", document, err)
			}
			if malformed.Site.Path != core.IgnoreFileName || malformed.Mechanism != core.Ignore {
				t.Errorf("IgnoreFile(%s) refused at %+v under mechanism %s, want %s and %s",
					document, malformed.Site, malformed.Mechanism, core.IgnoreFileName, core.Ignore)
			}
		})
	}
}

func TestIgnoreFileReportsADocumentItCannotRead(t *testing.T) {
	_, _, err := IgnoreFile(t.TempDir(), nil)
	if err == nil {
		t.Fatal("IgnoreFile over a directory error = nil, want one naming the path")
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, core.ErrMalformed) {
		t.Errorf("IgnoreFile over a directory error = %v, want neither an absent document nor a malformed one", err)
	}
}

func TestIgnoreFileLeavesEveryRecordOfAnotherSchemeToItsOwnAnalyzer(t *testing.T) {
	document := `{"ignore": [
  {"code": "DS1002", "symbol": "go://example.com/app#helper", "path": "main.go", "reason": "Go's own."},
  {"code": "DS1003", "symbol": "ts://@example/app/src/a.ts#A.b", "path": "src/a.ts", "reason": "The TypeScript analyzer's."},
  {"code": "DS1003", "symbol": "ts://@example/app/src/a.ts#A.c", "path": "src/a.ts"},
  {"code": "DS1003", "symbol": "ts://@example/app/src/a.ts#A.d", "reason": "Unscoped, and still not this analyzer's."}
]}`
	records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), nil)
	if err != nil {
		t.Fatalf("IgnoreFile(a document of two schemes) error = %v, want the records of this analyzer's scheme", err)
	}
	if len(records) != 1 || records[0].Symbol != "go://example.com/app#helper" {
		t.Errorf("IgnoreFile(a document of two schemes) records = %+v, want the one go:// record", records)
	}
	if len(refusals) != 0 {
		t.Errorf("IgnoreFile(a document of two schemes) refusals = %+v, want none: the other scheme's are its analyzer's to report", refusals)
	}

	malformed := `{"ignore": [{"code": "DS13", "symbol": "ts://@example/app/src/a.ts#A.b", "path": "src/a.ts", "reason": "r"}]}`
	if _, _, err := IgnoreFile(writeIgnoreFile(t, malformed), nil); !errors.Is(err, core.ErrMalformed) {
		t.Errorf("IgnoreFile(a malformed record of another scheme) error = %v, want core.ErrMalformed: every record is form-checked", err)
	}
}

// A dependency of a manifest nested below the target root in a package with no
// name is spelled with the manifest's path, as a source file of that package is, and
// an entry copying the reference reads as the other analyzer's record.
func TestIgnoreFileReadsTheReferenceOfANestedManifestsDependency(t *testing.T) {
	for _, symbol := range []string{
		"ts://./static-src/package.json#html-validate:dev-dependency",
		"ts://./package.json#html-validate:dev-dependency",
		"ts://@example/app/package.json#lodash:dependency",
	} {
		t.Run(symbol, func(t *testing.T) {
			document := `{"ignore": [{"code": "DS1701", "symbol": "` + symbol + `", "path": "static-src/package.json", "reason": "Run by hand."}]}`
			records, refusals, err := IgnoreFile(writeIgnoreFile(t, document), nil)
			if err != nil || len(records) != 0 || len(refusals) != 0 {
				t.Errorf("IgnoreFile(an entry naming %s) = %+v, %+v, %v, want the record left to its analyzer", symbol, records, refusals, err)
			}
		})
	}
	for _, symbol := range []string{
		"ts://./static-src#html-validate:dev-dependency",
		"ts://./static-src/package.json/#html-validate:dev-dependency",
		"ts://./static-src/manifest.json#html-validate:dev-dependency",
	} {
		t.Run(symbol, func(t *testing.T) {
			document := `{"ignore": [{"code": "DS1701", "symbol": "` + symbol + `", "path": "static-src/package.json", "reason": "Run by hand."}]}`
			if _, _, err := IgnoreFile(writeIgnoreFile(t, document), nil); !errors.Is(err, core.ErrMalformed) {
				t.Errorf("IgnoreFile(an entry naming %s) error = %v, want core.ErrMalformed: the scope ends in package.json", symbol, err)
			}
		})
	}
}

// An entry's path takes every character a file name can hold but a control
// character, so a route file's path is read and one holding a tab is refused.
func TestIgnoreFileReadsAPathOfAnyFileNameButOneHoldingAControlCharacter(t *testing.T) {
	entry := func(path string) string {
		return `{"ignore": [{"code": "DS1002", "symbol": "ts://./src/routes/(app)/+layout.svelte.ts#load", "path": "` + path + `", "reason": "Run by hand."}]}`
	}
	if _, _, err := IgnoreFile(writeIgnoreFile(t, entry("src/routes/(app)/[id]/$page café.ts")), nil); err != nil {
		t.Errorf("IgnoreFile(a route file's path) error = %v, want the entry read", err)
	}
	if _, _, err := IgnoreFile(writeIgnoreFile(t, entry(`src/a\tb.ts`)), nil); !errors.Is(err, core.ErrMalformed) {
		t.Errorf("IgnoreFile(a path holding a tab) error = %v, want core.ErrMalformed", err)
	}
}

// A refused document is refused with the shared reader's own error, so every
// product names one defect in one wording.
func TestIgnoreFileRefusesWithTheSharedReadersError(t *testing.T) {
	const document = `{"ignore": [{"symbol": "go://example.com/app#A", "path": "a.go", "reason": "no code."}]}`
	_, want := core.ReadIgnore([]byte(document))
	if _, _, err := IgnoreFile(writeIgnoreFile(t, document), nil); err == nil || want == nil || err.Error() != want.Error() {
		t.Errorf("IgnoreFile(an entry naming no code) = %v, want the shared reader's %v", err, want)
	}
}
