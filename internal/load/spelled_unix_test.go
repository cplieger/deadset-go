//go:build unix

package load

import (
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"golang.org/x/tools/go/packages"
)

// linkedTree is files written below a resolved temporary directory, that
// directory, and a link to it in another one: the target root a run spells as
// os.Getwd resolves it, and the spelling a scope document, GOENV or an absolute
// directive gives the same tree, as macOS spells /private/var through /var.
func linkedTree(t *testing.T, files map[string]string) (base, alias string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(writeTree(t, files))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	alias = filepath.Join(sandbox.Dir(t), "alias")
	if err := sandbox.Symlink(base, alias); err != nil {
		t.Fatalf("Setup: link %s to %s: %v", alias, base, err)
	}
	return base, alias
}

// A file of a declared consumer, or of the workspace it loads under, that the
// scope document spells through a link to the tree is named where it resolves,
// relative to the target root ([program.10]), never by the link's own path.
func TestDiscoverNamesAConsumersFileSpelledThroughALinkWhereItResolves(t *testing.T) {
	t.Parallel()
	consumer := map[string]string{
		"app/go.mod":    "module example.com/app\n\ngo 1.27.1\n",
		"app/main.go":   "package main\n\nfunc main() {}\n",
		"w/c/go.mod":    "module example.com/c\n\ngo 1.27.1\n",
		"w/c/main.go":   "package main\n\nfunc main() {}\n",
		"w/c/README.md": "consumer\n",
	}
	tests := []struct {
		name, entry string
		workspace   bool // whether the consumer loads under w/go.work
		make        func(t *testing.T, path string)
		failure     string // what Discover's error says, empty where it succeeds
		fileErr     string // what the consumer's module file error says
	}{
		{
			name: "a_module_file_that_is_a_pipe", entry: "w/c/go.mod", make: replaced(sandbox.Fifo),
			failure: "../w/c/go.mod: no regular file",
		},
		{
			name: "a_module_file_that_does_not_parse", entry: "w/c/go.mod", make: replaced(writeBody("modul example.com/c\n")),
			fileErr: "../w/c/go.mod:1: unknown directive: modul",
		},
		{
			name: "a_workspace_file_that_is_a_pipe", entry: "w/go.work", workspace: true, make: sandbox.Fifo,
			failure: "load: ../w/go.work: no regular file, but a pipe",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deadline.Row(t, time.Minute, "discovery opened a file that blocks", func(t *testing.T) {
				base, alias := linkedTree(t, consumer)
				tc.make(t, filepath.Join(base, filepath.FromSlash(tc.entry)))
				doc := scope.Document{
					Target:    scope.Module{Path: filepath.Join(base, "app")},
					Consumers: []scope.Module{{Path: filepath.Join(alias, "w", "c")}},
				}
				if tc.workspace {
					doc.Workspace = filepath.Join(alias, "w", "go.work")
				}
				top, err := Discover(deadline.Context(t), doc)
				switch {
				case tc.failure != "":
					if err == nil || !strings.Contains(err.Error(), tc.failure) || strings.Contains(err.Error(), alias) {
						t.Fatalf("Discover(a consumer spelled through a link holding %s) = _, %v, want an error naming %q", tc.name, err, tc.failure)
					}
					return
				case err != nil:
					t.Fatalf("Discover(a consumer spelled through a link holding %s) = _, %v, want no error", tc.name, err)
				}
				fileErr := top.Consumers[0].FileErr
				if fileErr == nil || !strings.Contains(fileErr.Error(), tc.fileErr) || strings.Contains(fileErr.Error(), alias) {
					t.Errorf("Discover(a consumer spelled through a link holding %s) kept the module file error %v, want one naming %q", tc.name, fileErr, tc.fileErr)
				}
			})
		})
	}
}

// A load run from a declared consumer the scope spells through a link reaches a
// left-out entry of the target through that link, so the toolchain's list error
// names the entry by that spelling: it is the entry the walk of the root met, and
// it is cleared all the same.
func TestDropUnreadErrorsClearsALeftOutEntryAConsumerSpelledThroughALinkOpens(t *testing.T) {
	t.Parallel()
	base, alias := linkedTree(t, map[string]string{"app/p/p.go": "package p\n", "c/main.go": "package main\n"})
	broken := filepath.Join(base, "app", "p", "broken.go")
	spec := &loadSpec{dir: filepath.Join(alias, "c"), top: &Topology{Root: filepath.Join(base, "app"), root: scope.RootOf(filepath.Join(base, "app")), LeftOut: []string{broken}}}
	p := &packages.Package{ID: "example.com/app/p", GoFiles: []string{filepath.Join(alias, "app", "p", "p.go")}, Errors: []packages.Error{
		{Kind: packages.ListError, Msg: "open " + filepath.Join(alias, "app", "p", "broken.go") + ": no such file or directory"},
		{Kind: packages.ListError, Msg: "open " + filepath.Join(alias, "app", "p", "other.go") + ": no such file or directory"},
	}}

	dropUnreadErrors([]*packages.Package{p}, spec)
	if len(p.Errors) != 1 || !strings.Contains(p.Errors[0].Msg, "other.go") {
		t.Errorf("dropUnreadErrors left %v, want the error naming the entry through the link cleared and the one naming another file kept", p.Errors)
	}
}

