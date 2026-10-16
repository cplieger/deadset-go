package config_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/document"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// product is this analyzer's name, as a case's products list names it.
const product = "deadset-go"

// caseFile reads one file of a published case, or nil when the case has none.
func caseFile(t *testing.T, dir, name string) []byte {
	t.Helper()

	data, err := spec.Vectors.ReadFile(path.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path.Join(dir, name), err)
	}
	return data
}

// Every configuration case the Contract publishes that applies to this analyzer
// resolves to the configuration or the refusal it declares.
func TestPublishedConfigurationVectors(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(spec.Vectors, "vectors/config")
	if err != nil {
		t.Fatalf("read vectors/config: %v", err)
	}
	ran := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := path.Join("vectors/config", entry.Name())
		var declared struct {
			Products []string `json:"products"`
		}
		if err := json.Unmarshal(caseFile(t, dir, "case.json"), &declared); err != nil {
			t.Fatalf("decode %s/case.json: %v", dir, err)
		}
		if declared.Products != nil && !slices.Contains(declared.Products, product) {
			continue
		}
		ran++
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			runCase(t, dir)
		})
	}
	if ran == 0 {
		t.Fatal("no published configuration case applies to this analyzer, so this test pins nothing")
	}
}

// runCase runs one published case: its documents resolved, and either the
// resolved configuration or the refusal the case declares.
func runCase(t *testing.T, dir string) {
	t.Helper()

	in := &config.Inputs{
		Repository: config.Document{Path: "repository.json", Data: caseFile(t, dir, "repository.json")},
		Central:    config.Document{Path: "central.json", Data: caseFile(t, dir, "central.json")},
	}
	if flags := caseFile(t, dir, "flags.json"); flags != nil {
		var settings map[vocab.Key]json.RawMessage
		if err := json.Unmarshal(flags, &settings); err != nil {
			t.Fatalf("decode %s/flags.json: %v", dir, err)
		}
		in.Flags = map[vocab.Key][]byte{}
		for key, value := range settings {
			in.Flags[key] = value
		}
	}
	expected, refusal := caseFile(t, dir, "expected.json"), caseFile(t, dir, "expected-error.json")
	if (expected == nil) == (refusal == nil) {
		t.Fatalf("%s carries expected.json %t and expected-error.json %t, want exactly one", dir, expected != nil, refusal != nil)
	}
	r, err := config.Resolve(in)
	if refusal != nil {
		var want struct {
			Names    string `json:"names"`
			ExitCode int    `json:"exit_code"`
		}
		if decodeErr := json.Unmarshal(refusal, &want); decodeErr != nil {
			t.Fatalf("decode %s/expected-error.json: %v", dir, decodeErr)
		}
		var refused *config.Error
		if !errors.As(err, &refused) || want.ExitCode != 2 || !strings.Contains(refused.Error(), want.Names) {
			t.Errorf("Resolve(%s) = %v, want a refusal with the usage code naming %q (expected exit %d)", dir, err, want.Names, want.ExitCode)
		}
		return
	}
	if err != nil {
		t.Fatalf("Resolve(%s) = error %v, want the resolved configuration", dir, err)
	}
	_, got := printedDocument(t, r)
	var want map[string]any
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatalf("decode %s/expected.json: %v", dir, err)
	}
	if want := readByThisAnalyzer(want); !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve(%s) printed\n%v\nwant the keys of expected.json the Go analysis reads\n%v", dir, got, want)
	}
}

// readByThisAnalyzer narrows a resolved configuration to what the Go analysis
// reads: the keys the table says it reads, a per-language value's Go member, and
// the provenance entries of those keys.
func readByThisAnalyzer(resolved map[string]any) map[string]any {
	narrowed := narrowSection("", resolved)
	provenance := map[string]any{}
	held, _ := resolved["provenance"].(map[string]any)
	for place, origin := range held {
		if readByGo(place) {
			provenance[place] = origin
		}
	}
	narrowed["provenance"] = provenance
	return narrowed
}

// narrowSection is the members of one section the Go analysis reads.
func narrowSection(at string, members map[string]any) map[string]any {
	held := map[string]any{}
	for name, value := range members {
		key := name
		if at != "" {
			key = at + "." + name
		}
		row, declared := vocab.ConfigKeyOf(vocab.Key(key))
		if !declared || !row.ReadIn(vocab.Go) || row.Role == vocab.RoleIgnored {
			continue
		}
		switch section, isSection := value.(map[string]any); {
		case row.Role == vocab.RoleSection && isSection:
			held[name] = narrowSection(key, section)
		case len(row.PerLanguage) > 0 && isSection:
			held[name] = map[string]any{string(vocab.Go): section[string(vocab.Go)]}
		default:
			held[name] = value
		}
	}
	return held
}

// Every configuration document the Contract publishes as refused is refused by
// this analyzer naming the place the schema's first refusal names, where the Go
// analysis reads it, and accepted unread where it does not.
func TestPublishedRefusedConfigurations(t *testing.T) {
	t.Parallel()

	const schemaPath = "contract/config.schema.json"
	schema, err := document.Compile(spec.Contract, schemaPath)
	if err != nil {
		t.Fatalf("compile %s: %v", schemaPath, err)
	}
	data, err := spec.Examples.ReadFile("examples/negatives/index.json")
	if err != nil {
		t.Fatalf("read examples/negatives/index.json: %v", err)
	}
	var index struct {
		Negatives []struct {
			File   string `json:"file"`
			Schema string `json:"schema"`
		} `json:"negatives"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("decode examples/negatives/index.json: %v", err)
	}
	ran := 0
	for _, row := range index.Negatives {
		if row.Schema != schemaPath {
			continue
		}
		ran++
		t.Run(strings.TrimSuffix(row.File, ".json"), func(t *testing.T) {
			t.Parallel()

			body, err := spec.Examples.ReadFile("examples/negatives/" + row.File)
			if err != nil {
				t.Fatalf("read examples/negatives/%s: %v", row.File, err)
			}
			value, err := document.Decode(body)
			if err != nil {
				t.Fatalf("decode examples/negatives/%s: %v", row.File, err)
			}
			first := schema.FirstRefusal(value)
			if first == nil {
				t.Fatalf("%s admits examples/negatives/%s, want a refused document", schemaPath, row.File)
			}
			place := first.Key("", value)
			in := &config.Inputs{
				Repository: config.Document{Path: row.File, Data: body},
				Central:    config.Document{Path: "central.json", Data: []byte(`{"target": {"kind": "library"}}`)},
			}
			_, err = config.Resolve(in)
			if !readByGo(place) {
				if err != nil {
					t.Errorf("Resolve(%s) = %v, want it accepted: the Go analysis does not read %s", row.File, err, place)
				}
				return
			}
			var refused *config.Error
			if !errors.As(err, &refused) || !strings.Contains(refused.Error(), place) {
				t.Errorf("Resolve(%s) = %v, want a refusal naming %q", row.File, err, place)
			}
		})
	}
	if ran == 0 {
		t.Fatal("examples/negatives/index.json names no refused configuration document, so this test pins nothing")
	}
}
