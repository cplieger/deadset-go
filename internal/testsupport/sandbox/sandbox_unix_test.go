//go:build unix

package sandbox_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// TestRefusesEveryPathOutsideItsRoots pins that no call makes anything outside a
// temporary root a test made: not at a path outside every root, not through a link
// inside a root that leads out, not through a .. that climbs out, and not through a
// .. after such a link, which the names alone keep inside the root, and that the
// same calls inside a root succeed.
func TestRefusesEveryPathOutsideItsRoots(t *testing.T) {
	root := sandbox.Dir(t)
	outside := t.TempDir() // a directory of t that no call of the package made a root
	if err := sandbox.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatalf("Symlink(a link inside the root) = %v, want no error", err)
	}
	tests := []struct {
		name string
		make func(path string) error
	}{
		{name: "a_pipe", make: func(path string) error { return sandbox.Mkfifo(path, 0o600) }},
		{name: "a_file", make: func(path string) error { return sandbox.WriteFile(path, []byte("x"), 0o600) }},
		{name: "a_directory", make: func(path string) error { return sandbox.MkdirAll(path, 0o700) }},
		{name: "a_link", make: func(path string) error { return sandbox.Symlink("/dev/zero", path) }},
		{name: "a_socket", make: func(path string) error {
			l, err := sandbox.ListenUnix(path)
			if err == nil {
				_ = l.Close()
			}
			return err
		}},
	}
	for _, tc := range tests {
		for _, at := range []struct{ how, path string }{
			{"outside_every_root", filepath.Join(outside, tc.name)},
			{"through_a_link_out_of_the_root", filepath.Join(root, "out", tc.name)},
			{"through_a_parent_segment", filepath.Join(root, "..", filepath.Base(outside), tc.name)},
			// Unjoined, so no lexical clean removes the segment: the kernel resolves out
			// first, and out/.. is outside's parent, while the names alone stay in root.
			{"through_a_link_then_a_parent_segment", root + "/out/../" + filepath.Base(outside) + "/" + tc.name},
		} {
			t.Run(tc.name+"_"+at.how, func(t *testing.T) {
				if err := tc.make(at.path); !errors.Is(err, sandbox.ErrOutside) {
					t.Errorf("making %s at %s = %v, want %v", tc.name, at.path, err, sandbox.ErrOutside)
				}
				if _, err := os.Lstat(filepath.Join(outside, tc.name)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("making %s at %s left %s, want nothing made", tc.name, at.path, filepath.Join(outside, tc.name))
				}
			})
		}
		t.Run(tc.name+"_inside_the_root", func(t *testing.T) {
			err := tc.make(filepath.Join(root, tc.name))
			if err != nil && tc.name == "a_socket" && !errors.Is(err, sandbox.ErrOutside) {
				t.Skipf("Setup: this host makes no Unix socket at %s: %v", filepath.Join(root, tc.name), err)
			}
			if err != nil {
				t.Errorf("making %s inside the root = %v, want no error", tc.name, err)
			}
		})
	}
}
