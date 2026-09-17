//go:build plan9

// Package absent has no file any configuration but plan9 selects, so no
// configuration this fixture is loaded with reports a package for it.
package absent

// notSelected is the package's only declaration.
func notSelected() int { return 1 }
