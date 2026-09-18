package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
)

// labelled returns the inputs one case supplies, with the file names a refusal
// and a provenance entry name them by.
func labelled(flags, repository, central string, flagLabels map[string]string) config.Inputs {
	in := config.Inputs{
		RepositoryLabel: "deadset.json",
		CentralLabel:    "central.json",
		FlagLabels:      flagLabels,
	}
	if flags != "" {
		in.Flags = []byte(flags)
	}
	if repository != "" {
		in.Repository = []byte(repository)
	}
	if central != "" {
		in.Central = []byte(central)
	}
	return in
}

func TestResolveAppliesTheHighestRankedSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		flags      string
		repository string
		central    string
		path       string
		read       func(config.Config) any
		want       any
		origin     config.Origin
	}{
		{
			name:       "a_flag_outranks_both_documents",
			flags:      `{"analysis.min_confidence": "possible"}`,
			repository: `{"target": {"kind": "library"}, "analysis": {"min_confidence": "probable"}}`,
			central:    `{"analysis": {"min_confidence": "certain"}}`,
			path:       "analysis.min_confidence",
			read:       func(c config.Config) any { return c.Analysis.MinConfidence },
			want:       config.Possible,
			origin:     config.Origin{Source: config.SourceFlag, Label: "--min-confidence"},
		},
		{
			name:       "the_repository_outranks_the_central_document",
			repository: `{"target": {"kind": "library"}, "reporters": {"fail_on": "allow"}}`,
			central:    `{"reporters": {"fail_on": "warn"}}`,
			path:       "reporters.fail_on",
			read:       func(c config.Config) any { return c.Reporters.FailOn },
			want:       config.Allow,
			origin:     config.Origin{Source: config.SourceRepository, Label: "deadset.json"},
		},
		{
			name:       "the_central_document_supplies_what_the_repository_omits",
			repository: `{"target": {"kind": "library"}}`,
			central:    `{"reporters": {"sort": "size"}}`,
			path:       "reporters.sort",
			read:       func(c config.Config) any { return c.Reporters.Sort },
			want:       config.BySize,
			origin:     config.Origin{Source: config.SourceCentral, Label: "central.json"},
		},
		{
			name:       "the_default_stands_where_no_source_supplies_one",
			repository: `{"target": {"kind": "library"}}`,
			path:       "analysis.generated_files",
			read:       func(c config.Config) any { return c.Analysis.GeneratedFiles },
			want:       config.ExcludeGenerated,
			origin:     config.Origin{Source: config.SourceDefault},
		},
		{
			name:       "an_array_from_the_higher_source_replaces_rather_than_merges",
			repository: `{"target": {"kind": "library"}, "roots": {"patterns": ["go://example.com/app#One"]}}`,
			central:    `{"roots": {"patterns": ["go://example.com/app#Two", "go://example.com/app#Three"]}}`,
			path:       "roots.patterns",
			read:       func(c config.Config) any { return c.Roots.Patterns },
			want:       []string{"go://example.com/app#One"},
			origin:     config.Origin{Source: config.SourceRepository, Label: "deadset.json"},
		},
		{
			name:       "an_empty_array_the_repository_names_outranks_a_central_value",
			repository: `{"target": {"kind": "library"}, "analysis": {"template_dirs": []}}`,
			central:    `{"analysis": {"template_dirs": ["templates"]}}`,
			path:       "analysis.template_dirs",
			read:       func(c config.Config) any { return c.Analysis.TemplateDirs },
			want:       []string{},
			origin:     config.Origin{Source: config.SourceRepository, Label: "deadset.json"},
		},
		{
			name:       "a_false_the_repository_names_outranks_a_central_true",
			repository: `{"target": {"kind": "library"}, "consumers": {"complete": false}}`,
			central:    `{"consumers": {"complete": true}}`,
			path:       "consumers.complete",
			read:       func(c config.Config) any { return c.Consumers.Complete },
			want:       false,
			origin:     config.Origin{Source: config.SourceRepository, Label: "deadset.json"},
		},
		{
			name:       "a_nested_declaration_resolves_on_its_own",
			repository: `{"target": {"kind": "library"}}`,
			central:    `{"analysis": {"matrix": {"complete": true}}}`,
			path:       "analysis.matrix.complete",
			read:       func(c config.Config) any { return c.Analysis.Matrix.Complete },
			want:       true,
			origin:     config.Origin{Source: config.SourceCentral, Label: "central.json"},
		},
		{
			name:       "the_contract_version_defaults_to_the_one_this_package_implements",
			repository: `{"target": {"kind": "application"}}`,
			path:       "contract_version",
			read:       func(c config.Config) any { return c.ContractVersion },
			want:       config.ContractVersion,
			origin:     config.Origin{Source: config.SourceDefault},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			labels := map[string]string{"analysis.min_confidence": "--min-confidence"}
			cfg, provenance, err := config.Resolve(labelled(tc.flags, tc.repository, tc.central, labels))
			if err != nil {
				t.Fatalf("Resolve(%s) = error %v, want the resolved configuration", tc.name, err)
			}
			if got := tc.read(cfg); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Resolve(%s) resolved %s = %#v, want %#v", tc.name, tc.path, got, tc.want)
			}
			if got := provenance[tc.path]; got != tc.origin {
				t.Errorf("Resolve(%s) provenance[%q] = %q, want %q", tc.name, tc.path, got, tc.origin)
			}
		})
	}
}

