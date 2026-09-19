package report

import (
	"fmt"
	"io"
	"strconv"
)

// The confidence a stale suppression is rendered with. The kind is fixed on at the
// failing severity and no configuration lowers it, so the record claims what the
// analysis knows.
const staleSuppressionConfidence = "certain"

// The kind token a stale suppression is rendered with, which is fixed rather than
// read from a subject: the record is about a suppression, whatever the subject the
// suppression named.
const staleSuppressionKind = "suppression"

// Text writes one line per finding and one per stale suppression, then one summary
// line.
//
// A finding line is the position, the kind, the name, the message, the confidence
// and the code, in the one shape every analyzer writes, so one expression finds
// every finding line of every analyzer's output. The summary is the report's totals
// in one line and is deliberately not in that shape, so a filter for finding lines
// yields exactly the finding lines. Nothing else is written: no timestamp, no
// duration and no host detail, so two runs over an unchanged tree write the same
// bytes.
func Text(w io.Writer, e *Envelope, _ Options) error {
	out := &sink{w: w}
	for i := range e.Findings {
		found := &e.Findings[i]
		out.printf("%s:%d:%d: %s %s: %s [%s] (%s)\n",
			found.Position.Path, found.Position.Line, found.Position.Column,
			found.Symbol.Kind, found.Symbol.Name, found.Message, found.Confidence, found.Code)
	}
	for i := range e.StaleSuppressions {
		stale := &e.StaleSuppressions[i]
		out.printf("%s:%d:%d: %s %s: %s [%s] (%s)\n",
			stale.Position.Path, stale.Position.Line, stale.Position.Column,
			staleSuppressionKind, stale.Symbol, stale.Message,
			staleSuppressionConfidence, stale.Code)
	}
	out.printf("%s\n", summary(&e.Totals))
	return out.err
}

// summary is the report's totals in one line: every count the totals hold, in the
// order the report declares them.
func summary(totals *Totals) string {
	return fmt.Sprintf("summary: %s (%d allow, %d warn, %d deny), %s, %s, %s, %s, %d pending, %d omitted",
		plural(totals.Findings, "finding", "findings"),
		totals.BySeverity.Allow, totals.BySeverity.Warn, totals.BySeverity.Deny,
		plural(totals.DeletableLines, "deletable line", "deletable lines"),
		plural(totals.SuppressionsInEffect, "suppression in effect", "suppressions in effect"),
		plural(totals.ReasonsRecorded, "reason recorded", "reasons recorded"),
		plural(totals.StaleSuppressions, "stale suppression", "stale suppressions"),
		totals.Pending, totals.Omitted)
}

// plural renders a count with the word for it, so a summary reads as a sentence.
func plural(n int, one, many string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + one
	}
	return strconv.Itoa(n) + " " + many
}

// sink is a writer that remembers the first error and writes nothing after it, so a
// rendering of many lines reports the failure once rather than checking every line.
type sink struct {
	w   io.Writer
	err error
}

// printf writes one formatted line unless a previous write failed.
func (s *sink) printf(format string, a ...any) {
	if s.err != nil {
		return
	}
	_, s.err = fmt.Fprintf(s.w, format, a...)
}
