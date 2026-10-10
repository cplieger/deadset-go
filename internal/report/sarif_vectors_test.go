package report_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/report"
	"github.com/cplieger/deadset-go/internal/reporttest"
	spec "github.com/cplieger/deadset-spec/v7"
)

// sarifVectorRoot is where the pinned Contract publishes the SARIF vectors.
const sarifVectorRoot = "vectors/sarif"

// mergedSarifCases are the published cases whose report is a merged one, which the
// invoking product renders: this analyzer writes the log of its own report alone, so
// it answers every other case.
var mergedSarifCases = []string{
	"merged-runs-and-totals",
	"merged-without-a-record-of-its-own",
	"record-naming-no-run",
}

// sarifVectorSources is a case's sources.json: the target files the line
// fingerprints read, by target-relative path.
type sarifVectorSources struct {
	Description string            `json:"description"`
	Files       map[string]string `json:"files"`
}

// TestPublishedSARIFVectors renders the report of every published case this analyzer
// answers and compares the document with the case's expected one as decoded values,
// or holds the rendering to the failure the case names.
func TestPublishedSARIFVectors(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(spec.Vectors, sarifVectorRoot)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", sarifVectorRoot, err)
	}
	var cases []string
	for _, entry := range entries {
		if entry.IsDir() {
			cases = append(cases, entry.Name())
		}
	}
	for _, merged := range mergedSarifCases {
		if !slices.Contains(cases, merged) {
			t.Errorf("%s holds no case %s, which this suite names as a merged one", sarifVectorRoot, merged)
		}
	}
	answered := slices.DeleteFunc(slices.Clone(cases), func(name string) bool {
		return slices.Contains(mergedSarifCases, name)
	})
	if len(answered) == 0 {
		t.Fatalf("Setup: %s holds no case this analyzer answers, so this test pins nothing", sarifVectorRoot)
	}

	for _, name := range answered {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := path.Join(sarifVectorRoot, name)
			envelope := vectorEnvelope(t, dir)
			read := vectorSources(t, dir)

			var written bytes.Buffer
			renderErr := report.SARIF(&written, &envelope, report.Options{Read: read})

			if exit := vectorFile(t, dir, "expected_exit"); exit != nil {
				if got := strings.TrimSpace(string(exit)); got != "3" {
					t.Fatalf("Setup: %s/expected_exit = %q, and a failed rendering exits 3", dir, got)
				}
				if renderErr == nil {
					t.Fatalf("SARIF(%s) wrote a document, want the rendering to fail with exit code 3", name)
				}
				return
			}
			if renderErr != nil {
				t.Fatalf("SARIF(%s) = %v, want the expected document", name, renderErr)
			}
			got := decodedValue(t, "the rendered document", written.Bytes())
			want := decodedValue(t, dir+"/expected.json", vectorFile(t, dir, "expected.json"))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("SARIF(%s) differs from expected.json as a decoded value:\n%s", name, valueDifferences(want, got, "$"))
			}
		})
	}
}

// vectorFile reads one file of a published case, or nil where the case has none.
func vectorFile(t *testing.T, dir, name string) []byte {
	t.Helper()

	data, err := spec.Vectors.ReadFile(path.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("Setup: read %s/%s: %v", dir, name, err)
	}
	return data
}

// vectorEnvelope decodes a case's report through the reader a report document this
// analyzer writes goes through.
func vectorEnvelope(t *testing.T, dir string) report.Envelope {
	t.Helper()

	envelope, err := reporttest.Read(vectorFile(t, dir, "report.json"))
	if err != nil {
		t.Fatalf("Setup: decode %s/report.json: %v", dir, err)
	}
	return envelope
}

// vectorSources writes a case's sources under a fresh target root and returns the
// reader a rendering reads them through.
func vectorSources(t *testing.T, dir string) func(string) ([]byte, error) {
	t.Helper()

	var sources sarifVectorSources
	if err := json.Unmarshal(vectorFile(t, dir, "sources.json"), &sources); err != nil {
		t.Fatalf("Setup: decode %s/sources.json: %v", dir, err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: open the target root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	for name, content := range sources.Files {
		if dir := path.Dir(name); dir != "." {
			if err := root.MkdirAll(dir, 0o750); err != nil {
				t.Fatalf("Setup: create %s: %v", dir, err)
			}
		}
		if err := root.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", name, err)
		}
	}
	return root.ReadFile
}

// decodedValue is one JSON document as the value it denotes, numbers kept as their
// literal so an integer compares exactly.
func decodedValue(t *testing.T, what string, data []byte) any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var held any
	if err := decoder.Decode(&held); err != nil {
		t.Fatalf("decode %s: %v", what, err)
	}
	return held
}

// valueDifferences names every path at which two decoded values differ, one per line,
// so a failure says where rather than that.
func valueDifferences(want, got any, at string) string {
	var lines []string
	collectDifferences(want, got, at, &lines)
	return strings.Join(lines, "\n")
}

func collectDifferences(want, got any, at string, lines *[]string) {
	if len(*lines) >= 40 {
		return
	}
	switch w := want.(type) {
	case map[string]any:
		g, isObject := got.(map[string]any)
		if !isObject {
			*lines = append(*lines, at+": want an object, got "+briefly(got))
			return
		}
		for _, key := range sortedKeys(w, g) {
			wv, inWant := w[key]
			gv, inGot := g[key]
			switch {
			case !inGot:
				*lines = append(*lines, at+"."+key+": missing, want "+briefly(wv))
			case !inWant:
				*lines = append(*lines, at+"."+key+": unexpected "+briefly(gv))
			default:
				collectDifferences(wv, gv, at+"."+key, lines)
			}
		}
	case []any:
		g, isArray := got.([]any)
		if !isArray || len(g) != len(w) {
			*lines = append(*lines, at+": want "+briefly(want)+", got "+briefly(got))
			return
		}
		for i := range w {
			collectDifferences(w[i], g[i], at+"["+strconv.Itoa(i)+"]", lines)
		}
	default:
		if !reflect.DeepEqual(want, got) {
			*lines = append(*lines, at+": want "+briefly(want)+", got "+briefly(got))
		}
	}
}

// sortedKeys is the member names of two objects, each once, in bytewise order.
func sortedKeys(a, b map[string]any) []string {
	keys := make([]string, 0, len(a)+len(b))
	for key := range a {
		keys = append(keys, key)
	}
	for key := range b {
		if _, held := a[key]; !held {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// briefly is one decoded value as a short line.
func briefly(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "an unencodable value"
	}
	if len(data) > 160 {
		return string(data[:160]) + "…"
	}
	return string(data)
}
