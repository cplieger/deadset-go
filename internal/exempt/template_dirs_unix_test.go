//go:build unix

package exempt

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// Two configured template directories that name one directory where both resolve
// below the root, one through a link inside it, are scanned once, under the first
// spelling; a directory configured twice by one spelling is kept once too.
func TestTemplateDirsScansADirectoryNamedThroughALinkOnce(t *testing.T) {
	t.Parallel()
	root := sandbox.Dir(t)
	if err := sandbox.MkdirAll(filepath.Join(root, "tpl", "sub"), 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := sandbox.Symlink("tpl", filepath.Join(root, "l")); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	got := templateDirs(root, []string{"tpl", "l", "./tpl/", "tpl/sub", "l/sub", "other"})
	if want := []string{"tpl", "tpl/sub", "other"}; !slices.Equal(got, want) {
		t.Errorf("templateDirs = %q, want %q", got, want)
	}
}
