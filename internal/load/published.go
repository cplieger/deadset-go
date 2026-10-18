package load

import (
	"slices"
	"strings"
)

// Published reports whether a program outside the module can import the package at
// pkgPath: not a main package, not an external test package, and not below an
// internal element.
func Published(pkgPath string, main bool) bool {
	return !main && !strings.HasSuffix(pkgPath, "_test") && !slices.Contains(strings.Split(pkgPath, "/"), "internal")
}
