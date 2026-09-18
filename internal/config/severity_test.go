package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	spec "github.com/cplieger/deadset-spec"
)

// intraFunctionFamily is the family prefix addressing the kinds that report dead
// code inside a function body, which is the one family a configuration is expected
// to disable whole.
const intraFunctionFamily = "DS18"

// publishedKinds returns the issue kinds the Contract publishes as live, in the
// order it lists them, with the default each carries.
func publishedKinds(t *testing.T) []kind {
	t.Helper()

	var document struct {
		Kinds []struct {
			Code     string   `json:"code"`
			Severity Severity `json:"default_severity"`
			Enabled  bool     `json:"default_enabled"`
			Fixed    bool     `json:"fixed"`
		} `json:"kinds"`
	}
	decodeContract(t, "kinds.json", &document)

	published := make([]kind, 0, len(document.Kinds))
	for _, row := range document.Kinds {
		published = append(published, kind{
			code:     row.Code,
			severity: row.Severity,
			enabled:  row.Enabled,
			fixed:    row.Fixed,
		})
	}
	if len(published) == 0 {
		t.Fatal("kinds.json publishes no live kind, so this test pins nothing")
	}
	return published
}

// retiredCodes returns the codes the Contract records as retired, which name no
// live kind.
func retiredCodes(t *testing.T) []string {
	t.Helper()

	var document struct {
		Retired []struct {
			Code string `json:"code"`
		} `json:"retired"`
	}
	decodeContract(t, "kinds.json", &document)

	codes := make([]string, 0, len(document.Retired))
	for _, row := range document.Retired {
		codes = append(codes, row.Code)
	}
	if len(codes) == 0 {
		t.Fatal("kinds.json retires no code, so this test pins nothing")
	}
	return codes
}

