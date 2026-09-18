package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	spec "github.com/cplieger/deadset-spec"
)

// contractDocument decodes one document of the Contract this package implements.
func contractDocument(t *testing.T, name string) map[string]any {
	t.Helper()

	data, err := spec.Contract.ReadFile("contract/" + name)
	if err != nil {
		t.Fatalf("read contract/%s: %v", name, err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode contract/%s: %v", name, err)
	}
	return document
}

// schemaNode builds one node of the closed key list from the Contract's
// configuration schema: an object declaring members is a section, an object
// leaving its member names to a pattern is an open object, an array of objects is
// a list, and everything else holds one value.
func schemaNode(t *testing.T, at string, declaration map[string]any) keyNode {
	t.Helper()

	if properties, held := declaration["properties"]; held {
		return keyNode{kind: keySection, members: schemaMembers(t, at, properties)}
	}
	if _, held := declaration["patternProperties"]; held {
		return keyNode{kind: keyMap}
	}
	entry, isObject := declaration["items"].(map[string]any)
	if isObject {
		if properties, held := entry["properties"]; held {
			return keyNode{kind: keyList, members: schemaMembers(t, at, properties)}
		}
	}
	return keyNode{kind: keyLeaf}
}

// schemaMembers builds the members one properties object declares.
func schemaMembers(t *testing.T, at string, properties any) map[string]keyNode {
	t.Helper()

	declared, isObject := properties.(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s: properties is %T, want an object", at, properties)
	}
	members := make(map[string]keyNode, len(declared))
	for name, declaration := range declared {
		member, isObject := declaration.(map[string]any)
		if !isObject {
			t.Fatalf("config.schema.json: %s: %s is %T, want an object", at, name, declaration)
		}
		members[name] = schemaNode(t, joinKey(at, name), member)
	}
	return members
}

func TestSchemaRootEqualsTheContract(t *testing.T) {
	t.Parallel()

	want := schemaNode(t, "", contractDocument(t, "config.schema.json"))
	got := schemaRoot()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("schemaRoot() = %+v, want the closed key list of config.schema.json %+v", got, want)
	}
}

func TestContractVersionEqualsTheContract(t *testing.T) {
	t.Parallel()

	want, isString := contractDocument(t, "contract.json")["contract_version"].(string)
	if !isString {
		t.Fatalf("contract.json: contract_version is not a string")
	}
	if ContractVersion != want {
		t.Errorf("ContractVersion = %q, want contract.json's %q", ContractVersion, want)
	}
}

func TestFixedSeverityCodesEqualTheContract(t *testing.T) {
	t.Parallel()

	kinds, isList := contractDocument(t, "kinds.json")["kinds"].([]any)
	if !isList {
		t.Fatalf("kinds.json: kinds is not a list")
	}
	var want []string
	for _, entry := range kinds {
		kind, isObject := entry.(map[string]any)
		if !isObject {
			t.Fatalf("kinds.json: a kind is %T, want an object", entry)
		}
		if fixed, isBool := kind["fixed"].(bool); isBool && fixed {
			code, isString := kind["code"].(string)
			if !isString {
				t.Fatalf("kinds.json: a fixed kind has no code")
			}
			want = append(want, code)
		}
	}
	slices.Sort(want)

	got := fixedSeverityCodes()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("fixedSeverityCodes() = %v, want the codes kinds.json fixes %v", got, want)
	}
}

func TestPatternsEqualTheContract(t *testing.T) {
	t.Parallel()

	schema := contractDocument(t, "config.schema.json")
	properties, isObject := schema["properties"].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: properties is not an object")
	}

	tests := []struct {
		name    string
		got     string
		want    func() string
		wantKey string
	}{
		{
			name: "the_severity_key_pattern",
			got:  severityKeyPattern.String(),
			want: func() string { return onlyPatternKey(t, properties, "severity") },
		},
		{
			name: "the_contract_version_pattern",
			got:  contractVersionPattern.String(),
			want: func() string { return stringAt(t, properties, "contract_version", "pattern") },
		},
		{
			name: "the_exemption_class_pattern",
			got:  exemptionClassPattern.String(),
			want: func() string { return itemsPattern(t, properties, "exemptions", "disabled") },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if want := tc.want(); tc.got != want {
				t.Errorf("%s = %q, want the schema's own spelling %q", tc.name, tc.got, want)
			}
		})
	}
}

