package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	spec "github.com/cplieger/deadset-spec"
)

// TestMain creates the directory the shared fixture modules are written under, runs
// the package's tests, and removes it.
//
// The directory cannot belong to a test: a fixture module is declared by several of
// them and written once, so it outlives whichever one declared it first.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests is TestMain's body, written so that the removal of the fixture directory
// runs before the exit.
func runTests(m *testing.M) int {
	base, err := os.MkdirTemp("", "deadset-fixtures")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create the fixture directory: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(base) }()

	fixtureBase = base
	return m.Run()
}

// exitCode is one row of contract/exit-codes.json.
type exitCode struct {
	Name string `json:"name"`
	Code int    `json:"code"`
}

// contractExitCodes reads the exit codes the Contract publishes, keyed by name.
func contractExitCodes(t *testing.T) map[string]int {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/exit-codes.json")
	if err != nil {
		t.Fatalf("Setup: read contract/exit-codes.json: %v", err)
	}
	var document struct {
		ExitCodes []exitCode `json:"exit_codes"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/exit-codes.json: %v", err)
	}

	codes := make(map[string]int, len(document.ExitCodes))
	for _, entry := range document.ExitCodes {
		codes[entry.Name] = entry.Code
	}
	return codes
}

// contractVersions reads the Contract version and the report schema versions
// contract/contract.json declares.
func contractVersions(t *testing.T) (contract string, schemas []string) {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/contract.json")
	if err != nil {
		t.Fatalf("Setup: read contract/contract.json: %v", err)
	}
	var document struct {
		ContractVersion string   `json:"contract_version"`
		SchemaVersions  []string `json:"schema_versions"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/contract.json: %v", err)
	}
	return document.ContractVersion, document.SchemaVersions
}

