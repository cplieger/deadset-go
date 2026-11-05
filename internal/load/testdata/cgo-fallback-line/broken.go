package cgofallbackline

/*
#include <stdlib.h>
*/
import "C"

// wrongType is a Go type error of this file, which a line directive renders in
// another file's name.
//
//line generated.y:10
var wrongType int = "not an int"

func viaBrokenC() int { return int(C.abs(-3)) }
