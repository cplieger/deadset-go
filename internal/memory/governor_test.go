package memory

import (
	"context"
	"errors"
	"math"
	"os"
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
		{"fraction", 2_345_000_000, 2_000_000_000, "at least 2.4 GB were needed, 2 GB are available"},
		{"below_one", 999_999_999, 512_000_000, "at least 1 GB were needed, 0.5 GB are available"},
		{"whole", 16_000_000_000, 15_990_000_000, "at least 16 GB were needed, 15.9 GB are available"},
		{"exact_tenth", 400_000_000, 300_000_000, "at least 0.4 GB were needed, 0.3 GB are available"},
		{"one_byte_apart", 400_000_001, 400_000_000, "at least 0.5 GB were needed, 0.4 GB are available"},
		{"zero", 0, 0, "at least 0 GB were needed, 0 GB are available"},
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
	gogc = debug.SetGCPercent(100)
	debug.SetGCPercent(gogc)
	t.Cleanup(func() {
		debug.SetMemoryLimit(limit)
		debug.SetGCPercent(gogc)
		pressed.Store(false)
	})
	return limit, gogc
}

func TestStartSetsNothingWhereNoSourceAnswers(t *testing.T) {
	_, gogc := keepGCSettings(t)
	// A GOMEMLIMIT in the test's environment is a caller limit, which answers on its own.
	limit := int64(math.MaxInt64)
	debug.SetMemoryLimit(limit)
	silent := func() (uint64, bool) { return 0, false }
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)

	stop := start(ctx, cancel, Machine{Sources: []Source{silent, silent}}, time.Millisecond)
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

// The governor bounds the heap through the soft limit alone: a collection target
// raised while memory is free would hold garbage the run does not need.
func TestTickSetsTheLimitAndLeavesGOGC(t *testing.T) {
	keepGCSettings(t)
	const gogc = 150
	debug.SetGCPercent(gogc)
	const room = 1 << 40
	g := &governor{sources: []Source{func() (uint64, bool) { return room, true }}, cancel: func(error) {}, samples: newSamples()}

	if g.tick() {
		t.Fatal("tick() with a terabyte of headroom ended the run")
	}
	if got := debug.SetGCPercent(gogc); got != gogc {
		t.Errorf("tick() with a terabyte of headroom: GOGC %d, want %d, as the process set it", got, gogc)
	}
	if got := debug.SetMemoryLimit(-1); got < room*8/10 || got > room {
		t.Errorf("tick() with %d bytes of headroom: limit %d, want 85 %% of the ceiling", uint64(room), got)
	}
}

// withoutGOMEMLIMIT runs a test as a process started with no GOMEMLIMIT.
func withoutGOMEMLIMIT(t *testing.T) {
	t.Helper()
	t.Setenv("GOMEMLIMIT", "")
	if err := os.Unsetenv("GOMEMLIMIT"); err != nil {
		t.Fatalf("Setup: unset GOMEMLIMIT: %v", err)
	}
	debug.SetMemoryLimit(math.MaxInt64)
}

// startedLimit is the soft limit start sets on machine, read before stop.
func startedLimit(t *testing.T, machine Machine) int64 {
	t.Helper()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	stop := start(ctx, cancel, machine, time.Hour)
	got := debug.SetMemoryLimit(-1)
	stop()
	if ctx.Err() != nil {
		t.Errorf("start cancelled the run: %v", context.Cause(ctx))
	}
	return got
}

func TestStartHoldsARunWithoutGOMEMLIMITToSeventyPercentOfTheMachine(t *testing.T) {
	keepGCSettings(t)
	withoutGOMEMLIMIT(t)
	const machineLimit = 10 << 30
	roomy := func() (uint64, bool) { return 1 << 40, true }
	got := startedLimit(t, Machine{Sources: []Source{roomy}, Limit: func() (uint64, bool) { return machineLimit, true }})
	if want := int64(percentOf(machineLimit, defaultPercent)); got != want {
		t.Errorf("start without GOMEMLIMIT on a 10 GiB machine with a terabyte of headroom set the limit to %d, want %d, 70 %% of the machine", got, want)
	}
}

