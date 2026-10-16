package config_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// threeSources is the inputs of a run reading a flag, a repository and a central
// configuration, each optional.
func threeSources(flags map[vocab.Key]string, repository, central string) *config.Inputs {
	in := &config.Inputs{}
	if len(flags) > 0 {
		in.Flags = map[vocab.Key][]byte{}
		for key, value := range flags {
			in.Flags[key] = []byte(value)
		}
	}
	if repository != "" {
		in.Repository = config.Document{Path: repositoryLabel, Data: []byte(repository)}
	}
	if central != "" {
		in.Central = config.Document{Path: "central.json", Data: []byte(central)}
	}
	return in
}

func TestResolveAppliesTheHighestRankedSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		flags      map[vocab.Key]string
		repository string
		central    string
		key        string
		want       string
		origin     string
	}{
		{
			name:       "a_flag_outranks_both_documents",
			flags:      map[vocab.Key]string{vocab.KeyAnalysisMinConfidence: `"possible"`},
			repository: `{"target": {"kind": "library"}, "analysis": {"min_confidence": "certain"}}`,
			central:    `{"analysis": {"min_confidence": "probable"}}`,
			key:        "analysis.min_confidence", want: `"possible"`, origin: "flag: --min-confidence",
		},
		{
			name:       "the_repository_outranks_the_central_document",
			repository: `{"target": {"kind": "library"}, "reporters": {"sort": "size"}}`,
			central:    `{"reporters": {"sort": "position"}}`,
			key:        "reporters.sort", want: `"size"`, origin: "repository: deadset.json",
		},
		{
			name:       "the_central_document_supplies_what_the_repository_omits",
			repository: `{"target": {"kind": "library"}}`,
			central:    `{"reporters": {"fail_on": "warn"}}`,
			key:        "reporters.fail_on", want: `"warn"`, origin: "central: central.json",
		},
		{
			name:       "the_default_stands_where_no_source_supplies_one",
			repository: `{"target": {"kind": "library"}}`,
			key:        "reporters.max_findings", want: `0`, origin: "default",
		},
		{
			name:       "an_array_from_the_higher_source_replaces_rather_than_merges",
			repository: `{"target": {"kind": "library"}, "roots": {"patterns": ["go://a#A"]}}`,
			central:    `{"roots": {"patterns": ["go://b#B", "go://c#C"]}}`,
			key:        "roots.patterns", want: `["go://a#A"]`, origin: "repository: deadset.json",
		},
		{
			name:       "a_false_the_repository_names_outranks_a_central_true",
			repository: `{"target": {"kind": "library"}, "consumers": {"complete": false}}`,
			central:    `{"consumers": {"complete": true}}`,
			key:        "consumers.complete", want: `false`, origin: "repository: deadset.json",
		},
		{
			name:       "a_string_target_kind_replaces_an_object_whole",
			repository: `{"target": {"kind": "application"}}`,
			central:    `{"target": {"kind": {"go": "library", "ts": "library"}}}`,
			key:        "target.kind", want: `"application"`, origin: "repository: deadset.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := resolved(t, threeSources(tc.flags, tc.repository, tc.central))
			_, document := printedDocument(t, r)
			if got, _ := valueAt(document, vocab.Key(tc.key)); !reflect.DeepEqual(got, decoded(t, tc.want)) {
				t.Errorf("the resolved %s = %#v, want %s", tc.key, got, tc.want)
			}
			if got := r.Provenance[tc.key].String(); got != tc.origin {
				t.Errorf("provenance[%s] = %q, want %q", tc.key, got, tc.origin)
			}
		})
	}
}

