package load

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// stableToolchain removes the ambient toolchain settings that would otherwise
// decide what the load sees, so a fixture stages the same packages on every host.
func stableToolchain(t *testing.T) {
	t.Helper()
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
}

// fixtureScope is the scope of the testdata module named by dir, resolved through
// the production reader.
func fixtureScope(t *testing.T, dir string) scope.Document {
	t.Helper()
	doc, err := scope.ForDir(filepath.Join("testdata", dir))
	if err != nil {
		t.Fatalf("scope.ForDir(testdata/%s) = _, %v, want no error", dir, err)
	}
	return doc
}

// loadFixture loads the testdata module named by dir under the host
// configuration and fails the test when the load does not succeed.
func loadFixture(t *testing.T, dir string) Result {
	t.Helper()
	got, err := Load(t.Context(), fixtureScope(t, dir), HostConfiguration())
	if err != nil {
		t.Fatalf("Load(testdata/%s) = _, %v, want no error", dir, err)
	}
	return got
}

// packageIDs is the sorted set of package identifiers a result carries.
func packageIDs(r Result) []string {
	ids := make([]string, 0, len(r.Packages))
	for _, p := range r.Packages {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	return ids
}

// findPackage returns the loaded package with the given import path, or nil.
func findPackage(r Result, pkgPath string) *packages.Package {
	for _, p := range r.Packages {
		if p.PkgPath == pkgPath && p.ForTest == "" && !strings.HasSuffix(p.ID, ".test") {
			return p
		}
	}
	return nil
}

// baseNames renders a file list as its base names, so an assertion does not
// depend on where the repository sits.
func baseNames(files []string) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	slices.Sort(names)
	return names
}

func TestHostConfiguration(t *testing.T) {
	got := HostConfiguration()
	want := Configuration{ID: runtime.GOOS + "-" + runtime.GOARCH, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HostConfiguration() = %+v, want %+v", got, want)
	}
	if got.Tags != nil {
		t.Errorf("HostConfiguration().Tags = %v, want nil", got.Tags)
	}
}

func TestBuildFlags(t *testing.T) {
	cases := map[string]struct {
		tags []string
		want []string
	}{
		"none":  {tags: nil, want: nil},
		"empty": {tags: []string{}, want: nil},
		"one":   {tags: []string{"integration"}, want: []string{"-tags=integration"}},
		"two":   {tags: []string{"integration", "netgo"}, want: []string{"-tags=integration,netgo"}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := buildFlags(test.tags); !slices.Equal(got, test.want) {
				t.Errorf("buildFlags(%v) = %v, want %v", test.tags, got, test.want)
			}
		})
	}
}

func TestLoadDropsTheSynthesizedTestMain(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, "clean")

	// The fixture carries an in-package and an external test file, so a test load
	// of it reports the plain package and both variants, and the toolchain
	// synthesizes the test binary the result must not carry.
	want := []string{
		"example.com/clean",
		"example.com/clean [example.com/clean.test]",
		"example.com/clean_test [example.com/clean.test]",
	}
	if ids := packageIDs(got); !slices.Equal(ids, want) {
		t.Errorf("Load(testdata/clean) package ids = %v, want %v", ids, want)
	}

	root, err := filepath.Abs(filepath.Join("testdata", "clean"))
	if err != nil {
		t.Fatalf("Setup: absolute path of testdata/clean: %v", err)
	}
	for _, p := range got.Packages {
		for _, file := range slices.Concat(p.GoFiles, p.CompiledGoFiles) {
			if rel, err := filepath.Rel(root, file); err != nil || !filepath.IsLocal(rel) {
				t.Errorf("Load(testdata/clean) package %s compiles %s, want every file under %s",
					p.ID, file, root)
			}
		}
	}
}

func TestLoadReportsTheConfigurationAndOneFileSet(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, "clean")

	if got.Fset == nil {
		t.Error("Load(testdata/clean).Fset = nil, want one FileSet for the configuration")
	}
	if len(got.ExcludedByCgo) != 0 {
		t.Errorf("Load(testdata/clean).ExcludedByCgo = %v, want empty", got.ExcludedByCgo)
	}
	if !reflect.DeepEqual(got.Configuration, HostConfiguration()) {
		t.Errorf("Load(testdata/clean).Configuration = %+v, want %+v", got.Configuration, HostConfiguration())
	}
}

