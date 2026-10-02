// Package jsondoc encodes the JSON documents this analyzer writes: the report, its
// renderings, the resolved configuration and the describe document, each one value
// with a closing newline.
//
// A string escapes what strict JSON requires and the line and paragraph separators,
// and writes every other character as itself, `<`, `>` and `&` included: a document
// is read by a program and by a maintainer, never embedded in a page, and a symbol
// reference, a pattern or a template delimiter is compared as the bytes it spells.
package jsondoc

import (
	"bytes"
	"encoding/json"
)

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
