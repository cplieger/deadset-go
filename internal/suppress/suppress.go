// Package suppress reads the suppressions of one target: the inline directives
// its source carries, and the entries of the ignore file and the rows of the
// baseline at its root, which the shared core reads and binds. A suppression is
// one code bound to one site, carrying the reason a reader needs.
//
// Nothing here emits a finding and nothing here ends a run. A reader returns the
// records the suppressions resolve to, the refusals the grammar reports as
// findings of their own, and an error for a directive or a document the grammar
// refuses before any finding exists.
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

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// errMalformed reports a directive the grammar refuses before any finding
// exists. Every [malformedError] carries it.
var errMalformed = errors.New("suppress: malformed suppression")

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
	Code   vocab.Code // the code this record suppresses
	Symbol string     // the reference of the declaration bound, empty where none was
	Path   string     // the file the suppression names
	Reason string     // never empty: a suppression carrying none is a Refusal

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
	Reported vocab.Code // the code the finding carries
	Code     vocab.Code // the code the refused suppression names
	Symbol   string     // the reference it names, empty for a directive
	Path     string     // the file it names, empty where that is the defect
	Reason   string     // the reason it carries, empty where that is the defect

	Site      token.Position // where the suppression is written
	Mechanism Mechanism
}

// malformedError is one directive the grammar refuses before any finding is
// produced, because it is an instruction the analysis cannot carry out: read as
// prose it would do nothing, and if the finding it was meant to cover has
// already gone, nothing would say so.
type malformedError struct {
	Text      string         // the directive at fault, as written
	Want      string         // what the grammar fixes for it
	Site      token.Position // where the defect is written
	Mechanism Mechanism
}

// Error names the mechanism, the site, the text at fault and the form expected,
// so a caller that prints the error prints everything a maintainer needs to
// correct the suppression.
func (e *malformedError) Error() string {
	at := e.Site.Filename
	if e.Site.Line > 0 {
		at = fmt.Sprintf("%s:%d:%d", at, e.Site.Line, e.Site.Column)
	}
	return fmt.Sprintf("suppress: %s %s: %s: want %s", e.Mechanism, at, e.Text, e.Want)
}

// Unwrap returns the class every malformed directive carries.
func (*malformedError) Unwrap() error { return errMalformed }

// Bound is the declarations the records mark live, which seed the sweep. A record
// that bound nothing marks nothing, which is what makes it stale; a record for a
// part kind marks nothing either: it withholds its finding and keeps nothing alive.
func Bound(records []Record) []graph.SymbolID {
	var marked []graph.SymbolID
	for i := range records {
		if records[i].Bound != "" && records[i].Code.Subject() != vocab.SubjectPart {
			marked = append(marked, records[i].Bound)
		}
	}
	return marked
}
