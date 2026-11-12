package load

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
)

// consumedScope is the scope of the consumed fixture: its target module, and one
// consumer per named directory of the fixture.
func consumedScope(t *testing.T, consumers ...string) scope.Document {
	t.Helper()
	doc := fixtureScope(t, filepath.Join("consumed", "target"))
	for _, name := range consumers {
		path, err := filepath.Abs(filepath.Join("testdata", "consumed", name))
		if err != nil {
			t.Fatalf("Setup: resolve testdata/consumed/%s: %v", name, err)
		}
		doc.Consumers = append(doc.Consumers, scope.Module{Path: path})
	}
	return doc
}

// consumerPackageIDs is the sorted set of package identifiers one loaded consumer
// carries.
func consumerPackageIDs(c Consumer) []string {
	ids := make([]string, 0, len(c.Packages))
	for _, p := range c.Packages {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestLoadCarriesEveryDeclaredConsumerApartFromTheTarget(t *testing.T) {
	stableToolchain(t)
	doc := consumedScope(t, "consumer")

	got, err := loadScope(t.Context(), doc, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(the consumed fixture with one consumer) = _, %v, want no error", err)
	}

	// The target's own package set is what it is without a consumer: a consumer's
	// packages are never part of it, because the declarations a run reports on are
	// the target's alone.
	if want := []string{"example.com/consumed"}; !slices.Equal(packageIDs(got), want) {
		t.Errorf("Load().Packages = %v, want %v", packageIDs(got), want)
	}
	if len(got.Consumers) != 1 {
		t.Fatalf("Load().Consumers = %+v, want one consumer", got.Consumers)
	}
	if got.Consumers[0].ID != "example.com/consumer" {
		t.Errorf("Load().Consumers[0].ID = %q, want %q", got.Consumers[0].ID, "example.com/consumer")
	}
	// The consumer's own test variant carries the reference its test file makes,
	// and the synthesized test binary is dropped from a consumer's set as it is
	// from the target's.
	want := []string{"example.com/consumer", "example.com/consumer [example.com/consumer.test]"}
	if ids := consumerPackageIDs(got.Consumers[0]); !slices.Equal(ids, want) {
		t.Errorf("Load().Consumers[0] package identifiers = %v, want %v", ids, want)
	}
}

func TestLoadResolvesTheTargetOfAConsumerAtTheTargetRoot(t *testing.T) {
	stableToolchain(t)
	doc := consumedScope(t, "consumer")

	got, err := loadScope(t.Context(), doc, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(the consumed fixture with one consumer) = _, %v, want no error", err)
	}

	// The reference a consumer makes is a reference to the declaration written at
	// one position, so the target the consumer compiled has to be the target being
	// analyzed rather than a copy of it.
	resolved := ""
	packages.Visit(got.Consumers[0].Packages, nil, func(p *packages.Package) {
		if p.Module != nil && p.Module.Path == "example.com/consumed" {
			resolved = p.Module.Dir
		}
	})
	want := mainModule(got.Packages).Dir
	if resolved != want {
		t.Errorf("the consumer resolved example.com/consumed at %q, want %q", resolved, want)
	}
}

func TestLoadSharesOneFileSetWithEveryConsumer(t *testing.T) {
	stableToolchain(t)
	doc := consumedScope(t, "consumer")

	got, err := loadScope(t.Context(), doc, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(the consumed fixture with one consumer) = _, %v, want no error", err)
	}

	// One file set per configuration is what makes a declaration of the target
	// render to one position whichever module's load reached it, so a consumer's
	// load is given the result's own file set and never a second one.
	target := got.Fset.Position(got.Packages[0].Syntax[0].FileStart)
	if !target.IsValid() {
		t.Fatalf("the target's first file has no position in the result's file set")
	}
	for _, p := range got.Consumers[0].Packages {
		for _, f := range p.Syntax {
			if at := got.Fset.Position(f.FileStart); !at.IsValid() {
				t.Errorf("a consumer file has no position in the result's file set")
			}
		}
	}
}

func TestLoadRefusesAConsumerItCannotCount(t *testing.T) {
	cases := map[string]struct {
		consumers []string
		declared  func(t *testing.T) scope.Document
		wantText  []string
	}{
		"a module identity the scope disagrees with": {
			declared: func(t *testing.T) scope.Document {
				doc := consumedScope(t, "consumer")
				doc.Consumers[0].ID = "example.com/elsewhere"
				return doc
			},
			wantText: []string{"example.com/elsewhere", "example.com/consumer"},
		},
		"the target module itself": {
			declared: func(t *testing.T) scope.Document {
				doc := consumedScope(t)
				doc.Consumers = append(doc.Consumers, scope.Module{Path: doc.Target.Path})
				return doc
			},
			wantText: []string{"example.com/consumed"},
		},
		"one module declared twice": {
			declared: func(t *testing.T) scope.Document { return consumedScope(t, "consumer", "consumer") },
			wantText: []string{"example.com/consumer", "twice"},
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			stableToolchain(t)
			got, err := loadScope(t.Context(), test.declared(t), HostConfiguration())

			if !errors.Is(err, errConsumer) {
				t.Fatalf("Load() = _, %v, want an error matching errConsumer", err)
			}
			for _, text := range test.wantText {
				if !strings.Contains(err.Error(), text) {
					t.Errorf("Load() error = %q, want it to name %q", err, text)
				}
			}
			if !reflect.DeepEqual(got, Result{}) {
				t.Errorf("Load() = %+v, want the zero Result", got)
			}
		})
	}
}

func TestLoadRefusesAnAbsentConsumerAsAMissingConsumer(t *testing.T) {
	stableToolchain(t)
	absent := filepath.Join(sandbox.Dir(t), "missing")
	doc := consumedScope(t)
	doc.Consumers = append(doc.Consumers, scope.Module{Path: absent})

	got, err := loadScope(t.Context(), doc, HostConfiguration())

	assertMissingConsumer(t, "a scope naming an absent consumer", err, doc.Target.Path, absent)
	if !reflect.DeepEqual(got, Result{}) {
		t.Errorf("Load(a scope naming an absent consumer) = %+v, want the zero Result", got)
	}
}

// A declared consumer whose required module is not installed is present and not
// installed, which is the second arm of the missing-consumer class.
func TestLoadRefusesAConsumerWhoseDependenciesAreNotInstalledAsAMissingConsumer(t *testing.T) {
	stableToolchain(t)
	consumer := sandbox.Dir(t)
	for name, body := range map[string]string{
		"go.mod":  "module example.com/uninstalled\n\ngo 1.27.1\n\nrequire example.com/nowhere v1.0.0\n",
		"main.go": "package main\n\nimport \"example.com/nowhere\"\n\nfunc main() { nowhere.Run() }\n",
	} {
		if err := sandbox.WriteFile(filepath.Join(consumer, name), []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", name, err)
		}
	}
	doc := consumedScope(t)
	doc.Consumers = append(doc.Consumers, scope.Module{Path: consumer})

	_, err := loadScope(t.Context(), doc, HostConfiguration())

	assertMissingConsumer(t, "a scope naming a consumer whose import nothing provides", err, doc.Target.Path, consumer, "example.com/nowhere")
}

// assertMissingConsumer checks that a load ended with one missing-consumer setup
// failure whose line names the consumer at dir relative to the target root, never
// by its absolute path, and every string of want.
func assertMissingConsumer(t *testing.T, what string, err error, root, dir string, want ...string) {
	t.Helper()
	setup, ok := errors.AsType[*failure.Error](err)
	if !ok || len(setup.Failures) == 0 {
		t.Fatalf("Load(%s) = _, %v, want a setup *failure.Error", what, err)
	}
	for _, one := range setup.Failures {
		if one.Class != vocab.SetupMissingConsumer {
			t.Errorf("Load(%s) failed with the class %q, want %q", what, one.Class, vocab.SetupMissingConsumer)
		}
		relative := scope.RootOf(root).Rel(dir)
		for _, text := range append(want, "consumer "+relative) {
			if !strings.Contains(one.Class+": "+one.Detail, text) {
				t.Errorf("Load(%s) line = %q, want it to name %q", what, one.Class+": "+one.Detail, text)
			}
		}
		// Where root and dir share only /, the relative spelling ends with dir itself.
		if strings.Contains(strings.ReplaceAll(one.Detail, relative, ""), dir) {
			t.Errorf("Load(%s) line = %q, want no absolute path in it", what, one.Class+": "+one.Detail)
		}
	}
}

func TestLoadRefusesAConsumerThatDoesNotTypeCheck(t *testing.T) {
	stableToolchain(t)
	doc := consumedScope(t, "broken")

	got, err := loadScope(t.Context(), doc, HostConfiguration())

	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("Load(a scope naming a consumer that does not type-check) = _, %v, want a *Error", err)
	}
	if want := scope.RootOf(doc.Target.Path).Rel(doc.Consumers[0].Path); failure.Module != want || filepath.Base(want) != "broken" {
		t.Errorf("(*Error).Module = %q, want the broken consumer relative to the target root, %q", failure.Module, want)
	}
	if len(failure.Diagnostics) == 0 {
		t.Error("(*Error).Diagnostics is empty, want the toolchain's own diagnostics")
	}
	if !reflect.DeepEqual(got, Result{}) {
		t.Errorf("Load() = %+v, want the zero Result", got)
	}
}

