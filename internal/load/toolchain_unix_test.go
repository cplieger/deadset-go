//go:build unix

package load

import (
	"archive/zip"
	"bytes"
	"cmp"
	"fmt"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/mod/module"
)

// Trees the rows of the toolchain table build on: a target root holding no module
// file with a module a beside the module b a row breaks, and a module m with a
// package p, an ignored directory and a nested module sub building against m.
var (
	siblingTree = map[string]string{
		"a/go.mod":  "module example.com/a\n\ngo 1.27.1\n",
		"a/main.go": "package main\n\nfunc main() {}\n",
		"b/go.mod":  "module example.com/b\n\ngo 1.27.1\n",
		"b/b.go":    "package b\n",
		"b/q/q.go":  "package q\n",
	}
	moduleTree = map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.27.1\n\nignore ./data\n",
		"main.go":     "package main\n\nimport \"example.com/m/p\"\n\nfunc main() { p.Used() }\n",
		"p/p.go":      "package p\n\nfunc Used() {}\n",
		"data/d.go":   "package data\n",
		"_ex/e/e.go":  "package e\n\nimport \"example.com/m/p\"\n\nfunc init() { p.Used() }\n",
		"sub/go.mod":  "module example.com/sub\n\ngo 1.27.1\n\nrequire example.com/m v0.0.0\n\nreplace example.com/m => ../\n",
		"sub/main.go": "package main\n\nimport \"example.com/m/p\"\n\nfunc main() { p.Used() }\n",
		"sub/q/q.go":  "package q\n",
	}
)

// toolchainRow is one row of the toolchain table: a tree, what a row prepares in
// it and the entries it puts there, and what the load of its target makes of them.
type toolchainRow struct {
	name      string
	tree      map[string]string
	target    string                          // the target below the tree's root, the root where empty
	consumers []string                        // the declared consumers' directories below the tree's root
	prepare   func(t *testing.T, root string) // run on the written tree before the entries are made
	entries   map[string]func(*testing.T, string)
	failure   string   // the entry the unreadable-configuration failure names, none where empty
	fix       string   // the fix that failure ends with, [scope.IrregularFix] where empty
	refusal   string   // the class of a setup failure that names no entry of the row
	units     []string // the directories of the loaded units, relative to the tree's root
	dropped   []string // the directories of the dropped modules
	nested    []string // the module paths of the nested consumers
	scripts   []string // the loaded package-level declarations the programs name, as path.Name
	hidden    []string // the hidden entries where code may hide ([Result.Hidden])
	excluded  []string // the files the opaque-C check could not read ([Result.ExcludedByCgo])
}

// meets loads the row's target and checks what the load made of the row's entries.
func (tc toolchainRow) meets(t *testing.T) {
	root := writeTree(t, tc.tree)
	if tc.prepare != nil {
		tc.prepare(t, root)
	}
	for _, entry := range slices.Sorted(maps.Keys(tc.entries)) {
		path := filepath.Join(root, filepath.FromSlash(entry))
		if err := sandbox.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		tc.entries[entry](t, path)
	}
	doc := scope.Document{Target: scope.Module{Path: filepath.Join(root, tc.target)}}
	for _, c := range tc.consumers {
		doc.Consumers = append(doc.Consumers, scope.Module{Path: filepath.Join(root, c)})
	}
	got, err := loadScope(deadline.Context(t), doc, HostConfiguration())
	switch {
	case tc.failure != "":
		named := relativeTo(t, doc.Target.Path, filepath.Join(root, filepath.FromSlash(tc.failure)))
		fix := cmp.Or(tc.fix, scope.IrregularFix)
		if failed := failure.Of(err); err == nil || failed[0].Class != vocab.SetupUnreadableConfiguration ||
			!strings.Contains(failed[0].Detail, named) || strings.Contains(failed[0].Detail, root) ||
			!strings.HasSuffix(failed[0].Detail, "; "+fix) {
			t.Errorf("Load(a tree holding %s) = _, %v, want the unreadable-configuration failure naming %s from the target, ending with %q", tc.name, err, named, fix)
		}
		return
	case tc.refusal != "":
		failed := failure.Of(err)
		if err == nil || failed[0].Class != tc.refusal || slices.ContainsFunc(slices.Collect(maps.Keys(tc.entries)), func(entry string) bool {
			return namesEntry(failed[0].Detail, entry)
		}) {
			t.Errorf("Load(a tree holding %s) = _, %v, want the %s failure, naming none of the row's entries", tc.name, err, tc.refusal)
		}
		return
	case err != nil:
		t.Fatalf("Load(a tree holding %s) = _, %v, want no error", tc.name, err)
	}
	tc.check(t, root, &got)
}

// check checks the units, the drops, the nested consumers, the scripts and the
// hidden entries of got, the load of the row's tree at root.
func (tc toolchainRow) check(t *testing.T, root string, got *Result) {
	t.Helper()
	units := make([]string, 0, len(got.Units))
	for _, u := range got.Units {
		units = append(units, relativeTo(t, root, u.Dir))
	}
	dropped := make([]string, 0, len(got.Dropped))
	for _, d := range got.Dropped {
		dropped = append(dropped, relativeTo(t, root, d.Dir))
		if strings.Contains(d.Err.Error(), root) {
			t.Errorf("Load(a tree holding %s) dropped %s with %q, want every path it names relative to the target root", tc.name, d.Dir, d.Err)
		}
	}
	nested := make([]string, 0, len(got.Nested))
	for _, c := range got.Nested {
		if !slices.Contains(nested, c.ID) {
			nested = append(nested, c.ID)
		}
	}
	slices.Sort(nested)
	if !slices.Equal(units, tc.units) || !slices.Equal(dropped, nonNilStrings(tc.dropped)) || !slices.Equal(nested, nonNilStrings(tc.nested)) {
		t.Errorf("Load(a tree holding %s) loaded the units %q, dropped %q and nested %q, want %q, %q and %q",
			tc.name, units, dropped, nested, tc.units, tc.dropped, tc.nested)
	}
	if scripts := namedByPrograms(got); !slices.Equal(scripts, nonNilStrings(tc.scripts)) {
		t.Errorf("Load(a tree holding %s) programs name %q, want %q", tc.name, scripts, tc.scripts)
	}
	hidden := make([]string, 0, len(got.Hidden))
	for _, path := range got.Hidden {
		hidden = append(hidden, relativeTo(t, root, path))
	}
	if !slices.Equal(hidden, nonNilStrings(tc.hidden)) {
		t.Errorf("Load(a tree holding %s) hid %q where code may hide, want %q", tc.name, hidden, tc.hidden)
	}
	if excluded := nonNilStrings(got.ExcludedByCgo); !slices.Equal(excluded, nonNilStrings(tc.excluded)) {
		t.Errorf("Load(a tree holding %s) left out the cgo files %q, want %q", tc.name, excluded, tc.excluded)
	}
}

