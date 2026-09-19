package load

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/tools/go/packages"
)

// declaredNames is the sorted set of package-level names one loaded package's
// types hold, which is what the second check adds to.
func declaredNames(p *packages.Package) []string {
	if p.Types == nil {
		return nil
	}
	names := slices.Clone(p.Types.Scope().Names())
	slices.Sort(names)
	return names
}

// definedAt renders the position every declaration of one loaded package is
// defined at, keyed by name, so a test reads which file the check recorded.
func definedAt(r Result, p *packages.Package) map[string]string {
	at := make(map[string]string)
	if p.TypesInfo == nil {
		return at
	}
	for id, obj := range p.TypesInfo.Defs {
		if obj == nil || obj.Parent() != p.Types.Scope() {
			continue
		}
		position := r.Fset.Position(id.Pos())
		at[id.Name] = fmt.Sprintf("%s:%d:%d", filepath.Base(position.Filename), position.Line, position.Column)
	}
	return at
}

func TestLoadReadsTheFilesCgoAloneExcludesAndLeavesTheRestIgnored(t *testing.T) {
	stableToolchain(t)
	// A host that enables cgo must not change the result: the load sets
	// CGO_ENABLED=0 after the ambient environment, so it wins. And no C toolchain
	// is reached at any point: the check runs in this process and the load it
	// spawns disables cgo, so a C compiler that does not exist changes nothing.
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("CC", filepath.Join(t.TempDir(), "no-such-c-compiler"))
	t.Setenv("CXX", filepath.Join(t.TempDir(), "no-such-cxx-compiler"))

	got := loadFixture(t, "cgo")

	if len(got.ExcludedByCgo) != 0 {
		t.Errorf("Load(testdata/cgo).ExcludedByCgo = %v, want empty: the opaque-C check reads both files",
			got.ExcludedByCgo)
	}
	pkg := findPackage(got, "example.com/cgo")
	if pkg == nil {
		t.Fatalf("Load(testdata/cgo) reported no example.com/cgo package, ids = %v", packageIDs(got))
	}

	// Each of these is ignored for a reason cgo does not decide alone, so each
	// stays in the population the build configurations reason about.
	ignored := baseNames(pkg.IgnoredFiles)
	for _, name := range []string{"unselected.go", "bridge_windows.go", "only_cgo_tag.go", "helper_plan9.c"} {
		if !slices.Contains(ignored, name) {
			t.Errorf("IgnoredFiles = %v, want it to contain %s", ignored, name)
		}
	}
	for _, name := range []string{"bridge.go", "bridge_tagged.go"} {
		if slices.Contains(ignored, name) {
			t.Errorf("IgnoredFiles = %v, want it not to contain %s: the check read it", ignored, name)
		}
		if compiled := baseNames(pkg.GoFiles); !slices.Contains(compiled, name) {
			t.Errorf("GoFiles = %v, want it to contain %s", compiled, name)
		}
		if compiled := baseNames(pkg.CompiledGoFiles); !slices.Contains(compiled, name) {
			t.Errorf("CompiledGoFiles = %v, want it to contain %s", compiled, name)
		}
	}

	// The declarations of both files are the checked package's, and the position
	// of each is the original file's rather than a generated one's.
	if want := []string{"Selected", "viaC", "viaCUnderTheTag"}; !slices.Equal(declaredNames(pkg), want) {
		t.Errorf("Load(testdata/cgo) declares %v, want %v", declaredNames(pkg), want)
	}
	at := definedAt(got, pkg)
	if want := "bridge.go:9:6"; at["viaC"] != want {
		t.Errorf("viaC is defined at %s, want %s", at["viaC"], want)
	}
	if want := "bridge_tagged.go:12:6"; at["viaCUnderTheTag"] != want {
		t.Errorf("viaCUnderTheTag is defined at %s, want %s", at["viaCUnderTheTag"], want)
	}

	// Nothing of the C half is a declaration of anything: the mode declares an
	// empty package for "C".
	for _, imported := range pkg.Types.Imports() {
		if imported.Path() == "C" && len(imported.Scope().Names()) != 0 {
			t.Errorf("the C package declares %v, want nothing", imported.Scope().Names())
		}
	}
}

