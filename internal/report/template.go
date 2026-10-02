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

// ParseTemplate parses the text of a template the user supplied, which is what
// [Options] carries to [Template]. A template the parser refuses is an error of the
// invocation that named it, so a caller parses before it analyzes anything.
func ParseTemplate(text string) (*template.Template, error) {
	parsed, err := template.New(templateName).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("report: parse the template: %w", err)
	}
	return parsed, nil
}

// Template renders the envelope through a template the user supplied.
//
// A template that names something the envelope does not carry fails and names it,
// rather than rendering the place it named as nothing: a report with a silently empty
// column is a report a reader draws the wrong conclusion from. For a field of the
// envelope the template engine's own rule does that; the option covers an index of a
// map, which no member of the envelope is today. The rendering is built whole before
// anything is written, so a template that fails writes no partial report.
func Template(w io.Writer, e *Envelope, opts Options) error {
	if opts.Template == nil {
		return fmt.Errorf("%w: a template rendering needs a template", ErrOptions)
	}
	var rendered bytes.Buffer
	if err := opts.Template.Execute(&rendered, e); err != nil {
		return fmt.Errorf("%w: render the template: %w", ErrOptions, err)
	}
	if _, err := w.Write(rendered.Bytes()); err != nil {
		return fmt.Errorf("report: write the template rendering: %w", err)
	}
	return nil
}
