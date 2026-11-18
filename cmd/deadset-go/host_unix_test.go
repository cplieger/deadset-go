//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/host"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// blockedHost starts a host of its own whose go command blocks ([blockingGo]),
// asks it for one analysis, and returns once that analysis waits on the go
// command, with the probe file the command records its processes in and the
// channel the analysis's Run answers on.
func blockedHost(t *testing.T) (h *host.Host, probe string, ran <-chan error) {
	t.Helper()
	bin, probes := sandbox.Dir(t), sandbox.Dir(t)
	probe = filepath.Join(probes, "probe")
	if err := sandbox.WriteFile(filepath.Join(bin, "go"), []byte(blockingGo), 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	dir := writeModule(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nfunc main() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
	h, err := host.Start(hostBinary(t), sandbox.Dir(t), []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "DEADSET_GO_PROBE=" + probe})
	if err != nil {
		t.Fatalf("Setup: start a host: %v", err)
	}
	answered := make(chan error, 1)
	go func() {
		_, _, _, err := h.Run(context.Background(), dir, []string{"analyze", "--target=.", "--report=" + filepath.Join(probes, "report.json")})
		answered <- err
	}()
	for wait := time.Now().Add(time.Minute); !goStarted(probe); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(wait) {
			t.Fatal("Setup: the host's analysis started no go command within a minute")
		}
	}
	return h, probe, answered
}

// within is whether done answers within a minute, and what it answered.
func within(done <-chan error) (answered bool, err error) {
	select {
	case err := <-done:
		return true, err
	case <-time.After(time.Minute):
		return false, nil
	}
}

// stopped kills every process probe records that still runs, and fails the test
// naming them.
func stopped(t *testing.T, probe, what string) {
	t.Helper()
	left := runningOf(t, probe)
	for _, pid := range left {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if len(left) > 0 {
		t.Errorf("%s left the processes %v running, want none it started", what, left)
	}
}

// A host whose input ends, as it does when the test binary dies, cancels the runs
// still running, stops their go commands and exits.
func TestAHostWhoseInputEndsStopsEveryRunAndExits(t *testing.T) {
	t.Parallel()
	h, probe, _ := blockedHost(t)

	closed := make(chan error, 1)
	go func() { closed <- h.Close() }()
	if exited, _ := within(closed); !exited {
		t.Error("the host, its input closed while a run waits on its go command, did not exit within a minute")
		_ = h.Signal(os.Kill)
		<-closed
	}
	stopped(t, probe, "the host, its input closed while a run waits on its go command,")
}

// A signal ends the host as it ends a process: the run in flight and every later
// request answer as a host that stopped, naming the interrupted-run class once,
// and the host exits with the failure code.
func TestASignalledHostStopsTakingRequestsAndNamesTheCause(t *testing.T) {
	t.Parallel()
	h, probe, ran := blockedHost(t)

	if err := h.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Setup: signal the host: %v", err)
	}
	answered, err := within(ran)
	if !answered {
		_ = h.Signal(os.Kill)
		stopped(t, probe, "the host, sent SIGTERM while a run waits on its go command,")
		t.Fatal("the host, sent SIGTERM while a run waits on its go command, did not answer within a minute")
	}
	stopped(t, probe, "the host, sent SIGTERM while a run waits on its go command,")
	if err == nil || !strings.Contains(err.Error(), vocab.RunInterruptedRun) {
		t.Errorf("Run(the analysis in flight) = %v, want an error naming the %s class", err, vocab.RunInterruptedRun)
	}
	if _, stderr, code := h.Invoke(t.Context(), "", []string{"version"}); code != -1 || strings.Count(stderr, vocab.RunInterruptedRun) != 1 {
		t.Errorf("Invoke(a request after the signal) = %d writing %q, want -1 and the %s class once", code, stderr, vocab.RunInterruptedRun)
	}
	if err := h.Close(); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("Close(the signalled host) = %v, want exit status 3", err)
	}
}
