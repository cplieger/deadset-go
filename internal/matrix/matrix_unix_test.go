//go:build unix

package matrix

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// TestDeriveReadsOnlyTheRegularGoFilesOfTheTree pins the Go file names the
// derivation meets in a package: it returns within a minute, reads a regular
// file it can open, records one it cannot as unread, and passes over a pipe, a
// device, and a link to a directory or to nothing, which no build reads. Each
// entry is made after the topology is read, so the row is the derivation's alone.
func TestDeriveReadsOnlyTheRegularGoFilesOfTheTree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		make   func(t *testing.T, path string)
		unread bool
	}{
		{name: "a_pipe", make: func(t *testing.T, path string) {
			t.Helper()
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skipf("Setup: this host makes no named pipe: %v", err)
			}
		}},
		{name: "a_link_to_a_device", make: link("/dev/zero")},
		{name: "a_link_to_a_directory", make: func(t *testing.T, path string) { t.Helper(); link(t.TempDir())(t, path) }},
		{name: "a_link_to_nothing", make: link("missing.go")},
		{name: "a_file_the_user_cannot_open", unread: true, make: func(t *testing.T, path string) {
			t.Helper()
			if os.Geteuid() == 0 {
				t.Skip("Setup: a mode of 0 leaves a file readable to root")
			}
			if err := os.WriteFile(path, []byte("package main\n"), 0); err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := extract(t, "boolean.txtar")
			top := topOf(t, dir)
			path := filepath.Join(dir, "entry.go")
			tc.make(t, path)

			done := make(chan error, 1)
			go func() {
				_, err := derive(top, new(fixtureHost()))
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("derive(a package holding %s named entry.go) = error %v, want none", tc.name, err)
				}
			case <-time.After(time.Minute):
				t.Fatalf("derive(a package holding %s named entry.go) did not return within a minute: it read an entry before deciding its kind", tc.name)
			}
			if got := slices.Contains(top.Unread, path); got != tc.unread {
				t.Errorf("derive(a package holding %s named entry.go) left the topology's unread paths %q, want %s held: %t", tc.name, top.Unread, path, tc.unread)
			}
		})
	}
}

// link makes path a symbolic link to target.
func link(target string) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("Setup: this host makes no symbolic link: %v", err)
		}
	}
}
