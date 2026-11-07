// Package memory adapts a run to the memory the machine makes available: it holds
// the runtime's soft memory limit below the ceiling the machine allows, puts the run
// under pressure near the limit ([Pressed], [OnPressureOf]), and ends the run
// through its context when even the run under pressure needs more than the machine
// has.
package memory

import (
	"context"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"time"
)

// Source reads how many more bytes this process may take before the bound it reads
// is enforced, and false where that bound does not apply here.
type Source func() (headroom uint64, ok bool)

// Machine is what the governor reads of the machine a run is on.
type Machine struct {
	// Limit is the memory the process may take at most: the limit of the cgroup or
	// job holding it, else the machine's physical memory, and false where neither
	// reads. Nil reads nothing.
	Limit func() (uint64, bool)
	// Sources are read on every reading, in order.
	Sources []Source
}

// The governor's policy, in percent of the ceiling unless named otherwise.
const (
	interval = 50 * time.Millisecond

	softLimitPercent = 85 // the runtime's memory limit
	defaultPercent   = 70 // of the machine's limit, the soft limit a run without GOMEMLIMIT holds to outside pressure
	abortPercent     = 90 // the live heap, or under a GC-bound window the memory held, past which the run cannot fit
	pressurePercent  = 80 // the live heap, of the soft limit, past which the run is under pressure
	relievedPercent  = 50 // the live heap with what pressure dropped, of the soft limit outside pressure, under which pressure ends
	limiterShare     = 0.5
	bytesPerTenthGB  = 100_000_000
	cpuWindowFactor  = 2 // the GC CPU limiter's window, in CPU-seconds per GOMAXPROCS
)

// ExhaustedError ends a run that needs more memory than the machine makes
// available: Needed is the least memory under which what the run used under
// pressure would fit, and Available the ceiling the machine allowed it. The
// governor only builds one where Needed exceeds Available ([exhausted]).
type ExhaustedError struct {
	Needed    uint64
	Available uint64
}

// Error is the line a run that ends this way prints. Needed is rounded up and
// Available down, so the line never reads as if the run fitted.
func (e *ExhaustedError) Error() string {
	needed := e.Needed/bytesPerTenthGB + min(e.Needed%bytesPerTenthGB, 1)
	return "at least " + gigabytes(needed) + " GB were needed, " +
		gigabytes(e.Available/bytesPerTenthGB) + " GB are available"
}

// gigabytes spells a count of tenths of a decimal gigabyte with at most one digit
// after the point.
func gigabytes(tenths uint64) string {
	return strconv.FormatFloat(float64(tenths)/10, 'f', -1, 64)
}

// Start governs this process's memory for the life of ctx with machine's readers,
// and ends the run by calling cancel with an *[ExhaustedError] when it cannot fit.
// GOMEMLIMIT bounds the ceiling as a source does; without it the soft limit stays
// at most 70 % of the machine's limit outside pressure. It leaves the collection
// target (GOGC) as the process set it. Where nothing reads it sets nothing. The
// returned function stops the governor and restores the limit it found; call it
// before exiting.
func Start(ctx context.Context, cancel context.CancelCauseFunc, machine Machine) (stop func()) {
	return start(ctx, cancel, machine, interval)
}

func start(ctx context.Context, cancel context.CancelCauseFunc, machine Machine, every time.Duration) (stop func()) {
	// A negative limit reads the current one without changing it.
	g := &governor{sources: machine.Sources, cancel: cancel, samples: newSamples(), previousLimit: debug.SetMemoryLimit(-1)}
	if g.previousLimit > 0 && g.previousLimit < math.MaxInt64 {
		g.callerLimit = uint64(g.previousLimit)
	}
	if g.callerLimit == 0 {
		g.machineLimit = defaultBound(machine)
	}
	if _, read := g.ceiling(); !read {
		return func() {}
	}
	restore := func() {
		debug.SetMemoryLimit(g.previousLimit)
		pressed.Store(false)
	}
	if g.tick() {
		return restore
	}

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
			}
			if g.tick() {
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		restore()
	}
}

// defaultBound is the machine's limit, where the process started with no GOMEMLIMIT,
// and 0 otherwise: GOMEMLIMIT=off reads as no limit and is still the caller's choice.
// An empty GOMEMLIMIT is unset, as the runtime reads it
// (https://pkg.go.dev/runtime#hdr-Environment_Variables).
func defaultBound(machine Machine) uint64 {
	if os.Getenv("GOMEMLIMIT") != "" || machine.Limit == nil {
		return 0
	}
	if limit, read := machine.Limit(); read {
		return limit
	}
	return 0
}

// governor is one run's policy state.
type governor struct {
	cancel        context.CancelCauseFunc
	sources       []Source
	samples       []metrics.Sample
	previousLimit int64
	callerLimit   uint64 // the memory limit the process started with, 0 where none was set
	machineLimit  uint64 // the machine's limit where the process started with none, else 0
	// regain is what the drops freed when the current pressure began, which the
	// dropped caches take again once pressure ends: pressure ends only where the
	// live heap and regain together sit under the relief line, so a run that fits
	// only without its caches stays under pressure rather than refilling them.
	regain uint64
	window cpuWindow
}

