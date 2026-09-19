package report

import (
	"encoding/json"
	"fmt"
	"io"
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
	encoded, err := json.MarshalIndent(e, "", jsonIndent)
	if err != nil {
		return fmt.Errorf("report: write the report document: %w", err)
	}
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("report: write the report document: %w", err)
	}
	return nil
}
