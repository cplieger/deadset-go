//go:build unix

package exempt

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestTemplateFilesOpensNoPipeOrDevice pins that a template file a parse names is
// read only where it is a regular file: a named pipe or a link to a device leaves
// the inventory incomplete, and the read returns at once.
func TestTemplateFilesOpensNoPipeOrDevice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pipe := filepath.Join(dir, "page.tmpl")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("Setup: this host makes no named pipe: %v", err)
	}
	device := filepath.Join(dir, "zero.tmpl")
	if err := os.Symlink("/dev/zero", device); err != nil {
		t.Skipf("Setup: this host makes no symbolic link: %v", err)
	}
	for _, path := range []string{pipe, device} {
		inv := &templateSet{complete: true}
		done := make(chan struct{})
		go func() {
			inv.files("text/template", []string{path}, "", false)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Fatalf("files(%s) did not return within a minute: it opened what is no regular file", filepath.Base(path))
		}
		if inv.complete {
			t.Errorf("files(%s) left the inventory complete, want it incomplete", filepath.Base(path))
		}
	}
}
