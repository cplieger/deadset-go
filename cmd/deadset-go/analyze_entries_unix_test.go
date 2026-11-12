//go:build unix

package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// aliasMark stands, in a tree a row of the entry-kind table writes, for the path of
// a link to the tree, which the row makes once the tree is written.
const aliasMark = "@ALIAS@"

// entryModule is the module every row of the entry-kind table adds its entry to:
// an ignored directory, an underscore directory, a testdata directory and a
// package beside the program. Unread, unused is dead in package main and Limit an
// unused export of package p.
var entryModule = map[string]string{
	"go.mod":             "module example.com/app\n\ngo 1.27.1\n\nignore ./data\n",
	"main.go":            "package main\n\nimport \"example.com/app/p\"\n\nfunc main() { p.Used() }\n\nfunc unused() {}\n",
	"p/p.go":             "package p\n\nfunc Used() {}\n\nconst Limit = 3\n",
	"data/README.md":     "data\n",
	"_x/README.md":       "examples\n",
	"testdata/README.md": "fixtures\n",
	repositoryDocument:   `{"target": {"kind": "application"}}`,
}

// withHelper is entryModule with an unused unexported function of package p.
var withHelper = func() map[string]string {
	tree := maps.Clone(entryModule)
	tree["p/p.go"] = "package p\n\nfunc Used() {}\n\nconst Limit = 3\n\nfunc helper() {}\n"
	return tree
}()

// withHelperUnexempted is withHelper with linkname-cgo-asm-plugin disabled, so a
// declaration of package p is judged by its class alone.
var withHelperUnexempted = func() map[string]string {
	tree := maps.Clone(withHelper)
	tree[repositoryDocument] = `{"target": {"kind": "application"}, "exemptions": {"disabled": ["linkname-cgo-asm-plugin"]}}`
	return tree
}()

