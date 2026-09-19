package report

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
	spec "github.com/cplieger/deadset-spec"
)

// contractCodes reads the exit codes contract/exit-codes.json publishes, keyed by
// name, so the verdicts this package returns are compared against the Contract's
// own numbers rather than against a second copy of them.
func contractCodes(t *testing.T) map[string]int {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/exit-codes.json")
	if err != nil {
		t.Fatalf("Setup: read contract/exit-codes.json: %v", err)
	}
	var document struct {
		ExitCodes []struct {
			Name string `json:"name"`
			Code int    `json:"code"`
		} `json:"exit_codes"`
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

func TestVerdictCodesEqualTheContract(t *testing.T) {
	t.Parallel()

	codes := contractCodes(t)
	for name, got := range map[string]int{"clean": verdictClean, "findings": verdictFindings, "pending": verdictPending} {
		if want, held := codes[name]; !held || got != want {
			t.Errorf("the verdict this package returns for %q is %d, want %d (present %t) as contract/exit-codes.json names it",
				name, got, want, held)
		}
	}
}

// staleInput is an assembly holding one stale suppression and no finding, which is
// the report a run over an adjudication that no longer matches anything produces.
func staleInput() BuildInput {
	in := fullInput()
	staleSuppressionsOnly(&in)
	in.EdgeEvaluations = nil
	return in
}

// severityInput is an assembly holding one finding at the severity named and
// nothing else.
func severityInput(severity config.Severity) BuildInput {
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{
		findingOf("DS1002", "unused-unexported", "catalog.go", 9, 9, severity,
			"deletable", "the function has no reference in the target"),
	}
	return in
}

// pendingInput is an assembly holding one edge evaluation whose side is dead, which
// is the one record that makes a report pending.
func pendingInput() BuildInput {
	in := fullInput()
	return in
}

func TestExitCodeIsTheVerdictTheContractsTableNames(t *testing.T) {
	t.Parallel()

	codes := contractCodes(t)
	tests := []struct {
		name  string
		build func() BuildInput
		on    config.Severity
		want  int
	}{
		{
			name:  "a_report_holding_nothing_is_clean",
			build: minimalInput,
			on:    config.Deny,
			want:  codes["clean"],
		},
		{
			name:  "a_warn_finding_alone_does_not_fail_the_run",
			build: func() BuildInput { return severityInput(config.Warn) },
			on:    config.Deny,
			want:  codes["clean"],
		},
		{
			name:  "a_deny_finding_fails_the_run",
			build: func() BuildInput { return severityInput(config.Deny) },
			on:    config.Deny,
			want:  codes["findings"],
		},
		{
			name:  "a_warn_finding_fails_a_run_configured_to_fail_on_warn",
			build: func() BuildInput { return severityInput(config.Warn) },
			on:    config.Warn,
			want:  codes["findings"],
		},
		{
			name:  "an_allow_finding_does_not_fail_a_run_configured_to_fail_on_warn",
			build: func() BuildInput { return severityInput(config.Allow) },
			on:    config.Warn,
			want:  codes["clean"],
		},
		{
			name:  "an_allow_finding_fails_a_run_configured_to_fail_on_allow",
			build: func() BuildInput { return severityInput(config.Allow) },
			on:    config.Allow,
			want:  codes["findings"],
		},
		{
			name:  "a_stale_suppression_fails_a_run_whose_every_kind_is_allowed",
			build: staleInput,
			on:    config.Allow,
			want:  codes["findings"],
		},
		{
			name:  "a_pending_finding_outranks_everything_the_report_holds",
			build: pendingInput,
			on:    config.Deny,
			want:  codes["pending"],
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := tc.build()
			envelope := built(t, &in)
			cfg := config.Default()
			cfg.Reporters.FailOn = tc.on

			if got := ExitCode(&envelope, &cfg, false); got != tc.want {
				t.Errorf("ExitCode(a report of %d findings %+v, %d stale, %d pending, fail_on %s) = %d, want %d",
					envelope.Totals.Findings, envelope.Totals.BySeverity, envelope.Totals.StaleSuppressions,
					envelope.Totals.Pending, tc.on, got, tc.want)
			}
			// The same report with the code configured off is the clean code,
			// and the verdict above is what a caller names beside it.
			if got := ExitCode(&envelope, &cfg, true); got != codes["clean"] {
				t.Errorf("ExitCode(the same report, the code configured off) = %d, want %d", got, codes["clean"])
			}
		})
	}
}

func TestExitCodeReadsTheWholeFindingSetOfACappedReport(t *testing.T) {
	t.Parallel()

	in := minimalInput()
	in.Result.Findings = []kinds.Finding{
		findingOf("DS1002", "unused-unexported", "a.go", 9, 9, config.Warn,
			"deletable", "the function has no reference in the target"),
		findingOf("DS1002", "unused-unexported", "b.go", 9, 9, config.Deny,
			"deletable", "the function has no reference in the target"),
	}
	envelope := built(t, &in)
	Cap(&envelope, 1)

	cfg := config.Default()
	if got, want := len(envelope.Findings), 1; got != want {
		t.Fatalf("Setup: the capped report prints %d findings, want %d", got, want)
	}
	if envelope.Findings[0].Severity != config.Warn {
		t.Fatalf("Setup: the printed finding carries %q, want %q: the deny finding is the omitted one",
			envelope.Findings[0].Severity, config.Warn)
	}
	if got, want := ExitCode(&envelope, &cfg, false), verdictFindings; got != want {
		t.Errorf("ExitCode(a report capped to its one warn finding, with a deny finding omitted) = %d, want %d",
			got, want)
	}
}

func TestFailOnIsTheDocumentedDefaultWhereTheConfigurationNamesNoSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want config.Severity
	}{
		{name: "no_configuration", cfg: nil, want: config.Deny},
		{name: "an_unset_value", cfg: &config.Config{}, want: config.Deny},
		{
			name: "a_value_outside_the_three_severities",
			cfg:  &config.Config{Reporters: config.Reporters{FailOn: config.Severity("error")}},
			want: config.Deny,
		},
		{
			name: "the_configured_value",
			cfg:  &config.Config{Reporters: config.Reporters{FailOn: config.Warn}},
			want: config.Warn,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := failOn(tc.cfg); got != tc.want {
				t.Errorf("failOn(%+v) = %q, want %q", tc.cfg, got, tc.want)
			}
		})
	}
}

