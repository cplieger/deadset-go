package config

import (
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// intraFunctionFamily is the family prefix addressing the kinds that report dead
// code inside a function body, which is the one family a configuration is expected
// to disable whole.
const intraFunctionFamily = "DS14"

// publishedKinds returns the issue kinds the vocabulary publishes as live, in the
// order it lists them, with the default each carries. The vocabulary's own suite
// pins those rows equal to the Contract's issue-kind document, so this package
// reads the rows rather than the document.
func publishedKinds(t *testing.T) []vocab.Kind {
	t.Helper()

	rows := vocab.Kinds()
	if len(rows) == 0 {
		t.Fatal("Setup: the vocabulary publishes no live kind, so this test pins nothing")
	}
	return rows
}

func TestNamesLiveKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "a_live_code", key: "DS1001", want: true},
		{name: "a_family_prefix_holding_live_kinds", key: intraFunctionFamily, want: true},
		{name: "a_family_prefix_holding_one_live_kind", key: "DS13", want: true},
		{name: "a_code_of_a_live_family_that_no_kind_carries", key: "DS1499", want: false},
		{name: "a_code_outside_every_family", key: "DS2999", want: false},
		{name: "a_family_prefix_no_range_declares", key: "DS29", want: false},
		{name: "a_family_prefix_whose_range_is_reserved", key: "DS18", want: false},
		{name: "a_code_of_a_reserved_family", key: "DS1801", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := namesLiveKind(tc.key); got != tc.want {
				t.Errorf("namesLiveKind(%q) = %t, want %t", tc.key, got, tc.want)
			}
		})
	}
}

// configFor returns the default configuration for one target kind, with the
// severity object and the consumer declaration a case carries.
func configFor(of TargetKind, consumersComplete bool, severity map[string]vocab.Severity) Config {
	cfg := Default()
	cfg.Target.Kind = OneKind(of)
	cfg.Consumers.Complete = consumersComplete
	if severity != nil {
		cfg.Severity = severity
	}
	return cfg
}

func TestEffectiveSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		severity map[string]vocab.Severity
		code     vocab.Code
		of       TargetKind
		want     vocab.Severity
		complete bool
	}{
		{
			name: "a_kind_no_source_names_takes_the_contract_default",
			of:   Application, code: "DS1001", want: vocab.Deny,
		},
		{
			name: "a_kind_the_contract_defaults_to_warn",
			of:   Application, code: "DS1501", want: vocab.Warn,
		},
		{
			name:     "the_configuration_key_for_the_code_outranks_the_default",
			severity: map[string]vocab.Severity{"DS1001": vocab.Warn},
			of:       Application, code: "DS1001", want: vocab.Warn,
		},
		{
			name:     "the_family_key_sets_every_kind_of_that_family",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow},
			of:       Application, code: "DS1401", want: vocab.Allow,
		},
		{
			name:     "the_family_key_reaches_a_deny_kind_of_the_same_family",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow},
			of:       Application, code: "DS1405", want: vocab.Allow,
		},
		{
			name:     "the_family_key_reaches_no_other_family",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow},
			of:       Application, code: "DS1001", want: vocab.Deny,
		},
		{
			name:     "the_code_key_outranks_the_family_key",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow, "DS1401": vocab.Deny},
			of:       Application, code: "DS1401", want: vocab.Deny,
		},

		{
			name: "a_library_keeps_the_unused_export_default",
			of:   Library, code: "DS1001", want: vocab.Deny,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_the_narrowing_kinds",
			of:   Library, code: "DS1501", want: vocab.Warn,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_every_other_kind",
			of:   Library, code: "DS1002", want: vocab.Deny,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_test_only_use",
			of:   Library, code: "DS1102", want: vocab.Deny,
		},

		{
			name: "an_application_reads_no_consumer_fact",
			of:   Application, code: "DS1001", want: vocab.Deny,
		},
		{
			name: "a_kind_whose_severity_the_contract_fixes",
			of:   Library, code: "DS9003", want: vocab.Deny,
		},
		{
			name: "a_code_no_live_kind_carries",
			of:   Application, code: "DS2999", want: vocab.Allow,
		},
		{
			name: "a_code_of_a_reserved_family",
			of:   Application, code: "DS1801", want: vocab.Allow,
		},
		{
			name: "a_family_prefix_is_not_a_kind",
			of:   Application, code: vocab.Code(intraFunctionFamily), want: vocab.Allow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := configFor(tc.of, tc.complete, tc.severity)
			if got := cfg.EffectiveSeverity(tc.code); got != tc.want {
				t.Errorf("EffectiveSeverity(%q) with target kind %q, severity %v and consumers.complete %t = %q, want %q",
					tc.code, tc.of, tc.severity, tc.complete, got, tc.want)
			}
		})
	}
}

func TestEffectiveSeverityIsTheVocabularysDefaultWhenNothingNamesTheKind(t *testing.T) {
	t.Parallel()

	cfg := configFor(Application, false, nil)
	for _, published := range publishedKinds(t) {
		want := published.DefaultSeverity
		if !published.DefaultEnabled {
			want = vocab.Allow
		}
		if got := cfg.EffectiveSeverity(published.Code); got != want {
			t.Errorf("EffectiveSeverity(%q) over an application naming no severity = %q, want the vocabulary's %q",
				published.Code, got, want)
		}
	}
}

func TestEffectiveSeverityDisablesTheFamilyTheConfigurationNamesAndNoOther(t *testing.T) {
	t.Parallel()

	cfg := configFor(Application, false, map[string]vocab.Severity{intraFunctionFamily: vocab.Allow})
	disabled := 0
	for _, published := range publishedKinds(t) {
		want := published.DefaultSeverity
		if vocab.FamilyOf(published.Code) == intraFunctionFamily {
			want = vocab.Allow
			disabled++
		}
		if got := cfg.EffectiveSeverity(published.Code); got != want {
			t.Errorf("EffectiveSeverity(%q) under severity %q = %q, want %q",
				published.Code, intraFunctionFamily, got, want)
		}
	}
	if disabled == 0 {
		t.Errorf("no live kind carries the family prefix %q, so this test pins nothing", intraFunctionFamily)
	}
}
