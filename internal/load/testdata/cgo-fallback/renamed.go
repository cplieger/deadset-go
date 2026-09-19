package cgofallback

/*
#include <stdlib.h>
*/
import bridge "C"

// viaRenamedC is in a file the language refuses, because the import of "C" cannot
// be renamed, so the check reports that and the file falls back.
func viaRenamedC() int { return helper() }
