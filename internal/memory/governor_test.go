package memory

import (
	"context"
	"errors"
	"math"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"testing"
	"time"
)

func TestExhaustedErrorIsTheContractLine(t *testing.T) {
	tests := []struct {
		name      string
		needed    uint64
		available uint64
		want      string
	}{
		{"fraction", 2_345_000_000, 2_000_000_000, "memory exhausted: at least 2.4 GB were needed, 2 GB are available"},
		{"below_one", 999_999_999, 512_000_000, "memory exhausted: at least 1 GB were needed, 0.5 GB are available"},
		{"whole", 16_000_000_000, 15_990_000_000, "memory exhausted: at least 16 GB were needed, 15.9 GB are available"},
		{"exact_tenth", 400_000_000, 300_000_000, "memory exhausted: at least 0.4 GB were needed, 0.3 GB are available"},
		{"one_byte_apart", 400_000_001, 400_000_000, "memory exhausted: at least 0.5 GB were needed, 0.4 GB are available"},
		{"zero", 0, 0, "memory exhausted: at least 0 GB were needed, 0 GB are available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&ExhaustedError{Needed: tt.needed, Available: tt.available}).Error()
			if got != tt.want {
				t.Errorf("ExhaustedError{%d, %d}.Error() = %q, want %q", tt.needed, tt.available, got, tt.want)
			}
		})
	}
}

func TestPercentOfDoesNotOverflow(t *testing.T) {
	got := percentOf(math.MaxUint64, softLimitPercent)
	want := uint64(float64(math.MaxUint64) * 0.85)
	if diff := max(got, want) - min(got, want); diff > want/1000 {
		t.Errorf("percentOf(MaxUint64, 85) = %d, want about %d", got, want)
	}
	if got := percentOf(1000, 85); got != 850 {
		t.Errorf("percentOf(1000, 85) = %d, want 850", got)
	}
}

// keepGCSettings restores the soft limit and the collection target a test changes.
func keepGCSettings(t *testing.T) (limit int64, gogc int) {
	t.Helper()
	limit = debug.SetMemoryLimit(-1)
	gogc = debug.SetGCPercent(defaultGOGC)
	debug.SetGCPercent(gogc)
	t.Cleanup(func() {
		debug.SetMemoryLimit(limit)
		debug.SetGCPercent(gogc)
	})
	return limit, gogc
}

func TestStartSetsNothingWhereNoSourceAnswers(t *testing.T) {
	limit, gogc := keepGCSettings(t)
	silent := func() (uint64, bool) { return 0, false }
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)

	stop := start(ctx, cancel, []Source{silent, silent}, time.Millisecond)
	gotLimit := debug.SetMemoryLimit(-1)
	gotGOGC := debug.SetGCPercent(gogc)
	stop()

	if gotLimit != limit || gotGOGC != gogc {
		t.Errorf("start with no answering source: limit %d and GOGC %d, want %d and %d unchanged", gotLimit, gotGOGC, limit, gogc)
	}
	if ctx.Err() != nil {
		t.Errorf("start with no answering source cancelled the run: %v", context.Cause(ctx))
	}
}

func TestTickRaisesGOGCAndSetsTheLimitUnderHeadroom(t *testing.T) {
	keepGCSettings(t)
	debug.SetGCPercent(defaultGOGC)
	const room = 1 << 40
	g := &governor{sources: []Source{func() (uint64, bool) { return room, true }}, cancel: func(error) {}, samples: newSamples(), gogc: defaultGOGC}

	if g.tick() {
		t.Fatal("tick() with a terabyte of headroom ended the run")
	}
	if got := debug.SetGCPercent(defaultGOGC); got != raisedGOGC {
		t.Errorf("tick() with a terabyte of headroom: GOGC %d, want %d", got, raisedGOGC)
	}
	if got := debug.SetMemoryLimit(-1); got < room*8/10 || got > room {
		t.Errorf("tick() with %d bytes of headroom: limit %d, want 85 %% of the ceiling", uint64(room), got)
	}
}

