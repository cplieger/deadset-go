// Package clean type-checks under every configuration and carries an in-package
// and an external test file, so a load of it reports the plain package and both
// test variants.
package clean

// Answer is referenced from the in-package test and from nowhere else.
func Answer() int { return 7 }
