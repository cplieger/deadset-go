package exempt

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	"github.com/cplieger/deadset-go/internal/testsupport"
	"golang.org/x/tools/txtar"
)

// The build configuration every fixture of this package is loaded for, which is
// one configuration because a class is computed per configuration and the matrix
// is the caller's concern.
const (
	fixtureOS   = "linux"
	fixtureArch = "amd64"
)

// extract writes one txtar archive of testdata into a directory of its own and
// returns the directory, which is the target root the load resolves.
func extract(t *testing.T, archive string) string {
	t.Helper()
	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	dir := t.TempDir()
	for _, f := range parsed.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	return dir
}

// The directories a two-module archive writes its target and its declared consumer
// into. An archive holding the first is loaded as a program of two modules, which is
// how a fixture reaches the consumer half of the boundary.
const (
	fixtureTargetDir   = "target"
	fixtureConsumerDir = "httpwire"
)

// loadDir resolves one directory through the production load, so the Need bits a
// class depends on have one owner.
func loadDir(t *testing.T, dir string) (*load.Result, string) {
	t.Helper()

	doc, err := scope.ForDir(dir)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	return loadScope(t, doc)
}

// loadTwoModules resolves the target and the declared consumer of one extracted
// two-module archive, which is the scope a run analysing a target with a consumer
// loaded reads.
func loadTwoModules(t *testing.T, dir string) (*load.Result, string) {
	t.Helper()

	return loadScope(t, scope.Document{
		Target:    scope.Module{Path: filepath.Join(dir, fixtureTargetDir)},
		Consumers: []scope.Module{{Path: filepath.Join(dir, fixtureConsumerDir)}},
	})
}

// loadScope loads one scope document under the fixture configuration.
func loadScope(t *testing.T, doc scope.Document) (*load.Result, string) {
	t.Helper()

	result, err := load.Load(t.Context(), doc, load.Configuration{
		ID: fixtureOS + "-" + fixtureArch, OS: fixtureOS, Arch: fixtureArch,
	})
	if err != nil {
		t.Fatalf("Setup: load.Load(%s): %v", doc.Target.Path, err)
	}
	return &result, doc.Target.Path
}

// twoModules reports whether one archive writes a target and a consumer rather than
// a single module, which is what decides the scope it is loaded under.
func twoModules(t *testing.T, archive string) bool {
	t.Helper()

	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	for _, f := range parsed.Files {
		if f.Name == fixtureTargetDir+"/go.mod" {
			return true
		}
	}
	return false
}

// inputOf extracts one archive, loads it, enumerates its declarations and builds
// the input every class reads, so a class is measured over the production path
// from the archive to the type information rather than over a graph written by
// hand.
func inputOf(t *testing.T, archive string, opts Options) *Input {
	t.Helper()

	dir := extract(t, archive)
	program := loadDir
	if twoModules(t, archive) {
		program = loadTwoModules
	}
	result, root := program(t, dir)
	symbols, err := graph.Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: graph.Symbols(%s): %v", archive, err)
	}
	resolve, err := graph.NewResolver(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.NewResolver(%s): %v", archive, err)
	}
	return &Input{
		Result:  result,
		Resolve: resolve,
		Symbols: symbols,
		Root:    root,
		Read:    os.ReadFile,
		Options: opts,
	}
}

// sharedAnalysis is what one archive answers under one set of options, read by every
// test that asks the same question of it: the inventory, what each class's detector
// returned when it ran alone, and the union Compute returned under each mode. No part
// of the input it was computed from is held.
type sharedAnalysis struct {
	symbols  []graph.Symbol
	detected map[Class][]graph.Exemption
	failed   map[Class]error
	union    map[bool][]graph.Exemption
	refused  map[bool]error
}

// analysisKey is one shared analysis: the archive and its options as fmt renders them
// in Go syntax, which quotes every string so two different sets of options never
// spell one key.
type analysisKey struct {
	archive string
	options string
}

// analyses holds every shared analysis the package's tests built.
var analyses testsupport.Memo[analysisKey, *sharedAnalysis]

// analysisOf is the shared analysis of one archive under one set of options, loaded
// once for every test that reads it.
func analysisOf(t *testing.T, archive string, opts Options) *sharedAnalysis {
	t.Helper()

	key := analysisKey{archive: archive, options: fmt.Sprintf("%#v", opts)}
	return analyses.Of(t, key, func(t *testing.T) *sharedAnalysis {
		return analyzeShared(inputOf(t, archive, opts))
	})
}

// analyzeShared answers every question of a shared analysis from one input: each
// detector over the input as the load built it, then the union under the plain mode
// and under a production run.
func analyzeShared(in *Input) *sharedAnalysis {
	held := &sharedAnalysis{
		symbols:  in.Symbols,
		detected: make(map[Class][]graph.Exemption),
		failed:   make(map[Class]error),
		union:    make(map[bool][]graph.Exemption),
		refused:  make(map[bool]error),
	}
	for class, detect := range goDetectors() {
		held.detected[class], held.failed[class] = detect(in)
	}
	for _, production := range []bool{false, true} {
		in.Mode = graph.Mode{Production: production}
		held.union[production], held.refused[production] = Compute(in, goDetectors())
	}
	return held
}

// inventory is the declarations the archive's load enumerated.
func (a *sharedAnalysis) inventory(t *testing.T) []graph.Symbol {
	t.Helper()

	return testsupport.Detached(t, a.symbols)
}

// detect is what one class's detector returned when it ran alone over the input.
func (a *sharedAnalysis) detect(t *testing.T, class Class) ([]graph.Exemption, error) {
	t.Helper()

	found, held := a.detected[class]
	if !held {
		t.Fatalf("Setup: a shared analysis runs no detector of %s", class)
	}
	return testsupport.Detached(t, found), a.failed[class]
}

// compute is what Compute returned over every class under the plain mode or under a
// production run.
func (a *sharedAnalysis) compute(t *testing.T, production bool) ([]graph.Exemption, error) {
	t.Helper()

	return testsupport.Detached(t, a.union[production]), a.refused[production]
}