// namesEntry reports whether detail names entry as a whole path, not as the start
// of a longer one, such as an import path through a link the entry is.
func namesEntry(detail, entry string) bool {
	for rest := detail; ; {
		i := strings.Index(rest, entry)
		if i < 0 {
			return false
		}
		rest = rest[i+len(entry):]
		if rest == "" || !strings.ContainsRune("/-_.", rune(rest[0])) && !unicode.IsLetter(rune(rest[0])) && !unicode.IsDigit(rune(rest[0])) {
			return true
		}
	}
}

// TestLoadMeetsAFileTheToolchainWouldBlockOnBeforeItRuns pins, within a minute a
// row, what a special file a load may read does to it ([program.6]): one the
// overlay hides changes nothing the toolchain lists and is named where code may
// hide; a module file, workspace file or vendor list read outside the overlay ends
// the run or drops a module; and a load that needs a hidden entry ends the run
// naming it, every path relative to the target root ([program.10]).
func TestLoadMeetsAFileTheToolchainWouldBlockOnBeforeItRuns(t *testing.T) {
	stableToolchain(t)
	tests := []toolchainRow{
		{
			name: "a_searched_module_whose_module_file_is_a_pipe", tree: siblingTree,
			entries: map[string]func(*testing.T, string){"b/go.mod": replaced(sandbox.Fifo)},
			units:   []string{"a"}, dropped: []string{"b"},
		},
		{
			name: "a_searched_module_whose_workspace_file_is_a_pipe", tree: siblingTree,
			entries: map[string]func(*testing.T, string){"b/go.work": sandbox.Fifo},
			units:   []string{"a"}, dropped: []string{"b"},
		},
		{
			name: "a_searched_module_whose_workspace_file_does_not_parse", tree: siblingTree,
			entries: map[string]func(*testing.T, string){"b/go.work": writeBody("garbage here\n")},
			units:   []string{"a"}, dropped: []string{"b"},
		},
		{
			name: "a_searched_module_whose_module_file_is_a_pipe_with_scripts", tree: siblingTree,
			entries: map[string]func(*testing.T, string){
				"b/go.mod":     replaced(sandbox.Fifo),
				"b/cmd/one.go": writeBody("package main\n\nimport \"net/http\"\n\nfunc main() { _ = http.StatusOK }\n"),
				"b/cmd/two.go": writeBody("package main\n\nimport \"net/http\"\n\nfunc main() { _ = http.StatusOK }\n"),
			},
			units: []string{"a"}, dropped: []string{"b"},
		},
		{
			name: "a_searched_module_whose_workspace_file_does_not_parse_holding_a_pipe_its_scripts_import", tree: siblingTree,
			entries: map[string]func(*testing.T, string){
				"b/go.work":    writeBody("garbage here\n"),
				"b/q/x.go":     sandbox.Fifo,
				"b/cmd/one.go": writeBody("package main\n\nimport _ \"example.com/b/q\"\n\nfunc main() {}\n"),
				"b/cmd/two.go": writeBody("package main\n\nfunc main() {}\n"),
			},
			units: []string{"a"}, dropped: []string{"b"},
		},
		{
			name: "a_searched_module_holding_a_pipe_named_a_go_file", tree: siblingTree,
			entries: map[string]func(*testing.T, string){"b/q/x.go": sandbox.Fifo},
			units:   []string{"a", "b"}, hidden: []string{"b/q/x.go"},
		},
		{
			name: "a_nested_module_whose_module_file_is_a_pipe", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"sub/go.mod": replaced(sandbox.Fifo)},
			units:   []string{"."}, nested: []string{"example.com/m"},
		},
		{
			name: "a_nested_module_holding_a_pipe_named_a_go_file", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"sub/q/x.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"sub/q/x.go"},
		},
		{
			name: "a_pipe_named_a_go_file_in_a_package", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"p/x.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"p/x.go"},
		},
		{
			name: "a_pipe_named_a_test_file_in_a_package", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"p/x_test.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"p/x_test.go"},
		},
		{
			name: "a_pipe_named_a_c_file_in_a_package", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"p/x.c": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"p/x.c"},
		},
		{
			name: "a_pipe_named_a_go_file_alone_in_a_directory", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"r/x.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"r/x.go"},
		},
		{
			name: "a_go_file_linked_to_an_endless_device", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"p/z.go": linkTo("/dev/zero")},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"p/z.go"},
		},
		{
			name: "a_go_file_linked_to_an_empty_device", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"p/n.go": linkTo("/dev/null")},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"p/n.go"},
		},
		{
			name: "a_socket_named_a_go_file_in_a_package", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"p/s.go": socket},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"p/s.go"},
		},
		{
			name: "a_pipe_named_a_go_file_in_a_member_the_target_names",
			tree: map[string]string{
				"go.mod":      "module example.com/m\n\ngo 1.27.1\n",
				"go.work":     "go 1.27.1\n\nuse (\n\t.\n\t./w\n)\n",
				"main.go":     "package main\n\nfunc main() {}\n",
				"w/go.mod":    "module example.com/w\n\ngo 1.27.1\n",
				"w/w.go":      "package w\n",
				"w/q/q.go":    "package q\n",
				"w/q/keep.go": "package q\n",
			},
			entries: map[string]func(*testing.T, string){"w/q/x.go": sandbox.Fifo},
			units:   []string{".", "w"}, hidden: []string{"w/q/x.go"},
		},
		{
			name: "a_pipe_named_a_go_file_in_a_workspace_reader",
			tree: map[string]string{
				"go.work":     "go 1.27.1\n\nuse (\n\t./lib\n\t./app\n)\n",
				"lib/go.mod":  "module example.com/lib\n\ngo 1.27.1\n",
				"lib/lib.go":  "package lib\n\n// Used is what app calls.\nfunc Used() {}\n",
				"app/go.mod":  "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/lib v0.0.0\n",
				"app/main.go": "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.Used() }\n",
				"app/q/q.go":  "package q\n",
			},
			target:  "lib",
			entries: map[string]func(*testing.T, string){"app/q/x.go": sandbox.Fifo},
			units:   []string{"lib"}, nested: []string{"example.com/app"}, hidden: []string{"app/q/x.go"},
		},
		{
			name: "a_pipe_named_a_go_file_in_a_declared_consumer",
			tree: map[string]string{
				"lib/go.mod":  "module example.com/lib\n\ngo 1.27.1\n",
				"lib/lib.go":  "package lib\n\n// Used is what app calls.\nfunc Used() {}\n",
				"app/go.mod":  "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n",
				"app/main.go": "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.Used() }\n",
				"app/q/q.go":  "package q\n",
			},
			target: "lib", consumers: []string{"app"},
			entries: map[string]func(*testing.T, string){"app/q/x.go": sandbox.Fifo},
			units:   []string{"lib"}, hidden: []string{"app/q/x.go"},
		},
		{
			name: "a_pipe_named_a_go_file_in_an_underscore_package", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"_ex/e/x.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"_ex/e/x.go"},
		},
		{
			name: "a_searched_module_importing_a_package_of_a_replaced_sibling_holding_a_pipe", tree: siblingTree,
			entries: map[string]func(*testing.T, string){
				"a/go.mod":  writeBody("module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n"),
				"a/main.go": writeBody("package main\n\nimport _ \"example.com/b/q\"\n\nfunc main() {}\n"),
				"b/q/x.go":  sandbox.Fifo,
			},
			units: []string{"a", "b"}, hidden: []string{"b/q/x.go"},
		},
		{
			name: "a_target_importing_a_package_of_a_replaced_module_holding_a_pipe", tree: siblingTree, target: "a",
			entries: map[string]func(*testing.T, string){
				"a/go.mod":  writeBody("module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n"),
				"a/main.go": writeBody("package main\n\nimport _ \"example.com/b/q\"\n\nfunc main() {}\n"),
				"b/q/x.go":  sandbox.Fifo,
			},
			units: []string{"a"}, hidden: []string{"b/q/x.go"},
		},
		{
			name: "a_target_whose_replaced_module_holds_a_pipe_in_a_package_nothing_imports", tree: siblingTree, target: "a",
			entries: map[string]func(*testing.T, string){
				"a/go.mod":  writeBody("module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n"),
				"a/main.go": writeBody("package main\n\nimport _ \"example.com/b\"\n\nfunc main() {}\n"),
				"b/q/x.go":  sandbox.Fifo,
			},
			units: []string{"a"},
		},
		{
			name: "a_target_whose_replaced_module_file_is_a_pipe", tree: siblingTree, target: "a",
			entries: map[string]func(*testing.T, string){
				"a/go.mod":  writeBody("module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n"),
				"a/main.go": writeBody("package main\n\nimport _ \"example.com/b\"\n\nfunc main() {}\n"),
				"b/go.mod":  replaced(sandbox.Fifo),
			},
			failure: "b/go.mod",
		},
		{
			name: "a_target_importing_a_package_of_a_workspace_member_holding_a_pipe",
			tree: map[string]string{
				"go.work":      "go 1.27.1\n\nuse (\n\t./lib\n\t./other\n)\n",
				"lib/go.mod":   "module example.com/lib\n\ngo 1.27.1\n",
				"lib/lib.go":   "package lib\n\nimport _ \"example.com/other/q\"\n",
				"other/go.mod": "module example.com/other\n\ngo 1.27.1\n",
				"other/q/q.go": "package q\n",
			},
			target:  "lib",
			entries: map[string]func(*testing.T, string){"other/q/x.go": sandbox.Fifo},
			units:   []string{"lib"}, hidden: []string{"other/q/x.go"},
		},
		{
			name: "a_nested_module_importing_a_package_of_a_unit_holding_a_pipe",
			tree: map[string]string{
				"a/go.mod":      "module example.com/a\n\ngo 1.27.1\n",
				"a/a.go":        "package a\n",
				"a/q/q.go":      "package q\n",
				"a/sub/go.mod":  "module example.com/sub\n\ngo 1.27.1\n\nrequire example.com/a v0.0.0\n\nreplace example.com/a => ../\n",
				"a/sub/main.go": "package main\n\nimport _ \"example.com/a/q\"\n\nfunc main() {}\n",
				"c/go.mod":      "module example.com/c\n\ngo 1.27.1\n",
				"c/main.go":     "package main\n\nfunc main() {}\n",
			},
			entries: map[string]func(*testing.T, string){"a/q/x.go": sandbox.Fifo},
			units:   []string{"a", "a/sub", "c"}, hidden: []string{"a/q/x.go"},
		},
		{
			name: "an_underscore_package_importing_another_holding_a_pipe", tree: moduleTree,
			entries: map[string]func(*testing.T, string){
				"_ex/e/e.go":      writeBody("package e\n\nimport _ \"example.com/m/_ex/shared\"\n"),
				"_ex/shared/s.go": writeBody("package shared\n"),
				"_ex/shared/x.go": sandbox.Fifo,
			},
			units: []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"_ex/shared/x.go"},
		},
		{
			name: "a_target_importing_a_package_of_an_ignored_directory_holding_a_pipe", tree: moduleTree,
			entries: map[string]func(*testing.T, string){
				"main.go":   writeBody("package main\n\nimport (\n\t_ \"example.com/m/data\"\n\t\"example.com/m/p\"\n)\n\nfunc main() { p.Used() }\n"),
				"data/x.go": sandbox.Fifo,
			},
			units: []string{"."}, nested: []string{"example.com/m", "example.com/sub"}, hidden: []string{"data/x.go"},
		},
		{
			name: "a_target_importing_a_package_through_a_linked_directory_holding_a_pipe",
			tree: map[string]string{
				"m/go.mod":     "module example.com/m\n\ngo 1.27.1\n",
				"m/main.go":    "package main\n\nimport _ \"example.com/m/shared/q\"\n\nfunc main() {}\n",
				"m/ext/q/q.go": "package q\n",
			},
			target: "m",
			entries: map[string]func(*testing.T, string){
				"m/shared":     linkTo("ext"),
				"m/ext/q/x.go": sandbox.Fifo,
			},
			units: []string{"m"}, hidden: []string{"m/ext/q/x.go", "m/shared/q/x.go"},
		},
		{
			name: "a_target_whose_vendor_list_is_a_pipe", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"vendor/modules.txt": sandbox.Fifo},
			failure: "vendor/modules.txt",
		},
		{
			name: "a_target_importing_a_vendored_package_holding_a_pipe",
			tree: map[string]string{
				"go.mod":                    "module example.com/m\n\ngo 1.27.1\n\nrequire example.com/v v1.0.0\n",
				"main.go":                   "package main\n\nimport _ \"example.com/v\"\n\nfunc main() {}\n",
				"vendor/modules.txt":        "# example.com/v v1.0.0\n## explicit\nexample.com/v\n",
				"vendor/example.com/v/v.go": "package v\n",
			},
			entries: map[string]func(*testing.T, string){"vendor/example.com/v/x.go": sandbox.Fifo},
			units:   []string{"."}, hidden: []string{"vendor/example.com/v/x.go"},
		},
		{
			name: "a_target_whose_replaced_module_holds_a_vendor_list_that_is_a_pipe", tree: siblingTree, target: "a",
			entries: map[string]func(*testing.T, string){
				"a/go.mod":             writeBody("module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n"),
				"a/main.go":            writeBody("package main\n\nimport _ \"example.com/b/q\"\n\nfunc main() {}\n"),
				"b/vendor/modules.txt": sandbox.Fifo,
			},
			units: []string{"a"},
		},
		{
			name: "a_target_whose_module_file_is_a_pipe", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"go.mod": replaced(sandbox.Fifo)},
			failure: "go.mod",
		},
		{
			name:    "a_target_whose_workspace_file_above_it_is_a_pipe",
			tree:    map[string]string{"m/go.mod": "module example.com/m\n\ngo 1.27.1\n", "m/main.go": "package main\n\nfunc main() {}\n"},
			target:  "m",
			entries: map[string]func(*testing.T, string){"go.work": sandbox.Fifo},
			failure: "go.work",
		},
		{
			name:    "a_module_whose_only_go_file_is_a_pipe",
			tree:    map[string]string{"go.mod": "module example.com/m\n\ngo 1.27.1\n"},
			entries: map[string]func(*testing.T, string){"x.go": sandbox.Fifo},
			units:   []string{"."}, hidden: []string{"x.go"},
		},
		{
			name:    "a_module_whose_only_go_file_is_a_pipe_in_a_package",
			tree:    map[string]string{"go.mod": "module example.com/m\n\ngo 1.27.1\n"},
			entries: map[string]func(*testing.T, string){"p/x.go": sandbox.Fifo},
			units:   []string{"."}, hidden: []string{"p/x.go"},
		},
		{
			name: "a_pipe_named_a_go_file_in_an_ignored_directory", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"data/x.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"},
		},
		{
			name: "a_pipe_named_a_go_file_in_a_testdata_directory", tree: moduleTree,
			entries: map[string]func(*testing.T, string){"p/testdata/x.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/m", "example.com/sub"},
		},
		{
			name: "a_workspace_below_go_1_22_whose_vendor_list_is_a_pipe",
			tree: map[string]string{
				"go.work":   "go 1.21\n\nuse ./a\n",
				"a/go.mod":  "module example.com/a\n\ngo 1.21\n",
				"a/main.go": "package main\n\nfunc main() {}\n",
			},
			entries: map[string]func(*testing.T, string){"vendor/modules.txt": sandbox.Fifo},
			failure: "vendor/modules.txt",
		},
		{
			name: "a_target_in_a_workspace_at_go_1_18_whose_vendor_list_is_a_pipe",
			tree: map[string]string{
				"go.work":   "go 1.18\n\nuse ./a\n",
				"a/go.mod":  "module example.com/a\n\ngo 1.18\n",
				"a/main.go": "package main\n\nfunc main() {}\n",
			},
			target:  "a",
			entries: map[string]func(*testing.T, string){"vendor/modules.txt": sandbox.Fifo},
			failure: "vendor/modules.txt",
		},
		{
			name: "a_module_vendor_tree_under_a_workspace_reading_a_replaced_package_holding_a_pipe", tree: vendoredTree,
			prepare: func(t *testing.T, root string) {
				vendorModule(t, root)
				writeBody("go 1.22\n\nuse .\n")(t, filepath.Join(root, "go.work"))
			},
			entries: map[string]func(*testing.T, string){"b/q/x.go": sandbox.Fifo},
			units:   []string{"."}, hidden: []string{"b/q/x.go"},
		},
		{
			name: "a_workspace_vendor_list_read_with_no_workspace_and_a_replaced_package_holding_a_pipe", tree: vendoredTree,
			prepare: func(t *testing.T, root string) {
				vendorModule(t, root)
				list := filepath.Join(root, "vendor", "modules.txt")
				text, err := os.ReadFile(list)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				writeBody("## workspace\n"+string(text))(t, list)
			},
			entries: map[string]func(*testing.T, string){"b/q/x.go": sandbox.Fifo},
			units:   []string{"."}, hidden: []string{"b/q/x.go"},
		},
		{
			name: "a_vendored_module_under_a_go_env_file_setting_mod_mode", tree: vendoredTree,
			prepare: func(t *testing.T, root string) {
				vendorModule(t, root)
				envFile(t, "GOFLAGS=-mod=mod\n")
			},
			entries: map[string]func(*testing.T, string){"b/q/x.go": sandbox.Fifo},
			units:   []string{"."},
		},
		{
			name: "a_vendored_module_under_the_null_go_env_file", tree: vendoredTree,
			prepare: func(t *testing.T, root string) {
				vendorModule(t, root)
				t.Setenv("GOENV", os.DevNull)
			},
			entries: map[string]func(*testing.T, string){"b/q/x.go": sandbox.Fifo},
			units:   []string{"."},
		},
		{
			name: "a_nested_module_the_target_requires_from_the_cache_holding_a_pipe", tree: cachedNestedTree,
			prepare: func(t *testing.T, root string) {
				tidyFromCache(t, root, cacheEntry{path: "example.com/m/sub", version: "v0.1.0", files: map[string]string{
					"go.mod": "module example.com/m/sub\n\ngo 1.22\n", "q/q.go": "package q\n\nfunc Q() {}\n",
				}})
			},
			entries: map[string]func(*testing.T, string){"sub/q/x.go": sandbox.Fifo},
			units:   []string{"."},
		},
		{
			name: "a_nested_module_the_target_imports_unrequired_holding_a_pipe", tree: cachedNestedTree,
			entries: map[string]func(*testing.T, string){
				"go.mod":     writeBody("module example.com/m\n\ngo 1.22\n"),
				"sub/q/x.go": sandbox.Fifo,
			},
			refusal: vocab.SetupMissingModule,
		},
		{
			name: "a_versioned_replace_the_selected_version_misses_holding_a_pipe", tree: versionedReplaceTree,
			prepare: func(t *testing.T, root string) {
				tidyFromCache(t, root, cachedX)
			},
			entries: map[string]func(*testing.T, string){"old/q/x.go": sandbox.Fifo},
			units:   []string{"."},
		},
		{
			name: "a_versioned_replace_the_selected_version_misses_whose_module_file_is_a_pipe", tree: versionedReplaceTree,
			prepare: func(t *testing.T, root string) {
				tidyFromCache(t, root, cachedX)
			},
			entries: map[string]func(*testing.T, string){"old/go.mod": replaced(sandbox.Fifo)},
			units:   []string{"."},
		},
		{
			name: "a_module_sum_file_that_is_a_pipe_where_a_cached_module_needs_it", tree: versionedReplaceTree,
			prepare: func(t *testing.T, root string) {
				tidyFromCache(t, root, cachedX)
			},
			entries: map[string]func(*testing.T, string){"go.sum": replaced(sandbox.Fifo)},
			failure: "go.sum",
		},
		{
			name: "a_target_importing_through_a_link_to_an_ancestor_a_package_holding_a_pipe", tree: ancestorLinkTree,
			entries: map[string]func(*testing.T, string){"p/q/up": linkTo(".."), "p/r/x.go": sandbox.Fifo},
			units:   []string{"."}, hidden: []string{"p/q/up/r/x.go", "p/r/x.go"},
		},
		{
			name: "a_module_importing_through_a_link_to_the_root_a_package_holding_a_pipe", tree: rootLinkTree,
			entries: map[string]func(*testing.T, string){"a/p/up": linkTo("../.."), "ext/r/x.go": sandbox.Fifo},
			units:   []string{"a"}, hidden: []string{"a/p/up/ext/r/x.go"},
		},
		{
			name: "a_target_importing_through_a_link_above_every_root_a_package_holding_no_pipe", tree: rootLinkTree, target: "a",
			entries: map[string]func(*testing.T, string){"a/p/up": linkTo("../..")},
			units:   []string{"a"},
		},
		{
			name: "a_target_holding_a_link_above_every_root_nothing_imports_through", tree: rootLinkTree, target: "a",
			entries: map[string]func(*testing.T, string){
				"a/main.go":  writeBody("package main\n\nfunc main() {}\n"),
				"a/p/up":     linkTo("../.."),
				"ext/r/x.go": sandbox.Fifo,
			},
			units: []string{"a"},
		},
		{
			name: "a_target_importing_directly_a_package_a_link_to_an_ancestor_reaches_holding_a_pipe", tree: ancestorLinkTree,
			entries: map[string]func(*testing.T, string){
				"main.go":  writeBody("package main\n\nimport \"example.com/a/p/r\"\n\nfunc main() { r.R() }\n"),
				"p/q/up":   linkTo(".."),
				"p/r/x.go": sandbox.Fifo,
			},
			units: []string{"."}, hidden: []string{"p/r/x.go"},
		},
		{
			name: "a_target_whose_unrequired_replaced_module_file_is_a_pipe", tree: siblingTree, target: "a",
			entries: map[string]func(*testing.T, string){
				"a/go.mod": writeBody("module example.com/a\n\ngo 1.22\n\nreplace example.com/z => ../z\n"),
				"z/go.mod": sandbox.Fifo,
			},
			units: []string{"a"},
		},
		{
			name: "a_target_whose_required_unimported_replaced_module_file_is_a_pipe", tree: siblingTree, target: "a",
			entries: map[string]func(*testing.T, string){
				"a/go.mod": writeBody("module example.com/a\n\ngo 1.22\n\nrequire example.com/z v0.0.0\n\nreplace example.com/z => ../z\n"),
				"z/go.mod": sandbox.Fifo,
			},
			units: []string{"a"},
		},
		{
			name: "a_target_importing_a_nested_replaced_module_whose_module_file_is_a_pipe",
			tree: map[string]string{
				"go.mod":     "module example.com/m\n\ngo 1.22\n\nrequire example.com/m/sub v0.0.0\n\nreplace example.com/m/sub => ./sub\n",
				"main.go":    "package main\n\nimport \"example.com/m/sub/s\"\n\nfunc main() { s.S() }\n",
				"sub/go.mod": "module example.com/m/sub\n\ngo 1.22\n",
				"sub/s/s.go": "package s\n\n// S is what main calls.\nfunc S() {}\n",
			},
			entries: map[string]func(*testing.T, string){"sub/go.mod": replaced(sandbox.Fifo)},
			failure: "sub/go.mod",
		},
	}
	tests = append(tests, linkRows()...)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deadline.Row(t, time.Minute, "the toolchain or a walk opened a file that blocks", tc.meets)
		})
	}
}

