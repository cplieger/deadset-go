package config_test

import (
	"errors"
	"reflect"
	"slices"
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
			name:       "a_key_in_one_declaration_entry",
			repository: `{"target": {"kind": "application"}, "ts": {"serializers": [{"global": "JSON.stringify", "space": 2}]}}`,
			key:        "ts.serializers[0].space",
			names:      []string{`"ts.serializers[0].space"`},
		},
		{
			name:       "a_key_in_a_component_entry_of_a_lifecycle_contract",
			repository: `{"target": {"kind": "application"}, "ts": {"lifecycle_contracts": [{"components": [{"global": "CustomElementRegistry.define", "argument": 1}], "members": ["connectedCallback"]}]}}`,
			key:        "ts.lifecycle_contracts[0].components[0].argument",
			names:      []string{`"ts.lifecycle_contracts[0].components[0].argument"`},
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
		{
			name:       "a_severity_code_no_live_kind_carries",
			repository: `{"target": {"kind": "application"}, "severity": {"DS2999": "allow"}}`,
			key:        "severity.DS2999",
			names:      []string{`"severity.DS2999"`, "no issue kind this analyzer ships carries the code DS2999"},
		},
		{
			name:       "a_severity_code_of_a_live_family_that_no_kind_carries",
			repository: `{"target": {"kind": "application"}, "severity": {"DS1899": "warn"}}`,
			key:        "severity.DS1899",
			names:      []string{`"severity.DS1899"`, "no issue kind this analyzer ships carries the code DS1899"},
		},
		{
			name:       "a_retired_severity_code",
			repository: `{"target": {"kind": "application"}, "severity": {"DS1402": "warn"}}`,
			key:        "severity.DS1402",
			names:      []string{`"severity.DS1402"`, "no issue kind this analyzer ships carries the code DS1402"},
		},
		{
			name:       "a_severity_family_prefix_no_range_declares",
			repository: `{"target": {"kind": "application"}, "severity": {"DS29": "allow"}}`,
			key:        "severity.DS29",
			names:      []string{`"severity.DS29"`, "no issue kind this analyzer ships carries a code of the family DS29"},
		},
		{
			name:       "a_severity_family_prefix_whose_range_is_retired",
			repository: `{"target": {"kind": "application"}, "severity": {"DS14": "allow"}}`,
			key:        "severity.DS14",
			names:      []string{`"severity.DS14"`, "no issue kind this analyzer ships carries a code of the family DS14"},
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
		says       []string
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
			name:       "a_build_matrix_entry_naming_members_of_both_shapes",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a", "os": "linux", "arch": "amd64", "project": "tsconfig.json"}]}}`,
			key:        "analysis.configurations[0]",
		},
		{
			name:       "a_build_matrix_entry_naming_tags_beside_a_project",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a", "project": "tsconfig.json", "tags": []}]}}`,
			key:        "analysis.configurations[0]",
		},
		{
			name:       "a_build_matrix_entry_naming_neither_shape",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a"}]}}`,
			key:        "analysis.configurations[0]",
		},
		{
			name:       "a_project_entry_naming_no_identifier",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"project": "tsconfig.json"}]}}`,
			key:        "analysis.configurations[0].id",
		},
		{
			name:       "a_project_entry_leading_out_of_the_target_root",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a", "project": "../tsconfig.json"}]}}`,
			key:        "analysis.configurations[0].project",
		},
		{
			name:       "a_project_entry_naming_a_directory",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a", "project": "packages/app/"}]}}`,
			key:        "analysis.configurations[0].project",
		},
		{
			name:       "a_second_entry_naming_members_of_both_shapes",
			repository: `{"target": {"kind": "library"}, "analysis": {"configurations": [{"id": "a", "project": "tsconfig.json"}, {"id": "b", "os": "linux", "arch": "amd64", "project": "b.json"}]}}`,
			key:        "analysis.configurations[1]",
		},
		{
			name:       "an_empty_test_file_pattern_list",
			repository: `{"target": {"kind": "library"}, "ts": {"test_files": []}}`,
			key:        "ts.test_files",
		},
		{
			name:       "a_declaration_entry_naming_no_shape",
			repository: `{"target": {"kind": "library"}, "ts": {"serializers": [{}]}}`,
			key:        "ts.serializers[0]",
		},
		{
			name:       "a_declaration_entry_naming_members_of_two_shapes",
			repository: `{"target": {"kind": "library"}, "ts": {"injection_registrations": [{"symbol": "ts://@example/app/src/a.ts#A", "global": "B"}]}}`,
			key:        "ts.injection_registrations[0]",
		},
		{
			name:       "a_declaration_symbol_in_the_go_form",
			repository: `{"target": {"kind": "library"}, "ts": {"serializers": [{"symbol": "go://example.com/app#Encode"}]}}`,
			key:        "ts.serializers[0].symbol",
		},
		{
			name:       "a_declaration_module_naming_no_export_path",
			repository: `{"target": {"kind": "library"}, "ts": {"injection_registrations": [{"module": "@example/container"}]}}`,
			key:        "ts.injection_registrations[0].name",
		},
		{
			name:       "a_declaration_export_path_naming_no_module",
			repository: `{"target": {"kind": "library"}, "ts": {"injection_registrations": [{"name": "Container.bind"}]}}`,
			key:        "ts.injection_registrations[0].module",
		},
		{
			name:       "a_declaration_module_written_as_a_relative_specifier",
			repository: `{"target": {"kind": "library"}, "ts": {"serializers": [{"module": "./wire.js", "name": "encode"}]}}`,
			key:        "ts.serializers[0].module",
		},
		{
			name:       "a_declaration_module_mapped_by_the_imports_field",
			repository: `{"target": {"kind": "library"}, "ts": {"serializers": [{"module": "#wire", "name": "encode"}]}}`,
			key:        "ts.serializers[0].module",
		},
		{
			name:       "a_declaration_naming_an_empty_global_path",
			repository: `{"target": {"kind": "library"}, "ts": {"serializers": [{"global": ""}]}}`,
			key:        "ts.serializers[0].global",
		},
		{
			name:       "a_second_declaration_entry_naming_no_shape",
			repository: `{"target": {"kind": "library"}, "ts": {"serializers": [{"global": "structuredClone"}, {}]}}`,
			key:        "ts.serializers[1]",
		},
		{
			name:       "a_lifecycle_contract_naming_no_members",
			repository: `{"target": {"kind": "library"}, "ts": {"lifecycle_contracts": [{"bases": [{"global": "HTMLElement"}]}]}}`,
			key:        "ts.lifecycle_contracts[0].members",
			says:       []string{"is required"},
		},
		{
			name:       "a_lifecycle_contract_naming_an_empty_member_list",
			repository: `{"target": {"kind": "library"}, "ts": {"lifecycle_contracts": [{"bases": [{"global": "HTMLElement"}], "members": []}]}}`,
			key:        "ts.lifecycle_contracts[0].members",
			says:       []string{"want at least 1"},
		},
		{
			name:       "a_lifecycle_contract_naming_one_member_twice",
			repository: `{"target": {"kind": "library"}, "ts": {"lifecycle_contracts": [{"bases": [{"global": "HTMLElement"}], "members": ["connectedCallback", "connectedCallback"]}]}}`,
			key:        "ts.lifecycle_contracts[0].members",
		},
		{
			name:       "a_lifecycle_contract_whose_component_routes_are_empty",
			repository: `{"target": {"kind": "library"}, "ts": {"lifecycle_contracts": [{"components": [], "bases": [], "members": ["connectedCallback"]}]}}`,
			key:        "ts.lifecycle_contracts[0]",
		},
		{
			name:       "a_lifecycle_component_naming_no_shape",
			repository: `{"target": {"kind": "library"}, "ts": {"lifecycle_contracts": [{"components": [{}], "members": ["connectedCallback"]}]}}`,
			key:        "ts.lifecycle_contracts[0].components[0]",
		},
		{
			name:       "a_lifecycle_base_written_as_a_relative_specifier",
			repository: `{"target": {"kind": "library"}, "ts": {"lifecycle_contracts": [{"bases": [{"module": "../base.js", "name": "Base"}], "members": ["connectedCallback"]}]}}`,
			key:        "ts.lifecycle_contracts[0].bases[0].module",
		},
		{
			name:       "a_severity_outside_the_closed_set",
			repository: `{"target": {"kind": "library"}, "severity": {"DS1101": "error"}}`,
			key:        "severity.DS1101",
		},
		{
			name:       "a_value_of_the_wrong_type",
			repository: `{"target": {"kind": "library"}, "consumers": {"complete": "yes"}}`,
			key:        "consumers.complete",
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
			assertMessageNames(t, tc.name, refusal, append([]string{"deadset.json"}, tc.says...))
		})
	}
}