// cpuWindow is the GC and total CPU time read at the start of the current window.
type cpuWindow struct {
	gc, total float64
	open      bool
}

// The runtime metrics the governor reads, in the order samples holds them.
const (
	metricLive = iota
	metricTotal
	metricReleased
	metricGCCPU
	metricAllCPU
)

func newSamples() []metrics.Sample {
	return []metrics.Sample{
		{Name: "/gc/heap/live:bytes"},
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/cpu/classes/total:cpu-seconds"},
	}
}

// read refreshes the samples and returns the live heap and what the runtime holds
// from the operating system.
func (g *governor) read() (live, held uint64) {
	metrics.Read(g.samples)
	live = g.samples[metricLive].Value.Uint64()
	total, released := g.samples[metricTotal].Value.Uint64(), g.samples[metricReleased].Value.Uint64()
	return live, total - min(released, total)
}

// ceiling is the most memory this process may hold: what it holds now plus the
// least headroom any source reports, and false where nothing answers. A memory
// limit the process started with (GOMEMLIMIT), or else the machine's limit, caps
// it, so a run never holds more than the limit its caller set, even once the
// runtime already holds more.
func (g *governor) ceiling() (uint64, bool) {
	_, held := g.read()
	var least uint64
	answered := false
	for _, source := range g.sources {
		if room, ok := source(); ok && (!answered || room < least) {
			least, answered = room, true
		}
	}
	ceiling := held + least
	if bound := max(g.callerLimit, g.machineLimit); bound > 0 && (!answered || bound < ceiling) {
		ceiling, answered = bound, true
	}
	return ceiling, answered
}

// tick applies one reading and reports whether the run was ended. A run is judged
// not to fit only on a reading taken under a pressure that began on an earlier
// tick, so every [onPressure] function dropped what it holds, a collection ran, and
// the run has since gone on in its minimal shape.
func (g *governor) tick() bool {
	ceiling, read := g.ceiling()
	if !read {
		return false
	}
	capped := percentOf(ceiling, softLimitPercent)
	if g.machineLimit > 0 {
		capped = min(capped, percentOf(g.machineLimit, defaultPercent))
	}
	live, held := g.read()
	judged := pressed.Load()
	switch {
	case !judged && live > percentOf(capped, pressurePercent):
		pressed.Store(true)
		// The live heap read is of the last collection, so one runs first: regain
		// then counts what the drops freed of what was allocated since.
		runtime.GC()
		before, _ := g.read()
		shed()
		runtime.GC()
		live, held = g.read()
		g.regain = before - min(live, before)
	case judged && live+g.regain < percentOf(capped, relievedPercent):
		pressed.Store(false)
		judged = false
	}
	// Under pressure the collector may use the whole share of the ceiling before the
	// run is judged not to fit, so the 70 % cap no longer applies.
	soft := capped
	if pressed.Load() {
		soft = percentOf(ceiling, softLimitPercent)
	}
	debug.SetMemoryLimit(int64(min(soft, math.MaxInt64)))

	gc, total := g.samples[metricGCCPU].Value.Float64(), g.samples[metricAllCPU].Value.Float64()
	bound := g.window.boundByCollection(gc, total, runtime.GOMAXPROCS(0))
	if !judged {
		return false
	}
	if live > percentOf(ceiling, abortPercent) {
		runtime.GC()
		if live, _ = g.read(); live > percentOf(ceiling, abortPercent) {
			g.cancel(exhausted(live, ceiling))
			return true
		}
	}
	// A collector bound by its CPU limiter lets the heap outgrow the soft limit, so
	// such a run is judged by what it holds: slow while that stays under the
	// ceiling, unable to fit once it nears it.
	if bound && held > percentOf(ceiling, abortPercent) {
		g.cancel(exhausted(held, ceiling))
		return true
	}
	return false
}

// exhausted is the error ending a run that needs more than ceiling, used being the
// memory whose reading passed ceiling's abort line, as the caller ensures. Needed is
// the least ceiling under which that reading would sit at the abort line, so it is
// above ceiling.
func exhausted(used, ceiling uint64) *ExhaustedError {
	return &ExhaustedError{Needed: used/abortPercent*100 + (used%abortPercent*100+abortPercent-1)/abortPercent, Available: ceiling}
}

// boundByCollection reports, once the CPU time spent since the window opened fills
// one window of the collector's CPU limiter, whether collection took more than the
// limiter's share of it, and opens the next window. Paired with a live heap pressed
// against the soft limit, that is a run thrashing below a ceiling it will not fit
// under.
func (w *cpuWindow) boundByCollection(gc, total float64, procs int) bool {
	if !w.open {
		*w = cpuWindow{gc: gc, total: total, open: true}
		return false
	}
	spent := total - w.total
	if spent < cpuWindowFactor*float64(procs) {
		return false
	}
	share := (gc - w.gc) / spent
	*w = cpuWindow{gc: gc, total: total, open: true}
	return share > limiterShare
}

// percentOf is percent of value, divided first so no product overflows.
func percentOf(value, percent uint64) uint64 {
	return value/100*percent + value%100*percent/100
}
