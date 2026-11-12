//go:build unix

package load

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// probeVariable names the file where the probe run of
// TestALoadRowAtItsDeadlineLeavesNoProcessBehind has its blocking toolchain record
// the processes it starts.
const probeVariable = "DEADSET_DEADLINE_PROBE"

// blockingToolchain is a go command that never answers: it records its own
// process and a child's in the probe file, then waits on that child, which sleeps
// past any deadline.
const blockingToolchain = "#!/bin/sh\nsleep 600 &\necho $$ $! >>\"$" + probeVariable + "\"\nwait\n"

// TestALoadRowAtItsDeadlineLeavesNoProcessBehind pins that a row of the load
// tables leaves nothing running once it fails at its deadline: run against a tree
// whose toolchain blocks, the row's context ends or the row stalls, and neither the
// toolchain it started nor that toolchain's own child survives it, so no process
// keeps a descriptor the go test run handed down, the heavy-test lock included.
func TestALoadRowAtItsDeadlineLeavesNoProcessBehind(t *testing.T) {
	if os.Getenv(probeVariable) != "" {
		stalledLoadRow(t)
		return
	}
	dir := sandbox.Dir(t)
	probe, log := filepath.Join(dir, "probe"), filepath.Join(dir, "log")
	out, err := sandbox.Create(log)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := deadline.Command(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), probeVariable+"="+probe)
	cmd.Stdout, cmd.Stderr = out, out
	runErr := cmd.Run()
	_ = out.Close()
	left := surviving(t, probe)
	deadline.Reap(cmd)
	written, _ := os.ReadFile(log)
	ended := strings.Contains(string(written), "did not return within") || strings.Contains(string(written), "want it never to end before the row's context")
	if _, exited := errors.AsType[*exec.ExitError](runErr); ctx.Err() != nil || !exited || !ended {
		t.Fatalf("Setup: the probe run = %v, want its row to fail at its deadline:\n%s", runErr, written)
	}
	if len(left) > 0 {
		t.Errorf("a load row that failed at its deadline left the processes %v running, want none it started", left)
	}
}

// stalledLoadRow is the probe run's row: a load of a module whose go command is
// [blockingToolchain], under a deadline of half a minute.
func stalledLoadRow(t *testing.T) {
	deadline.Row(t, 30*time.Second, "the go command never answers", func(t *testing.T) {
		bin := sandbox.Dir(t)
		if err := sandbox.WriteFile(filepath.Join(bin, "go"), []byte(blockingToolchain), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		root := writeTree(t, map[string]string{"go.mod": "module example.com/m\n\ngo 1.27.1\n", "main.go": "package main\n\nfunc main() {}\n"})
		_, err := loadScope(deadline.Context(t), scope.Document{Target: scope.Module{Path: root}}, HostConfiguration())
		t.Errorf("the load of a module whose go command never answers ended with %v, want it never to end before the row's context", err)
	})
}

// surviving is every process the probe file records that is still running a
// few seconds after the probe run ended; a process that exited and awaits its
// parent's wait runs nothing.
func surviving(t *testing.T, probe string) []int {
	t.Helper()
	recorded, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("Setup: the blocking toolchain recorded no process: %v", err)
	}
	var pids []int
	for field := range strings.FieldsSeq(string(recorded)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("Setup: the probe file holds %q: %v", field, err)
		}
		pids = append(pids, pid)
	}
	for wait := time.Now().Add(3 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		left := slices.DeleteFunc(slices.Clone(pids), func(pid int) bool { return !running(pid) })
		if len(left) == 0 || time.Now().After(wait) {
			return left
		}
	}
}

// running reports whether the process pid exists and is no zombie.
func running(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(state)), "Z")
}