// analyzerPattern reads one pattern the report schema declares for a member of
// its analyzer object, so a document this command writes is checked against the
// Contract's own spelling of that member.
func analyzerPattern(t *testing.T, member string) *regexp.Regexp {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/report.schema.json")
	if err != nil {
		t.Fatalf("Setup: read contract/report.schema.json: %v", err)
	}
	var document struct {
		Properties struct {
			Analyzer struct {
				Properties map[string]struct {
					Pattern string `json:"pattern"`
				} `json:"properties"`
			} `json:"analyzer"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/report.schema.json: %v", err)
	}

	pattern := document.Properties.Analyzer.Properties[member].Pattern
	if pattern == "" {
		t.Fatalf("Setup: contract/report.schema.json declares no pattern for analyzer.%s", member)
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("Setup: compile the pattern of analyzer.%s (%q): %v", member, pattern, err)
	}
	return compiled
}

func TestExitCodesEqualTheContract(t *testing.T) {
	t.Parallel()

	codes := contractExitCodes(t)
	published := map[string]int{"clean": 0, "findings": 1, "usage": 2, "failure": 3, "pending": 4}
	if len(codes) != len(published) {
		t.Errorf("contract/exit-codes.json declares %d codes (%v), want %d", len(codes), codes, len(published))
	}
	for name, want := range published {
		if got, ok := codes[name]; !ok || got != want {
			t.Errorf("contract/exit-codes.json code %q = %d (present %t), want %d", name, got, ok, want)
		}
	}

	wired := map[string]int{"clean": exitClean, "findings": exitFindings, "usage": exitUsage, "failure": exitFailure}
	for name, got := range wired {
		if want := codes[name]; got != want {
			t.Errorf("the code this command returns for %q is %d, want %d as contract/exit-codes.json names it", name, got, want)
		}
	}
}

func TestExitCodeForMapsEveryWiredFailure(t *testing.T) {
	t.Parallel()

	codes := contractExitCodes(t)
	loadFailure := &load.Error{
		Configuration: "linux-amd64",
		Diagnostics: []load.Diagnostic{
			{Package: "example.com/app", Position: "app.go:3:6", Message: "undefined: missing"},
			{Package: "example.com/app", Position: "app.go:9:2", Message: "declared and not used: n"},
		},
	}

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "a_malformed_configuration_is_a_usage_error", err: &config.Error{Kind: config.KindMalformed, Key: "reporters.sort", Message: "malformed"}, want: codes["usage"]},
		{name: "an_unimplemented_key_is_a_usage_error", err: &config.Error{Kind: config.KindUnimplementedKey, Key: "reporters.fail_under", Message: "not implemented"}, want: codes["usage"]},
		{name: "a_missing_target_kind_is_a_usage_error", err: &config.Error{Kind: config.KindMissingTargetKind, Key: "target.kind", Message: "not set"}, want: codes["usage"]},
		{name: "a_wrapped_refusal_is_a_usage_error", err: errors.Join(errors.New("resolve"), &config.Error{Kind: config.KindMalformed, Message: "malformed"}), want: codes["usage"]},
		{name: "a_configured_template_directory_the_target_does_not_hold_is_a_usage_error", err: fmt.Errorf("exempt: %s: %w: absent", exempt.TemplateField, exempt.ErrTemplateDir), want: codes["usage"]},
		{name: "a_matrix_holding_no_configuration_is_a_usage_error", err: fmt.Errorf("%w: %s", load.ErrNoConfiguration, "/src/app"), want: codes["usage"]},
		{name: "a_matrix_the_merge_cannot_key_a_configuration_of_is_a_usage_error", err: fmt.Errorf("%w: %d configurations, at most 64", graph.ErrMatrix, 65), want: codes["usage"]},
		{name: "a_load_failure_is_a_failure", err: loadFailure, want: codes["failure"]},
		{name: "a_source_that_names_no_file_or_flag_is_a_failure", err: config.ErrNoSourceLabel, want: codes["failure"]},
		{name: "an_unclassified_error_is_a_failure", err: errors.New("write the resolved configuration"), want: codes["failure"]},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := exitCodeFor(tc.err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}

	// A load failure prints every package error, because the run that met it
	// prints no finding list at all.
	rendered := loadFailure.Error()
	for _, diagnostic := range loadFailure.Diagnostics {
		if !strings.Contains(rendered, diagnostic.Message) {
			t.Errorf("(*load.Error).Error() = %q, want it to name %q", rendered, diagnostic.Message)
		}
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	contractVersion, _ := contractVersions(t)

	tests := []struct {
		name       string
		args       []string
		wantStdout []string
		wantStderr []string
		wantCode   int
	}{
		{
			name:       "version_prints_the_analyzer_and_the_contract_it_implements",
			args:       []string{"version"},
			wantCode:   exitClean,
			wantStdout: []string{"deadset-go " + version + "\n", "contract " + contractVersion + "\n"},
		},
		{
			name:       "no_arguments_is_a_usage_error",
			args:       nil,
			wantCode:   exitUsage,
			wantStderr: []string{"usage: deadset-go", "analyze", "explain", "print-config", "print-roots", "print-retained", "describe", "version"},
		},
		{
			name:       "an_unknown_verb_is_a_usage_error",
			args:       []string{"analyse"},
			wantCode:   exitUsage,
			wantStderr: []string{`unknown verb "analyse"`, "usage: deadset-go"},
		},
		{
			name:       "fix_flag_is_refused_by_name_with_the_usage_text",
			args:       []string{"analyze", "--fix"},
			wantCode:   exitUsage,
			wantStderr: []string{"--fix", "not supported", "never edits a source file", "usage: deadset-go"},
		},
		{
			name:       "rewrite_flag_is_refused_by_name_with_the_usage_text",
			args:       []string{"analyze", "--rewrite"},
			wantCode:   exitUsage,
			wantStderr: []string{"--rewrite", "not supported", "never edits a source file", "usage: deadset-go"},
		},
		{
			name:       "analyze_writes_its_report_to_a_path_the_invocation_names",
			args:       []string{"analyze"},
			wantCode:   exitUsage,
			wantStderr: []string{"--report names", "usage: deadset-go analyze"},
		},
		{
			name:       "analyze_refuses_a_format_it_renders_nothing_for",
			args:       []string{"analyze", "--report=report.json", "--format=annotations"},
			wantCode:   exitUsage,
			wantStderr: []string{`"annotations" is not a format`, "github", "sarif", "template", "text"},
		},
		{
			name:       "analyze_refuses_a_format_named_twice",
			args:       []string{"analyze", "--report=report.json", "--format=text", "--format=text"},
			wantCode:   exitUsage,
			wantStderr: []string{`"text" is named twice`},
		},
		{
			name:       "analyze_takes_no_argument",
			args:       []string{"analyze", "--report=report.json", "."},
			wantCode:   exitUsage,
			wantStderr: []string{`analyze takes no argument, got "."`, "usage: deadset-go analyze"},
		},
		{
			name:       "explain_explains_the_symbol_the_argument_names",
			args:       []string{"explain"},
			wantCode:   exitUsage,
			wantStderr: []string{"no symbol was named", "usage: deadset-go explain"},
		},
		{
			name:       "explain_explains_one_symbol",
			args:       []string{"explain", "--why=go://example.com/app#Catalog", "go://example.com/app#Other"},
			wantCode:   exitUsage,
			wantStderr: []string{"explains one symbol", "the argument, --why"},
		},
		{
			name:       "print_roots_takes_no_argument",
			args:       []string{"print-roots", "."},
			wantCode:   exitUsage,
			wantStderr: []string{`print-roots takes no argument, got "."`, "usage: deadset-go print-roots"},
		},
		{
			name:       "print_roots_refuses_an_undefined_flag",
			args:       []string{"print-roots", "--roots=go://example.com/app#Catalog"},
			wantCode:   exitUsage,
			wantStderr: []string{"flag provided but not defined", "usage: deadset-go print-roots"},
		},
		{
			name:       "print_retained_takes_no_argument",
			args:       []string{"print-retained", "."},
			wantCode:   exitUsage,
			wantStderr: []string{`print-retained takes no argument, got "."`, "usage: deadset-go print-retained"},
		},
		{
			name:       "describe_takes_no_argument",
			args:       []string{"describe", "corpus"},
			wantCode:   exitUsage,
			wantStderr: []string{`describe takes no argument, got "corpus"`},
		},
		{
			name:       "print_config_takes_no_argument",
			args:       []string{"print-config", "."},
			wantCode:   exitUsage,
			wantStderr: []string{`print-config takes no argument, got "."`, "usage: deadset-go print-config"},
		},
		{
			name:       "print_config_refuses_an_undefined_flag",
			args:       []string{"print-config", "--config-file=deadset.json"},
			wantCode:   exitUsage,
			wantStderr: []string{"flag provided but not defined", "usage: deadset-go print-config"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			if got := run(t.Context(), tc.args, &stdout, &stderr); got != tc.wantCode {
				t.Errorf("run(%q) = %d, want %d\nstdout: %q\nstderr: %q", tc.args, got, tc.wantCode, stdout.String(), stderr.String())
			}
			for _, want := range tc.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("run(%q) stdout = %q, want it to contain %q", tc.args, stdout.String(), want)
				}
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("run(%q) stderr = %q, want it to contain %q", tc.args, stderr.String(), want)
				}
			}
			if tc.wantCode == exitClean && stderr.Len() != 0 {
				t.Errorf("run(%q) stderr = %q, want empty on exit 0", tc.args, stderr.String())
			}
			if tc.wantCode != exitClean && stdout.Len() != 0 {
				t.Errorf("run(%q) stdout = %q, want empty on exit %d", tc.args, stdout.String(), tc.wantCode)
			}
		})
	}
}

func TestDescribeNamesTheContractItImplements(t *testing.T) {
	t.Parallel()

	contractVersion, schemaVersions := contractVersions(t)

	var stdout, stderr bytes.Buffer
	if got := run(t.Context(), []string{"describe"}, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(describe) = %d, want %d\nstderr: %q", got, exitClean, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run(describe) stderr = %q, want empty", stderr.String())
	}

	// The document is one JSON object and every member is declared: a member the
	// decode does not know is a member this command should not be writing.
	var described struct {
		Name                   string          `json:"name"`
		Version                string          `json:"version"`
		ContractVersion        string          `json:"contract_version"`
		SchemaVersionsAccepted []string        `json:"schema_versions_accepted"`
		Languages              []string        `json:"languages"`
		Conformance            json.RawMessage `json:"conformance"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&described); err != nil {
		t.Fatalf("decode the describe document %q: %v", stdout.String(), err)
	}
	if decoder.More() {
		t.Errorf("run(describe) stdout = %q, want one JSON object", stdout.String())
	}

	if described.Name != name {
		t.Errorf("describe named the analyzer %q, want %q", described.Name, name)
	}
	if described.Version != version {
		t.Errorf("describe named version %q, want %q", described.Version, version)
	}
	if described.ContractVersion != contractVersion {
		t.Errorf("describe named contract version %q, want %q as contract/contract.json names it", described.ContractVersion, contractVersion)
	}
	if !slices.Equal(described.SchemaVersionsAccepted, schemaVersions) {
		t.Errorf("describe accepts schema versions %v, want %v as contract/contract.json names them", described.SchemaVersionsAccepted, schemaVersions)
	}
	if want := []string{string(config.GoLanguage)}; !slices.Equal(described.Languages, want) {
		t.Errorf("describe claimed languages %v, want %v", described.Languages, want)
	}
	// Nothing has answered the corpus, so the record is null rather than a result
	// this analyzer never produced.
	if got := string(described.Conformance); got != "null" {
		t.Errorf("describe recorded conformance %s, want null until a corpus run records one", got)
	}

	// The analyzer this document names is the analyzer a report names, so both
	// members are spelled the way the report schema declares.
	for member, value := range map[string]string{"name": described.Name, "version": described.Version} {
		if pattern := analyzerPattern(t, member); !pattern.MatchString(value) {
			t.Errorf("describe named %s %q, which the report schema pattern %q refuses", member, value, pattern)
		}
	}
}

// writeDocument writes one configuration document into dir and returns its path.
func writeDocument(t *testing.T, dir, base, body string) string {
	t.Helper()

	file := filepath.Join(dir, base)
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", file, err)
	}
	return file
}