func TestFailingFindingsCountsEverySeverityAtOrAboveTheFailingOne(t *testing.T) {
	t.Parallel()

	counted := BySeverity{Allow: 2, Warn: 3, Deny: 5}
	tests := []struct {
		at   config.Severity
		want int
	}{
		{at: config.Deny, want: 5},
		{at: config.Warn, want: 8},
		{at: config.Allow, want: 10},
	}

	for _, tc := range tests {
		t.Run(string(tc.at), func(t *testing.T) {
			t.Parallel()

			if got := failingFindings(&counted, tc.at); got != tc.want {
				t.Errorf("failingFindings(%+v, %q) = %d, want %d", counted, tc.at, got, tc.want)
			}
		})
	}
}

func TestEverySeverityTheContractDeclaresIsARankThisPackageOrders(t *testing.T) {
	t.Parallel()

	body, err := spec.Contract.ReadFile("contract/kinds.json")
	if err != nil {
		t.Fatalf("Setup: read contract/kinds.json: %v", err)
	}
	var document struct {
		Kinds []struct {
			DefaultSeverity string `json:"default_severity"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/kinds.json: %v", err)
	}

	ranked := []config.Severity{config.Allow, config.Warn, config.Deny}
	for _, kind := range document.Kinds {
		if !slices.Contains(ranked, config.Severity(kind.DefaultSeverity)) {
			t.Errorf("contract/kinds.json declares the default severity %q, which this package does not rank",
				kind.DefaultSeverity)
		}
	}
}