func TestResolveWithoutARepositoryConfiguration(t *testing.T) {
	t.Parallel()

	cfg, provenance, err := config.Resolve(labelled("", "", `{"target": {"kind": "application"}}`, nil))
	if err != nil {
		t.Fatalf("Resolve(central only) = error %v, want the documented defaults", err)
	}

	want := config.Default()
	want.Target.Kind = config.Application
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Resolve(central only) = %+v, want the documented defaults with the declared kind %+v", cfg, want)
	}
	if got := provenance["target.kind"]; got.Source != config.SourceCentral {
		t.Errorf("Resolve(central only) provenance[target.kind] = %q, want the central configuration", got)
	}
	if got := provenance["reporters.formats"]; got.Source != config.SourceDefault {
		t.Errorf("Resolve(central only) provenance[reporters.formats] = %q, want the default", got)
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
			names:      []string{"target.kind", "deadset.json", "central.json"},
		},
		{
			name:  "no_source_present_at_all",
			names: []string{"target.kind", "not present"},
		},
		{
			name:       "only_a_repository_configuration_that_omits_it",
			repository: `{"reporters": {"sort": "size"}}`,
			names:      []string{"target.kind", "deadset.json", "the central configuration (not present)"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := config.Resolve(labelled("", tc.repository, tc.central, nil))
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(%s) = error %v, want a *config.Error", tc.name, err)
			}
			if refusal.Kind != config.KindMissingTargetKind {
				t.Errorf("Resolve(%s) = kind %v, want %v", tc.name, refusal.Kind, config.KindMissingTargetKind)
			}
			if refusal.Key != "target.kind" {
				t.Errorf("Resolve(%s) named %q, want %q", tc.name, refusal.Key, "target.kind")
			}
			assertMessageNames(t, tc.name, refusal, tc.names)
		})
	}
}

func TestResolveRefusesAnUnimplementedKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		flags      string
		repository string
		key        string
		names      []string
	}{
		{
			name:       "a_near_miss_of_a_setting_names_the_nearest_key",
			repository: `{"target": {"kind": "application"}, "reporters": {"fail_under": "warn"}}`,
			key:        "reporters.fail_under",
			names:      []string{`"reporters.fail_under"`, `"reporters.fail_on"`},
		},
		{
			name:       "a_dotted_path_written_as_one_key",
			repository: `{"target": {"kind": "application"}, "analysis.min_confidence": "certain"}`,
			key:        "analysis.min_confidence",
			names:      []string{`"analysis.min_confidence"`, "nested objects"},
		},
		{
			name:       "a_key_in_a_section_that_declares_none",
			repository: `{"target": {"kind": "application"}, "go": {"test_files": ["x"]}}`,
			key:        "go.test_files",
			names:      []string{`"go.test_files"`},
		},
		{
			name:       "a_key_in_one_entry_of_the_build_matrix",
			repository: `{"target": {"kind": "application"}, "analysis": {"configurations": [{"id": "a", "os": "linux", "arch": "amd64", "cgo": true}]}}`,
			key:        "analysis.configurations[0].cgo",
			names:      []string{`"analysis.configurations[0].cgo"`},
		},
		{
			name:       "a_flag_naming_no_setting",
			flags:      `{"analysis.min_confidences": "certain"}`,
			repository: `{"target": {"kind": "application"}}`,
			key:        "analysis.min_confidences",
			names:      []string{`"analysis.min_confidences"`, `"analysis.min_confidence"`},
		},
		{
			name:       "a_flag_naming_a_whole_section",
			flags:      `{"analysis": "certain"}`,
			repository: `{"target": {"kind": "application"}}`,
			key:        "analysis",
			names:      []string{`"analysis"`},
		},
		{
			name:       "a_severity_key_that_is_not_a_code",
			repository: `{"target": {"kind": "application"}, "severity": {"unused-exported": "warn"}}`,
			key:        "severity.unused-exported",
			names:      []string{`"severity.unused-exported"`, "family prefix"},
		},
		{
			name:       "a_severity_code_the_contract_fixes",
			repository: `{"target": {"kind": "application"}, "severity": {"DS1703": "allow"}}`,
			key:        "severity.DS1703",
			names:      []string{`"severity.DS1703"`, "fixes the severity of DS1703"},
		},
		{
			name:       "the_other_severity_code_the_contract_fixes",
			repository: `{"target": {"kind": "application"}, "severity": {"DS1704": "warn"}}`,
			key:        "severity.DS1704",
			names:      []string{`"severity.DS1704"`, "fixes the severity of DS1704"},
		},
		{
			name:       "a_severity_family_prefix_holding_a_fixed_code",
			repository: `{"target": {"kind": "application"}, "severity": {"DS17": "allow"}}`,
			key:        "severity.DS17",
			names:      []string{`"severity.DS17"`, "fixes the severity of DS1703 and DS1704"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := config.Resolve(labelled(tc.flags, tc.repository, "", nil))
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(%s) = error %v, want a *config.Error", tc.name, err)
			}
			if refusal.Kind != config.KindUnimplementedKey {
				t.Errorf("Resolve(%s) = kind %v, want %v", tc.name, refusal.Kind, config.KindUnimplementedKey)
			}
			if refusal.Key != tc.key {
				t.Errorf("Resolve(%s) named %q, want %q", tc.name, refusal.Key, tc.key)
			}
			assertMessageNames(t, tc.name, refusal, tc.names)
		})
	}
}

func TestResolveNamesEveryFixedCodeASeverityKeyCovers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repository string
		want       string
	}{
		{
			name:       "a_family_prefix_names_every_fixed_code_of_that_family",
			repository: `{"target": {"kind": "application"}, "severity": {"DS17": "allow"}}`,
			want: `deadset.json: key "severity.DS17" is not implemented: ` +
				"the Contract fixes the severity of DS1703 and DS1704, which this key names",
		},
		{
			name:       "a_code_names_that_code_alone",
			repository: `{"target": {"kind": "application"}, "severity": {"DS1704": "warn"}}`,
			want: `deadset.json: key "severity.DS1704" is not implemented: ` +
				"the Contract fixes the severity of DS1704, which this key names",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := config.Resolve(labelled("", tc.repository, "", nil))
			if err == nil {
				t.Fatalf("Resolve(%s) = no error, want the key refused", tc.repository)
			}
			if got := err.Error(); got != tc.want {
				t.Errorf("Resolve(%s) = %q, want %q", tc.repository, got, tc.want)
			}
		})
	}
}

func TestResolveRefusesADuplicateMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		flags      string
		repository string
		key        string
	}{
		{
			name:       "at_the_root",
			repository: `{"target": {"kind": "library"}, "severity": {"DS1101": "warn"}, "severity": {"DS1101": "deny"}}`,
			key:        "severity",
		},
		{
			name:       "inside_a_section",
			repository: `{"target": {"kind": "library"}, "reporters": {"sort": "size", "sort": "position"}}`,
			key:        "reporters.sort",
		},
		{
			name:       "inside_one_entry_of_the_build_matrix",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a", "id": "b", "os": "linux", "arch": "amd64"}]}}`,
			key:        "analysis.configurations[0].id",
		},
		{
			name:       "in_the_flag_document",
			flags:      `{"reporters.sort": "size", "reporters.sort": "position"}`,
			repository: `{"target": {"kind": "library"}}`,
			key:        "reporters.sort",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := config.Resolve(labelled(tc.flags, tc.repository, "", map[string]string{"reporters.sort": "--sort"}))
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(%s) = error %v, want a *config.Error", tc.name, err)
			}
			if refusal.Kind != config.KindMalformed {
				t.Errorf("Resolve(%s) = kind %v, want %v", tc.name, refusal.Kind, config.KindMalformed)
			}
			if refusal.Key != tc.key {
				t.Errorf("Resolve(%s) named %q, want %q", tc.name, refusal.Key, tc.key)
			}
			assertMessageNames(t, tc.name, refusal, []string{"twice"})
		})
	}
}