func TestStartBoundsARunWhereOnlyTheMachineLimitReads(t *testing.T) {
	keepGCSettings(t)
	withoutGOMEMLIMIT(t)
	const machineLimit = 10 << 30
	silent := func() (uint64, bool) { return 0, false }
	got := startedLimit(t, Machine{Sources: []Source{silent}, Limit: func() (uint64, bool) { return machineLimit, true }})
	if want := int64(percentOf(machineLimit, defaultPercent)); got != want {
		t.Errorf("start without GOMEMLIMIT where only the 10 GiB machine limit reads set the limit to %d, want %d", got, want)
	}
}

func TestStartBoundsARunWhoseGOMEMLIMITIsEmpty(t *testing.T) {
	keepGCSettings(t)
	withoutGOMEMLIMIT(t)
	t.Setenv("GOMEMLIMIT", "")
	const machineLimit = 10 << 30
	roomy := func() (uint64, bool) { return 1 << 40, true }
	got := startedLimit(t, Machine{Sources: []Source{roomy}, Limit: func() (uint64, bool) { return machineLimit, true }})
	if want := int64(percentOf(machineLimit, defaultPercent)); got != want {
		t.Errorf("start under an empty GOMEMLIMIT on a 10 GiB machine set the limit to %d, want %d, as with none", got, want)
	}
}

func TestStartLeavesTheBoundToAnExplicitGOMEMLIMIT(t *testing.T) {
	const machineLimit = 10 << 30
	roomy := func() (uint64, bool) { return 1 << 40, true }
	machine := Machine{Sources: []Source{roomy}, Limit: func() (uint64, bool) { return machineLimit, true }}
	tests := []struct {
		name    string
		env     string
		limit   int64
		atLeast int64
	}{
		{"above_the_machine", "20GiB", 20 << 30, (20 << 30) * 8 / 10},
		{"off", "off", math.MaxInt64, 1 << 39},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGCSettings(t)
			t.Setenv("GOMEMLIMIT", tt.env)
			debug.SetMemoryLimit(tt.limit)
			if got := startedLimit(t, machine); got < tt.atLeast || got <= machineLimit {
				t.Errorf("start under GOMEMLIMIT=%s on a 10 GiB machine set the limit to %d, want at least %d: the caller's limit wins", tt.env, got, tt.atLeast)
			}
		})
	}
}

func TestStartKeepsTheLimitTheProcessStartedWith(t *testing.T) {
	keepGCSettings(t)
	const callerLimit = 4 << 30
	debug.SetMemoryLimit(callerLimit)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)

	stop := start(ctx, cancel, Machine{Sources: []Source{func() (uint64, bool) { return 1 << 40, true }}}, time.Hour)
	got := debug.SetMemoryLimit(-1)
	stop()

	if got > callerLimit {
		t.Errorf("start under a %d-byte memory limit with a terabyte of headroom set the limit to %d, want at most %d", callerLimit, got, callerLimit)
	}
	if got < callerLimit*8/10 {
		t.Errorf("start under a %d-byte memory limit set the limit to %d, want 85 %% of it", callerLimit, got)
	}
	if ctx.Err() != nil {
		t.Errorf("start under a %d-byte memory limit cancelled the run: %v", callerLimit, context.Cause(ctx))
	}
}

func TestTickKeepsCollectionOffWhereTheProcessTurnedItOff(t *testing.T) {
	keepGCSettings(t)
	g := &governor{sources: []Source{func() (uint64, bool) { return 1 << 40, true }}, cancel: func(error) {}, samples: newSamples()}
	debug.SetGCPercent(-1)
	g.tick()
	if got := debug.SetGCPercent(-1); got != -1 {
		t.Errorf("tick() under GOGC=off: GOGC %d, want -1", got)
	}
}

// sink holds the allocation the low-budget tests keep live, and lateSink one made
// after the last collection.
var sink, lateSink []byte

// heldTight allocates sink and returns a caller limit 80 % of the live heap with
// it: the ceiling the governor reads is then that limit, whatever the runtime
// holds from earlier tests, so pressure begins on the first reading and the live
// heap sits past the abort line while sink is held.
func heldTight(t *testing.T) uint64 {
	t.Helper()
	sink = make([]byte, 256<<20)
	for i := range sink {
		sink[i] = byte(i)
	}
	t.Cleanup(func() { sink = nil; runtime.GC() })
	runtime.GC()
	live, _ := (&governor{samples: newSamples()}).read()
	return live / 10 * 8
}

