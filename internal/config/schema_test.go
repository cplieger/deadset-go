package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	spec "github.com/cplieger/deadset-spec/v5"
)

// settingMark is the member the configuration schema marks a setting written as
// an object with. An object without it is a section.
const settingMark = "x-setting"

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
// configuration schema: an object declaring members is a section, one the schema
// marks as a setting is a setting written as an object, an object leaving its
// member names to a pattern is an open object, an array of objects is a list, and
// everything else holds one value. A list whose entries take one of several shapes
// declares the members of every shape.
//
// The mark is what tells the two objects apart, and it is the schema's own
// statement of the difference, read rather than inferred: a higher-ranked source
// replaces a marked object whole and names it in one provenance entry, where a
// section's keys are replaced one by one and named one entry each.
func schemaNode(t *testing.T, at string, declaration map[string]any) keyNode {
	t.Helper()

	if properties, held := declaration["properties"]; held {
		node := keyNode{kind: keySection, members: schemaMembers(t, at, properties)}
		if marked, isBool := declaration[settingMark].(bool); isBool && marked {
			node.kind = keyObject
		}
		return node
	}
	if _, held := declaration["patternProperties"]; held {
		return keyNode{kind: keyMap}
	}
	entry, isObject := declaration["items"].(map[string]any)
	if !isObject {
		return keyNode{kind: keyLeaf}
	}
	if properties, held := entry["properties"]; held {
		return keyNode{kind: keyList, members: schemaMembers(t, at, properties)}
	}
	if shapes, held := entry["oneOf"].([]any); held {
		return keyNode{kind: keyList, members: shapeMembers(t, at, shapes)}
	}
	return keyNode{kind: keyLeaf}
}

// shapeMembers builds the members the shapes of one list's entries declare between
// them. A member two shapes both declare is one member of the list, so the shapes
// must agree on what it is.
func shapeMembers(t *testing.T, at string, shapes []any) map[string]keyNode {
	t.Helper()

	members := map[string]keyNode{}
	for index, shape := range shapes {
		declared, isObject := shape.(map[string]any)
		if !isObject {
			t.Fatalf("config.schema.json: %s: shape %d is %T, want an object", at, index, shape)
		}
		for name, member := range schemaMembers(t, at, declared["properties"]) {
			if held, seen := members[name]; seen && !reflect.DeepEqual(held, member) {
				t.Fatalf("config.schema.json: %s: shapes declare %s as %+v and %+v, want one member",
					at, name, held, member)
			}
			members[name] = member
		}
	}
	return members
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
		{
			name: "the_project_path_pattern",
			got:  projectPathPattern.String(),
			want: func() string { return shapePattern(t, properties, "analysis", "configurations", "project") },
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

func TestDeclarationPatternsEqualTheContractAtEveryList(t *testing.T) {
	t.Parallel()

	found := map[string][]shapePatternAt{}
	collectShapePatterns(contractDocument(t, "config.schema.json"), "", found)
	tests := []struct {
		member string
		got    string
	}{
		{member: "symbol", got: typescriptReferencePattern.String()},
		{member: "module", got: bareSpecifierPattern.String()},
	}

	for _, tc := range tests {
		t.Run(tc.member, func(t *testing.T) {
			t.Parallel()

			declared := found[tc.member]
			if len(declared) == 0 {
				t.Fatalf("config.schema.json declares no shape with a %s pattern, so this test pins nothing", tc.member)
			}
			for _, declaredAt := range declared {
				if declaredAt.pattern != tc.got {
					t.Errorf("the %s pattern = %q, want the schema's own spelling %q at %s",
						tc.member, tc.got, declaredAt.pattern, declaredAt.at)
				}
			}
		})
	}
}

// shapePatternAt is one pattern a shape of a list entry declares for one member, with
// the place of the list.
type shapePatternAt struct {
	at      string
	pattern string
}

// collectShapePatterns records, for every list entry the schema declares in shapes,
// the pattern each shape declares for a member of the TypeScript declaration shapes,
// keyed by the member, so every list holding such entries is reached however deep it
// sits.
func collectShapePatterns(declaration map[string]any, at string, into map[string][]shapePatternAt) {
	if shapes, held := declaration["oneOf"].([]any); held {
		for _, shape := range shapes {
			declared, _ := shape.(map[string]any)
			properties, _ := declared["properties"].(map[string]any)
			for _, member := range []string{"symbol", "module"} {
				if pattern, isString := nestedString(properties, member, "pattern"); isString {
					into[member] = append(into[member], shapePatternAt{at: at, pattern: pattern})
				}
			}
		}
	}
	if items, held := declaration["items"].(map[string]any); held {
		collectShapePatterns(items, at+"[]", into)
	}
	properties, _ := declaration["properties"].(map[string]any)
	for name, member := range properties {
		if declared, isObject := member.(map[string]any); isObject {
			collectShapePatterns(declared, joinKey(at, name), into)
		}
	}
}

// nestedString returns one string member of one member of a properties object.
func nestedString(properties map[string]any, member, key string) (string, bool) {
	declared, _ := properties[member].(map[string]any)
	value, isString := declared[key].(string)
	return value, isString
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

// shapePattern returns the pattern one member of a list entry's shapes must match:
// the pattern of the one shape that declares the member.
func shapePattern(t *testing.T, properties map[string]any, section, list, member string) string {
	t.Helper()

	declared, isObject := properties[section].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s is not an object", section)
	}
	members, isObject := declared["properties"].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s declares no properties", section)
	}
	array, isObject := members[list].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s.%s is not an object", section, list)
	}
	items, isObject := array["items"].(map[string]any)
	if !isObject {
		t.Fatalf("config.schema.json: %s.%s declares no items", section, list)
	}
	shapes, isList := items["oneOf"].([]any)
	if !isList {
		t.Fatalf("config.schema.json: %s.%s items declare no shapes", section, list)
	}
	var patterns []string
	for _, shape := range shapes {
		declaredShape, isObject := shape.(map[string]any)
		if !isObject {
			t.Fatalf("config.schema.json: %s.%s declares a shape that is not an object", section, list)
		}
		shapeMembers, isObject := declaredShape["properties"].(map[string]any)
		if !isObject {
			continue
		}
		if one, held := shapeMembers[member].(map[string]any); held {
			if pattern, isString := one["pattern"].(string); isString {
				patterns = append(patterns, pattern)
			}
		}
	}
	if len(patterns) != 1 {
		t.Fatalf("config.schema.json: %s.%s declares %d patterns for %s, want one", section, list, len(patterns), member)
	}
	return patterns[0]
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
		"analysis.template_delimiters", "analysis.template_delimiters.left",
	} {
		if !slices.Contains(keys, want) {
			t.Errorf("declaredKeys() = %v, want it to contain %q", keys, want)
		}
	}
	for _, unwanted := range []string{
		"severity", "analysis.configurations[].id", "analysis.matrix",
		"analysis.template_delimiters.left", "analysis.template_delimiters.right",
	} {
		if slices.Contains(settingPaths(), unwanted) {
			t.Errorf("settingPaths() = %v, want it to omit %q, which holds no value of its own",
				settingPaths(), unwanted)
		}
	}
	for _, want := range []string{"analysis.configurations", "analysis.template_delimiters"} {
		if !slices.Contains(settingPaths(), want) {
			t.Errorf("settingPaths() = %v, want it to contain %q, which one source supplies whole",
				settingPaths(), want)
		}
	}
}

