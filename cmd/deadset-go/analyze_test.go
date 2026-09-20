package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/report"
	spec "github.com/cplieger/deadset-spec/v2"
)

// analyzed is one run of the analyze verb over dir: the verb is invoked from the
// target, because a report names every path relative to the directory the run was
// invoked from and can name none outside it, and the report is written outside the
// target so the run leaves the tree alone.
//
// A test that calls this is sequential, because the working directory is the
// process's.
type analyzed struct {
	reportPath string
	stderr     string
	code       int
}

// runAnalyze invokes the verb over dir with the flags given, with --target and
// --report supplied, and returns what it wrote and the code it returned.
func runAnalyze(t *testing.T, dir string, args ...string) analyzed {
	t.Helper()

	t.Chdir(dir)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	var stdout, stderr bytes.Buffer
	invoked := append([]string{"analyze", "--target=.", "--report=" + reportPath}, args...)
	code := run(t.Context(), invoked, &stdout, &stderr)
	// The verb writes nothing to stdout: the report is a file and the verdict is
	// the code, so a stream carries neither.
	if stdout.Len() != 0 {
		t.Errorf("analyze wrote %q to stdout, want nothing: the report is the file --report names", stdout.String())
	}
	return analyzed{reportPath: reportPath, stderr: stderr.String(), code: code}
}

// envelopeAt reads the report one run wrote, refusing a member the report package
// does not declare.
func envelopeAt(t *testing.T, path string) report.Envelope {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the report %s: %v", path, err)
	}
	var envelope report.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode the report %s: %v", path, err)
	}
	return envelope
}

// liveModule is a module the analysis reports nothing about: the entry point
// reaches every declaration.
func liveModule(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { used() }\n\n" +
			"// used is what the entry point calls.\nfunc used() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
}

// everyKindAllowed is a repository configuration allowing every family of the
// vocabulary a configuration may name. The self-check family is absent because the
// Contract fixes the severity of a kind inside it, so a key naming that family is
// refused; every kind a finding of this fixture could carry is in one of the
// families listed.
const everyKindAllowed = `{
  "target": {"kind": "application"},
  "severity": {
    "DS10": "allow", "DS11": "allow", "DS12": "allow",
    "DS13": "allow", "DS15": "allow", "DS16": "allow", "DS18": "allow"
  }
}`

// edgedModule is the findings fixture with an edge document pairing one of its dead
// declarations with the TypeScript generated from it, which is what makes the
// report pending.
func edgedModule(t *testing.T) string {
	t.Helper()

	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	writeDocument(t, dir, "deadset-edges.json", `{
  "description": "One edge pairing a dead Go declaration with the TypeScript generated from it.",
  "edges": [
    {
      "id": "wire/forgotten",
      "because": "generated",
      "provides": "go://example.com/app#forgotten",
      "used_by": "ts://@example/app/src/wire.ts#forgotten"
    }
  ]
}
`)
	return dir
}

func TestAnalyzeExitsWithTheCodeTheContractsTableGivesTheRun(t *testing.T) {
	codes := contractExitCodes(t)
	tests := []struct {
		name       string
		module     func(t *testing.T) string
		args       []string
		wantStderr []string
		want       int
		wantReport bool
	}{
		{
			name:       "a_module_the_analysis_reports_nothing_about_is_clean",
			module:     liveModule,
			want:       codes["clean"],
			wantReport: true,
		},
		{
			name:       "a_finding_at_the_failing_severity_is_a_finding_run",
			module:     func(t *testing.T) string { return findingsFixture(t, `{"target": {"kind": "application"}}`) },
			want:       codes["findings"],
			wantReport: true,
		},
		{
			name:       "a_stale_suppression_is_a_finding_run_whatever_the_severity_map_allows",
			module:     func(t *testing.T) string { return suppressedModuleWith(t, everyKindAllowed) },
			wantStderr: []string{"1 stale suppression"},
			want:       codes["findings"],
			wantReport: true,
		},
		{
			name:       "an_invocation_this_verb_cannot_serve_is_a_usage_error",
			module:     liveModule,
			args:       []string{"--exit-code=maybe"},
			wantStderr: []string{`--exit-code="maybe"`, "the values are on and off"},
			want:       codes["usage"],
		},
		{
			name:   "a_target_that_does_not_type-check_is_a_failure",
			module: brokenModule,
			// A run that produced no answer prints the load errors and no
			// finding list, so it writes no report either.
			wantStderr: []string{"undefined: missing"},
			want:       codes["failure"],
		},
		{
			name:       "a_pending_finding_outranks_the_findings_the_run_holds",
			module:     edgedModule,
			wantStderr: []string{"1 pending finding", "no merge has resolved it"},
			want:       codes["pending"],
			wantReport: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runAnalyze(t, tc.module(t), tc.args...)
			if got.code != tc.want {
				t.Errorf("analyze over %s = %d, want %d\nstderr: %s", tc.name, got.code, tc.want, got.stderr)
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(got.stderr, want) {
					t.Errorf("analyze stderr = %q, want it to contain %q", got.stderr, want)
				}
			}
			_, err := os.Stat(got.reportPath)
			if tc.wantReport && err != nil {
				t.Errorf("analyze wrote no report at %s, want the report of the run: %v", got.reportPath, err)
			}
			if !tc.wantReport && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("analyze left a file at %s, want none: a run that produced no answer writes no report", got.reportPath)
			}
		})
	}
}

