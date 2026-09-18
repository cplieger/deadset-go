package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"pgregory.net/rapid"
)

// targetKindPath is the one setting a resolved configuration must carry, so at
// least one source names it in every draw.
const targetKindPath = "target.kind"

// severityPrefix leads the dotted path of every setting of the severity object.
const severityPrefix = "severity."

// drawSources draws, for each setting in scope, which of the three sources name
// it and the value each carries, and returns the draw together with the settings
// it ranged over. The severity keys are drawn too, so the object's members are the
// generator's rather than three names chosen once.
//
// The target kind is drawn for at least one source, because a resolved
// configuration carrying none is refused rather than resolved, which is the
// subject of another test.
func drawSources(t *rapid.T, settable []string) (map[config.Source]map[string]any, []string) {
	generators := settingGenerators()
	for _, key := range severityKeys(settable).Draw(t, "the severity keys") {
		generators[severityPrefix+key] = severityValue()
	}
	paths := slices.Sorted(maps.Keys(generators))

	drawn := map[config.Source]map[string]any{}
	for _, source := range rankedSources() {
		drawn[source] = map[string]any{}
	}
	named := false
	for _, path := range paths {
		for _, source := range rankedSources() {
			if !rapid.Bool().Draw(t, string(source)+" names "+path) {
				continue
			}
			drawn[source][path] = generators[path].Draw(t, string(source)+" "+path)
			named = named || path == targetKindPath
		}
	}
	if !named {
		source := rapid.SampledFrom(rankedSources()).Draw(t, "the source of "+targetKindPath)
		drawn[source][targetKindPath] = generators[targetKindPath].Draw(t, targetKindPath)
	}
	return drawn, paths
}

// drawProvenanceObject draws a provenance object one source carries at its root:
// an entry for any of the settings in scope, each naming a source in the syntax
// the closed key list declares for it.
func drawProvenanceObject(t *rapid.T, paths []string) map[string]any {
	entries := map[string]any{}
	carried := rapid.SliceOfNDistinct(rapid.SampledFrom(paths), 0, 3, rapid.ID[string])
	for _, path := range carried.Draw(t, "the settings the provenance object names") {
		entries[path] = provenanceEntry().Draw(t, "the provenance of "+path)
	}
	return map[string]any{"provenance": entries}
}

// inputsOf renders one draw as the documents an invocation supplies: the flat flag
// document keyed by dotted path, and the nested repository and central documents.
func inputsOf(t *rapid.T, drawn map[config.Source]map[string]any, extra map[string]any) config.Inputs {
	t.Helper()

	in := config.Inputs{
		RepositoryLabel: "deadset.json",
		CentralLabel:    "central.json",
		FlagLabels:      map[string]string{},
	}
	if flags := drawn[config.SourceFlag]; len(flags) > 0 {
		flat := map[string]any{}
		for path, value := range flags {
			flat[path] = value
			in.FlagLabels[path] = flagName(path)
		}
		in.Flags = encodeDocument(t, flat)
	}
	in.Repository = nestedDocument(t, drawn[config.SourceRepository], extra)
	in.Central = nestedDocument(t, drawn[config.SourceCentral], extra)
	return in
}

// flagName spells the flag one dotted setting path was supplied through.
func flagName(path string) string {
	return "--" + strings.NewReplacer(".", "-", "_", "-").Replace(path)
}

// nestedDocument renders the settings one source names as the nested object the
// closed key list declares, or nil when the source names none. extra is written at
// the root as it stands, which is how a provenance object reaches a source.
func nestedDocument(t *rapid.T, values, extra map[string]any) []byte {
	t.Helper()

	if len(values) == 0 && len(extra) == 0 {
		return nil
	}
	document := map[string]any{}
	for _, path := range slices.Sorted(maps.Keys(values)) {
		nestInto(t, document, path, values[path])
	}
	maps.Copy(document, extra)
	return encodeDocument(t, document)
}

// fataler is how a helper a test and a property both call reports a failure of its
// own. Neither *testing.T nor *rapid.T satisfies the other's interface, and these
// two methods are all such a helper needs.
type fataler interface {
	Fatalf(format string, args ...any)
	Helper()
}

// nestInto writes one value into a document at the path its dotted segments name.
func nestInto(t fataler, into map[string]any, path string, value any) {
	t.Helper()

	segments := strings.Split(path, ".")
	for _, segment := range segments[:len(segments)-1] {
		child, held := into[segment]
		if !held {
			child = map[string]any{}
			into[segment] = child
		}
		section, isSection := child.(map[string]any)
		if !isSection {
			t.Fatalf("nestInto(%q): %q already holds %T", path, segment, child)
		}
		into = section
	}
	into[segments[len(segments)-1]] = value
}

// encodeDocument renders one document a test or a property built.
func encodeDocument(t fataler, document map[string]any) []byte {
	t.Helper()

	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("render a generated document: %v", err)
	}
	return data
}

// normalized returns one value as a JSON decode produces it, so a generated value
// and a value read back from a printed configuration compare equal.
func normalized(t *rapid.T, value any) any {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("render a generated value: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode a generated value: %v", err)
	}
	return decoded
}

