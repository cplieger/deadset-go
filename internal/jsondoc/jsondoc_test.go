package jsondoc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/deadset-spec/v8/document"
)

func TestEncodeWritesTheCharactersAPageEscapesAsThemselves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, indent string
		value        any
		want         string
	}{
		{name: "compact", value: map[string]string{"pattern": "<a&b>"}, want: "{\"pattern\":\"<a&b>\"}\n"},
		{name: "indented", indent: "  ", value: []string{"<%", "%>"}, want: "[\n  \"<%\",\n  \"%>\"\n]\n"},
		{name: "separators", value: "a\u2028b", want: "\"a\\u2028b\"\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Encode(tc.value, tc.indent)
			if err != nil {
				t.Fatalf("Encode(%v, %q) = %v", tc.value, tc.indent, err)
			}
			if string(got) != tc.want {
				t.Errorf("Encode(%v, %q) = %q, want %q", tc.value, tc.indent, got, tc.want)
			}
		})
	}
}

func TestReadHoldsADocumentToItsBound(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "document.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 8)), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx   context.Context
		want  error
		name  string
		limit int64
	}{
		{name: "at_the_bound", ctx: t.Context(), limit: 8},
		{name: "over_the_bound", ctx: t.Context(), limit: 7, want: atomicfile.ErrFileTooLarge},
		{name: "a_cancelled_read", ctx: cancelled, limit: 8, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body, err := Read(tc.ctx, path, tc.limit)
			if tc.want == nil {
				if err != nil || len(body) != 8 {
					t.Errorf("Read(%s, %d) = %d bytes, %v, want the 8 bytes", path, tc.limit, len(body), err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Read(%s, %d) = _, %v, want an error matching %v", path, tc.limit, err, tc.want)
			}
		})
	}
}

func TestReadNamesTheDocumentAndTheBoundItExceeds(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "document.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	_, err := Read(t.Context(), path, 2)
	if err == nil || !strings.Contains(err.Error(), path+" is larger than 2 bytes") {
		t.Errorf("Read(%s, 2) = _, %v, want it to name the document and the bound", path, err)
	}
}

func TestUnmarshalNamesTheDocumentAndThePlaceItRefuses(t *testing.T) {
	t.Parallel()

	schema, err := document.CompileBytes("value", []byte(`{
		"type": "object", "additionalProperties": false, "required": ["edges"],
		"properties": {
			"edges": {"type": "object", "additionalProperties": {"type": "object", "required": ["used_by"]}},
			"list": {"type": "array", "items": {"type": "string"}}
		}
	}`))
	if err != nil {
		t.Fatalf("Setup: CompileBytes = %v", err)
	}
	for _, tc := range []struct {
		name, body, want string
		malformed        bool
	}{
		{name: "the whole document", body: `[]`, want: "doc.json: the document: "},
		{name: "a member named by digits alone", body: `{"edges": {"1": {}}}`, want: "doc.json: edges.1.used_by: "},
		{name: "an entry of an array", body: `{"edges": {}, "list": [1]}`, want: "doc.json: list[0]: "},
		{name: "a member written twice", body: `{"edges": {}, "edges": {}}`, want: "doc.json: ", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var v map[string]any
			err := Unmarshal(schema, "doc.json", []byte(tc.body), &v)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("Unmarshal(%s) = %v, want an error starting %q", tc.body, err, tc.want)
			}
			if malformed := errors.Is(err, document.ErrMalformed); malformed != tc.malformed {
				t.Errorf("Unmarshal(%s) = %v, matching document.ErrMalformed %t, want %t", tc.body, err, malformed, tc.malformed)
			}
		})
	}
	var v struct {
		Edges map[string]struct {
			UsedBy string `json:"used_by"`
		} `json:"edges"`
	}
	if err := Unmarshal(schema, "doc.json", []byte(`{"edges": {"a": {"used_by": "b"}}}`), &v); err != nil || v.Edges["a"].UsedBy != "b" {
		t.Errorf("Unmarshal(an instance) = %v, holding %+v, want the document decoded", err, v)
	}
}
