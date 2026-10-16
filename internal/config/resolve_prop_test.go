package config_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"pgregory.net/rapid"
)

// Every setting the Go analysis reads takes its origin from the highest-ranked
// source that supplies it, whichever sources supply which settings.
func TestPropertyEverySettingComesFromTheHighestRankedSourceSupplyingIt(t *testing.T) {
	t.Parallel()

	var settings []vocab.ConfigKey
	for _, row := range vocab.ConfigKeys() {
		if row.ReadIn(vocab.Go) && row.Role == vocab.RoleSetting && row.Default != "" {
			settings = append(settings, row)
		}
	}
	rapid.Check(t, func(t *rapid.T) {
		in := &config.Inputs{Flags: map[vocab.Key][]byte{}}
		repository := map[string]any{"target": map[string]any{"kind": "library"}}
		central := map[string]any{}
		want := map[vocab.Key]string{}
		for i := range settings {
			row := &settings[i]
			flag := row.Option != "" && rapid.Bool().Draw(t, "flag "+string(row.Key))
			inRepository := rapid.Bool().Draw(t, "repository "+string(row.Key))
			inCentral := rapid.Bool().Draw(t, "central "+string(row.Key))
			want[row.Key] = "default"
			if inCentral {
				place(central, row.Key, row.Default)
				want[row.Key] = "central: central.json"
			}
			if inRepository {
				place(repository, row.Key, row.Default)
				want[row.Key] = "repository: " + repositoryLabel
			}
			if flag {
				in.Flags[row.Key] = []byte(row.Default)
				want[row.Key] = "flag: --" + row.Option
			}
		}
		in.Repository = config.Document{Path: repositoryLabel, Data: encoded(t, repository)}
		in.Central = config.Document{Path: "central.json", Data: encoded(t, central)}
		r, err := config.Resolve(in)
		if err != nil {
			t.Fatalf("Resolve = %v, want the resolved configuration", err)
		}
		for key, origin := range want {
			if got := r.Provenance[string(key)].String(); got != origin {
				t.Fatalf("provenance[%s] = %q, want %q", key, got, origin)
			}
		}
	})
}

// place writes one value at a key's dotted path inside a document.
func place(document map[string]any, key vocab.Key, value string) {
	segments := strings.Split(string(key), ".")
	at := document
	for _, segment := range segments[:len(segments)-1] {
		next, held := at[segment].(map[string]any)
		if !held {
			next = map[string]any{}
			at[segment] = next
		}
		at = next
	}
	at[segments[len(segments)-1]] = json.RawMessage(value)
}

// encoded is one document as JSON.
func encoded(t *rapid.T, document map[string]any) []byte {
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode %v: %v", document, err)
	}
	return data
}