// TestAnalyzeSortsEveryEntryKindOfTheTree pins what each kind of entry does to a
// run within two minutes: a special file or link no build reads changes nothing;
// a directory or Go file the run cannot read where the load reads makes every
// export possible, and one rule N cannot read keeps every function and variable;
// a special file with a source file's name in a package directory is hidden and
// makes its package's declarations possible; and a Go file the run cannot open
// there may import "C", so linkname-cgo-asm-plugin retains its package's
// declarations, or, with that class disabled, makes them possible.
func TestAnalyzeSortsEveryEntryKindOfTheTree(t *testing.T) {
	t.Parallel()
	certain := map[string]vocab.Class{"unused": vocab.Certain, "Limit": vocab.Certain}
	tests := []struct {
		name, entry string
		tree        map[string]string
		make        func(t *testing.T, path string)
		code        string
		notes       []string
		findings    map[string]vocab.Class
	}{
		{name: "a_regular_go_file", entry: "data/x.go", make: writeFile("package data\n"), code: "findings", findings: certain},
		{name: "a_directory", entry: "data/sub", make: makeDir, code: "findings", findings: certain},
		{name: "a_pipe_named_a_go_file_in_an_ignored_directory", entry: "data/x.go", make: sandbox.Fifo, code: "findings", findings: certain},
		{name: "a_pipe_named_a_test_file_in_an_ignored_directory", entry: "data/foo_test.go", make: sandbox.Fifo, code: "findings", findings: certain},
		{name: "a_pipe_named_a_go_file_in_an_underscore_directory", entry: "_x/y.go", make: sandbox.Fifo, code: "findings", findings: certain},
		{name: "a_pipe_named_a_c_header", entry: "testdata/p.h", make: sandbox.Fifo, code: "findings", findings: certain},
		{name: "a_socket_named_a_go_file", entry: "data/s.go", make: makeSocket, code: "findings", findings: certain},
		{name: "a_link_to_a_device", entry: "data/z.go", make: symlinkTo("/dev/zero"), code: "findings", findings: certain},
		{name: "a_link_that_loops", entry: "data/loop.go", make: symlinkTo("loop.go"), code: "findings", findings: certain},
		{name: "a_c_header_linked_through_a_file", entry: "p/a.h", make: symlinkTo("p.go/nothing.h"), code: "findings", findings: certain},
		{name: "a_go_file_linked_through_a_file", entry: "p/through.go", make: symlinkTo("p.go/x.go"), code: "findings", findings: certain},
		{name: "a_go_file_linked_to_nothing", entry: "p/broken.go", make: symlinkTo("missing.go"), code: "findings", findings: certain},
		{name: "a_go_file_linked_to_a_directory", entry: "p/linked.go", make: symlinkTo("../data"), code: "findings", findings: certain},
		{
			name: "a_c_header_linked_to_a_name_too_long_to_look_up", entry: "testdata/long.h",
			make: symlinkTo(strings.Repeat("x", 300)), code: "findings",
			notes: []string{"testdata/long.h"}, findings: map[string]vocab.Class{"Limit": vocab.Certain},
		},
		{
			name: "a_directory_the_load_reads_and_the_user_cannot_list", entry: "data/pg",
			make: unlistable, code: "findings",
			notes: []string{"data/pg"}, findings: map[string]vocab.Class{"Limit": vocab.Possible},
		},
		{
			name: "a_directory_only_rule_n_reads_and_the_user_cannot_list", entry: "testdata/pg",
			make: unlistable, code: "findings",
			notes: []string{"testdata/pg"}, findings: map[string]vocab.Class{"Limit": vocab.Certain},
		},
		{
			name: "a_go_file_the_user_cannot_open", entry: "data/secret.go",
			make: unopenable, code: "findings",
			notes: []string{"data/secret.go"}, findings: map[string]vocab.Class{"unused": vocab.Certain, "Limit": vocab.Possible},
		},
		{
			name: "a_pipe_named_a_go_file_in_a_package", entry: "p/x.go", tree: withHelper, make: sandbox.Fifo, code: "findings",
			notes: []string{"p/x.go"}, findings: map[string]vocab.Class{"unused": vocab.Certain, "Limit": vocab.Possible, "helper": vocab.Possible},
		},
		{
			name: "a_go_file_linked_to_a_device_in_a_package", entry: "p/z.go", tree: withHelper, make: symlinkTo("/dev/zero"), code: "findings",
			notes: []string{"p/z.go"}, findings: map[string]vocab.Class{"unused": vocab.Certain, "Limit": vocab.Possible, "helper": vocab.Possible},
		},
		{
			name: "a_socket_named_a_go_file_in_a_package", entry: "p/s.go", tree: withHelper, make: makeSocket, code: "findings",
			notes: []string{"p/s.go"}, findings: map[string]vocab.Class{"unused": vocab.Certain, "Limit": vocab.Possible, "helper": vocab.Possible},
		},
		{
			name: "a_go_file_the_user_cannot_open_in_a_package", entry: "p/secret.go", tree: withHelper, make: unopenable, code: "findings",
			notes: []string{"p/secret.go"}, findings: map[string]vocab.Class{"unused": vocab.Certain},
		},
		{
			name: "a_go_file_the_user_cannot_open_in_a_package_no_exemption_keeps", entry: "p/secret.go", tree: withHelperUnexempted, make: unopenable, code: "findings",
			notes: []string{"p/secret.go"}, findings: map[string]vocab.Class{"unused": vocab.Certain, "Limit": vocab.Possible, "helper": vocab.Possible},
		},
		{
			name: "the_directory_of_the_only_go_file", entry: "src",
			tree: map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
				"src/main.go":      "package main\n\nfunc main() {}\n",
				repositoryDocument: `{"target": {"kind": "application"}}`,
			},
			make: unlistable, code: "clean", notes: []string{"src"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tree := tc.tree
			if tree == nil {
				tree = entryModule
			}
			dir := writeModule(t, tree)
			tc.make(t, filepath.Join(dir, filepath.FromSlash(tc.entry)))

			reportPath := filepath.Join(sandbox.Dir(t), "report.json")
			got, stalled := invokedWithin(t, 2*time.Minute, dir, "analyze", "--target=.", "--report="+reportPath, "--min-confidence=possible")
			if stalled {
				t.Fatalf("analyze over a tree holding %s did not return within two minutes: it opened an entry before deciding its kind", tc.name)
			}
			if want := contractExitCodes(t)[tc.code]; got.code != want {
				t.Fatalf("analyze over a tree holding %s = %d, want %d (%s)\nstderr: %s", tc.name, got.code, want, tc.code, got.stderr)
			}
			envelope := envelopeAt(t, reportPath)
			var notes []string
			for _, note := range envelope.Notes {
				if note.Kind == report.NoteKindUnreadEntry {
					notes = append(notes, note.Path)
				}
			}
			if !slices.Equal(notes, tc.notes) {
				t.Errorf("analyze over a tree holding %s wrote unread-entry notes for %q, want %q", tc.name, notes, tc.notes)
			}
			findings := make(map[string]vocab.Class)
			for i := range envelope.Findings {
				findings[envelope.Findings[i].Symbol.Name] = envelope.Findings[i].Confidence
			}
			if want := tc.findings; !maps.Equal(findings, want) {
				t.Errorf("analyze over a tree holding %s reported %v, want %v", tc.name, findings, want)
			}
		})
	}
}

