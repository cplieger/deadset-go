// Package typeerror does not type-check, so a load of it produces diagnostics
// and no package set at all.
package typeerror

// Mismatch returns a string where its signature promises an int.
func Mismatch() int { return "not an int" }