// printedConfiguration is a resolved configuration as print-config writes it: the
// closed key list, then the provenance object.
type printedConfiguration struct {
	config.Config
	Provenance map[string]string `json:"provenance"`
}

// decodePrinted decodes what print-config wrote, refusing any member the closed
// key list does not declare.
func decodePrinted(t *testing.T, printed []byte) printedConfiguration {
	t.Helper()

	var document printedConfiguration
	decoder := json.NewDecoder(bytes.NewReader(printed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode the printed configuration %q: %v", printed, err)
	}
	return document
}

func TestPrintConfigNamesTheSourceOfEverySetting(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repository := writeDocument(t, dir, "deadset.json", `{"target": {"kind": "library"}, "reporters": {"sort": "size"}}`)
	central := writeDocument(t, dir, "central.json", `{"reporters": {"sort": "position", "max_findings": 20}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-config", "--target=" + dir, "--central=" + central, "--min-confidence=certain"}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
	}

	printed := decodePrinted(t, stdout.Bytes())
	if printed.Target.Kind != config.Library {
		t.Errorf("run(%q) printed target.kind = %q, want %q from the repository configuration", args, printed.Target.Kind, config.Library)
	}
	if printed.Reporters.Sort != config.BySize {
		t.Errorf("run(%q) printed reporters.sort = %q, want %q: the repository configuration outranks the central one", args, printed.Reporters.Sort, config.BySize)
	}
	if printed.Reporters.MaxFindings != 20 {
		t.Errorf("run(%q) printed reporters.max_findings = %d, want 20 from the central configuration", args, printed.Reporters.MaxFindings)
	}
	if printed.Analysis.MinConfidence != config.Certain {
		t.Errorf("run(%q) printed analysis.min_confidence = %q, want %q from the flag", args, printed.Analysis.MinConfidence, config.Certain)
	}

	wantProvenance := map[string]string{
		"target.kind":              "repository: " + repository,
		"reporters.sort":           "repository: " + repository,
		"reporters.max_findings":   "central: " + central,
		"analysis.min_confidence":  "flag: --min-confidence",
		"analysis.generated_files": "default",
	}
	for path, want := range wantProvenance {
		if got := printed.Provenance[path]; got != want {
			t.Errorf("run(%q) printed provenance[%q] = %q, want %q", args, path, got, want)
		}
	}
}

func TestPrintConfigOutputReadBackResolvesToTheSameConfiguration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDocument(t, dir, "deadset.json", `{"target": {"kind": "application"}, "severity": {"DS1101": "warn"}, "roots": {"patterns": ["go://example.com/app#Catalog"]}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-config", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	first := decodePrinted(t, stdout.Bytes())

	// The printed configuration is valid input: reading it back as the repository
	// configuration resolves to an equivalent configuration.
	readBack := writeDocument(t, t.TempDir(), "deadset.json", stdout.String())
	var again, stderrAgain bytes.Buffer
	argsAgain := []string{"print-config", "--target=" + filepath.Dir(readBack)}
	if got := run(t.Context(), argsAgain, &again, &stderrAgain); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", argsAgain, got, exitClean, stderrAgain.String())
	}
	second := decodePrinted(t, again.Bytes())

	if !reflect.DeepEqual(first.Config, second.Config) {
		t.Errorf("print-config output read back resolved to\n%+v\nwant\n%+v", second.Config, first.Config)
	}
	if got, want := second.Provenance["target.kind"], "repository: "+readBack; got != want {
		t.Errorf("the read-back provenance of target.kind = %q, want %q", got, want)
	}
}

func TestPrintConfigRefusals(t *testing.T) {
	t.Parallel()

	codes := contractExitCodes(t)

	tests := []struct {
		name       string
		document   string
		writeIt    bool
		central    string
		wantStderr []string
		wantCode   int
	}{
		{
			name:       "no_source_supplies_the_target_kind",
			document:   `{"reporters": {"sort": "size"}}`,
			writeIt:    true,
			wantCode:   codes["usage"],
			wantStderr: []string{"target.kind is not set", "the repository configuration", "the central configuration", "usage: deadset-go print-config"},
		},
		{
			name:       "no_document_at_all",
			writeIt:    false,
			wantCode:   codes["usage"],
			wantStderr: []string{"target.kind is not set", "(not present)"},
		},
		{
			name:       "a_key_the_analyzer_does_not_implement",
			document:   `{"target": {"kind": "library"}, "reporters": {"fail_under": "warn"}}`,
			writeIt:    true,
			wantCode:   codes["usage"],
			wantStderr: []string{`key "reporters.fail_under" is not implemented`, `the nearest implemented key is "reporters.fail_on"`},
		},
		{
			name:       "a_document_that_is_not_one_instance_of_the_key_list",
			document:   `{"target": {"kind": "sometimes"}}`,
			writeIt:    true,
			wantCode:   codes["usage"],
			wantStderr: []string{"target.kind"},
		},
		{
			name:       "a_central_document_the_invocation_named_and_the_filesystem_does_not_hold",
			document:   `{"target": {"kind": "library"}}`,
			writeIt:    true,
			central:    "absent.json",
			wantCode:   codes["usage"],
			wantStderr: []string{"read the configuration", "absent.json", "usage: deadset-go print-config"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tc.writeIt {
				writeDocument(t, dir, "deadset.json", tc.document)
			}
			args := []string{"print-config", "--target=" + dir}
			if tc.central != "" {
				args = append(args, "--central="+filepath.Join(dir, tc.central))
			}

			var stdout, stderr bytes.Buffer
			if got := run(t.Context(), args, &stdout, &stderr); got != tc.wantCode {
				t.Errorf("run(%q) = %d, want %d\nstderr: %q", args, got, tc.wantCode, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("run(%q) stdout = %q, want empty: a refusal prints no configuration", args, stdout.String())
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("run(%q) stderr = %q, want it to contain %q", args, stderr.String(), want)
				}
			}
		})
	}
}

func TestPrintConfigRefusesADocumentOverTheSizeBound(t *testing.T) {
	t.Parallel()

	// One mebibyte of padding, written as the number rather than read from the
	// bound the command declares, so the test states the bound itself.
	dir := t.TempDir()
	writeDocument(t, dir, "deadset.json", `{"target": {"kind": "library"}}`+strings.Repeat(" ", 1048576))

	var stdout, stderr bytes.Buffer
	args := []string{"print-config", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitUsage {
		t.Errorf("run(%q) = %d, want %d for a document over the size bound", args, got, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("run(%q) stdout = %q, want empty", args, stdout.String())
	}
	for _, want := range []string{"is larger than", "1048576 bytes"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run(%q) stderr = %q, want it to contain %q", args, stderr.String(), want)
		}
	}
}

func TestPrintConfigReadsTheDocumentTheInvocationNames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	named := writeDocument(t, dir, "named.json", `{"target": {"kind": "library"}}`)
	writeDocument(t, dir, "deadset.json", `{"target": {"kind": "application"}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-config", "--target=" + dir, "--config=" + named}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}

	printed := decodePrinted(t, stdout.Bytes())
	if printed.Target.Kind != config.Library {
		t.Errorf("run(%q) printed target.kind = %q, want %q: --config names the repository configuration to read", args, printed.Target.Kind, config.Library)
	}
	if got, want := printed.Provenance["target.kind"], "repository: "+named; got != want {
		t.Errorf("run(%q) printed provenance[target.kind] = %q, want %q", args, got, want)
	}
}

func TestSourceEditFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantName string
		wantOK   bool
	}{
		{name: "no_flag", args: []string{"analyze", "--target=."}, wantName: "", wantOK: false},
		{name: "a_fix_flag", args: []string{"analyze", "--fix"}, wantName: "--fix", wantOK: true},
		{name: "a_fix_flag_with_a_value", args: []string{"analyze", "-fix=all"}, wantName: "-fix", wantOK: true},
		{name: "a_flag_whose_name_carries_fix", args: []string{"analyze", "--fix-imports"}, wantName: "--fix-imports", wantOK: true},
		{name: "an_edit_flag", args: []string{"analyze", "--edit"}, wantName: "--edit", wantOK: true},
		{name: "a_delete_flag", args: []string{"analyze", "--delete-dead"}, wantName: "--delete-dead", wantOK: true},
		{name: "a_rewrite_flag", args: []string{"analyze", "--rewrite=all"}, wantName: "--rewrite", wantOK: true},
		{name: "a_value_carrying_fix_is_not_a_request", args: []string{"print-config", "--config=prefix.json"}, wantName: "", wantOK: false},
		{name: "a_value_carrying_rewrite_is_not_a_request", args: []string{"print-config", "--central=rewrite.json"}, wantName: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotName, gotOK := sourceEditFlag(tc.args)
			if gotName != tc.wantName || gotOK != tc.wantOK {
				t.Errorf("sourceEditFlag(%q) = %q, %t, want %q, %t", tc.args, gotName, gotOK, tc.wantName, tc.wantOK)
			}
		})
	}
}

func TestSettingFlagsSupplyASchemaKey(t *testing.T) {
	t.Parallel()

	// The reverse direction is deliberately not asserted: a key the schema
	// declares with no flag is normal, because most settings are document-only, so
	// a Contract that adds a key adds no flag and this test stays green. Do not add
	// that assertion.
	if len(settingFlags) == 0 {
		t.Fatal("settingFlags is empty, so no flag supplies a setting and this test asserts nothing")
	}
	for flagName, setting := range settingFlags {
		t.Run(flagName, func(t *testing.T) {
			t.Parallel()

			// config.DeclaresSetting answers over the same schema this command
			// resolves a document against, and that schema is pinned equal to
			// contract/config.schema.json key by key in internal/config, so a flag
			// naming something the Contract does not declare fails here.
			if !config.DeclaresSetting(setting.path) {
				t.Errorf("settingFlags[%q].path = %q, want a setting contract/config.schema.json declares",
					flagName, setting.path)
			}
		})
	}
}

// contractKind reads one issue kind contract/kinds.json publishes.
func contractKind(t *testing.T, code string) map[string]any {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/kinds.json")
	if err != nil {
		t.Fatalf("Setup: read contract/kinds.json: %v", err)
	}
	var document struct {
		Kinds []map[string]any `json:"kinds"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/kinds.json: %v", err)
	}

	for _, kind := range document.Kinds {
		if kind["code"] == code {
			return kind
		}
	}
	t.Fatalf("Setup: contract/kinds.json declares no kind %s", code)
	return nil
}

func TestUnmatchedRootNamesTheKindTheContractPublishes(t *testing.T) {
	t.Parallel()

	kind := contractKind(t, unmatchedRoot)
	if got, want := kind["name"], "unmatched-root"; got != want {
		t.Errorf("contract/kinds.json names %s %q, want %q: the code this command prints is that kind", unmatchedRoot, got, want)
	}
	// The run fails on the finding because the kind's default severity is the
	// failing one, which is what makes the exit code this verb returns correct.
	if got, want := kind["default_severity"], string(config.Deny); got != want {
		t.Errorf("contract/kinds.json gives %s default severity %q, want %q", unmatchedRoot, got, want)
	}
	if got := kind["default_enabled"]; got != true {
		t.Errorf("contract/kinds.json has %s default_enabled %v, want true", unmatchedRoot, got)
	}
}

// writeModule writes one fixture module into a temporary directory and returns
// the directory, which is a target print-roots resolves and loads.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for base, body := range files {
		if parent := filepath.Dir(base); parent != "." {
			if err := os.MkdirAll(filepath.Join(dir, parent), 0o750); err != nil {
				t.Fatalf("Setup: create %s: %v", filepath.Join(dir, parent), err)
			}
		}
		writeDocument(t, dir, base, body)
	}
	return dir
}

