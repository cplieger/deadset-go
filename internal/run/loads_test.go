package run

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"weak"

	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// platformTree is a module whose matrix holds two configurations, the host pinned
// to linux-amd64 and one for its windows file, with declarations every kind of the
// findings pass reads.
func platformTree(t *testing.T) string {
	t.Helper()
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOOS", "linux")
	t.Setenv("GOARCH", "amd64")
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/p\n\ngo 1.27.1\n",
		"main.go": `package main

import "fmt"

type Mode int

const (
	Fast Mode = iota
	Slow
)

type Store struct {
	written int
	Name    string
}

type Reader interface{ Read() string }

var _ Reader = (*Store)(nil)

func (s *Store) Read() string { return s.Name }

func describe(unused int, s *Store) string {
	s.written = 1
	x := 2
	x = 3
	return fmt.Sprint(s.Name)
}

func dead() {}

func main() { println(describe(0, &Store{}), Fast, platform()) }
`,
		"os_linux.go":   "package main\n\nfunc platform() string { return \"linux\" }\n",
		"os_windows.go": "package main\n\nfunc platform() string { return \"windows\" }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", name, err)
		}
	}
	return root
}

// loadedTree runs the load stage over the platform tree.
func loadedTree(t *testing.T, root string) *Loaded {
	t.Helper()
	loaded, err := Load(t.Context(), &Request{Scope: scope.Document{Target: scope.Module{Path: root}}, Config: defaultConfig()})
	if err != nil {
		t.Fatalf("Setup: Load(%s) = %v", root, err)
	}
	if got := loaded.loads.Len(); got != 2 {
		t.Fatalf("Setup: the tree's matrix holds %d configurations, want 2", got)
	}
	return loaded
}

// Under memory pressure the store holds one configuration's load at a time: the
// one it handed out before is collectable once the next is read, and reading it
// again loads it again with the same declarations.
func TestLoadsHoldOneConfigurationAtATimeUnderPressure(t *testing.T) {
	loaded := loadedTree(t, platformTree(t))
	l := loaded.loads
	l.pressed = func() bool { return true }
	l.drop()

	first, err := l.At(0)
	if err != nil {
		t.Fatalf("At(0) = %v", err)
	}
	symbols := first.Symbols
	held := weak.Make(first)
	result := weak.Make(first.Result)
	if _, err := l.At(1); err != nil {
		t.Fatalf("At(1) = %v", err)
	}
	runtime.GC()
	if held.Value() != nil || result.Value() != nil {
		t.Error("configuration 0's load is reachable once configuration 1 is read under pressure, want it collectable")
	}
	again, err := l.At(0)
	if err != nil {
		t.Fatalf("At(0) again = %v", err)
	}
	if !reflect.DeepEqual(symbols, again.Symbols) {
		t.Error("configuration 0 loaded again declares other declarations than its first load")
	}
}

// Outside pressure the store keeps every configuration it loaded, so a later
// stage reads it without loading it again.
func TestLoadsKeepEveryConfigurationWithoutPressure(t *testing.T) {
	l := loadedTree(t, platformTree(t)).loads
	l.pressed = func() bool { return false }
	first, err := l.At(0)
	if err != nil {
		t.Fatalf("At(0) = %v", err)
	}
	if _, err := l.At(1); err != nil {
		t.Fatalf("At(1) = %v", err)
	}
	if again, _ := l.At(0); again != first {
		t.Error("At(0) after At(1) loaded configuration 0 again, want the load it kept")
	}
}

// Every stage after the load answers the same whether it reads the configurations
// it kept or loads each again one at a time, so memory pressure changes how a run
// goes and never what it finds.
func TestFindingsUnderPressureAreTheFindingsWithout(t *testing.T) {
	root := platformTree(t)
	compute := func(pressed bool) findings.Result {
		loaded := loadedTree(t, root)
		if pressed {
			loaded.loads.pressed = func() bool { return true }
			loaded.loads.drop()
		}
		prepared, err := loaded.Prepare(true)
		if err != nil {
			t.Fatalf("Prepare = %v", err)
		}
		swept, err := prepared.Sweep()
		if err != nil {
			t.Fatalf("Sweep = %v", err)
		}
		found, err := swept.Findings(t.Context())
		if err != nil {
			t.Fatalf("Findings = %v", err)
		}
		return found.Result
	}
	relieved, pressed := compute(false), compute(true)
	if len(relieved.Findings) == 0 {
		t.Fatal("Setup: the tree has no finding, so this test pins nothing")
	}
	if !reflect.DeepEqual(relieved, pressed) {
		t.Errorf("the findings under pressure = %+v, want the findings without: %+v", pressed.Findings, relieved.Findings)
	}
}

