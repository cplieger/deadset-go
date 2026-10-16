package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/deadset-spec/v8/document"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// supplied is what one checked document supplies: the value of every setting
// and the members of every keyed section it writes, by key, as decoded with
// their numbers exact.
type supplied struct {
	values map[vocab.Key]any
	keyed  map[vocab.Key]map[string]any
}

// schemas is the compiled value schema of every key, of a key written per
// language, of the Go member of its object form, and of a keyed or ignored
// key, the pattern its member names match.
type schemas struct {
	values   map[vocab.Key]*document.Schema
	members  map[vocab.Key]*document.Schema
	patterns map[vocab.Key]*regexp.Regexp
}

var compiled = sync.OnceValues(func() (*schemas, error) {
	held := &schemas{values: map[vocab.Key]*document.Schema{}, members: map[vocab.Key]*document.Schema{}, patterns: map[vocab.Key]*regexp.Regexp{}}
	rows := vocab.ConfigKeys()
	for i := range rows {
		row := &rows[i]
		if row.KeyPattern != "" {
			pattern, err := regexp.Compile(row.KeyPattern)
			if err != nil {
				return nil, fmt.Errorf("the key pattern of %s: %w", row.Key, err)
			}
			held.patterns[row.Key] = pattern
		}
		if row.Value == "" {
			continue
		}
		schema, err := document.CompileBytes(string(row.Key), []byte(row.Value))
		if err != nil {
			return nil, err
		}
		held.values[row.Key] = schema
		if len(row.PerLanguage) == 0 {
			continue
		}
		member, err := memberSchema(row)
		if err != nil {
			return nil, err
		}
		held.members[row.Key] = member
	}
	return held, nil
})

