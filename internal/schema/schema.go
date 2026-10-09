// Package schema checks a JSON document against a JSON Schema 2020-12 document
// written in the keywords the Contract's schemas use, and holds byte copies of the
// two schemas the report this analyzer writes is an instance of.
//
// A schema naming a keyword this package does not evaluate fails to compile, so a
// schema release that adopts one fails every check rather than passing values the
// keyword would refuse. An annotation is not evaluated: a title, a description, a
// default, an example, a comment and any keyword starting with x-.
package schema

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/big"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// contract holds the copies of the report and finding schemas, which a test pins
// byte for byte to the Contract version this analyzer implements.
//
//go:embed contract/report.schema.json contract/finding.schema.json
var contract embed.FS

// reportSchema is the path of the report schema inside [contract].
const reportSchema = "contract/report.schema.json"

// The keyword and the type names a check names more than once.
const (
	typeKeyword = "type"
	arrayType   = "array"
	objectType  = "object"
)

// ErrSchema reports a schema this package cannot compile.
var ErrSchema = errors.New("schema: the schema cannot be compiled")

// Report is the compiled report schema, compiled on first use.
var Report = sync.OnceValues(func() (*Schema, error) { return Compile(contract, reportSchema) })

// Schema is one compiled schema document.
type Schema struct {
	root *node
	name string
}

// Name is the path the schema was compiled from.
func (s *Schema) Name() string { return s.name }

// node is one compiled schema. A boolean schema is a node holding only never or
// nothing, and a $ref is a node holding the node it names.
//
//nolint:govet // fieldalignment: the fields are grouped by the keyword family that reads them
type node struct {
	never bool
	types []string

	enum     []string // canonical encodings
	constant *string
	pattern  *regexp.Regexp
	minLen   int
	minimum  *big.Rat

	required   []string
	properties map[string]*node
	patterns   []patternNode
	additional *node

	items    *node
	maxItems *int
	minItems int
	unique   bool

	allOf, anyOf, oneOf []*node
	not                 *node
	ifNode, thenNode    *node
	elseNode            *node
	ref                 *node
}

// patternNode is one member of patternProperties.
type patternNode struct {
	pattern *regexp.Regexp
	schema  *node
}

// annotations are the keywords a check reads nothing from.
var annotations = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "$defs": true,
	"title": true, "description": true, "default": true, "examples": true,
}

// compiler compiles the documents of one file system, each schema once by its
// location, so a $ref cycle resolves to the node being built.
type compiler struct {
	fsys  fs.FS
	docs  map[string]any
	built map[string]*node
}

// Compile compiles the schema document at name in fsys, reading every document a
// $ref names relative to it.
func Compile(fsys fs.FS, name string) (*Schema, error) {
	c := &compiler{fsys: fsys, docs: make(map[string]any), built: make(map[string]*node)}
	root, err := c.at(name, "")
	if err != nil {
		return nil, err
	}
	return &Schema{root: root, name: name}, nil
}

// at compiles the schema at one JSON Pointer of one document.
func (c *compiler) at(doc, pointer string) (*node, error) {
	key := doc + "#" + pointer
	if built, held := c.built[key]; held {
		return built, nil
	}
	raw, err := c.document(doc)
	if err != nil {
		return nil, err
	}
	value, err := resolvePointer(raw, pointer)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSchema, key, err)
	}
	n := &node{}
	c.built[key] = n
	if err := c.fill(n, doc, pointer, value); err != nil {
		return nil, err
	}
	return n, nil
}

// document reads and decodes one schema document once.
func (c *compiler) document(name string) (any, error) {
	if held, read := c.docs[name]; read {
		return held, nil
	}
	body, err := fs.ReadFile(c.fsys, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSchema, err)
	}
	value, err := decode(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSchema, name, err)
	}
	c.docs[name] = value
	return value, nil
}

// resolvePointer is the value one JSON Pointer names inside a document.
func resolvePointer(doc any, pointer string) (any, error) {
	if pointer == "" {
		return doc, nil
	}
	value := doc
	for token := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		var held bool
		switch container := value.(type) {
		case map[string]any:
			value, held = container[token]
		case []any:
			index, err := strconv.Atoi(token)
			held = err == nil && index >= 0 && index < len(container)
			if held {
				value = container[index]
			}
		}
		if !held {
			return nil, fmt.Errorf("%s names no value", pointer)
		}
	}
	return value, nil
}

