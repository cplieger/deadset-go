package scope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// shape is one node of the scope document's closed key list. An object names its
// members, an array its element, and a leaf holds a scalar. A value of another
// type than its shape declares is left to the decoder, which names the type.
type shape struct {
	members map[string]*shape
	element *shape
}

// documentShape returns the closed key list contract/scope.schema.json declares.
func documentShape() *shape {
	module := func() *shape {
		return &shape{members: map[string]*shape{"id": {}, "role": {}, "path": {}}}
	}
	return &shape{members: map[string]*shape{
		"target":    module(),
		"workspace": {},
		"consumers": {element: module()},
	}}
}

// checkMembers walks one document before it is decoded and refuses what
// encoding/json admits and the closed key list does not: a key matched to a
// declared one only by case, a key written twice in one object, and a null value,
// which the decoder reads as the member's absence.
func checkMembers(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	return walk(dec, documentShape(), "")
}

// walk reads the one value at the decoder's position against its shape, at
// naming where it sits. A nil shape accepts any value, because a value of the
// wrong type is the decoder's to refuse.
func walk(dec *json.Decoder, s *shape, at string) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	switch token {
	case nil:
		return fmt.Errorf("%w: %s is null; a document that leaves a member to its default omits it", ErrMember, at)
	case json.Delim('{'):
		if s != nil && s.members == nil {
			s = nil
		}
		return walkObject(dec, s, at)
	case json.Delim('['):
		var element *shape
		if s != nil {
			element = s.element
		}
		for index := 0; dec.More(); index++ {
			if err := walk(dec, element, at+"["+strconv.Itoa(index)+"]"); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	default:
		return nil
	}
}

// walkObject reads the members of the object the decoder has just opened.
func walkObject(dec *json.Decoder, s *shape, at string) error {
	seen := make(map[string]bool)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := token.(string)
		path := name
		if at != "" {
			path = at + "." + name
		}
		if seen[name] {
			return fmt.Errorf("%w: %s is written twice, so neither value is chosen", ErrMember, path)
		}
		seen[name] = true
		var member *shape
		if s != nil {
			declared, ok := s.members[name]
			if !ok {
				return fmt.Errorf("%w: %s is not a key the scope document declares", ErrMember, path)
			}
			member = declared
		}
		if err := walk(dec, member, path); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	return err
}
