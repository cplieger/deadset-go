package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	spec "github.com/cplieger/deadset-spec/v3"
)

// usageExitCode is the code every refusal this package makes maps to.
const usageExitCode = 2

// resolvedDocument is a resolved configuration as the Contract publishes it: the
// closed key list, then the provenance object. Decoding both the expectation and
// the printed output into it compares them as values, so indentation and key
// order decide nothing, and an unknown member on either side is a refusal.
type resolvedDocument struct {
	config.Config
	Provenance map[string]string `json:"provenance"`
}

// publishedCases are the configuration cases the Contract publishes. The suite
// requires the directory to hold exactly these, so a case the Contract adds fails
// this test rather than going unexercised.
func publishedCases() []string {
	return []string{
		"array-spanning-lines",
		"duplicated-key",
		"missing-target-kind",
		"provenance-on-input",
		"quoted-key-with-a-dot",
		"resolved-configuration-round-trip",
		"template-delimiters-configured",
		"template-delimiters-half",
		"typescript-matrix-declared",
		"unimplemented-key",
	}
}

// caseFlagLabels are the flags one case's flag document was supplied through, as
// that case's expected provenance spells them.
func caseFlagLabels(name string) map[string]string {
	if name == "array-spanning-lines" {
		return map[string]string{"analysis.min_confidence": "--min-confidence"}
	}
	return nil
}

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

// decodeResolved decodes one resolved configuration, refusing a member the closed
// key list does not declare so a Contract key this package does not implement
// fails here.
func decodeResolved(t *testing.T, what string, data []byte) resolvedDocument {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var document resolvedDocument
	if err := dec.Decode(&document); err != nil {
		t.Fatalf("decode %s: %v\n%s", what, err, data)
	}
	return document
}

func TestPublishedConfigurationVectors(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(spec.Vectors, "vectors/config")
	if err != nil {
		t.Fatalf("read vectors/config: %v", err)
	}
	var found []string
	for _, entry := range entries {
		if entry.IsDir() {
			found = append(found, entry.Name())
		}
	}
	slices.Sort(found)
	if !slices.Equal(found, publishedCases()) {
		t.Fatalf("vectors/config holds %v, want the cases this suite runs %v", found, publishedCases())
	}

	for _, name := range publishedCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runCase(t, name)
		})
	}
}

// runCase runs one published case: its documents resolved, and either the
// resolved configuration or the refusal the case declares.
func runCase(t *testing.T, name string) {
	t.Helper()

	dir := path.Join("vectors/config", name)
	in := config.Inputs{
		Flags:           caseFile(t, dir, "flags.json"),
		Repository:      caseFile(t, dir, "repository.json"),
		Central:         caseFile(t, dir, "central.json"),
		RepositoryLabel: "repository.json",
		CentralLabel:    "central.json",
		FlagLabels:      caseFlagLabels(name),
	}
	resolved := caseFile(t, dir, "expected.json")
	refused := caseFile(t, dir, "expected-error.json")
	if (resolved == nil) == (refused == nil) {
		t.Fatalf("case %s carries expected.json = %t and expected-error.json = %t, want exactly one",
			name, resolved != nil, refused != nil)
	}

	cfg, provenance, err := config.Resolve(in)
	if refused != nil {
		assertRefused(t, name, refused, err)
		return
	}
	if err != nil {
		t.Fatalf("Resolve(%s) = error %v, want the resolved configuration", name, err)
	}
	assertResolved(t, name, resolved, cfg, provenance)
}

// refusedDocument is one row of the Contract's index of refused documents: the file,
// the schema that refuses it and the instance path the refusal names.
type refusedDocument struct {
	File         string `json:"file"`
	Schema       string `json:"schema"`
	InstancePath string `json:"instance_path"`
}

// dottedPath spells a JSON Pointer the way a refusal names a key: its member names
// joined by dots, and an array index in brackets after the array it indexes.
func dottedPath(pointer string) string {
	var path strings.Builder
	for segment := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		if segment != "" && strings.Trim(segment, "0123456789") == "" {
			path.WriteString("[" + segment + "]")
			continue
		}
		if path.Len() > 0 {
			path.WriteString(".")
		}
		path.WriteString(segment)
	}
	return path.String()
}

// refusedKey returns the key a refusal of one published refused document names: the
// value the schema refuses, or, where that value is an object carrying a member the
// schema declares nowhere at that place, the member, because a key the closed key list
// does not declare is refused by its own name.
func refusedKey(t *testing.T, schema map[string]any, document []byte, pointer string) string {
	t.Helper()

	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatalf("decode the refused document: %v", err)
	}
	declarations := []map[string]any{schema}
	for segment := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		if segment == "" {
			continue
		}
		declarations = schemaStep(declarations, segment)
		value = documentStep(value, segment)
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return dottedPath(pointer)
	}
	declared := map[string]bool{}
	for _, declaration := range declarations {
		for _, shape := range shapesOf(declaration) {
			properties, _ := shape["properties"].(map[string]any)
			for name := range properties {
				declared[name] = true
			}
		}
	}
	var undeclared []string
	for name := range object {
		if !declared[name] {
			undeclared = append(undeclared, name)
		}
	}
	switch len(undeclared) {
	case 0:
		return dottedPath(pointer)
	case 1:
		if pointer == "" {
			return undeclared[0]
		}
		return dottedPath(pointer) + "." + undeclared[0]
	default:
		t.Fatalf("the refused value at %q carries %d members its schema does not declare, %v, "+
			"want a document refused for one constraint", pointer, len(undeclared), undeclared)
		return ""
	}
}

