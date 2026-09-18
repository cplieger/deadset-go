package matrix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/txtar"
)

// The host every fixture is derived against, stated rather than read, so a
// fixture's expected configuration identifiers are the same on every machine.
const (
	fixtureOS   = "linux"
	fixtureArch = "amd64"
)

// fixtureHost is that host as a configuration, spelled the way the production
// host configuration spells its own identifier.
func fixtureHost() load.Configuration {
	return load.Configuration{ID: fixtureOS + "-" + fixtureArch, OS: fixtureOS, Arch: fixtureArch}
}

// extract writes one txtar archive of testdata into a directory of its own and
// returns the directory, which is the target root derivation reads.
func extract(t *testing.T, archive string) string {
	t.Helper()

	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	dir := t.TempDir()
	for _, f := range parsed.Files {
		write(t, filepath.Join(dir, filepath.FromSlash(f.Name)), f.Data)
	}
	return dir
}

// write puts one file of a fixture on disk, creating the directories above it.
func write(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
}

// identifiers returns the identifier of every configuration of a matrix, in
// order.
func identifiers(configurations []load.Configuration) []string {
	ids := make([]string, len(configurations))
	for i, c := range configurations {
		ids[i] = c.ID
	}
	return ids
}
