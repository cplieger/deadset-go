package exempt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
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

// loadDir resolves one directory through the production load, so the Need bits a
// class depends on have one owner.
func loadDir(t *testing.T, dir string) (*load.Result, string) {
	t.Helper()

	doc, err := scope.ForDir(dir)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	result, err := load.Load(t.Context(), doc, load.Configuration{
		ID: fixtureOS + "-" + fixtureArch, OS: fixtureOS, Arch: fixtureArch,
	})
	if err != nil {
		t.Fatalf("Setup: load.Load(%s): %v", dir, err)
	}
	return &result, doc.Target.Path
}

// inputOf extracts one archive, loads it, enumerates its declarations and builds
// the input every class reads, so a class is measured over the production path
// from the archive to the type information rather than over a graph written by
// hand.
func inputOf(t *testing.T, archive string, opts Options) *Input {
	t.Helper()

	result, root := loadDir(t, extract(t, archive))
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
