//go:build unix

package load

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// logVariable names the file where [loggingToolchain] records each go command.
const logVariable = "DEADSET_GO_LOG"

// loggingToolchain is a go command that records its GOFLAGS and its arguments,
// then runs the go command at the path it is written with.
const loggingToolchain = "#!/bin/sh\nprintf '%s\\t%s\\n' \"$GOFLAGS\" \"$*\" >>\"$" + logVariable + "\"\nexec %q \"$@\"\n"

// TestEveryGoCommandALoadRunsCarriesTheOverlay pins, within a minute, that every
// go command a load of a tree holding a hidden pipe starts reads the tree through
// the overlay ([program.6]): go/packages' own queries take no build flags, so the
// overlay must reach them through GOFLAGS.
func TestEveryGoCommandALoadRunsCarriesTheOverlay(t *testing.T) {
	stableToolchain(t)
	deadline.Row(t, time.Minute, "a go command read the pipe the overlay hides", func(t *testing.T) {
		real, err := exec.LookPath("go")
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		bin, logs := sandbox.Dir(t), sandbox.Dir(t)
		log := filepath.Join(logs, "go.log")
		if err := sandbox.WriteFile(filepath.Join(bin, "go"), []byte(strings.Replace(loggingToolchain, "%q", `"`+real+`"`, 1)), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv(logVariable, log)
		root := writeTree(t, moduleTree)
		sandbox.Fifo(t, filepath.Join(root, "p", "x.go"))
		if _, err := loadScope(deadline.Context(t), scope.Document{Target: scope.Module{Path: root}}, HostConfiguration()); err != nil {
			t.Fatalf("Load(a module holding a pipe in a package) = _, %v, want no error", err)
		}
		written, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("Load(a module holding a pipe in a package) ran no go command: %v", err)
		}
		for line := range strings.Lines(string(written)) {
			if !strings.Contains(line, overlayFlag) {
				t.Errorf("Load(a module holding a pipe in a package) ran the go command %q, want it to carry the overlay", strings.TrimSpace(line))
			}
		}
	})
}

// TestACancelledLoadLeavesNoChildRunning pins, within a minute, that a load whose
// context is cancelled while its go command runs returns and leaves neither that
// command nor anything it started running.
func TestACancelledLoadLeavesNoChildRunning(t *testing.T) {
	stableToolchain(t)
	deadline.Row(t, time.Minute, "the cancelled load did not return", func(t *testing.T) {
		bin, probes := sandbox.Dir(t), sandbox.Dir(t)
		probe := filepath.Join(probes, "probe")
		if err := sandbox.WriteFile(filepath.Join(bin, "go"), []byte(blockingToolchain), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv(probeVariable, probe)
		root := writeTree(t, map[string]string{"go.mod": "module example.com/m\n\ngo 1.27.1\n", "main.go": "package main\n\nfunc main() {}\n"})
		ctx, cancel := context.WithCancel(deadline.Context(t))
		go func() {
			for !started(probe) && ctx.Err() == nil {
				time.Sleep(50 * time.Millisecond)
			}
			cancel()
		}()
		defer cancel()
		if _, err := loadScope(ctx, scope.Document{Target: scope.Module{Path: root}}, HostConfiguration()); err == nil {
			t.Fatal("Load(a module whose go command never answers, cancelled) = _, nil, want the cancellation")
		}
		if left := surviving(t, probe); len(left) > 0 {
			for _, pid := range left {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			t.Errorf("a cancelled load left the processes %v running, want none it started", left)
		}
	})
}

// started reports whether the blocking toolchain recorded a process in probe.
func started(probe string) bool {
	written, err := os.ReadFile(probe)
	return err == nil && len(strings.Fields(string(written))) > 0
}