// suppressedModuleWith is the suppression fixture under one repository
// configuration: two ignore entries, of which one binds a finding of the run and
// one names a declaration nothing declares.
func suppressedModuleWith(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, suppressedArchive(document))
}

// brokenModule is a module that does not type-check, which is the run that produces
// no answer.
func brokenModule(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"app.go":           "package main\n\nfunc main() { missing() }\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
}

func TestAnalyzeExitCodeConfiguredOffWritesTheReportAndNamesTheVerdict(t *testing.T) {
	codes := contractExitCodes(t)
	got := runAnalyze(t, edgedModule(t), "--exit-code=off")

	if want := codes["clean"]; got.code != want {
		t.Errorf("analyze --exit-code=off = %d, want %d\nstderr: %s", got.code, want, got.stderr)
	}
	for _, want := range []string{"1 pending finding", "the verdict of this run is 4"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("analyze --exit-code=off stderr = %q, want it to contain %q", got.stderr, want)
		}
	}
	envelope := envelopeAt(t, got.reportPath)
	if envelope.Totals.Pending != 1 {
		t.Errorf("the report written with the exit code off counts %d pending findings, want 1", envelope.Totals.Pending)
	}
}

func TestAnalyzeWritesEveryRenderingBesideTheReportItNames(t *testing.T) {
	got := runAnalyze(t, findingsFixture(t, `{"target": {"kind": "application"}}`),
		"--format=text", "--format=json", "--format=github", "--format=sarif")

	for _, suffix := range []string{".txt", ".json", ".annotations", ".sarif"} {
		path := got.reportPath + suffix
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the rendering %s: %v", path, err)
		}
		if len(body) == 0 {
			t.Errorf("the rendering %s is empty, want the rendering of the run's findings", path)
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm() != documentMode {
			t.Errorf("the rendering %s carries mode %v, want %v", path, info.Mode().Perm(), fs.FileMode(documentMode))
		}
	}

	// Every rendering reads the one ordered finding list, so the text rendering
	// names the same declarations the JSON report does, in the same order.
	envelope := envelopeAt(t, got.reportPath)
	text, err := os.ReadFile(got.reportPath + ".txt")
	if err != nil {
		t.Fatalf("read the text rendering: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(text), "\n"), "\n")
	if len(lines) != len(envelope.Findings)+1 {
		t.Fatalf("the text rendering holds %d lines, want %d findings and one summary",
			len(lines), len(envelope.Findings))
	}
	for i := range envelope.Findings {
		if !strings.HasPrefix(lines[i], envelope.Findings[i].Position.Path) {
			t.Errorf("text line %d is %q, want it to start with %q as the report's finding %d does",
				i+1, lines[i], envelope.Findings[i].Position.Path, i+1)
		}
	}
}

func TestAnalyzeRenderingsAreTheConfiguredFormatsWhereTheInvocationNamesNone(t *testing.T) {
	got := runAnalyze(t, findingsFixture(t,
		`{"target": {"kind": "application"}, "reporters": {"formats": ["sarif"]}}`))

	if _, err := os.Stat(got.reportPath + ".sarif"); err != nil {
		t.Errorf("analyze wrote no SARIF rendering, want the one reporters.formats names: %v", err)
	}
	if _, err := os.Stat(got.reportPath + ".txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("analyze wrote a text rendering, want only the format the configuration names: %v", err)
	}
}

func TestAnalyzeRenderingsAreTheInvocationsWhereItNamesAny(t *testing.T) {
	got := runAnalyze(t, findingsFixture(t,
		`{"target": {"kind": "application"}, "reporters": {"formats": ["sarif"]}}`), "--format=text")

	if _, err := os.Stat(got.reportPath + ".txt"); err != nil {
		t.Errorf("analyze wrote no text rendering, want the one the invocation named: %v", err)
	}
	if _, err := os.Stat(got.reportPath + ".sarif"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("analyze wrote the configured SARIF rendering, want the flag to outrank the source: %v", err)
	}
}

func TestAnalyzeTemplateRenderingReadsTheTemplateTheInvocationNames(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	templatePath := writeDocument(t, dir, "one-line.tmpl",
		"{{range .Findings}}{{.Code}} {{.Symbol.Ref}}\n{{end}}")
	got := runAnalyze(t, dir, "--format=template", "--template="+templatePath)

	body, err := os.ReadFile(got.reportPath + ".tmpl")
	if err != nil {
		t.Fatalf("read the template rendering: %v", err)
	}
	if want := "DS1002 go://example.com/app#forgotten\n"; !strings.Contains(string(body), want) {
		t.Errorf("the template rendering is %q, want it to contain %q", body, want)
	}
}

func TestAnalyzeRefusesTheTemplateRenderingWithNoTemplate(t *testing.T) {
	got := runAnalyze(t, findingsFixture(t, `{"target": {"kind": "application"}}`), "--format=template")

	if want := contractExitCodes(t)["usage"]; got.code != want {
		t.Errorf("analyze --format=template with no --template = %d, want %d", got.code, want)
	}
	if !strings.Contains(got.stderr, "--template names") {
		t.Errorf("analyze stderr = %q, want it to name the flag the rendering reads", got.stderr)
	}
	if _, err := os.Stat(got.reportPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("analyze wrote a report before refusing the invocation, want the refusal to cost no analysis: %v", err)
	}
}

func TestAnalyzeWritesTheBaselineOfEveryFindingOfTheRun(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	baseline := filepath.Join(t.TempDir(), "deadset-baseline.json")
	got := runAnalyze(t, dir, "--baseline-write="+baseline, "--max-findings=1")

	body, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatalf("read the baseline %s: %v", baseline, err)
	}
	var document struct {
		Baseline []struct {
			Code   string `json:"code"`
			Symbol string `json:"symbol"`
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"baseline"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the baseline %s: %v", baseline, err)
	}

	// The maximum finding count bounds what a rendering prints and not what the
	// baseline records: a baseline missing a finding this run reported would fail
	// the next run on it.
	envelope := envelopeAt(t, got.reportPath)
	if len(envelope.Findings) != 1 {
		t.Fatalf("the capped report prints %d findings, want 1", len(envelope.Findings))
	}
	if got, want := len(document.Baseline), envelope.Totals.Findings; got != want {
		t.Errorf("the baseline records %d rows, want %d, the findings of the whole run", got, want)
	}
	for _, row := range document.Baseline {
		if row.Code == "" || row.Symbol == "" || row.Path == "" || row.Reason == "" {
			t.Errorf("the baseline row %+v leaves a required field empty", row)
		}
		if !strings.Contains(row.Reason, name) || !strings.Contains(row.Reason, version()) {
			t.Errorf("the baseline row reason is %q, want the provenance of the run that recorded it", row.Reason)
		}
	}
}

func TestAnalyzeWritesNoPartialDocumentWhereThePublishCannotHappen(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	t.Chdir(dir)

	// The report path names a file under a path that is a regular file, so the
	// temporary file the write goes through cannot be created and the rename that
	// publishes the document never happens.
	held := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(held, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", held, err)
	}
	reportPath := filepath.Join(held, "report.json")

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"analyze", "--target=.", "--report=" + reportPath}, &stdout, &stderr)
	if want := contractExitCodes(t)["failure"]; code != want {
		t.Errorf("analyze with a report path no write can publish = %d, want %d\nstderr: %s", code, want, stderr.String())
	}
	if !strings.Contains(stderr.String(), reportPath) {
		t.Errorf("analyze stderr = %q, want it to name the path it could not write", stderr.String())
	}
	// The path is not reachable at all, so the check is that nothing stands
	// there: a parent that is a regular file answers ENOTDIR rather than ENOENT.
	if _, err := os.Stat(reportPath); err == nil {
		t.Errorf("analyze left a file at %s, want none: a write that cannot publish leaves no document", reportPath)
	}
	// The temporary file the write would have gone through is not left behind
	// either, so a failed run adds nothing to the directory it wrote in.
	entries, err := os.ReadDir(filepath.Dir(held))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Dir(held), err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries after the failed write, want 1: the occupied path alone", len(entries))
	}
}

