package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/report"
	"github.com/cplieger/deadset-go/internal/suppress"
	spec "github.com/cplieger/deadset-spec/v5"
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
// case's report, writes them as this analyzer writes a baseline, and compares the
// rows with the case's expected baseline, row by row and in order.
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
			dir := path.Join(baselineVectorRoot, name)
			body, err := spec.Vectors.ReadFile(path.Join(dir, "report.json"))
			if err != nil {
				t.Fatalf("Setup: read %s/report.json: %v", dir, err)
			}
			var envelope report.Envelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("Setup: decode %s/report.json: %v", dir, err)
			}

			rows, _ := recordRound(envelope.Findings, nil, nil)
			var written bytes.Buffer
			provenance := suppress.Provenance{Analyzer: envelope.Analyzer.Name, Version: envelope.Analyzer.Version}
			if err := suppress.WriteBaseline(&written, rows, provenance); err != nil {
				t.Fatalf("WriteBaseline(%s) = %v, want the document", name, err)
			}

			got := baselineVectorRows(t, "the written baseline", written.Bytes())
			expected, err := spec.Vectors.ReadFile(path.Join(dir, "expected.json"))
			if err != nil {
				t.Fatalf("Setup: read %s/expected.json: %v", dir, err)
			}
			want := baselineVectorRows(t, dir+"/expected.json", expected)
			if !slices.Equal(got, want) {
				t.Errorf("the rows one round records from %s = %+v, want %+v", name, got, want)
			}
		})
	}
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
		"DS1801 go://example.com/app#feed",
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
