package config

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/jsondoc"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Print writes the resolved configuration, every key the Go analysis reads in
// the closed key list's order and the members of a keyed section in ascending
// order, then the provenance object naming where each setting came from, as
// one indented JSON object ending in a newline. Read back as a repository
// configuration it resolves to the same configuration: resolution reads past
// the provenance object.
func (r *Resolved) Print(w io.Writer) error {
	rows := vocab.ConfigKeys()
	tree := make(object, 0, len(rows))
	sections := map[vocab.Key]*object{"": &tree}
	for i := range rows {
		row := &rows[i]
		if !row.ReadIn(vocab.Go) || row.Role == vocab.RoleIgnored {
			continue
		}
		at, name := splitKey(row.Key)
		parent := sections[at]
		switch row.Role {
		case vocab.RoleSection:
			child := &object{}
			sections[row.Key] = child
			*parent = append(*parent, member{name: name, value: child})
		case vocab.RoleKeyed:
			members := make(object, 0, len(r.keyed[row.Key]))
			for _, code := range slices.Sorted(maps.Keys(r.keyed[row.Key])) {
				members = append(members, member{name: code, value: r.keyed[row.Key][code]})
			}
			*parent = append(*parent, member{name: name, value: members})
		default:
			*parent = append(*parent, member{name: name, value: r.values[row.Key]})
		}
	}
	provenance := make(object, 0, len(r.Provenance))
	for _, path := range slices.Sorted(maps.Keys(r.Provenance)) {
		provenance = append(provenance, member{name: path, value: r.Provenance[path].String()})
	}
	tree = append(tree, member{name: string(vocab.KeyProvenance), value: provenance})

	encoded, err := jsondoc.Encode(tree, "  ")
	if err != nil {
		return fmt.Errorf("render the resolved configuration: %w", err)
	}
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("write the resolved configuration: %w", err)
	}
	return nil
}

// splitKey is the key a key sits in, "" for the root, and its own name.
func splitKey(key vocab.Key) (at vocab.Key, name string) {
	cut := strings.LastIndexByte(string(key), '.')
	if cut < 0 {
		return "", string(key)
	}
	return key[:cut], string(key[cut+1:])
}

// object is a JSON object whose members are written in their order.
type object []member

// member is one member of an object: a name and a value encodable as JSON.
type member struct {
	value any
	name  string
}

// MarshalJSON writes the members in their order.
func (o object) MarshalJSON() ([]byte, error) {
	encoded := []byte{'{'}
	for i, m := range o {
		if i > 0 {
			encoded = append(encoded, ',')
		}
		name, err := encodeValue(m.name)
		if err != nil {
			return nil, err
		}
		value, err := encodeValue(m.value)
		if err != nil {
			return nil, err
		}
		encoded = append(append(append(encoded, name...), ':'), value...)
	}
	return append(encoded, '}'), nil
}

var _ json.Marshaler = object(nil)