// A load error of a declared consumer the scope spells through a link names the
// consumer, and every position in it, where it resolves, relative to the target
// root ([program.10]).
func TestLoadNamesAConsumerSpelledThroughALinkThatDoesNotTypeCheckWhereItResolves(t *testing.T) {
	stableToolchain(t)
	doc := consumedScope(t, "broken")
	alias := filepath.Join(sandbox.Dir(t), "consumed")
	if err := sandbox.Symlink(filepath.Dir(doc.Consumers[0].Path), alias); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	want := "../broken"
	doc.Consumers[0].Path = filepath.Join(alias, "broken")

	_, err := loadScope(deadline.Context(t), doc, HostConfiguration())

	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("Load(a consumer spelled through a link that does not type-check) = _, %v, want a *Error", err)
	}
	if failure.Module != want {
		t.Errorf("(*Error).Module = %q, want the consumer where it resolves, %q", failure.Module, want)
	}
	if len(failure.Diagnostics) == 0 {
		t.Fatal("(*Error).Diagnostics is empty, want the toolchain's own diagnostics")
	}
	for _, d := range failure.Diagnostics {
		if d.Position != "" && !strings.HasPrefix(d.Position, want+"/") {
			t.Errorf("(*Error) diagnostic at %q, want a position below %q", d.Position, want)
		}
	}
}

// A declared consumer the scope spells through a link to the directory holding it
// and the target reaches the analyzed directory through that link, by its relative
// replace, so it loads as the same consumer spelled without the link does.
func TestLoadCarriesAConsumerSpelledThroughALinkThatReachesTheTarget(t *testing.T) {
	deadline.Row(t, 2*time.Minute, "a load of a consumer spelled through a link did not return", func(t *testing.T) {
		stableToolchain(t)
		doc := consumedScope(t, "consumer")
		unlinked, err := loadScope(deadline.Context(t), doc, HostConfiguration())
		if err != nil || len(unlinked.Consumers) != 1 {
			t.Fatalf("Setup: Load(the consumed fixture with one consumer) = %+v, %v", unlinked.Consumers, err)
		}
		alias := filepath.Join(sandbox.Dir(t), "consumed")
		if err := sandbox.Symlink(filepath.Dir(doc.Consumers[0].Path), alias); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		doc.Consumers[0].Path = filepath.Join(alias, "consumer")

		got, err := loadScope(deadline.Context(t), doc, HostConfiguration())
		if err != nil {
			t.Fatalf("Load(a consumer spelled through a link) = _, %v, want no error", err)
		}
		want := consumerPackageIDs(unlinked.Consumers[0])
		if len(got.Consumers) != 1 || !slices.Equal(consumerPackageIDs(got.Consumers[0]), want) {
			t.Errorf("Load(a consumer spelled through a link).Consumers = %+v, want one consumer with the packages %v", got.Consumers, want)
		}
	})
}

// A declared consumer that reaches a copy of the target is refused, naming the
// copy and the analyzed directory where each resolves: two directories, so two
// names, whichever way the scope spells the consumer.
func TestLoadRefusesAConsumerThatReachesACopyOfTheTargetNamingBoth(t *testing.T) {
	deadline.Row(t, 2*time.Minute, "a load of a consumer reaching a copy did not return", func(t *testing.T) {
		stableToolchain(t)
		lib := "package app\n\n// A is what the consumer calls.\nfunc A() {}\n"
		base, alias := linkedTree(t, map[string]string{
			"app/go.mod":  "module example.com/app\n\ngo 1.27.1\n",
			"app/a.go":    lib,
			"copy/go.mod": "module example.com/app\n\ngo 1.27.1\n",
			"copy/a.go":   lib,
			"c/go.mod":    "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../copy\n",
			"c/main.go":   "package main\n\nimport \"example.com/app\"\n\nfunc main() { app.A() }\n",
		})
		doc := scope.Document{Target: scope.Module{Path: filepath.Join(base, "app")}, Consumers: []scope.Module{{Path: filepath.Join(alias, "c")}}}

		_, err := loadScope(deadline.Context(t), doc, HostConfiguration())

		want := "../c: it resolves example.com/app at ../copy rather than at ."
		if !errors.Is(err, errConsumer) || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(a consumer reaching a copy of the target) = _, %v, want an error saying %q", err, want)
		}
	})
}

