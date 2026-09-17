package load

import (
	"cmp"
	"fmt"
	"strings"
)

// Diagnostic is one error a load reported, at the position the toolchain named.
type Diagnostic struct {
	Package  string // the package the error was reported against, empty when none
	Position string // file:line:col as the toolchain rendered it, empty when none
	Message  string
}

// Error reports every diagnostic one configuration's load produced. A load that
// returns one produced no analyzable package set, so no finding list follows it.
type Error struct {
	Configuration string
	Diagnostics   []Diagnostic
}

// Error renders the configuration, the number of diagnostics, and every
// diagnostic on its own line, so a caller that prints the error prints the whole
// list.
func (e *Error) Error() string {
	var b strings.Builder
	noun := "errors"
	if len(e.Diagnostics) == 1 {
		noun = "error"
	}
	fmt.Fprintf(&b, "load %s: %d %s", e.Configuration, len(e.Diagnostics), noun)
	for _, d := range e.Diagnostics {
		b.WriteString("\n  ")
		if d.Position != "" {
			b.WriteString(d.Position)
			b.WriteString(": ")
		}
		b.WriteString(d.Message)
	}
	return b.String()
}

// compareDiagnostics is the total order the load reports diagnostics in, so a
// repeated run prints the same bytes. Position leads, because that is the order a
// reader walks the list in, and it puts every diagnostic of one source site
// together whichever package variant reported it.
func compareDiagnostics(a, b Diagnostic) int {
	return cmp.Or(
		cmp.Compare(a.Position, b.Position),
		cmp.Compare(a.Message, b.Message),
		cmp.Compare(a.Package, b.Package),
	)
}

// sameDiagnostic reports whether two adjacent diagnostics of the sorted list name
// one problem. A package and its test variant type-check the same files
// independently and report the same error twice, so a positioned diagnostic is
// keyed on its source site; one with no position is keyed on its package, which
// is all that distinguishes it.
func sameDiagnostic(a, b Diagnostic) bool {
	if a.Position != b.Position || a.Message != b.Message {
		return false
	}
	return a.Position != "" || a.Package == b.Package
}