func TestLoadFallsBackPerFileOnAnErrorOfItsOwn(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, "cgo-fallback")

	// The file carrying a Go type error and the file the language refuses each
	// fall back; the third is read.
	if want := []string{"broken.go", "renamed.go"}; !slices.Equal(got.ExcludedByCgo, want) {
		t.Errorf("Load(testdata/cgo-fallback).ExcludedByCgo = %v, want %v", got.ExcludedByCgo, want)
	}
	pkg := findPackage(got, "example.com/cgofallback")
	if pkg == nil {
		t.Fatalf("Load(testdata/cgo-fallback) reported no package, ids = %v", packageIDs(got))
	}
	if want := []string{"Selected", "helper", "viaC"}; !slices.Equal(declaredNames(pkg), want) {
		t.Errorf("Load(testdata/cgo-fallback) declares %v, want %v", declaredNames(pkg), want)
	}
	if compiled := baseNames(pkg.GoFiles); !slices.Equal(compiled, []string{"app.go", "bridge.go"}) {
		t.Errorf("GoFiles = %v, want [app.go bridge.go]", compiled)
	}
	// Every package of the import path is updated, the in-package test variant
	// included: a variant that still ignored the file would leave two packages of
	// one directory disagreeing about which files the configuration builds.
	variants := 0
	for _, p := range got.Packages {
		if p.PkgPath != "example.com/cgofallback" {
			continue
		}
		variants++
		if compiled := baseNames(p.GoFiles); !slices.Contains(compiled, "bridge.go") {
			t.Errorf("%s GoFiles = %v, want it to contain bridge.go", p.ID, compiled)
		}
		if want := []string{"broken.go", "renamed.go"}; !slices.Equal(baseNames(p.IgnoredFiles), want) {
			t.Errorf("%s IgnoredFiles = %v, want %v", p.ID, baseNames(p.IgnoredFiles), want)
		}
	}
	if variants != 2 {
		t.Errorf("Load(testdata/cgo-fallback) reported %d packages of example.com/cgofallback, want the package and its in-package test variant (ids = %v)",
			variants, packageIDs(got))
	}
}

func TestLoadResolvesAnImportOnlyAFileImportingCWrites(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, "cgo-import")

	if len(got.ExcludedByCgo) != 0 {
		t.Errorf("Load(testdata/cgo-import).ExcludedByCgo = %v, want empty", got.ExcludedByCgo)
	}
	pkg := findPackage(got, "example.com/cgoimport")
	if pkg == nil {
		t.Fatalf("Load(testdata/cgo-import) reported no package, ids = %v", packageIDs(got))
	}
	if want := []string{"Selected", "helper", "viaC"}; !slices.Equal(declaredNames(pkg), want) {
		t.Errorf("Load(testdata/cgo-import) declares %v, want %v", declaredNames(pkg), want)
	}
	// The package the file importing "C" imports is an import of the package that
	// holds the file, the way an import of a compiled file is.
	if pkg.Imports["strings"] == nil {
		t.Errorf("Imports = %v, want it to hold strings", slices.Sorted(maps.Keys(pkg.Imports)))
	}
}

func TestCgoOnlyDecidesWhichIgnoredFilesTheCheckReads(t *testing.T) {
	ctxt := cgoEnabledContext(HostConfiguration())
	cases := map[string]bool{
		"bridge.go":         true,
		"bridge_tagged.go":  true,
		"bridge_windows.go": false,
		"only_cgo_tag.go":   false,
		"unselected.go":     false,
		"helper_plan9.c":    false,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			path, err := filepath.Abs(filepath.Join("testdata", "cgo", name))
			if err != nil {
				t.Fatalf("Setup: absolute path of %s: %v", name, err)
			}
			got, err := excludedByCgoAlone(&ctxt, path)
			if err != nil {
				t.Fatalf("excludedByCgoAlone(%s) = _, %v, want no error", name, err)
			}
			if got != want {
				t.Errorf("excludedByCgoAlone(%s) = %t, want %t", name, got, want)
			}
		})
	}
}

