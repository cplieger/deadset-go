package load

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWalkTreeFollowsARootThatIsALinkAndKeepsItsSpelling(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var walked []string
	err := WalkTree(link, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		walked = append(walked, path)
		return nil
	})
	if want := []string{link, filepath.Join(link, "sub")}; err != nil || !slices.Equal(walked, want) {
		t.Errorf("WalkTree(%s) walked %q, %v, want %q", link, walked, err, want)
	}
}
