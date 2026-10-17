package load

import (
	"context"
	"errors"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// absentRequirement is a module file requiring the library through a replace and a
// module no cache holds, so a load of it ends with a setup failure.
const absentRequirement = "module example.com/tool\n\ngo 1.27.1\n\nrequire (\n\texample.com/b v0.0.0\n\texample.com/absent v1.0.0\n)\n\nreplace example.com/b => ../b\n"

// absentImport is a command importing the library and the module nothing provides.
const absentImport = "package main\n\nimport (\n\t\"example.com/absent\"\n\t\"example.com/b\"\n)\n\nfunc main() { b.Used(); absent.Run() }\n"

// loadTree loads the target at dir of root under the host configuration.
func loadTree(t *testing.T, root, dir string) (Result, error) {
	t.Helper()
	doc, err := scope.ForDir(filepath.Join(root, dir))
	if err != nil {
		t.Fatalf("Setup: scope.ForDir = _, %v", err)
	}
	return loadScope(t.Context(), doc, HostConfiguration())
}

// A module the walk finds below a target that is no module root, and that does not
// load, is dropped with its failure, positioned against the target root, and the
// unit it requires has it as a consumer the run did not analyze.
func TestLoadDropsAWalkedModuleThatDoesNotLoad(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"a/go.mod":     "module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n",
		"a/main.go":    "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"b/go.mod":     "module example.com/b\n\ngo 1.27.1\n",
		"b/b.go":       "package b\n\n// Used is what a calls.\nfunc Used() {}\n",
		"tool/go.mod":  absentRequirement,
		"tool/main.go": absentImport,
	})

	got, err := loadTree(t, root, ".")
	if err != nil {
		t.Fatalf("Load(a tree with a module that does not load) = _, %v, want no error", err)
	}
	consumed := make(map[string]bool)
	for _, u := range got.Units {
		consumed[u.Path] = u.UnanalysedConsumer
	}
	if want := map[string]bool{"example.com/a": false, "example.com/b": true}; !maps.Equal(consumed, want) {
		t.Errorf("Load() read the units %v (path to unanalysed consumer), want %v", consumed, want)
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Dir != filepath.Join(root, "tool") {
		t.Fatalf("Load() dropped %+v, want the tool module alone", got.Dropped)
	}
	setup, ok := errors.AsType[*SetupError](got.Dropped[0].Err)
	if !ok || len(setup.Failures) == 0 || !strings.Contains(setup.Failures[0].Detail, "tool/main.go:") {
		t.Errorf("Load() dropped the tool module with %v, want a setup failure positioned at tool/main.go", got.Dropped[0].Err)
	}
}

// A walked module's failure ends the run where no module is left, and a module the
// target names ends it whatever else loads, its position relative to the target root.
func TestLoadEndsOnTheFailureOfAModuleItCannotDrop(t *testing.T) {
	stableToolchain(t)
	for name, test := range map[string]struct {
		files map[string]string
		at    string
	}{
		"every walked module fails": {
			files: map[string]string{
				"b/go.mod":     "module example.com/b\n\ngo 1.27.1\n",
				"b/b.go":       "package b\n\nimport \"example.com/b/gen\"\n\nvar V = gen.V\n",
				"tool/go.mod":  absentRequirement,
				"tool/main.go": absentImport,
			},
			at: "b/b.go:3:8",
		},
		"a workspace member fails": {
			files: map[string]string{
				"go.work":     "go 1.27.1\n\nuse (\n\t.\n\t./sub\n)\n",
				"go.mod":      "module example.com/r\n\ngo 1.27.1\n",
				"main.go":     "package main\n\nfunc main() {}\n",
				"sub/go.mod":  "module example.com/sub\n\ngo 1.27.1\n",
				"sub/main.go": "package main\n\nimport \"example.com/sub/gen\"\n\nfunc main() { println(gen.V) }\n",
			},
			at: "sub/main.go:3:8",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTree(t, writeTree(t, test.files), ".")
			setup, ok := errors.AsType[*SetupError](err)
			if !ok || !slices.ContainsFunc(setup.Failures, func(f setupFailure) bool { return strings.HasPrefix(f.Detail, test.at+":") }) {
				t.Errorf("Load() = _, %v, want a setup failure positioned at %s", err, test.at)
			}
		})
	}
}

