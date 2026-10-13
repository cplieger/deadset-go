package matrix

import (
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
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

// aliases maps an operating system to the name a build expression may still use
// for the system it grew out of, which the toolchain answers for it.
var aliases = map[string]string{
	"android": "linux",
	"illumos": "solaris",
	"ios":     "darwin",
}

// unixOS is the set of operating systems the unix build tag stands for.
var unixOS = []string{
	"aix", "android", "darwin", "dragonfly", "freebsd", "hurd",
	"illumos", "ios", "linux", "netbsd", "openbsd", "solaris",
}

const (
	cgoTag                 = "cgo"
	boringcryptoExperiment = "goexperiment.boringcrypto"
)

// satisfies returns the function that answers whether configuration c satisfies
// one build tag, by the toolchain's rule with cgo disabled. It is the oracle the
// tests evaluate the package's reading of a constraint with, and is pinned
// against the toolchain's own file matching.
func satisfies(c load.Configuration) func(tag string) bool {
	return func(tag string) bool {
		switch {
		case tag == c.OS, tag == c.Arch, tag == runtime.Compiler:
			return true
		case tag == aliases[c.OS]:
			return true
		case tag == unixTag:
			return slices.Contains(unixOS, c.OS)
		case slices.Contains(c.Tags, tag):
			return true
		case tag == cgoTag:
			return false
		case tag == boringcryptoTag:
			tag = boringcryptoExperiment
		}
		return slices.Contains(build.Default.ReleaseTags, tag) ||
			slices.Contains(build.Default.ToolTags, tag)
	}
}

// pathed is one Go file of a fixture tree and its constraint.
type pathed struct {
	path string // root-relative, forward slashes
	file fileConstraint
}

// pathedFiles reads every Go file the derivation reads under root, as collect walks
// the tree, each with its root-relative path.
func pathedFiles(t *testing.T, root string) []pathed {
	t.Helper()

	var found []pathed
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir() && path != root && (unbuilt(entry.Name()) || startsAnotherModule(path)):
			return fs.SkipDir
		case entry.IsDir() || !isGoFile(entry.Name()):
			return nil
		}
		file, err := readConstraint(root, path)
		if err != nil {
			return err
		}
		found = append(found, pathed{path: relativeToSlash(root, path), file: file})
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: read the constraints under %s: %v", root, err)
	}
	return found
}

// constraintsOf is the constraints of pathed files, in their order.
func constraintsOf(files []pathed) []fileConstraint {
	held := make([]fileConstraint, len(files))
	for i := range files {
		held[i] = files[i].file
	}
	return held
}

// unbuiltUnder returns the path of every file no configuration of the matrix
// builds, in the order the tree was read.
func unbuiltUnder(files []pathed, configurations []load.Configuration) []string {
	var unbuilt []string
	for _, one := range files {
		if !slices.ContainsFunc(configurations, func(c load.Configuration) bool { return selects(one.file, c) }) {
			unbuilt = append(unbuilt, one.path)
		}
	}
	return unbuilt
}
