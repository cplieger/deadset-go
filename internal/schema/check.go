package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"unicode/utf8"
)

// Error is one value a schema refuses: the keyword that refuses it and the JSON
// Pointer (RFC 6901) of the value, the empty string for the whole document.
type Error struct {
	Keyword      string
	InstancePath string
	Reason       string
}

// Error renders the violation as one line.
func (v *Error) Error() string {
	at := v.InstancePath
	if at == "" {
		at = "the document"
	}
	return fmt.Sprintf("%s: %s: %s", at, v.Keyword, v.Reason)
}

// errDocument reports a document that is not one JSON value.
var errDocument = errors.New("schema: the document is not one JSON value")

// Check reads one JSON document and returns what the schema refuses in it. An
// object or array whose schema constrains only its members is checked member by
// member as it is read, so the document is never held whole, and the check stops
// at the first member holding a violation without reading the rest. The
// violations returned are that member's, in document order; none means the
// document is an instance.
func (s *validator) Check(r io.Reader) ([]Error, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	found, err := s.root.stream(dec, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDocument, err)
	}
	if len(found) > 0 {
		return found, nil
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data follows the value", errDocument)
	}
	return found, nil
}

// decode reads one JSON value with its numbers kept exact.
func decode(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// check appends what n refuses in v to found.
func (n *node) check(v any, at string, found *[]Error) {
	if n.ref != nil {
		n.ref.check(v, at, found)
	}
	if n.never {
		*found = append(*found, Error{Keyword: "false", InstancePath: at, Reason: "no value is allowed here"})
		return
	}
	if len(n.types) > 0 && !slices.ContainsFunc(n.types, func(t string) bool { return hasType(v, t) }) {
		*found = append(*found, Error{
			Keyword: typeKeyword, InstancePath: at,
			Reason: fmt.Sprintf("the value is %s, want %v", typeOf(v), n.types),
		})
		return
	}
	n.checkValue(v, at, found)
	switch value := v.(type) {
	case string:
		n.checkString(value, at, found)
	case json.Number:
		n.checkNumber(value, at, found)
	case map[string]any:
		n.checkObject(value, at, found)
	case []any:
		n.checkArray(value, at, found)
	}
	n.checkApplicators(v, at, found)
}

// checkValue applies enum and const.
func (n *node) checkValue(v any, at string, found *[]Error) {
	if n.enum == nil && n.constant == nil {
		return
	}
	encoded := canonical(v)
	if n.enum != nil && !slices.Contains(n.enum, encoded) {
		*found = append(*found, Error{
			Keyword: "enum", InstancePath: at,
			Reason: encoded + " is not one of the values the schema lists",
		})
	}
	if n.constant != nil && *n.constant != encoded {
		*found = append(*found, Error{
			Keyword: "const", InstancePath: at,
			Reason: fmt.Sprintf("%s is not %s", encoded, *n.constant),
		})
	}
}

// checkString applies pattern and minLength.
func (n *node) checkString(v, at string, found *[]Error) {
	if n.pattern != nil && !n.pattern.MatchString(v) {
		*found = append(*found, Error{
			Keyword: "pattern", InstancePath: at,
			Reason: fmt.Sprintf("%q does not match %s", v, n.pattern),
		})
	}
	if utf8.RuneCountInString(v) < n.minLen {
		*found = append(*found, Error{
			Keyword: "minLength", InstancePath: at,
			Reason: fmt.Sprintf("%q is shorter than %d characters", v, n.minLen),
		})
	}
}

// checkNumber applies minimum and maximum.
func (n *node) checkNumber(v json.Number, at string, found *[]Error) {
	if n.minimum == nil && n.maximum == nil {
		return
	}
	value, parsed := new(big.Rat).SetString(v.String())
	if !parsed {
		return
	}
	if n.minimum != nil && value.Cmp(n.minimum) < 0 {
		*found = append(*found, Error{
			Keyword: "minimum", InstancePath: at,
			Reason: fmt.Sprintf("%s is below %s", v, n.minimum.RatString()),
		})
	}
	if n.maximum != nil && value.Cmp(n.maximum) > 0 {
		*found = append(*found, Error{
			Keyword: "maximum", InstancePath: at,
			Reason: fmt.Sprintf("%s is above %s", v, n.maximum.RatString()),
		})
	}
}

// checkObject applies required and every member keyword.
func (n *node) checkObject(v map[string]any, at string, found *[]Error) {
	for _, name := range slices.Sorted(maps.Keys(v)) {
		n.checkMember(name, v[name], at, found)
	}
	for _, name := range n.required {
		if _, held := v[name]; !held {
			*found = append(*found, missing(name, at))
		}
	}
}

// checkMember applies properties, patternProperties and additionalProperties to one
// member of an object.
func (n *node) checkMember(name string, v any, at string, found *[]Error) {
	member := at + "/" + escape(name)
	declared, named := n.properties[name]
	if named {
		declared.check(v, member, found)
	}
	for _, p := range n.patterns {
		if p.pattern.MatchString(name) {
			named = true
			p.schema.check(v, member, found)
		}
	}
	if named || n.additional == nil {
		return
	}
	if n.additional.never {
		*found = append(*found, unknown(name, at))
		return
	}
	n.additional.check(v, member, found)
}

// missing is the violation of one required member that is absent.
func missing(name, at string) Error {
	return Error{Keyword: "required", InstancePath: at, Reason: "the member " + strconv.Quote(name) + " is absent"}
}

// unknown is the violation of one member the schema does not declare.
func unknown(name, at string) Error {
	return Error{
		Keyword: "additionalProperties", InstancePath: at,
		Reason: "the member " + strconv.Quote(name) + " is not one the schema declares",
	}
}

// checkArray applies items, minItems, maxItems and uniqueItems.
func (n *node) checkArray(v []any, at string, found *[]Error) {
	if n.items != nil {
		for i, item := range v {
			n.items.check(item, at+"/"+strconv.Itoa(i), found)
		}
	}
	n.checkCount(len(v), at, found)
	if !n.unique {
		return
	}
	seen := make(map[string]bool, len(v))
	for _, item := range v {
		encoded := canonical(item)
		if seen[encoded] {
			*found = append(*found, Error{
				Keyword: "uniqueItems", InstancePath: at,
				Reason: encoded + " is listed twice",
			})
			return
		}
		seen[encoded] = true
	}
}

// checkCount applies minItems and maxItems to an array of length n.
func (n *node) checkCount(length int, at string, found *[]Error) {
	if length < n.minItems {
		*found = append(*found, Error{
			Keyword: "minItems", InstancePath: at,
			Reason: fmt.Sprintf("the array holds %d items, fewer than %d", length, n.minItems),
		})
	}
	if n.maxItems != nil && length > *n.maxItems {
		*found = append(*found, Error{
			Keyword: "maxItems", InstancePath: at,
			Reason: fmt.Sprintf("the array holds %d items, more than %d", length, *n.maxItems),
		})
	}
}

// checkApplicators applies allOf, anyOf, oneOf, not and if, then and else. A failed
// anyOf, oneOf or not is one violation of its own keyword, because no single branch
// is the one the value should have matched.
func (n *node) checkApplicators(v any, at string, found *[]Error) {
	for _, each := range n.allOf {
		each.check(v, at, found)
	}
	if n.anyOf != nil && countMatching(n.anyOf, v, at) == 0 {
		*found = append(*found, Error{Keyword: "anyOf", InstancePath: at, Reason: "the value matches none of the alternatives"})
	}
	if n.oneOf != nil {
		if matched := countMatching(n.oneOf, v, at); matched != 1 {
			*found = append(*found, Error{
				Keyword: "oneOf", InstancePath: at,
				Reason: fmt.Sprintf("the value matches %d of the alternatives, want exactly one", matched),
			})
		}
	}
	if n.not != nil && n.not.matches(v, at) {
		*found = append(*found, Error{Keyword: "not", InstancePath: at, Reason: n.not.forbidden()})
	}
	if n.ifNode == nil {
		return
	}
	branch := n.elseNode
	if n.ifNode.matches(v, at) {
		branch = n.thenNode
	}
	if branch != nil {
		branch.check(v, at, found)
	}
}

// forbidden says what a value matching a not schema holds. A not over required alone
// names the members, which is the shape of a rule forbidding a member.
func (n *node) forbidden() string {
	if n.ref == nil && len(n.required) > 0 && len(n.types) == 0 && n.properties == nil && n.allOf == nil && n.anyOf == nil {
		return fmt.Sprintf("the value holds %v, which the schema forbids here", n.required)
	}
	return "the value matches a schema it must not match"
}

// matches reports whether v is an instance of n.
func (n *node) matches(v any, at string) bool {
	var found []Error
	n.check(v, at, &found)
	return len(found) == 0
}

// countMatching counts the schemas of a list v is an instance of.
func countMatching(list []*node, v any, at string) int {
	matched := 0
	for _, each := range list {
		if each.matches(v, at) {
			matched++
		}
	}
	return matched
}

// hasType reports whether v is of one JSON Schema type.
func hasType(v any, name string) bool {
	switch value := v.(type) {
	case nil:
		return name == "null"
	case bool:
		return name == "boolean"
	case string:
		return name == "string"
	case map[string]any:
		return name == objectType
	case []any:
		return name == arrayType
	case json.Number:
		if name == "number" {
			return true
		}
		n, parsed := new(big.Rat).SetString(value.String())
		return name == "integer" && parsed && n.IsInt()
	}
	return false
}

// typeOf names the JSON type of v for a message.
func typeOf(v any) string {
	for _, name := range []string{"null", "boolean", "string", objectType, arrayType, "integer", "number"} {
		if hasType(v, name) {
			return name
		}
	}
	return fmt.Sprintf("%T", v)
}

// canonical is one value's encoding with object members sorted, so two equal values
// encode alike.
func canonical(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(encoded)
}
