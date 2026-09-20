package report

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
	spec "github.com/cplieger/deadset-spec/v2"
)

// TestBuildRefusesWhatTheContractCannotCarry pins every assembly the envelope refuses
// rather than writing a document no reader admits.
func TestBuildRefusesWhatTheContractCannotCarry(t *testing.T) {
	stale := findingOf(staleSuppressionCode, "stale-suppression", "deadset-ignore.json", 7, 7,
		config.Deny, "none", "the ignore entry named DS1001 and no candidate reports it")
	live := findingOf("DS1001", "unused-exported", "catalog.go", 4, 8,
		config.Deny, "deletable", "exported function has no reference in the target")

	cases := []struct {
		name string
		of   func(in *BuildInput)
		want string
	}{
		{"no analyzer name", func(in *BuildInput) { in.Analyzer.Name = "" }, "names no analyzer"},
		{"no analyzer version", func(in *BuildInput) { in.Analyzer.Version = "" }, "names no version"},
		{"no language", func(in *BuildInput) { in.Analyzer.Languages = nil }, "names no language"},
		{
			"a schema version it does not read",
			func(in *BuildInput) { in.Analyzer.SchemaVersionsAccepted = []string{"9.9.9"} },
			"writes schema version",
		},
		{
			"no conformance result",
			func(in *BuildInput) { in.Analyzer.Conformance = Conformance{} },
			"carries no conformance result",
		},
		{"no target kind", func(in *BuildInput) { in.Target.Kind = "" }, "declares no target kind"},
		{"no target root", func(in *BuildInput) { in.Target.Root = "" }, "names no target root"},
		{"no target identity", func(in *BuildInput) { in.Target.Identity = "" }, "names no target identity"},
		{"no configuration", func(in *BuildInput) { in.Configurations = nil }, "names no build configuration"},
		{
			"one configuration both built and not built",
			func(in *BuildInput) {
				in.ConfigurationsNotBuilt = []ConfigurationNotBuilt{{
					ID: in.Configurations[0].ID, OS: "linux", Arch: "amd64", Error: "load linux-amd64: 1 error",
				}}
			},
			"as built and as not built",
		},
		{
			"a consumer count that disagrees",
			func(in *BuildInput) { in.Consumers = Consumers{Declared: 2} },
			"declares 2 consumers and lists 0",
		},
		{
			"a stale suppression naming no mechanism",
			func(in *BuildInput) { in.Result.Findings = []kinds.Finding{stale} },
			"names no mechanism",
		},
		{
			"a stale suppression carrying no record",
			func(in *BuildInput) {
				named := stale
				named.Details.Mechanism = "inline"
				in.Result.Findings = []kinds.Finding{named}
			},
			"carries no suppression record",
		},
		{
			"a stale suppression whose record names no reason",
			func(in *BuildInput) {
				named := stale
				named.Details.Mechanism = "inline"
				named.Details.Entry = &kinds.Entry{Code: "DS1001", Path: "catalog.go"}
				in.Result.Findings = []kinds.Finding{named}
			},
			"reason \"\"",
		},
		{
			"a dead side with no pending finding",
			func(in *BuildInput) {
				in.EdgeEvaluations = []EdgeEvaluation{{Edge: "wire/plan", Side: "provides", State: "dead"}}
			},
			"carries no pending finding",
		},
		{
			"a live side with a pending finding",
			func(in *BuildInput) {
				in.EdgeEvaluations = []EdgeEvaluation{{
					Edge: "wire/plan", Side: "used_by", State: "live", Finding: &live,
				}}
			},
			"carries a pending finding",
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			in := minimalInput()
			one.of(&in)
			_, err := Build(&in)
			if !errors.Is(err, ErrInput) {
				t.Fatalf("Build(%s) = error %v, want one carrying ErrInput", one.name, err)
			}
			if got := err.Error(); !strings.Contains(got, one.want) {
				t.Errorf("Build(%s) = error %q, want one naming %q", one.name, got, one.want)
			}
		})
	}
}

// TestBuildStatesTheVersionsItself pins the two versions the assembly fills rather
// than reads, so a caller cannot name a schema or a Contract version the analyzer does
// not implement.
func TestBuildStatesTheVersionsItself(t *testing.T) {
	in := minimalInput()
	envelope := built(t, &in)

	if envelope.SchemaVersion != SchemaVersion {
		t.Errorf("Build().SchemaVersion = %q, want %q", envelope.SchemaVersion, SchemaVersion)
	}
	if envelope.ContractVersion != config.ContractVersion {
		t.Errorf("Build().ContractVersion = %q, want %q", envelope.ContractVersion, config.ContractVersion)
	}
}

