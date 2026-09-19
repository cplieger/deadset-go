package report

import (
	"bytes"
	"fmt"
	"io"
	"text/template"
)

// templateName is the name the parser gives the user's template, which is what an
// error names the template as.
const templateName = "report"

// Template renders the envelope through a template the user supplied.
//
// A template that names something the envelope does not carry fails and names it,
// rather than rendering the place it named as nothing: a report with a silently empty
// column is a report a reader draws the wrong conclusion from. For a field of the
// envelope the template engine's own rule does that; the option covers an index of a
// map, which no member of the envelope is today. The rendering is built whole before
// anything is written, so a template that fails writes no partial report.
func Template(w io.Writer, e *Envelope, opts Options) error {
	if opts.Template == "" {
		return fmt.Errorf("%w: a template rendering needs a template", ErrOptions)
	}
	parsed, err := template.New(templateName).Option("missingkey=error").Parse(opts.Template)
	if err != nil {
		return fmt.Errorf("%w: parse the template: %w", ErrOptions, err)
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, e); err != nil {
		return fmt.Errorf("%w: render the template: %w", ErrOptions, err)
	}
	if _, err := w.Write(rendered.Bytes()); err != nil {
		return fmt.Errorf("report: write the template rendering: %w", err)
	}
	return nil
}