// parseFiles parses one source into a file set through the check's own parser, so
// a test reads the trees the check would hold.
func parseFiles(t *testing.T, sources map[string]string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	dir := t.TempDir()
	parsed := make(map[string]*ast.File, len(sources))
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(sources[name]), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, nil, parser.AllErrors|parser.ParseComments)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", path, err)
		}
		parsed[path] = f
	}
	return fset, parsed
}

func TestAtOpaqueImportReadsThePositionAndNotTheText(t *testing.T) {
	const bridge = "package app\n\nimport \"C\"\n\nfunc viaC() {}\n"
	const renamed = "package app\n\nimport bridge \"C\"\n\nfunc viaRenamedC() {}\n"
	const plain = "package app\n\nimport \"strings\"\n\nvar _ = strings.TrimSpace\n"
	fset, parsed := parseFiles(t, map[string]string{
		"bridge.go": bridge, "renamed.go": renamed, "plain.go": plain,
	})
	o := &opaqueCheck{fset: fset, parsed: parsed}

	// Every position of the three files, named by what is written at it, so a case
	// names a position rather than a number.
	spec := func(name string, want bool) {
		t.Helper()
		for path, f := range parsed {
			if filepath.Base(path) != name {
				continue
			}
			for _, imp := range f.Imports {
				if got := o.atOpaqueImport(imp.Pos()); got != want {
					t.Errorf("atOpaqueImport(the import spec of %s) = %t, want %t", name, got, want)
				}
			}
		}
	}
	// The unnamed import of "C" is the one position the mode's own diagnostic
	// carries. A renamed one is refused by the language, and an import of anything
	// else is an ordinary import.
	spec("bridge.go", true)
	spec("renamed.go", false)
	spec("plain.go", false)

	for path, f := range parsed {
		name := filepath.Base(path)
		for _, decl := range f.Decls {
			if got := o.atOpaqueImport(decl.Pos()); got {
				t.Errorf("atOpaqueImport(the first declaration of %s) = true, want false", name)
			}
		}
		if got := o.atOpaqueImport(f.FileStart); got {
			t.Errorf("atOpaqueImport(the start of %s) = true, want false", name)
		}
	}
	if got := o.atOpaqueImport(token.NoPos); got {
		t.Error("atOpaqueImport(an invalid position) = true, want false")
	}
}

func TestParseReportsTheFilesThatDoNotParse(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
		return path
	}
	// A file whose imports read and whose body does not is the one the load's own
	// classification cannot refuse, because that reads the imports alone.
	good := write("bridge.go", "package app\n\nimport \"C\"\n\nfunc viaC() int { return 1 }\n")
	bad := write("broken.go", "package app\n\nimport \"C\"\n\nfunc viaC() int { return \n")
	absent := filepath.Join(dir, "absent.go")

	o := &opaqueCheck{fset: token.NewFileSet(), parsed: make(map[string]*ast.File)}
	files, unparsed := o.parse([]string{absent, bad, good})

	if len(files) != 1 || o.fset.Position(files[0].FileStart).Filename != good {
		t.Errorf("parse() read %d files, want the one that parses", len(files))
	}
	if want := []string{absent, bad}; !slices.Equal(slices.Sorted(maps.Keys(unparsed)), want) {
		t.Errorf("parse() reported %v unparsed, want %v", slices.Sorted(maps.Keys(unparsed)), want)
	}
	// One tree per path, so a second call over the same path returns the tree the
	// first parsed and one source site keeps one position.
	again, _ := o.parse([]string{good})
	if len(again) != 1 || again[0] != files[0] {
		t.Error("parse() parsed the same path twice, want one tree per path")
	}
}
