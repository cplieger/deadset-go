package graph

import (
	"slices"
	"strings"
)

// linknameDirectives are the two spellings of the directive that joins a
// declaration to a name outside its own package.
var linknameDirectives = [...]string{"//go:linkname", "//go:linknamestd"}

// LinknameDirective reads one comment as a linkname directive, and returns the
// name of the declaration it binds and the qualified name it binds that
// declaration to.
//
// The directive is a comment whose first word is one of the two spellings,
// followed by the name of a function or a variable of the comment's own package,
// and optionally by a qualified name: an import path and a name, separated by the
// last full stop of the word. A comment carrying any other number of words is not
// the directive, prose naming the directive included. With no qualified name the
// directive marks the declaration as one another package may reach by its object
// symbol name, and remote is empty. The directive binds only in a file importing
// unsafe, which is read from the file rather than from the comment.
func LinknameDirective(text string) (local, remote string, ok bool) {
	fields := strings.Fields(text)
	if len(fields) < 2 || len(fields) > 3 || !slices.Contains(linknameDirectives[:], fields[0]) {
		return "", "", false
	}
	if len(fields) == 3 {
		remote = fields[2]
	}
	return fields[1], remote, true
}
