package report

import (
	"io"
	"strings"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
)

// The workflow commands an annotation is written as, one per severity. A finding at
// the failing severity is an error, so a pull request shows it as a failure; a
// finding the configuration lowered is a warning, and one it lowered further is a
// notice.
const (
	commandError   = "error"
	commandWarning = "warning"
	commandNotice  = "notice"
)

// Annotations writes one workflow annotation per finding and one per stale
// suppression.
//
// A stale suppression is an error whatever the rest of the severity map holds,
// because the kind is fixed on at the failing severity. Each annotation names the
// file, the line, the column and the last line of the subject, carries the code and
// the kind as its title, and carries the finding's message; a value is escaped the
// way the workflow command grammar requires, so a message or a path holding a
// separator does not truncate the annotation.
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
	return out.err
}

// commandOf is the workflow command one severity is annotated with.
func commandOf(severity config.Severity) string {
	switch severity {
	case config.Deny:
		return commandError
	case config.Warn:
		return commandWarning
	default:
		return commandNotice
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
