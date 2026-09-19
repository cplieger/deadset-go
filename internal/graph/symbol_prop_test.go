package graph

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"pgregory.net/rapid"
)

// drawnDeclaration is one drawn declaration: the shape it takes and whether it is
// exported.
type drawnDeclaration struct {
	shape    string
	exported bool
}

// drawnPackage is one package as it was drawn: the declarations of each of its
// production files, and whether each kind of test file is present.
type drawnPackage struct {
	files     [][]drawnDeclaration
	inPackage bool
	external  bool
}

// generatedPackage is one drawn package rendered: its files, keyed by their path
// relative to the module root, and the counts the property is stated against.
type generatedPackage struct {
	files     map[string]string
	name      string
	goFiles   int // every .go file of this package, test files included
	packages  int // this package, plus its external test package when one exists
	inPackage bool
}

// shapes are the shapes a drawn declaration takes. A type-method renders two
// declarations, a defined type and a method on it.
func shapes() []string {
	return []string{"func", "struct", "const", "var", "interface", "type-method"}
}

// configurations are the build configurations a draw loads under. A generated file
// carries no build constraint, so every one of them builds under each.
func configurations() []load.Configuration {
	return []load.Configuration{
		{ID: "linux-amd64", OS: "linux", Arch: "amd64"},
		{ID: "linux-arm64", OS: "linux", Arch: "arm64"},
		{ID: "darwin-arm64", OS: "darwin", Arch: "arm64"},
		{ID: "windows-amd64", OS: "windows", Arch: "amd64"},
	}
}

// drawnDeclarations draws the declarations of one file: one to four of them, each shape
// and each visibility drawn on its own, so a shrink reaches the one declaration
// that carries a failure.
func drawnDeclarations() *rapid.Generator[[]drawnDeclaration] {
	one := rapid.Custom(func(t *rapid.T) drawnDeclaration {
		return drawnDeclaration{
			shape:    rapid.SampledFrom(shapes()).Draw(t, "the declaration's shape"),
			exported: rapid.Bool().Draw(t, "the declaration is exported"),
		}
	})
	return rapid.SliceOfN(one, 1, 4)
}

// drawnPackages draws one to three packages of one module, each of one to three
// production files, with an in-package test file and an external test file present
// or absent independently.
func drawnPackages() *rapid.Generator[[]drawnPackage] {
	one := rapid.Custom(func(t *rapid.T) drawnPackage {
		return drawnPackage{
			files:     rapid.SliceOfN(drawnDeclarations(), 1, 3).Draw(t, "the production files"),
			inPackage: rapid.Bool().Draw(t, "an in-package test file is present"),
			external:  rapid.Bool().Draw(t, "an external test file is present"),
		}
	})
	return rapid.SliceOfN(one, 1, 3)
}

// renderPackage names one drawn package and renders its files. A declaration is
// named for the positions of its file and of itself, so every declaration of a
// package is uniquely named however the draw shrinks.
//
// A test file imports nothing but the target: a package's test variant exists
// because a _test.go file does, and importing testing would make every generated
// package type-check that whole dependency graph.
func renderPackage(drawn drawnPackage, name string) generatedPackage {
	p := generatedPackage{
		name:      name,
		files:     make(map[string]string, len(drawn.files)+2),
		goFiles:   len(drawn.files),
		packages:  1,
		inPackage: drawn.inPackage,
	}
	for f, declared := range drawn.files {
		var b strings.Builder
		fmt.Fprintf(&b, "package %s\n", name)
		if f == 0 {
			b.WriteString("\nfunc anchor() {}\n\nfunc Anchor() {}\n")
		}
		for d, decl := range declared {
			declaredName := fmt.Sprintf("Decl%d%d", f, d)
			if !decl.exported {
				declaredName = fmt.Sprintf("decl%d%d", f, d)
			}
			b.WriteString("\n")
			b.WriteString(renderDeclaration(decl.shape, declaredName))
		}
		p.files[filepath.Join(name, fmt.Sprintf("gen%d.go", f))] = b.String()
	}

	if drawn.inPackage {
		p.files[filepath.Join(name, "gen_test.go")] = fmt.Sprintf(
			"package %s\n\nvar usesUnexported = anchor\n", name,
		)
		p.goFiles++
	}
	if drawn.external {
		p.files[filepath.Join(name, "external_test.go")] = fmt.Sprintf(
			"package %s_test\n\nimport \"example.test/generated/%s\"\n\nvar UsesExported = %s.Anchor\n", name, name, name,
		)
		p.goFiles++
		p.packages++
	}
	return p
}

// renderDeclaration renders one package-level declaration of a generated package.
func renderDeclaration(shape, name string) string {
	switch shape {
	case "func":
		return "func " + name + "() {}\n"
	case "struct":
		return "type " + name + " struct {\n\tfield int\n\tShared []struct{ Inner int }\n}\n"
	case "const":
		return "const " + name + " = 1\n"
	case "var":
		return "var " + name + " = 1\n"
	case "interface":
		return "type " + name + " interface {\n\tmethod() error\n\tExported() error\n}\n"
	default:
		return "type " + name + " int\n\nfunc (v " + name + ") String() string { return \"\" }\n"
	}
}