// linkRows are the rows of symbolic links to directories, on trees with no special
// file each load as with no run ([program.6]): a link graph whose paths multiply, a
// link into /proc, to the file system's root, to a parent or out of every tree the
// scan searches, imported through or not, a missing package below a link, and a
// file importing "C" through a link; and a package in a directory a dot starts.
func linkRows() []toolchainRow {
	dag := func(at string, depth int) map[string]func(*testing.T, string) {
		entries := make(map[string]func(*testing.T, string))
		for i := range depth {
			for j := range 4 {
				entries[fmt.Sprintf("%s/L%d/s%d", at, i, j)] = linkTo(fmt.Sprintf("../L%d", i+1))
			}
		}
		return entries
	}
	with := func(entries, more map[string]func(*testing.T, string)) map[string]func(*testing.T, string) {
		maps.Copy(entries, more)
		return entries
	}
	importing := func(path string) map[string]string {
		return map[string]string{
			"m/go.mod":  "module example.com/m\n\ngo 1.22\n",
			"m/main.go": "package main\n\nimport _ \"example.com/m/" + path + "\"\n\nfunc main() {}\n",
			"m/p/p.go":  "package p\n",
		}
	}
	outTree := map[string]string{
		"m/go.mod":   "module example.com/m\n\ngo 1.22\n",
		"m/main.go":  "package main\n\nimport _ \"example.com/m/shared/q\"\n\nfunc main() {}\n",
		"ext/q/q.go": "package q\n",
		"ext/r/r.go": "package r\n",
	}
	// Below m, main imports a/d through a/b/up, a link to a.
	cycle := func(imported string) map[string]string {
		return map[string]string{
			"m/go.mod":   "module example.com/m\n\ngo 1.22\n",
			"m/main.go":  "package main\n\nimport _ \"example.com/m/" + imported + "\"\n\nfunc main() {}\n",
			"m/a/d/d.go": "package d\n\nfunc F() {}\n",
		}
	}
	missing := func(link string) map[string]string {
		return map[string]string{
			"m/go.mod":  "module example.com/m\n\ngo 1.22\n",
			"m/main.go": "package main\n\nimport _ \"example.com/m/" + link + "/zz\"\n\nfunc main() {}\n",
		}
	}
	return []toolchainRow{
		{
			name: "a_target_holding_a_link_to_a_graph_of_links_whose_paths_multiply_outside_it", tree: importing("p"), target: "m",
			entries: with(dag("out", 9), map[string]func(*testing.T, string){"m/p/l": linkTo("../../out/L0"), "out/L9/z.go": writeBody("package z\n")}),
			units:   []string{"m"},
		},
		{
			name: "a_target_holding_a_graph_of_links_whose_paths_multiply", tree: importing("p"), target: "m",
			entries: with(dag("m/dag", 9), map[string]func(*testing.T, string){"m/dag/L9/z.go": writeBody("package z\n")}),
			units:   []string{"m"},
		},
		{
			name: "a_target_holding_a_graph_of_links_whose_paths_multiply_to_a_pipe", tree: importing("p"), target: "m",
			entries: with(dag("m/dag", 9), map[string]func(*testing.T, string){"m/dag/L9/z.go": writeBody("package z\n"), "m/dag/L9/x.go": sandbox.Fifo}),
			units:   []string{"m"}, hidden: []string{"m/dag/L9/x.go"},
		},
		{
			name: "a_target_importing_through_a_graph_of_links_out_of_every_tree", tree: importing("p/l/s0/s1/s2/s3"), target: "m",
			entries: with(dag("out", 4), map[string]func(*testing.T, string){"m/p/l": linkTo("../../out/L0"), "out/L4/z.go": writeBody("package z\n")}),
			units:   []string{"m"},
		},
		{
			name: "a_target_holding_a_link_into_proc_nothing_imports_through", tree: importing("p"), target: "m",
			entries: map[string]func(*testing.T, string){"m/p/sys": linkTo("/proc")},
			units:   []string{"m"},
		},
		{
			name: "a_target_holding_a_link_to_the_file_system_root_nothing_imports_through", tree: importing("p"), target: "m",
			entries: map[string]func(*testing.T, string){"m/up": linkTo("/")},
			units:   []string{"m"},
		},
		{
			name: "a_target_importing_through_a_link_to_its_parent_a_package_holding_no_pipe",
			tree: map[string]string{
				"repo/go/go.mod":   "module example.com/a\n\ngo 1.22\n",
				"repo/go/main.go":  "package main\n\nimport \"example.com/a/up/shared\"\n\nfunc main() { shared.S() }\n",
				"repo/shared/s.go": "package shared\n\n// S is what main calls.\nfunc S() {}\n",
			},
			target: "repo/go", entries: map[string]func(*testing.T, string){"repo/go/up": linkTo("..")},
			units: []string{"repo/go"},
		},
		{
			name: "a_target_importing_through_a_link_out_of_every_tree_a_package_beside_one_holding_a_pipe", tree: outTree, target: "m",
			entries: map[string]func(*testing.T, string){"m/shared": linkTo("../ext"), "ext/r/x.go": sandbox.Fifo},
			units:   []string{"m"},
		},
		{
			name: "a_target_importing_through_a_link_out_of_every_tree", tree: outTree, target: "m",
			entries: map[string]func(*testing.T, string){"m/shared": linkTo("../ext")},
			units:   []string{"m"},
		},
		{
			name: "a_target_importing_through_a_link_to_an_ancestor_beside_a_link_to_the_file_system_root", tree: cycle("a/b/up/d"), target: "m",
			entries: map[string]func(*testing.T, string){"m/a/b/up": linkTo(".."), "m/a/c/ext": linkTo("/")},
			units:   []string{"m"},
		},
		{
			name: "a_target_importing_through_a_link_to_an_ancestor_beside_a_link_to_a_pipe_nothing_imports_through", tree: cycle("a/b/up/d"), target: "m",
			entries: map[string]func(*testing.T, string){
				"m/a/b/up": linkTo(".."), "m/a/c/ext": linkTo("../../../ext"), "ext/z/z.go": writeBody("package z\n"), "ext/z/x.go": sandbox.Fifo,
			},
			units: []string{"m"},
		},
		{
			name: "a_target_importing_a_missing_package_through_a_link_to_an_ancestor_holding_a_pipe_in_testdata", tree: cycle("a/b/up/nope"), target: "m",
			entries: map[string]func(*testing.T, string){"m/a/b/up": linkTo(".."), "m/a/testdata/x.go": sandbox.Fifo},
			refusal: vocab.SetupMissingModule,
		},
		{
			name: "a_target_importing_a_missing_package_through_a_link_into_proc", tree: missing("l"), target: "m",
			entries: map[string]func(*testing.T, string){"m/l": linkTo("/proc")},
			refusal: vocab.SetupMissingModule,
		},
		{
			name: "a_target_importing_a_missing_package_through_a_link_out_of_every_tree", tree: missing("l"), target: "m",
			entries: map[string]func(*testing.T, string){"m/l": linkTo("/usr/share")},
			refusal: vocab.SetupMissingModule,
		},
		{
			name: "a_cgo_file_importing_a_package_through_a_link_out_of_every_tree",
			tree: map[string]string{
				"m/go.mod":  "module example.com/m\n\ngo 1.22\n",
				"m/main.go": "package main\n\nimport \"example.com/m/p\"\n\nfunc main() { p.Run() }\n",
				"m/p/a.go":  "package p\n\nfunc Run() {}\n\nfunc helper() {}\n",
				"m/p/c.go": "package p\n\n// #include <stdlib.h>\nimport \"C\"\n\nimport (\n\t\"example.com/m/l/q\"\n\t\"example.com/m/r\"\n)\n\n" +
					"func init() { helper(); q.F(); r.G() }\n",
				"m/r/r.go":   "package r\n\nfunc G() {}\n\nfunc H() {}\n",
				"out/q/q.go": "package q\n\nfunc F() {}\n",
			},
			target: "m", entries: map[string]func(*testing.T, string){"m/l": linkTo("../out")},
			units: []string{"m"},
		},
		{
			name: "a_target_importing_a_package_in_a_directory_a_dot_starts",
			tree: map[string]string{
				"go.mod":    "module example.com/a\n\ngo 1.22\n",
				"main.go":   "package main\n\nimport \"example.com/a/.hid\"\n\nfunc main() { hid.H() }\n",
				".hid/p.go": "package hid\n\n// H is what main calls.\nfunc H() {}\n",
			},
			units: []string{"."}, nested: []string{"example.com/a"},
		},
		{
			name: "a_target_importing_a_package_in_a_directory_a_dot_starts_holding_a_pipe",
			tree: map[string]string{
				"go.mod":    "module example.com/a\n\ngo 1.22\n",
				"main.go":   "package main\n\nimport \"example.com/a/.hid\"\n\nfunc main() { hid.H() }\n",
				".hid/p.go": "package hid\n\n// H is what main calls.\nfunc H() {}\n",
			},
			entries: map[string]func(*testing.T, string){".hid/x.go": sandbox.Fifo},
			units:   []string{"."}, nested: []string{"example.com/a"}, hidden: []string{".hid/x.go"},
		},
		{
			name: "a_target_importing_a_package_named_objects_beside_a_head_file_and_refs_holding_a_pipe", tree: objectsTree,
			entries: map[string]func(*testing.T, string){"store/objects/x.go": sandbox.Fifo},
			units:   []string{"."}, hidden: []string{"store/objects/x.go"},
		},
		{
			name: "a_target_holding_a_pipe_in_its_git_object_store", tree: objectsTree,
			entries: map[string]func(*testing.T, string){".git/HEAD": writeBody("ref: refs/heads/main\n"), ".git/refs/heads/main": writeBody(""), ".git/objects/x.go": sandbox.Fifo},
			units:   []string{"."},
		},
	}
}

