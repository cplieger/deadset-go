// Package unbuilt is built under every configuration, beside a package whose
// every file only windows builds.
package unbuilt

// Selected is compiled under every configuration.
func Selected() int { return 1 }
