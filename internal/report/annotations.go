package report

import (
	"io"
	"strings"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
)

// The workflow commands an annotation is written as. A finding at or above the
// failing severity is an error, so a pull request shows it as a failure, and a
// finding below it is a warning.
const (
	commandError   = "error"
	commandWarning = "warning"
)

// The title and the message prefix of a type-error skip's annotation.
const (
	typeErrorSkipTitle   = "type error skipped"
	typeErrorSkipMessage = "the analysis did not evaluate the function or statement holding this type error: "
)

// Annotations writes one workflow annotation per finding, one per stale
// suppression and one warning per type-error skip.
//
// A stale suppression is an error whatever the rest of the severity map holds,
// because the kind is fixed on at the failing severity. Each annotation names the
// file, the line, the column and the last line of the subject, carries the code and
// the kind as its title, and carries the finding's message; a value is escaped the
// way the workflow command grammar requires, so a message or a path holding a
// separator does not truncate the annotation.
func Annotations(w io.Writer, e *Envelope, opts Options) error {
	failing := normalizedFailOn(opts.FailOn)
	out := &sink{w: w}
	for i := range e.Findings {
		found := &e.Findings[i]
		out.printf("::%s file=%s,line=%d,col=%d,endLine=%d,title=%s::%s\n",
			commandOf(found.Severity, failing),
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
	return out.err
}

// commandOf is the workflow command one severity is annotated with under the
// failing severity.
func commandOf(severity, failing config.Severity) string {
	if fails(severity, failing) {
		return commandError
	}
	return commandWarning
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