// enumeratedSetting is one setting the Contract's configuration schema declares a
// closed set of values for: the dotted path a document writes a value at, the
// malformed value it writes there, and the path the refusal names, which for the
// severity object is the member rather than the object.
type enumeratedSetting struct {
	value any
	at    string
	names string
}

// valueOutsideEveryClosedSet is a value no closed set of the configuration schema
// declares, so writing it at an enumerated setting is refused wherever that setting
// is.
const valueOutsideEveryClosedSet = "no-such-value"

// openObjectSamples names, per open object of the configuration schema, one member
// name matching the pattern that object declares, which is how a document reaches
// the enumerated value inside one. The derivation fails on an open object with no
// sample rather than passing over it.
func openObjectSamples() map[string]string {
	return map[string]string{"severity": "DS1101"}
}

// enumeratedSettings returns every setting the Contract's configuration schema
// declares a closed set of values for, derived from the schema's own enums so a
// closed set the Contract adds is exercised without a test edit.
func enumeratedSettings(t *testing.T) []enumeratedSetting {
	t.Helper()

	var found []enumeratedSetting
	collectEnums(t, configSchema(t), "", &found)
	if len(found) == 0 {
		t.Fatal("config.schema.json declares no closed set of values, so this test asserts nothing")
	}
	return found
}