// applicationModule writes the fixture the root verb is driven against: a main
// package declaring an entry point, an initializer, an exported function and a
// test function, with document as its repository configuration. The entry point
// is declared before the initializer, so the printed order is the declaration
// order rather than the alphabetical one.
func applicationModule(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() {}\n\nfunc init() {}\n\n" +
			"// Helper is exported, and an application publishes nothing.\nfunc Helper() string { return \"\" }\n",
		"app_test.go":      "package main\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) { _ = Helper() }\n",
		repositoryDocument: document,
	})
}

func TestPrintRootsNamesEveryRootAndWhyItIsOne(t *testing.T) {
	t.Parallel()

	dir := applicationModule(t, `{"target": {"kind": "application"}, "roots": {"patterns": ["go://example.com/app#Helper"]}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-roots", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
	}

	want := "go://example.com/app#main\tmain\n" +
		"go://example.com/app#init\tinit\n" +
		"go://example.com/app#Helper\tconfigured\tgo://example.com/app#Helper\n" +
		"go://example.com/app#TestHelper\ttest\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, want)
	}
}

func TestPrintRootsRootsThePublishedAPIOfALibraryTarget(t *testing.T) {
	t.Parallel()

	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package app\n\n// Helper is exported, so a library publishes it.\n" +
			"func Helper() string { return helper() }\n\nfunc helper() string { return \"\" }\n",
	})
	documents := t.TempDir()

	tests := []struct {
		name string
		kind config.TargetKind
		want string
	}{
		{
			name: "a_library_publishes_the_exported_declarations_of_an_importable_package",
			kind: config.Library,
			want: "go://example.com/app#Helper\tpublished-api\n",
		},
		{
			name: "an_application_publishes_nothing_and_has_no_root_at_all_here",
			kind: config.Application,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			document := writeDocument(t, documents, string(tc.kind)+".json", `{"target": {"kind": "`+string(tc.kind)+`"}}`)
			var stdout, stderr bytes.Buffer
			args := []string{"print-roots", "--target=" + dir, "--config=" + document}
			if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
				t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
			}
			if got := stdout.String(); got != tc.want {
				t.Errorf("run(%q) stdout = %q, want %q", args, got, tc.want)
			}
		})
	}
}

func TestPrintRootsReportsEveryConfiguredStringThatNamesNothing(t *testing.T) {
	t.Parallel()

	dir := applicationModule(t, `{"target": {"kind": "application"}, "roots": {"patterns": ["go://example.com/app#Absent", "go://example.com/app#Help*"]}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-roots", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitFindings {
		t.Fatalf("run(%q) = %d, want %d for a configured string that names nothing\nstderr: %q", args, got, exitFindings, stderr.String())
	}

	// The roots the run did find are printed: the unmatched string is a finding
	// about the configuration, not a failure that leaves the set unknown.
	wantStdout := "go://example.com/app#main\tmain\n" +
		"go://example.com/app#init\tinit\n" +
		"go://example.com/app#Helper\tpattern\tgo://example.com/app#Help*\n" +
		"go://example.com/app#TestHelper\ttest\n"
	if got := stdout.String(); got != wantStdout {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, wantStdout)
	}
	wantStderr := unmatchedRoot + ": roots.patterns names nothing: go://example.com/app#Absent\n"
	if got := stderr.String(); got != wantStderr {
		t.Errorf("run(%q) stderr = %q, want %q", args, got, wantStderr)
	}
}

