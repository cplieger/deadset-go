package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
)

// provenanceValue is the spelling the Contract declares for a provenance value.
var provenanceValue = regexp.MustCompile(`^(default|(repository|central|flag): .+)$`)

// schemaKeyOrder is the order the Contract's configuration schema declares its
// root keys in, which is the order the emitter writes.
func schemaKeyOrder() []string {
	return []string{
		"contract_version", "target", "analysis", "consumers", "roots",
		"severity", "exemptions", "reporters", "go", "ts", "provenance",
	}
}

// printedKeyOrder returns the root keys of one printed document in the order the
// document writes them.
func printedKeyOrder(t *testing.T, printed []byte) []string {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(printed))
	open, err := dec.Token()
	if err != nil {
		t.Fatalf("read the printed configuration: %v", err)
	}
	if open != json.Delim('{') {
		t.Fatalf("the printed configuration opens with %v, want an object", open)
	}
	var keys []string
	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			t.Fatalf("read the printed configuration: %v", err)
		}
		key, isKey := name.(string)
		if !isKey {
			t.Fatalf("the printed configuration names %v, want a member name", name)
		}
		keys = append(keys, key)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("read the printed configuration: %v", err)
		}
	}
	return keys
}

// printed resolves one repository configuration and prints it.
func printed(t *testing.T, repository, flags string, flagLabels map[string]string) []byte {
	t.Helper()

	in := config.Inputs{Repository: []byte(repository), RepositoryLabel: "deadset.json", FlagLabels: flagLabels}
	if flags != "" {
		in.Flags = []byte(flags)
	}
	cfg, provenance, err := config.Resolve(in)
	if err != nil {
		t.Fatalf("Resolve(%s) = error %v, want the resolved configuration", repository, err)
	}
	var out bytes.Buffer
	if err := config.Print(&out, cfg, provenance); err != nil {
		t.Fatalf("Print(%s) = error %v, want the resolved configuration", repository, err)
	}
	return out.Bytes()
}

func TestPrintWritesTheSchemaKeyOrder(t *testing.T) {
	t.Parallel()

	document := printed(t, `{"reporters": {"sort": "size"}, "target": {"kind": "library"}}`, "", nil)
	got := printedKeyOrder(t, document)
	if !slices.Equal(got, schemaKeyOrder()) {
		t.Errorf("Print() wrote the keys %v, want the schema's order %v", got, schemaKeyOrder())
	}
}

func TestPrintWritesOneIndentedObjectEndingInANewline(t *testing.T) {
	t.Parallel()

	document := printed(t, `{"target": {"kind": "library"}}`, "", nil)
	if !bytes.HasSuffix(document, []byte("}\n")) {
		t.Errorf("Print() = %q, want it to end in a newline", document[max(0, len(document)-8):])
	}
	if !bytes.Contains(document, []byte("\n  \"target\": {")) {
		t.Errorf("Print() = %q, want two-space indentation", document)
	}
	if bytes.Contains(document, []byte("\t")) {
		t.Errorf("Print() = %q, want no tab", document)
	}
}

func TestPrintIsDeterministic(t *testing.T) {
	t.Parallel()

	document := `{"target": {"kind": "library"}, "severity": {"DS1801": "allow", "DS1101": "warn", "DS18": "deny"}}`
	first := printed(t, document, "", nil)
	second := printed(t, document, "", nil)
	if !bytes.Equal(first, second) {
		t.Errorf("Print() = %q on a second call, want the bytes of the first %q", second, first)
	}
}

func TestPrintWritesSeverityCodesInAscendingOrder(t *testing.T) {
	t.Parallel()

	document := printed(t,
		`{"target": {"kind": "library"}, "severity": {"DS1801": "allow", "DS1101": "warn", "DS18": "deny"}}`, "", nil)
	got := printedKeyOrder(t, section(t, document, "severity"))
	want := []string{"DS1101", "DS18", "DS1801"}
	if !slices.Equal(got, want) {
		t.Errorf("Print() wrote the severity codes %v, want them ascending %v", got, want)
	}
}

func TestPrintWritesEveryProvenanceValueTheContractAccepts(t *testing.T) {
	t.Parallel()

	document := printed(t,
		`{"target": {"kind": "library"}, "severity": {"DS1101": "warn"}}`,
		`{"reporters.sort": "size"}`,
		map[string]string{"reporters.sort": "--sort"},
	)
	var decoded struct {
		Provenance map[string]string `json:"provenance"`
	}
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("decode the printed configuration: %v", err)
	}
	for _, path := range slices.Sorted(maps.Keys(decoded.Provenance)) {
		if !provenanceValue.MatchString(decoded.Provenance[path]) {
			t.Errorf("Print() wrote provenance[%q] = %q, want the Contract's spelling", path, decoded.Provenance[path])
		}
	}
	for path, want := range map[string]string{
		"target.kind":            "repository: deadset.json",
		"reporters.sort":         "flag: --sort",
		"severity.DS1101":        "repository: deadset.json",
		"reporters.max_findings": "default",
	} {
		if got := decoded.Provenance[path]; got != want {
			t.Errorf("Print() wrote provenance[%q] = %q, want %q", path, got, want)
		}
	}
}

func TestPrintWritesAnEmptyArrayForAnAbsentOne(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Target: config.Target{Kind: config.Library}}
	var out bytes.Buffer
	if err := config.Print(&out, cfg, config.Provenance{}); err != nil {
		t.Fatalf("Print(a configuration with no arrays) = error %v, want the configuration", err)
	}
	if strings.Contains(out.String(), "null") {
		t.Errorf("Print(a configuration with no arrays) = %s, want no null", out.String())
	}
}

func TestPrintDoesNotMutateItsArgument(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Target:   config.Target{Kind: config.Library},
		Analysis: config.Analysis{Configurations: []config.Configuration{{ID: "a", OS: "linux", Arch: "amd64"}}},
	}
	var out bytes.Buffer
	if err := config.Print(&out, cfg, config.Provenance{}); err != nil {
		t.Fatalf("Print(a configuration with a matrix entry) = error %v, want the configuration", err)
	}
	if got := cfg.Analysis.Configurations[0].Tags; got != nil {
		t.Errorf("Print() left the caller's matrix entry tags = %#v, want them untouched", got)
	}
}

func TestPrintReportsAWriteFailure(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Target.Kind = config.Library
	err := config.Print(refusingWriter{}, cfg, config.Provenance{})
	if !errors.Is(err, errRefusingWriter) {
		t.Errorf("Print(a writer that refuses) = %v, want it to be %v", err, errRefusingWriter)
	}
}

// errRefusingWriter is what refusingWriter answers with.
var errRefusingWriter = errors.New("the writer refuses")

// refusingWriter is a writer that answers every write with a failure, so the
// failure path of Print is exercised.
type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errRefusingWriter }

// section decodes one section of a printed document as raw JSON.
func section(t *testing.T, document []byte, name string) json.RawMessage {
	t.Helper()

	var sections map[string]json.RawMessage
	if err := json.Unmarshal(document, &sections); err != nil {
		t.Fatalf("decode the printed configuration: %v", err)
	}
	raw, held := sections[name]
	if !held {
		t.Fatalf("the printed configuration names no %q", name)
	}
	return raw
}
