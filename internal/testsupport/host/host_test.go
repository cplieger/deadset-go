package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/hostwire"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/rerun"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// fakeVariable makes this test binary a host that writes a line to stderr and
// exits 3 before answering anything, and echoVariable one that answers each
// request in turn, as it reads them, with echoed bytes of stdout.
const (
	fakeVariable = "DEADSET_HOST_FAKE_FAILS"
	echoVariable = "DEADSET_HOST_FAKE_ECHO"
	echoed       = 96 << 10
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeVariable) != "" {
		fmt.Fprintln(os.Stderr, "the fake host fails")
		os.Exit(3)
	}
	if os.Getenv(echoVariable) != "" {
		requests, answers := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
		for {
			var req hostwire.Request
			if requests.Decode(&req) != nil {
				os.Exit(0)
			}
			if answers.Encode(hostwire.Response{ID: req.ID, Stdout: bytes.Repeat([]byte{'x'}, echoed)}) != nil {
				os.Exit(3)
			}
		}
	}
	os.Exit(m.Run())
}

// A client whose requests wait on a host that is writing an answer still reads
// that answer: sixteen Runs at once, each request and each answer larger than a
// pipe's share, against a host that reads no request while it writes, are all
// answered within a minute.
func TestRunsAtOnceAreAnsweredWhileTheHostWritesBeforeItReads(t *testing.T) {
	t.Parallel()

	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("Setup: os.Executable: %v", err)
	}
	h, err := Start(binary, sandbox.Dir(t), rerun.Environment([]string{echoVariable + "=1"}))
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	args := []string{strings.Repeat("x", 8<<10)}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 8 {
				stdout, _, _, err := h.Run(t.Context(), "", args)
				if err != nil || len(stdout) != echoed {
					t.Errorf("Run = %d bytes of stdout (%v), want %d", len(stdout), err, echoed)
					return
				}
			}
		})
	}
	answered := make(chan struct{})
	go func() {
		wg.Wait()
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(time.Minute):
		t.Fatal("sixteen Runs at once were not answered within a minute: the client stopped reading answers while a request waited on the host")
	}
	if err := h.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

func TestAHostThatStopsAnsweringIsAnErrorNamingItsStderr(t *testing.T) {
	t.Parallel()

	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("Setup: os.Executable: %v", err)
	}
	h, err := Start(binary, sandbox.Dir(t), rerun.Environment([]string{fakeVariable + "=1"}))
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	dir := sandbox.Dir(t)
	if _, _, _, err := h.Run(t.Context(), dir, []string{"describe"}); err == nil || !strings.Contains(err.Error(), "the fake host fails") {
		t.Errorf("Run(a host that exited) = %v, want an error naming its stderr", err)
	}
	if _, stderr, code := h.Invoke(t.Context(), dir, []string{"describe"}); code != -1 || !strings.Contains(stderr, "the fake host fails") {
		t.Errorf("Invoke(a host that exited) = %d writing %q, want -1 and a line naming its stderr", code, stderr)
	}
	if err := h.Close(); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("Close(a host that exited 3) = %v, want its exit failure", err)
	}
}

func TestRunRefusesARelativeDirectory(t *testing.T) {
	t.Parallel()

	h := &Host{}
	if _, _, _, err := h.Run(context.Background(), "relative", []string{"describe"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("Run(a relative directory) = %v, want a refusal", err)
	}
}