// The packages of the target's own module below an underscore directory, which the
// go tool's wildcard skips, load as nested consumers of it, a type error being read
// as the target's own are; one whose imports do not resolve is left out, and a module
// or a test input below such a directory is not one of them.
func TestLoadReadsTheUnderscoredPackagesOfTheModuleAsANestedConsumer(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"go.mod":                       "module example.com/c\n\ngo 1.27.1\n",
		"c.go":                         "package c\n\n// Used is what the example calls.\nfunc Used() {}\n",
		"_examples/limits/main.go":     "package main\n\nimport \"example.com/c\"\n\nfunc main() { c.Used() }\n",
		"_examples/_nested/x/x.go":     "package x\n\nimport \"example.com/c\"\n\nvar _ = c.Used\n",
		"_examples/stale/main.go":      "package main\n\nimport \"example.com/c\"\n\nfunc main() { c.Gone() }\n",
		"_examples/absent/main.go":     "package main\n\nimport \"example.org/absent\"\n\nfunc main() { absent.Run() }\n",
		"_examples/onlytest/x_test.go": "package onlytest\n",
		"_examples/own/go.mod":         "module example.com/own\n\ngo 1.27.1\n",
		"_examples/own/main.go":        "package main\n\nfunc main() {}\n",
		"testdata/_input/in.go":        "package in\n",
	})
	patterns, err := underscoredPackages(t.Context(), root)
	if want := []string{"./_examples/_nested/x", "./_examples/absent", "./_examples/limits", "./_examples/stale"}; err != nil || !slices.Equal(patterns, want) {
		t.Errorf("underscoredPackages() = %v, %v, want %v, nil", patterns, err, want)
	}

	got, err := loadTree(t, root, ".")
	if err != nil {
		t.Fatalf("Load(a module with underscored packages) = _, %v, want no error", err)
	}
	var loaded []string
	for _, consumer := range got.Nested {
		for _, p := range consumer.Packages {
			loaded = append(loaded, consumer.ID+" "+p.PkgPath)
		}
	}
	want := []string{
		"example.com/c example.com/c/_examples/_nested/x",
		"example.com/c example.com/c/_examples/limits",
		"example.com/c example.com/c/_examples/stale",
	}
	if !slices.Equal(loaded, want) {
		t.Errorf("Load() read the nested packages %v, want %v", loaded, want)
	}
	if len(got.Dropped) != 0 || got.Units[0].UnanalysedConsumer {
		t.Errorf("Load() dropped %v and flagged %t, want neither", got.Dropped, got.Units[0].UnanalysedConsumer)
	}
}

