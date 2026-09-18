package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	spec "github.com/cplieger/deadset-spec"
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
