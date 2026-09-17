// Package cgo holds one file excluded only because it imports "C", one excluded
// by the cgo build tag as well as by importing "C", one excluded only by a
// platform constraint, one excluded by the platform as well as by cgo, one
// excluded by the cgo build tag without importing "C", and one non-Go file the
// toolchain also ignores.
package cgo

// Selected is compiled under every configuration.
func Selected() int { return 1 }
