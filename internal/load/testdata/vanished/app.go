// Package vanished holds a subdirectory whose every file is excluded, so the
// load reports no package for that directory at all.
package vanished

// Selected is compiled under every configuration.
func Selected() int { return 1 }