// A configuration loaded again reads the packages its first load read, even where
// the module's files changed since: here its dependency now resolves to another
// directory, as after a go.mod edit during the run.
func TestLoadsLoadAgainWhatTheFirstLoadRead(t *testing.T) {
	root := platformTree(t)
	dep := "module example.com/dep\n\ngo 1.27.1\n"
	for name, body := range map[string]string{
		"dep1/go.mod": dep,
		"dep1/dep.go": "package dep\n\nfunc Name() string { return \"d\" }\n",
		"dep2/go.mod": dep,
		"dep2/dep.go": "package dep\n\nfunc Name() string { return \"d\" }\n",
		"go.mod":      "module example.com/p\n\ngo 1.27.1\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep1\n",
		"dep_use.go":  "package main\n\nimport \"example.com/dep\"\n\nvar _ = dep.Name()\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatalf("Setup: mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", name, err)
		}
	}
	l := loadedTree(t, root).loads
	l.pressed = func() bool { return true }
	l.drop()
	moved := "module example.com/p\n\ngo 1.27.1\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep2\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(moved), 0o600); err != nil {
		t.Fatalf("Setup: write go.mod: %v", err)
	}
	again, err := l.At(0)
	if err != nil {
		t.Fatalf("At(0) once the dependency moved = %v, want the first load's packages", err)
	}
	var dirs []string
	packages.Visit(again.Result.Packages, nil, func(p *packages.Package) {
		if p.PkgPath == "example.com/dep" {
			dirs = append(dirs, filepath.Base(p.Dir))
		}
	})
	if len(dirs) == 0 || slices.ContainsFunc(dirs, func(dir string) bool { return dir != "dep1" }) {
		t.Errorf("At(0) once the dependency moved read example.com/dep from %v, want dep1, as the first load did", dirs)
	}
}

// Each part of a package's digest changes with what that part names alone.
func TestPackageDigestPartsAreWhatTheyName(t *testing.T) {
	fset := token.NewFileSet()
	file := fset.AddFile("a.go", -1, 10)
	base := func() *packages.Package {
		return &packages.Package{
			PkgPath: "example.com/a", Name: "a",
			Module:          &packages.Module{Path: "example.com/a", Dir: "/a"},
			CompiledGoFiles: []string{"a.go"},
			Syntax:          []*ast.File{{FileStart: token.Pos(file.Base())}},
			Imports:         map[string]*packages.Package{"strings": {ID: "strings"}},
		}
	}
	first := packageDigestOf(fset, base())
	for _, tt := range []struct {
		part   string
		change func(p *packages.Package)
	}{
		{"name", func(p *packages.Package) { p.Name = "b" }},
		{"module", func(p *packages.Package) { p.Module.Dir = "/b" }},
		{"compiled files", func(p *packages.Package) { p.CompiledGoFiles = append(p.CompiledGoFiles, "b.go") }},
		{"parsed files", func(p *packages.Package) { p.Syntax = nil }},
		{"imports", func(p *packages.Package) { p.Imports["bytes"] = &packages.Package{ID: "bytes"} }},
	} {
		p := base()
		tt.change(p)
		got := packageDigestOf(fset, p)
		for part := range packageParts {
			if (got[part] != first[part]) != (packageParts[part] == tt.part) {
				t.Errorf("changing its %s changed its %s digest: %t", tt.part, packageParts[part], got[part] != first[part])
			}
		}
	}
}

// A load digest names the first package that differs, and the part of it, and
// else the declarations.
func TestLoadDigestNamesWhatDiffers(t *testing.T) {
	parts := func(imports uint64) [len(packageParts)]uint64 { return [...]uint64{1, 2, 3, 4, imports} }
	first := loadDigest{packages: []packageDigest{{id: "a", parts: parts(5)}, {id: "b", parts: parts(6)}}, declarations: 7}
	movedModule := parts(6)
	movedModule[1] = 9
	for _, tt := range []struct {
		name string
		got  loadDigest
		want string
	}{
		{"same", first, ""},
		{"imports", loadDigest{packages: []packageDigest{{id: "a", parts: parts(5)}, {id: "b", parts: parts(8)}}, declarations: 7}, "package b differs from its first load in its imports"},
		{"module", loadDigest{packages: []packageDigest{{id: "a", parts: parts(5)}, {id: "b", parts: movedModule}}, declarations: 7}, "package b differs from its first load in its module"},
		{"missing", loadDigest{packages: []packageDigest{{id: "b", parts: parts(6)}}, declarations: 7}, "package a of its first load is not read"},
		{"extra", loadDigest{packages: []packageDigest{{id: "a", parts: parts(5)}, {id: "ab", parts: parts(5)}, {id: "b", parts: parts(6)}}, declarations: 7}, "package ab is read, which its first load did not"},
		{"declarations", loadDigest{packages: first.packages, declarations: 8}, "the declarations differ from its first load"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, differs := first.differs(tt.got)
			if got != tt.want || differs != (tt.want != "") {
				t.Errorf("differs = %q, %t; want %q, %t", got, differs, tt.want, tt.want != "")
			}
		})
	}
}