// expectedSetting returns the source the resolution must take one setting from
// and the value it must resolve to, computed from the draw rather than from the
// package.
func expectedSetting(path string, drawn map[config.Source]map[string]any, defaults map[string]any) (config.Source, any) {
	for _, source := range rankedSources() {
		if value, named := drawn[source][path]; named {
			return source, value
		}
	}
	if path == "contract_version" {
		return config.SourceDefault, config.ContractVersion
	}
	return config.SourceDefault, defaults[path]
}

// printResolved prints one resolved configuration and decodes it, which is how a
// resolved value is read back by its dotted path.
func printResolved(t *rapid.T, cfg config.Config, provenance config.Provenance) ([]byte, map[string]any) {
	t.Helper()

	var out bytes.Buffer
	if err := config.Print(&out, cfg, provenance); err != nil {
		t.Fatalf("Print() = error %v, want the resolved configuration", err)
	}
	var document map[string]any
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("decode the printed configuration: %v", err)
	}
	if len(document) == 0 {
		t.Fatal("the printed configuration is empty")
	}
	return out.Bytes(), document
}

// Property dead-code-suite/P24: for any pair of a central and a repository
// configuration plus flags, every setting the repository names resolves to the
// repository's value, every setting only the central configuration names resolves
// to the central value, a flag outranks both, a provenance object present in any
// source changes no resolved value, and the printed configuration read back as a
// repository configuration resolves to an equivalent configuration.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a
// deeper local run.
func TestPropertyConfigurationResolutionRoundTrips(t *testing.T) {
	t.Parallel()

	defaults := schemaDefaults(t)
	settable := settableSeverityKeys(t)

	rapid.Check(t, func(t *rapid.T) {
		drawn, paths := drawSources(t, settable)

		in := inputsOf(t, drawn, nil)
		cfg, provenance, err := config.Resolve(in)
		if err != nil {
			t.Fatalf("Resolve(%s | %s | %s) = error %v, want the resolved configuration",
				in.Flags, in.Repository, in.Central, err)
		}
		_, document := printResolved(t, cfg, provenance)

		for _, path := range paths {
			wantSource, wantValue := expectedSetting(path, drawn, defaults)
			if wantSource == config.SourceDefault && strings.HasPrefix(path, severityPrefix) {
				continue
			}
			if got := provenance[path].Source; got != wantSource {
				t.Fatalf("Resolve(%s | %s | %s) provenance[%q] = %q, want %q",
					in.Flags, in.Repository, in.Central, path, got, wantSource)
			}
			got, held := valueAt(document, path)
			if !held {
				t.Fatalf("Resolve(%s | %s | %s) printed no %q", in.Flags, in.Repository, in.Central, path)
			}
			if !reflect.DeepEqual(got, normalized(t, wantValue)) {
				t.Fatalf("Resolve(%s | %s | %s) resolved %s = %#v, want %s's %#v",
					in.Flags, in.Repository, in.Central, path, got, wantSource, wantValue)
			}
		}

		assertProvenanceOnInputChangesNothing(t, drawn, paths, cfg, provenance)
		assertPrintedReadsBack(t, cfg, provenance)
	})
}

// assertProvenanceOnInputChangesNothing checks that a provenance object carried by
// a source changes neither a resolved value nor the origin resolution found.
func assertProvenanceOnInputChangesNothing(
	t *rapid.T, drawn map[config.Source]map[string]any, paths []string,
	cfg config.Config, provenance config.Provenance,
) {
	t.Helper()

	carried := drawProvenanceObject(t, paths)
	in := inputsOf(t, drawn, carried)
	withProvenance, withProvenanceOrigins, err := config.Resolve(in)
	if err != nil {
		t.Fatalf("Resolve(%s | %s | %s) = error %v, want the same resolved configuration",
			in.Flags, in.Repository, in.Central, err)
	}
	if !reflect.DeepEqual(withProvenance, cfg) {
		t.Fatalf("Resolve(the draw carrying %v) = %+v, want the same configuration %+v",
			carried, withProvenance, cfg)
	}
	if !reflect.DeepEqual(withProvenanceOrigins, provenance) {
		t.Fatalf("Resolve(the draw carrying %v) provenance = %v, want %v",
			carried, withProvenanceOrigins, provenance)
	}
}

// assertPrintedReadsBack checks that the printed configuration read back as a
// repository configuration resolves to the same configuration, every setting now
// coming from that document.
func assertPrintedReadsBack(t *rapid.T, cfg config.Config, provenance config.Provenance) {
	t.Helper()

	printed, _ := printResolved(t, cfg, provenance)
	back, backOrigins, err := config.Resolve(config.Inputs{Repository: printed, RepositoryLabel: "printed.json"})
	if err != nil {
		t.Fatalf("Resolve(the printed configuration) = error %v, want it to read back\n%s", err, printed)
	}
	if !reflect.DeepEqual(back, cfg) {
		t.Fatalf("Resolve(the printed configuration) = %+v, want the configuration it was printed from %+v\n%s",
			back, cfg, printed)
	}
	want := config.Origin{Source: config.SourceRepository, Label: "printed.json"}
	for _, path := range slices.Sorted(maps.Keys(backOrigins)) {
		if got := backOrigins[path]; got != want {
			t.Fatalf("Resolve(the printed configuration) provenance[%q] = %q, want %q\n%s",
				path, got, want, printed)
		}
	}
}

