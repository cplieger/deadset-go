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

	"github.com/cplieger/deadset-go/v2/internal/memory"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// memoryExhaustionLine is the line this analyzer prints for a run that needs more
// memory than the machine makes available, as a pattern whose two groups are N and
// M. The text after the class is the one the Contract's memory-exhausted
// run failure opens with.
func memoryExhaustionLine(t *testing.T) *regexp.Regexp {
	t.Helper()
	body, err := spec.Contract.ReadFile("contract/exit-codes.json")
	if err != nil {
		t.Fatalf("Setup: read contract/exit-codes.json: %v", err)
	}
	var document struct {
		FailureClasses struct {
			Run struct {
				Classes []struct {
					Class string `json:"class"`
					Fix   string `json:"fix"`
				} `json:"classes"`
			} `json:"run"`
		} `json:"failure_classes"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/exit-codes.json: %v", err)
	}
	var fix string
	for _, class := range document.FailureClasses.Run.Classes {
		if class.Class == "memory-exhausted" {
			fix = class.Fix
		}
	}
	quoted := regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(fix)
	if quoted == nil {
		t.Fatalf("Setup: the memory-exhausted run failure's fix %q quotes no line", fix)
	}
	number := `(\d+(?:\.\d)?)`
	pattern := strings.NewReplacer(" N ", " "+number+" ", " M ", " "+number+" ").Replace(regexp.QuoteMeta(quoted[1]))
	return regexp.MustCompile(`^` + regexp.QuoteMeta(vocab.RunFailurePrefix+vocab.RunMemoryExhausted+": ") + pattern + `; ` + `[^\n]+\n$`)
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
	code := governed([]string{"analyze", "--target=.", "--report=" + reportPath}, &stdout, &stderr, memory.Machine{Sources: []memory.Source{noHeadroom}})

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
	want := vocab.RunFailurePrefix + vocab.RunMemoryExhausted + ": at least 1 GB were needed, 0.5 GB are available; run on a machine with more memory, or narrow the scope\n"
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
	code := governed([]string{"analyze", "--target=.", "--report=" + reportPath}, &stdout, &stderr, memory.Machine{})
	if want := contractExitCodes(t)["failure"]; code == want {
		t.Errorf("analyze with no memory reader = %d, want a verdict\nstderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Errorf("analyze with no memory reader wrote no report: %v", err)
	}
}
