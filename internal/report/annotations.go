package report

import (
	"io"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/catalog"
	"github.com/cplieger/deadset-go/v2/internal/config"
)

// The workflow commands an annotation is written as.
const (
	commandError   = "error"
	commandWarning = "warning"
	commandNotice  = "notice"
)

// The title and the message prefix of a type-error skip's annotation.
const (
	typeErrorSkipTitle   = "type error skipped"
	typeErrorSkipMessage = "the analysis did not evaluate the function or statement holding this type error: "
)

// Annotations writes the workflow annotations the contract's annotations grammar
// states: one per finding, at the command its severity alone names, then one error
// per stale suppression, one warning per type-error skip and one per note. A finding
// and a stale suppression carry their file, line, column, last line and code and
// kind as title; a skip its file and line; a note its directory. Every value is
// escaped as the workflow command grammar requires.
func Annotations(w io.Writer, e *Envelope, _ Options) error {
	out := &sink{w: w}
	for i := range e.Findings {
		found := &e.Findings[i]
		out.printf("::%s file=%s,line=%d,col=%d,endLine=%d,title=%s::%s\n",
			commandOf(found.Severity),
			escapeProperty(found.Position.Path), found.Position.Line, found.Position.Column,
			found.Position.EndLine,
			escapeProperty(found.Code+" "+found.Kind), escapeData(found.Message))
	}
	for i := range e.StaleSuppressions {
		stale := &e.StaleSuppressions[i]
		out.printf("::%s file=%s,line=%d,col=%d,endLine=%d,title=%s::%s\n",
			commandError,
			escapeProperty(stale.Position.Path), stale.Position.Line, stale.Position.Column,
			stale.Position.Line,
			escapeProperty(stale.Code+" "+kindName(stale.Code)), escapeData(stale.Message))
	}
	for _, skip := range e.TypeErrorSkips {
		out.printf("::%s file=%s,line=%d,title=%s::%s\n",
			commandWarning, escapeProperty(skip.Path), skip.Line, escapeProperty(typeErrorSkipTitle),
			escapeData(typeErrorSkipMessage+skip.Message))
	}
	for _, note := range e.Notes {
		out.printf("::%s file=%s,title=%s::%s\n",
			commandWarning, escapeProperty(note.Path), escapeProperty(note.Kind), escapeData(note.Message))
	}
	return out.err
}

// commandOf is the workflow command a finding of one severity is annotated with.
func commandOf(severity config.Severity) string {
	switch severity {
	case config.Deny:
		return commandError
	case config.Allow:
		return commandNotice
	default:
		return commandWarning
	}
}

// kindName is the name of the kind one code names, and the code itself where the
// vocabulary holds no live row for it, so a title never loses the code it was given.
func kindName(code string) string {
	if row, live := catalog.Kind(code); live {
		return row.Name
	}
	return code
}

// escapeData escapes a workflow command's message: the escape character itself and
// the two line breaks, which would otherwise end the command.
func escapeData(value string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(value)
}

// escapeProperty escapes a workflow command's property value: what a message
// escapes, and the two separators of the property list.
func escapeProperty(value string) string {
	return strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
		":", "%3A",
		",", "%2C",
	).Replace(value)
}
