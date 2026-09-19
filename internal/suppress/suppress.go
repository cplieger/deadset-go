// Package suppress reads the two suppression mechanisms of one target: the
// inline directives its source carries, and the entries of the ignore file at
// its root. One grammar covers both, and a suppression is one code bound to one
// site, carrying the reason a reader needs.
//
// Nothing here emits a finding and nothing here ends a run. A reader returns the
// records the suppressions resolve to, the refusals the grammar reports as
// findings of their own, and a [MalformedError] for a directive or a document
// the grammar refuses before any finding exists.
//
// The findings pass reads four things from a record: the declaration it bound,
// which is marked live before the sweep so the suppressed symbol keeps every
// symbol it references alive; the code, which decides the finding withheld for
// that declaration; the mechanism and the entry, which a stale-suppression
// finding carries; and the reason, which the counted totals report. A refusal
// binds nothing: Reported is the code its finding carries, and its remaining
// fields are the suppression as written, a field empty exactly where the
// suppression lacks it.
package suppress

import (
	"errors"
	"fmt"
	"go/token"
	"strconv"

	"github.com/cplieger/deadset-go/internal/graph"
)

// The codes a refused suppression is reported under.
const (
	codeNoReason = "DS1701" // a suppression that carries no reason
	codeUnscoped = "DS1702" // an entry that names a symbol and no path
)

// ErrMalformed reports a suppression the grammar refuses before any finding
// exists. Every [MalformedError] carries it, so a caller maps the whole class to
// the usage exit code with errors.Is and reads the site with errors.As.
var ErrMalformed = errors.New("suppress: malformed suppression")

// Mechanism names where a suppression is written. The spelling is the one a
// finding carries.
type Mechanism uint8

// The three documents a run reads. Two of them are suppression mechanisms and
// there is no third; the baseline shares their record shape and adjudicates
// nothing, so it is a mechanism here and a ratchet on the finding total there.
const (
	MechanismInline   Mechanism = iota // a directive in the source
	MechanismIgnore                    // an entry of the ignore file
	MechanismBaseline                  // a row of the baseline
)

var mechanismNames = [...]string{
	MechanismInline:   "inline",
	MechanismIgnore:   "ignore",
	MechanismBaseline: "baseline",
}

// String returns the mechanism's spelling, and a numbered form for a value
// outside the set so a message never loses the number it was given.
func (m Mechanism) String() string {
	if int(m) >= len(mechanismNames) {
		return "Mechanism(" + strconv.Itoa(int(m)) + ")"
	}
	return mechanismNames[m]
}

// Record is one suppression: one code bound to one site, with the reason it
// carries. A directive naming two codes is two records with one reason, and a
// directive above a line that declares two symbols is one record per symbol.
type Record struct {
	Code   string // the code this record suppresses
	Symbol string // the reference of the declaration bound, empty where none was
	Path   string // the file the suppression names
	Reason string // never empty: a suppression carrying none is a Refusal

	// Bound is the declaration this record marks live. It is empty where the
	// suppression bound to nothing, which a directive on any line other than
	// the one above a declaration is, and an entry naming a symbol and a path no
	// declaration of the inventory holds.
	Bound     graph.SymbolID
	Site      token.Position // where the suppression is written
	Mechanism Mechanism
}

// Refusal is one suppression the grammar refuses with a finding of its own
// rather than with an exit: one carrying no reason, and an entry naming a symbol
// and no path. It binds nothing, and the finding it becomes reports the
// suppression as written.
type Refusal struct {
	Reported string // the code the finding carries
	Code     string // the code the refused suppression names
	Symbol   string // the reference it names, empty for a directive
	Path     string // the file it names, empty where that is the defect
	Reason   string // the reason it carries, empty where that is the defect

	Site      token.Position // where the suppression is written
	Mechanism Mechanism
}

// MalformedError is one directive, one entry or one document the grammar refuses
// before any finding is produced, because it is an instruction the analysis
// cannot carry out: read as prose the instruction would do nothing, and if the
// finding it was meant to cover has already gone, nothing would say so.
type MalformedError struct {
	// Err is the decoder's own error, and is nil where the grammar refused a
	// value the decoder accepted.
	Err       error
	Text      string         // the directive, the value or the member at fault, as written
	Want      string         // what the grammar fixes for it
	Site      token.Position // where the defect is written
	Mechanism Mechanism
}

// Error names the mechanism, the site, the text at fault and the form expected,
// so a caller that prints the error prints everything a maintainer needs to
// correct the suppression.
func (e *MalformedError) Error() string {
	at := e.Site.Filename
	if e.Site.Line > 0 {
		at = fmt.Sprintf("%s:%d:%d", at, e.Site.Line, e.Site.Column)
	}
	message := fmt.Sprintf("suppress: %s %s: %s", e.Mechanism, at, e.Text)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message + ": want " + e.Want
}

// Unwrap returns the class every malformed suppression carries and, where a
// decoder refused the document, the error it returned, so a caller maps the
// class to the usage code with errors.Is and still reaches the cause.
func (e *MalformedError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrMalformed}
	}
	return []error{ErrMalformed, e.Err}
}
