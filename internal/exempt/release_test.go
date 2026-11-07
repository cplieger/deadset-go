package exempt

import (
	"context"
	"go/types"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"testing"
	"weak"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/memory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The source a program reads of the packages outside it is the largest allocation
// of a run. Once the classes ran, neither what they computed nor the configuration's
// load keeps any of it reachable, so it goes with the program.
func TestComputeLeavesNoSourceTheProgramReadReachable(t *testing.T) {
	in := inputOf(t, "encoding-reflection-relayed-outside.txtar", Options{})
	var read []weak.Pointer[load.Source]
	for _, pkg := range in.outside {
		if source, ok := in.sources.Read(pkg); ok {
			read = append(read, weak.Make(source))
		}
	}
	if len(read) == 0 {
		t.Fatalf("Setup: no package outside the program read from source")
	}
	loaded := in.Loaded
	computed, err := Compute(in, Detectors)
	if err != nil {
		t.Fatalf("Compute() = _, %v", err)
	}
	runtime.GC()
	for i, source := range read {
		if source.Value() != nil {
			t.Errorf("source %d of %d is still reachable once the program is dropped", i+1, len(read))
		}
	}
	runtime.KeepAlive(loaded)
	runtime.KeepAlive(computed)
}

// underPressure puts the process under memory pressure through the governor, for
// the rest of the test: a governor whose machine allows one byte presses on its
// first reading and ends its run on the next, and pressure stays until it stops.
func underPressure(t *testing.T) {
	t.Helper()
	t.Setenv("GOMEMLIMIT", "")
	if err := os.Unsetenv("GOMEMLIMIT"); err != nil {
		t.Fatalf("Setup: unset GOMEMLIMIT: %v", err)
	}
	limit := debug.SetMemoryLimit(math.MaxInt64)
	ctx, cancel := context.WithCancelCause(t.Context())
	stop := memory.Start(ctx, cancel, memory.Machine{Limit: func() (uint64, bool) { return 1, true }})
	<-ctx.Done()
	debug.SetMemoryLimit(math.MaxInt64)
	t.Cleanup(func() { stop(); debug.SetMemoryLimit(limit) })
	if !memory.Pressed() {
		t.Fatal("Setup: the governor did not put the run under pressure")
	}
}

// readBodies is the type information of the first function of each package outside
// the program whose body the decoder flow reads from source, at most two.
func readBodies(t *testing.T, in *Program, d *decoders) []*types.Package {
	t.Helper()
	var found []*types.Package
	for _, pkg := range in.outside {
		for _, name := range pkg.Scope().Names() {
			if fn, isFunc := pkg.Scope().Lookup(name).(*types.Func); isFunc {
				if decl, info := d.body(fn); decl != nil && info != nil {
					found = append(found, pkg)
					break
				}
			}
		}
		if len(found) == 2 {
			return found
		}
	}
	t.Fatalf("Setup: %d packages outside the program with a body read from source, want 2", len(found))
	return nil
}

// bodyInfo is the type information the decoder flow reads one package's first
// function body from.
func bodyInfo(d *decoders, pkg *types.Package) weak.Pointer[types.Info] {
	for _, name := range pkg.Scope().Names() {
		if fn, isFunc := pkg.Scope().Lookup(name).(*types.Func); isFunc {
			if decl, info := d.body(fn); decl != nil && info != nil {
				return weak.Make(info)
			}
		}
	}
	return weak.Pointer[types.Info]{}
}

// The decoder flow reads function bodies of packages outside the program from
// their source. Pressure drops every reading it holds, and under pressure it holds
// only the last, while the program and the flow stay alive.
func TestDecodersKeepNoReadingUnderPressure(t *testing.T) {
	in := inputOf(t, "encoding-reflection-relayed-outside.txtar", Options{})
	d := newDecoders(&encodingFlow{kept: newRetention(in, vocab.EncodingReflection)})
	pkgs := readBodies(t, in, d)

	before := bodyInfo(d, pkgs[0])
	underPressure(t)
	runtime.GC()
	if before.Value() != nil {
		t.Error("a reading the decoder flow held when pressure began is still reachable")
	}

	first := bodyInfo(d, pkgs[0])
	bodyInfo(d, pkgs[1])
	runtime.GC()
	if first.Value() != nil {
		t.Error("under pressure a reading the decoder flow read before the last is still reachable")
	}
	runtime.KeepAlive(d)
	runtime.KeepAlive(in)
}

// The decoder flow answers for functions of the loaded program only, so asking
// about every parameter of every function outside the program keys no answer by a
// reading's function, and pressure leaves no reading reachable through the answers.
func TestDecoderAnswersNameNoReading(t *testing.T) {
	in := inputOf(t, "encoding-reflection-relayed-outside.txtar", Options{})
	d := newDecoders(&encodingFlow{kept: newRetention(in, vocab.EncodingReflection)})
	loaded := in.Result.TypesIDs()
	var readings []weak.Pointer[types.Package]
	for _, pkg := range in.outside {
		source, ok := in.sources.Read(pkg)
		if !ok {
			continue
		}
		for _, obj := range source.Info.Defs {
			if obj != nil && obj.Pkg() != nil && obj.Pkg() != pkg {
				readings = append(readings, weak.Make(obj.Pkg()))
				break
			}
		}
	}
	asked := 0
	for _, pkg := range in.outside {
		for _, fn := range scopeFuncs(pkg) {
			for i := range fn.Signature().Params().Len() {
				d.at(fn, i)
				asked++
			}
		}
	}
	if len(readings) == 0 || len(d.answered) == 0 {
		t.Fatalf("Setup: %d readings, %d parameters asked, %d answers", len(readings), asked, len(d.answered))
	}
	ofReadings := 0
	for fn := range d.answered {
		if _, held := loaded[fn.Pkg()]; fn.Pkg() != nil && !held {
			ofReadings++
		}
	}
	if ofReadings > 0 {
		t.Errorf("%d of %d answers are keyed by a function of a reading, want each keyed by the loaded function", ofReadings, len(d.answered))
	}
	underPressure(t)
	runtime.GC()
	kept := 0
	for _, reading := range readings {
		if reading.Value() != nil {
			kept++
		}
	}
	if kept > 0 {
		t.Errorf("%d of %d readings are still reachable through the decoder's answers under pressure", kept, len(readings))
	}
	runtime.KeepAlive(d)
	runtime.KeepAlive(in)
}

// scopeFuncs is every function and method a package's scope declares.
func scopeFuncs(pkg *types.Package) []*types.Func {
	var held []*types.Func
	for _, name := range pkg.Scope().Names() {
		switch obj := pkg.Scope().Lookup(name).(type) {
		case *types.Func:
			held = append(held, obj)
		case *types.TypeName:
			if named, isNamed := obj.Type().(*types.Named); isNamed && !obj.IsAlias() {
				held = slices.AppendSeq(held, named.Methods())
			}
		}
	}
	return held
}

// What the assertable class computed of a package outside the program names the
// loaded program's types, so it keeps no reading of the package's source
// reachable once the reader drops it.
func TestAssertableKeepsNoReading(t *testing.T) {
	in := inputOf(t, "encoding-reflection-relayed-outside.txtar", Options{})
	var sources []weak.Pointer[load.Source]
	var readings []weak.Pointer[types.Package]
	for _, pkg := range in.outside {
		in.assert.asserted(pkg)
		in.assert.ownInterfaces(pkg)
		source, ok := in.sources.Read(pkg)
		if !ok {
			continue
		}
		sources = append(sources, weak.Make(source))
		for _, obj := range source.Info.Defs {
			if obj != nil && obj.Pkg() != nil && obj.Pkg() != pkg {
				readings = append(readings, weak.Make(obj.Pkg()))
				break
			}
		}
	}
	if len(sources) == 0 || len(readings) == 0 {
		t.Fatalf("Setup: %d sources and %d readings outside the program", len(sources), len(readings))
	}
	underPressure(t)
	runtime.GC()
	for i := range sources {
		if sources[i].Value() != nil {
			t.Errorf("source %d of %d is still reachable from the assertable class", i+1, len(sources))
		}
	}
	for i := range readings {
		if readings[i].Value() != nil {
			t.Errorf("the package of reading %d of %d is still reachable from the assertable class", i+1, len(readings))
		}
	}
	runtime.KeepAlive(in)
}
