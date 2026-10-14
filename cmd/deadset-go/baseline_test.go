package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/reporttest"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	spec "github.com/cplieger/deadset-spec/v8"
)

// baselineVectorRoot is where the pinned Contract publishes the baseline vectors.
const baselineVectorRoot = "vectors/baseline"

// baselineVectorRow is one row of a case's expected baseline, by its four members.
type baselineVectorRow struct {
	Code   string `json:"code"`
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// TestPublishedBaselineVectors records the rows of one round from every published
// case's report and writes them as this analyzer writes a baseline, over the
// document the case places at the target root where it has one. The rows are
// compared with the case's expected baseline row by row, in order and member for
// member, then the document byte for byte, or the write is refused with the case's
// exit code and leaves the document as it was.
func TestPublishedBaselineVectors(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(spec.Vectors, baselineVectorRoot)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", baselineVectorRoot, err)
	}
	var cases []string
	for _, entry := range entries {
		if entry.IsDir() {
			cases = append(cases, entry.Name())
		}
	}
	if len(cases) == 0 {
		t.Fatalf("Setup: %s holds no case, so this test pins nothing", baselineVectorRoot)
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runBaselineVector(t, path.Join(baselineVectorRoot, name))
		})
	}
}

// runBaselineVector runs one published baseline case.
func runBaselineVector(t *testing.T, dir string) {
	t.Helper()

	body := vectorFile(t, dir, "report.json")
	envelope := reporttest.MustRead(t, dir+"/report.json", body)
	if len(envelope.Analyzer.Languages) != 1 {
		t.Fatalf("Setup: %s/report.json names the languages %v, want the one language of the round", dir, envelope.Analyzer.Languages)
	}
	existing, expected, refused := vectorFile(t, dir, "existing.json"), vectorFile(t, dir, "expected.json"), vectorFile(t, dir, "expected-error.json")
	if (expected == nil) == (refused == nil) || (refused != nil && existing == nil) {
		t.Fatalf("Setup: %s carries existing.json = %t, expected.json = %t and expected-error.json = %t, "+
			"want exactly one of the last two, and existing.json beside expected-error.json",
			dir, existing != nil, expected != nil, refused != nil)
	}
	target := filepath.Join(t.TempDir(), suppress.BaselineFileName)
	if existing != nil {
		if err := os.WriteFile(target, existing, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", target, err)
		}
	}

	rows, _ := recordRound(envelope.Findings, nil, nil)
	provenance := suppress.Provenance{Analyzer: envelope.Analyzer.Name, Version: envelope.Analyzer.Version}
	err := writeBaseline(target, rows, provenance, envelope.Analyzer.Languages[0]+"://")
	written, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read the baseline %s: %v", target, readErr)
	}
	if refused != nil {
		var want struct {
			ExitCode int `json:"exit_code"`
		}
		if err := json.Unmarshal(refused, &want); err != nil {
			t.Fatalf("Setup: decode %s/expected-error.json: %v", dir, err)
		}
		if err == nil || want.ExitCode != exitFailure {
			t.Errorf("the write over %s/existing.json = %v, ending the run with %d, want it refused with exit code %d",
				dir, err, exitFailure, want.ExitCode)
		}
		if !bytes.Equal(written, existing) {
			t.Errorf("the refused write over %s/existing.json left\n%s\nwant the document as it was\n%s", dir, written, existing)
		}
		return
	}
	if err != nil {
		t.Fatalf("the write of %s = %v, want the document", dir, err)
	}
	if got, want := vectorRowMembers(t, "the written baseline", written), vectorRowMembers(t, dir+"/expected.json", expected); !reflect.DeepEqual(got, want) {
		t.Errorf("the rows one round records from %s = %+v, want %+v", dir, got, want)
	}
	if !bytes.Equal(written, expected) {
		t.Errorf("the baseline one round writes from %s =\n%s\nwant the bytes of %s/expected.json\n%s", dir, written, dir, expected)
	}
}

// vectorFile reads one file of a published baseline case, or nil when the case has
// none.
func vectorFile(t *testing.T, dir, name string) []byte {
	t.Helper()

	body, err := spec.Vectors.ReadFile(path.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("Setup: read %s/%s: %v", dir, name, err)
	}
	return body
}

