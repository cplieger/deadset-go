// Package cgofallback holds three files the toolchain ignores solely for
// importing "C": one the opaque-C check reads, one carrying a Go type error of its
// own, and one renaming the import of "C", which the language refuses.
package cgofallback

// helper is called from the file the check reads and from the one it does not.
func helper() int { return 1 }

// Selected is compiled under every configuration.
func Selected() int { return helper() }