func TestResolveReadsTheSettingsTheGoAnalysisReads(t *testing.T) {
	t.Parallel()

	r := resolved(t, fromRepository(`{
		"target": {"kind": {"go": "library", "ts": "application"}},
		"analysis": {
			"min_confidence": "possible", "generated_files": "include", "consumer_tests": "production",
			"configurations": [
				{"id": "linux", "os": "linux", "arch": "amd64", "tags": ["netgo"]},
				{"id": "web", "project": "tsconfig.json"},
				{"id": "darwin", "os": "darwin", "arch": "arm64"}
			],
			"matrix": {"complete": true},
			"template_dirs": ["templates"],
			"template_delimiters": {"left": "[[", "right": "]]"}
		},
		"consumers": {"complete": true},
		"roots": {"patterns": ["go://example.com/app#Run"]},
		"severity": {"DS14": "allow"},
		"exemptions": {"disabled": ["enum-group"]},
		"reporters": {"formats": ["json"], "sort": "size", "cascade": "full", "max_findings": 7, "fail_on": "warn"}
	}`))
	want := config.Config{
		Severity: map[string]vocab.Severity{"DS14": vocab.Allow},
		Target:   config.Target{Kind: report.TargetKindLibrary},
		Analysis: config.Analysis{
			MinConfidence:      vocab.Possible,
			TemplateDelimiters: config.TemplateDelimiters{Left: "[[", Right: "]]"},
			TemplateDirs:       []string{"templates"},
			Platforms: []config.Platform{
				{ID: "linux", OS: "linux", Arch: "amd64", Tags: []string{"netgo"}},
				{ID: "darwin", OS: "darwin", Arch: "arm64"},
			},
			Matrix:                  config.Matrix{Complete: true},
			IncludeGenerated:        true,
			ConsumerTestsProduction: true,
		},
		Reporters: config.Reporters{
			FailOn: vocab.Warn, Formats: []vocab.Format{vocab.FormatJSON}, MaxFindings: 7, BySize: true, CascadeFull: true,
		},
		Roots:      config.Roots{Patterns: []string{"go://example.com/app#Run"}},
		Exemptions: config.Exemptions{Disabled: []vocab.Exemption{vocab.EnumGroup}},
		Consumers:  config.Consumers{Complete: true},
	}
	if !reflect.DeepEqual(r.Config, want) {
		t.Errorf("Resolve read\n%+v\nwant\n%+v", r.Config, want)
	}
}

// Every key the Go analysis does not read is accepted whatever its value and left
// out of the resolved configuration: the orchestrator checks it, and the analyzer
// of its own language reads it.
func TestResolveAcceptsEveryKeyAnotherProductReadsUnread(t *testing.T) {
	t.Parallel()

	accepted := 0
	for _, row := range vocab.ConfigKeys() {
		if row.ReadIn(vocab.Go) {
			continue
		}
		accepted++
		t.Run(string(row.Key), func(t *testing.T) {
			t.Parallel()

			document := `{"target": {"kind": "library"}, ` + strings.TrimPrefix(nestedDocument(row.Key, `{"planted": null}`), "{")
			r := resolved(t, fromRepository(document))
			_, printed := printedDocument(t, r)
			if got, held := valueAt(printed, row.Key); held {
				t.Errorf("the resolved configuration names %s = %#v, want the key left out", row.Key, got)
			}
			if _, held := r.Provenance[string(row.Key)]; held {
				t.Errorf("provenance names %s, want the key left out", row.Key)
			}
		})
	}
	if accepted == 0 {
		t.Fatal("the Go analysis reads every key, so this test pins nothing")
	}
}

func TestResolveReadsTheGoMemberOfATargetKindWrittenPerLanguage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		kind  string
		want  report.TargetKind
		names string
	}{
		{name: "another_language_left_out", kind: `{"go": "library"}`, want: report.TargetKindLibrary},
		{name: "another_languages_value_refused_by_its_own_analysis", kind: `{"go": "application", "ts": "service"}`, want: report.TargetKindApplication},
		{name: "another_languages_member_null", kind: `{"go": "library", "ts": null}`, want: report.TargetKindLibrary},
		{name: "a_member_naming_no_language", kind: `{"go": "library", "py": "library"}`, names: `"target.kind.py" is not implemented`},
		{name: "an_undeclared_member_before_the_go_member_left_out", kind: `{"ts": "library", "rb": "library"}`, names: `"target.kind.rb" is not implemented`},
		{name: "the_go_member_left_out", kind: `{"ts": "library"}`, names: "target.kind.go: is required and absent"},
		{name: "the_go_member_refused", kind: `{"go": "service", "ts": "library"}`, names: `target.kind.go: "service" is not one`},
		{name: "the_go_member_null", kind: `{"go": null, "ts": "library"}`, names: "target.kind.go: the value is null"},
		{name: "a_string_refused", kind: `"service"`, names: `target.kind: "service" is not one`},
		{name: "a_value_of_neither_shape", kind: `["library"]`, names: "target.kind: the value matches 0 of the alternatives"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := fromRepository(`{"target": {"kind": ` + tc.kind + `}}`)
			if tc.names != "" {
				if refusal := refused(t, in); !strings.Contains(refusal.Error(), tc.names) {
					t.Errorf("Resolve(target.kind %s) = %q, want it to name %q", tc.kind, refusal, tc.names)
				}
				return
			}
			r := resolved(t, in)
			if r.Config.Target.Kind != tc.want {
				t.Errorf("Resolve(target.kind %s).Target.Kind = %q, want %q", tc.kind, r.Config.Target.Kind, tc.want)
			}
			_, printed := printedDocument(t, r)
			if got, _ := valueAt(printed, vocab.KeyTargetKind); !reflect.DeepEqual(got, map[string]any{"go": string(tc.want)}) {
				t.Errorf("the printed target.kind = %#v, want the Go member alone", got)
			}
		})
	}
}

func TestResolveRefusesAMissingTargetKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repository string
		central    string
		names      []string
	}{
		{
			name:       "both_sources_present_and_neither_supplies_it",
			repository: `{"analysis": {"min_confidence": "certain"}}`,
			central:    `{"reporters": {"fail_on": "warn"}}`,
			names:      []string{"target.kind is not set", "deadset.json", "central.json"},
		},
		{name: "no_source_present_at_all", names: []string{"target.kind is not set", "the repository configuration (not present)"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			refusal := refused(t, threeSources(nil, tc.repository, tc.central))
			for _, name := range tc.names {
				if !strings.Contains(refusal.Error(), name) {
					t.Errorf("Resolve = %q, want it to name %q", refusal, name)
				}
			}
		})
	}
}

func TestResolveRefusesAKeyTheClosedKeyListDoesNotDeclare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
		names    string
	}{
		{name: "a_near_miss_of_a_setting", document: `{"reporters": {"fail_under": "warn"}}`, names: `"reporters.fail_under" is not implemented`},
		{name: "a_dotted_path_written_as_one_key", document: `{"analysis.min_confidence": "certain"}`, names: `"analysis.min_confidence" is not implemented: it is one setting`},
		{name: "a_key_in_a_section_declaring_none", document: `{"go": {"test_files": ["x"]}}`, names: `"go.test_files" is not implemented`},
		{name: "a_key_in_a_build_matrix_entry", document: `{"analysis": {"configurations": [{"id": "a", "os": "linux", "arch": "amd64", "cgo": true}]}}`, names: `"analysis.configurations[0].cgo" is not implemented`},
		{name: "a_member_of_a_setting_written_as_an_object", document: `{"analysis": {"template_delimiters": {"left": "[", "right": "]", "middle": "|"}}}`, names: `"analysis.template_delimiters.middle" is not implemented`},
		{name: "a_severity_key_that_is_not_a_code", document: `{"severity": {"DS1": "warn"}}`, names: `"severity.DS1" is not implemented: a severity key is one issue-kind code`},
		{name: "a_severity_code_the_contract_fixes", document: `{"severity": {"DS9003": "warn"}}`, names: "the Contract fixes the severity of DS9003"},
		{name: "a_severity_code_no_kind_carries", document: `{"severity": {"DS2999": "warn"}}`, names: "no issue kind carries the code DS2999"},
		{name: "a_severity_family_no_range_declares", document: `{"severity": {"DS29": "warn"}}`, names: "no issue kind carries a code of the family DS29"},
		{name: "a_member_named_empty_at_the_root", document: `{"": {"kind": "library"}}`, names: `key "" is not implemented`},
		{name: "a_member_named_empty_in_a_section", document: `{"reporters": {"": {"fail_on": "warn"}}}`, names: `key "reporters." is not implemented`},
		{name: "a_member_named_empty_in_a_nested_section", document: `{"analysis": {"matrix": {"": {"complete": true}}}}`, names: `key "analysis.matrix." is not implemented`},
		{name: "a_member_named_empty_in_a_setting_written_as_an_object", document: `{"analysis": {"template_delimiters": {"left": "[", "right": "]", "": 1}}}`, names: `key "analysis.template_delimiters." is not implemented`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			document := `{"target": {"kind": "library"}, ` + strings.TrimPrefix(tc.document, "{")
			if refusal := refused(t, fromRepository(document)); !strings.Contains(refusal.Error(), tc.names) {
				t.Errorf("Resolve(%s) = %q, want it to name %q", tc.document, refusal, tc.names)
			}
		})
	}
}

