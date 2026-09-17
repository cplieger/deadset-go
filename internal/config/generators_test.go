package config_test

import (
	"encoding/json"
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
		"consumers.complete":       rapid.Bool().AsAny(),
		"roots.patterns":           arrayOfDistinct(symbolReference(), 0, 2),
		"exemptions.disabled":      arrayOfDistinct(rapid.StringMatching(`^[a-z][a-z0-9-]{0,10}$`), 0, 2),
		"reporters.formats": arrayOfDistinct(rapid.SampledFrom([]config.Format{
			config.Text, config.JSON, config.GitHub, config.SARIF, config.Template,
		}), 1, 3),
		"reporters.sort":         enumOf(config.ByPosition, config.BySize),
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

// severityKeys draws the keys of a generated severity object: distinct issue-kind
// codes and two-digit family prefixes, none of them naming a kind whose severity
// the Contract fixes, which would be an unimplemented key rather than a setting.
func severityKeys(fixed []string) *rapid.Generator[[]string] {
	key := rapid.StringMatching(`^DS[0-9]{2}([0-9]{2})?$`).Filter(func(key string) bool {
		return !namesFixedSeverity(key, fixed)
	})
	return rapid.SliceOfNDistinct(key, 0, 3, rapid.ID[string])
}

// namesFixedSeverity reports whether one severity key names a kind whose severity
// the Contract fixes, either as the code or as the family prefix holding it.
func namesFixedSeverity(key string, fixed []string) bool {
	for _, code := range fixed {
		if key == code || (len(key) == 4 && strings.HasPrefix(code, key)) {
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

// fixedSeverityCodes returns the codes whose severity the Contract fixes, read
// from the Contract rather than restated, so a Contract that fixes another code
// narrows the generator with it.
func fixedSeverityCodes(t *testing.T) []string {
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
	var fixed []string
	for _, kind := range document.Kinds {
		if kind.Fixed {
			fixed = append(fixed, kind.Code)
		}
	}
	slices.Sort(fixed)
	if len(fixed) == 0 {
		t.Fatal("contract/kinds.json fixes no severity, so the generator's filter pins nothing")
	}
	return fixed
}
