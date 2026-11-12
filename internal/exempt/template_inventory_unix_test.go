//go:build unix

package exempt

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// TestTemplateFilesOpensNoPipeOrDevice pins that a template file a parse names is
// read only where it is a regular file: a named pipe or a link to a device leaves
// the inventory incomplete, and the read returns at once.
func TestTemplateFilesOpensNoPipeOrDevice(t *testing.T) {
	t.Parallel()
	deadline.Row(t, time.Minute, "it opened what is no regular file", func(t *testing.T) {
		dir := sandbox.Dir(t)
		pipe := filepath.Join(dir, "page.tmpl")
		sandbox.Fifo(t, pipe)
		device := filepath.Join(dir, "zero.tmpl")
		if err := sandbox.Symlink("/dev/zero", device); err != nil {
			t.Skipf("Setup: this host makes no symbolic link: %v", err)
		}
		for _, path := range []string{pipe, device} {
			inv := &templateSet{complete: true}
			inv.files("text/template", []string{path}, "", false)
			if inv.complete {
				t.Errorf("files(%s) left the inventory complete, want it incomplete", filepath.Base(path))
			}
		}
	})
}