// Every live severity key the vocabulary holds is a setting, and every
// well-formed key it does not hold is refused.
func TestResolveAdmitsExactlyTheSeverityKeysTheTableHolds(t *testing.T) {
	t.Parallel()

	row, _ := vocab.ConfigKeyOf(vocab.KeySeverity)
	if len(row.Values) == 0 {
		t.Fatal("the severity key holds no vocabulary, so this test pins nothing")
	}
	for _, kind := range vocab.Kinds() {
		for _, key := range []string{string(kind.Code), vocab.FamilyOf(kind.Code)} {
			_, err := config.Resolve(fromRepository(`{"target": {"kind": "library"}, "severity": {"` + key + `": "warn"}}`))
			if held := slices.Contains(row.Values, key); held != (err == nil) {
				t.Errorf("Resolve(severity.%s) = %v, want a refusal exactly when the vocabulary does not hold it (holds %t)", key, err, held)
			}
		}
	}
}

func TestResolveRefusesAMalformedValueOfAKeyTheGoAnalysisReads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
		names    string
	}{
		{name: "a_value_outside_a_closed_set", document: `{"analysis": {"min_confidence": "maybe"}}`, names: `analysis.min_confidence: "maybe" is not one`},
		{name: "an_array_naming_one_entry_twice", document: `{"reporters": {"formats": ["json", "json"]}}`, names: `reporters.formats: "json" is listed twice`},
		{name: "an_empty_format_list", document: `{"reporters": {"formats": []}}`, names: "reporters.formats: the array holds 0 items"},
		{name: "a_negative_finding_cap", document: `{"reporters": {"max_findings": -1}}`, names: "reporters.max_findings: -1 is below 0"},
		{name: "an_integer_written_with_a_fraction", document: `{"reporters": {"max_findings": 100.0}}`, names: "reporters.max_findings: holds 100.0, want an integer"},
		{name: "an_integer_written_with_an_exponent", document: `{"reporters": {"max_findings": 1e2}}`, names: "reporters.max_findings: holds 1e2"},
		{name: "a_contract_version_that_is_not_semantic", document: `{"contract_version": "8"}`, names: "contract_version: \"8\" does not match"},
		{name: "a_section_of_another_type", document: `{"analysis": []}`, names: "analysis: holds an array, want an object"},
		{name: "a_null_setting", document: `{"reporters": {"max_findings": null}}`, names: "reporters.max_findings: the value is null"},
		{name: "a_null_section", document: `{"go": null}`, names: "go: holds null, want an object"},
		{name: "a_build_matrix_entry_naming_no_architecture", document: `{"analysis": {"configurations": [{"id": "a", "os": "linux"}]}}`, names: "analysis.configurations[0].arch: is required and absent"},
		{name: "a_project_entry_leading_out_of_the_target_root", document: `{"analysis": {"configurations": [{"id": "a", "project": "../tsconfig.json"}]}}`, names: "analysis.configurations[0].project"},
		{name: "a_delimiter_pair_naming_one_member", document: `{"analysis": {"template_delimiters": {"left": "[["}}}`, names: "analysis.template_delimiters.right: is required and absent"},
		{name: "a_severity_outside_the_closed_set", document: `{"severity": {"DS1001": "error"}}`, names: `severity.DS1001: "error" is not one`},
		{name: "a_provenance_entry_of_another_type", document: `{"provenance": {"target.kind": 1}}`, names: "provenance.target.kind: the value is integer"},
		{name: "a_document_that_is_not_an_object", document: `["target"]`, names: "deadset.json: want one JSON object"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			document := tc.document
			if rest, isObject := strings.CutPrefix(document, "{"); isObject {
				document = `{"target": {"kind": "library"}, ` + rest
			}
			if refusal := refused(t, fromRepository(document)); !strings.Contains(refusal.Error(), tc.names) {
				t.Errorf("Resolve(%s) = %q, want it to name %q", tc.document, refusal, tc.names)
			}
		})
	}
}

// exemptions.disabled admits only the classes contract/exemptions.json declares,
// refused at configuration naming the entry and the classes.
func TestResolveRefusesAnExemptionClassTheVocabularyDoesNotHold(t *testing.T) {
	t.Parallel()

	refusal := refused(t, fromRepository(`{"target": {"kind": "library"}, "exemptions": {"disabled": ["enum-group", "planted-class"]}}`))
	for _, want := range []string{`exemptions.disabled[1]: "planted-class"`, "; the exemption classes are " + string(vocab.InterfaceSatisfaction) + ", "} {
		if !strings.Contains(refusal.Error(), want) {
			t.Errorf("Resolve = %q, want it to name %q", refusal, want)
		}
	}
}

