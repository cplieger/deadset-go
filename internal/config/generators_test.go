package config_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	spec "github.com/cplieger/deadset-spec"
	"pgregory.net/rapid"
)

// rankedSources are the sources of a setting, in the order they outrank each
// other.
func rankedSources() []config.Source {
	return []config.Source{config.SourceFlag, config.SourceRepository, config.SourceCentral}
}

// enumOf draws one value of a closed set, so a shrink walks the set from its
// first member.
func enumOf[T ~string](values ...T) *rapid.Generator[any] {
	return rapid.SampledFrom(values).AsAny()
}

// arrayOfDistinct draws an array of distinct entries, which is what every array
// of the closed key list accepts, between minLen and maxLen of them.
func arrayOfDistinct[T comparable](entry *rapid.Generator[T], minLen, maxLen int) *rapid.Generator[any] {
	return rapid.SliceOfNDistinct(entry, minLen, maxLen, rapid.ID[T]).AsAny()
}

// settingGenerators returns one generator per setting the closed key list
// declares, keyed by the setting's dotted path. Every draw is a value that key
// accepts, so a document built from a draw resolves rather than being refused,
// and every generator is composed of rapid's combinators rather than switching on
// one drawn number, so a counterexample shrinks to the settings and the values
// that carry the failure.
//
// Each setting has at least two reachable values, because a source outranking
// another is only observable where the two carry different values.
func settingGenerators() map[string]*rapid.Generator[any] {
	return map[string]*rapid.Generator[any]{
		"contract_version":         rapid.StringMatching(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`).AsAny(),
		"target.kind":              enumOf(config.Application, config.Library),
		"analysis.languages":       arrayOfDistinct(rapid.SampledFrom([]config.Language{config.GoLanguage, config.TSLanguage}), 0, 2),
		"analysis.min_confidence":  enumOf(config.Certain, config.Probable, config.Possible),
		"analysis.generated_files": enumOf(config.ExcludeGenerated, config.IncludeGenerated),
		"analysis.consumer_tests":  enumOf(config.TestReference, config.ProductionReference),
		"analysis.configurations":  rapid.SliceOfN(configurationEntry(), 0, 3).AsAny(),
		"analysis.matrix.complete": rapid.Bool().AsAny(),
		"analysis.template_dirs":   arrayOfDistinct(rapid.StringMatching(`^[a-z]{1,8}(/[a-z]{1,8})?$`), 0, 2),
		"analysis.template_delimiters": rapid.Custom(func(t *rapid.T) map[string]any {
			return map[string]any{
				"left":  rapid.SampledFrom([]string{"{{", "[[", "<%", "{%"}).Draw(t, "the left delimiter"),
				"right": rapid.SampledFrom([]string{"}}", "]]", "%>", "%}"}).Draw(t, "the right delimiter"),
			}
		}).AsAny(),
		"consumers.complete":  rapid.Bool().AsAny(),
		"roots.patterns":      arrayOfDistinct(symbolReference(), 0, 2),
		"exemptions.disabled": arrayOfDistinct(rapid.StringMatching(`^[a-z][a-z0-9-]{0,10}$`), 0, 2),
		"reporters.formats": arrayOfDistinct(rapid.SampledFrom([]config.Format{
			config.Text, config.JSON, config.GitHub, config.SARIF, config.Template,
		}), 1, 3),
		"reporters.sort":         enumOf(config.ByPosition, config.BySize),
		"reporters.cascade":      enumOf(config.CascadeRoots, config.CascadeFull),
		"reporters.max_findings": rapid.IntRange(0, 1000).AsAny(),
		"reporters.fail_on":      enumOf(config.Allow, config.Warn, config.Deny),
		"ts.test_files":          arrayOfDistinct(rapid.StringMatching(`^\*\*/\*\.[a-z]{2,4}$`), 1, 2),
		"ts.entry_files":         arrayOfDistinct(rapid.StringMatching(`^src/[a-z]{1,6}\.ts$`), 0, 2),
	}
}

// configurationEntry draws one entry of the build matrix, each member drawn on its
// own so a shrink reaches the member that carries a failure.
func configurationEntry() *rapid.Generator[map[string]any] {
	return rapid.Custom(func(t *rapid.T) map[string]any {
		return map[string]any{
			"id":   rapid.StringMatching(`^[a-z]{1,7}-[a-z0-9]{1,6}$`).Draw(t, "configuration id"),
			"os":   rapid.SampledFrom([]string{"linux", "darwin", "windows"}).Draw(t, "configuration os"),
			"arch": rapid.SampledFrom([]string{"amd64", "arm64", "386"}).Draw(t, "configuration arch"),
			"tags": rapid.SliceOfNDistinct(rapid.StringMatching(`^[a-z][a-z0-9]{0,5}$`), 0, 2,
				rapid.ID[string]).Draw(t, "configuration tags"),
		}
	})
}

// symbolReference draws one entry of roots.patterns: a symbol reference, or a
// pattern over one, in the grammar the Contract states.
func symbolReference() *rapid.Generator[string] {
	return rapid.StringMatching(`^(go|ts)://example\.(com|test)/[a-z]{1,4}#([A-Z][A-Za-z]{0,4}|\*)(\.([A-Za-z]{1,4}|\*))?$`)
}

// familyKeyLength is the length of a severity key naming a whole family: the
// prefix and two digits, where a code carries four.
const familyKeyLength = 4

// severityKeys draws the keys of a generated severity object: distinct keys drawn
// from the ones a configuration may set.
func severityKeys(settable []string) *rapid.Generator[[]string] {
	return rapid.SliceOfNDistinct(rapid.SampledFrom(settable), 0, 3, rapid.ID[string])
}

// namesFixedSeverity reports whether one severity key names a kind whose severity
// the Contract fixes, either as the code or as the family prefix holding it.
func namesFixedSeverity(key string, fixed []string) bool {
	for _, code := range fixed {
		if key == code || (len(key) == familyKeyLength && strings.HasPrefix(code, key)) {
			return true
		}
	}
	return false
}

// severityValue draws one value of the severity object.
func severityValue() *rapid.Generator[any] {
	return enumOf(config.Allow, config.Warn, config.Deny)
}

// provenanceEntry draws one value of a provenance object, in the syntax the
// closed key list declares for it.
func provenanceEntry() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.Just(string(config.SourceDefault)),
		rapid.StringMatching(`^(repository|central|flag): [a-z]{1,6}\.json$`),
	)
}

