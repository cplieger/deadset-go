// Package memory adapts a run to the memory the machine makes available: it holds
// the runtime's soft memory limit below the ceiling the machine allows, collects
// less often while the heap is far below it, and ends the run through its context
// when the analysis needs more than the machine has.
package memory

import (
	"context"
	"math"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"time"
)

// Source reads how many more bytes this process may take before the bound it reads
// is enforced, and false where that bound does not apply here.
type Source func() (headroom uint64, ok bool)

// The governor's policy, in percent of the ceiling unless named otherwise.
const (
	interval = 50 * time.Millisecond

	softLimitPercent = 85 // the runtime's memory limit
	abortPercent     = 90 // the live heap past which the run cannot fit
	pressurePercent  = 80 // the live heap, of the soft limit, past which a GC-bound window ends the run
	raisedGOGC       = 400
	headroomPercent  = 70 // the projected heap, of the soft limit, under which GOGC stays raised
	limiterShare     = 0.5
	bytesPerTenthGB  = 100_000_000
	defaultGOGC      = 100
	cpuWindowFactor  = 2 // the GC CPU limiter's window, in CPU-seconds per GOMAXPROCS
)

// ExhaustedError ends a run that needs more memory than the machine makes
// available: Needed is the heap the analysis was about to need, the goal of the
// next collection cycle at the default collection target, and Available the
// ceiling the machine allowed it. The governor only builds one where Needed
// exceeds Available.
type ExhaustedError struct {
	Needed    uint64
	Available uint64
}

// Error is the line a run that ends this way prints. Needed is rounded up and
// Available down, so the line never reads as if the run fitted.
func (e *ExhaustedError) Error() string {
	needed := e.Needed/bytesPerTenthGB + min(e.Needed%bytesPerTenthGB, 1)
	return "memory exhausted: at least " + gigabytes(needed) + " GB were needed, " +
		gigabytes(e.Available/bytesPerTenthGB) + " GB are available"
}

// gigabytes spells a count of tenths of a decimal gigabyte with at most one digit
// after the point.
func gigabytes(tenths uint64) string {
	return strconv.FormatFloat(float64(tenths)/10, 'f', -1, 64)
}

// Start governs this process's memory for the life of ctx with sources, read in
// order, and ends the run by calling cancel with an *[ExhaustedError] when it cannot
// fit. The first reading is applied before Start returns, so a run that cannot fit
// is cancelled before it starts. Where no source answers that reading the governor
// sets nothing at all, and a machine whose memory cannot be read runs as it would
// without it. The returned function stops the governor and restores the limit and
// the collection target it found; call it before the process exits.
func Start(ctx context.Context, cancel context.CancelCauseFunc, sources []Source) (stop func()) {
	return start(ctx, cancel, sources, interval)
}

func start(ctx context.Context, cancel context.CancelCauseFunc, sources []Source, every time.Duration) (stop func()) {
	g := &governor{sources: sources, cancel: cancel, samples: newSamples()}
	if _, read := g.ceiling(); !read {
		return func() {}
	}
	// A negative limit reads the current one without changing it; the collection
	// target has no such read, so it is set to the default and put back.
	g.previousLimit = debug.SetMemoryLimit(-1)
	g.previousGOGC = debug.SetGCPercent(defaultGOGC)
	debug.SetGCPercent(g.previousGOGC)
	g.gogc = g.previousGOGC
	restore := func() {
		debug.SetMemoryLimit(g.previousLimit)
		debug.SetGCPercent(g.previousGOGC)
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

// governor is one run's policy state.
type governor struct {
	cancel        context.CancelCauseFunc
	sources       []Source
	samples       []metrics.Sample
	previousLimit int64
	previousGOGC  int
	gogc          int
	window        cpuWindow
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
// least headroom any source reports, and false where no source answers.
func (g *governor) ceiling() (uint64, bool) {
	_, held := g.read()
	var least uint64
	answered := false
	for _, source := range g.sources {
		if room, ok := source(); ok && (!answered || room < least) {
			least, answered = room, true
		}
	}
	return held + least, answered
}

// tick applies one reading and reports whether the run was ended.
func (g *governor) tick() bool {
	ceiling, read := g.ceiling()
	if !read {
		return false
	}
	soft := percentOf(ceiling, softLimitPercent)
	debug.SetMemoryLimit(int64(min(soft, math.MaxInt64)))

	live, _ := g.read()
	if live > percentOf(ceiling, abortPercent) {
		runtime.GC()
		if live, _ = g.read(); live > percentOf(ceiling, abortPercent) {
			g.cancel(exhausted(live, ceiling))
			return true
		}
	}
	gc, total := g.samples[metricGCCPU].Value.Float64(), g.samples[metricAllCPU].Value.Float64()
	if g.window.boundByCollection(gc, total, runtime.GOMAXPROCS(0)) && live > percentOf(soft, pressurePercent) {
		g.cancel(exhausted(live, ceiling))
		return true
	}

	want := defaultGOGC
	if live/100*(100+raisedGOGC) < percentOf(soft, headroomPercent) {
		want = raisedGOGC
	}
	if g.previousGOGC < 0 {
		want = g.previousGOGC
	}
	if want != g.gogc {
		debug.SetGCPercent(want)
		g.gogc = want
	}
	return false
}

// exhausted is the error ending a run whose live heap does not fit under ceiling.
// Needed is the heap goal the default target sets on that live heap. Both abort
// rules fire with live above half the ceiling, so Needed is above the ceiling.
func exhausted(live, ceiling uint64) *ExhaustedError {
	return &ExhaustedError{Needed: live + percentOf(live, defaultGOGC), Available: ceiling}
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
