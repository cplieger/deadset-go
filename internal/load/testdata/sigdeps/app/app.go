// Package sigdeps imports a dependency checked for its declarations alone, one
// checked whole because it imports this module, and the standard library.
package sigdeps

import (
	"strings"

	"example.com/sigdep/back"
	"example.com/sigdep/plain"
)

// Area reads every dependency.
func Area() int { return plain.Width*plain.Height + back.Twice() + len(strings.ToUpper("a")) }
