// Package cgofallbackline holds two files the toolchain ignores solely for
// importing "C": one the opaque-C check reads, and one carrying a Go type error
// under a line directive naming another file.
package cgofallbackline

// Selected is compiled under every configuration.
func Selected() int { return 1 }
