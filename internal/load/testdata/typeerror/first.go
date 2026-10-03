// Package typeerror does not type-check: a load records its type errors and
// fails nothing.
package typeerror

// Mismatch returns a string where its signature promises an int.
func Mismatch() int { return "not an int" }