func TestResolveRefusesADocumentThatIsNotStrictJSON(t *testing.T) {
	t.Parallel()

	for name, document := range map[string]string{
		"a_member_written_twice": `{"target": {"kind": "library"}, "severity": {"DS1501": "warn"}, "severity": {}}`,
		"a_second_document":      `{"target": {"kind": "library"}} {}`,
		"a_bracket_after_it":     `{"target": {"kind": "library"}}]`,
		"malformed_json":         `{"target": {"kind": "library"},}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if refusal := refused(t, fromRepository(document)); !strings.HasPrefix(refusal.Error(), repositoryLabel+": ") {
				t.Errorf("Resolve(%s) = %q, want it to name the document", document, refusal)
			}
		})
	}
}

func TestResolveSeverityPerCode(t *testing.T) {
	t.Parallel()

	r := resolved(t, threeSources(nil,
		`{"target": {"kind": "library"}, "severity": {"DS1501": "allow"}}`,
		`{"severity": {"DS1501": "deny", "DS1201": "warn"}}`))
	want := map[string]vocab.Severity{"DS1501": vocab.Allow, "DS1201": vocab.Warn}
	if !reflect.DeepEqual(r.Config.Severity, want) {
		t.Errorf("Severity = %v, want %v", r.Config.Severity, want)
	}
	for code, origin := range map[string]string{"severity.DS1501": "repository: deadset.json", "severity.DS1201": "central: central.json"} {
		if got := r.Provenance[code].String(); got != origin {
			t.Errorf("provenance[%s] = %q, want %q", code, got, origin)
		}
	}
	if _, held := r.Provenance["severity"]; held {
		t.Error("provenance names severity, want one entry per code alone")
	}
}

func TestResolveSeverityWhenItResolvesEmpty(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ document, origin string }{
		"no_source_names_the_object":    {`{"target": {"kind": "library"}}`, "default"},
		"the_repository_names_it_empty": {`{"target": {"kind": "library"}, "severity": {}}`, "repository: deadset.json"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := resolved(t, fromRepository(tc.document)).Provenance["severity"].String(); got != tc.origin {
				t.Errorf("provenance[severity] = %q, want %q", got, tc.origin)
			}
		})
	}
}

func TestResolveReadsPastProvenanceOnInput(t *testing.T) {
	t.Parallel()

	r := resolved(t, fromRepository(`{"target": {"kind": "library"}, "provenance": {"target.kind": "central: elsewhere.json", "ts.test_files": "default"}}`))
	if got := r.Provenance["target.kind"].String(); got != "repository: deadset.json" {
		t.Errorf("provenance[target.kind] = %q, want the document that supplied it", got)
	}
}

func TestResolveRefusesAFlagNoOptionNames(t *testing.T) {
	t.Parallel()

	in := fromRepository(`{"target": {"kind": "library"}}`)
	in.Flags = map[vocab.Key][]byte{vocab.KeyAnalysisTemplateDirs: []byte(`["x"]`)}
	if refusal := refused(t, in); !strings.Contains(refusal.Error(), `"analysis.template_dirs" is not implemented: no flag supplies it`) {
		t.Errorf("Resolve(a flag for analysis.template_dirs) = %q, want it refused", refusal)
	}
}

func TestResolveNamesTheFlagThatSuppliedARefusedValue(t *testing.T) {
	t.Parallel()

	in := fromRepository(`{"target": {"kind": "library"}}`)
	in.Flags = map[vocab.Key][]byte{vocab.KeyReportersSort: []byte(`"largest"`)}
	if refusal := refused(t, in); !strings.HasPrefix(refusal.Error(), `--sort: reporters.sort: "largest"`) {
		t.Errorf("Resolve(--sort=largest) = %q, want it to name the flag and the setting", refusal)
	}
}

func TestResolveRequiresEveryDocumentToNameItsPath(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]*config.Inputs{
		"a_repository_configuration": {Repository: config.Document{Data: []byte(`{"target": {"kind": "library"}}`)}},
		"a_central_configuration":    {Central: config.Document{Data: []byte(`{"target": {"kind": "library"}}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := config.Resolve(in); !errors.Is(err, config.ErrNoSourceLabel) {
				t.Errorf("Resolve(%s with no path) = %v, want ErrNoSourceLabel", name, err)
			}
		})
	}
}
