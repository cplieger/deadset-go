// Package typed lists cleanly and does not type-check.
package typed

// Mismatch returns a string where its signature promises an int.
func Mismatch() int { return "not an int" }
