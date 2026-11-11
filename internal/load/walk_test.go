package load

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

func TestWalkFollowsARootThatIsALinkAndKeepsItsSpelling(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var walked []string
	top := &Topology{Root: link}
	err := top.Walk(link, func(string) bool { return true }, func(path string, kind Kind) error {
		if kind != directory {
			t.Errorf("Walk(%s) handed %s as kind %d, want a directory", link, path, kind)
		}
		walked = append(walked, path)
		return nil
	})
	if want := []string{link, filepath.Join(link, "sub")}; err != nil || !slices.Equal(walked, want) {
		t.Errorf("Walk(%s) walked %q, %v, want %q", link, walked, err, want)
	}
}

// A directory the walk does not enter is neither listed nor handed to the visit,
// so one the run cannot list is recorded only where a walk enters it.
func TestWalkListsOnlyTheDirectoriesItEnters(t *testing.T) {
	root := writeTree(t, map[string]string{"skipped/a.go": "package a\n", "kept/b.go": "package b\n"})
	top := &Topology{Root: root}
	var walked []string
	err := top.Walk(root, func(dir string) bool { return filepath.Base(dir) != "skipped" }, func(path string, _ Kind) error {
		walked = append(walked, unitPrefix(root, path))
		return nil
	})
	if want := []string{"", "kept", "kept/b.go"}; err != nil || !slices.Equal(walked, want) {
		t.Errorf("Walk(a tree with a skipped directory) walked %q, %v, want %q", walked, err, want)
	}
}

// Where a directory the run cannot list lies below a module, the module's packages
// are listed one directory at a time, since the toolchain's walk of ./... ends at
// that directory; the tree's own skips and a nested module are left out.
func TestTreePatternsNamesEachPackageDirectoryWhereADirectoryIsUnread(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.27.1\n",
		"main.go":        "package main\n",
		"a/a.go":         "package a\n",
		"a/b/b_test.go":  "package b\n",
		"data/README.md": "data\n",
		"testdata/t.go":  "package t\n",
		"_tools/tool.go": "package tools\n",
		"nested/go.mod":  "module example.com/nested\n",
		"nested/n.go":    "package nested\n",
		"z/.hidden.go":   "package z\n",
		"z/y/y.go":       "package y\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	plain := &loadSpec{dir: root, workspace: workspaceOff, top: &Topology{Root: root}}
	if got, byDir, err := listPatterns(t.Context(), plain); err != nil || byDir || !slices.Equal(got, []string{loadPattern}) {
		t.Errorf("listPatterns(no unread directory) = %q, %t, %v, want %q", got, byDir, err, loadPattern)
	}
	unread := &loadSpec{dir: root, workspace: workspaceOff, top: &Topology{Root: root, Unread: []string{filepath.Join(root, "data"), filepath.Join(root, "a", "a.go")}}}
	got, byDir, err := listPatterns(t.Context(), unread)
	if want := []string{"./a", "./a/b", ".", "./z/y"}; err != nil || !byDir || !slices.Equal(got, want) {
		t.Errorf("listPatterns(an unread directory) = %q, %t, %v, want %q", got, byDir, err, want)
	}
}

// A list error that reports opening an unread or a left-out path is cleared, and a
// root left with no file and no error, the toolchain's answer for the pattern its
// walk ended in, is dropped; every other error stays.
func TestDropUnreadErrorsClearsOnlyWhatOpensAnUnreadPath(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "m")
	secret := filepath.Join(root, "p", "secret.go")
	broken := filepath.Join(root, "p", "broken.go")
	spec := &loadSpec{dir: root, top: &Topology{Root: root, Unread: []string{filepath.Join(root, "data"), secret}, LeftOut: []string{broken}}}
	pattern := &packages.Package{ID: "./...", Errors: []packages.Error{{Kind: packages.ListError, Msg: "pattern ./...: open " + filepath.Join("data") + ": permission denied"}}}
	p := &packages.Package{ID: "example.com/m/p", GoFiles: []string{filepath.Join(root, "p", "p.go")}, Errors: []packages.Error{
		{Kind: packages.ListError, Pos: "main.go:3:8", Msg: "open " + secret + ": permission denied"},
		{Kind: packages.ListError, Msg: "open " + broken + ": no such file or directory"},
		{Kind: packages.TypeError, Msg: "undefined: x"},
	}}
	other := &packages.Package{ID: "example.com/m/q", Errors: []packages.Error{{Kind: packages.ListError, Msg: "open " + filepath.Join(root, "q") + ": permission denied"}}}

	got := dropUnreadErrors([]*packages.Package{pattern, p, other}, spec)
	if !slices.Equal(got, []*packages.Package{p, other}) {
		t.Errorf("dropUnreadErrors kept %d packages, want the package and the one whose error names a readable path", len(got))
	}
	if len(p.Errors) != 1 || p.Errors[0].Kind != packages.TypeError {
		t.Errorf("dropUnreadErrors left %v on the package, want its type error alone", p.Errors)
	}
	if len(other.Errors) != 1 {
		t.Errorf("dropUnreadErrors left %v on the package whose error names no unread path, want it kept", other.Errors)
	}
}

// Where an entry below a module is unread, the module's packages are listed one
// directory at a time, and that listing loads the packages the wildcard loads: a
// directory whose every Go file the configuration's constraints exclude is none,
// so no unread entry makes its declarations count as linked.
func TestLoadListsThePackagesTheWildcardListsWhereAnEntryIsUnread(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"go.mod":            "module example.com/m\n\ngo 1.27.1\n",
		"main.go":           "package main\n\nfunc main() {}\n",
		"gen/gen.go":        "//go:build tools\n\npackage gen\n\nfunc helper() {}\n",
		"wasm/w.go":         "//go:build js && wasm\n\npackage wasm\n\nfunc w() {}\n",
		"cg/c.go":           "package cg\n\nimport \"C\"\n\nfunc c() {}\n",
		"data/pg/README.md": "data\n",
	})
	doc, err := scope.ForDir(root)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir = _, %v", err)
	}
	c := HostConfiguration()
	plain, err := Load(t.Context(), discovered(t, doc), &c)
	if err != nil {
		t.Fatalf("Load(a module with no unread entry) = _, %v, want no error", err)
	}
	top := discovered(t, doc)
	top.AddUnread(filepath.Join(root, "data", "pg"))
	unread, err := Load(t.Context(), top, &c)
	if err != nil {
		t.Fatalf("Load(the module with an unread entry) = _, %v, want no error", err)
	}
	if got, want := packageIDs(unread), packageIDs(plain); !slices.Equal(got, want) {
		t.Errorf("Load(the module with an unread entry) loaded %q, want the wildcard's %q", got, want)
	}
}
