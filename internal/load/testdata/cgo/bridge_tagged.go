//go:build cgo

package cgo

/*
#include <stdlib.h>
*/
import "C"

// viaCUnderTheTag states the cgo tag as well as importing "C", so both reasons it
// is excluded are cgo.
func viaCUnderTheTag() int { return int(C.abs(-7)) }