// collectEnums appends one case per closed set declared at or below one
// declaration: at the value itself, at an array's entries, or at the value of an
// open object's member.
func collectEnums(t *testing.T, declaration map[string]any, at string, into *[]enumeratedSetting) {
	t.Helper()

	if _, held := declaration["enum"]; held && at != "" {
		*into = append(*into, enumeratedSetting{at: at, value: valueOutsideEveryClosedSet, names: at})
	}
	if entry, isObject := declaration["items"].(map[string]any); isObject {
		if _, held := entry["enum"]; held {
			*into = append(*into, enumeratedSetting{
				at:    at,
				value: []any{valueOutsideEveryClosedSet},
				names: at,
			})
		}
		if members, holds := entry["properties"]; holds {
			collectEnumsUnderList(t, at, members)
		}
	}
	for name, member := range declaredMembers(t, declaration, "properties", at) {
		collectEnums(t, member, joinTestKey(at, name), into)
	}
	for pattern, member := range declaredMembers(t, declaration, "patternProperties", at) {
		if _, held := member["enum"]; !held {
			continue
		}
		sample, named := openObjectSamples()[at]
		if !named {
			t.Fatalf("config.schema.json: %s declares a closed set under the pattern %s and this test names no member of it",
				at, pattern)
		}
		*into = append(*into, enumeratedSetting{
			at:    joinTestKey(at, sample),
			value: valueOutsideEveryClosedSet,
			names: joinTestKey(at, sample),
		})
	}
}

// collectEnumsUnderList fails on a closed set declared inside one entry of a list,
// which this derivation builds no document for, so such a set is refused here
// rather than passed over.
func collectEnumsUnderList(t *testing.T, at string, members any) {
	t.Helper()

	for name, member := range declaredMembers(t, map[string]any{"properties": members}, "properties", at) {
		_, direct := member["enum"]
		entry, isObject := member["items"].(map[string]any)
		_, perEntry := entry["enum"]
		if direct || (isObject && perEntry) {
			t.Fatalf("config.schema.json: %s[].%s declares a closed set and this test builds no document naming one entry of a list",
				at, name)
		}
	}
}

// declaredMembers returns the members one keyword of a declaration declares, empty
// where it declares none.
func declaredMembers(t *testing.T, declaration map[string]any, keyword, at string) map[string]map[string]any {
	t.Helper()

	held, carries := declaration[keyword]
	if !carries {
		return nil
	}
	declared, isObject := held.(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s: %s is %T, want an object", at, keyword, held)
	}
	members := make(map[string]map[string]any, len(declared))
	for name, member := range declared {
		asObject, isObject := member.(map[string]any)
		if !isObject {
			t.Fatalf("config.schema.json: %s: %s.%s is %T, want an object", at, keyword, name, member)
		}
		members[name] = asObject
	}
	return members
}