func TestTickKeepsCollectionOffWhereTheProcessTurnedItOff(t *testing.T) {
	keepGCSettings(t)
	g := &governor{sources: []Source{func() (uint64, bool) { return 1 << 40, true }}, cancel: func(error) {}, samples: newSamples(), previousGOGC: -1, gogc: -1}
	debug.SetGCPercent(-1)
	g.tick()
	if got := debug.SetGCPercent(-1); got != -1 {
		t.Errorf("tick() under GOGC=off: GOGC %d, want -1", got)
	}
}

// sink holds the allocation the low-budget test keeps live.
var sink []byte

func TestTickEndsARunThatDoesNotFitWithTheContractLine(t *testing.T) {
	keepGCSettings(t)
	const held = 256 << 20
	sink = make([]byte, held)
	for i := range sink {
		sink[i] = byte(i)
	}
	t.Cleanup(func() { sink = nil; runtime.GC() })

	var cause error
	g := &governor{
		sources: []Source{func() (uint64, bool) { return 0, true }},
		cancel:  func(err error) { cause = err },
		samples: newSamples(),
		gogc:    defaultGOGC,
	}
	if !g.tick() {
		t.Fatal("tick() with no headroom and a 256 MB live heap did not end the run")
	}
	exhausted, ok := errors.AsType[*ExhaustedError](cause)
	if !ok {
		t.Fatalf("tick() cancelled with %v, want an *ExhaustedError", cause)
	}
	if exhausted.Needed < 2*held || exhausted.Needed <= exhausted.Available {
		t.Errorf("tick() = needed %d, available %d; want needed at least %d, twice the live heap, and above available",
			exhausted.Needed, exhausted.Available, 2*held)
	}
	line := regexp.MustCompile(`^memory exhausted: at least (\d+(?:\.\d)?) GB were needed, (\d+(?:\.\d)?) GB are available$`)
	match := line.FindStringSubmatch(exhausted.Error())
	if match == nil {
		t.Fatalf("tick() line = %q, want %v", exhausted.Error(), line)
	}
	if needed, available := mustFloat(t, match[1]), mustFloat(t, match[2]); needed <= available {
		t.Errorf("tick() line = %q, want N above M", exhausted.Error())
	}
}

// mustFloat is one figure of the memory line.
func mustFloat(t *testing.T, figure string) float64 {
	t.Helper()
	value, err := strconv.ParseFloat(figure, 64)
	if err != nil {
		t.Fatalf("Setup: ParseFloat(%q): %v", figure, err)
	}
	return value
}

func TestBoundByCollectionReadsOneFullWindow(t *testing.T) {
	const procs = 4
	window := 2.0 * procs
	var w cpuWindow
	if w.boundByCollection(0, 0, procs) {
		t.Fatal("boundByCollection on the first reading = true, want false: it only opens the window")
	}
	if w.boundByCollection(window, window-1, procs) {
		t.Error("boundByCollection before a full window = true, want false")
	}
	if !w.boundByCollection(0.6*window, window, procs) {
		t.Error("boundByCollection after a full window at a 60 % collection share = false, want true")
	}
	if w.boundByCollection(0.6*window+0.4*window, 2*window, procs) {
		t.Error("boundByCollection after a full window at a 40 % collection share = true, want false")
	}
}

func TestStopRestoresTheSettingsItFound(t *testing.T) {
	limit, gogc := keepGCSettings(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	stop := start(ctx, cancel, []Source{func() (uint64, bool) { return 1 << 40, true }}, time.Millisecond)
	if got := debug.SetMemoryLimit(-1); got == limit {
		t.Fatalf("start with a terabyte of headroom left the limit at %d, want it set before start returns", got)
	}
	stop()
	if got := debug.SetMemoryLimit(-1); got != limit {
		t.Errorf("after stop the limit is %d, want %d", got, limit)
	}
	if got := debug.SetGCPercent(gogc); got != gogc {
		t.Errorf("after stop GOGC is %d, want %d", got, gogc)
	}
}