// objectsTree is a module whose main package imports store/objects, a package
// beside the HEAD file and the refs directory git writes beside its object store.
var objectsTree = map[string]string{
	"go.mod":                "module example.com/a\n\ngo 1.22\n",
	"main.go":               "package main\n\nimport \"example.com/a/store/objects\"\n\nfunc main() { objects.Used() }\n",
	"store/HEAD":            "ref: refs/heads/main\n",
	"store/refs/heads/main": "",
	"store/objects/o.go":    "package objects\n\n// Used is what main calls.\nfunc Used() {}\n",
}

// namedByPrograms is every package-level declaration of a package r loaded that
// one of r's programs names, as path.Name, sorted.
func namedByPrograms(r *Result) []string {
	loaded := make(map[*types.Package]bool)
	for _, p := range r.Packages {
		loaded[p.Types] = true
	}
	named := []string{}
	for _, program := range r.Programs {
		for _, obj := range program.Info.Uses {
			if pkg := obj.Pkg(); loaded[pkg] && pkg.Scope().Lookup(obj.Name()) == obj {
				if name := pkg.Path() + "." + obj.Name(); !slices.Contains(named, name) {
					named = append(named, name)
				}
			}
		}
	}
	slices.Sort(named)
	return named
}

// replaced removes the regular file a tree wrote at path before make makes the
// entry there.
func replaced(make func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		make(t, path)
	}
}

