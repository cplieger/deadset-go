package load

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/scope"
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

func TestLoadReportsThePackageItsTestVariantAndTheTestBinary(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, "clean")

	want := []string{
		"example.com/clean",
		"example.com/clean [example.com/clean.test]",
		"example.com/clean.test",
	}
	if ids := packageIDs(got); !slices.Equal(ids, want) {
		t.Errorf("Load(testdata/clean) package ids = %v, want %v", ids, want)
	}
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

	got, err := Load(t.Context(), fixtureScope(t, "typeerror"), c)

	var loadErr *Error
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load(testdata/typeerror) = _, %v, want a *load.Error", err)
	}
	if !reflect.DeepEqual(got, Result{}) {
		t.Errorf("Load(testdata/typeerror) = %+v, want the zero Result so no finding list follows", got)
	}
	if loadErr.Configuration != c.ID {
		t.Errorf("Load(testdata/typeerror) error names configuration %q, want %q", loadErr.Configuration, c.ID)
	}
	// The fixture holds one error per file and an in-package test variant that
	// type-checks both files again, so a walk that keys on the source site
	// reports two diagnostics and one that does not reports four.
	if len(loadErr.Diagnostics) != 2 {
		t.Fatalf("Load(testdata/typeerror) reported %d diagnostics, want 2: %v", len(loadErr.Diagnostics), loadErr.Diagnostics)
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
		t.Errorf("Load(testdata/typeerror) named files %v, want %v", files, want)
	}
	rendered := loadErr.Error()
	for _, d := range loadErr.Diagnostics {
		if !strings.Contains(rendered, d.Message) {
			t.Errorf("(*Error).Error() = %q, want it to contain %q", rendered, d.Message)
		}
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

func TestLoadRecordsOnlyTheFileCgoAloneExcludes(t *testing.T) {
	stableToolchain(t)
	// A host that enables cgo must not change the result: the load sets
	// CGO_ENABLED=0 after the ambient environment, so it wins.
	t.Setenv("CGO_ENABLED", "1")

	got := loadFixture(t, "cgo")

	if want := []string{"bridge.go", "bridge_tagged.go"}; !slices.Equal(got.ExcludedByCgo, want) {
		t.Errorf("Load(testdata/cgo).ExcludedByCgo = %v, want %v", got.ExcludedByCgo, want)
	}
	pkg := findPackage(got, "example.com/cgo")
	if pkg == nil {
		t.Fatalf("Load(testdata/cgo) reported no example.com/cgo package, ids = %v", packageIDs(got))
	}
	ignored := baseNames(pkg.IgnoredFiles)
	// Each of these is ignored for a reason cgo does not decide alone, so each
	// stays in the population the build configurations reason about.
	for _, name := range []string{"unselected.go", "bridge_windows.go", "only_cgo_tag.go", "helper_plan9.c"} {
		if !slices.Contains(ignored, name) {
			t.Errorf("IgnoredFiles = %v, want it to contain %s", ignored, name)
		}
		if slices.Contains(got.ExcludedByCgo, name) {
			t.Errorf("ExcludedByCgo = %v, want it not to contain %s", got.ExcludedByCgo, name)
		}
	}
	if compiled := baseNames(pkg.GoFiles); slices.Contains(compiled, "bridge.go") {
		t.Errorf("GoFiles = %v, want it not to contain bridge.go: the load disables cgo", compiled)
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
			wantErr: ErrConfiguration,
		},
		"configuration with no architecture": {
			doc:     func(t *testing.T) scope.Document { return fixtureScope(t, "clean") },
			config:  Configuration{ID: "linux-amd64", OS: "linux"},
			wantErr: ErrConfiguration,
		},
		"declared consumer": {
			doc: func(t *testing.T) scope.Document {
				doc := fixtureScope(t, "clean")
				doc.Consumers = []scope.Module{{Role: scope.RoleConsumer, Path: "/nowhere"}}
				return doc
			},
			config:  HostConfiguration(),
			wantErr: ErrConsumersUnimplemented,
		},
		"target with no path": {
			doc:     func(*testing.T) scope.Document { return scope.Document{} },
			config:  HostConfiguration(),
			wantErr: scope.ErrNoTarget,
		},
		"absent target": {
			doc: func(*testing.T) scope.Document {
				return scope.Document{Target: scope.Module{Role: scope.RoleTarget, Path: absent}}
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