func TestTickEndsARunThatDoesNotFitWithTheContractLine(t *testing.T) {
	keepGCSettings(t)
	const held = 256 << 20
	limit := heldTight(t)
	dropped := 0
	unregister := onPressure(func() { dropped++ })
	defer unregister()

	var cause error
	g := &governor{
		sources:     []Source{func() (uint64, bool) { return 0, true }},
		cancel:      func(err error) { cause = err },
		samples:     newSamples(),
		callerLimit: limit,
	}
	if g.tick() || cause != nil {
		t.Fatalf("tick() ended the run on the reading that put it under pressure (%v), want it judged once it ran under pressure", cause)
	}
	if dropped != 1 || !Pressed() {
		t.Fatalf("tick() with no headroom: dropped %d times, Pressed() = %v; want 1 and true", dropped, Pressed())
	}
	if !g.tick() {
		t.Fatal("tick() under pressure with no headroom and a 256 MB live heap the drop kept did not end the run")
	}
	exhausted, ok := errors.AsType[*ExhaustedError](cause)
	if !ok {
		t.Fatalf("tick() cancelled with %v, want an *ExhaustedError", cause)
	}
	if exhausted.Needed < held*100/abortPercent || exhausted.Needed <= exhausted.Available {
		t.Errorf("tick() = needed %d, available %d; want needed at least %d, the least ceiling the live heap fits under, and above available",
			exhausted.Needed, exhausted.Available, held*100/abortPercent)
	}
	line := regexp.MustCompile(`^at least (\d+(?:\.\d)?) GB were needed, (\d+(?:\.\d)?) GB are available$`)
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
	stop := start(ctx, cancel, Machine{Sources: []Source{func() (uint64, bool) { return 1 << 40, true }}}, time.Millisecond)
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

func TestCeilingIsTheCallerLimitOnceTheRuntimeHoldsMore(t *testing.T) {
	g := &governor{samples: newSamples(), callerLimit: 1 << 20}
	if _, held := g.read(); held <= g.callerLimit {
		t.Skipf("Setup: the runtime holds %d bytes, not above the %d-byte limit", held, g.callerLimit)
	}
	if ceiling, read := g.ceiling(); !read || ceiling != g.callerLimit {
		t.Errorf("ceiling() under a 1 MiB limit = %d, %t, want %d, true", ceiling, read, g.callerLimit)
	}
}

// A run whose live heap nears the limit is put under pressure: what it registered
// to drop is dropped and collected before the run is judged, so a heap that fits
// once its caches go ends nothing, and the run stays under pressure.
func TestTickUnderPressureDropsCachesBeforeJudgingTheRun(t *testing.T) {
	keepGCSettings(t)
	limit := heldTight(t)
	dropped := 0
	unregister := onPressure(func() { dropped++; sink = nil })
	defer unregister()

	ended := false
	var room uint64
	g := &governor{
		sources:     []Source{func() (uint64, bool) { return room, true }},
		cancel:      func(error) { ended = true },
		samples:     newSamples(),
		callerLimit: limit,
	}
	if g.tick() || ended {
		t.Fatal("tick() ended a run whose heap fits once its cache is dropped")
	}
	if dropped != 1 || !Pressed() {
		t.Errorf("tick() with a 256 MB cache and no headroom: dropped %d times, Pressed() = %v; want 1 and true", dropped, Pressed())
	}
	if g.tick() || ended {
		t.Error("a second tick under pressure ended a run whose heap fits without its cache")
	}
	if dropped != 1 {
		t.Errorf("a second tick under pressure dropped again (%d drops), want the drop once per pressure", dropped)
	}
	if !Pressed() {
		t.Error("the heap the drop emptied ended pressure, want the run kept under pressure while its cache would not fit again")
	}
	room, g.callerLimit = 1<<40, 0
	if g.tick(); Pressed() {
		t.Error("tick() with a terabyte of headroom kept the run under pressure, want pressure ended once the cache fits again")
	}
}

// What pressure keeps off counts what the drops freed of memory allocated since the
// last collection, which that collection's live heap does not hold.
func TestTickCountsWhatTheDropFreedSinceTheLastCollection(t *testing.T) {
	keepGCSettings(t)
	limit := heldTight(t)
	const late = 128 << 20
	lateSink = make([]byte, late)
	for i := range lateSink {
		lateSink[i] = byte(i)
	}
	unregister := onPressure(func() { sink, lateSink = nil, nil })
	defer unregister()

	g := &governor{
		sources:     []Source{func() (uint64, bool) { return 0, true }},
		cancel:      func(error) {},
		samples:     newSamples(),
		callerLimit: limit,
	}
	g.tick()
	if !Pressed() || g.regain < 256<<20+late*9/10 {
		t.Errorf("tick() dropping a 256 MB cache and a %d MB one allocated since the last collection: Pressed() = %t, regain %d MB; want true and at least %d MB",
			late>>20, Pressed(), g.regain>>20, (256<<20+late*9/10)>>20)
	}
}

// A collector bound by its CPU limiter ends only a run already under pressure: a
// run that never shed its caches has not been tried in its minimal shape.
func TestTickJudgesACollectionBoundRunOnlyUnderPressure(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pressed bool
	}{{"relieved", false}, {"pressed", true}} {
		t.Run(tt.name, func(t *testing.T) {
			keepGCSettings(t)
			pressed.Store(tt.pressed)
			var cause error
			g := &governor{
				sources: []Source{func() (uint64, bool) { return 0, true }},
				cancel:  func(err error) { cause = err },
				samples: newSamples(),
				// A window opened a billion CPU-seconds ago, all of them collection's.
				window: cpuWindow{gc: -1e9, total: -1e9, open: true},
				regain: math.MaxUint64 / 2,
			}
			ended := g.tick()
			if tt.pressed {
				if _, ok := errors.AsType[*ExhaustedError](cause); !ended || !ok {
					t.Errorf("tick() under pressure in a collection-bound window holding the whole ceiling = %t, %v; want the run ended with an *ExhaustedError", ended, cause)
				}
				return
			}
			if ended || cause != nil {
				t.Errorf("tick() outside pressure in a collection-bound window holding the whole ceiling ended the run (%v), want it put under pressure first", cause)
			}
		})
	}
}