// A module below a module-root target that replaces the target with its directory,
// relative or absolute and below an underscore directory too, loads as a nested
// consumer; one requiring a published version, or replacing it with another module,
// is no consumer of this tree, and one that does not load is dropped with the unit
// flagged.
func TestLoadReadsANestedModuleAsAConsumerOfTheUnitItReplaces(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"go.mod":                "module example.com/b\n\ngo 1.27.1\n",
		"b.go":                  "package b\n\n// Used is what the example calls.\nfunc Used() {}\n",
		"examples/go.mod":       "module example.com/b/examples\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../\n",
		"examples/demo/main.go": "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"published/go.mod":      "module example.com/b/published\n\ngo 1.27.1\n\nrequire example.com/b v1.0.0\n",
		"published/main.go":     "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"forked/go.mod":         "module example.com/b/forked\n\ngo 1.27.1\n\nrequire example.com/b v1.0.0\n\nreplace example.com/b => example.com/fork v1.2.3\n",
		"forked/main.go":        "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"_examples/go.mod":      "module example.com/b/underscored\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../\n",
		"_examples/main.go":     "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"absolute/main.go":      "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
	})
	absolute := "module example.com/b/absolute\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => " + root + "\n"
	if err := os.WriteFile(filepath.Join(root, "absolute", "go.mod"), []byte(absolute), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadTree(t, root, ".")
	if err != nil {
		t.Fatalf("Load(a module with a nested consumer) = _, %v, want no error", err)
	}
	var nested []string
	for _, c := range got.Nested {
		nested = append(nested, c.ID)
	}
	if want := []string{"example.com/b/underscored", "example.com/b/absolute", "example.com/b/examples"}; !slices.Equal(nested, want) {
		t.Errorf("Load() read the nested consumers %v, want %v", nested, want)
	}
	if len(got.Consumers) != 0 || len(got.Dropped) != 0 || got.Units[0].UnanalysedConsumer {
		t.Errorf("Load() read the consumers %v and dropped %v, want neither, and no unanalysed consumer", got.Consumers, got.Dropped)
	}
	if referencing := got.Referencing(); len(referencing) != 3 {
		t.Errorf("Referencing() = %v, want the three nested consumers alone", referencing)
	}

	failing := writeTree(t, map[string]string{
		"go.mod":         "module example.com/b\n\ngo 1.27.1\n",
		"b.go":           "package b\n\nfunc Used() {}\n",
		"tool/go.mod":    strings.Replace(absentRequirement, "../b", "../", 1),
		"tool/main.go":   absentImport,
		"broken/go.mod":  "module example.com/b/broken\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../\n",
		"broken/main.go": "package main\n\nfunc main() {\n",
	})
	got, err = loadTree(t, failing, ".")
	if err != nil {
		t.Fatalf("Load(a module with nested consumers that do not load) = _, %v, want no error", err)
	}
	if len(got.Nested) != 0 || len(got.Dropped) != 2 || !got.Units[0].UnanalysedConsumer {
		t.Fatalf("Load() read the nested consumers %v, dropped %v and flagged %t, want none, the two modules and true",
			got.Nested, got.Dropped, got.Units[0].UnanalysedConsumer)
	}
	for _, dropped := range got.Dropped {
		first, _, _ := strings.Cut(dropped.Err.Error(), "\n")
		if strings.Contains(first, failing) {
			t.Errorf("Load() dropped %s with %q, want a first line naming no host path", dropped.Dir, first)
		}
	}
}

// unlistable is a module file the go command refuses to list offline: under go 1.16
// it needs the module file of a requirement no cache holds, so the listing fails
// before it reports any package.
const unlistable = "module example.com/b/unlistable\n\ngo 1.16\n\nrequire (\n\texample.com/b v0.0.0\n\texample.com/absent v1.0.0\n)\n\nreplace example.com/b => ../\n"

// A nested or walked module the go command refuses to list is dropped like one whose
// packages report errors, its unit flagged, while a consumer the scope names that the
// go command refuses to list ends the load.
func TestLoadDropsAModuleTheGoCommandRefusesToList(t *testing.T) {
	stableToolchain(t)
	for name, test := range map[string]struct {
		files map[string]string
		dir   string
	}{
		"nested below a module root": {
			files: map[string]string{
				"go.mod":             "module example.com/b\n\ngo 1.27.1\n",
				"b.go":               "package b\n\n// Used is what the module calls.\nfunc Used() {}\n",
				"unlistable/go.mod":  unlistable,
				"unlistable/main.go": "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
			},
			dir: "unlistable",
		},
		"walked below a directory": {
			files: map[string]string{
				"b/go.mod":           "module example.com/b\n\ngo 1.27.1\n",
				"b/b.go":             "package b\n\n// Used is what the module calls.\nfunc Used() {}\n",
				"unlistable/go.mod":  strings.Replace(unlistable, "../", "../b", 1),
				"unlistable/main.go": "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
			},
			dir: "unlistable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := writeTree(t, test.files)
			got, err := loadTree(t, root, ".")
			if err != nil {
				t.Fatalf("Load(a tree with a module the go command refuses to list) = _, %v, want no error", err)
			}
			if len(got.Dropped) != 1 || got.Dropped[0].Dir != filepath.Join(root, test.dir) {
				t.Fatalf("Load() dropped %+v, want the %s module alone", got.Dropped, test.dir)
			}
			if !got.Units[0].UnanalysedConsumer {
				t.Errorf("Load() left %s unflagged, want it read as required by a module the run did not analyze", got.Units[0].Path)
			}
			if first, _, _ := strings.Cut(got.Dropped[0].Err.Error(), "\n"); !strings.Contains(first, "example.com/absent") {
				t.Errorf("Load() dropped the module with the first line %q, want it to name example.com/absent", first)
			}
		})
	}

	t.Run("declared consumer", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"lib/go.mod":         "module example.com/b\n\ngo 1.27.1\n",
			"lib/b.go":           "package b\n\n// Used is what the consumer calls.\nfunc Used() {}\n",
			"unlistable/go.mod":  strings.Replace(unlistable, "../", "../lib", 1),
			"unlistable/main.go": "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		})
		doc := scope.Document{
			Target:    scope.Module{Path: filepath.Join(root, "lib")},
			Consumers: []scope.Module{{Path: filepath.Join(root, "unlistable")}},
		}
		if _, err := loadScope(t.Context(), doc, HostConfiguration()); err == nil {
			t.Errorf("Load(a scope naming a consumer the go command refuses to list) = _, nil, want an error")
		}
	})
}

