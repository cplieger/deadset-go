package report

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// The analyzer identity every envelope of this package's tests is assembled under,
// stated rather than read from the composition root, so a rendering's bytes do not
// move when the analyzer's own version does.
const (
	testAnalyzerName    = "deadset-go"
	testAnalyzerVersion = "1.6.0"
)

// analyzerOf is the analyzer object an assembly is given: the identity, the schema
// version this package writes, and a conformance result, which the Contract requires
// on every report.
func analyzerOf() Analyzer {
	return Analyzer{
		Name:                   testAnalyzerName,
		Version:                testAnalyzerVersion,
		Languages:              []string{"go"},
		SchemaVersionsAccepted: []string{SchemaVersion},
		Conformance: Conformance{
			CorpusVersion: "1.0.0",
			Result:        "pass",
			Digest:        "sha256:" + strings.Repeat("ab", 32),
		},
	}
}

// targetOf is the target an assembly is given.
func targetOf() Target {
	return Target{Kind: "library", Root: ".", Identity: "example.com/app"}
}

// configurationsOf is the matrix an assembly is given: two entries, the second
// carrying a tag, so a rendering that names a configuration names one of them.
func configurationsOf() []Configuration {
	return []Configuration{
		{ID: "linux-amd64", OS: "linux", Arch: "amd64", Tags: []string{}},
		{ID: "linux-arm64-netgo", OS: "linux", Arch: "arm64", Tags: []string{"netgo"}},
	}
}

// minimalInput is an assembly that passes every refusal and carries no record, which
// is the input a test that checks one refusal starts from.
func minimalInput() BuildInput {
	return BuildInput{
		Analyzer:       analyzerOf(),
		Target:         targetOf(),
		Configurations: configurationsOf(),
	}
}

// built assembles one envelope and fails the test where the assembly is refused.
func built(t *testing.T, in *BuildInput) Envelope {
	t.Helper()

	envelope, err := Build(in)
	if err != nil {
		t.Fatalf("Build() = error %v, want an envelope", err)
	}
	return envelope
}

// findingOf is one finding with every field the framework fills, so a test states
// only what its own subject needs.
func findingOf(code, kind, path string, line, endLine int, severity config.Severity,
	fixability, message string,
) kinds.Finding {
	return kinds.Finding{
		Code:     code,
		Kind:     kind,
		Language: "go",
		Position: kinds.Position{Path: path, Line: line, Column: 6, EndLine: endLine},
		Symbol: kinds.Subject{
			Ref:       "go://example.com/app#" + kind,
			Kind:      "function",
			Name:      kind,
			SizeLines: endLine - line + 1,
		},
		Class:           kinds.Certain,
		Confidence:      kinds.Certain,
		Relation:        graph.ReferenceCounting,
		Component:       kinds.Component{ID: "deadset-go/c-1", Root: true, SymbolCount: 1},
		RetainedBy:      []string{},
		Configurations:  []string{"linux-amd64", "linux-arm64-netgo"},
		ConsumersLoaded: []string{"example.com/consumer"},
		Fixability:      fixability,
		Severity:        severity,
		Message:         message,
		Details:         kinds.Details{},
	}
}

