//go:build unix

package exempt

import (
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// TestLinknameCgoAsmPluginDetectorOpensNoPipeOrDeviceALinkNames pins that rule N
// decides a symbolic link without opening what it points at: a named pipe would
// block the run, and a device would be read without end.
func TestLinknameCgoAsmPluginDetectorOpensNoPipeOrDeviceALinkNames(t *testing.T) {
	t.Parallel()
	in := inputOf(t, "non-go-pipe.txtar", Options{})
	if err := syscall.Mkfifo(filepath.Join(in.Root, "testdata", "pipe"), 0o600); err != nil {
		t.Skipf("Setup: this host makes no named pipe: %v", err)
	}

	type result struct {
		got []Exemption
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := LinknameCgoAsmPluginDetector(in)
		done <- result{got: got, err: err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(time.Minute):
		t.Fatal("LinknameCgoAsmPluginDetector(non-go-pipe.txtar) did not return in a minute: it opened a pipe or a device a link names")
	}
	if r.err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(non-go-pipe.txtar) error: %v", r.err)
	}

	want := []string{"named\tlinkname-cgo-asm-plugin\tdocs/names.h:1:1\t" + detailNonGoName}
	if lines := rendered(in.Symbols, r.got); !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(non-go-pipe.txtar) = %q, want %q", lines, want)
	}
	if unread := in.unreadEntries(); len(unread) != 0 {
		t.Errorf("LinknameCgoAsmPluginDetector(non-go-pipe.txtar) left %q unread, want every entry decided", unread)
	}
}
