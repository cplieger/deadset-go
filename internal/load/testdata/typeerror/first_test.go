package typeerror

import "testing"

// TestMismatch gives the package an in-package test variant, so the plain
// package and the variant report the same diagnostics independently.
func TestMismatch(t *testing.T) {
	if Mismatch() != 0 {
		t.Errorf("Mismatch() = %d, want 0", Mismatch())
	}
}