// A module below the target that replaces the target by an absolute path through
// a link builds against the target's own tree, as the toolchain reads that
// directive, so it is a nested module whose references count.
func TestDiscoverKeepsANestedModuleReplacingTheTargetThroughALink(t *testing.T) {
	t.Parallel()
	deadline.Row(t, time.Minute, "discovery did not return", func(t *testing.T) {
		base, alias := linkedTree(t, map[string]string{
			"app/go.mod":     "module example.com/app\n\ngo 1.27.1\n",
			"app/a.go":       "package app\n\nfunc A() {}\n",
			"app/tools/t.go": "package tools\n\nimport \"example.com/app\"\n\nfunc T() { app.A() }\n",
		})
		module := "module example.com/tools\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => " + filepath.Join(alias, "app") + "\n"
		if err := sandbox.WriteFile(filepath.Join(base, "app", "tools", "go.mod"), []byte(module), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		top, err := Discover(deadline.Context(t), scope.Document{Target: scope.Module{Path: filepath.Join(base, "app")}})
		if err != nil {
			t.Fatalf("Discover(a nested module replacing the target through a link) = _, %v, want no error", err)
		}
		tools := filepath.Join(base, "app", "tools")
		if len(top.Nested) != 1 || top.Nested[0].Dir != tools || !slices.Equal(top.Nested[0].Consumes, []string{"example.com/app"}) {
			t.Errorf("Discover().Nested = %+v, want %s alone, consuming example.com/app", top.Nested, tools)
		}
	})
}

// A declared consumer the scope spells through a link is the module the walk below
// the target, or the workspace listing the target, meets by another spelling: it
// is read once, as the declared consumer, and never again as a nested module or a
// workspace reader.
func TestDiscoverReadsAConsumerSpelledThroughALinkOnceAsDeclared(t *testing.T) {
	t.Parallel()
	consumer := "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n"
	tests := []struct {
		name, consumer string
		files          map[string]string
	}{
		{
			name: "a_nested_module", consumer: "app/c",
			files: map[string]string{"app/c/go.mod": consumer + "\nreplace example.com/app => ../\n"},
		},
		{
			name: "a_workspace_reader", consumer: "c",
			files: map[string]string{"c/go.mod": consumer, "go.work": "go 1.27.1\n\nuse (\n\t./app\n\t./c\n)\n"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deadline.Row(t, time.Minute, "discovery did not return", func(t *testing.T) {
				files := map[string]string{
					"app/go.mod": "module example.com/app\n\ngo 1.27.1\n",
					"app/a.go":   "package app\n\nfunc A() {}\n",
				}
				files[tc.consumer+"/main.go"] = "package main\n\nimport \"example.com/app\"\n\nfunc main() { app.A() }\n"
				maps.Copy(files, tc.files)
				base, alias := linkedTree(t, files)
				doc := scope.Document{
					Target:    scope.Module{Path: filepath.Join(base, "app")},
					Consumers: []scope.Module{{Path: filepath.Join(alias, filepath.FromSlash(tc.consumer))}},
				}

				top, err := Discover(deadline.Context(t), doc)
				if err != nil {
					t.Fatalf("Discover(a consumer spelled through a link) = _, %v, want no error", err)
				}
				if len(top.Consumers) != 1 || len(top.Nested) != 0 || len(top.Readers) != 0 {
					t.Errorf("Discover() read the consumers %+v, the nested modules %+v and the readers %+v, want the declared consumer alone",
						top.Consumers, top.Nested, top.Readers)
				}
			})
		})
	}
}

// A declared consumer below the target that the scope spells through a link holds
// the entries the walk of the root recorded as the root spells them, so an entry
// below it the run cannot read is below it where both resolve: its load names each
// package directory rather than ending the toolchain's walk at that entry.
func TestUnreadBelowPlacesAConsumerSpelledThroughALinkWhereItResolves(t *testing.T) {
	t.Parallel()
	base, alias := linkedTree(t, map[string]string{"app/c/main.go": "package main\n", "app/d/d.go": "package d\n"})
	app := filepath.Join(base, "app")
	top := &Topology{Root: app, root: scope.RootOf(app), Unread: []string{filepath.Join(app, "c", "m")}}
	for _, tc := range []struct {
		dir  string
		want bool
	}{
		{filepath.Join(app, "c"), true},
		{filepath.Join(alias, "app", "c"), true},
		{filepath.Join(alias, "app", "d"), false},
		{filepath.Join(alias, "app", "c", "m"), false},
	} {
		if got := top.unreadBelow(tc.dir); got != tc.want {
			t.Errorf("unreadBelow(%s) over the unread %s = %v, want %v", tc.dir, top.Unread[0], got, tc.want)
		}
	}
}
