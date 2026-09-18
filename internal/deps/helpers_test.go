package deps

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	"golang.org/x/tools/txtar"
)

// The build configuration every fixture of this package is loaded for, which is
// one configuration because a dependency answer is one configuration's and the
// matrix is the caller's concern.
const (
	fixtureOS   = "linux"
	fixtureArch = "amd64"
)

// extract writes one txtar archive of testdata into a directory of its own and
// returns the directory, which is the target root the load resolves. Two calls
// over one archive give two directories, so a call that changes the module file
// leaves the other untouched.
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
// dependency answer depends on have one owner.
func loadDir(t *testing.T, dir string) *load.Result {
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
	return &result
}

// moduleFileOf reads one archive's module file through the production reader.
func moduleFileOf(t *testing.T, dir string) File {
	t.Helper()
	f, err := ModuleFile(t.Context(), dir)
	if err != nil {
		t.Fatalf("Setup: ModuleFile(%s): %v", dir, err)
	}
	return f
}

// targetOf extracts one archive, loads it and reads its module file, which is
// every input both population rules take.
func targetOf(t *testing.T, archive string) (File, *load.Result) {
	t.Helper()
	dir := extract(t, archive)
	return moduleFileOf(t, dir), loadDir(t, dir)
}

// requirementPaths lists the module paths of a requirement set, in the order the
// set holds them.
func requirementPaths(requirements []Requirement) []string {
	paths := make([]string, 0, len(requirements))
	for _, r := range requirements {
		paths = append(paths, r.Path)
	}
	return paths
}

// tidyRemoved returns the module paths the toolchain's own tidying removes from a
// fresh extraction of one archive, sorted. It is the oracle the unused-requirement
// population is measured against, and it runs in its own extraction so the
// directory a test loads is never the one tidying rewrote.
func tidyRemoved(t *testing.T, archive string) []string {
	t.Helper()

	dir := extract(t, archive)
	before := requirementPaths(moduleFileOf(t, dir).Requires)

	tidy := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	tidy.Dir = dir
	// The module file is the one thing tidying must be allowed to write, and
	// every module of every fixture is a directory of the fixture, so nothing is
	// fetched.
	tidy.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("Setup: go mod tidy in %s: %v: %s", dir, err, out)
	}

	after := requirementPaths(moduleFileOf(t, dir).Requires)
	removed := make([]string, 0, len(before))
	for _, path := range before {
		if !slices.Contains(after, path) {
			removed = append(removed, path)
		}
	}
	slices.Sort(removed)
	return removed
}