// vectorRowMembers decodes the rows of one baseline document, each by the members it
// holds, so a member a row leaves out compares as left out.
func vectorRowMembers(t *testing.T, what string, body []byte) []map[string]any {
	t.Helper()

	var document struct {
		Description string           `json:"description"`
		Baseline    []map[string]any `json:"baseline"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode %s: %v", what, err)
	}
	if document.Baseline == nil {
		return []map[string]any{}
	}
	return document.Baseline
}

// baselineVectorRows decodes the rows of one baseline document.
func baselineVectorRows(t *testing.T, what string, body []byte) []baselineVectorRow {
	t.Helper()

	var document struct {
		Description string              `json:"description"`
		Baseline    []baselineVectorRow `json:"baseline"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode %s: %v", what, err)
	}
	if document.Baseline == nil {
		return []baselineVectorRow{}
	}
	return document.Baseline
}

// baselineFixpointModule is a target whose baseline needs two rounds and leaves a
// document row out: a dead type whose field falls with it until the type's row marks
// it live, an uncalled function whose parameter its body never reads, and a
// configured root that matches nothing.
func baselineFixpointModule(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() {}\n\n" +
			"// spare is used by nothing, and its field is read by nothing.\n" +
			"type spare struct {\n\tfield int\n}\n\n" +
			"// feed is called by nothing and never reads chunk.\n" +
			"func feed(chunk []byte) int {\n\treturn 1\n}\n",
		repositoryDocument: `{"target": {"kind": "application"}, "roots": {"patterns": ["go://example.com/app#Absent"]}}`,
	})
}

func TestAnalyzeWritesTheBaselineFixpointAndNoDocumentRow(t *testing.T) {
	dir := baselineFixpointModule(t)
	baseline := path.Join(dir, suppress.BaselineFileName)
	runAnalyze(t, dir, "--baseline-write="+baseline)

	body, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatalf("read the baseline %s: %v", baseline, err)
	}
	got := baselineVectorRows(t, baseline, body)
	codes := make([]string, len(got))
	for i := range got {
		codes[i] = got[i].Code + " " + got[i].Symbol
	}
	want := []string{
		"DS1002 go://example.com/app#spare",
		"DS1002 go://example.com/app#feed",
		"DS1801 go://example.com/app#feed(chunk)",
		"DS1003 go://example.com/app#spare.field",
	}
	if !slices.Equal(codes, want) {
		t.Errorf("analyze --baseline-write recorded the rows %q, want %q: the rows of every round, the second "+
			"recording the field the first round's row exposed, and none for the configured root that matches nothing",
			codes, want)
	}
}

func TestAnalyzeReadsBackTheBaselineItWroteWithNoStaleRow(t *testing.T) {
	dir := baselineFixpointModule(t)
	baseline := path.Join(dir, suppress.BaselineFileName)
	runAnalyze(t, dir, "--baseline-write="+baseline)

	got := runAnalyze(t, dir)
	envelope := envelopeAt(t, got.reportPath)
	if want := contractExitCodes(t)["findings"]; got.code != want {
		t.Fatalf("analyze over the baseline it wrote = %d, want %d for the root that matches nothing\nstderr: %s",
			got.code, want, got.stderr)
	}
	if len(envelope.StaleSuppressions) != 0 {
		t.Errorf("analyze over the baseline it wrote reports %d stale rows, want none: %+v",
			len(envelope.StaleSuppressions), envelope.StaleSuppressions)
	}
	var reported []string
	for i := range envelope.Findings {
		reported = append(reported, envelope.Findings[i].Code)
	}
	if !slices.Equal(reported, []string{unmatchedRoot}) {
		t.Errorf("analyze over the baseline it wrote reports %v, want only the %s no row can withhold", reported, unmatchedRoot)
	}
}

func TestAnalyzeWritesItsOwnRowsAndKeepsTheOtherSchemesRows(t *testing.T) {
	dir := baselineFixpointModule(t)
	baseline := path.Join(dir, suppress.BaselineFileName)
	const other = `{"code": "DS1301", "symbol": "ts://@example/app/src/a.ts#A.b", "path": "src/a.ts", "reason": "recorded by deadset-ts 7.0.0"}`
	if err := os.WriteFile(baseline, []byte(`{"baseline": [`+other+`]}`), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", baseline, err)
	}
	runAnalyze(t, dir, "--baseline-write="+baseline)

	body, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatalf("read the baseline %s: %v", baseline, err)
	}
	got := baselineVectorRows(t, baseline, body)
	symbols := make([]string, len(got))
	for i := range got {
		symbols[i] = got[i].Symbol
	}
	want := []string{
		"go://example.com/app#spare", "go://example.com/app#feed", "go://example.com/app#feed(chunk)",
		"go://example.com/app#spare.field", "ts://@example/app/src/a.ts#A.b",
	}
	if !slices.Equal(symbols, want) {
		t.Errorf("analyze --baseline-write over a document holding a ts:// row wrote the rows %q, want %q: this analyzer's rows, then the other scheme's as the document held them",
			symbols, want)
	}
}
