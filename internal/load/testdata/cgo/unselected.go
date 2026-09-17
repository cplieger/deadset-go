//go:build plan9

package cgo

// notSelected is excluded by a build constraint and imports nothing.
func notSelected() int { return 4 }
