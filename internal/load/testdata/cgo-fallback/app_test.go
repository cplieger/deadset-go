package cgofallback

import "testing"

// TestSelected gives the package an in-package test variant, so the load returns a
// second package that type-checks the same production files and ignores the same
// files.
func TestSelected(t *testing.T) {
	if got := Selected(); got != 1 {
		t.Errorf("Selected() = %d, want 1", got)
	}
}