func TestLoadKeysOnePositionAcrossVariantsAndReportsEveryDiagnostic(t *testing.T) {
	stableToolchain(t)
	c := HostConfiguration()

	got, err := Load(t.Context(), unparsedScope(t, "syntaxerror"), c)

	var loadErr *Error
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load(testdata/syntaxerror) = _, %v, want a *load.Error", err)
	}
	if !reflect.DeepEqual(got, Result{}) {
		t.Errorf("Load(testdata/syntaxerror) = %+v, want the zero Result so no finding list follows", got)
	}
	if loadErr.Configuration != c.ID {
		t.Errorf("Load(testdata/syntaxerror) error names configuration %q, want %q", loadErr.Configuration, c.ID)
	}
	// The fixture holds one error per file and an in-package test variant that
	// compiles both files again, so a walk that keys on the source site
	// reports two diagnostics and one that does not reports four.
	if len(loadErr.Diagnostics) != 2 {
		t.Fatalf("Load(testdata/syntaxerror) reported %d diagnostics, want 2: %v", len(loadErr.Diagnostics), loadErr.Diagnostics)
	}
	files := make([]string, 0, len(loadErr.Diagnostics))
	for _, d := range loadErr.Diagnostics {
		file, _, ok := strings.Cut(d.Position, ":")
		if !ok {
			t.Errorf("diagnostic %+v carries no file:line:col position", d)
			continue
		}
		files = append(files, filepath.Base(file))
		if d.Message == "" {
			t.Errorf("diagnostic %+v carries no message", d)
		}
	}
	slices.Sort(files)
	if want := []string{"first.go", "second.go"}; !slices.Equal(files, want) {
		t.Errorf("Load(testdata/syntaxerror) named files %v, want %v", files, want)
	}
	rendered := loadErr.Error()
	for _, d := range loadErr.Diagnostics {
		if !strings.Contains(rendered, d.Message) {
			t.Errorf("(*Error).Error() = %q, want it to contain %q", rendered, d.Message)
		}
	}
}

// A type error in a package of the target fails nothing: the load records it in the
// package's TypeErrors, once per variant that checks the file, and the analysis skips
// the declaration that holds it.
func TestLoadRecordsATypeErrorOfTheTargetWithoutFailing(t *testing.T) {
	stableToolchain(t)

	got, err := Load(t.Context(), fixtureScope(t, "typeerror"), HostConfiguration())
	if err != nil {
		t.Fatalf("Load(testdata/typeerror) = _, %v, want the package set", err)
	}
	positions := make(map[string]bool)
	for _, p := range got.Packages {
		if len(p.Errors) != 0 {
			t.Errorf("Load(testdata/typeerror) package %s carries the errors %v, want none", p.ID, p.Errors)
		}
		for _, e := range p.TypeErrors {
			positions[filepath.Base(e.Fset.Position(e.Pos).Filename)] = true
		}
	}
	if want := map[string]bool{"first.go": true, "second.go": true}; !maps.Equal(positions, want) {
		t.Errorf("Load(testdata/typeerror) recorded type errors in %v, want %v", positions, want)
	}
}

func TestLoadReportsAConstraintExcludedFileAsIgnoredAndNotAsCompiled(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, "constraint")

	pkg := findPackage(got, "example.com/constraint")
	if pkg == nil {
		t.Fatalf("Load(testdata/constraint) reported no example.com/constraint package, ids = %v", packageIDs(got))
	}
	if ignored := baseNames(pkg.IgnoredFiles); !slices.Contains(ignored, "unselected.go") {
		t.Errorf("IgnoredFiles = %v, want it to contain unselected.go", ignored)
	}
	if compiled := baseNames(pkg.GoFiles); slices.Contains(compiled, "unselected.go") {
		t.Errorf("GoFiles = %v, want it not to contain unselected.go", compiled)
	}
	if compiled := baseNames(pkg.CompiledGoFiles); slices.Contains(compiled, "unselected.go") {
		t.Errorf("CompiledGoFiles = %v, want it not to contain unselected.go", compiled)
	}
	if len(got.ExcludedByCgo) != 0 {
		t.Errorf("ExcludedByCgo = %v, want empty: no file of the fixture imports \"C\"", got.ExcludedByCgo)
	}
}