func TestLoadRefusesAConsumerRequiringAModuleNoCacheHolds(t *testing.T) {
	stableToolchain(t)
	doc := consumedScope(t, "uncached")

	got, err := loadScope(t.Context(), doc, HostConfiguration())

	// GOPROXY=off is what makes the analysis's no-network rule the toolchain's
	// rule: a module the local cache does not hold is not fetched, so the run ends
	// with the toolchain's own message naming the module rather than reaching the
	// network for it.
	if err == nil {
		t.Fatalf("Load(a scope naming a consumer requiring an uncached module) = %+v, nil, want an error", got)
	}
	if !strings.Contains(err.Error(), "example.com/absent") {
		t.Errorf("Load() error = %q, want it to name the module the cache does not hold", err)
	}
	if !reflect.DeepEqual(got, Result{}) {
		t.Errorf("Load() = %+v, want the zero Result", got)
	}
}

func TestLoadWithNoConsumerCarriesNone(t *testing.T) {
	stableToolchain(t)

	got := loadFixture(t, "clean")

	if got.Consumers != nil {
		t.Errorf("Load(a scope declaring no consumer).Consumers = %+v, want nil", got.Consumers)
	}
}

func TestSameDir(t *testing.T) {
	cases := map[string]struct {
		a, b string
		want bool
	}{
		"identical":              {a: "/src/app", b: "/src/app", want: true},
		"a trailing separator":   {a: "/src/app/", b: "/src/app", want: true},
		"an unnormalized parent": {a: "/src/lib/../app", b: "/src/app", want: true},
		"a different directory":  {a: "/src/app", b: "/src/other", want: false},
		"one below the other":    {a: "/src/app/inner", b: "/src/app", want: false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			a, b := filepath.FromSlash(test.a), filepath.FromSlash(test.b)
			if got := sameDir(a, b); got != test.want {
				t.Errorf("sameDir(%q, %q) = %t, want %t", a, b, got, test.want)
			}
		})
	}
}

func TestMainModuleReportsTheFirstModuleTheRootsName(t *testing.T) {
	cases := map[string]struct {
		pkgs []*packages.Package
		want string
	}{
		"no package": {pkgs: nil, want: ""},
		"a package with no module": {
			pkgs: []*packages.Package{{ID: "example.com/a"}},
			want: "",
		},
		"the first module named": {
			pkgs: []*packages.Package{
				{ID: "example.com/a", Module: &packages.Module{}},
				{ID: "example.com/b", Module: &packages.Module{Path: "example.com/b"}},
				{ID: "example.com/c", Module: &packages.Module{Path: "example.com/c"}},
			},
			want: "example.com/b",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := ""
			if module := mainModule(test.pkgs); module != nil {
				got = module.Path
			}
			if got != test.want {
				t.Errorf("mainModule() = %q, want %q", got, test.want)
			}
		})
	}
}