// writeFile makes path a regular file holding body.
func writeFile(body string) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := sandbox.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
}

// makeDir makes path an empty directory.
func makeDir(t *testing.T, path string) {
	t.Helper()
	if err := sandbox.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

// makeSocket makes a Unix socket at path, an inode no listener holds, else one a
// listener holds until the test ends.
func makeSocket(t *testing.T, path string) {
	t.Helper()
	if err := sandbox.Mknod(path, syscall.S_IFSOCK|0o600, 0); err == nil {
		return
	}
	listener, err := sandbox.ListenUnix(path)
	if err != nil {
		t.Skipf("Setup: this host makes no Unix socket at %s: %v", path, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
}

// symlinkTo makes path a symbolic link to target.
func symlinkTo(target string) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := sandbox.Symlink(target, path); err != nil {
			t.Skipf("Setup: this host makes no such symbolic link: %v", err)
		}
	}
}

// unlistable makes path a directory the user cannot list until the test ends,
// creating it where it is absent.
func unlistable(t *testing.T, path string) {
	t.Helper()
	asNonRootUser(t)
	if err := sandbox.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := sandbox.Chmod(path, 0); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = sandbox.Chmod(path, 0o700) })
}

// unopenable makes path a Go file the user cannot open.
func unopenable(t *testing.T, path string) {
	t.Helper()
	asNonRootUser(t)
	if err := sandbox.WriteFile(path, []byte("package data\n"), 0); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

// asNonRootUser skips a row whose entry the run can read only because it runs as
// root.
func asNonRootUser(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("Setup: a mode of 0 leaves an entry readable to root")
	}
}