// joinTestKey spells the dotted path of one member of the value at at.
func joinTestKey(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

func TestResolveRefusesEveryEnumeratedSettingsMalformedValue(t *testing.T) {
	t.Parallel()

	found := enumeratedSettings(t)
	// The settings the schema enumerates today. A closed set the Contract adds
	// joins the table above without a test edit; one this derivation stops
	// reaching fails here.
	for _, want := range []string{
		"target.kind", "analysis.languages", "analysis.min_confidence", "analysis.generated_files",
		"analysis.consumer_tests", "severity.DS1101", "reporters.formats", "reporters.sort",
		"reporters.cascade", "reporters.fail_on",
	} {
		if !slices.ContainsFunc(found, func(setting enumeratedSetting) bool { return setting.names == want }) {
			t.Errorf("enumeratedSettings() = %v, want it to name %q", names(found), want)
		}
	}

	for _, setting := range found {
		t.Run(setting.names, func(t *testing.T) {
			t.Parallel()

			document := map[string]any{"target": map[string]any{"kind": string(config.Library)}}
			nestInto(t, document, setting.at, setting.value)

			_, _, err := config.Resolve(labelled("", string(encodeDocument(t, document)), "", nil))
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(%s = %v) = error %v, want a *config.Error", setting.at, setting.value, err)
			}
			if refusal.Kind != config.KindMalformed {
				t.Errorf("Resolve(%s = %v) = kind %v, want %v",
					setting.at, setting.value, refusal.Kind, config.KindMalformed)
			}
			if refusal.Key != setting.names {
				t.Errorf("Resolve(%s = %v) named %q, want %q",
					setting.at, setting.value, refusal.Key, setting.names)
			}
			assertMessageNames(t, setting.names, refusal, []string{valueOutsideEveryClosedSet})
		})
	}
}

// names lists the settings one derivation found, for a failure to name what it
// holds.
func names(settings []enumeratedSetting) []string {
	found := make([]string, 0, len(settings))
	for _, setting := range settings {
		found = append(found, setting.names)
	}
	slices.Sort(found)
	return found
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

func TestResolveWritesTheDefaultRouteOfALifecycleContractItOmits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contract string
		want     config.LifecycleContract
	}{
		{
			name:     "a_contract_naming_components_alone",
			contract: `{"components": [{"global": "CustomElementRegistry.define"}], "members": ["connectedCallback"]}`,
			want: config.LifecycleContract{
				Components: []config.Declaration{{Global: new("CustomElementRegistry.define")}},
				Bases:      []config.Declaration{},
				Members:    []string{"connectedCallback"},
			},
		},
		{
			name:     "a_contract_naming_bases_alone",
			contract: `{"bases": [{"global": "HTMLElement"}], "members": ["observedAttributes:static"]}`,
			want: config.LifecycleContract{
				Components: []config.Declaration{},
				Bases:      []config.Declaration{{Global: new("HTMLElement")}},
				Members:    []string{"observedAttributes:static"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repository := `{"target": {"kind": "library"}, "ts": {"lifecycle_contracts": [` + tc.contract + `]}}`
			cfg, _, err := config.Resolve(labelled("", repository, "", nil))
			if err != nil {
				t.Fatalf("Resolve(%s) = error %v, want the resolved configuration", tc.contract, err)
			}
			want := []config.LifecycleContract{tc.want}
			if got := cfg.TS.LifecycleContracts; !reflect.DeepEqual(got, want) {
				t.Errorf("Resolve(%s).TS.LifecycleContracts = %#v, want %#v", tc.contract, got, want)
			}
		})
	}
}