// Pressure ends once the live heap falls well under the soft limit.
func TestTickEndsPressureOnceTheHeapFalls(t *testing.T) {
	keepGCSettings(t)
	pressed.Store(true)
	g := &governor{sources: []Source{func() (uint64, bool) { return 1 << 40, true }}, cancel: func(error) {}, samples: newSamples()}
	if g.tick() || Pressed() {
		t.Errorf("tick() with a terabyte of headroom: Pressed() = %v, want false", Pressed())
	}
}

// An unregistered drop function is never called.
func TestOnPressureUnregisterRemovesTheDrop(t *testing.T) {
	called := false
	onPressure(func() { called = true })()
	shed()
	if called {
		t.Error("shed() called a drop function after its unregister")
	}
}

// Needed is the least ceiling under which the memory that passed the abort line
// sits at it, rounded up, so it always exceeds what was available.
func TestExhaustedNeedsTheLeastCeilingTheReadingFits(t *testing.T) {
	for _, used := range []uint64{900, 901, 1_000_000_007, math.MaxUint64 / 2} {
		ceiling := percentOf(used, abortPercent)
		got := exhausted(used, ceiling)
		if got.Needed < used || percentOf(got.Needed, abortPercent) < ceiling {
			t.Errorf("exhausted(%d).Needed = %d, want the least ceiling %d sits at %d %% of", used, got.Needed, used, abortPercent)
		}
		if got.Needed <= ceiling {
			t.Errorf("exhausted(%d, %d).Needed = %d, want above the ceiling", used, ceiling, got.Needed)
		}
	}
	// A reading just past the abort line of a ceiling, which is what every caller
	// passes, needs more than that ceiling.
	for _, tt := range []struct{ used, ceiling uint64 }{{910, 1000}, {901, 1000}, {9_000_000_001, 10_000_000_000}} {
		if got := exhausted(tt.used, tt.ceiling); got.Needed <= tt.ceiling {
			t.Errorf("exhausted(%d, %d).Needed = %d, want above the ceiling", tt.used, tt.ceiling, got.Needed)
		}
	}
}