// fullInput is an assembly carrying every member of the envelope: a finding of six
// kinds so that every per-kind detail the findings pass fills is rendered and both
// halves of the liveness relation's presence rule are exercised, a stale
// suppression, an edge evaluation with its pending finding, a declared gap, a file
// the cgo policy excluded, two test-file rules and both consumer lists.
//
// The two findings whose subject is a row of a document are left unmarked, which the
// findings pass never does: a subject no relation over declarations answers for is
// what the rendering reads, so the goldens carry no relation on either of them even
// though the mark says one decided the finding.
func fullInput() BuildInput {
	pending := findingOf("DS1001", "unused-exported", "wire.go", 9, 12,
		config.Deny, "deletable", "exported function is named by a declared edge and has no other reference")

	interfaceFinding := findingOf("DS1203", "uncalled-interface-method", "catalog.go", 31, 31,
		config.Warn, "manual", "no call through the interface reaches the method")
	interfaceFinding.Symbol.Kind = "interface-method"
	interfaceFinding.Details.Implementations = []kinds.Positioned{{
		Ref:      "go://example.com/app#memoryStore.Purge",
		Name:     "(*memoryStore).Purge",
		Position: kinds.Position{Path: "store.go", Line: 88, Column: 18, EndLine: 94},
	}}

	writeOnly := findingOf("DS1301", "write-only-symbol", "catalog.go", 12, 12,
		config.Deny, "deletable", "the field is written and never read")
	writeOnly.Live = true
	writeOnly.Symbol.Kind = "field"
	writeOnly.Details.WritePositions = []kinds.Position{
		{Path: "catalog.go", Line: 41, Column: 3, EndLine: 41},
		{Path: "catalog.go", Line: 52, Column: 3, EndLine: 52},
	}

	deletable := findingOf("DS1002", "unused-unexported", "catalog.go", 214, 231,
		config.Deny, "deletable", "the function has no reference in the target")
	deletable.Details.RemovesLastUseOf = []string{"example.com/left", "example.com/right"}

	stale := findingOf(staleSuppressionCode, "stale-suppression", "deadset-ignore.json", 7, 7,
		config.Deny, "none", "the ignore entry named DS1001 and the symbol it names reports DS1003")
	stale.Position.Column = 5
	stale.Symbol.Kind = "suppression"
	stale.Symbol.Ref = "go://example.com/app#Catalog.legacyAlias"
	stale.Symbol.Name = stale.Symbol.Ref
	stale.Details.Mechanism = "ignore"
	stale.Details.Entry = &kinds.Entry{
		Code:   "DS1001",
		Symbol: "go://example.com/app#Catalog.legacyAlias",
		Path:   "catalog.go",
		Reason: "removal is breaking for the published API",
	}

	dependency := findingOf("DS1601", "unused-dependency", "go.mod", 12, 12,
		config.Deny, "manual", "required by no package in the build list")
	dependency.Position.Column = 2
	dependency.Symbol.Kind = "dependency"
	dependency.Symbol.Name = "example.com/left"
	dependency.Details.DependencyClass = "require"

	neverBuilt := findingOf("DS1501", "file-never-built", "render_plan9.go", 1, 1,
		config.Deny, "deletable", "built under no declared configuration")
	neverBuilt.Position.Column = 1
	neverBuilt.Symbol.Kind = "file"
	neverBuilt.Symbol.Name = "render_plan9.go"
	neverBuilt.Details.ExcludedBy = "//go:build plan9"

	narrowing := findingOf("DS1101", "unnecessary-export", "normalize.go", 12, 27,
		config.Warn, "narrowable",
		"exported function is referenced only inside the package that declares it")
	narrowing.Live = true
	narrowing.Details.NarrowerVisibility = "package"

	stale.Component = kinds.Component{ID: "deadset-go/c-6", Root: true, SymbolCount: 1}
	narrowing.Component = kinds.Component{ID: "deadset-go/c-7", Root: true, SymbolCount: 1}
	dependency.Component = kinds.Component{ID: "deadset-go/c-1", Root: true, SymbolCount: 1, DeletableLines: 1}
	neverBuilt.Component = kinds.Component{ID: "deadset-go/c-2", Root: true, SymbolCount: 1, DeletableLines: 9}
	writeOnly.Component = kinds.Component{ID: "deadset-go/c-3", Root: true, SymbolCount: 1, DeletableLines: 1}
	interfaceFinding.Component = kinds.Component{ID: "deadset-go/c-4", Root: true, SymbolCount: 1}
	deletable.Component = kinds.Component{ID: "deadset-go/c-5", Root: true, SymbolCount: 3, DeletableLines: 41}

	return BuildInput{
		Analyzer:       analyzerOf(),
		Target:         targetOf(),
		Configurations: configurationsOf(),
		Consumers: Consumers{
			Declared: 2,
			Loaded:   []LoadedConsumer{{ID: "example.com/consumer", Path: "../consumer"}},
			Unavailable: []UnavailableConsumer{{
				ID:     "example.com/absent",
				Reason: "the configuration declared the consumer and named no path to load it from",
			}},
		},
		Result: kinds.Result{
			Findings: []kinds.Finding{
				dependency, neverBuilt, writeOnly, interfaceFinding, deletable, narrowing, stale,
			},
			OmittedBelowMinConfidence: 1,
		},
		EdgeEvaluations: []EdgeEvaluation{{
			Edge:    "wire/plan",
			Side:    "provides",
			Symbol:  "go://example.com/app#Plan",
			State:   "dead",
			Finding: &pending,
		}},
		DeclaredGaps: []DeclaredGap{{
			Fixture:    "private-member-unread",
			Capability: "DS1003",
			Reason:     "member-level analysis is not implemented, so no member of a type is reported",
		}},
		ExcludedByCgo: []string{"internal/bridge/glue.go", "internal/bridge/glue.go"},
		TestFileRules: []graph.TestFileRule{
			{Rule: "go-test-suffix", Matched: 61},
			{Rule: "go-test-suffix", Matched: 60},
			{Rule: "configured-test-glob", Matched: 4},
		},
		Suppressions: Suppressions{InEffect: 4, ReasonsRecorded: 6},
	}
}

