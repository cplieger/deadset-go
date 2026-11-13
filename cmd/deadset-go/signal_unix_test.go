//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// blockingGo is a go command that never answers: it records its own process and
// a child's in the file $DEADSET_GO_PROBE names, then waits on that child.
const blockingGo = "#!/bin/sh\nsleep 600 &\necho $$ $! >>\"$DEADSET_GO_PROBE\"\nwait\n"

// TestASignalledAnalysisLeavesNoChildRunning pins, within a minute a signal, that
// an analysis interrupted or terminated while its go command runs stops that
// command and everything it started before it exits.
func TestASignalledAnalysisLeavesNoChildRunning(t *testing.T) {
	t.Parallel()
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			t.Parallel()
			bin, probes := sandbox.Dir(t), sandbox.Dir(t)
			probe := filepath.Join(probes, "probe")
			if err := sandbox.WriteFile(filepath.Join(bin, "go"), []byte(blockingGo), 0o700); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			dir := writeModule(t, map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
				"main.go":          "package main\n\nfunc main() {}\n",
				repositoryDocument: `{"target": {"kind": "application"}}`,
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			cmd := deadline.Command(ctx, commandBinary(t), "analyze", "--target=.", "--report="+filepath.Join(probes, "report.json"))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "DEADSET_GO_PROBE="+probe)
			if err := cmd.Start(); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			for !goStarted(probe) && ctx.Err() == nil {
				time.Sleep(50 * time.Millisecond)
			}
			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			_ = cmd.Wait()
			if ctx.Err() != nil {
				t.Fatalf("analyze, sent %s while its go command runs, did not exit within a minute", signal)
			}
			left := runningOf(t, probe)
			deadline.Reap(cmd)
			for _, pid := range left {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			if len(left) > 0 {
				t.Errorf("analyze, sent %s while its go command runs, left the processes %v running, want none it started", signal, left)
			}
		})
	}
}

// goStarted reports whether the blocking go command recorded a process in probe.
func goStarted(probe string) bool {
	written, err := os.ReadFile(probe)
	return err == nil && len(strings.Fields(string(written))) > 0
}

// runningOf is every process probe records that still runs a few seconds on.
func runningOf(t *testing.T, probe string) []int {
	t.Helper()
	written, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("Setup: the blocking go command recorded no process: %v", err)
	}
	var pids []int
	for field := range strings.FieldsSeq(string(written)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("Setup: the probe file holds %q: %v", field, err)
		}
		pids = append(pids, pid)
	}
	for wait := time.Now().Add(3 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		left := slices.DeleteFunc(slices.Clone(pids), func(pid int) bool {
			state, err := exec.CommandContext(t.Context(), "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
			return err != nil || strings.HasPrefix(strings.TrimSpace(string(state)), "Z")
		})
		if len(left) == 0 || time.Now().After(wait) {
			return left
		}
	}
}