// TestAnalyzeMeetsAFileTheToolchainWouldBlockOn pins, within two minutes a row,
// what a special file or a link the go command would read does to a run: one in a
// package directory is hidden and noted, at its path through a link too; a module
// file, workspace file or vendor list a named module reads ends the run naming it,
// and drops a searched module; a go env file is read by no toolchain; and on a tree
// with no special file the go command reads, links to an ancestor, out of every
// tree, into /proc, multiplying paths or into a directory a dot starts end as on
// dev.50, byte for byte where a row says so. Each path is relative where it resolves.
func TestAnalyzeMeetsAFileTheToolchainWouldBlockOn(t *testing.T) {
	t.Parallel()
	siblings := map[string]string{
		"a/go.mod":         "module example.com/a\n\ngo 1.27.1\n",
		"a/main.go":        "package main\n\nfunc main() {}\n\nfunc unused() {}\n",
		"b/go.mod":         "module example.com/b\n\ngo 1.27.1\n",
		"b/q/q.go":         "package q\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	nested := map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nfunc main() {}\n\nfunc unused() {}\n",
		"sub/go.mod":       "module example.com/sub\n\ngo 1.27.1\n",
		"sub/s.go":         "package sub\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	unparsed := maps.Clone(siblings)
	unparsed["b/go.work"] = "garbage here\n"
	unparsed["b/cmd/one.go"] = "package main\n\nimport _ \"example.com/b/q\"\n\nfunc main() {}\n"
	unparsed["b/cmd/two.go"] = "package main\n\nfunc main() {}\n"
	replacing := maps.Clone(siblings)
	replacing["a/go.mod"] = "module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n"
	replacing["a/main.go"] = "package main\n\nimport _ \"example.com/b/q\"\n\nfunc main() {}\n"
	fromA := maps.Clone(replacing)
	fromA["a/"+repositoryDocument] = replacing[repositoryDocument]
	throughLink := maps.Clone(fromA)
	throughLink["a/go.mod"] = "module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => " + aliasMark + "/b\n"
	workspace := map[string]string{
		"go.work":          "go 1.21\n\nuse ./a\n",
		"a/go.mod":         "module example.com/a\n\ngo 1.21\n",
		"a/main.go":        "package main\n\nfunc main() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	ancestor := map[string]string{
		"go.mod":           "module example.com/a\n\ngo 1.22\n",
		"main.go":          "package main\n\nimport \"example.com/a/p/q/up/r\"\n\nfunc main() { r.R() }\n\nfunc unused() {}\n",
		"p/q/q.go":         "package q\n",
		"p/r/r.go":         "package r\n\n// R is what main calls.\nfunc R() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	direct := maps.Clone(ancestor)
	direct["main.go"] = "package main\n\nimport \"example.com/a/p/r\"\n\nfunc main() { r.R() }\n\nfunc unused() {}\n"
	unrequired := map[string]string{
		"a/go.mod":                "module example.com/a\n\ngo 1.22\n\nreplace example.com/z => ../z\n",
		"a/main.go":               "package main\n\nfunc main() {}\n\nfunc unused() {}\n",
		"a/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	unused := map[string]vocab.Class{"unused": vocab.Certain}
	// Below m, a module importing p, and beside it a graph of links four wide and
	// nine deep at out, with a package at its end, which m/p/l leads to.
	linked := map[string]string{
		"m/go.mod":                "module example.com/m\n\ngo 1.22\n",
		"m/main.go":               "package main\n\nimport _ \"example.com/m/p\"\n\nfunc main() {}\n\nfunc unused() {}\n",
		"m/p/p.go":                "package p\n",
		"m/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	dag := func(at string) map[string]string {
		links := make(map[string]string)
		for i := range 9 {
			for j := range 4 {
				links[fmt.Sprintf("%s/L%d/s%d", at, i, j)] = fmt.Sprintf("../L%d", i+1)
			}
		}
		return links
	}
	outsideDAG := dag("out")
	outsideDAG["m/p/l"] = "../../out/L0"
	parent := map[string]string{
		"repo/go/go.mod":                "module example.com/a\n\ngo 1.22\n",
		"repo/go/main.go":               "package main\n\nimport \"example.com/a/up/shared\"\n\nfunc main() { shared.S() }\n\nfunc unused() {}\n",
		"repo/shared/s.go":              "package shared\n\n// S is what main calls.\nfunc S() {}\n",
		"repo/go/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	dotted := map[string]string{
		"go.mod":           "module example.com/a\n\ngo 1.22\n",
		"main.go":          "package main\n\nimport \"example.com/a/.hid\"\n\nfunc main() { hid.H() }\n\nfunc unused() {}\n",
		".hid/p.go":        "package hid\n\n// H is what main calls.\nfunc H() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	// A module whose main package imports store/objects, a package beside the HEAD
	// file and the refs directory git writes beside its object store, in a tree
	// whose .git directory holds one.
	objects := map[string]string{
		"go.mod":                "module example.com/a\n\ngo 1.22\n",
		"main.go":               "package main\n\nimport \"example.com/a/store/objects\"\n\nfunc main() { objects.Used() }\n\nfunc unused() {}\n",
		"store/HEAD":            "ref: refs/heads/main\n",
		"store/refs/heads/main": "",
		"store/objects/o.go":    "package objects\n\n// Used is what main calls.\nfunc Used() {}\n",
		".git/HEAD":             "ref: refs/heads/main\n",
		".git/refs/heads/main":  "",
		repositoryDocument:      `{"target": {"kind": "application"}}`,
	}
	// Trees below m: main imports a/d through a/b/up, a link to a; or a
	// package below a link that is not there; or p, whose file importing "C"
	// imports q through l, a link out of the target, and r, which nothing else
	// imports.
	cycle := map[string]string{
		"m/go.mod":                "module example.com/m\n\ngo 1.22\n",
		"m/main.go":               "package main\n\nimport \"example.com/m/a/b/up/d\"\n\nfunc main() { d.F() }\n\nfunc unused() {}\n",
		"m/a/d/d.go":              "package d\n\nfunc F() {}\n",
		"ext/z/z.go":              "package z\n",
		"m/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	missingThroughCycle := maps.Clone(cycle)
	missingThroughCycle["m/main.go"] = "package main\n\nimport _ \"example.com/m/a/b/up/nope\"\n\nfunc main() {}\n"
	missingThroughLink := map[string]string{
		"m/go.mod":                "module example.com/m\n\ngo 1.22\n",
		"m/main.go":               "package main\n\nimport _ \"example.com/m/l/zz\"\n\nfunc main() {}\n",
		"m/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	cgo := map[string]string{
		"m/go.mod":  "module example.com/m\n\ngo 1.22\n",
		"m/main.go": "package main\n\nimport \"example.com/m/p\"\n\nfunc main() { p.Run() }\n",
		"m/p/a.go":  "package p\n\nfunc Run() {}\n\nfunc helper() {}\n",
		"m/p/c.go": "package p\n\n// #include <stdlib.h>\nimport \"C\"\n\nimport (\n\t\"example.com/m/l/q\"\n\t\"example.com/m/r\"\n)\n\n" +
			"func init() { helper(); q.F(); r.G() }\n",
		"m/r/r.go":                "package r\n\nfunc G() {}\n\nfunc H() {}\n",
		"out/q/q.go":              "package q\n\nfunc F() {}\n",
		"m/" + repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`,
	}
	outside := maps.Clone(linked)
	outside["m/main.go"] = "package main\n\nimport _ \"example.com/m/shared/q\"\n\nfunc main() {}\n\nfunc unused() {}\n"
	outside["ext/q/q.go"] = "package q\n"
	tests := []struct {
		name, entry string
		tree        map[string]string
		target      string            // the directory the run starts from below the tree, the tree's root where empty
		goenv       bool              // whether GOENV names the entry
		relative    bool              // whether that GOENV is relative to the target, the run starting from its parent
		alias       bool              // whether GOENV, and aliasMark in the tree, spell the tree through a link to it
		links       map[string]string // the symbolic links below the tree, to their targets
		make        func(t *testing.T, path string)
		code        string
		failure     string   // the entry the failure names, relative to the target
		stderr      string   // the whole standard error, where the row pins it
		detail      string   // what the failure says of it, "no regular file, but a pipe" where empty
		fix         string   // the fix the failure ends with, "replace it with a regular file" where empty
		notes       []string // the directories the module-not-loaded notes name
		hidden      []string // what the unread-entry notes name, with the path the message starts with
		findings    map[string]vocab.Class
	}{
		{
			name: "a_searched_module_whose_module_file_is_a_pipe", entry: "b/go.mod", tree: siblings,
			make: removedThen(sandbox.Fifo), code: "findings", notes: []string{"b"}, findings: unused,
		},
		{
			name: "a_searched_module_holding_a_pipe_named_a_go_file", entry: "b/q/x.go", tree: siblings,
			make: sandbox.Fifo, code: "findings", hidden: []string{"b/q/x.go b/q/x.go"},
			findings: map[string]vocab.Class{"unused": vocab.Certain, "q.go": vocab.Certain},
		},
		{
			name: "a_searched_module_whose_workspace_file_does_not_parse_holding_a_pipe_its_scripts_import", entry: "b/q/x.go", tree: unparsed,
			make: sandbox.Fifo, code: "findings", notes: []string{"b"}, findings: unused,
		},
		{
			name: "a_nested_module_whose_module_file_is_a_pipe", entry: "sub/go.mod", tree: nested,
			make: removedThen(sandbox.Fifo), code: "findings", findings: unused,
		},
		{
			name: "a_pipe_named_a_go_file_in_a_package", entry: "p/x.go", make: sandbox.Fifo,
			code: "findings", hidden: []string{"p/x.go p/x.go"}, findings: unused,
		},
		{
			name: "a_go_file_linked_to_a_device_in_a_package", entry: "p/z.go", make: symlinkTo("/dev/zero"),
			code: "findings", hidden: []string{"p/z.go p/z.go"}, findings: unused,
		},
		{
			name: "a_searched_module_importing_a_package_of_a_replaced_sibling_holding_a_pipe", entry: "b/q/x.go", tree: replacing,
			make: sandbox.Fifo, code: "clean", hidden: []string{"b/q/x.go b/q/x.go"},
		},
		{
			name: "a_target_importing_a_package_of_a_replaced_module_holding_a_pipe", entry: "b/q/x.go", tree: fromA, target: "a",
			make: sandbox.Fifo, code: "clean", hidden: []string{". ../b/q/x.go"},
		},
		{
			name: "a_module_whose_only_go_file_is_a_pipe", entry: "x.go",
			tree: map[string]string{"go.mod": "module example.com/m\n\ngo 1.27.1\n", repositoryDocument: `{"target": {"kind": "application"}}`},
			make: sandbox.Fifo, code: "clean", hidden: []string{"x.go x.go"},
		},
		{
			name: "a_workspace_whose_vendor_list_is_a_pipe", entry: "vendor/modules.txt", tree: workspace,
			make: inNewDir(sandbox.Fifo), code: "failure", failure: "vendor/modules.txt",
		},
		{
			name: "a_go_env_file_that_is_a_pipe", entry: "env", tree: nested, goenv: true,
			make: sandbox.Fifo, code: "findings", hidden: []string{". the go env file GOENV names, env,"}, findings: unused,
		},
		{
			name: "a_go_env_file_that_is_a_pipe_named_relative_to_the_target", entry: "env", tree: nested, goenv: true, relative: true,
			make: sandbox.Fifo, code: "findings", hidden: []string{". the go env file GOENV names, env,"}, findings: unused,
		},
		{
			name: "a_go_env_file_that_is_a_pipe_named_through_a_link_to_the_target", entry: "env", tree: nested, goenv: true, alias: true,
			make: sandbox.Fifo, code: "findings", hidden: []string{". the go env file GOENV names, env,"}, findings: unused,
		},
		{
			name: "a_target_importing_a_package_of_a_module_replaced_through_a_link_holding_a_pipe", entry: "b/q/x.go", tree: throughLink, target: "a",
			alias: true, make: sandbox.Fifo, code: "clean", hidden: []string{". ../b/q/x.go"},
		},
		{
			name: "a_package_holding_a_pipe_imported_through_a_link_to_an_ancestor", entry: "p/r/x.go", tree: ancestor,
			links: map[string]string{"p/q/up": ".."}, make: sandbox.Fifo, code: "findings",
			hidden:   []string{"p/q/up/r/x.go p/q/up/r/x.go", "p/r/x.go p/r/x.go"},
			findings: map[string]vocab.Class{"unused": vocab.Certain, "q.go": vocab.Certain, "r.go": vocab.Certain},
		},
		{
			name: "a_package_holding_a_pipe_a_link_to_an_ancestor_reaches_imported_directly", entry: "p/r/x.go", tree: direct,
			links: map[string]string{"p/q/up": ".."}, make: sandbox.Fifo, code: "findings",
			hidden:   []string{"p/r/x.go p/r/x.go"},
			findings: map[string]vocab.Class{"unused": vocab.Certain, "q.go": vocab.Certain},
		},
		{
			name: "a_graph_of_links_whose_paths_multiply_behind_a_link_out_of_the_target", entry: "out/L9/z.go", tree: linked, target: "m",
			links: outsideDAG, make: inNewDir(writeFile("package z\n")), code: "findings", findings: unused,
		},
		{
			name: "a_graph_of_links_whose_paths_multiply_inside_the_target", entry: "m/dag/L9/z.go", tree: linked, target: "m",
			links: dag("m/dag"), make: inNewDir(writeFile("package z\n")), code: "findings",
			findings: map[string]vocab.Class{"unused": vocab.Certain, "z.go": vocab.Certain},
		},
		{
			name: "a_link_into_proc_nothing_imports_through", entry: "m/p/sys", tree: linked, target: "m",
			make: symlinkTo("/proc"), code: "findings", findings: unused,
		},
		{
			name: "a_link_to_the_file_system_root_nothing_imports_through", entry: "m/up", tree: linked, target: "m",
			make: symlinkTo("/"), code: "findings", findings: unused,
		},
		{
			name: "an_import_through_a_link_to_the_parent_of_the_target", entry: "repo/go/up", tree: parent, target: "repo/go",
			make: symlinkTo(".."), code: "findings", findings: unused,
		},
		{
			name: "an_import_through_a_link_out_of_the_target", entry: "m/shared", tree: outside, target: "m",
			make: symlinkTo("../ext"), code: "findings",
			findings: map[string]vocab.Class{"unused": vocab.Certain, "p.go": vocab.Certain},
		},
		{
			name: "an_import_through_a_link_to_an_ancestor_beside_a_link_to_the_file_system_root", entry: "m/a/c/ext", tree: cycle, target: "m",
			links: map[string]string{"m/a/b/up": ".."}, make: inNewDir(symlinkTo("/")), code: "findings",
			findings: map[string]vocab.Class{"unused": vocab.Certain, "d.go": vocab.Certain},
		},
		{
			name: "an_import_through_a_link_to_an_ancestor_beside_a_link_to_a_pipe_nothing_imports_through", entry: "ext/z/x.go", tree: cycle, target: "m",
			links: map[string]string{"m/a/b/up": "..", "m/a/c/ext": "../../../ext"}, make: sandbox.Fifo, code: "findings",
			findings: map[string]vocab.Class{"unused": vocab.Certain, "d.go": vocab.Certain},
		},
		{
			name: "an_import_of_a_missing_package_through_a_link_to_an_ancestor_beside_a_pipe_in_testdata", entry: "m/a/testdata/x.go", tree: missingThroughCycle, target: "m",
			links: map[string]string{"m/a/b/up": ".."}, make: inNewDir(sandbox.Fifo), code: "failure",
			stderr: "setup failure: missing-module: main.go:3:8: the import example.com/m/a/b/up/nope names a package nothing provides; run the generator, the build or the install that writes it\n",
		},
		{
			name: "an_import_of_a_missing_package_through_a_link_into_proc", entry: "m/l", tree: missingThroughLink, target: "m",
			make: symlinkTo("/proc"), code: "failure",
			stderr: "setup failure: missing-module: main.go:3:8: the import example.com/m/l/zz names a package nothing provides; run the generator, the build or the install that writes it\n",
		},
		{
			name: "an_import_of_a_missing_package_through_a_link_out_of_every_tree", entry: "m/l", tree: missingThroughLink, target: "m",
			make: symlinkTo("/usr/share"), code: "failure",
			stderr: "setup failure: missing-module: main.go:3:8: the import example.com/m/l/zz names a package nothing provides; run the generator, the build or the install that writes it\n",
		},
		{
			name: "a_cgo_file_importing_a_package_through_a_link_out_of_the_target", entry: "m/l", tree: cgo, target: "m",
			make: symlinkTo("../out"), code: "findings", findings: map[string]vocab.Class{"H": vocab.Certain},
		},
		{
			name: "an_imported_package_in_a_directory_a_dot_starts", entry: ".hid/q.go", tree: dotted,
			make: writeFile("package hid\n"), code: "findings", findings: unused,
		},
		{
			name: "an_imported_package_in_a_directory_a_dot_starts_holding_a_pipe", entry: ".hid/x.go", tree: dotted,
			make: sandbox.Fifo, code: "findings", hidden: []string{".hid/x.go .hid/x.go"}, findings: unused,
		},
		{
			name: "an_imported_package_named_objects_beside_a_head_file_and_refs_holding_a_pipe", entry: "store/objects/x.go", tree: objects,
			make: sandbox.Fifo, code: "findings", hidden: []string{"store/objects/x.go store/objects/x.go"}, findings: unused,
		},
		{
			name: "a_pipe_in_the_git_object_store", entry: ".git/objects/x.go", tree: objects,
			make: inNewDir(sandbox.Fifo), code: "findings", findings: unused,
		},
		{
			name: "an_unrequired_replaced_module_whose_module_file_is_a_pipe", entry: "z/go.mod", tree: unrequired, target: "a",
			make: inNewDir(sandbox.Fifo), code: "findings",
			findings: map[string]vocab.Class{"unused": vocab.Certain, "example.com/z": vocab.Certain},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tree := tc.tree
			if tree == nil {
				tree = entryModule
			}
			var alias string
			if tc.alias {
				alias = filepath.Join(sandbox.Dir(t), "alias")
				tree = maps.Clone(tree)
				for name, body := range tree {
					tree[name] = strings.ReplaceAll(body, aliasMark, filepath.ToSlash(alias))
				}
			}
			dir := writeModule(t, tree)
			if tc.alias {
				symlinkTo(dir)(t, alias)
			}
			for _, link := range slices.Sorted(maps.Keys(tc.links)) {
				path := filepath.Join(dir, filepath.FromSlash(link))
				if err := sandbox.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatalf("Setup: %v", err)
				}
				symlinkTo(tc.links[link])(t, path)
			}
			entry := filepath.Join(dir, filepath.FromSlash(tc.entry))
			tc.make(t, entry)
			from, target := filepath.Join(dir, tc.target), "."
			var env []string
			switch {
			case tc.relative:
				from, target = filepath.Dir(from), filepath.Base(from)
				env = []string{"GOENV=" + filepath.FromSlash(tc.entry)}
			case tc.goenv && tc.alias:
				env = []string{"GOENV=" + filepath.Join(alias, filepath.FromSlash(tc.entry))}
			case tc.goenv:
				env = []string{"GOENV=" + entry}
			}

			reportPath := filepath.Join(sandbox.Dir(t), "report.json")
			got, stalled := invokedWithinEnv(t, 2*time.Minute, from, env, "analyze", "--target="+target, "--report="+reportPath)
			if stalled {
				t.Fatalf("analyze over a tree holding %s did not return within two minutes: the toolchain read a file that blocks", tc.name)
			}
			if want := contractExitCodes(t)[tc.code]; got.code != want {
				t.Fatalf("analyze over a tree holding %s = %d, want %d (%s)\nstderr: %s", tc.name, got.code, want, tc.code, got.stderr)
			}
			if tc.stderr != "" && got.stderr != tc.stderr {
				t.Errorf("analyze over a tree holding %s wrote %q, want %q", tc.name, got.stderr, tc.stderr)
			}
			if tc.failure == "" && tc.code == "failure" {
				return
			}
			if tc.failure != "" {
				want := string(vocab.SetupUnreadableConfiguration) + ": "
				detail, fix := cmp.Or(tc.detail, "no regular file, but a pipe"), cmp.Or(tc.fix, "replace it with a regular file")
				if !strings.Contains(got.stderr, want) || !strings.Contains(got.stderr, "load: "+tc.failure+": "+detail) ||
					strings.Contains(got.stderr, dir) || !strings.HasSuffix(got.stderr, "; "+fix+"\n") {
					t.Errorf("analyze over a tree holding %s wrote %q, want the %s failure naming %s, ending with %q", tc.name, got.stderr, vocab.SetupUnreadableConfiguration, tc.failure, fix)
				}
				return
			}
			envelope := envelopeAt(t, reportPath)
			var notes, hidden []string
			for _, note := range envelope.Notes {
				switch note.Kind {
				case report.NoteKindModuleNotLoaded:
					notes = append(notes, note.Path)
				case report.NoteKindUnreadEntry:
					first, _, _ := strings.Cut(note.Message, " is ")
					hidden = append(hidden, note.Path+" "+first)
				}
				if strings.Contains(note.Message, dir) {
					t.Errorf("analyze over a tree holding %s wrote the note %q, want every path in it relative to the target root", tc.name, note.Message)
				}
			}
			if !slices.Equal(notes, tc.notes) || !slices.Equal(hidden, tc.hidden) {
				t.Errorf("analyze over a tree holding %s wrote module-not-loaded notes for %q and unread-entry notes %q, want %q and %q", tc.name, notes, hidden, tc.notes, tc.hidden)
			}
			findings := make(map[string]vocab.Class)
			for i := range envelope.Findings {
				findings[envelope.Findings[i].Symbol.Name] = envelope.Findings[i].Confidence
			}
			if !maps.Equal(findings, tc.findings) {
				t.Errorf("analyze over a tree holding %s reported %v, want %v", tc.name, findings, tc.findings)
			}
		})
	}
}

// inNewDir makes the directory of path before make makes the entry there.
func inNewDir(make func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := sandbox.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		make(t, path)
	}
}

// removedThen removes the regular file a tree wrote at path before make makes the
// entry there.
func removedThen(make func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		make(t, path)
	}
}

// invokedWithin is one invocation of the built command from dir, as [invokedIn]
// runs it, in a process group of its own that is killed whole at limit and again
// once the command returns, so no go command it started outlives the row; stalled
// reports that limit ended it.
func invokedWithin(t *testing.T, limit time.Duration, dir string, args ...string) (got invoked, stalled bool) {
	t.Helper()
	return invokedWithinEnv(t, limit, dir, nil, args...)
}

// invokedWithinEnv is [invokedWithin] with env added to the environment.
func invokedWithinEnv(t *testing.T, limit time.Duration, dir string, env []string, args ...string) (got invoked, stalled bool) {
	t.Helper()
	return invokedBinaryWithinEnv(t, commandBinary(t), limit, dir, env, args...)
}

// invokedBinaryWithin is [invokedWithin] of binary rather than the built command.
func invokedBinaryWithin(t *testing.T, binary string, limit time.Duration, dir string, args ...string) (got invoked, stalled bool) {
	t.Helper()
	return invokedBinaryWithinEnv(t, binary, limit, dir, nil, args...)
}

// invokedBinaryWithinEnv is [invokedBinaryWithin] with env added to the environment.
func invokedBinaryWithinEnv(t *testing.T, binary string, limit time.Duration, dir string, env []string, args ...string) (got invoked, stalled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), limit)
	defer cancel()
	cmd := deadline.Command(ctx, binary, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	deadline.Reap(cmd)
	if ctx.Err() != nil {
		return invoked{}, true
	}
	if _, exited := errors.AsType[*exec.ExitError](err); err != nil && !exited {
		t.Fatalf("Setup: run %q from %s: %v", args, dir, err)
	}
	return invoked{stdout: stdout.String(), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}, false
}