func TestLoadOmitsADirectoryNoFileOfWhichIsSelected(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, "vanished")

	if pkg := findPackage(got, "example.com/vanished/absent"); pkg != nil {
		t.Errorf("Load(testdata/vanished) reported package %q, want it absent from the load", pkg.ID)
	}
	if pkg := findPackage(got, "example.com/vanished"); pkg == nil {
		t.Errorf("Load(testdata/vanished) reported no example.com/vanished package, ids = %v", packageIDs(got))
	}
}

// unparsedSuffix ends the name of a fixture file that does not parse, which is kept
// under another extension so no formatter of the repository reads it, and which
// unparsedScope writes under its Go name.
const unparsedSuffix = ".unparsed"

// unparsedScope copies the testdata module named by dir into a directory of its own,
// each file ending in unparsedSuffix written under its Go name, and returns the
// scope of the copy.
func unparsedScope(t *testing.T, dir string) scope.Document {
	t.Helper()
	dst := t.TempDir()
	copyFixture(t, dir, dst)
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", dst, err)
	}
	for _, e := range entries {
		if named, ok := strings.CutSuffix(e.Name(), unparsedSuffix); ok {
			if err := os.Rename(filepath.Join(dst, e.Name()), filepath.Join(dst, named)); err != nil {
				t.Fatalf("Setup: rename %s: %v", e.Name(), err)
			}
		}
	}
	doc, err := scope.ForDir(dst)
	if err != nil {
		t.Fatalf("scope.ForDir(a copy of testdata/%s) = _, %v, want no error", dir, err)
	}
	return doc
}

// copyFixture copies the testdata module named by dir into dst.
func copyFixture(t *testing.T, dir, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", dst, err)
	}
	entries, err := os.ReadDir(filepath.Join("testdata", dir))
	if err != nil {
		t.Fatalf("Setup: read testdata/%s: %v", dir, err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join("testdata", dir, e.Name()))
		if err != nil {
			t.Fatalf("Setup: read testdata/%s/%s: %v", dir, e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), body, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", filepath.Join(dst, e.Name()), err)
		}
	}
}

func TestLoadPinsGoworkOffAgainstAnAmbientWorkspace(t *testing.T) {
	// These two tests stage the ambient settings the pin answers, so they are the
	// tests in the package that do not neutralise them first.
	t.Setenv("GOWORK", "")

	root := t.TempDir()
	module := filepath.Join(root, "module")
	copyFixture(t, "constraint", module)
	workspace := filepath.Join(root, "go.work")
	if err := os.WriteFile(workspace, []byte("go 1.27.1\n\nuse ./absent\n"), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", workspace, err)
	}
	doc, err := scope.ForDir(module)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s) = _, %v, want no error", module, err)
	}

	got, err := Load(t.Context(), doc, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(a module under a go.work naming an absent module) = _, %v, want no error", err)
	}
	if pkg := findPackage(got, "example.com/constraint"); pkg == nil {
		t.Errorf("Load() reported no example.com/constraint package, ids = %v", packageIDs(got))
	}
}

func TestLoadPinsGoflagsEmptyAgainstAnAmbientTagList(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-tags=plan9")

	got, err := Load(t.Context(), fixtureScope(t, "constraint"), HostConfiguration())
	if err != nil {
		t.Fatalf("Load(testdata/constraint) = _, %v, want no error", err)
	}

	pkg := findPackage(got, "example.com/constraint")
	if pkg == nil {
		t.Fatalf("Load() reported no example.com/constraint package, ids = %v", packageIDs(got))
	}
	if compiled := baseNames(pkg.GoFiles); slices.Contains(compiled, "unselected.go") {
		t.Errorf("GoFiles = %v, want it not to contain unselected.go: an ambient tag list selects no file", compiled)
	}
	if ignored := baseNames(pkg.IgnoredFiles); !slices.Contains(ignored, "unselected.go") {
		t.Errorf("IgnoredFiles = %v, want it to contain unselected.go", ignored)
	}
}

