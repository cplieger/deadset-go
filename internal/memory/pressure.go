package memory

import (
	"runtime"
	"sync"
	"sync/atomic"
	"weak"
)

// pressed is the governor's last answer to whether the live heap stands near the
// soft limit. It is process-wide because one governor governs the process.
var pressed atomic.Bool

// droppers is every function [onPressure] registered, by registration.
var droppers struct {
	fns  map[int]func()
	next int
	mu   sync.Mutex
}

// Pressed reports whether the run is under memory pressure: a caller then runs one
// task at a time and keeps no cache it can rebuild, so the run goes slower rather
// than out of memory.
func Pressed() bool { return pressed.Load() }

// onPressure registers drop, which the governor calls each time pressure begins and
// before it judges whether the run fits, to free what the caller can recompute.
// The returned function unregisters it; call it once the cache is gone.
func onPressure(drop func()) (unregister func()) {
	droppers.mu.Lock()
	defer droppers.mu.Unlock()
	if droppers.fns == nil {
		droppers.fns = make(map[int]func())
	}
	id := droppers.next
	droppers.next++
	droppers.fns[id] = drop
	return func() {
		droppers.mu.Lock()
		defer droppers.mu.Unlock()
		delete(droppers.fns, id)
	}
}

// OnPressureOf registers drop for owner as [onPressure] does, holding owner only
// through a weak pointer: the registration keeps no cache alive, and it ends once
// owner is collected.
func OnPressureOf[T any](owner *T, drop func(*T)) {
	held := weak.Make(owner)
	unregister := onPressure(func() {
		if o := held.Value(); o != nil {
			drop(o)
		}
	})
	runtime.AddCleanup(owner, func(unregister func()) { unregister() }, unregister)
}

// shed calls every registered drop function, outside the registry's lock so a drop
// may unregister itself.
func shed() {
	droppers.mu.Lock()
	fns := make([]func(), 0, len(droppers.fns))
	for _, drop := range droppers.fns {
		fns = append(fns, drop)
	}
	droppers.mu.Unlock()
	for _, drop := range fns {
		drop()
	}
}