func TestAnalyzeWritesNoPartialDocumentWhereTheRenameIsRefused(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	t.Chdir(dir)

	// The report path names a directory that is not empty, so the temporary file is
	// created and written and the rename that would publish it is the step the
	// filesystem refuses.
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.MkdirAll(filepath.Join(reportPath, "occupied"), 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", reportPath, err)
	}

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"analyze", "--target=.", "--report=" + reportPath}, &stdout, &stderr)
	if want := contractExitCodes(t)["failure"]; code != want {
		t.Errorf("analyze with a report path the rename cannot take = %d, want %d\nstderr: %s",
			code, want, stderr.String())
	}
	if !strings.Contains(stderr.String(), "publish "+reportPath) {
		t.Errorf("analyze stderr = %q, want it to name the publish it could not make", stderr.String())
	}
	// The directory at the report path is untouched and the temporary file the
	// write went through is gone, so the refused write leaves nothing behind.
	entries, err := os.ReadDir(filepath.Dir(reportPath))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Dir(reportPath), err)
	}
	if len(entries) != 1 || entries[0].Name() != "report.json" {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Errorf("the directory holds %v after the refused write, want the report path alone", names)
	}
}

func TestWriteAtomicallyLeavesNoTemporaryFileBehind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "document.json")
	if err := writeAtomically(path, func(w io.Writer) error {
		_, err := w.Write([]byte("{}\n"))
		return err
	}); err != nil {
		t.Fatalf("writeAtomically(%s) = error %v, want the document written", path, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 1 || entries[0].Name() != "document.json" {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Errorf("the directory holds %v after the write, want the document alone", names)
	}

	// A rendering that fails leaves the path as it was and no temporary file.
	failing := errors.New("the rendering refused")
	if err := writeAtomically(path, func(io.Writer) error { return failing }); !errors.Is(err, failing) {
		t.Errorf("writeAtomically() with a failing rendering = %v, want the rendering's own error", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got, want := string(body), "{}\n"; got != want {
		t.Errorf("the document at %s is %q after a failed write, want %q: the previous document stands", path, got, want)
	}
	if entries, err = os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Errorf("the directory holds %d entries after the failed write, want 1", len(entries))
	}
}

func TestFormatListNamesEveryFormatTheContractDeclares(t *testing.T) {
	t.Parallel()

	body, err := spec.Contract.ReadFile("contract/config.schema.json")
	if err != nil {
		t.Fatalf("Setup: read contract/config.schema.json: %v", err)
	}
	var document struct {
		Properties struct {
			Reporters struct {
				Properties struct {
					Formats struct {
						Items struct {
							Enum []string `json:"enum"`
						} `json:"items"`
					} `json:"formats"`
				} `json:"properties"`
			} `json:"reporters"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/config.schema.json: %v", err)
	}

	published := document.Properties.Reporters.Properties.Formats.Items.Enum
	if len(published) == 0 {
		t.Fatal("Setup: contract/config.schema.json declares no format enum")
	}
	for _, format := range published {
		if _, held := renderings[config.Format(format)]; !held {
			t.Errorf("the Contract declares the format %q and this analyzer renders nothing for it", format)
		}
	}
	if len(renderings) != len(published) {
		t.Errorf("this analyzer renders %d formats and the Contract declares %d (%v)",
			len(renderings), len(published), published)
	}

	// A format the vocabulary does not hold is refused by name, and a format named
	// twice is refused rather than written twice.
	var held formatList
	if err := held.Set("annotations"); err == nil {
		t.Error("formatList.Set(\"annotations\") = nil, want a refusal: the format is named github")
	}
	for _, format := range published {
		if err := held.Set(format); err != nil {
			t.Errorf("formatList.Set(%q) = %v, want the format recorded", format, err)
		}
	}
	if err := held.Set(published[0]); err == nil {
		t.Errorf("formatList.Set(%q) twice = nil, want a refusal", published[0])
	}
	if got := held.String(); !strings.Contains(got, published[0]) {
		t.Errorf("formatList.String() = %q, want it to name %q", got, published[0])
	}
}

func TestSettingValueRendersEachSettingAsItsDeclaredType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setting settingFlag
		value   string
		want    string
		wantErr bool
	}{
		{
			name:    "a_string_setting",
			setting: settingFlags["min-confidence"],
			value:   "probable",
			want:    `"probable"`,
		},
		{
			name:    "a_number_setting",
			setting: settingFlags["max-findings"],
			value:   "20",
			want:    "20",
		},
		{
			name:    "a_number_setting_given_something_that_is_not_one",
			setting: settingFlags["max-findings"],
			value:   "twenty",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := settingValue(&tc.setting, tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("settingValue(%+v, %q) = %v, want an error %t", tc.setting, tc.value, err, tc.wantErr)
			}
			if err == nil && string(got) != tc.want {
				t.Errorf("settingValue(%+v, %q) = %s, want %s", tc.setting, tc.value, got, tc.want)
			}
		})
	}
}

func TestAnalyzeAppliesTheSettingsTheCommandLineSupplies(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}, "reporters": {"max_findings": 0}}`)
	got := runAnalyze(t, dir, "--max-findings=2", "--sort=size", "--fail-on=allow")

	envelope := envelopeAt(t, got.reportPath)
	if len(envelope.Findings) != 2 {
		t.Errorf("the report prints %d findings, want 2: the flag outranks the configuration's zero",
			len(envelope.Findings))
	}
	if got, want := envelope.Totals.Omitted, envelope.Totals.Findings-2; got != want {
		t.Errorf("the report counts %d omitted findings, want %d", got, want)
	}
	// The size order puts the component removing the most lines first, and the
	// canonical order breaks the tie, so the printed pair is sorted by it.
	sizes := make([]int, len(envelope.Findings))
	for i := range envelope.Findings {
		sizes[i] = envelope.Findings[i].Component.DeletableLines
	}
	if !slices.IsSortedFunc(sizes, func(a, b int) int { return b - a }) {
		t.Errorf("the printed findings remove %v lines, want them ordered largest first", sizes)
	}
}

func TestAnalyzeRefusesANumericSettingThatIsNotANumber(t *testing.T) {
	got := runAnalyze(t, liveModule(t), "--max-findings=all")

	if want := contractExitCodes(t)["usage"]; got.code != want {
		t.Errorf("analyze --max-findings=all = %d, want %d", got.code, want)
	}
	if !strings.Contains(got.stderr, "--max-findings") {
		t.Errorf("analyze stderr = %q, want it to name the flag it refused", got.stderr)
	}
}

// consumedTree is a target library, one module that consumes it, and a scope
// document naming both. The consumer references one of the target's two exported
// declarations, so what the report says about the target is not what it says with
// the document unread.
func consumedTree(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"target/go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"target/lib.go": "// Package app exports one declaration a consumer references and one\n" +
			"// nothing references.\npackage app\n\n" +
			"// Used is what the consumer calls.\nfunc Used() string { return \"used\" }\n\n" +
			"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
		"target/" + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}}`,
		"consumer/go.mod": "module example.com/consumer\n\ngo 1.27.1\n\n" +
			"require example.com/app v0.0.0\n\nreplace example.com/app => ../target\n",
		"consumer/main.go": "// Command consumer references one exported declaration of the target.\npackage main\n\n" +
			"import \"example.com/app\"\n\nfunc main() {\n\tif app.Used() == \"\" {\n\t\tpanic(\"the target returned an empty string\")\n\t}\n}\n",
		"scope.json": `{
  "target": {"id": "example.com/app", "role": "target", "path": "target"},
  "consumers": [{"id": "example.com/consumer", "role": "consumer", "path": "consumer"}]
}
`,
	})
}

func TestAnalyzeReadsTheScopeDocumentTheInvocationNames(t *testing.T) {
	dir := consumedTree(t)
	t.Chdir(dir)
	reportPath := filepath.Join(t.TempDir(), "report.json")

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{
		"analyze", "--target=target", "--scope=scope.json", "--report=" + reportPath,
	}, &stdout, &stderr)
	if want := contractExitCodes(t)["findings"]; code != want {
		t.Fatalf("analyze --scope=scope.json = %d, want %d\nstderr: %s", code, want, stderr.String())
	}

	envelope := envelopeAt(t, reportPath)
	if got := envelope.Consumers.Declared; got != 1 {
		t.Errorf("the report declares %d consumers, want 1 as the scope document names", got)
	}
	if got := len(envelope.Consumers.Loaded); got != 1 || envelope.Consumers.Loaded[0].ID != "example.com/consumer" {
		t.Errorf("the report lists %+v as loaded, want the one consumer the document names", envelope.Consumers.Loaded)
	}

	// The declaration the consumer references is live because the consumer was
	// loaded, so the one exported declaration reported is the other.
	var exported []string
	for i := range envelope.Findings {
		if envelope.Findings[i].Code == "DS1001" {
			exported = append(exported, envelope.Findings[i].Symbol.Ref)
		}
	}
	if want := []string{"go://example.com/app#Forgotten"}; !slices.Equal(exported, want) {
		t.Errorf("the report reports %v under DS1001, want %v: the consumer's reference holds the other live", exported, want)
	}
}
