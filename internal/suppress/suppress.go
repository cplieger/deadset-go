// Package suppress reads the suppressions of one target into the shared core's
// records and refusals: the inline directives its source carries, placed on the
// declarations they sit beside, and the entries of the ignore file and the rows
// of the baseline, which the core reads and this package binds to the inventory.
// What a record then does is the core's to decide.
package suppress

import "errors"

// OwnScheme opens every reference of this analyzer's language. A record naming
// another scheme belongs to the analyzer of that language, which reads the same
// document.
const OwnScheme = "go://"

// ErrUnreadable reports a suppression document present at its path that cannot
// be read. Which failure class that is depends on where the caller reads it from.
var ErrUnreadable = errors.New("cannot be read")
