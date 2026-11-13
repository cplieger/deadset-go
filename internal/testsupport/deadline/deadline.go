//go:build unix

package deadline

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// rowVariable names the row a re-executed test binary runs in place.
const rowVariable = "DEADSET_DEADLINE_ROW"

// waitDelay is how long the output of a killed group may stay open before Wait
// stops reading it.
const waitDelay = 10 * time.Second

// psLimit is how long one reading of the process table may take.
const psLimit = 5 * time.Second

// outcome is how the child process of a row ended.
type outcome int

const (
	returned outcome = iota // the child exited 0: the row passed or skipped
	failed                  // the child exited with another code
	stalled                 // the deadline killed the child's group
)

// Row runs row as the test t within limit, in a child process of the test binary
// that leads a process group of its own. At limit the group is killed and t fails
// naming why; the child's failure fails t with the child's output, and its skip
// skips t. Inside that child, Row runs row on t in place, and [Context] of t ends
// [stopGrace] before limit, at half of it at most, so a load the row runs under it
// is cancelled, and stops what it started, before the group is killed.
func Row(t *testing.T, limit time.Duration, why string, row func(t *testing.T)) {
	t.Helper()
	if os.Getenv(rowVariable) == t.Name() {
		ctx, cancel := context.WithTimeout(t.Context(), limit-min(limit/2, stopGrace))
		contexts.Store(t, ctx)
		defer func() {
			contexts.Delete(t)
			cancel()
		}()
		row(t)
		return
	}
	output, ended, err := run(t, limit)
	switch {
	case err != nil:
		t.Fatalf("Setup: run %s in a process of its own: %v\n%s", t.Name(), err, output)
	case ended == stalled:
		t.Fatalf("%s did not return within %s: %s\n%s", t.Name(), limit, why, output)
	case ended == failed:
		t.Fatalf("%s failed in its own process:\n%s", t.Name(), output)
	case strings.Contains(output, "--- SKIP: "+t.Name()+" "):
		t.Skipf("%s skipped in its own process:\n%s", t.Name(), output)
	case !strings.Contains(output, "--- PASS: "+t.Name()+" "):
		t.Fatalf("Setup: %s ran no row in its own process:\n%s", t.Name(), output)
	}
}

// run re-executes the test binary for the row t alone, within limit, with its
// temporary directory below one of t's, and reports the child's output and how it
// ended; the error is a failure to run it at all.
func run(t *testing.T, limit time.Duration) (string, outcome, error) {
	t.Helper()
	temp := sandbox.Dir(t)
	t.Cleanup(func() { unblockBelow(temp) })
	ctx, cancel := context.WithTimeout(t.Context(), limit)
	defer cancel()
	args := []string{"-test.run=" + pattern(t.Name()), "-test.count=1", "-test.v"}
	if testing.Short() {
		args = append(args, "-test.short")
	}
	// The test binary does not stop its children on SIGTERM, so its tree is killed
	// at once: its own context ended a quarter of limit earlier ([Context]).
	cmd := Command(ctx, os.Args[0], args...)
	cmd.Cancel = func() error { return killTree(cmd.Process.Pid) }
	cmd.Env = append(os.Environ(), rowVariable+"="+t.Name(), "TMPDIR="+temp)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	Reap(cmd)
	_, exited := errors.AsType[*exec.ExitError](err)
	switch {
	case ctx.Err() != nil:
		return out.String(), stalled, nil
	case err == nil:
		return out.String(), returned, nil
	case exited:
		return out.String(), failed, nil
	default:
		return out.String(), returned, err
	}
}

// stopGrace is how long before its deadline a row's context ends: the x/tools
// loader interrupts the go command it started when its context ends and kills the
// command's process group 5 s later (internal/gocommand runCmdContext), and a
// child the interrupt leaves behind is no longer a descendant the deadline finds.
const stopGrace = 15 * time.Second

// contexts holds the context of each row running in place ([Row]).
var contexts sync.Map

// Context is the context a row t runs its loads under: one that ends before the
// row's deadline where [Row] runs t in place, and t's own otherwise.
func Context(t *testing.T) context.Context {
	if held, ok := contexts.Load(t); ok {
		if ctx, ok := held.(context.Context); ok {
			return ctx
		}
	}
	return t.Context()
}

