// Package clean type-checks under every configuration and carries a test, so a
// load of it reports the plain package, its in-package test variant and the
// synthesized test binary.
package clean

// Answer is referenced from the in-package test and from nowhere else.
func Answer() int { return 7 }
