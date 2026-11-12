//go:build unix

package exempt

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// TestLinknameCgoAsmPluginDetectorOpensNoPipeOrDeviceALinkNames pins that rule N
// decides a symbolic link without opening what it points at: a named pipe would
// block the run, and a device would be read without end.
func TestLinknameCgoAsmPluginDetectorOpensNoPipeOrDeviceALinkNames(t *testing.T) {
	t.Parallel()
	deadline.Row(t, time.Minute, "it opened a pipe or a device a link names", func(t *testing.T) {
		in := inputOf(t, "non-go-pipe.txtar", Options{})
		sandbox.Fifo(t, filepath.Join(in.Root, "testdata", "pipe"))
		got, err := LinknameCgoAsmPluginDetector(in)
		if err != nil {
			t.Fatalf("LinknameCgoAsmPluginDetector(non-go-pipe.txtar) error: %v", err)
		}
		want := []string{"named\tlinkname-cgo-asm-plugin\tdocs/names.h:1:1\t" + detailNonGoName}
		if lines := rendered(in.Symbols, got); !slices.Equal(lines, want) {
			t.Errorf("LinknameCgoAsmPluginDetector(non-go-pipe.txtar) = %q, want %q", lines, want)
		}
		if unread := in.unreadEntries(); len(unread) != 0 {
			t.Errorf("LinknameCgoAsmPluginDetector(non-go-pipe.txtar) left %q unread, want every entry decided", unread)
		}
	})
}
