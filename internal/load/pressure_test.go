package load

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"weak"

	"golang.org/x/tools/go/packages"
)

// Under memory pressure every parse and check of the process runs alone, whichever
// checker runs it; outside it each checker runs as many at once as it has slots.
func TestCheckerRunsOneTaskAtATimeUnderPressure(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pressed bool
		most    int32
	}{{"pressed", true, 1}, {"relieved", false, 8}} {
		t.Run(tt.name, func(t *testing.T) {
			checkers := []*checker{
				{cpu: make(chan struct{}, 4), pressed: func() bool { return tt.pressed }},
				{cpu: make(chan struct{}, 4), pressed: func() bool { return tt.pressed }},
			}
			var running, most atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range 16 {
				c := checkers[i%len(checkers)]
				wg.Go(func() {
					<-start
					release := c.acquire()
					now := running.Add(1)
					for seen := most.Load(); now > seen && !most.CompareAndSwap(seen, now); seen = most.Load() {
					}
					runtime.Gosched()
					running.Add(-1)
					release()
				})
			}
			close(start)
			wg.Wait()
			if got := most.Load(); got > tt.most {
				t.Errorf("at most %d tasks ran at once, want at most %d", got, tt.most)
			}
		})
	}
}

// A parse waiting for the process's slot under pressure has not read its file yet,
// so the bytes of every file on the ready frontier are not live at once.
func TestParseReadsItsFileOnlyOnceItHoldsTheSlot(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(name, []byte("package before\n"), 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	c := &checker{fset: token.NewFileSet(), cpu: make(chan struct{}, 1), pressed: func() bool { return true }}
	pressedSlot.Lock()
	parsed := make(chan *ast.File, 1)
	go func() {
		f, _ := c.parseOnce(name, signatureParse)
		parsed <- f
	}()
	for len(c.cpu) == 0 {
		runtime.Gosched()
	}
	if err := os.WriteFile(name, []byte("package after\n"), 0o600); err != nil {
		pressedSlot.Unlock()
		t.Fatalf("Setup: %v", err)
	}
	pressedSlot.Unlock()
	if f := <-parsed; f == nil || f.Name.Name != "after" {
		t.Errorf("parseOnce read the file before it held the slot, want the read inside it")
	}
}

// Pressure drops the sources a reader holds, and a read under pressure keeps
// nothing, so each read source and its file set are collectable once its caller
// lets it go.
func TestSourcesKeepNoReadUnderPressure(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"a/go.mod": "module example.com/a\n\ngo 1.27.1\n",
		"a/a.go":   "package a\n\nimport \"strings\"\n\nvar A = strings.ToUpper(\"a\")\n",
	})
	got, err := loadTree(t, root, "a")
	if err != nil {
		t.Fatalf("Setup: Load(a) = _, %v", err)
	}
	own := got.Packages[slices.IndexFunc(got.Packages, func(p *packages.Package) bool { return p.PkgPath == "example.com/a" && p.ForTest == "" })]
	dependency := own.Imports["strings"].Types

	sources := got.Sources()
	first, read := sources.Read(dependency)
	if !read {
		t.Fatalf("Setup: Sources().Read(strings) = _, false in %s", filepath.Join(root, "a"))
	}
	held, heldFiles := weak.Make(first), weak.Make(first.Fset)
	sources.drop()
	runtime.GC()
	if held.Value() != nil {
		t.Error("a source a dropped reader read is still reachable")
	}
	if heldFiles.Value() != nil {
		t.Error("the file set of a source a dropped reader read is still reachable")
	}

	sources.pressed = func() bool { return true }
	underFiles := func() []weak.Pointer[token.FileSet] {
		var held []weak.Pointer[token.FileSet]
		var last *Source
		for range 4 {
			under, _ := sources.Read(dependency)
			if under == last {
				t.Fatal("two reads under pressure returned one held source, want each read afresh")
			}
			held = append(held, weak.Make(under.Fset))
			last = under
		}
		return held
	}()
	runtime.GC()
	for i, files := range underFiles {
		if files.Value() != nil {
			t.Errorf("the file set of read %d under pressure is still reachable after its caller let it go", i+1)
		}
	}
	runtime.KeepAlive(sources)
}
