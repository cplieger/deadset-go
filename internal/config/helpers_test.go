package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// repositoryLabel is the path every repository configuration of these tests is
// read from.
const repositoryLabel = "deadset.json"

// fromRepository is the inputs of a run reading one repository configuration.
func fromRepository(document string) *config.Inputs {
	return &config.Inputs{Repository: config.Document{Path: repositoryLabel, Data: []byte(document)}}
}

// resolved resolves inputs, failing the test on a refusal.
func resolved(t *testing.T, in *config.Inputs) *config.Resolved {
	t.Helper()

	r, err := config.Resolve(in)
	if err != nil {
		t.Fatalf("Resolve = error %v, want the resolved configuration", err)
	}
	return r
}

// refused resolves inputs and returns the refusal, failing the test on anything
// else.
func refused(t *testing.T, in *config.Inputs) *config.Error {
	t.Helper()

	_, err := config.Resolve(in)
	var refusal *config.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve = error %v, want a *config.Error", err)
	}
	return refusal
}

// printedDocument prints a resolved configuration and decodes it.
func printedDocument(t *testing.T, r *config.Resolved) (raw []byte, document map[string]any) {
	t.Helper()

	var out bytes.Buffer
	if err := r.Print(&out); err != nil {
		t.Fatalf("Print = error %v, want the resolved configuration", err)
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("decode the printed configuration: %v\n%s", err, out.Bytes())
	}
	return out.Bytes(), document
}

// valueAt reads the value one dotted key names in a decoded document.
func valueAt(document map[string]any, key vocab.Key) (any, bool) {
	var value any = document
	for segment := range strings.SplitSeq(string(key), ".") {
		section, isSection := value.(map[string]any)
		if !isSection {
			return nil, false
		}
		held, named := section[segment]
		if !named {
			return nil, false
		}
		value = held
	}
	return value, true
}

// decoded decodes one JSON text.
func decoded(t *testing.T, text string) any {
	t.Helper()

	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return value
}

// nestedDocument is the document holding one value at a key's dotted path.
func nestedDocument(key vocab.Key, value string) string {
	document := value
	for _, segment := range slices.Backward(strings.Split(string(key), ".")) {
		document = `{"` + segment + `": ` + document + `}`
	}
	return document
}

// readByGo reports whether the Go analysis reads the place a refusal names: the
// key it is at or inside, and of a key written per language, the Go member.
func readByGo(place string) bool {
	path := place
	for {
		if row, declared := vocab.ConfigKeyOf(vocab.Key(path)); declared {
			if !row.ReadIn(vocab.Go) {
				return false
			}
			member, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(place, path), "."), ".")
			return len(row.PerLanguage) == 0 || member == "" || member == string(vocab.Go) ||
				!isLanguage(member)
		}
		cut := strings.LastIndexAny(path, ".[")
		if cut < 0 {
			return true
		}
		path = path[:cut]
	}
}

// isLanguage reports whether a name is one of the suite's languages.
func isLanguage(name string) bool {
	for _, language := range vocab.Languages() {
		if string(language) == name {
			return true
		}
	}
	return false
}
