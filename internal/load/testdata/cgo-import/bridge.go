package cgoimport

/*
#include <stdlib.h>
*/
import "C"

import "strings"

// viaC names a package of the standard library that no compiled file of this
// module imports.
func viaC(name string) string {
	return strings.TrimSpace(name) + strings.Repeat("!", int(C.abs(-1))+helper())
}
