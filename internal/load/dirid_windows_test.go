//go:build windows

package load

import (
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// Two directories on one volume are on one device, and that device is named, so a
// link to another volume is told apart from one on the module's.
func TestDeviceOfNamesOneDeviceForTwoDirectoriesOnOneVolume(t *testing.T) {
	t.Parallel()
	dir := sandbox.Dir(t)
	below := filepath.Join(dir, "below")
	if err := sandbox.MkdirAll(below, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", below, err)
	}
	outer, inner := deviceOf(dir), deviceOf(below)
	if outer == nil || inner == nil || outer != inner {
		t.Errorf("deviceOf(%s) = %v, deviceOf(%s) = %v, want one device named for both", dir, outer, below, inner)
	}
	if missing := deviceOf(filepath.Join(dir, "absent")); missing != nil {
		t.Errorf("deviceOf(an absent entry) = %v, want nil", missing)
	}
}