// pattern is the -test.run expression that selects the test named name alone.
func pattern(name string) string {
	levels := strings.Split(name, "/")
	for i, level := range levels {
		levels[i] = "^" + regexp.QuoteMeta(level) + "$"
	}
	return strings.Join(levels, "/")
}

// Command is the command name with args, run as the leader of a process group of
// its own. When ctx ends the command gets SIGTERM, so a command that stops what it
// started on that signal does, and where it still runs termGrace later, every
// process descending from it is stopped and then killed with its whole process
// group: the go command the x/tools loader starts leads a group of its own, which
// a kill of the command's group alone misses. The caller calls [Reap] once it
// returns, which kills what the command left running.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G702: a test runs its own binary or one it built
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return killTree(pid)
		}
		for until := time.Now().Add(termGrace); time.Now().Before(until); time.Sleep(100 * time.Millisecond) {
			if !alive(pid) {
				return nil
			}
		}
		return killTree(pid)
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// termGrace is how long a cancelled command has to stop on SIGTERM.
const termGrace = 10 * time.Second

// alive reports whether the process pid runs: it exists and has not exited.
func alive(pid int) bool {
	tree, err := processes()
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	return slices.ContainsFunc(tree, func(p process) bool { return p.pid == pid && !p.zombie })
}

// Reap kills every process left in the group cmd leads, a child the command
// started and did not wait for included.
func Reap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = killGroup(cmd.Process.Pid)
	}
}

// killTree stops root and every process descending from it, so none forks or
// exits and orphans a child meanwhile, then kills the process group of each. The
// descendants are read from ps, which Linux and macOS both ship; where ps cannot
// be read, root's own group is still killed.
func killTree(root int) error {
	groups, stopped := stopTree(root)
	err := killGroup(root)
	for group := range groups {
		if group != root {
			_ = killGroup(group)
		}
	}
	for pid := range stopped {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return err
}

// stopTree stops root and its descendants, reading ps again until a reading
// finds no process it has not stopped, and reports every process group they
// lead or belong to beside the test's own, and every process it stopped.
func stopTree(root int) (groups, stopped map[int]bool) {
	own := syscall.Getpgrp()
	groups, stopped = map[int]bool{root: true}, map[int]bool{}
	for range 5 {
		tree, err := processes()
		if err != nil {
			return groups, stopped
		}
		added := false
		for _, p := range descendants(root, tree) {
			if !stopped[p.pid] {
				_ = syscall.Kill(p.pid, syscall.SIGSTOP)
				stopped[p.pid], added = true, true
			}
			if p.pgid != own {
				groups[p.pgid] = true
			}
		}
		if !added {
			break
		}
	}
	return groups, stopped
}

// killGroup kills the process group pgid; a group already empty is gone.
func killGroup(pgid int) error {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// process is one process: its id, its parent's and its group's, and whether it
// exited and awaits its parent's wait.
type process struct {
	pid, ppid, pgid int
	zombie          bool
}

// processes is every process ps lists, read within psLimit.
func processes() ([]process, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psLimit)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "pgid=", "-o", "stat=").Output()
	if err != nil {
		return nil, err
	}
	var listed []process
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			continue
		}
		var ids [3]int
		for i, field := range fields[:3] {
			if ids[i], err = strconv.Atoi(field); err != nil {
				return nil, err
			}
		}
		listed = append(listed, process{pid: ids[0], ppid: ids[1], pgid: ids[2], zombie: strings.HasPrefix(fields[3], "Z")})
	}
	return listed, nil
}

// descendants is root and every process of tree below it.
func descendants(root int, tree []process) []process {
	children := make(map[int][]process)
	var self []process
	for _, p := range tree {
		children[p.ppid] = append(children[p.ppid], p)
		if p.pid == root {
			self = append(self, p)
		}
	}
	found := self
	for queue := []int{root}; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if child.pid != root {
				found = append(found, child)
				queue = append(queue, child.pid)
			}
		}
	}
	return found
}

// unblockBelow unblocks every named pipe below dir, where the clean-up of a killed
// row never ran.
func unblockBelow(dir string) {
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type()&fs.ModeNamedPipe != 0 {
			sandbox.Unblock(path)
		}
		return nil
	})
}