// writeBody makes path a regular file holding body.
func writeBody(body string) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := sandbox.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
}

// relativeTo is dir relative to root, in forward slashes.
func relativeTo(t *testing.T, root, dir string) string {
	t.Helper()
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return filepath.ToSlash(rel)
}

// nonNilStrings is s, or an empty slice where s is nil.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Trees of the rows whose module mode or module cache decides what the toolchain
// reads: a module that replaces example.com/b with its directory b and imports
// b/q, a module that requires its nested module example.com/m/sub from the module
// cache, and a module that requires example.com/x v1.2.0 from the cache and
// replaces only v1.0.0 with its directory old; and a module whose main package
// imports p/r through p/q/up, a symbolic link to p a row makes, and a module a
// whose main package imports ext/r beside it through a/p/up, a symbolic link to
// the tree's root a row makes.
var (
	vendoredTree = map[string]string{
		"go.mod":   "module example.com/m\n\ngo 1.22\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ./b\n",
		"main.go":  "package main\n\nimport \"example.com/b/q\"\n\nfunc main() { q.Q() }\n",
		"b/go.mod": "module example.com/b\n\ngo 1.22\n",
		"b/q/q.go": "package q\n\n// Q is what main calls.\nfunc Q() {}\n",
	}
	cachedNestedTree = map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.22\n\nrequire example.com/m/sub v0.1.0\n",
		"main.go":    "package main\n\nimport \"example.com/m/sub/q\"\n\nfunc main() { q.Q() }\n",
		"sub/go.mod": "module example.com/m/sub\n\ngo 1.22\n",
		"sub/q/q.go": "package q\n\n// Q is what main calls.\nfunc Q() {}\n",
	}
	versionedReplaceTree = map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.22\n\nrequire example.com/x v1.2.0\n\nreplace example.com/x v1.0.0 => ./old\n",
		"main.go":    "package main\n\nimport \"example.com/x/q\"\n\nfunc main() { q.Q() }\n",
		"old/go.mod": "module example.com/x\n\ngo 1.22\n",
		"old/q/q.go": "package q\n\n// Q is what main calls.\nfunc Q() {}\n",
	}
	ancestorLinkTree = map[string]string{
		"go.mod":   "module example.com/a\n\ngo 1.22\n",
		"main.go":  "package main\n\nimport \"example.com/a/p/q/up/r\"\n\nfunc main() { r.R() }\n",
		"p/q/q.go": "package q\n",
		"p/r/r.go": "package r\n\n// R is what main calls.\nfunc R() {}\n",
	}
	rootLinkTree = map[string]string{
		"a/go.mod":   "module example.com/a\n\ngo 1.22\n",
		"a/main.go":  "package main\n\nimport \"example.com/a/p/up/ext/r\"\n\nfunc main() { r.R() }\n",
		"a/p/p.go":   "package p\n",
		"ext/r/r.go": "package r\n\n// R is what main calls.\nfunc R() {}\n",
	}
	cachedX = cacheEntry{path: "example.com/x", version: "v1.2.0", files: map[string]string{
		"go.mod": "module example.com/x\n\ngo 1.22\n", "q/q.go": "package q\n\n// Q is what main calls.\nfunc Q() {}\n",
	}}
)