// The go command's failure renders without its progress lines, so its first line
// names what failed.
func TestListErrorRendersTheFailureWithoutProgressLines(t *testing.T) {
	err := &listError{err: errors.New("err: exit status 1: stderr: go: downloading example.com/absent v1.0.0\n" +
		"go: example.com/absent@v1.0.0: module lookup disabled by GOPROXY=off\n")}
	want := "err: exit status 1: stderr: go: example.com/absent@v1.0.0: module lookup disabled by GOPROXY=off"
	if got := err.Error(); got != want {
		t.Errorf("(*listError).Error() = %q, want %q", got, want)
	}
}

func TestRebasedJoinsARelativePositionToThePrefix(t *testing.T) {
	for _, test := range []struct{ position, prefix, want string }{
		{position: "main.go:4:2", prefix: "tool", want: "tool/main.go:4:2"},
		{position: "main.go:4:2", prefix: "", want: "main.go:4:2"},
		{position: "/abs/main.go:4:2", prefix: "tool", want: "/abs/main.go:4:2"},
		{position: "", prefix: "tool", want: ""},
	} {
		if got := rebased(test.position, test.prefix); got != test.want {
			t.Errorf("rebased(%q, %q) = %q, want %q", test.position, test.prefix, got, test.want)
		}
	}
}

// A workspace member the scope does not declare is a reader whatever named the
// scope: it is loaded through the workspace as a nested consumer when its module
// file requires a unit, and passed over when it does not.
func TestLoadReadsAWorkspaceReaderThatRequiresAUnit(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, readerTree)
	got, err := loadScope(t.Context(), scope.Document{Target: scope.Module{Path: filepath.Join(root, "lib")}}, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(a workspace member) = _, %v, want no error", err)
	}
	var nested []string
	for _, c := range got.Nested {
		nested = append(nested, c.ID)
	}
	if want := []string{"example.com/app"}; !slices.Equal(nested, want) || len(got.Consumers) != 0 || len(got.Dropped) != 0 {
		t.Errorf("Load() read the nested consumers %v, the consumers %v and dropped %v, want %v and neither", nested, got.Consumers, got.Dropped, want)
	}
}