func TestPrintRootsFailures(t *testing.T) {
	t.Parallel()

	codes := contractExitCodes(t)

	tests := []struct {
		name       string
		files      map[string]string
		target     string
		wantStderr []string
	}{
		{
			name: "a_target_that_does_not_type_check",
			files: map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
				"app.go":           "package main\n\nfunc main() {\n\tvar n int = \"one\"\n\t_ = n\n}\n",
				repositoryDocument: `{"target": {"kind": "application"}}`,
			},
			wantStderr: []string{"1 error", "app.go:4:14", "cannot use"},
		},
		{
			name:       "a_target_the_filesystem_does_not_hold",
			files:      map[string]string{repositoryDocument: `{"target": {"kind": "application"}}`},
			target:     "absent",
			wantStderr: []string{"scope: target", "absent", "no such file or directory"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := writeModule(t, tc.files)
			target := dir
			if tc.target != "" {
				target = filepath.Join(dir, tc.target)
			}

			var stdout, stderr bytes.Buffer
			args := []string{"print-roots", "--target=" + target, "--config=" + filepath.Join(dir, repositoryDocument)}
			if got := run(t.Context(), args, &stdout, &stderr); got != codes["failure"] {
				t.Errorf("run(%q) = %d, want %d\nstderr: %q", args, got, codes["failure"], stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("run(%q) stdout = %q, want empty: a run that produced no root set prints none", args, stdout.String())
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("run(%q) stderr = %q, want it to contain %q", args, stderr.String(), want)
				}
			}
		})
	}
}

func TestPrintRootsStopsWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()

	dir := applicationModule(t, `{"target": {"kind": "application"}}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer
	args := []string{"print-roots", "--target=" + dir}
	if got := run(ctx, args, &stdout, &stderr); got != exitFailure {
		t.Errorf("run(a cancelled run, %q) = %d, want %d\nstderr: %q", args, got, exitFailure, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("run(a cancelled run, %q) stdout = %q, want empty", args, stdout.String())
	}
	if want := context.Canceled.Error(); !strings.Contains(stderr.String(), want) {
		t.Errorf("run(a cancelled run, %q) stderr = %q, want it to contain %q", args, stderr.String(), want)
	}
}

// exemptedModule writes the fixture the retained verb is driven against: a main
// package holding one method reached only through an interface the program
// converts to, and one reached only by a formatting verb, so one exemption class
// retains each and no identifier of the module names either. The two declarations
// are written in the order the retained record reads them.
func exemptedModule(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"io\"\n\t\"os\"\n)\n\n" +
			"// Sink counts the bytes written to it.\ntype Sink struct{ written int }\n\n" +
			"// Write is what io.Writer requires, and nothing calls it by name.\n" +
			"func (s *Sink) Write(p []byte) (int, error) {\n\ts.written += len(p)\n\treturn len(p), nil\n}\n\n" +
			"// Tier is a level the program prints.\ntype Tier int\n\n" +
			"// String is what a formatting verb calls, and nothing calls it by name.\n" +
			"func (t Tier) String() string { return \"tier\" }\n\n" +
			"func main() {\n\tvar sink Sink\n\tif _, err := io.Copy(&sink, os.Stdin); err != nil {\n\t\treturn\n\t}\n" +
			"\tfmt.Println(Tier(1))\n}\n",
		repositoryDocument: document,
	})
}

func TestPrintRetainedNamesEverySymbolAnExemptionHeldBackAndTheClassThatHeldIt(t *testing.T) {
	t.Parallel()

	dir := exemptedModule(t, `{"target": {"kind": "application"}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-retained", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
	}

	want := "go://example.com/app#Sink.Write\tinterface-satisfaction\tapp.go:26:23\tsatisfies io.Writer\n" +
		"go://example.com/app#Tier.String\tformat-verb-contract\tapp.go:29:14\tformatted by fmt.Println\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, want)
	}
}

func TestPrintRetainedDropsTheClassTheConfigurationDisables(t *testing.T) {
	t.Parallel()

	dir := exemptedModule(t, `{"target": {"kind": "application"}, "exemptions": {"disabled": ["format-verb-contract"]}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-retained", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}

	// The disabled class retains nothing, so its symbol is a reported candidate
	// rather than a retained one and its line is gone; the class that still runs
	// holds its own symbol back exactly as before.
	want := "go://example.com/app#Sink.Write\tinterface-satisfaction\tapp.go:26:23\tsatisfies io.Writer\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, want)
	}
}

func TestPrintRetainedRefusesAClassNameOutsideTheVocabulary(t *testing.T) {
	t.Parallel()

	dir := exemptedModule(t, `{"target": {"kind": "application"}, "exemptions": {"disabled": ["interface-satisfation"]}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-retained", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitUsage {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitUsage, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("run(%q) stdout = %q, want empty: a refused configuration computes no exemption", args, stdout.String())
	}
	for _, want := range []string{`"interface-satisfation" is not an exemption class`, "interface-satisfaction", "reflective-lookup"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run(%q) stderr = %q, want it to contain %q", args, stderr.String(), want)
		}
	}
}

