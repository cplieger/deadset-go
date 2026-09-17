package cgo

/*
#include <stdlib.h>
*/
import "C"

// viaC is excluded with cgo disabled and by nothing else.
func viaC() int { return int(C.abs(-2)) }
