// Package constraint carries one file no configuration but plan9 selects, so a
// load reports that file as ignored while compiling the rest.
package constraint

// Selected is compiled under every configuration.
func Selected() int { return 1 }