// contractKinds returns the codes of the issue kinds the Contract publishes as
// live and the codes among them whose severity it fixes, both in ascending order,
// read from the Contract rather than restated so a kind the Contract adds or fixes
// moves the generator with it.
func contractKinds(t *testing.T) (live, fixed []string) {
	t.Helper()

	data, err := spec.Contract.ReadFile("contract/kinds.json")
	if err != nil {
		t.Fatalf("read contract/kinds.json: %v", err)
	}
	var document struct {
		Kinds []struct {
			Code  string `json:"code"`
			Fixed bool   `json:"fixed"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode contract/kinds.json: %v", err)
	}
	for _, kind := range document.Kinds {
		live = append(live, kind.Code)
		if kind.Fixed {
			fixed = append(fixed, kind.Code)
		}
	}
	slices.Sort(live)
	slices.Sort(fixed)
	if len(live) == 0 || len(fixed) == 0 {
		t.Fatalf("contract/kinds.json publishes %d live kinds and fixes %d, want some of each",
			len(live), len(fixed))
	}
	return live, fixed
}

// settableSeverityKeys returns every key a severity object may set: one live
// issue-kind code, or one two-digit family prefix naming at least one live kind,
// with every key naming a kind whose severity the Contract fixes left out, because
// such a key is an unimplemented key rather than a setting.
func settableSeverityKeys(t *testing.T) []string {
	t.Helper()

	live, fixed := contractKinds(t)
	keys := make([]string, 0, len(live))
	for _, code := range live {
		for _, key := range []string{code, code[:familyKeyLength]} {
			if !slices.Contains(keys, key) && !namesFixedSeverity(key, fixed) {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// schemaSettings returns the dotted path of every setting the Contract's
// configuration schema declares, in ascending order: a value, an array, and an
// object carrying a default of its own, which is one setting a source supplies
// whole. A section holds no value, and an object whose member names a pattern
// declares resolves per member rather than as one setting.
func schemaSettings(t *testing.T) []string {
	t.Helper()

	var paths []string
	collectSchemaSettings(t, configSchema(t), "", &paths)
	if len(paths) == 0 {
		t.Fatal("config.schema.json declares no setting, so this test pins nothing")
	}
	slices.Sort(paths)
	return paths
}

// collectSchemaSettings appends the path of every setting at or below one
// declaration.
func collectSchemaSettings(t *testing.T, declaration map[string]any, at string, into *[]string) {
	t.Helper()

	if _, open := declaration["patternProperties"]; open {
		return
	}
	properties, holds := declaration["properties"]
	if !holds {
		if at != "" {
			*into = append(*into, at)
		}
		return
	}
	if _, carries := declaration["default"]; carries && at != "" {
		*into = append(*into, at)
		return
	}
	declared, isObject := properties.(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s: properties is %T, want an object", at, properties)
	}
	for name, member := range declared {
		asObject, isObject := member.(map[string]any)
		if !isObject {
			t.Fatalf("config.schema.json: %s: %s is %T, want an object", at, name, member)
		}
		path := name
		if at != "" {
			path = at + "." + name
		}
		collectSchemaSettings(t, asObject, path, into)
	}
}

func TestSettingGeneratorsNameEveryDeclaredSetting(t *testing.T) {
	t.Parallel()

	// Without this the round-trip property ranges over the settings a generator
	// happened to be written for, so a key the Contract adds resolves, prints and
	// reads back untested.
	want := schemaSettings(t)
	got := slices.Sorted(maps.Keys(settingGenerators()))
	if !slices.Equal(got, want) {
		t.Errorf("settingGenerators() names %v, want the settings config.schema.json declares %v", got, want)
	}
	for _, path := range got {
		if !config.DeclaresSetting(path) {
			t.Errorf("settingGenerators() names %q, want every key to be a setting DeclaresSetting resolves", path)
		}
	}
}