func TestLoadSelectsTheFileAConfiguredTagNames(t *testing.T) {
	stableToolchain(t)
	c := HostConfiguration()
	c.ID += "-plan9"
	c.Tags = []string{"plan9"}

	got, err := Load(t.Context(), fixtureScope(t, "constraint"), c)
	if err != nil {
		t.Fatalf("Load(testdata/constraint, %s) = _, %v, want no error", c.ID, err)
	}

	pkg := findPackage(got, "example.com/constraint")
	if pkg == nil {
		t.Fatalf("Load() reported no example.com/constraint package, ids = %v", packageIDs(got))
	}
	if compiled := baseNames(pkg.GoFiles); !slices.Contains(compiled, "unselected.go") {
		t.Errorf("GoFiles = %v, want it to contain unselected.go: the configuration names the tag that selects it", compiled)
	}
}

func TestLoadIsRepeatable(t *testing.T) {
	stableToolchain(t)
	first := loadFixture(t, "cgo")
	second := loadFixture(t, "cgo")

	if !slices.Equal(first.ExcludedByCgo, second.ExcludedByCgo) {
		t.Errorf("ExcludedByCgo = %v then %v, want the same list twice", first.ExcludedByCgo, second.ExcludedByCgo)
	}
	if a, b := packageIDs(first), packageIDs(second); !slices.Equal(a, b) {
		t.Errorf("package ids = %v then %v, want the same set twice", a, b)
	}
	// The opaque-C check runs inside the load, so what it accepted and what it
	// declared are part of what two loads have to agree on.
	a, b := findPackage(first, "example.com/cgo"), findPackage(second, "example.com/cgo")
	if a == nil || b == nil {
		t.Fatalf("Load(testdata/cgo) reported no example.com/cgo package, ids = %v", packageIDs(first))
	}
	if !slices.Equal(a.GoFiles, b.GoFiles) {
		t.Errorf("GoFiles = %v then %v, want the same list twice", baseNames(a.GoFiles), baseNames(b.GoFiles))
	}
	if !slices.Equal(declaredNames(a), declaredNames(b)) {
		t.Errorf("declarations = %v then %v, want the same set twice", declaredNames(a), declaredNames(b))
	}
}

func TestLoadRefusals(t *testing.T) {
	stableToolchain(t)
	absent := filepath.Join(t.TempDir(), "no-such-target")

	cases := map[string]struct {
		doc     func(t *testing.T) scope.Document
		config  Configuration
		cancel  bool
		wantErr error
	}{
		"cancelled context": {
			doc:     func(t *testing.T) scope.Document { return fixtureScope(t, "clean") },
			config:  HostConfiguration(),
			cancel:  true,
			wantErr: context.Canceled,
		},
		// Cancellation is read before anything else, so a cancelled run reports
		// the cancellation rather than a fault it would otherwise have found,
		// and spawns no toolchain to find one.
		"cancelled context outranks every other refusal": {
			doc:     func(*testing.T) scope.Document { return scope.Document{} },
			config:  Configuration{},
			cancel:  true,
			wantErr: context.Canceled,
		},
		"configuration with no identifier": {
			doc:     func(t *testing.T) scope.Document { return fixtureScope(t, "clean") },
			config:  Configuration{OS: runtime.GOOS, Arch: runtime.GOARCH},
			wantErr: errConfiguration,
		},
		"configuration with no architecture": {
			doc:     func(t *testing.T) scope.Document { return fixtureScope(t, "clean") },
			config:  Configuration{ID: "linux-amd64", OS: "linux"},
			wantErr: errConfiguration,
		},
		"target with no path": {
			doc:     func(*testing.T) scope.Document { return scope.Document{} },
			config:  HostConfiguration(),
			wantErr: scope.ErrNoTarget,
		},
		"absent target": {
			doc: func(*testing.T) scope.Document {
				return scope.Document{Target: scope.Module{Path: absent}}
			},
			config:  HostConfiguration(),
			wantErr: fs.ErrNotExist,
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if test.cancel {
				cancellable, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancellable
			}
			got, err := Load(ctx, test.doc(t), test.config)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("Load() = _, %v, want an error matching %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(got, Result{}) {
				t.Errorf("Load() = %+v, want the zero Result", got)
			}
		})
	}
}

