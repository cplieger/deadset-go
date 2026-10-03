// Package viac is imported only by a file of the dependent module that imports "C".
package viac

// Name is what the file importing "C" reads.
func Name() string { return "viac" }

// Broken returns a string where its signature promises an int, and the error is
// inside its body.
func Broken() int { return "not an int" }
