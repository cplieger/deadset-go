package config_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	spec "github.com/cplieger/deadset-spec"
)

// configSchema decodes the Contract's configuration schema, which is what the
// closed key list, its defaults and its section members are checked against.
func configSchema(t *testing.T) map[string]any {
	t.Helper()

	data, err := spec.Contract.ReadFile("contract/config.schema.json")
	if err != nil {
		t.Fatalf("read contract/config.schema.json: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode contract/config.schema.json: %v", err)
	}
	return schema
}

// schemaDefaults returns every default the Contract's configuration schema
// declares, keyed by the setting's dotted path. It does not descend into an
// array's entries: a default configuration holds none.
func schemaDefaults(t *testing.T) map[string]any {
	t.Helper()

	defaults := make(map[string]any)
	collectDefaults(t, configSchema(t), "", defaults)
	return defaults
}

// collectDefaults appends the declared default of every key below one declaration.
func collectDefaults(t *testing.T, declaration map[string]any, at string, into map[string]any) {
	t.Helper()

	properties, held := declaration["properties"].(map[string]any)
	if !held {
		return
	}
	for name, member := range properties {
		declared, isObject := member.(map[string]any)
		if !isObject {
			t.Fatalf("config.schema.json: %s.%s is %T, want an object", at, name, member)
		}
		path := name
		if at != "" {
			path = at + "." + name
		}
		if value, carries := declared["default"]; carries {
			into[path] = value
		}
		collectDefaults(t, declared, path, into)
	}
}

// valueAt reads the value one dotted path names in a decoded document.
func valueAt(document map[string]any, path string) (any, bool) {
	segments := strings.Split(path, ".")
	for _, segment := range segments[:len(segments)-1] {
		section, isSection := document[segment].(map[string]any)
		if !isSection {
			return nil, false
		}
		document = section
	}
	value, held := document[segments[len(segments)-1]]
	return value, held
}

// printedDefault returns the default configuration as a decoded document, with the
// one field that has no default supplied.
func printedDefault(t *testing.T) map[string]any {
	t.Helper()

	cfg := config.Default()
	cfg.Target.Kind = config.Library
	var printed bytes.Buffer
	if err := config.Print(&printed, cfg, config.Provenance{}); err != nil {
		t.Fatalf("Print(Default()) = error %v, want the default configuration", err)
	}
	var document map[string]any
	if err := json.Unmarshal(printed.Bytes(), &document); err != nil {
		t.Fatalf("decode the printed default configuration: %v", err)
	}
	return document
}

func TestDefaultAppliesEveryDeclaredDefault(t *testing.T) {
	t.Parallel()

	document := printedDefault(t)
	defaults := schemaDefaults(t)
	if len(defaults) == 0 {
		t.Fatalf("schemaDefaults() = %v, want the defaults config.schema.json declares", defaults)
	}
	for _, path := range slices.Sorted(maps.Keys(defaults)) {
		want := defaults[path]
		got, held := valueAt(document, path)
		if !held {
			t.Errorf("Default() names no %q, want the declared default %#v", path, want)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Default() resolved %s = %#v, want the declared default %#v", path, got, want)
		}
	}
}

func TestDefaultLeavesTheTargetKindUnset(t *testing.T) {
	t.Parallel()

	if got := config.Default().Target.Kind; got != "" {
		t.Errorf("Default().Target.Kind = %q, want it unset: the field has no default", got)
	}
}

func TestOriginString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		origin config.Origin
		want   string
	}{
		{name: "a_default", origin: config.Origin{Source: config.SourceDefault}, want: "default"},
		{
			name:   "a_repository_configuration",
			origin: config.Origin{Source: config.SourceRepository, Label: "/src/app/deadset.json"},
			want:   "repository: /src/app/deadset.json",
		},
		{
			name:   "a_central_configuration",
			origin: config.Origin{Source: config.SourceCentral, Label: "/etc/deadset/central.json"},
			want:   "central: /etc/deadset/central.json",
		},
		{
			name:   "a_flag",
			origin: config.Origin{Source: config.SourceFlag, Label: "--min-confidence"},
			want:   "flag: --min-confidence",
		},
		{
			name:   "a_default_ignores_a_label",
			origin: config.Origin{Source: config.SourceDefault, Label: "ignored"},
			want:   "default",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.origin.String(); got != tc.want {
				t.Errorf("Origin{%q, %q}.String() = %q, want %q", tc.origin.Source, tc.origin.Label, got, tc.want)
			}
		})
	}
}

func TestErrorKindString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind config.ErrorKind
		want string
	}{
		{name: "malformed", kind: config.KindMalformed, want: "malformed"},
		{name: "unimplemented_key", kind: config.KindUnimplementedKey, want: "unimplemented key"},
		{name: "missing_target_kind", kind: config.KindMissingTargetKind, want: "missing target kind"},
		{name: "outside_the_declared_kinds", kind: config.ErrorKind(9), want: "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.kind.String(); got != tc.want {
				t.Errorf("ErrorKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

func TestErrorCarriesItsMessage(t *testing.T) {
	t.Parallel()

	refusal := &config.Error{Kind: config.KindMalformed, Key: "target.kind", Message: "deadset.json: target.kind: bad"}
	if got := refusal.Error(); got != refusal.Message {
		t.Errorf("(&Error{Message: %q}).Error() = %q, want the message", refusal.Message, got)
	}
}