func TestErrorRendersTheConfigurationTheCountAndEveryDiagnostic(t *testing.T) {
	cases := map[string]struct {
		err  *Error
		want string
	}{
		"one diagnostic": {
			err: &Error{
				Configuration: "linux-amd64",
				Diagnostics:   []Diagnostic{{Package: "example.com/a", Position: "a.go:1:2", Message: "boom"}},
			},
			want: "load linux-amd64: 1 error\n  a.go:1:2: boom",
		},
		"two diagnostics, one without a position": {
			err: &Error{
				Configuration: "darwin-arm64",
				Diagnostics: []Diagnostic{
					{Package: "example.com/a", Message: "no Go files"},
					{Package: "example.com/a", Position: "a.go:1:2", Message: "boom"},
				},
			},
			want: "load darwin-arm64: 2 errors\n  no Go files\n  a.go:1:2: boom",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := test.err.Error(); got != test.want {
				t.Errorf("(*Error).Error() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSameDiagnosticKeysAPositionedProblemOnItsSourceSite(t *testing.T) {
	positioned := Diagnostic{Package: "example.com/a", Position: "a.go:1:2", Message: "boom"}
	variant := Diagnostic{Package: "example.com/a [example.com/a.test]", Position: "a.go:1:2", Message: "boom"}
	unpositioned := Diagnostic{Package: "example.com/a", Message: "no Go files"}
	otherPackage := Diagnostic{Package: "example.com/b", Message: "no Go files"}

	cases := map[string]struct {
		a, b Diagnostic
		want bool
	}{
		"same site reported by two variants": {a: positioned, b: variant, want: true},
		"different message at one site":      {a: positioned, b: Diagnostic{Package: positioned.Package, Position: positioned.Position, Message: "other"}, want: false},
		"no position, same package":          {a: unpositioned, b: unpositioned, want: true},
		"no position, other package":         {a: unpositioned, b: otherPackage, want: false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sameDiagnostic(test.a, test.b); got != test.want {
				t.Errorf("sameDiagnostic(%+v, %+v) = %t, want %t", test.a, test.b, got, test.want)
			}
		})
	}
}

// A package whose every file but its external test the configuration excludes is
// one the pattern names and the configuration does not build. The toolchain
// reports it with an error only while its build cache holds no entry for it, so
// the load answers the same on a cold cache as on a warm one.
func TestLoadReadsAPackageTheConfigurationBuildsNoFileOfTheSameOnAColdBuildCache(t *testing.T) {
	stableToolchain(t)
	t.Setenv("GOCACHE", t.TempDir())

	got, err := Load(t.Context(), fixtureScope(t, "unbuilt"), Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Load(testdata/unbuilt) on an empty build cache = _, %v, want no error", err)
	}
	if p := findPackage(got, "example.com/unbuilt"); p == nil || len(p.GoFiles) != 1 {
		t.Errorf("Load(testdata/unbuilt) carries %v for the built package, want its one file", p)
	}
}

// cgoDependencyScope writes a target that imports a dependency, reached through a
// replace so nothing is fetched, whose only Go file the given constraint line
// heads, and returns the target's scope.
func cgoDependencyScope(t *testing.T, header string) scope.Document {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"app/go.mod": "module example.com/app\n\ngo 1.27.1\n\n" +
			"require example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n",
		"app/app.go": "package app\n\nimport \"example.com/dep\"\n\n" +
			"// Size reads the dependency.\nfunc Size() int { return dep.Size() }\n",
		"dep/go.mod": "module example.com/dep\n\ngo 1.27.1\n",
		"dep/dep.go": header + "package dep\n\n// #define SIZE 4\nimport \"C\"\n\n" +
			"// Size is what the target reads.\nfunc Size() int { return int(C.SIZE) }\n",
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	doc, err := scope.ForDir(filepath.Join(root, "app"))
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s) = _, %v, want no error", root, err)
	}
	return doc
}

// A dependency whose Go files a load excludes only because it disables cgo is a
// package the configuration builds, so the load answers, and the target's reference
// to a declaration it lost is a type error of the target.
func TestLoadReadsADependencyCgoAloneExcludes(t *testing.T) {
	stableToolchain(t)
	t.Setenv("GOCACHE", t.TempDir())

	got, err := Load(t.Context(), cgoDependencyScope(t, ""), HostConfiguration())
	if err != nil {
		t.Fatalf("Load(a target importing a cgo-only dependency) = _, %v, want no error", err)
	}
	p := findPackage(got, "example.com/app")
	if p == nil {
		t.Fatalf("Load() reported no example.com/app package, ids = %v", packageIDs(got))
	}
	if len(p.TypeErrors) == 0 {
		t.Errorf("Load() recorded no type error in example.com/app, want the one naming dep.Size")
	}
}

// A dependency a constraint other than cgo excludes is no package the configuration
// builds, so the load still fails on it.
func TestLoadFailsOnADependencyAnotherConstraintExcludes(t *testing.T) {
	stableToolchain(t)
	t.Setenv("GOCACHE", t.TempDir())

	_, err := Load(t.Context(), cgoDependencyScope(t, "//go:build never\n\n"), HostConfiguration())
	if failure, ok := errors.AsType[*Error](err); !ok || len(failure.Diagnostics) == 0 {
		t.Errorf("Load(a target importing a dependency no configuration builds) = _, %v, want a *Error with its diagnostics", err)
	}
}

// writeTree writes files under a fresh temporary directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	return root
}

