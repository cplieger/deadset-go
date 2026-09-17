package cgo

/*
#include <stdlib.h>
*/
import "C"

// viaCOnWindows is excluded by its filename as well as by cgo, so the platform
// decides it and cgo is not its only reason.
func viaCOnWindows() int { return int(C.abs(-3)) }
