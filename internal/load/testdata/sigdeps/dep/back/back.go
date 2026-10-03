// Package back imports a package of the module that depends on it.
package back

import "example.com/sigdeps/core"

// Twice doubles what core counts.
func Twice() int { return 2 * core.Count() }
