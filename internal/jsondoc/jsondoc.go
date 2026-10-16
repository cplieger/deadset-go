// Package jsondoc reads and writes the one-value JSON documents the analyzer
// handles: it reads a document within a size bound, decodes one the Contract
// declares a schema for, and encodes the resolved configuration and the
// describe document, each one value with a closing newline.
//
// A string escapes what strict JSON requires and the line and paragraph separators,
// and writes every other character as itself, `<`, `>` and `&` included: a document
// is read by a program and by a maintainer, never embedded in a page, and a symbol
// reference, a pattern or a template delimiter is compared as the bytes it spells.
package jsondoc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/deadset-spec/v8/document"
)

// Read returns the bytes of the document at path, refusing one larger than limit
// bytes, wrapping [atomicfile.ErrFileTooLarge], without reading it whole. An
// absent document is an error satisfying errors.Is(err, fs.ErrNotExist).
func Read(ctx context.Context, path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	body, err := atomicfile.ReadBoundedFile(ctx, file, limit)
	if errors.Is(err, atomicfile.ErrFileTooLarge) {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", atomicfile.ErrFileTooLarge, path, limit)
	}
	return body, err
}

// Unmarshal decodes body, the document name names, into v as one strict
// instance of schema. A refusal reads "<name>: <place>: <reason>", the place
// spelled as a configuration key and "the document" for the whole of it; a
// document that is not strict JSON is refused wrapping [document.ErrMalformed].
func Unmarshal(schema *document.Schema, name string, body []byte, v any) error {
	err := schema.Unmarshal(body, v)
	if err == nil {
		return nil
	}
	refused, isRefusal := errors.AsType[*document.Error](err)
	if !isRefusal {
		return fmt.Errorf("%s: %w", name, err)
	}
	// schema.Unmarshal decoded these bytes already, so this decode cannot fail.
	value, _ := document.Decode(body)
	place := refused.Key("", value)
	if place == "" {
		place = "the document"
	}
	return fmt.Errorf("%s: %s: %s", name, place, refused.Reason)
}

// Encode is one value as JSON with a closing newline, indented by indent or compact
// where indent is empty.
func Encode(v any, indent string) ([]byte, error) {
	var written bytes.Buffer
	encoder := json.NewEncoder(&written)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", indent)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return written.Bytes(), nil
}