func TestDeclaresSetting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "a_setting_at_the_root", path: "contract_version", want: true},
		{name: "a_setting_inside_a_section", path: "analysis.min_confidence", want: true},
		{name: "a_setting_inside_a_nested_section", path: "analysis.matrix.complete", want: true},
		{name: "a_list_is_one_setting", path: "analysis.configurations", want: true},
		{name: "one_entry_of_a_list_is_not", path: "analysis.configurations[0].id", want: false},
		{name: "a_setting_written_as_an_object", path: "analysis.template_delimiters", want: true},
		{name: "one_member_of_that_object_is_not", path: "analysis.template_delimiters.left", want: false},
		{name: "a_section_is_not_a_setting", path: "analysis", want: false},
		{name: "a_section_declaring_no_member_is_not", path: "go", want: false},
		{name: "the_severity_object_is_not_a_setting", path: "severity", want: false},
		{name: "one_code_of_the_severity_object_is", path: "severity.DS1101", want: true},
		{name: "a_code_naming_no_live_kind_is_still_a_declared_setting", path: "severity.DS9999", want: true},
		{name: "a_path_below_a_severity_code_is_not", path: "severity.DS1101.warn", want: false},
		{name: "the_severity_object_with_an_empty_code_is_not", path: "severity.", want: false},
		{name: "a_near_miss_of_a_setting_is_not", path: "analysis.min_confidences", want: false},
		{name: "the_empty_path_is_not", path: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := DeclaresSetting(tc.path); got != tc.want {
				t.Errorf("DeclaresSetting(%q) = %t, want %t", tc.path, got, tc.want)
			}
		})
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
		{name: "the_family_prefix_holding_every_fixed_code", code: "DS17", want: []string{"DS1703", "DS1704", "DS1706"}},
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