// A flag document is decoded without the member walk a configuration file goes
// through, so a declaration entry supplied as a flag is held to the members its
// shapes declare by the decoder alone.
func TestResolveRefusesAFlagSuppliedDeclarationNamingAnUndeclaredMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setting string
		value   string
	}{
		{name: "a_serializer", setting: "ts.serializers", value: `[{"global": "structuredClone", "returns": "string"}]`},
		{
			name:    "a_lifecycle_contract",
			setting: "ts.lifecycle_contracts",
			value:   `[{"framework": "elements", "bases": [{"global": "HTMLElement"}], "members": ["connectedCallback"]}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			flags := `{"` + tc.setting + `": ` + tc.value + `}`
			_, _, err := config.Resolve(labelled(flags, `{"target": {"kind": "application"}}`, "",
				map[string]string{tc.setting: "--" + tc.name}))
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(a flag supplying %s = %s) = error %v, want a *config.Error", tc.setting, tc.value, err)
			}
			if refusal.Kind != config.KindMalformed {
				t.Errorf("Resolve(a flag supplying %s = %s) = kind %v, want %v",
					tc.setting, tc.value, refusal.Kind, config.KindMalformed)
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

func TestResolveRefusesAProviderEntryOfNeitherShape(t *testing.T) {
	t.Parallel()

	const digest = `"sha256:4444444444444444444444444444444444444444444444444444444444444444"`
	tests := []struct {
		name  string
		entry string
		key   string
	}{
		{
			name:  "a_name_that_is_not_hyphenated_lowercase_words",
			entry: `{"name": "Example_Go", "languages": ["go"], "command": "example-go"}`,
			key:   "providers.analyzers[0].name",
		},
		{
			name:  "no_language",
			entry: `{"name": "example-go", "languages": [], "command": "example-go"}`,
			key:   "providers.analyzers[0].languages",
		},
		{
			name:  "a_language_no_kind_carries",
			entry: `{"name": "example-go", "languages": ["rust"], "command": "example-go"}`,
			key:   "providers.analyzers[0].languages",
		},
		{
			name:  "no_command",
			entry: `{"name": "example-go", "languages": ["go"]}`,
			key:   "providers.analyzers[0].command",
		},
		{
			name:  "a_source_without_its_version_and_digest",
			entry: `{"name": "example-go", "languages": ["go"], "command": "example-go", "source": "go:example.com/x"}`,
			key:   "providers.analyzers[0]",
		},
		{
			name: "a_version_with_a_leading_v",
			entry: `{"name": "example-go", "languages": ["go"], "command": "example-go", ` +
				`"source": "go:example.com/x", "version": "v1.4.0", "digest": ` + digest + `}`,
			key: "providers.analyzers[0].version",
		},
		{
			name: "a_source_of_another_scheme",
			entry: `{"name": "example-go", "languages": ["go"], "command": "example-go", ` +
				`"source": "https://example.com/x", "version": "1.4.0", "digest": ` + digest + `}`,
			key: "providers.analyzers[0].source",
		},
		{
			name: "a_digest_in_uppercase",
			entry: `{"name": "example-go", "languages": ["go"], "command": "example-go", ` +
				`"source": "go:example.com/x", "version": "1.4.0", "digest": "sha256:` +
				strings.Repeat("A", 64) + `"}`,
			key: "providers.analyzers[0].digest",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			document := `{"target": {"kind": "library"}, "providers": {"analyzers": [` + tc.entry + `]}}`
			_, _, err := config.Resolve(labelled("", document, "", nil))
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(%s) = error %v, want a *config.Error", tc.name, err)
			}
			if refusal.Key != tc.key {
				t.Errorf("Resolve(%s) named %q, want %q", tc.name, refusal.Key, tc.key)
			}
			assertMessageNames(t, tc.name, refusal, []string{"deadset.json", tc.key})
		})
	}
}

func TestResolveAcceptsBothProviderShapes(t *testing.T) {
	t.Parallel()

	const document = `{"target": {"kind": "library"}, "providers": {"analyzers": [
  {"name": "deadset-ts", "languages": ["ts"], "command": "deadset-ts"},
  {"name": "example-go", "languages": ["go"], "command": "/opt/example-go/bin/example-go",
   "source": "npm:@example/go", "version": "1.4.0-rc.1+build.7",
   "digest": "sha256:4444444444444444444444444444444444444444444444444444444444444444"}
]}}`
	cfg, provenance, err := config.Resolve(labelled("", document, "", nil))
	if err != nil {
		t.Fatalf("Resolve(two provider shapes) = error %v, want the resolved configuration", err)
	}
	if got := len(cfg.Providers.Analyzers); got != 2 {
		t.Fatalf("Resolve(two provider shapes) resolved %d providers, want 2", got)
	}
	if got := cfg.Providers.Analyzers[1].Digest; got == nil {
		t.Errorf("Resolve(two provider shapes) dropped the acquirable entry's digest")
	}
	if got := provenance["providers.analyzers"].String(); got != "repository: deadset.json" {
		t.Errorf("Resolve(two provider shapes) provenance of providers.analyzers = %q, want %q",
			got, "repository: deadset.json")
	}
}