// fill compiles one schema value into n.
func (c *compiler) fill(n *node, doc, pointer string, value any) error {
	switch v := value.(type) {
	case bool:
		n.never = !v
		return nil
	case map[string]any:
		for _, keyword := range slices.Sorted(maps.Keys(v)) {
			if annotations[keyword] || strings.HasPrefix(keyword, "x-") {
				continue
			}
			err := c.keyword(n, doc, pointer+"/"+escape(keyword), keyword, v[keyword])
			if err != nil && !errors.Is(err, ErrSchema) {
				err = fmt.Errorf("%w: %s#%s: %w", ErrSchema, doc, pointer, err)
			}
			if err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: %s#%s is neither an object nor a boolean", ErrSchema, doc, pointer)
	}
}

// keyword compiles one keyword of a schema object.
func (c *compiler) keyword(n *node, doc, at, keyword string, value any) (err error) {
	switch keyword {
	case typeKeyword:
		n.types, err = stringList(value)
	case "enum":
		n.enum, err = canonicalList(value)
	case "const":
		encoded := canonical(value)
		n.constant = &encoded
	case "pattern":
		n.pattern, err = compilePattern(value)
	case "minLength":
		n.minLen, err = count(value)
	case "minimum":
		n.minimum, err = number(value)
	case "minItems":
		n.minItems, err = count(value)
	case "maxItems":
		var most int
		most, err = count(value)
		n.maxItems = &most
	case "uniqueItems":
		n.unique, _ = value.(bool)
	case "required":
		n.required, err = stringList(value)
	default:
		return c.applicator(n, doc, at, keyword, value)
	}
	return err
}

// applicator compiles one keyword whose value holds schemas.
func (c *compiler) applicator(n *node, doc, at, keyword string, value any) (err error) {
	switch keyword {
	case "properties":
		n.properties, err = c.members(doc, at, value)
	case "patternProperties":
		n.patterns, err = c.patternMembers(doc, at, value)
	case "additionalProperties":
		n.additional, err = c.at(doc, at)
	case "items":
		n.items, err = c.at(doc, at)
	case "not":
		n.not, err = c.at(doc, at)
	case "if":
		n.ifNode, err = c.at(doc, at)
	case "then":
		n.thenNode, err = c.at(doc, at)
	case "else":
		n.elseNode, err = c.at(doc, at)
	case "allOf":
		n.allOf, err = c.list(doc, at, value)
	case "anyOf":
		n.anyOf, err = c.list(doc, at, value)
	case "oneOf":
		n.oneOf, err = c.list(doc, at, value)
	case "$ref":
		n.ref, err = c.reference(doc, value)
	default:
		return fmt.Errorf("the keyword %q is not one this package evaluates", keyword)
	}
	return err
}

// members compiles the schemas of properties.
func (c *compiler) members(doc, at string, value any) (map[string]*node, error) {
	object, isObject := value.(map[string]any)
	if !isObject {
		return nil, errors.New("properties is not an object")
	}
	held := make(map[string]*node, len(object))
	for name := range object {
		member, err := c.at(doc, at+"/"+escape(name))
		if err != nil {
			return nil, err
		}
		held[name] = member
	}
	return held, nil
}

// patternMembers compiles the schemas of patternProperties.
func (c *compiler) patternMembers(doc, at string, value any) ([]patternNode, error) {
	object, isObject := value.(map[string]any)
	if !isObject {
		return nil, errors.New("patternProperties is not an object")
	}
	held := make([]patternNode, 0, len(object))
	for _, spelled := range slices.Sorted(maps.Keys(object)) {
		pattern, err := regexp.Compile(spelled)
		if err != nil {
			return nil, err
		}
		member, err := c.at(doc, at+"/"+escape(spelled))
		if err != nil {
			return nil, err
		}
		held = append(held, patternNode{pattern: pattern, schema: member})
	}
	return held, nil
}

// list compiles the schemas of allOf, anyOf or oneOf.
func (c *compiler) list(doc, at string, value any) ([]*node, error) {
	items, isList := value.([]any)
	if !isList || len(items) == 0 {
		return nil, errors.New("a schema list is not a non-empty array")
	}
	held := make([]*node, len(items))
	for i := range items {
		member, err := c.at(doc, fmt.Sprintf("%s/%d", at, i))
		if err != nil {
			return nil, err
		}
		held[i] = member
	}
	return held, nil
}

// reference compiles the schema one $ref names: a document beside doc, a pointer
// inside doc, or a pointer inside a document beside it.
func (c *compiler) reference(doc string, value any) (*node, error) {
	spelled, isString := value.(string)
	if !isString {
		return nil, errors.New("$ref is not a string")
	}
	file, pointer, _ := strings.Cut(spelled, "#")
	target := doc
	if file != "" {
		target = path.Join(path.Dir(doc), file)
	}
	return c.at(target, pointer)
}

// compilePattern compiles one pattern. The Contract's patterns are written to
// compile and to match alike in RE2 and in ECMAScript.
func compilePattern(value any) (*regexp.Regexp, error) {
	spelled, isString := value.(string)
	if !isString {
		return nil, errors.New("pattern is not a string")
	}
	return regexp.Compile(spelled)
}

// stringList reads a string or an array of strings.
func stringList(value any) ([]string, error) {
	if one, isString := value.(string); isString {
		return []string{one}, nil
	}
	items, isList := value.([]any)
	if !isList {
		return nil, errors.New("a list of names is not an array")
	}
	held := make([]string, 0, len(items))
	for _, item := range items {
		one, isString := item.(string)
		if !isString {
			return nil, errors.New("a list of names holds a value that is not a string")
		}
		held = append(held, one)
	}
	return held, nil
}

// canonicalList is the canonical encoding of every value of an enum.
func canonicalList(value any) ([]string, error) {
	items, isList := value.([]any)
	if !isList {
		return nil, errors.New("enum is not an array")
	}
	held := make([]string, len(items))
	for i, item := range items {
		held[i] = canonical(item)
	}
	return held, nil
}

// count reads a non-negative integer.
func count(value any) (int, error) {
	n, err := number(value)
	if err != nil || !n.IsInt() || n.Sign() < 0 || !n.Num().IsInt64() {
		return 0, errors.New("a count is not a non-negative integer")
	}
	return int(n.Num().Int64()), nil
}

// number reads a JSON number exactly.
func number(value any) (*big.Rat, error) {
	spelled, isNumber := value.(json.Number)
	if !isNumber {
		return nil, errors.New("a bound is not a number")
	}
	n, parsed := new(big.Rat).SetString(spelled.String())
	if !parsed {
		return nil, fmt.Errorf("%q is not a number", spelled)
	}
	return n, nil
}

// escape spells one member name as a JSON Pointer token.
func escape(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}