func TestPrintRetainedReportsEveryConfiguredStringThatNamesNothing(t *testing.T) {
	t.Parallel()

	dir := exemptedModule(t, `{"target": {"kind": "application"}, "roots": {"patterns": ["go://example.com/app#Absent"]}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-retained", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitFindings {
		t.Fatalf("run(%q) = %d, want %d for a configured root that names nothing\nstderr: %q", args, got, exitFindings, stderr.String())
	}

	// The root set is what the sweep decided the candidates against, so a root
	// that names nothing is reported here as the root verb reports it, after the
	// retained set the run did compute.
	if got, want := stdout.String(), "go://example.com/app#Sink.Write"; !strings.Contains(got, want) {
		t.Errorf("run(%q) stdout =\n%s\nwant it to contain %q", args, got, want)
	}
	wantStderr := unmatchedRoot + ": roots.patterns names nothing: go://example.com/app#Absent\n"
	if got := stderr.String(); got != wantStderr {
		t.Errorf("run(%q) stderr = %q, want %q", args, got, wantStderr)
	}
}

func TestDetectorsNameEveryClassOfTheVocabulary(t *testing.T) {
	t.Parallel()

	// A class the table does not hold retains nothing, and nothing else refuses
	// the run: the configuration that disables such a class is still accepted,
	// because the vocabulary is what a class name is checked against, and the
	// symbols the class would have held back are reported as candidates instead.
	// So the table is the only place the vocabulary is wired through, and a class
	// added to it without a row here is an exemption the analyzer never computes.
	classes := exempt.Classes()
	for _, class := range classes {
		if detectors[class] == nil {
			t.Errorf("detectors[%q] = nil, want the detection of every class exempt.Classes() names", class)
		}
	}
	if len(detectors) != len(classes) {
		t.Errorf("detectors names %d classes, want the %d of the vocabulary: %v", len(detectors), len(classes), classes)
	}
}

// testedModule writes a fixture the retained verb is driven against that has test
// files: the main package of exemptedModule, an in-package test file and an
// external test package, each converting a type of its own to an interface no
// identifier of the module names the method through. A load with tests
// synthesizes a test binary for such a module, whose one file lies in the build
// cache, so this is the fixture that answers whether the analysis reasons about a
// module with tests at all.
func testedModule(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"io\"\n\t\"os\"\n)\n\n" +
			"// Sink counts the bytes written to it.\ntype Sink struct{ written int }\n\n" +
			"// Write is what io.Writer requires, and nothing calls it by name.\n" +
			"func (s *Sink) Write(p []byte) (int, error) {\n\ts.written += len(p)\n\treturn len(p), nil\n}\n\n" +
			"// Tier is a level the program prints.\ntype Tier int\n\n" +
			"// String is what a formatting verb calls, and nothing calls it by name.\n" +
			"func (t Tier) String() string { return \"tier\" }\n\n" +
			"func main() {\n\tvar sink Sink\n\tif _, err := io.Copy(&sink, os.Stdin); err != nil {\n\t\treturn\n\t}\n" +
			"\tfmt.Println(Tier(1))\n}\n",
		"app_test.go": "package main\n\nimport (\n\t\"io\"\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
			"// probe counts the bytes a test writes to it.\ntype probe struct{ written int }\n\n" +
			"// Write is what io.Writer requires, and no test calls it by name.\n" +
			"func (p *probe) Write(b []byte) (int, error) {\n\tp.written += len(b)\n\treturn len(b), nil\n}\n\n" +
			"func TestSink(t *testing.T) {\n\tvar p probe\n\tif _, err := io.Copy(&p, strings.NewReader(\"x\")); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
			"\tif p.written != 1 {\n\t\tt.Errorf(\"probe recorded %d bytes, want 1\", p.written)\n\t}\n}\n",
		"app_ext_test.go": "package main_test\n\nimport (\n\t\"io\"\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
			"// tally counts the bytes an external test writes to it.\ntype tally struct{ written int }\n\n" +
			"// Write is what io.Writer requires, and no test calls it by name.\n" +
			"func (t *tally) Write(b []byte) (int, error) {\n\tt.written += len(b)\n\treturn len(b), nil\n}\n\n" +
			"func TestTally(t *testing.T) {\n\tvar w tally\n\tif _, err := io.Copy(&w, strings.NewReader(\"xy\")); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
			"\tif w.written != 2 {\n\t\tt.Errorf(\"tally recorded %d bytes, want 2\", w.written)\n\t}\n}\n",
		repositoryDocument: document,
	})
}

func TestPrintRetainedOverAModuleThatHasTestFiles(t *testing.T) {
	t.Parallel()

	dir := testedModule(t, `{"target": {"kind": "application"}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-retained", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) over a module with test files = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
	}

	// A method declared in a test file is held back by the same class as one
	// declared in production, and the external test package is its own package in
	// the reference the line carries. The order is the site's: the production file
	// first, then the external test file, then the in-package one.
	want := "go://example.com/app#Sink.Write\tinterface-satisfaction\tapp.go:26:23\tsatisfies io.Writer\n" +
		"go://example.com/app#Tier.String\tformat-verb-contract\tapp.go:29:14\tformatted by fmt.Println\n" +
		"go://example.com/app_test#tally.Write\tinterface-satisfaction\tapp_ext_test.go:20:23\tsatisfies io.Writer\n" +
		"go://example.com/app#probe.Write\tinterface-satisfaction\tapp_test.go:20:23\tsatisfies io.Writer\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, want)
	}
}

// platformModule writes the fixture the matrix is driven against: a library whose
// exported surface differs by operating system, so one configuration of the matrix
// declares a root the other one does not and the union names both. The Windows
// file's body is windows, which a test that wants that configuration to fail to
// load replaces with one that does not type-check.
func platformModule(t *testing.T, document, windows string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package app\n\n// Helper names the platform this build serves.\n" +
			"func Helper() string { return platform() }\n",
		"platform_linux.go":   "package app\n\nfunc platform() string { return \"linux\" }\n",
		"platform_windows.go": "package app\n\nfunc platform() string { return " + windows + " }\n\n" + "// Elevated is declared on Windows alone.\nfunc Elevated() bool { return false }\n",
		repositoryDocument:    document,
	})
}

// twoConfigurations is the repository configuration naming a matrix of the two
// operating systems the platform fixture builds under.
const twoConfigurations = `{"target": {"kind": "library"}, "analysis": {"configurations": [` +
	`{"id": "linux-amd64", "os": "linux", "arch": "amd64", "tags": []},` +
	`{"id": "windows-amd64", "os": "windows", "arch": "amd64", "tags": []}]}}`

func TestPrintRootsOverAMatrixLoadsEveryConfigurationAndNamesThemPerRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
		want     string
	}{
		{
			// Nothing is configured, so the matrix is the one the tree implies: the
			// host, and one configuration per operating system its file names carry.
			// The second line is the evidence that the Windows configuration was
			// loaded, since nothing declares Elevated but the file that
			// configuration alone compiles. The third field is the configured string,
			// empty for a detected class, and the fourth the configurations that
			// detected the root.
			name:     "the_matrix_the_target_tree_implies",
			document: `{"target": {"kind": "library"}}`,
			want: "go://example.com/app#Helper\tpublished-api\t\tlinux-amd64 windows-amd64\n" +
				"go://example.com/app#Elevated\tpublished-api\t\twindows-amd64\n",
		},
		{
			// The configuration names one configuration, which is not the host and is
			// not what derivation would answer, so it is the matrix and derivation
			// runs at all. One configuration prints no configuration field.
			name: "the_matrix_the_configuration_names_in_place_of_it",
			document: `{"target": {"kind": "library"}, "analysis": {"configurations": [` +
				`{"id": "windows-amd64", "os": "windows", "arch": "amd64", "tags": []}]}}`,
			want: "go://example.com/app#Helper\tpublished-api\n" +
				"go://example.com/app#Elevated\tpublished-api\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := platformModule(t, tc.document, `"windows"`)

			var stdout, stderr bytes.Buffer
			args := []string{"print-roots", "--target=" + dir}
			if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
				t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
			}
			if got := stdout.String(); got != tc.want {
				t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, tc.want)
			}
		})
	}
}

