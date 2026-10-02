package report

import (
	"fmt"
	"io"

	"github.com/cplieger/deadset-go/internal/jsondoc"
)

// jsonIndent is the indentation of the report document: two spaces, so the bytes of
// a report are fixed by the schema's member order and this one choice.
const jsonIndent = "  "

// JSON writes the report document: the envelope, indented, with a closing newline.
//
// The document is the Contract's, and the only shape an envelope marshals to, so
// this reporter decides nothing beyond the indentation. Reading the bytes back into
// an envelope refuses a member the schema does not declare and yields the same
// bytes again.
func JSON(w io.Writer, e *Envelope, _ Options) error {
	if err := writeDocument(w, e); err != nil {
		return fmt.Errorf("report: write the report document: %w", err)
	}
	return nil
}

// writeDocument writes one document as indented JSON with a closing newline.
func writeDocument(w io.Writer, v any) error {
	encoded, err := encoded(v, jsonIndent)
	if err != nil {
		return err
	}
	_, err = w.Write(encoded)
	return err
}

// encoded is one value as JSON with a closing newline, indented by indent or compact
// where indent is empty, written the way every document of this analyzer is.
func encoded(v any, indent string) ([]byte, error) {
	return jsondoc.Encode(v, indent)
}