// workspaceTree is a go.work listing two modules, app requiring lib through the
// workspace alone: no replace and no module sum, so only a workspace build
// resolves it.
func workspaceTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"go.work":     "go 1.27.1\n\nuse (\n\t./app\n\t./lib\n)\n",
		"app/go.mod":  "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/lib v0.0.0\n",
		"app/main.go": "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.Used() }\n",
		"lib/go.mod":  "module example.com/lib\n\ngo 1.27.1\n",
		"lib/lib.go":  "package lib\n\n// Used is what app calls.\nfunc Used() {}\n",
	})
}

// A module a go.work lists loads through that workspace, as the toolchain builds it
// there, and the workspace's other members are its dependencies rather than its own
// code.
func TestLoadReadsAMemberThroughTheWorkspaceThatListsIt(t *testing.T) {
	stableToolchain(t)
	root := workspaceTree(t)
	doc, err := scope.ForDir(filepath.Join(root, "app"))
	if err != nil {
		t.Fatalf("Setup: scope.ForDir = _, %v", err)
	}

	got, err := Load(t.Context(), doc, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(a workspace member requiring another member) = _, %v, want no error", err)
	}
	if p := findPackage(got, "example.com/app"); p == nil || p.Module == nil || !p.Module.Main {
		t.Errorf("Load() carries %v for example.com/app, want a package of the main module", p)
	}
	packages.Visit(got.Packages, nil, func(p *packages.Package) {
		if p.PkgPath == "example.com/lib" && (p.Module == nil || p.Module.Main) {
			t.Errorf("Load() carries example.com/lib with module %+v, want a dependency", p.Module)
		}
	})
	if len(got.Units) != 1 {
		t.Errorf("Load() read %d units, want the one module the target is", len(got.Units))
	}
}