func TestPrintRootsFailsWhenOneConfigurationOfTheMatrixDoesNotLoad(t *testing.T) {
	t.Parallel()

	// The Windows file returns an integer where the signature says string, so the
	// first configuration of the matrix loads and the second does not.
	dir := platformModule(t, twoConfigurations, "1")

	var stdout, stderr bytes.Buffer
	args := []string{"print-roots", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitFailure {
		t.Fatalf("run(%q) over a matrix whose second configuration does not load = %d, want %d\nstderr: %q",
			args, got, exitFailure, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("run(%q) stdout = %q, want empty: a matrix missing a configuration has no intersection to print", args, stdout.String())
	}
	for _, want := range []string{"windows-amd64", "platform_windows.go:3:33", "cannot use 1"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run(%q) stderr = %q, want it to contain %q", args, stderr.String(), want)
		}
	}
	// The configuration that did load is not named, because the run failed on the
	// one that did not and reports that one.
	if got := stderr.String(); strings.Contains(got, "linux-amd64") {
		t.Errorf("run(%q) stderr = %q, want it to name the configuration that failed and no other", args, got)
	}
}

func TestPrintRetainedReadsTheDelimitersTheConfigurationSets(t *testing.T) {
	t.Parallel()

	// The template names the field with delimiters of the project's own, so the
	// class retains the field only if the configured pair reached it.
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\n// Page is what a template renders.\ntype Page struct {\n" +
			"\t// Subtitle is named by a template and by no identifier of this module.\n\tSubtitle string\n}\n\n" +
			"func main() { _ = Page{} }\n",
		"templates/page.tmpl": "<aside>[[ .Subtitle ]]</aside>\n",
		repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"template_dirs": ["templates"],` +
			` "template_delimiters": {"left": "[[", "right": "]]"}}}`,
	})

	var stdout, stderr bytes.Buffer
	args := []string{"print-retained", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
	}

	want := "go://example.com/app#Page.Subtitle\ttemplate-field\ttemplates/page.tmpl:1:11\tnamed by [[.Subtitle]]\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, want)
	}
}

func TestPrintRootsReportsNoConfiguredStringOneConfigurationOfTheMatrixMatched(t *testing.T) {
	t.Parallel()

	// Elevated is declared in the Windows file alone, so the pattern that names it
	// matches nothing under the first configuration and something under the second.
	dir := platformModule(t, `{"target": {"kind": "application"}, "roots": {"patterns":`+
		` ["go://example.com/app#Elevated", "go://example.com/app#Absent"]}, "analysis": {"configurations": [`+
		`{"id": "linux-amd64", "os": "linux", "arch": "amd64", "tags": []},`+
		`{"id": "windows-amd64", "os": "windows", "arch": "amd64", "tags": []}]}}`, `"windows"`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-roots", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitFindings {
		t.Fatalf("run(%q) = %d, want %d for the one configured string no configuration matched\nstderr: %q",
			args, got, exitFindings, stderr.String())
	}

	want := "go://example.com/app#Elevated\tconfigured\tgo://example.com/app#Elevated\twindows-amd64\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, want)
	}
	wantStderr := unmatchedRoot + ": roots.patterns names nothing: go://example.com/app#Absent\n"
	if got := stderr.String(); got != wantStderr {
		t.Errorf("run(%q) stderr = %q, want %q: a string one configuration matched names something", args, got, wantStderr)
	}
}

// platformExemptedModule writes the fixture the retained union is driven against: a
// main package whose Windows file alone declares a type a formatting verb reaches,
// so one exemption class retains a symbol the other configuration does not hold.
func platformExemptedModule(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod":            "module example.com/app\n\ngo 1.27.1\n",
		"app.go":            "package main\n\nfunc main() { _ = platform() }\n",
		"platform_linux.go": "package main\n\nfunc platform() string { return \"linux\" }\n",
		"platform_windows.go": "package main\n\nimport \"fmt\"\n\n" +
			"// Tier is a level this build prints.\ntype Tier int\n\n" +
			"// String is what a formatting verb calls, and nothing calls it by name.\n" +
			"func (t Tier) String() string { return \"tier\" }\n\n" +
			"func platform() string {\n\tfmt.Println(Tier(1))\n\treturn \"windows\"\n}\n",
		repositoryDocument: document,
	})
}

func TestPrintRetainedOverAMatrixNamesWhatAnyConfigurationRetained(t *testing.T) {
	t.Parallel()

	dir := platformExemptedModule(t, `{"target": {"kind": "application"}, "analysis": {"configurations": [`+
		`{"id": "linux-amd64", "os": "linux", "arch": "amd64", "tags": []},`+
		`{"id": "windows-amd64", "os": "windows", "arch": "amd64", "tags": []}]}}`)

	var stdout, stderr bytes.Buffer
	args := []string{"print-retained", "--target=" + dir}
	if got := run(t.Context(), args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) over a matrix of two configurations = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
	}

	// The record exists under the second configuration alone, so it is here only
	// because the classes ran over every configuration of the matrix.
	want := "go://example.com/app#Tier.String\tformat-verb-contract\tplatform_windows.go:12:14\tformatted by fmt.Println\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%q) stdout =\n%s\nwant\n%s", args, got, want)
	}
}