// onlyPatternKey returns the one key of an open object's patternProperties.
func onlyPatternKey(t *testing.T, properties map[string]any, name string) string {
	t.Helper()

	declared, isObject := properties[name].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s is not an object", name)
	}
	patterns, isObject := declared["patternProperties"].(map[string]any)
	if !isObject || len(patterns) != 1 {
		t.Fatalf("config.schema.json: %s declares %d patternProperties, want one", name, len(patterns))
	}
	for pattern := range patterns {
		return pattern
	}
	return ""
}

// stringAt returns one string a member of the schema declares.
func stringAt(t *testing.T, properties map[string]any, name, key string) string {
	t.Helper()

	declared, isObject := properties[name].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s is not an object", name)
	}
	value, isString := declared[key].(string)
	if !isString {
		t.Fatalf("config.schema.json: %s declares no %s", name, key)
	}
	return value
}

// itemsPattern returns the pattern the entries of one array member must match.
func itemsPattern(t *testing.T, properties map[string]any, section, member string) string {
	t.Helper()

	declared, isObject := properties[section].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s is not an object", section)
	}
	members, isObject := declared["properties"].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s declares no properties", section)
	}
	array, isObject := members[member].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s.%s is not an object", section, member)
	}
	items, isObject := array["items"].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s.%s declares no items", section, member)
	}
	pattern, isString := items["pattern"].(string)
	if !isString {
		t.Fatalf("config.schema.json: %s.%s items declare no pattern", section, member)
	}
	return pattern
}

func TestDeclaredKeysCoverEverySetting(t *testing.T) {
	t.Parallel()

	keys := declaredKeys()
	for _, path := range settingPaths() {
		if !slices.Contains(keys, path) {
			t.Errorf("declaredKeys() = %v, want it to contain the setting %q", keys, path)
		}
	}
	for _, want := range []string{
		"target.kind", "analysis.matrix.complete", "severity", "provenance", "go",
		"analysis.configurations", "analysis.configurations[].id",
	} {
		if !slices.Contains(keys, want) {
			t.Errorf("declaredKeys() = %v, want it to contain %q", keys, want)
		}
	}
	for _, unwanted := range []string{"severity", "analysis.configurations[].id", "analysis.matrix"} {
		if slices.Contains(settingPaths(), unwanted) {
			t.Errorf("settingPaths() = %v, want it to omit %q, which holds no value of its own",
				settingPaths(), unwanted)
		}
	}
	if !slices.Contains(settingPaths(), "analysis.configurations") {
		t.Errorf("settingPaths() = %v, want it to contain %q, which one flag supplies whole",
			settingPaths(), "analysis.configurations")
	}
}

func TestNearestKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "a_misspelled_leaf_names_its_own_section", key: "reporters.fail_under", want: "reporters.fail_on"},
		{name: "a_dotted_path_written_as_one_key_names_that_path", key: "analysis.min_confidence", want: "analysis.min_confidence"},
		{name: "a_misspelled_section_keeps_the_whole_path", key: "reporter.fail_on", want: "reporters.fail_on"},
		{name: "a_near_miss_of_a_root_key_names_that_key", key: "contract_versions", want: "contract_version"},
		{name: "a_misspelled_nested_section_names_the_section", key: "analysis.matrics", want: "analysis.matrix"},
		{name: "an_unrelated_key_still_names_one", key: "zzz", want: "go"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := nearestKey(tc.key); got != tc.want {
				t.Errorf("nearestKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestEditDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from string
		to   string
		want int
	}{
		{name: "equal_strings", from: "fail_on", to: "fail_on", want: 0},
		{name: "one_substitution", from: "fail_on", to: "fail_in", want: 1},
		{name: "one_deletion", from: "fail_on", to: "fail_o", want: 1},
		{name: "one_insertion", from: "fail_o", to: "fail_on", want: 1},
		{name: "empty_to_full", from: "", to: "sort", want: 4},
		{name: "counted_in_runes", from: "é", to: "e", want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := editDistance(tc.from, tc.to); got != tc.want {
				t.Errorf("editDistance(%q, %q) = %d, want %d", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestFixedByContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		want []string
	}{
		{name: "the_fixed_code_itself", code: "DS1703", want: []string{"DS1703"}},
		{name: "the_other_fixed_code", code: "DS1704", want: []string{"DS1704"}},
		{name: "the_family_prefix_holding_both", code: "DS17", want: []string{"DS1703", "DS1704"}},
		{name: "a_sibling_code_of_that_family", code: "DS1701", want: nil},
		{name: "another_family_prefix", code: "DS18", want: nil},
		{name: "another_code", code: "DS1101", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := fixedByContract(tc.code); !slices.Equal(got, tc.want) {
				t.Errorf("fixedByContract(%q) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}
