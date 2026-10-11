package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/deadset-spec/v7"
)

func TestTheEmbeddedSchemasAreTheContracts(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"contract/report.schema.json", "contract/finding.schema.json"} {
		held, err := fs.ReadFile(contract, name)
		if err != nil {
			t.Fatalf("Setup: read the embedded %s: %v", name, err)
		}
		published, err := spec.Contract.ReadFile(name)
		if err != nil {
			t.Fatalf("Setup: read %s from the Contract: %v", name, err)
		}
		if !bytes.Equal(held, published) {
			t.Errorf("the embedded %s differs from the Contract's: copy the pinned version's file", name)
		}
	}
}

// compiled compiles one of the Contract's schemas from the published tree.
func compiled(t *testing.T, name string) *validator {
	t.Helper()
	s, err := compile(spec.Contract, name)
	if err != nil {
		t.Fatalf("Setup: compile(%s) = %v", name, err)
	}
	return s
}

func TestEveryContractSchemaCompiles(t *testing.T) {
	t.Parallel()

	names, err := fs.Glob(spec.Contract, "contract/*.schema.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("Setup: list the Contract's schemas: %v, %d found", err, len(names))
	}
	for _, name := range names {
		if _, err := compile(spec.Contract, name); err != nil {
			t.Errorf("compile(%s) = %v, want the compiled schema", name, err)
		}
	}
	if _, err := Report(); err != nil {
		t.Errorf("Report() = %v, want the embedded report schema compiled", err)
	}
}

func TestCheckAcceptsEveryExample(t *testing.T) {
	t.Parallel()

	for dir, name := range map[string]string{
		"examples/reports":  "contract/report.schema.json",
		"examples/findings": "contract/finding.schema.json",
		"examples/scope":    "contract/scope.schema.json",
		"examples/describe": "contract/describe.schema.json",
	} {
		s := compiled(t, name)
		files, err := fs.Glob(spec.Examples, dir+"/*.json")
		if err != nil || len(files) == 0 {
			t.Fatalf("Setup: list %s: %v, %d found", dir, err, len(files))
		}
		for _, file := range files {
			body, err := spec.Examples.ReadFile(file)
			if err != nil {
				t.Fatalf("Setup: read %s: %v", file, err)
			}
			found, err := s.Check(bytes.NewReader(body))
			if err != nil || len(found) != 0 {
				t.Errorf("Check(%s against %s) = %v, %v, want no violation", file, name, found, err)
			}
		}
	}
}

// negative is one row of the example set's index of refused documents.
type negative struct {
	File         string `json:"file"`
	Schema       string `json:"schema"`
	Constraint   string `json:"constraint"`
	InstancePath string `json:"instance_path"`
}

func TestCheckRefusesEveryNegativeAtItsConstraint(t *testing.T) {
	t.Parallel()

	body, err := spec.Examples.ReadFile("examples/negatives/index.json")
	if err != nil {
		t.Fatalf("Setup: read the negatives index: %v", err)
	}
	var index struct {
		Negatives []negative `json:"negatives"`
	}
	if err := json.Unmarshal(body, &index); err != nil || len(index.Negatives) == 0 {
		t.Fatalf("Setup: decode the negatives index: %v, %d rows", err, len(index.Negatives))
	}
	for _, row := range index.Negatives {
		document, err := spec.Examples.ReadFile(path.Join("examples/negatives", row.File))
		if err != nil {
			t.Fatalf("Setup: read %s: %v", row.File, err)
		}
		s := compiled(t, row.Schema)
		streamed, err := s.Check(bytes.NewReader(document))
		if err != nil {
			t.Errorf("Check(%s) = %v, want the document read", row.File, err)
			continue
		}
		value, err := decode(document)
		if err != nil {
			t.Fatalf("Setup: decode %s: %v", row.File, err)
		}
		var decoded []Error
		s.root.check(value, "", &decoded)
		for mode, found := range map[string][]Error{"Check": streamed, "check": decoded} {
			if !refusedAt(found, row) {
				t.Errorf("%s(%s) = %v, want violations at %q only, one of them %s",
					mode, row.File, found, row.InstancePath, row.Constraint)
			}
		}
	}
}

// refusedAt reports whether every violation is at the row's instance path and one
// is the row's constraint.
func refusedAt(found []Error, row negative) bool {
	if len(found) == 0 {
		return false
	}
	for _, v := range found {
		if v.InstancePath != row.InstancePath {
			return false
		}
	}
	return slices.ContainsFunc(found, func(v Error) bool { return v.Keyword == row.Constraint })
}

func TestCompileRefusesAKeywordItDoesNotEvaluate(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"one.schema.json": {Data: []byte(`{"type": "string", "format": "uri", "x-owner": "a", "description": "d"}`)}}
	if _, err := compile(fsys, "one.schema.json"); !errors.Is(err, errSchema) || !strings.Contains(err.Error(), `"format"`) {
		t.Errorf("compile(a schema naming format) = %v, want errSchema naming the keyword", err)
	}
}

func TestCheckStopsAtTheFirstMemberHoldingAViolation(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"one.schema.json": {Data: []byte(`{
		"type": "object", "additionalProperties": false, "required": ["a", "b"],
		"properties": {
			"a": {"type": "array", "items": {"type": "integer", "minimum": 1, "maximum": 3}},
			"b": {"type": "string"}
		}
	}`)}}
	s, err := compile(fsys, "one.schema.json")
	if err != nil {
		t.Fatalf("Setup: Compile = %v", err)
	}
	for _, tc := range []struct {
		name     string
		document string
		want     []Error
	}{
		{name: "instance", document: `{"a": [1, 2], "b": "x"}`},
		{name: "second_item", document: `{"a": [1, 0, -1], "b": 2}`, want: []Error{{Keyword: "minimum", InstancePath: "/a/1"}}},
		{name: "above_maximum", document: `{"a": [3, 4], "b": "x"}`, want: []Error{{Keyword: "maximum", InstancePath: "/a/1"}}},
		{name: "member_type", document: `{"a": [1], "b": 2}`, want: []Error{{Keyword: "type", InstancePath: "/b"}}},
		{name: "required", document: `{"a": []}`, want: []Error{{Keyword: "required", InstancePath: ""}}},
		{name: "unknown", document: `{"a": [], "c": 1, "b": "x"}`, want: []Error{{Keyword: "additionalProperties", InstancePath: ""}}},
		{name: "container_type", document: `{"a": {"x": [1]}, "b": "x"}`, want: []Error{{Keyword: "type", InstancePath: "/a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, err := s.Check(strings.NewReader(tc.document))
			if err != nil {
				t.Fatalf("Check(%s) = %v", tc.document, err)
			}
			got := make([]Error, len(found))
			for i, v := range found {
				got[i] = Error{Keyword: v.Keyword, InstancePath: v.InstancePath}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Check(%s) = %v, want %v", tc.document, found, tc.want)
			}
		})
	}
}

func TestCheckRefusesTrailingData(t *testing.T) {
	t.Parallel()

	s, err := compile(fstest.MapFS{"one.schema.json": {Data: []byte(`{"type": "object"}`)}}, "one.schema.json")
	if err != nil {
		t.Fatalf("Setup: Compile = %v", err)
	}
	if _, err := s.Check(strings.NewReader(`{} {}`)); !errors.Is(err, errDocument) {
		t.Errorf("Check(two values) = %v, want errDocument", err)
	}
}
