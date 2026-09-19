package cgofallback

/*
#include <stdlib.h>
*/
import "C"

// wrongType is a Go type error the opaque check reports in this file, which is
// what makes the file fall back to exclusion.
var wrongType int = "not an int"

// viaBrokenC calls helper from the file that falls back, so the reference is
// outside the reference set.
func viaBrokenC() int { return int(C.abs(-3)) + helper() }