func TestResolveSeverityPerCode(t *testing.T) {
	t.Parallel()

	in := labelled(
		`{"severity.DS1801": "deny"}`,
		`{"target": {"kind": "library"}, "severity": {"DS1101": "allow", "DS1801": "warn"}}`,
		`{"severity": {"DS1101": "deny", "DS1201": "warn", "DS18": "allow"}}`,
		map[string]string{"severity.DS1801": "--severity"},
	)
	cfg, provenance, err := config.Resolve(in)
	if err != nil {
		t.Fatalf("Resolve(severity) = error %v, want the resolved configuration", err)
	}

	wantSeverity := map[string]config.Severity{
		"DS1101": config.Allow,
		"DS1201": config.Warn,
		"DS18":   config.Allow,
		"DS1801": config.Deny,
	}
	if !reflect.DeepEqual(cfg.Severity, wantSeverity) {
		t.Errorf("Resolve(severity) = %v, want %v", cfg.Severity, wantSeverity)
	}
	wantOrigins := map[string]config.Origin{
		"severity.DS1101": {Source: config.SourceRepository, Label: "deadset.json"},
		"severity.DS1201": {Source: config.SourceCentral, Label: "central.json"},
		"severity.DS18":   {Source: config.SourceCentral, Label: "central.json"},
		"severity.DS1801": {Source: config.SourceFlag, Label: "--severity"},
	}
	for path, want := range wantOrigins {
		if got := provenance[path]; got != want {
			t.Errorf("Resolve(severity) provenance[%q] = %q, want %q", path, got, want)
		}
	}
	if _, held := provenance["severity"]; held {
		t.Errorf("Resolve(severity) provenance holds %q, want one entry per code instead", "severity")
	}
}

func TestResolveSeverityWhenItResolvesEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repository string
		want       config.Origin
	}{
		{
			name:       "no_source_names_the_object",
			repository: `{"target": {"kind": "library"}}`,
			want:       config.Origin{Source: config.SourceDefault},
		},
		{
			name:       "the_repository_names_it_empty",
			repository: `{"target": {"kind": "library"}, "severity": {}}`,
			want:       config.Origin{Source: config.SourceRepository, Label: "deadset.json"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, provenance, err := config.Resolve(labelled("", tc.repository, "", nil))
			if err != nil {
				t.Fatalf("Resolve(%s) = error %v, want the resolved configuration", tc.name, err)
			}
			if len(cfg.Severity) != 0 {
				t.Errorf("Resolve(%s) = severity %v, want it empty", tc.name, cfg.Severity)
			}
			if got := provenance["severity"]; got != tc.want {
				t.Errorf("Resolve(%s) provenance[%q] = %q, want %q", tc.name, "severity", got, tc.want)
			}
		})
	}
}

func TestResolveRefusesAMalformedDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repository string
		key        string
	}{
		{
			name:       "a_value_outside_a_closed_set",
			repository: `{"target": {"kind": "module"}}`,
			key:        "target.kind",
		},
		{
			name:       "a_language_outside_the_closed_set",
			repository: `{"target": {"kind": "library"}, "analysis": {"languages": ["go", "rust"]}}`,
			key:        "analysis.languages",
		},
		{
			name:       "an_array_naming_one_entry_twice",
			repository: `{"target": {"kind": "library"}, "roots": {"patterns": ["go://a#B", "go://a#B"]}}`,
			key:        "roots.patterns",
		},
		{
			name:       "an_empty_format_list",
			repository: `{"target": {"kind": "library"}, "reporters": {"formats": []}}`,
			key:        "reporters.formats",
		},
		{
			name:       "a_cascade_rendering_outside_the_closed_set",
			repository: `{"target": {"kind": "library"}, "reporters": {"cascade": "members"}}`,
			key:        "reporters.cascade",
		},
		{
			name:       "a_negative_finding_cap",
			repository: `{"target": {"kind": "library"}, "reporters": {"max_findings": -1}}`,
			key:        "reporters.max_findings",
		},
		{
			name:       "a_contract_version_that_is_not_semantic",
			repository: `{"target": {"kind": "library"}, "contract_version": "1.0"}`,
			key:        "contract_version",
		},
		{
			name:       "an_exemption_class_that_is_not_a_class_name",
			repository: `{"target": {"kind": "library"}, "exemptions": {"disabled": ["TemplateField"]}}`,
			key:        "exemptions.disabled",
		},
		{
			name:       "a_build_matrix_entry_naming_no_architecture",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a", "os": "linux"}]}}`,
			key:        "analysis.configurations[0].arch",
		},
		{
			name:       "an_empty_test_file_pattern_list",
			repository: `{"target": {"kind": "library"}, "ts": {"test_files": []}}`,
			key:        "ts.test_files",
		},
		{
			name:       "a_severity_outside_the_closed_set",
			repository: `{"target": {"kind": "library"}, "severity": {"DS1101": "error"}}`,
			key:        "severity.DS1101",
		},
		{
			name:       "a_value_of_the_wrong_type",
			repository: `{"target": {"kind": "library"}, "consumers": {"complete": "yes"}}`,
			key:        "",
		},
		{
			name:       "a_second_document_after_the_first",
			repository: `{"target": {"kind": "library"}} {"target": {"kind": "application"}}`,
			key:        "",
		},
		{
			name:       "a_document_that_is_not_an_object",
			repository: `["target"]`,
			key:        "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := config.Resolve(labelled("", tc.repository, "", nil))
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(%s) = error %v, want a *config.Error", tc.name, err)
			}
			if refusal.Kind != config.KindMalformed {
				t.Errorf("Resolve(%s) = kind %v, want %v", tc.name, refusal.Kind, config.KindMalformed)
			}
			if refusal.Key != tc.key {
				t.Errorf("Resolve(%s) named %q, want %q", tc.name, refusal.Key, tc.key)
			}
			assertMessageNames(t, tc.name, refusal, []string{"deadset.json"})
		})
	}
}

func TestResolveIgnoresProvenanceOnInput(t *testing.T) {
	t.Parallel()

	withProvenance := `{
		"target": {"kind": "library"},
		"reporters": {"fail_on": "warn"},
		"provenance": {
			"target.kind": "flag: --target-kind",
			"reporters.fail_on": "central: elsewhere.json",
			"analysis.min_confidence": "default"
		}
	}`
	withoutProvenance := `{"target": {"kind": "library"}, "reporters": {"fail_on": "warn"}}`

	got, gotProvenance, err := config.Resolve(labelled("", withProvenance, "", nil))
	if err != nil {
		t.Fatalf("Resolve(a document carrying provenance) = error %v, want the resolved configuration", err)
	}
	want, wantProvenance, err := config.Resolve(labelled("", withoutProvenance, "", nil))
	if err != nil {
		t.Fatalf("Resolve(the same document without provenance) = error %v, want the resolved configuration", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve(a document carrying provenance) = %+v, want the same as without it %+v", got, want)
	}
	if !reflect.DeepEqual(gotProvenance, wantProvenance) {
		t.Errorf("Resolve(a document carrying provenance) provenance = %v, want %v", gotProvenance, wantProvenance)
	}
}

func TestResolveRequiresEverySourceToNameItself(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   config.Inputs
	}{
		{
			name: "a_repository_configuration_with_no_path",
			in:   config.Inputs{Repository: []byte(`{"target": {"kind": "library"}}`)},
		},
		{
			name: "a_central_configuration_with_no_path",
			in: config.Inputs{
				Repository:      []byte(`{"target": {"kind": "library"}}`),
				RepositoryLabel: "deadset.json",
				Central:         []byte(`{"reporters": {"sort": "size"}}`),
			},
		},
		{
			name: "a_flag_with_no_name",
			in: config.Inputs{
				Flags:           []byte(`{"reporters.sort": "size"}`),
				Repository:      []byte(`{"target": {"kind": "library"}}`),
				RepositoryLabel: "deadset.json",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := config.Resolve(tc.in)
			if !errors.Is(err, config.ErrNoSourceLabel) {
				t.Errorf("Resolve(%s) = error %v, want it to be %v", tc.name, err, config.ErrNoSourceLabel)
			}
		})
	}
}

// assertMessageNames checks that one refusal's message names everything the
// operator needs to act on it.
func assertMessageNames(t *testing.T, name string, refusal *config.Error, names []string) {
	t.Helper()

	for _, want := range names {
		if !strings.Contains(refusal.Error(), want) {
			t.Errorf("Resolve(%s) = %q, want the message to name %q", name, refusal.Error(), want)
		}
	}
}
