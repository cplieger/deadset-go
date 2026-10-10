package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
)

// mixedMatrix is a repository configuration whose build matrix lists both shapes,
// interleaved, so an order the platforms keep is distinguishable from an order the
// shapes were grouped into.
const mixedMatrix = `{"target": {"kind": "application"}, "analysis": {"configurations": [
	{"id": "tsconfig.json", "project": "tsconfig.json"},
	{"id": "linux-amd64", "os": "linux", "arch": "amd64"},
	{"id": "packages/app/tsconfig.json", "project": "packages/app/tsconfig.json"},
	{"id": "linux-arm64-netgo", "os": "linux", "arch": "arm64", "tags": ["netgo"]}
]}}`

func TestPlatformsAreThePlatformEntriesInTheOrderTheMatrixListsThem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repository string
		want       []config.Platform
	}{
		{
			name:       "a_matrix_listing_both_shapes",
			repository: mixedMatrix,
			want: []config.Platform{
				{ID: "linux-amd64", OS: "linux", Arch: "amd64", Tags: []string{}},
				{ID: "linux-arm64-netgo", OS: "linux", Arch: "arm64", Tags: []string{"netgo"}},
			},
		},
		{
			name: "a_matrix_listing_projects_alone",
			repository: `{"target": {"kind": "application"}, "analysis": {"configurations": [` +
				`{"id": "tsconfig.json", "project": "tsconfig.json"}]}}`,
			want: nil,
		},
		{
			name:       "no_matrix",
			repository: `{"target": {"kind": "application"}}`,
			want:       nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, _, err := config.Resolve(labelled("", tc.repository, "", nil))
			if err != nil {
				t.Fatalf("Resolve(%s) = error %v, want the resolved configuration", tc.name, err)
			}
			if got := cfg.Analysis.Platforms(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Resolve(%s).Analysis.Platforms() = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}

func TestPrintWritesEveryMatrixEntryInTheShapeTheDocumentWroteIt(t *testing.T) {
	t.Parallel()

	cfg, provenance, err := config.Resolve(labelled("", mixedMatrix, "", nil))
	if err != nil {
		t.Fatalf("Resolve(a matrix listing both shapes) = error %v, want the resolved configuration", err)
	}
	var printed bytes.Buffer
	if err := config.Print(&printed, cfg, provenance); err != nil {
		t.Fatalf("Print(a matrix listing both shapes) = error %v, want the configuration", err)
	}
	var document struct {
		Analysis struct {
			Configurations []map[string]any `json:"configurations"`
		} `json:"analysis"`
	}
	if err := json.Unmarshal(printed.Bytes(), &document); err != nil {
		t.Fatalf("decode the printed configuration: %v\n%s", err, printed.String())
	}
	want := []map[string]any{
		{"id": "tsconfig.json", "project": "tsconfig.json"},
		{"id": "linux-amd64", "os": "linux", "arch": "amd64", "tags": []any{}},
		{"id": "packages/app/tsconfig.json", "project": "packages/app/tsconfig.json"},
		{"id": "linux-arm64-netgo", "os": "linux", "arch": "arm64", "tags": []any{"netgo"}},
	}
	if got := document.Analysis.Configurations; !reflect.DeepEqual(got, want) {
		t.Errorf("Print(a matrix listing both shapes) wrote analysis.configurations = %v, want %v", got, want)
	}
}

// A flag document is decoded without the member walk a configuration file goes
// through, so an entry of the build matrix supplied as a flag is held to the closed
// member list of its shape by its own decoding.
func TestResolveRefusesAFlagSuppliedMatrixEntryNamingAnUndeclaredMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry string
	}{
		{name: "a_platform_entry", entry: `{"id": "a", "os": "linux", "arch": "amd64", "cgo": true}`},
		{name: "a_project_entry", entry: `{"id": "a", "project": "tsconfig.json", "references": []}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			flags := `{"analysis.configurations": [` + tc.entry + `]}`
			_, _, err := config.Resolve(labelled(flags, `{"target": {"kind": "application"}}`, "",
				map[string]string{"analysis.configurations": "--configurations"}))
			if _, ok := errors.AsType[*config.Error](err); !ok {
				t.Fatalf("Resolve(a flag supplying %s) = error %v, want a *config.Error", tc.entry, err)
			}
		})
	}
}

func TestPrintRefusesAMatrixEntryOfNoSingleShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry config.Configuration
	}{
		{name: "neither_shape", entry: config.Configuration{}},
		{name: "both_shapes", entry: config.Configuration{
			Platform: &config.Platform{ID: "a", OS: "linux", Arch: "amd64"},
			Project:  &config.Project{ID: "a", Path: "tsconfig.json"},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.Target.Kind = config.Application
			cfg.Analysis.Configurations = []config.Configuration{tc.entry}
			var printed bytes.Buffer
			if err := config.Print(&printed, cfg, config.Provenance{}); err == nil {
				t.Errorf("Print(a matrix entry of %s) = nil, want an error: the entry has no document form\n%s",
					tc.name, printed.String())
			}
		})
	}
}