// withoutStaleSuppressions is the full assembly with the stale suppression dropped, for
// a test whose subject is a finding.
func withoutStaleSuppressions(in *BuildInput) {
	kept := make([]kinds.Finding, 0, len(in.Result.Findings))
	for i := range in.Result.Findings {
		if in.Result.Findings[i].Code != staleSuppressionCode {
			kept = append(kept, in.Result.Findings[i])
		}
	}
	in.Result.Findings = kept
}

// staleSuppressionsOnly is the full assembly with every finding but the stale
// suppression dropped, for a test whose subject is that record.
func staleSuppressionsOnly(in *BuildInput) {
	for i := range in.Result.Findings {
		if in.Result.Findings[i].Code == staleSuppressionCode {
			in.Result.Findings = in.Result.Findings[i : i+1]
			return
		}
	}
	in.Result.Findings = nil
}

// lines is a reader of synthetic source: four hundred lines naming the file and the
// line, so a rendering that hashes a source line has one to hash whatever position a
// hand-built finding names, and two files never hash the same.
func lines() graph.ReadFile {
	return func(path string) ([]byte, error) {
		var held strings.Builder
		held.WriteString(path + "\n")
		for i := 2; i <= 400; i++ {
			held.WriteString("\tline " + strconv.Itoa(i) + " of " + path + "\n")
		}
		return []byte(held.String()), nil
	}
}

// renderings are the four renderings a golden is committed for, each under the name
// its golden carries.
func renderings() []struct {
	name   string
	render func(w *bytes.Buffer, e *Envelope, opts Options) error
} {
	return []struct {
		name   string
		render func(w *bytes.Buffer, e *Envelope, opts Options) error
	}{
		{"text", func(w *bytes.Buffer, e *Envelope, opts Options) error { return Text(w, e, opts) }},
		{"json", func(w *bytes.Buffer, e *Envelope, opts Options) error { return JSON(w, e, opts) }},
		{"annotations", func(w *bytes.Buffer, e *Envelope, opts Options) error { return Annotations(w, e, opts) }},
		{"sarif", func(w *bytes.Buffer, e *Envelope, opts Options) error { return SARIF(w, e, opts) }},
	}
}

// rendered is one rendering of one envelope.
func rendered(t *testing.T, name string, e *Envelope, opts Options) []byte {
	t.Helper()

	for _, one := range renderings() {
		if one.name != name {
			continue
		}
		var held bytes.Buffer
		if err := one.render(&held, e, opts); err != nil {
			t.Fatalf("%s() = error %v, want a rendering", name, err)
		}
		return held.Bytes()
	}
	t.Fatalf("Setup: %q names no rendering", name)
	return nil
}

// updateVariable is the gate that regenerates a golden. A run that does not set it
// never writes one.
const updateVariable = "UPDATE_GOLDEN"

// goldenCommand is the invocation that regenerates every golden of this package,
// which a failure names so a reader does not go looking for it.
const goldenCommand = "UPDATE_GOLDEN=1 go test ./internal/report/"

// checkGolden compares one rendering against its committed golden, and writes the
// golden instead where the gate is open.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")
	if os.Getenv(updateVariable) == "1" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the golden %s (regenerate with %s): %v", path, goldenCommand, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the rendering of %s differs from its golden (regenerate with %s):\n%s",
			name, goldenCommand, diff(string(want), string(got)))
	}
}

// diff is a line-by-line report of the first differences between two renderings, for
// a failure that has to say what moved.
func diff(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	var held strings.Builder
	for i := range max(len(wantLines), len(gotLines)) {
		at, to := "", ""
		if i < len(wantLines) {
			at = wantLines[i]
		}
		if i < len(gotLines) {
			to = gotLines[i]
		}
		if at == to {
			continue
		}
		fmt.Fprintf(&held, "line %d:\n--- want %q\n+++ got  %q\n", i+1, at, to)
		if held.Len() > 2000 {
			held.WriteString("...\n")
			break
		}
	}
	return held.String()
}