// shapesOf returns one schema declaration together with the shapes its oneOf names,
// which between them declare the members a value at that place may carry.
func shapesOf(declaration map[string]any) []map[string]any {
	shapes := []map[string]any{declaration}
	alternatives, _ := declaration["oneOf"].([]any)
	for _, alternative := range alternatives {
		if shape, isObject := alternative.(map[string]any); isObject {
			shapes = append(shapes, shape)
		}
	}
	return shapes
}

// schemaStep returns the declarations of the value one JSON Pointer segment reaches
// from the values the given declarations describe: an array's items for an index,
// and the member a properties object declares for a name.
func schemaStep(declarations []map[string]any, segment string) []map[string]any {
	var next []map[string]any
	for _, declaration := range declarations {
		for _, shape := range shapesOf(declaration) {
			if strings.Trim(segment, "0123456789") == "" {
				if items, isObject := shape["items"].(map[string]any); isObject {
					next = append(next, items)
				}
				continue
			}
			properties, _ := shape["properties"].(map[string]any)
			if member, isObject := properties[segment].(map[string]any); isObject {
				next = append(next, member)
			}
		}
	}
	return next
}

// documentStep returns the value one JSON Pointer segment names inside a decoded
// value, or nil where it names none.
func documentStep(value any, segment string) any {
	switch held := value.(type) {
	case map[string]any:
		return held[segment]
	case []any:
		index, err := strconv.Atoi(segment)
		if err != nil || index < 0 || index >= len(held) {
			return nil
		}
		return held[index]
	default:
		return nil
	}
}

// Every configuration document the Contract publishes as refused is refused here
// with the usage code, and the refusal names the place in the document the schema's
// own refusal names, or the key inside it the closed key list does not declare.
func TestPublishedRefusedConfigurations(t *testing.T) {
	t.Parallel()

	schema := configSchema(t)
	data, err := spec.Examples.ReadFile("examples/negatives/index.json")
	if err != nil {
		t.Fatalf("read examples/negatives/index.json: %v", err)
	}
	var index struct {
		Negatives []refusedDocument `json:"negatives"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("decode examples/negatives/index.json: %v", err)
	}
	var refused []refusedDocument
	for _, row := range index.Negatives {
		if row.Schema == "contract/config.schema.json" {
			refused = append(refused, row)
		}
	}
	if len(refused) == 0 {
		t.Fatal("examples/negatives/index.json names no refused configuration document, so this test pins nothing")
	}

	for _, row := range refused {
		t.Run(strings.TrimSuffix(row.File, ".json"), func(t *testing.T) {
			t.Parallel()

			document, err := spec.Examples.ReadFile("examples/negatives/" + row.File)
			if err != nil {
				t.Fatalf("read examples/negatives/%s: %v", row.File, err)
			}
			_, _, err = config.Resolve(config.Inputs{Repository: document, RepositoryLabel: row.File})
			var refusal *config.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Resolve(%s) = error %v, want a *config.Error", row.File, err)
			}
			if want := refusedKey(t, schema, document, row.InstancePath); refusal.Key != want {
				t.Errorf("Resolve(%s) named %q, want %q, for the instance path %q the schema refuses",
					row.File, refusal.Key, want, row.InstancePath)
			}
		})
	}
}

// assertRefused checks the refusal against the case's expected-error.json: the
// exit code it maps to and the key or field its message names.
func assertRefused(t *testing.T, name string, expected []byte, err error) {
	t.Helper()

	var want struct {
		ExitCode int    `json:"exit_code"`
		Names    string `json:"names"`
	}
	if decodeErr := json.Unmarshal(expected, &want); decodeErr != nil {
		t.Fatalf("decode %s expected-error.json: %v", name, decodeErr)
	}

	var refusal *config.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve(%s) = error %v, want a *config.Error", name, err)
	}
	if want.ExitCode != usageExitCode {
		t.Errorf("case %s expects exit code %d, and every *config.Error maps to %d",
			name, want.ExitCode, usageExitCode)
	}
	if refusal.Key != want.Names {
		t.Errorf("Resolve(%s) named %q, want %q", name, refusal.Key, want.Names)
	}
	if !strings.Contains(refusal.Error(), want.Names) {
		t.Errorf("Resolve(%s) = %q, want the message to name %q", name, refusal.Error(), want.Names)
	}
}

// assertResolved checks the resolved configuration and its provenance against the
// case's expected.json, compared as decoded values, and checks that the printed
// output read back as a repository configuration resolves to the same
// configuration.
func assertResolved(t *testing.T, name string, expected []byte, cfg config.Config, provenance config.Provenance) {
	t.Helper()

	var printed bytes.Buffer
	if err := config.Print(&printed, cfg, provenance); err != nil {
		t.Fatalf("Print(%s) = error %v, want the resolved configuration", name, err)
	}
	got := decodeResolved(t, "the printed configuration of "+name, printed.Bytes())
	want := decodeResolved(t, name+"/expected.json", expected)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve(%s) printed\n%s\nwant\n%s", name, printed.String(), expected)
	}

	back, _, err := config.Resolve(config.Inputs{Repository: printed.Bytes(), RepositoryLabel: "printed.json"})
	if err != nil {
		t.Fatalf("Resolve(the printed configuration of %s) = error %v, want it to read back", name, err)
	}
	if !reflect.DeepEqual(back, cfg) {
		t.Errorf("Resolve(the printed configuration of %s) = %+v, want the configuration it was printed from %+v",
			name, back, cfg)
	}
}
