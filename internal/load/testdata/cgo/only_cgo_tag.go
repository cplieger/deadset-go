//go:build cgo

package cgo

// notSelectedWithoutCgo is excluded by a build tag a configuration can name, and
// it imports nothing, so cgo is not the thing that excludes it.
func notSelectedWithoutCgo() int { return 6 }
