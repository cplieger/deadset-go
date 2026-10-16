package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// memberOrder returns the member names of one JSON object in the order the
// encoding writes them.
func memberOrder(t *testing.T, encoded []byte) []string {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(encoded))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		t.Fatalf("read %s: %v %v, want an object", encoded, open, err)
	}
	var names []string
	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			t.Fatalf("read %s: %v", encoded, err)
		}
		names = append(names, name.(string))
		var skipped json.RawMessage
		if err := dec.Decode(&skipped); err != nil {
			t.Fatalf("read %s: %v", encoded, err)
		}
	}
	return names
}

// printedSection is one member of a printed configuration, encoded.
func printedSection(t *testing.T, printed []byte, name string) []byte {
	t.Helper()

	var members map[string]json.RawMessage
	if err := json.Unmarshal(printed, &members); err != nil {
		t.Fatalf("decode the printed configuration: %v", err)
	}
	return members[name]
}

func TestPrintWritesTheKeysTheGoAnalysisReadsInTheTablesOrder(t *testing.T) {
	t.Parallel()

	raw, _ := printedDocument(t, resolved(t, fromRepository(`{"reporters": {"sort": "size"}, "target": {"kind": "library"}}`)))
	var want []string
	for _, row := range vocab.ConfigKeys() {
		if !strings.Contains(string(row.Key), ".") && row.ReadIn(vocab.Go) {
			want = append(want, string(row.Key))
		}
	}
	if got := memberOrder(t, raw); !slices.Equal(got, want) {
		t.Errorf("Print wrote the keys %v, want the table's order of the keys the Go analysis reads %v", got, want)
	}
}

func TestPrintWritesOneIndentedObjectEndingInANewline(t *testing.T) {
	t.Parallel()

	raw, _ := printedDocument(t, resolved(t, fromRepository(`{"target": {"kind": "library"}}`)))
	if !bytes.HasSuffix(raw, []byte("}\n")) || !bytes.Contains(raw, []byte("\n  \"target\": {")) || bytes.Contains(raw, []byte("\t")) {
		t.Errorf("Print = %s, want one object indented by two spaces ending in a newline", raw)
	}
}

func TestPrintIsDeterministicAndWritesSeverityCodesAscending(t *testing.T) {
	t.Parallel()

	document := `{"target": {"kind": "library"}, "severity": {"DS1401": "allow", "DS1501": "warn", "DS15": "deny"}}`
	first, _ := printedDocument(t, resolved(t, fromRepository(document)))
	second, _ := printedDocument(t, resolved(t, fromRepository(document)))
	if !bytes.Equal(first, second) {
		t.Errorf("Print = %s on a second resolution, want the bytes of the first %s", second, first)
	}
	if got, want := memberOrder(t, printedSection(t, first, "severity")), []string{"DS1401", "DS15", "DS1501"}; !slices.Equal(got, want) {
		t.Errorf("Print wrote the severity codes %v, want them ascending %v", got, want)
	}
}

// provenanceValue is the spelling the Contract declares for a provenance value.
var provenanceValue = regexp.MustCompile(`^(default|(repository|central|flag): .+)$`)

func TestPrintWritesOneProvenanceEntryPerSetting(t *testing.T) {
	t.Parallel()

	in := fromRepository(`{"target": {"kind": "library"}, "severity": {"DS1501": "warn"}}`)
	in.Flags = map[vocab.Key][]byte{vocab.KeyReportersSort: []byte(`"size"`)}
	_, document := printedDocument(t, resolved(t, in))
	provenance, _ := document["provenance"].(map[string]any)
	for path, value := range provenance {
		if spelled, _ := value.(string); !provenanceValue.MatchString(spelled) {
			t.Errorf("provenance[%q] = %#v, want the Contract's spelling", path, value)
		}
	}
	for _, row := range vocab.ConfigKeys() {
		if row.ReadIn(vocab.Go) && row.Role == vocab.RoleSetting {
			if _, held := provenance[string(row.Key)]; !held {
				t.Errorf("provenance names no %s, want one entry per setting", row.Key)
			}
		}
	}
	for path, want := range map[string]string{"reporters.sort": "flag: --sort", "severity.DS1501": "repository: deadset.json"} {
		if got := provenance[path]; got != want {
			t.Errorf("provenance[%q] = %#v, want %q", path, got, want)
		}
	}
}

func TestPrintWritesTheCharactersAPageEscapesAsThemselves(t *testing.T) {
	t.Parallel()

	raw, _ := printedDocument(t, resolved(t, fromRepository(`{"target": {"kind": "library"},
		"analysis": {"template_delimiters": {"left": "<%", "right": "%>"}},
		"roots": {"patterns": ["go://example.com/app#Run&Stop"]}}`)))
	for _, want := range []string{`"left": "<%"`, `"right": "%>"`, `"go://example.com/app#Run&Stop"`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("Print = %s, want it to write %s as the bytes it spells", raw, want)
		}
	}
}

func TestPrintedConfigurationReadsBackAsItself(t *testing.T) {
	t.Parallel()

	r := resolved(t, threeSources(map[vocab.Key]string{vocab.KeyReportersMaxFindings: `3`},
		`{"target": {"kind": {"go": "library", "ts": "application"}}, "severity": {"DS14": "allow"},
		  "analysis": {"configurations": [{"id": "a", "os": "linux", "arch": "amd64"}, {"id": "b", "project": "tsconfig.json"}]}}`,
		`{"exemptions": {"disabled": ["enum-group"]}}`))
	raw, document := printedDocument(t, r)
	back := resolved(t, fromRepository(string(raw)))
	rawBack, again := printedDocument(t, back)
	delete(document, "provenance")
	delete(again, "provenance")
	if !reflect.DeepEqual(back.Config, r.Config) || !reflect.DeepEqual(again, document) {
		t.Errorf("the printed configuration read back prints\n%s\nwant\n%s", rawBack, raw)
	}
}

func TestPrintReportsAWriteFailure(t *testing.T) {
	t.Parallel()

	r := resolved(t, fromRepository(`{"target": {"kind": "library"}}`))
	if err := r.Print(refusingWriter{}); !errors.Is(err, errRefusingWriter) {
		t.Errorf("Print(a writer that refuses) = %v, want it to wrap %v", err, errRefusingWriter)
	}
}

// errRefusingWriter is what refusingWriter answers with.
var errRefusingWriter = errors.New("the writer refuses")

// refusingWriter answers every write with a failure.
type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errRefusingWriter }
