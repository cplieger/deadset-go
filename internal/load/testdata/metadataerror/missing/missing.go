// Package missing imports a package no module of the build list provides, so the
// toolchain's own metadata carries an error.
package missing

import "example.com/nowhere"

// Answer names the missing package.
func Answer() int { return nowhere.Value }
