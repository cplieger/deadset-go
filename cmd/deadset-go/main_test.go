package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/load"
	spec "github.com/cplieger/deadset-spec"
)

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

	wired := map[string]int{"clean": exitClean, "usage": exitUsage, "failure": exitFailure}
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
			name:       "analyze_is_not_implemented",
			args:       []string{"analyze"},
			wantCode:   exitUsage,
			wantStderr: []string{"analyze is not implemented in this version", "usage: deadset-go"},
		},
		{
			name:       "explain_is_not_implemented",
			args:       []string{"explain", "--why", "go://example.com/app#Catalog"},
			wantCode:   exitUsage,
			wantStderr: []string{"explain is not implemented in this version"},
		},
		{
			name:       "print_roots_is_not_implemented",
			args:       []string{"print-roots"},
			wantCode:   exitUsage,
			wantStderr: []string{"print-roots is not implemented in this version"},
		},
		{
			name:       "print_retained_is_not_implemented",
			args:       []string{"print-retained"},
			wantCode:   exitUsage,
			wantStderr: []string{"print-retained is not implemented in this version"},
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
			if got := run(tc.args, &stdout, &stderr); got != tc.wantCode {
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
	if got := run([]string{"describe"}, &stdout, &stderr); got != exitClean {
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
	if got := run(args, &stdout, &stderr); got != exitClean {
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
	if got := run(args, &stdout, &stderr); got != exitClean {
		t.Fatalf("run(%q) = %d, want %d\nstderr: %q", args, got, exitClean, stderr.String())
	}
	first := decodePrinted(t, stdout.Bytes())

	// The printed configuration is valid input: reading it back as the repository
	// configuration resolves to an equivalent configuration.
	readBack := writeDocument(t, t.TempDir(), "deadset.json", stdout.String())
	var again, stderrAgain bytes.Buffer
	argsAgain := []string{"print-config", "--target=" + filepath.Dir(readBack)}
	if got := run(argsAgain, &again, &stderrAgain); got != exitClean {
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
			if got := run(args, &stdout, &stderr); got != tc.wantCode {
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
	if got := run(args, &stdout, &stderr); got != exitUsage {
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
	if got := run(args, &stdout, &stderr); got != exitClean {
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

// configSchema decodes the Contract's configuration schema, the closed key list
// every setting a flag supplies is a key of.
func configSchema(t *testing.T) map[string]any {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/config.schema.json")
	if err != nil {
		t.Fatalf("Setup: read contract/config.schema.json: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/config.schema.json: %v", err)
	}
	return document
}

// schemaObject reads one declaration of the schema as the object a declaration is.
func schemaObject(t *testing.T, at string, declaration any) map[string]any {
	t.Helper()

	object, isObject := declaration.(map[string]any)
	if !isObject {
		t.Fatalf("Setup: contract/config.schema.json declares %s as %T, want an object", at, declaration)
	}
	return object
}

// schemaMember resolves one member name against one declaration: the members that
// declaration names, then the patterns it declares where the member names are
// open. It returns the member's own declaration.
func schemaMember(t *testing.T, at string, declaration map[string]any, name string) (map[string]any, bool) {
	t.Helper()

	properties, _ := declaration["properties"].(map[string]any)
	if member, declared := properties[name]; declared {
		return schemaObject(t, joinSchemaKey(at, name), member), true
	}
	patterns, _ := declaration["patternProperties"].(map[string]any)
	for pattern, member := range patterns {
		matched, err := regexp.MatchString(pattern, name)
		if err != nil {
			t.Fatalf("Setup: compile the pattern %q contract/config.schema.json declares at %s: %v", pattern, at, err)
		}
		if matched {
			return schemaObject(t, joinSchemaKey(at, name), member), true
		}
	}
	return nil, false
}

// schemaMembers lists the member names one declaration declares, in ascending
// order, so a failure names what the schema holds where a path left it.
func schemaMembers(declaration map[string]any) []string {
	properties, _ := declaration["properties"].(map[string]any)
	patterns, _ := declaration["patternProperties"].(map[string]any)
	return append(slices.Sorted(maps.Keys(properties)), slices.Sorted(maps.Keys(patterns))...)
}

// joinSchemaKey spells the dotted path of one member of the value at at.
func joinSchemaKey(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

// schemaDeclares reports whether one dotted setting path names a key the Contract's
// configuration schema declares. Where it does not, it also returns the prefix that
// did resolve and the members that prefix declares.
func schemaDeclares(t *testing.T, path string) (resolved string, members []string, declared bool) {
	t.Helper()

	at := configSchema(t)
	segments := strings.Split(path, ".")
	for index, name := range segments {
		member, isDeclared := schemaMember(t, strings.Join(segments[:index], "."), at, name)
		if !isDeclared {
			return strings.Join(segments[:index], "."), schemaMembers(at), false
		}
		at = member
	}
	return path, nil, true
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

			resolved, members, declared := schemaDeclares(t, setting.path)
			if !declared {
				t.Errorf("settingFlags[%q].path = %q, want a key contract/config.schema.json declares: %q declares %v", flagName, setting.path, resolved, members)
			}
		})
	}
}
