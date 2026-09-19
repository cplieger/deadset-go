// Package cgoimport holds one file the toolchain ignores solely for importing
// "C", which imports a package no file the load compiles imports, so the opaque-C
// check has to resolve that package itself.
package cgoimport

// helper is called from the file importing "C".
func helper() int { return 1 }

// Selected is compiled under every configuration and imports nothing.
func Selected() int { return helper() }