// A reader whose module file the toolchain reads and this analyzer's parser
// refuses, as a directive newer than the parser leaves it, may require any unit, so
// it fails closed: it is dropped with the module file's error and every unit's
// exports are possible. The test stands in for that disagreement by refusing the
// file when the topology reads it and restoring it before the toolchain lists the
// workspace.
func TestLoadDropsAWorkspaceReaderWhoseModuleFileOnlyTheToolchainReads(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, readerTree)
	reader := filepath.Join(root, "app", "go.mod")
	readable, err := os.ReadFile(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reader, append(slices.Clone(readable), "unknowndirective x\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	top := discovered(t, scope.Document{Target: scope.Module{Path: filepath.Join(root, "lib")}})
	if err := os.WriteFile(reader, readable, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(t.Context(), top, new(HostConfiguration()))
	if err != nil {
		t.Fatalf("Load(a workspace whose reader's module file cannot be read) = _, %v, want no error", err)
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Dir != filepath.Join(root, "app") || !errors.Is(got.Dropped[0].Err, ErrModuleFile) {
		t.Fatalf("Load() dropped %+v, want the app reader alone, with %v", got.Dropped, ErrModuleFile)
	}
	if len(got.Nested) != 0 || !got.Units[0].UnanalysedConsumer {
		t.Errorf("Load() read the nested consumers %v and flagged the unit %t, want none and true", got.Nested, got.Units[0].UnanalysedConsumer)
	}
}

// readerTree is a workspace listing a library, an application calling it and a
// module that requires nothing of it.
var readerTree = map[string]string{
	"go.work":       "go 1.27.1\n\nuse (\n\t./app\n\t./lib\n\t./other\n)\n",
	"app/go.mod":    "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/lib v0.0.0\n",
	"app/main.go":   "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.Used() }\n",
	"lib/go.mod":    "module example.com/lib\n\ngo 1.27.1\n",
	"lib/lib.go":    "package lib\n\n// Used is what app calls.\nfunc Used() {}\n",
	"other/go.mod":  "module example.com/other\n\ngo 1.27.1\n",
	"other/main.go": "package main\n\nfunc main() {}\n",
}

// Discovery searches no directory the Go row of the language table skips, so
// detection and discovery read one fact: a module below each skipped name is no
// unit, and a module below an ordinary name is one.
func TestDiscoveryLeavesEveryDirectoryTheGoRowSkipsUnsearched(t *testing.T) {
	if len(goRow.SkippedDirectories) == 0 {
		t.Fatal("Setup: the Go row of the language table skips no directory")
	}
	files := map[string]string{"kept/go.mod": "module example.com/kept\n\ngo 1.27.1\n"}
	for _, pattern := range goRow.SkippedDirectories {
		name := strings.ReplaceAll(pattern, "*", "x")
		files[name+"/m/go.mod"] = "module example.com/" + strings.Trim(name, "._") + "\n\ngo 1.27.1\n"
	}
	root := writeTree(t, files)
	got, err := unitDirs(t.Context(), root)
	if want := []string{filepath.Join(root, "kept")}; err != nil || !slices.Equal(got, want) {
		t.Errorf("unitDirs(a tree with a module below each skipped directory of %v) = %v, %v, want %v", goRow.SkippedDirectories, got, err, want)
	}
}

// A workspace member is a module root of its own, so a reader whose directory holds
// no module file is one whose module file cannot be read, whatever module file a
// directory above it holds.
func TestDiscoverReadsAReaderModuleFileInItsOwnDirectoryAlone(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.work":              "go 1.27.1\n\nuse (\n\t./lib\n\t./outer/reader\n)\n",
		"lib/go.mod":           "module example.com/lib\n\ngo 1.27.1\n",
		"lib/lib.go":           "package lib\n",
		"outer/go.mod":         "module example.com/outer\n\ngo 1.27.1\n",
		"outer/reader/main.go": "package main\n\nfunc main() {}\n",
	})
	top := discovered(t, scope.Document{Target: scope.Module{Path: filepath.Join(root, "lib")}})
	if len(top.Readers) != 1 || top.Readers[0].Dir != filepath.Join(root, "outer", "reader") || !errors.Is(top.Readers[0].FileErr, ErrModuleFile) {
		t.Errorf("Discover() read the readers %+v, want outer/reader alone, with %v", top.Readers, ErrModuleFile)
	}
}

// stagedContext answers its first live checks of Err as a live context and every
// later one as a cancelled one, so a cancellation lands after a search has begun.
type stagedContext struct {
	context.Context
	live, checks int
}

func (c *stagedContext) Err() error {
	c.checks++
	if c.checks > c.live {
		return context.Canceled
	}
	return nil
}

// Every search and loop of discovery checks its context again after it has begun,
// and ends with the cancellation at whichever later check first sees it, so a run
// cancelled while discovery reads a large tree or workspace ends with the
// cancellation rather than a partial topology.
func TestDiscoveryStopsWhenCancelledAfterItBegins(t *testing.T) {
	module := writeTree(t, map[string]string{
		"go.work":           "go 1.27.1\n\nuse (\n\t.\n\t./member\n\t./other\n\t./outside/reader\n)\n",
		"go.mod":            "module example.com/c\n\ngo 1.27.1\n",
		"_examples/main.go": "package main\n",
		"_examples/more.go": "package main\n",
		"member/go.mod":     "module example.com/m\n\ngo 1.27.1\n",
		"other/go.mod":      "module example.com/o\n\ngo 1.27.1\n",
		"nested/go.mod":     "module example.com/n\n\ngo 1.27.1\n",
	})
	walked := writeTree(t, map[string]string{"a/go.mod": "module example.com/a\n\ngo 1.27.1\n", "b/go.mod": "module example.com/b\n\ngo 1.27.1\n"})
	unlisted := writeTree(t, map[string]string{
		"go.work":        "go 1.27.1\n\nuse ./sibling\n",
		"sibling/go.mod": "module example.com/s\n\ngo 1.27.1\n",
		"target/go.mod":  "module example.com/t\n\ngo 1.27.1\n",
	})
	unit := filepath.Join(module, "member")
	top := &Topology{Root: unit, Units: []discoveredModule{{Dir: unit, Path: "example.com/m"}}}
	consumers := []scope.Module{{Path: filepath.Join(module, "other")}, {Path: walked}}
	checks := map[string]int{}
	for name, search := range map[string]func(context.Context) error{
		"unitDirs":    func(ctx context.Context) error { _, err := unitDirs(ctx, walked); return err },
		"withMembers": func(ctx context.Context) error { _, err := withMembers(ctx, module); return err },
		"withMembers under a workspace that does not list it": func(ctx context.Context) error {
			_, err := withMembers(ctx, filepath.Join(unlisted, "target"))
			return err
		},
		"nestedModules": func(ctx context.Context) error {
			_, err := (&Topology{Root: module}).nestedModules(ctx, nil)
			return err
		},
		"underscoredPackages": func(ctx context.Context) error { _, err := underscoredPackages(ctx, module); return err },
		"readers":             func(ctx context.Context) error { _, err := top.readers(ctx, nil); return err },
		"declaredConsumers":   func(ctx context.Context) error { _, err := declaredConsumers(ctx, consumers); return err },
	} {
		counted := &stagedContext{Context: t.Context(), live: math.MaxInt}
		if err := search(counted); err != nil {
			t.Fatalf("Setup: %s(a live context) = %v", name, err)
		}
		checks[name] = counted.checks
		if counted.checks < 2 {
			t.Errorf("%s checks its context %d times, want a check after it has begun", name, counted.checks)
		}
		for live := 1; live < counted.checks; live++ {
			if err := search(&stagedContext{Context: t.Context(), live: live}); !errors.Is(err, context.Canceled) {
				t.Errorf("%s(a context cancelled after %d checks) = %v, want %v", name, live, err, context.Canceled)
			}
		}
	}
	if listedChecks, unlistedChecks := checks["withMembers"], checks["withMembers under a workspace that does not list it"]; listedChecks <= unlistedChecks {
		t.Errorf("withMembers checks its context %d times over a workspace listing members and %d over one not listing the target, want a check for each member", listedChecks, unlistedChecks)
	}
}

// A narrowing of a result reads the one source cache of the whole result, so the
// source of a unit's package reads the same whichever narrowing asks first.
func TestSourceReadsEveryPackageWhicheverNarrowingAsksFirst(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"a/go.mod":  "module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n",
		"a/main.go": "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"b/go.mod":  "module example.com/b\n\ngo 1.27.1\n",
		"b/b.go":    "package b\n\n// Used is what a calls.\nfunc Used() {}\n",
	})
	got, err := loadTree(t, root, ".")
	if err != nil {
		t.Fatalf("Load(two walked modules) = _, %v, want no error", err)
	}
	b := got.Unit(filepath.Join(root, "b"))
	if b == nil {
		t.Fatalf("Load() read the units %+v, want b among them", got.Units)
	}
	own := b.Packages[slices.IndexFunc(b.Packages, func(p *packages.Package) bool { return p.PkgPath == "example.com/b" && p.ForTest == "" })]
	got.Unit(filepath.Join(root, "a")).Source(own.Types)
	if _, read := got.Source(own.Types); !read {
		t.Errorf("Source(b's own package) after a narrowing to a asked for it = _, false, want its source")
	}
}
