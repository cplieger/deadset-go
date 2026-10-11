package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/memory"
	spec "github.com/cplieger/deadset-spec/v7"
)

// memoryExhaustionLine is the line the Contract's exit-code table states for a run
// that needs more memory than the machine makes available, as a pattern whose two
// groups are N and M.
func memoryExhaustionLine(t *testing.T) *regexp.Regexp {
	t.Helper()
	body, err := spec.Contract.ReadFile("contract/exit-codes.json")
	if err != nil {
		t.Fatalf("Setup: read contract/exit-codes.json: %v", err)
	}
	var document struct {
		MemoryExhaustion string `json:"memory_exhaustion"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/exit-codes.json: %v", err)
	}
	quoted := regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(document.MemoryExhaustion)
	if quoted == nil {
		t.Fatalf("Setup: memory_exhaustion %q quotes no line", document.MemoryExhaustion)
	}
	number := `(\d+(?:\.\d)?)`
	pattern := strings.NewReplacer(" N ", " "+number+" ", " M ", " "+number+" ").Replace(regexp.QuoteMeta(quoted[1]))
	return regexp.MustCompile(`^` + pattern + `\n$`)
}

// held keeps the live heap the low-budget test runs under.
var held []byte

func TestARunThatCannotFitExitsWithTheMemoryLine(t *testing.T) {
	line := memoryExhaustionLine(t)
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	t.Chdir(dir)
	reportPath := filepath.Join(t.TempDir(), "report.json")

	// A machine with no memory left beyond what the process holds, and a live heap
	// of half a gigabyte, is a run the governor cannot fit.
	held = make([]byte, 512<<20)
	for i := range held {
		held[i] = 1
	}
	t.Cleanup(func() { held = nil; runtime.GC() })
	noHeadroom := func() (uint64, bool) { return 0, true }

	var stdout, stderr bytes.Buffer
	code := governed([]string{"analyze", "--target=.", "--report=" + reportPath}, &stdout, &stderr, []memory.Source{noHeadroom})

	if want := contractExitCodes(t)["failure"]; code != want {
		t.Errorf("analyze with no memory to spare = %d, want %d\nstderr: %s", code, want, stderr.String())
	}
	match := line.FindStringSubmatch(stderr.String())
	if match == nil {
		t.Fatalf("analyze with no memory to spare: stderr = %q, want the one line %v", stderr.String(), line)
	}
	needed, _ := strconv.ParseFloat(match[1], 64)
	available, _ := strconv.ParseFloat(match[2], 64)
	if needed < 1 || needed <= available {
		t.Errorf("analyze with a 0.5 GB live heap and no headroom: needed %v GB, available %v GB; want needed at least 1, twice the live heap, and above available", needed, available)
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Errorf("analyze that ran out of memory left a report at %s (stat: %v), want none", reportPath, err)
	}
}

func TestAStageTheGovernorCancelledEndsWithTheMemoryLine(t *testing.T) {
	line := memoryExhaustionLine(t)
	// The governor cancels the run's context with the memory error as its cause, and
	// the stage running at that moment returns its own wrapping of the cancellation.
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(&memory.ExhaustedError{Needed: 900_000_001, Available: 550_000_000})
	stage := fmt.Errorf("deps: read the module file of /src/app: no diagnostic: %w", ctx.Err())

	var stderr bytes.Buffer
	code := failed(ctx, &stderr, stage)

	if want := contractExitCodes(t)["failure"]; code != want {
		t.Errorf("failed(governor-cancelled context, %q) = %d, want %d", stage, code, want)
	}
	want := "memory exhausted: at least 1 GB were needed, 0.5 GB are available\n"
	if got := stderr.String(); got != want {
		t.Errorf("failed(governor-cancelled context, %q) printed %q, want %q", stage, got, want)
	}
	if !line.MatchString(stderr.String()) {
		t.Errorf("failed(governor-cancelled context, %q) printed %q, want the Contract's line %v", stage, stderr.String(), line)
	}
}

func TestARunWhoseMemoryCannotBeReadIsUngoverned(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	t.Chdir(dir)
	reportPath := filepath.Join(t.TempDir(), "report.json")

	var stdout, stderr bytes.Buffer
	code := governed([]string{"analyze", "--target=.", "--report=" + reportPath}, &stdout, &stderr, nil)
	if want := contractExitCodes(t)["failure"]; code == want {
		t.Errorf("analyze with no memory reader = %d, want a verdict\nstderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Errorf("analyze with no memory reader wrote no report: %v", err)
	}
}