// decodeContract decodes one document of the Contract into the shape a test reads
// from it.
func decodeContract(t *testing.T, name string, into any) {
	t.Helper()

	data, err := spec.Contract.ReadFile("contract/" + name)
	if err != nil {
		t.Fatalf("read contract/%s: %v", name, err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("decode contract/%s: %v", name, err)
	}
}

func TestLiveKindsEqualTheContract(t *testing.T) {
	t.Parallel()

	want := publishedKinds(t)
	got := liveKinds()
	if len(got) != len(want) {
		t.Fatalf("liveKinds() holds %d kinds, want the %d kinds kinds.json publishes as live",
			len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("liveKinds()[%d] = %+v, want kinds.json's %+v", index, got[index], want[index])
		}
	}
}

func TestLiveKindsNameNoRetiredCode(t *testing.T) {
	t.Parallel()

	for _, code := range retiredCodes(t) {
		if _, live := liveKind(code); live {
			t.Errorf("liveKind(%q) reports a live kind, want none: kinds.json retires the code", code)
		}
		if namesLiveKind(code) {
			t.Errorf("namesLiveKind(%q) = true, want false: kinds.json retires the code", code)
		}
	}
}

func TestFixedSeverityCodesEqualTheContract(t *testing.T) {
	t.Parallel()

	var want []string
	for _, published := range publishedKinds(t) {
		if published.fixed {
			want = append(want, published.code)
		}
	}
	slices.Sort(want)
	if len(want) == 0 {
		t.Fatal("kinds.json fixes no severity, so this test pins nothing")
	}

	got := fixedSeverityCodes()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("fixedSeverityCodes() = %v, want the codes kinds.json fixes %v", got, want)
	}
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
		{name: "a_family_prefix_holding_one_live_kind", key: "DS16", want: true},
		{name: "a_code_of_a_live_family_that_no_kind_carries", key: "DS1899", want: false},
		{name: "a_code_outside_every_family", key: "DS2999", want: false},
		{name: "a_family_prefix_no_range_declares", key: "DS29", want: false},
		{name: "a_family_prefix_whose_range_is_retired", key: "DS14", want: false},
		{name: "a_retired_code", key: "DS1402", want: false},
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

func TestFamilyPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		want string
	}{
		{name: "a_code", code: "DS1801", want: intraFunctionFamily},
		{name: "a_family_prefix_is_its_own", code: intraFunctionFamily, want: intraFunctionFamily},
		{name: "a_key_shorter_than_a_family_prefix", code: "DS", want: "DS"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := familyPrefix(tc.code); got != tc.want {
				t.Errorf("familyPrefix(%q) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// configFor returns the default configuration for one target kind, with the
// severity object and the consumer declaration a case carries.
func configFor(of TargetKind, consumersComplete bool, severity map[string]Severity) Config {
	cfg := Default()
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
		name            string
		severity        map[string]Severity
		code            string
		of              TargetKind
		want            Severity
		complete        bool
		consumersLoaded bool
	}{
		{
			name: "a_kind_no_source_names_takes_the_contract_default",
			of:   Application, code: "DS1001", want: Deny,
		},
		{
			name: "a_kind_the_contract_defaults_to_warn",
			of:   Application, code: "DS1101", want: Warn,
		},
		{
			name:     "the_configuration_key_for_the_code_outranks_the_default",
			severity: map[string]Severity{"DS1001": Warn},
			of:       Application, code: "DS1001", want: Warn,
		},
		{
			name:     "the_family_key_sets_every_kind_of_that_family",
			severity: map[string]Severity{intraFunctionFamily: Allow},
			of:       Application, code: "DS1801", want: Allow,
		},
		{
			name:     "the_family_key_reaches_a_deny_kind_of_the_same_family",
			severity: map[string]Severity{intraFunctionFamily: Allow},
			of:       Application, code: "DS1805", want: Allow,
		},
		{
			name:     "the_family_key_reaches_no_other_family",
			severity: map[string]Severity{intraFunctionFamily: Allow},
			of:       Application, code: "DS1001", want: Deny,
		},
		{
			name:     "the_code_key_outranks_the_family_key",
			severity: map[string]Severity{intraFunctionFamily: Allow, "DS1801": Deny},
			of:       Application, code: "DS1801", want: Deny,
		},
		{
			name: "a_library_with_no_consumer_information_reports_no_unused_export",
			of:   Library, code: "DS1001", want: Allow,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_the_narrowing_kinds",
			of:   Library, code: "DS1101", want: Warn,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_every_other_kind",
			of:   Library, code: "DS1002", want: Deny,
		},
		{
			name: "a_library_with_no_consumer_information_keeps_test_only_use",
			of:   Library, code: "DS1004", want: Deny,
		},
		{
			name: "a_library_declaring_its_consumer_set_complete_that_loaded_none",
			of:   Library, complete: true, code: "DS1001", want: Allow,
		},
		{
			name: "a_library_whose_every_declared_consumer_loaded",
			of:   Library, complete: true, consumersLoaded: true, code: "DS1001", want: Deny,
		},
		{
			name: "a_library_that_loaded_consumers_it_never_declared_complete",
			of:   Library, consumersLoaded: true, code: "DS1001", want: Allow,
		},
		{
			name:     "a_library_naming_the_kind_outranks_the_library_default",
			severity: map[string]Severity{"DS1001": Deny},
			of:       Library, code: "DS1001", want: Deny,
		},
		{
			name: "an_application_reads_no_consumer_fact",
			of:   Application, code: "DS1001", want: Deny,
		},
		{
			name: "a_kind_whose_severity_the_contract_fixes",
			of:   Library, code: "DS1703", want: Deny,
		},
		{
			name: "a_code_no_live_kind_carries",
			of:   Application, code: "DS2999", want: Allow,
		},
		{
			name: "a_retired_code",
			of:   Application, code: "DS1402", want: Allow,
		},
		{
			name: "a_family_prefix_is_not_a_kind",
			of:   Application, code: intraFunctionFamily, want: Allow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := configFor(tc.of, tc.complete, tc.severity)
			if got := cfg.EffectiveSeverity(tc.code, tc.consumersLoaded); got != tc.want {
				t.Errorf("EffectiveSeverity(%q, %t) with target kind %q, severity %v and consumers.complete %t = %q, want %q",
					tc.code, tc.consumersLoaded, tc.of, tc.severity, tc.complete, got, tc.want)
			}
		})
	}
}

func TestEffectiveSeverityIsTheContractDefaultWhenNothingNamesTheKind(t *testing.T) {
	t.Parallel()

	cfg := configFor(Application, false, nil)
	for _, published := range publishedKinds(t) {
		want := published.severity
		if !published.enabled {
			want = Allow
		}
		if got := cfg.EffectiveSeverity(published.code, false); got != want {
			t.Errorf("EffectiveSeverity(%q, false) over an application naming no severity = %q, want kinds.json's %q",
				published.code, got, want)
		}
	}
}

func TestEffectiveSeverityDisablesTheFamilyTheConfigurationNamesAndNoOther(t *testing.T) {
	t.Parallel()

	cfg := configFor(Application, false, map[string]Severity{intraFunctionFamily: Allow})
	disabled := 0
	for _, published := range publishedKinds(t) {
		want := published.severity
		if strings.HasPrefix(published.code, intraFunctionFamily) {
			want = Allow
			disabled++
		}
		if got := cfg.EffectiveSeverity(published.code, false); got != want {
			t.Errorf("EffectiveSeverity(%q, false) under severity %q = %q, want %q",
				published.code, intraFunctionFamily, got, want)
		}
	}
	if disabled == 0 {
		t.Errorf("no live kind carries the family prefix %q, so this test pins nothing", intraFunctionFamily)
	}
}