// cacheEntry is one module version a row's module cache holds.
type cacheEntry struct {
	files         map[string]string
	path, version string
}

// vendorModule writes the vendor directory of the module at root.
func vendorModule(t *testing.T, root string) {
	t.Helper()
	runGo(t, root, nil, "mod", "vendor")
}

// envFile makes a go env file holding body the one every toolchain reads.
func envFile(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(sandbox.Dir(t), "env")
	writeBody(body)(t, path)
	t.Setenv("GOENV", path)
}

// tidyFromCache gives every load a module cache of its own holding modules, and
// writes the module sum of the module at root against it.
func tidyFromCache(t *testing.T, root string, modules ...cacheEntry) {
	t.Helper()
	proxy := sandbox.Dir(t)
	for _, m := range modules {
		escaped, err := module.EscapePath(m.path)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		at := filepath.Join(proxy, filepath.FromSlash(escaped), "@v")
		writeBody(m.version+"\n")(t, filepath.Join(mkdir(t, at), "list"))
		writeBody(`{"Version":"`+m.version+`"}`)(t, filepath.Join(at, m.version+".info"))
		writeBody(m.files["go.mod"])(t, filepath.Join(at, m.version+".mod"))
		var archive bytes.Buffer
		w := zip.NewWriter(&archive)
		for _, name := range slices.Sorted(maps.Keys(m.files)) {
			f, err := w.Create(m.path + "@" + m.version + "/" + name)
			if err == nil {
				_, err = f.Write([]byte(m.files[name]))
			}
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		writeBody(archive.String())(t, filepath.Join(at, m.version+".zip"))
	}
	t.Setenv("GOMODCACHE", sandbox.Dir(t))
	runGo(t, root, []string{"GOPROXY=file://" + filepath.ToSlash(proxy), "GOSUMDB=off"}, "mod", "tidy", "-e")
}

// runGo runs the go command with args in dir under env, in module mode with a
// writable module cache, failing the test where it fails.
func runGo(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := deadline.Command(deadline.Context(t), "go", args...)
	cmd.Dir = dir
	cmd.Env = slices.Concat(os.Environ(), []string{"GOWORK=off", "GOFLAGS=-mod=mod -modcacherw"}, env)
	out, err := cmd.CombinedOutput()
	deadline.Reap(cmd)
	if err != nil {
		t.Fatalf("Setup: go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// mkdir makes the directory dir and returns it.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := sandbox.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return dir
}

// TestLoadReadsNoGoflagsFromTheGoEnvFile pins that the GOFLAGS a go env file sets
// reaches no load ([program.5]): the toolchain reads an empty GOFLAGS from that
// file, so a tag it names would select a file no configuration selects.
func TestLoadReadsNoGoflagsFromTheGoEnvFile(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"go.mod":  "module example.com/m\n\ngo 1.27.1\n",
		"main.go": "package main\n\nfunc main() {}\n",
		"foo.go":  "//go:build foo\n\npackage main\n",
	})
	envFile(t, "GOFLAGS=-tags=foo\n")
	got, err := loadScope(t.Context(), scope.Document{Target: scope.Module{Path: root}}, HostConfiguration())
	if err != nil {
		t.Fatalf("Load(a module under a go env file naming a tag) = _, %v, want no error", err)
	}
	for _, p := range got.Packages {
		if slices.ContainsFunc(p.GoFiles, func(file string) bool { return filepath.Base(file) == "foo.go" }) {
			t.Errorf("Load(a module under a go env file naming a tag) built foo.go into %s, want the file's tag left unset", p.ID)
		}
	}
}

// TestLoadRunsWithoutAGoEnvFileThatIsAPipe pins, within a minute a row, that a go
// env file that is a pipe, named by GOENV or at the user's default place, is one
// no toolchain reads: Discover records it and every load runs with GOENV=off
// ([program.6]). A relative GOENV names a file in the directory each load runs
// from, as the go command resolves it, whatever directory the run starts in.
func TestLoadRunsWithoutAGoEnvFileThatIsAPipe(t *testing.T) {
	stableToolchain(t)
	tests := []struct {
		name  string
		place func(t *testing.T, root string) string // makes the go env file's place the toolchain run in root reads, returning it
		named bool
	}{
		{name: "named_by_goenv", named: true, place: func(t *testing.T, _ string) string {
			path := filepath.Join(sandbox.Dir(t), "env")
			t.Setenv("GOENV", path)
			return path
		}},
		{name: "named_by_a_relative_goenv_in_the_load_directory", named: true, place: func(t *testing.T, root string) string {
			t.Setenv("GOENV", "envpipe")
			return filepath.Join(root, "envpipe")
		}},
		{name: "at_the_default_place", place: func(t *testing.T, _ string) string {
			home := sandbox.Dir(t)
			t.Setenv("GOENV", "")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			dir, err := os.UserConfigDir()
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			return filepath.Join(mkdir(t, filepath.Join(dir, "go")), "env")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deadline.Row(t, time.Minute, "the toolchain read a go env file that blocks", func(t *testing.T) {
				root := writeTree(t, moduleTree)
				path := tc.place(t, root)
				sandbox.Fifo(t, path)
				top, err := Discover(deadline.Context(t), scope.Document{Target: scope.Module{Path: root}})
				if err != nil {
					t.Fatalf("Discover(a module under a go env file that is a pipe) = _, %v, want no error", err)
				}
				if want := []EnvFile{{Path: path, Named: tc.named}}; !slices.Equal(top.EnvFiles, want) {
					t.Errorf("Discover(a module under a go env file that is a pipe) recorded %+v, want %+v", top.EnvFiles, want)
				}
				c := HostConfiguration()
				if _, err := Load(deadline.Context(t), top, &c); err != nil {
					t.Errorf("Load(a module under a go env file that is a pipe) = _, %v, want no error", err)
				}
			})
		})
	}
}