// writeModule writes one rendered package set as a module in a directory of its
// own under base, which owns the directory's lifetime.
func writeModule(t failureSink, base string, generated []generatedPackage) string {
	t.Helper()

	dir, err := os.MkdirTemp(base, "module")
	if err != nil {
		t.Fatalf("Setup: create a module directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.test/generated\n\ngo 1.27.1\n"), 0o600); err != nil {
		t.Fatalf("Setup: write go.mod: %v", err)
	}
	for _, p := range generated {
		for _, name := range slices.Sorted(maps.Keys(p.files)) {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
			}
			if err := os.WriteFile(path, []byte(p.files[name]), 0o600); err != nil {
				t.Fatalf("Setup: write %s: %v", path, err)
			}
		}
	}
	return dir
}

// loadedModules is the number of module-and-configuration pairs the property is
// stated over.
//
// One load of a generated module costs about a second under the race detector: a
// module carrying any test file makes the toolchain synthesize a test main, and the
// load takes the syntax and the types of that main's whole dependency graph, which is
// the testing package's. A test file is the property's premise, so the cost cannot be
// drawn away, and the number of loads is therefore what the property costs. It is this
// number and not the iteration count: an iteration draws one pair and reads the
// enumeration of it, which is where every assertion of the property is.
//
// Four modules per build configuration is what the number buys.
const loadedModules = 16

// loadedModule is one pair the property draws: the packages as they were generated,
// the configuration they were loaded under, and the load.
type loadedModule struct {
	result    *load.Result
	root      string
	generated []generatedPackage
	config    load.Configuration
}

// loadedGeneratedModules generates the modules the property draws from and loads each
// of them once, under one configuration each so that every configuration is loaded.
//
// The modules are the fixed examples of the package generator, one per seed, so the
// set is the same on every run and a failure names a seed a re-run reproduces.
func loadedGeneratedModules(t *testing.T) []*loadedModule {
	t.Helper()

	base := t.TempDir()
	built := configurations()
	held := make([]*loadedModule, loadedModules)
	for seed := range held {
		drawn := drawnPackages().Example(seed)
		generated := make([]generatedPackage, 0, len(drawn))
		for index, p := range drawn {
			generated = append(generated, renderPackage(p, fmt.Sprintf("p%02d", index)))
		}
		c := built[seed%len(built)]
		result, root := loadDirUnder(t.Context(), t, writeModule(t, base, generated), c)
		held[seed] = &loadedModule{result: result, root: root, generated: generated, config: c}
	}
	return held
}

// Property dead-code-suite/P1: for any target tree, any build configuration and
// any number of package variants of one source file, each declaration appears
// exactly once in the symbol graph.
//
// The enumeration takes one configuration's load, so an iteration draws one of the
// loads the property was stated over.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a
// deeper local run.
func TestProperty01OneDeclarationPerSourceSite(t *testing.T) {
	loaded := loadedGeneratedModules(t)

	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.IntRange(0, loadedModules-1).Draw(t, "the generated module's seed")
		checkOneDeclarationPerSourceSite(t, loaded[seed])
	})
}

// checkOneDeclarationPerSourceSite is the property's own body over one load: the
// premise about the variants the toolchain returned, then the enumeration and what it
// holds per source site, per file and per package.
func checkOneDeclarationPerSourceSite(t *rapid.T, one *loadedModule) {
	// The premise: an in-package test file makes the toolchain return a
	// variant that type-checks the production files a second time.
	variants := make(map[string]int)
	for _, p := range one.result.Packages {
		variants[p.PkgPath]++
	}
	for _, p := range one.generated {
		if want, got := 1+boolToInt(p.inPackage), variants["example.test/generated/"+p.name]; got != want {
			t.Fatalf("load returned %d variants of %s under %s, want %d\n%s",
				got, p.name, one.config.ID, want, describe(p))
		}
	}

	symbols, err := Symbols(one.result, one.root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols over %d generated packages under %s error: %v", len(one.generated), one.config.ID, err)
	}
	if len(symbols) == 0 {
		t.Fatalf("Symbols over %d generated packages under %s returned nothing", len(one.generated), one.config.ID)
	}

	at := make(map[SymbolID][]string, len(symbols))
	for _, s := range symbols {
		at[s.ID] = append(at[s.ID], s.Kind.String()+" "+s.Name)
	}
	for _, id := range slices.Sorted(maps.Keys(at)) {
		if names := at[id]; len(names) != 1 {
			t.Fatalf("Symbols holds %d symbols at %s (%v), want 1", len(names), id, names)
		}
	}

	files, packageSymbols := make(map[string]int), make(map[string]int)
	for _, s := range symbols {
		owner, _, _ := strings.Cut(s.Pos.Filename, "/")
		switch s.Kind {
		case KindFile:
			files[owner]++
		case KindPackage:
			packageSymbols[owner]++
		}
	}
	for _, p := range one.generated {
		if got := files[p.name]; got != p.goFiles {
			t.Fatalf("Symbols holds %d file symbols under %s, want %d\n%s",
				got, p.name, p.goFiles, describe(p))
		}
		if got := packageSymbols[p.name]; got != p.packages {
			t.Fatalf("Symbols holds %d package symbols under %s, want %d\n%s",
				got, p.name, p.packages, describe(p))
		}
	}
}

// boolToInt counts a present file or package.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// describe prints a generated package so a failure carries the source that
// produced it rather than only its name.
func describe(p generatedPackage) string {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(p.files)) {
		fmt.Fprintf(&b, "-- %s --\n%s", name, p.files[name])
	}
	return b.String()
}
