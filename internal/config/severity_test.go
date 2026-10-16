package config

import (
	"testing"

	"github.com/cplieger/deadset-spec/v8/report"
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

// configFor returns a configuration of one target kind, with the
// severity object and the consumer declaration a case carries.
func configFor(of report.TargetKind, consumersComplete bool, severity map[string]vocab.Severity) Config {
	cfg := Config{Severity: map[string]vocab.Severity{}}
	cfg.Target.Kind = of
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
		of       report.TargetKind
		want     vocab.Severity
		complete bool
	}{
		{
			name: "a_kind_no_source_names_takes_the_contract_default",
			of:   report.TargetKindApplication, code: "DS1001", want: vocab.Deny,
		},
		{
			name: "a_kind_the_contract_defaults_to_warn",
			of:   report.TargetKindApplication, code: "DS1501", want: vocab.Warn,
		},
		{
			name:     "the_configuration_key_for_the_code_outranks_the_default",
			severity: map[string]vocab.Severity{"DS1001": vocab.Warn},
			of:       report.TargetKindApplication, code: "DS1001", want: vocab.Warn,
		},
		{
			name:     "the_family_key_sets_every_kind_of_that_family",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow},
			of:       report.TargetKindApplication, code: "DS1401", want: vocab.Allow,
		},
		{
			name:     "the_family_key_reaches_a_deny_kind_of_the_same_family",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow},
			of:       report.TargetKindApplication, code: "DS1405", want: vocab.Allow,
		},
		{
			name:     "the_family_key_reaches_no_other_family",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow},
			of:       report.TargetKindApplication, code: "DS1001", want: vocab.Deny,
		},
		{
			name:     "the_code_key_outranks_the_family_key",
			severity: map[string]vocab.Severity{intraFunctionFamily: vocab.Allow, "DS1401": vocab.Deny},
			of:       report.TargetKindApplication, code: "DS1401", want: vocab.Deny,
		},

		{
			name: "a_library_keeps_the_unused_export_default",
			of:   report.TargetKindLibrary, code: "DS1001", want: vocab.Deny,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_the_narrowing_kinds",
			of:   report.TargetKindLibrary, code: "DS1501", want: vocab.Warn,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_every_other_kind",
			of:   report.TargetKindLibrary, code: "DS1002", want: vocab.Deny,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_test_only_use",
			of:   report.TargetKindLibrary, code: "DS1102", want: vocab.Deny,
		},

		{
			name: "an_application_reads_no_consumer_fact",
			of:   report.TargetKindApplication, code: "DS1001", want: vocab.Deny,
		},
		{
			name: "a_kind_whose_severity_the_contract_fixes",
			of:   report.TargetKindLibrary, code: "DS9003", want: vocab.Deny,
		},
		{
			name: "a_code_no_live_kind_carries",
			of:   report.TargetKindApplication, code: "DS2999", want: vocab.Allow,
		},
		{
			name: "a_code_of_a_reserved_family",
			of:   report.TargetKindApplication, code: "DS1801", want: vocab.Allow,
		},
		{
			name: "a_family_prefix_is_not_a_kind",
			of:   report.TargetKindApplication, code: vocab.Code(intraFunctionFamily), want: vocab.Allow,
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

	cfg := configFor(report.TargetKindApplication, false, nil)
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

	cfg := configFor(report.TargetKindApplication, false, map[string]vocab.Severity{intraFunctionFamily: vocab.Allow})
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