// declaredSections returns the members the Contract's configuration schema
// declares for each section, keyed by the section's dotted path, the root under the
// empty path. An open object declares no member and is not a section, and an
// array's entries are not reached.
func declaredSections(t *testing.T) map[string][]string {
	t.Helper()

	sections := map[string][]string{}
	collectSections(t, configSchema(t), "", sections)
	return sections
}

// collectSections appends the members of every section below one declaration.
func collectSections(t *testing.T, declaration map[string]any, at string, into map[string][]string) {
	t.Helper()

	properties, held := declaration["properties"].(map[string]any)
	if !held {
		return
	}
	members := make([]string, 0, len(properties))
	for name, member := range properties {
		declared, isObject := member.(map[string]any)
		if !isObject {
			t.Fatalf("config.schema.json: %s.%s is %T, want an object", at, name, member)
		}
		members = append(members, name)
		path := name
		if at != "" {
			path = at + "." + name
		}
		collectSections(t, declared, path, into)
	}
	slices.Sort(members)
	into[at] = members
}

// drawUndeclaredKey draws a key one section does not declare: a near miss of a
// member it declares, or a name no member resembles. A draw that lands on a
// declared member is no case of this property, and rapid draws another.
func drawUndeclaredKey(t *rapid.T, members []string) string {
	keys := rapid.StringMatching(`^[a-z]{3,8}$`)
	if len(members) > 0 {
		keys = rapid.OneOf(nearMissOf(members), keys)
	}
	key := keys.Draw(t, "a key the section does not declare")
	if slices.Contains(members, key) {
		t.Skipf("%q is a member the section declares", key)
	}
	return key
}

// nearMissOf draws a name one member of the closed key list is one edit away
// from: a rune dropped, doubled, replaced or transposed, or one appended. Each
// part of the edit is its own draw, so a shrink reaches the shortest member, the
// earliest position and the first letter rather than another arbitrary mutation.
func nearMissOf(members []string) *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		name := []rune(rapid.SampledFrom(members).Draw(t, "the member missed"))
		written := rapid.SampledFrom(letters()).Draw(t, "the letter written")
		if len(name) < 2 {
			return string(name) + string(written)
		}
		at := rapid.IntRange(0, len(name)-1).Draw(t, "the position edited")
		switch rapid.SampledFrom(edits()).Draw(t, "the edit") {
		case "append":
			return string(name) + string(written)
		case "drop":
			return string(slices.Delete(slices.Clone(name), at, at+1))
		case "double":
			return string(slices.Insert(slices.Clone(name), at, name[at]))
		case "replace":
			edited := slices.Clone(name)
			edited[at] = written
			return string(edited)
		default:
			edited := slices.Clone(name)
			other := (at + 1) % len(edited)
			edited[at], edited[other] = edited[other], edited[at]
			return string(edited)
		}
	})
}

// edits are the one-rune edits a near miss of a declared member applies.
func edits() []string {
	return []string{"append", "drop", "double", "replace", "transpose"}
}

// letters are the runes a near miss writes.
func letters() []rune {
	return []rune("abcdefghijklmnopqrstuvwxyz")
}

// Property dead-code-suite/P25: for any generated key absent from the Contract's
// configuration schema, including a near miss of an implemented key, the run is
// refused with the usage code and the refusal names that key.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a
// deeper local run.
func TestPropertyAnUnimplementedKeyIsNamedRatherThanIgnored(t *testing.T) {
	t.Parallel()

	sections := declaredSections(t)
	sectionPaths := slices.Sorted(maps.Keys(sections))

	rapid.Check(t, func(t *rapid.T) {
		section := rapid.SampledFrom(sectionPaths).Draw(t, "the section holding the key")
		key := drawUndeclaredKey(t, sections[section])

		path := key
		if section != "" {
			path = section + "." + key
		}
		document := map[string]any{"target": map[string]any{"kind": "library"}}
		nestInto(t, document, path, "a value the run never reads")

		_, _, err := config.Resolve(config.Inputs{
			Repository:      encodeDocument(t, document),
			RepositoryLabel: "deadset.json",
		})
		var refusal *config.Error
		if !errors.As(err, &refusal) {
			t.Fatalf("Resolve(a document naming %q) = error %v, want a *config.Error", path, err)
		}
		if refusal.Kind != config.KindUnimplementedKey {
			t.Fatalf("Resolve(a document naming %q) = kind %v, want %v",
				path, refusal.Kind, config.KindUnimplementedKey)
		}
		if refusal.Key != path {
			t.Fatalf("Resolve(a document naming %q) named %q, want %q", path, refusal.Key, path)
		}
		if !strings.Contains(refusal.Error(), path) {
			t.Fatalf("Resolve(a document naming %q) = %q, want the message to name the key",
				path, refusal.Error())
		}
	})
}
