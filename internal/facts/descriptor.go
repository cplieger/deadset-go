package facts

import (
	"errors"
	"fmt"
)

// errDescriptor is a serialized file descriptor this reader cannot decode.
var errDescriptor = errors.New("not a serialized file descriptor")

// fileDescriptor is the part of a google.protobuf.FileDescriptorProto a binding
// reads: https://github.com/protocolbuffers/protobuf/blob/main/src/google/protobuf/descriptor.proto
type fileDescriptor struct {
	name, pkg string
	messages  []messageDescriptor
	enums     []enumDescriptor
}

type messageDescriptor struct {
	name   string
	fields []fieldDescriptor
	nested []messageDescriptor
	enums  []enumDescriptor
}

type fieldDescriptor struct {
	name   string
	number int
}

type enumDescriptor struct {
	name   string
	values []fieldDescriptor
}

// wire is one message of the protobuf binary encoding, read field by field:
// https://protobuf.dev/programming-guides/encoding/
type wire struct {
	err error
	b   []byte
}

// next reads the next field: its number, and its bytes where it is length-delimited
// or its value where it is a varint. A fixed-width field is skipped.
func (w *wire) next() (number int, value uint64, bytes []byte, ok bool) {
	for len(w.b) > 0 && w.err == nil {
		key := w.varint()
		number, kind := int(key>>3), key&7
		switch kind {
		case 0:
			return number, w.varint(), nil, w.err == nil
		case 2:
			n := w.varint()
			if w.err != nil || n > uint64(len(w.b)) {
				w.fail()
				return 0, 0, nil, false
			}
			bytes, w.b = w.b[:n], w.b[n:]
			return number, 0, bytes, true
		case 1:
			w.skip(8)
		case 5:
			w.skip(4)
		default:
			w.fail()
		}
	}
	return 0, 0, nil, false
}

func (w *wire) varint() uint64 {
	var v uint64
	for shift := 0; shift < 64; shift += 7 {
		if len(w.b) == 0 {
			break
		}
		c := w.b[0]
		w.b = w.b[1:]
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v
		}
	}
	w.fail()
	return 0
}

func (w *wire) skip(n int) {
	if n > len(w.b) {
		w.fail()
		return
	}
	w.b = w.b[n:]
}

func (w *wire) fail() { w.err, w.b = errDescriptor, nil }

// decodeFile reads one serialized FileDescriptorProto.
func decodeFile(b []byte) (fileDescriptor, error) {
	var d fileDescriptor
	w := &wire{b: b}
	for {
		number, _, bytes, ok := w.next()
		if !ok {
			break
		}
		switch number {
		case 1:
			d.name = string(bytes)
		case 2:
			d.pkg = string(bytes)
		case 4:
			d.messages = append(d.messages, decodeMessage(bytes, w))
		case 5:
			d.enums = append(d.enums, decodeEnum(bytes, w))
		}
	}
	if w.err != nil || d.name == "" {
		return fileDescriptor{}, fmt.Errorf("%w: it names no schema file", errDescriptor)
	}
	return d, nil
}

func decodeMessage(b []byte, parent *wire) messageDescriptor {
	var m messageDescriptor
	w := &wire{b: b}
	for {
		number, _, bytes, ok := w.next()
		if !ok {
			break
		}
		switch number {
		case 1:
			m.name = string(bytes)
		case 2:
			m.fields = append(m.fields, decodeNumbered(bytes, 3, w))
		case 3:
			m.nested = append(m.nested, decodeMessage(bytes, w))
		case 4:
			m.enums = append(m.enums, decodeEnum(bytes, w))
		}
	}
	keep(parent, w)
	return m
}

func decodeEnum(b []byte, parent *wire) enumDescriptor {
	var e enumDescriptor
	w := &wire{b: b}
	for {
		number, _, bytes, ok := w.next()
		if !ok {
			break
		}
		switch number {
		case 1:
			e.name = string(bytes)
		case 2:
			e.values = append(e.values, decodeNumbered(bytes, 2, w))
		}
	}
	keep(parent, w)
	return e
}

// decodeNumbered reads a field or an enum value: its name, field 1, and its
// number, the varint field at.
func decodeNumbered(b []byte, at int, parent *wire) fieldDescriptor {
	var f fieldDescriptor
	w := &wire{b: b}
	for {
		number, value, bytes, ok := w.next()
		if !ok {
			break
		}
		switch number {
		case 1:
			f.name = string(bytes)
		case at:
			f.number = int(int32(value)) //nolint:gosec // G115: an int32 field's varint is its value sign-extended to 64 bits (protobuf.dev/programming-guides/encoding/#signed-ints)
		}
	}
	keep(parent, w)
	return f
}

// keep carries a nested message's decoding failure to the message holding it.
func keep(parent, w *wire) {
	if w.err != nil {
		parent.fail()
	}
}
