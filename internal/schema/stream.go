package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// stream checks the next value of dec against n, member by member where n allows it.
func (n *node) stream(dec *json.Decoder, at string) ([]Error, error) {
	switch {
	case n.membersOnly(objectType):
		return n.streamObject(dec, at)
	case n.membersOnly(arrayType):
		return n.streamArray(dec, at)
	}
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var found []Error
	n.check(v, at, &found)
	return found, nil
}

// membersOnly reports whether n is a schema of one type whose every keyword reads
// one member or the member count, so its value can be checked as it is read.
func (n *node) membersOnly(kind string) bool {
	if !slices.Equal(n.types, []string{kind}) || n.never || n.ref != nil || n.enum != nil || n.constant != nil ||
		n.allOf != nil || n.anyOf != nil || n.oneOf != nil || n.not != nil || n.ifNode != nil {
		return false
	}
	if kind == arrayType {
		return !n.unique
	}
	return true
}

// open reads the delimiter that starts a container, and reports a value of another
// type as the type violation.
func open(dec *json.Decoder, want json.Delim, kind, at string) ([]Error, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if token == want {
		return nil, nil
	}
	if _, isDelim := token.(json.Delim); isDelim {
		if err := skip(dec); err != nil {
			return nil, err
		}
	}
	return []Error{{Keyword: typeKeyword, InstancePath: at, Reason: "the value is not an " + kind}}, nil
}

// skip reads the rest of a container whose opening delimiter was read.
func skip(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}

// streamObject checks an object one member at a time.
func (n *node) streamObject(dec *json.Decoder, at string) ([]Error, error) {
	if found, err := open(dec, json.Delim('{'), objectType, at); err != nil || found != nil {
		return found, err
	}
	seen := make(map[string]bool, len(n.properties))
	for dec.More() {
		found, err := n.streamNext(dec, at, seen)
		if err != nil || len(found) > 0 {
			return found, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var found []Error
	for _, name := range n.required {
		if !seen[name] {
			found = append(found, missing(name, at))
		}
	}
	return found, nil
}

// streamNext reads the name of the next member of a streamed object and checks its
// value, refusing a name the object already holds.
func (n *node) streamNext(dec *json.Decoder, at string, seen map[string]bool) ([]Error, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	name, isName := token.(string)
	if !isName {
		return nil, errors.New("an object member has no name")
	}
	if seen[name] {
		return nil, fmt.Errorf("the member %q is named twice", name)
	}
	seen[name] = true
	return n.streamMember(dec, name, at)
}

// streamMember checks the value of one member of a streamed object.
func (n *node) streamMember(dec *json.Decoder, name, at string) ([]Error, error) {
	member := at + "/" + escape(name)
	if declared, named := n.properties[name]; named && len(n.patterns) == 0 {
		return declared.stream(dec, member)
	}
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var found []Error
	n.checkMember(name, v, at, &found)
	return found, nil
}

// streamArray checks an array one item at a time.
func (n *node) streamArray(dec *json.Decoder, at string) ([]Error, error) {
	if found, err := open(dec, json.Delim('['), arrayType, at); err != nil || found != nil {
		return found, err
	}
	items := n.items
	if items == nil {
		items = &node{}
	}
	length := 0
	for dec.More() {
		found, err := items.stream(dec, at+"/"+strconv.Itoa(length))
		if err != nil || len(found) > 0 {
			return found, err
		}
		length++
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var found []Error
	n.checkCount(length, at, &found)
	return found, nil
}