// A target that is no module root holds every module below it, each loaded from
// its own directory, and a directory no build reads is not searched.
func TestLoadReadsEveryModuleUnderATargetThatIsNoModuleRoot(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"a/go.mod":                  "module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n",
		"a/main.go":                 "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"b/go.mod":                  "module example.com/b\n\ngo 1.27.1\n",
		"b/b.go":                    "package b\n\n// Used is what a calls.\nfunc Used() {}\n",
		"node_modules/x/go.mod":     "module example.com/x\n\ngo 1.27.1\n",
		"node_modules/x/x.go":       "package x\n",
		"b/testdata/fixture/go.mod": "module example.com/fixture\n\ngo 1.27.1\n",
		"vendor/v/go.mod":           "module example.com/v\n\ngo 1.27.1\n",
		"vendor/v/v.go":             "package v\n",
		".hidden/h/go.mod":          "module example.com/h\n\ngo 1.27.1\n",
		".hidden/h/h.go":            "package h\n",
		"_ignored/i/go.mod":         "module example.com/i\n\ngo 1.27.1\n",
		"_ignored/i/i.go":           "package i\n",
	})
	doc, err := scope.ForDir(root)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir = _, %v", err)
	}

	got, err := Load(t.Context(), doc, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(a tree of two modules) = _, %v, want no error", err)
	}
	var units []string
	for _, u := range got.Units {
		units = append(units, u.Path)
	}
	if want := []string{"example.com/a", "example.com/b"}; !slices.Equal(units, want) {
		t.Errorf("Load() read the units %v, want %v", units, want)
	}
	for _, path := range []string{"example.com/a", "example.com/b"} {
		if p := findPackage(got, path); p == nil || p.Module == nil || !p.Module.Main {
			t.Errorf("Load() carries %v for %s, want a package of a main module", p, path)
		}
	}
}

// A directory a module file above it governs is a directory of that module, and a
// module nested below it is no module of the target: the target is its own unit.
func TestUnitsOfADirectoryAModuleAboveGovernsIsTheDirectory(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":           "module example.com/r\n\ngo 1.27.1\n",
		"sub/sub.go":       "package sub\n",
		"sub/tool/go.mod":  "module example.com/tool\n\ngo 1.27.1\n",
		"sub/tool/main.go": "package main\n\nfunc main() {}\n",
	})
	target := filepath.Join(root, "sub")

	got, err := Units(t.Context(), target)
	if err != nil {
		t.Fatalf("Units(a directory of an enclosing module) = _, %v, want no error", err)
	}
	if want := []string{target}; !slices.Equal(got, want) {
		t.Errorf("Units() = %v, want %v", got, want)
	}
}

func TestLoadNamesThePackageClauseOfEveryGeneratedFile(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.27.1\n",
		"app.go":  "package main\n\nfunc main() {}\n",
		"wire.go": "// Code generated by a generator. DO NOT EDIT.\n\npackage main\n",
	})
	r, err := loadTree(t, root, ".")
	if err != nil {
		t.Fatalf("Setup: Load() = _, %v", err)
	}

	var files []string
	for _, clause := range r.Generated {
		files = append(files, filepath.Base(r.Fset.Position(clause).Filename))
	}
	if want := []string{"wire.go"}; !slices.Equal(files, want) {
		t.Errorf("Load().Generated names %v, want %v: only wire.go carries the generation marker", files, want)
	}
}

// A result narrowed to one unit names the generated files of that unit's
// packages alone.
func TestUnitNamesThePackageClauseOfTheUnitsGeneratedFilesAlone(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"a/go.mod":  "module example.com/a\n\ngo 1.27.1\n",
		"a/main.go": "package main\n\nfunc main() {}\n",
		"a/wire.go": "// Code generated by a generator. DO NOT EDIT.\n\npackage main\n",
		"b/go.mod":  "module example.com/b\n\ngo 1.27.1\n",
		"b/main.go": "package main\n\nfunc main() {}\n",
		"b/wire.go": "// Code generated by a generator. DO NOT EDIT.\n\npackage main\n",
	})
	r, err := loadTree(t, root, ".")
	if err != nil {
		t.Fatalf("Setup: Load() = _, %v", err)
	}
	if len(r.Generated) != 2 {
		t.Fatalf("Setup: Load().Generated holds %d clauses, want one per unit", len(r.Generated))
	}

	narrowed := r.Unit(filepath.Join(root, "a"))
	if narrowed == nil {
		t.Fatal("Unit(a) = nil, want the unit a")
	}
	var files []string
	for _, clause := range narrowed.Generated {
		rel, err := filepath.Rel(root, narrowed.Fset.Position(clause).Filename)
		if err != nil {
			t.Fatalf("filepath.Rel(%s) = _, %v", root, err)
		}
		files = append(files, filepath.ToSlash(rel))
	}
	if want := []string{"a/wire.go"}; !slices.Equal(files, want) {
		t.Errorf("Unit(a).Generated names %v, want %v: b's generated file is outside the unit", files, want)
	}
}
