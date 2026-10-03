package sigdeps

/*
#include <stdlib.h>
*/
import "C"

import "example.com/sigdep/viac"

// bridged reads a dependency no compiled file imports, through a file the toolchain
// ignores for importing "C".
func bridged() string { return viac.Name() + string(rune(C.abs(-1))) }
