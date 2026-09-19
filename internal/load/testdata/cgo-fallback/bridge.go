package cgofallback

/*
#include <stdlib.h>
*/
import "C"

// viaC reaches C and Go, and everything it writes in Go type-checks.
func viaC() int { return int(C.abs(-2)) + helper() }