// TestBuildOrdersEveryArray pins that the assembly orders each array by the key the
// Contract fixes for it, whatever order its input arrives in.
func TestBuildOrdersEveryArray(t *testing.T) {
	in := fullInput()
	slices.Reverse(in.Result.Findings)
	slices.Reverse(in.Configurations)
	in.ConfigurationsNotBuilt = append(in.ConfigurationsNotBuilt, ConfigurationNotBuilt{
		ID: "js-wasm", OS: "js", Arch: "wasm", Error: "load js-wasm: 2 errors",
	})
	second := findingOf(staleSuppressionCode, "stale-suppression", "catalog.go", 3, 3,
		config.Deny, "none",
		"the inline directive named DS1002 and no candidate under that code sits below it")
	second.Position.Column = 1
	second.Symbol.Kind = "suppression"
	second.Symbol.Ref = "go://example.com/app#catalog.go:file"
	second.Symbol.Name = second.Symbol.Ref
	second.Details.Mechanism = "inline"
	second.Details.Entry = &kinds.Entry{
		Code: "DS1002", Path: "catalog.go", Reason: "kept for the plugin loader",
	}
	in.Result.Findings = append(in.Result.Findings, second)
	in.DeclaredGaps = append(in.DeclaredGaps, DeclaredGap{
		Fixture: "another-fixture", Capability: "DS1005", Reason: "the kind is not implemented",
	})
	envelope := built(t, &in)

	wantFindings := []string{
		"catalog.go", "catalog.go", "catalog.go", "go.mod", "normalize.go", "render_plan9.go",
	}
	got := make([]string, 0, len(envelope.Findings))
	for i := range envelope.Findings {
		got = append(got, envelope.Findings[i].Position.Path)
	}
	if !slices.Equal(got, wantFindings) {
		t.Errorf("Build().Findings paths = %v, want %v", got, wantFindings)
	}
	if !slices.IsSortedFunc(envelope.Findings, kinds.Compare) {
		t.Error("Build().Findings is not in the canonical order")
	}
	if !slices.IsSortedFunc(envelope.StaleSuppressions, compareStaleSuppressions) {
		t.Error("Build().StaleSuppressions is not in the canonical order")
	}
	if !slices.IsSortedFunc(envelope.DeclaredGaps, compareDeclaredGaps) {
		t.Error("Build().DeclaredGaps is not in the canonical order")
	}
	if envelope.Configurations[0].ID != "linux-amd64" {
		t.Errorf("Build().Configurations[0].ID = %q, want the first identifier bytewise",
			envelope.Configurations[0].ID)
	}
	if envelope.ConfigurationsNotBuilt[0].ID != "js-wasm" {
		t.Errorf("Build().ConfigurationsNotBuilt[0].ID = %q, want the first identifier bytewise",
			envelope.ConfigurationsNotBuilt[0].ID)
	}
}

// TestBuildUnionsTheDeclaredLimits pins the two members several configurations each
// answer: a file the cgo policy excluded is named once, and a test-file rule carries
// the greatest count any configuration reported for it.
func TestBuildUnionsTheDeclaredLimits(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)

	if want := []string{"internal/bridge/glue.go"}; !slices.Equal(envelope.ExcludedByCgo, want) {
		t.Errorf("Build().ExcludedByCgo = %v, want %v", envelope.ExcludedByCgo, want)
	}
	want := []graph.TestFileRule{
		{Rule: "configured-test-glob", Matched: 4},
		{Rule: "go-test-suffix", Matched: 61},
	}
	if !slices.Equal(envelope.TestFileRules, want) {
		t.Errorf("Build().TestFileRules = %v, want %v", envelope.TestFileRules, want)
	}
}

// TestBuildCountsTheTotals pins every total the assembly computes, against a finding
// set whose severities, components and records are known.
func TestBuildCountsTheTotals(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)

	want := Totals{
		Findings:             6,
		BySeverity:           BySeverity{Warn: 2, Deny: 4},
		DeletableLines:       1 + 9 + 1 + 0 + 41 + 0,
		SuppressionsInEffect: 4,
		ReasonsRecorded:      6,
		StaleSuppressions:    1,
		Pending:              1,
	}
	if envelope.Totals != want {
		t.Errorf("Build().Totals = %+v, want %+v", envelope.Totals, want)
	}
}

// TestBuildCopiesWhatItWasGiven pins that reordering an envelope does not reorder the
// finding list the caller still holds.
func TestBuildCopiesWhatItWasGiven(t *testing.T) {
	in := fullInput()
	first := in.Result.Findings[0].Code
	envelope := built(t, &in)
	Sort(&envelope, config.BySize)

	if in.Result.Findings[0].Code != first {
		t.Errorf("Build() then Sort() left the caller's first finding as %s, want %s",
			in.Result.Findings[0].Code, first)
	}
}

// TestTheSchemaVersionIsOneThePinnedContractAdmits pins the constant against the
// Contract the module pins. A report names its schema version and a reader admits
// it by membership in contract.json's schema_versions, so a constant the pinned
// Contract does not list is a document every reader on that Contract refuses.
func TestTheSchemaVersionIsOneThePinnedContractAdmits(t *testing.T) {
	t.Parallel()

	body, err := spec.Contract.ReadFile("contract/contract.json")
	if err != nil {
		t.Fatalf("Setup: read contract/contract.json: %v", err)
	}
	var contract struct {
		SchemaVersions []string `json:"schema_versions"`
	}
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatalf("Setup: decode contract/contract.json: %v", err)
	}
	if len(contract.SchemaVersions) == 0 {
		t.Fatalf("Setup: contract/contract.json declares no schema_versions")
	}

	if !slices.Contains(contract.SchemaVersions, SchemaVersion) {
		t.Errorf("SchemaVersion = %q, want one of contract.json's %v", SchemaVersion, contract.SchemaVersions)
	}
}