// memberSchema compiles the schema of the Go member of a per-language key's
// object form: the oneOf alternative of type object, its property go.
func memberSchema(row *vocab.ConfigKey) (*document.Schema, error) {
	var value struct {
		OneOf []struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Type       string                     `json:"type"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal([]byte(row.Value), &value); err != nil {
		return nil, fmt.Errorf("the value schema of %s: %w", row.Key, err)
	}
	for _, alternative := range value.OneOf {
		if member, held := alternative.Properties[string(vocab.Go)]; held && alternative.Type == "object" {
			return document.CompileBytes(string(row.Key)+"."+string(vocab.Go), member)
		}
	}
	return nil, fmt.Errorf("the value schema of %s declares no object form naming %s", row.Key, vocab.Go)
}

// check refuses a configuration document, label naming its source, that is not
// one strict JSON object, that names a key the closed key list does not
// declare, or whose value of a key the Go analysis reads the key's schema
// refuses. The members of an object are checked in ascending order and a key
// is checked whole before the next.
func check(data []byte, label string) (*supplied, error) {
	held, err := compiled()
	if err != nil {
		return nil, err
	}
	value, err := document.Decode(data)
	if err != nil {
		return nil, malformed(label, "", err.Error())
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return nil, malformed(label, "", "want one JSON object")
	}
	s := &supplied{values: map[vocab.Key]any{}, keyed: map[vocab.Key]map[string]any{}}
	if err := s.section(held, label, "", object); err != nil {
		return nil, err
	}
	return s, nil
}

// section checks the members of the section at, the root for "".
func (s *supplied) section(held *schemas, label, at string, members map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(members)) {
		key := vocab.Key(joinKey(at, name))
		row, declared := vocab.ConfigKeyOf(key)
		switch {
		case strings.Contains(name, "."):
			reason := ""
			if declared {
				reason = "it is one setting, written as nested objects"
			}
			return unimplemented(label, string(key), reason)
		case !declared:
			return unimplemented(label, string(key), "")
		case !row.ReadIn(vocab.Go):
			continue
		}
		if err := s.key(held, label, &row, members[name]); err != nil {
			return err
		}
	}
	return nil
}

// key checks the value one document writes for a key the Go analysis reads.
func (s *supplied) key(held *schemas, label string, row *vocab.ConfigKey, value any) error {
	path := string(row.Key)
	switch row.Role {
	case vocab.RoleSection:
		members, isObject := value.(map[string]any)
		if !isObject {
			return malformed(label, path, "holds "+jsonType(value)+", want an object")
		}
		return s.section(held, label, path, members)
	case vocab.RoleKeyed, vocab.RoleIgnored:
		members, err := checkKeyed(held, label, row, value)
		if err == nil && row.Role == vocab.RoleKeyed {
			s.keyed[row.Key] = members
		}
		return err
	default:
		if len(row.PerLanguage) > 0 {
			value = narrowed(row, value)
		}
		if err := checkSetting(held, label, row, value); err != nil {
			return err
		}
		s.values[row.Key] = value
		return nil
	}
}

// narrowed is a per-language value as the Go analysis reads it: a string
// whole, and of the object form every member but another language's, which
// that language's analysis reads. A member naming no language is kept, so it
// is refused.
func narrowed(row *vocab.ConfigKey, value any) any {
	members, isObject := value.(map[string]any)
	if !isObject {
		return value
	}
	held := map[string]any{}
	for name, member := range members {
		if name == string(vocab.Go) || !slices.Contains(row.PerLanguage, vocab.Language(name)) {
			held[name] = member
		}
	}
	return held
}

// checkSetting refuses a setting's value its schema refuses, the Go member of
// a per-language object checked against the member's schema, and an integer
// written with a fraction or an exponent, which JSON Schema admits.
func checkSetting(held *schemas, label string, row *vocab.ConfigKey, value any) error {
	path := string(row.Key)
	if members, isObject := value.(map[string]any); isObject && len(row.PerLanguage) > 0 {
		for _, name := range slices.Sorted(maps.Keys(members)) {
			if name != string(vocab.Go) {
				return unimplemented(label, path+"."+name, "")
			}
		}
		member, set := members[string(vocab.Go)]
		if !set {
			return malformed(label, path+"."+string(vocab.Go), "is required and absent")
		}
		return refusal(label, path+"."+string(vocab.Go), row, member, held.members[row.Key].FirstRefusal(member))
	}
	if err := refusal(label, path, row, value, held.values[row.Key].FirstRefusal(value)); err != nil {
		return err
	}
	if number, isNumber := value.(json.Number); isNumber && !wholeNumber.MatchString(number.String()) {
		return malformed(label, path, "holds "+number.String()+", want an integer written as an optional minus sign and digits alone")
	}
	return nil
}

// wholeNumber is a JSON number written with no fraction and no exponent. A
// number that schema admits is an integer, so a match is one written as one.
var wholeNumber = regexp.MustCompile(`^-?\d+$`)

// checkKeyed refuses a keyed or ignored section that is not an object, a
// member name its key pattern or its vocabulary refuses, and a member value
// its schema refuses, the members in ascending order.
func checkKeyed(held *schemas, label string, row *vocab.ConfigKey, value any) (map[string]any, error) {
	path := string(row.Key)
	members, isObject := value.(map[string]any)
	if !isObject {
		return nil, malformed(label, path, "holds "+jsonType(value)+", want an object")
	}
	for _, name := range slices.Sorted(maps.Keys(members)) {
		at := path + "." + name
		switch {
		case !held.patterns[row.Key].MatchString(name):
			return nil, unimplemented(label, at, keyShape(row.Key))
		case len(row.Values) > 0 && !slices.Contains(row.Values, name):
			return nil, unimplemented(label, at, memberOutside(row, name))
		}
		if err := refusal(label, at, row, members[name], held.values[row.Key].FirstRefusal(members[name])); err != nil {
			return nil, err
		}
	}
	return members, nil
}

// memberOutside says why a member name the vocabulary of a keyed section does
// not hold is refused.
func memberOutside(row *vocab.ConfigKey, name string) string {
	if row.Key == vocab.KeySeverity {
		return severityKeyRefused(name)
	}
	return outside(row)
}

// outside names the vocabulary a key draws from and its values.
func outside(row *vocab.ConfigKey) string {
	return "the " + strings.ReplaceAll(row.Vocabulary, "-", " ") + " are " + strings.Join(row.Values, ", ")
}

// keyShape says what a member name of a keyed or ignored section is.
func keyShape(key vocab.Key) string {
	if key == vocab.KeySeverity {
		return "a severity key is one issue-kind code or one two-digit family prefix"
	}
	return "a member name of " + string(key) + " is a dotted key path"
}

// refusal is the error one schema refusal of value, the value of the key at, is,
// nil for none, naming the place inside the value it is about, and a value
// outside the vocabulary its setting draws from followed by that vocabulary.
func refusal(label, at string, row *vocab.ConfigKey, value any, refused *document.Error) error {
	if refused == nil {
		return nil
	}
	path := refused.Key(at, value)
	switch refused.Keyword {
	case "additionalProperties", "propertyNames":
		return unimplemented(label, path, "")
	case "required":
		return malformed(label, path, "is required and absent")
	case "enum":
		if row.Vocabulary != "" && row.Role == vocab.RoleSetting {
			return malformed(label, path, refused.Reason+"; "+outside(row))
		}
	}
	return malformed(label, path, refused.Reason)
}

// jsonType names the JSON type of a decoded value for a refusal.
func jsonType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "a Boolean"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case []any:
		return "an array"
	default:
		return "an object"
	}
}

// joinKey spells the dotted path of one member of the value at at.
func joinKey(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

// malformed refuses a document, or the value at path inside it.
func malformed(label, path, detail string) *Error {
	if path == "" {
		return &Error{Message: label + ": " + detail}
	}
	return &Error{Message: label + ": " + path + ": " + detail}
}

// unimplemented refuses a key the closed key list does not declare, with the
// reason where there is more to say than that.
func unimplemented(label, path, reason string) *Error {
	message := fmt.Sprintf("%s: key %q is not implemented", label, path)
	if reason != "" {
		message += ": " + reason
	}
	return &Error{Message: message}
}
