//go:build unix

package load

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// entryTree is the module every row of the entry-kind table adds its entry to: an
// ignored directory, an underscore directory and a package beside the program.
var entryTree = map[string]string{
	"go.mod":         "module example.com/m\n\ngo 1.27.1\n\nignore ./data\n",
	"main.go":        "package main\n\nimport \"example.com/m/p\"\n\nfunc main() { p.Used() }\n",
	"p/p.go":         "package p\n\nfunc Used() {}\n",
	"data/README.md": "data\n",
	"_x/README.md":   "examples\n",
}

// asNonRoot skips a row whose entry the run can read only because it runs as root.
func asNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("Setup: a mode of 0 leaves an entry readable to root")
	}
}

// mode0 makes the entry at path, made by make, unreadable to its owner until the
// test ends.
func mode0(t *testing.T, path string, make func(string) error) {
	t.Helper()
	asNonRoot(t)
	if err := make(path); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := sandbox.Chmod(path, 0); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = sandbox.Chmod(path, 0o700) })
}

// linkTo makes path a symbolic link to target.
func linkTo(target string) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := sandbox.Symlink(target, path); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
}

// entryRow is one row of the entry-kind table: an entry, the tree it is made in,
// and how Discover sorts it.
type entryRow struct {
	name, entry string
	tree        map[string]string
	make        func(t *testing.T, path string)
	want        Kind   // 0 where the entry is recorded nowhere
	failure     string // what Discover's error holds, empty for none
	class       string // the class of that error, no-program where empty
	hidden      bool   // whether every toolchain reads the entry as absent
}

// TestDiscoverSortsEveryEntryKindBeforeOpeningIt pins the one classification
// every walk shares: Discover returns within a minute, records an unreadable entry
// as unread and one no walk opens as left out, hides every pipe, device and
// socket from the toolchain but those in a .git/objects directory, names an unlistable
// directory in no-program where no module is found, and refuses unopened, as
// unreadable-configuration, a module or workspace file that is no regular file,
// every failure naming each path relative to the root.
func TestDiscoverSortsEveryEntryKindBeforeOpeningIt(t *testing.T) {
	t.Parallel()
	tests := []entryRow{
		{name: "a_regular_go_file", entry: "data/x.go", make: func(t *testing.T, path string) {
			t.Helper()
			if err := sandbox.WriteFile(path, []byte("package data\n"), 0o600); err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}},
		{name: "a_directory", entry: "data/sub", make: func(t *testing.T, path string) {
			t.Helper()
			if err := sandbox.Mkdir(path, 0o700); err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}},
		{name: "a_pipe_named_a_go_file_in_an_ignored_directory", entry: "data/x.go", make: sandbox.Fifo, want: leftOut, hidden: true},
		{name: "a_pipe_named_a_go_file_in_an_underscore_directory", entry: "_x/y.go", make: sandbox.Fifo, want: leftOut, hidden: true},
		{name: "a_pipe_named_a_go_file_in_a_package", entry: "p/x.go", make: sandbox.Fifo, want: leftOut, hidden: true},
		{name: "a_pipe_named_a_test_file_in_a_package", entry: "p/foo_test.go", make: sandbox.Fifo, want: leftOut, hidden: true},
		{name: "a_pipe_in_a_directory_whose_name_starts_with_a_dot", entry: ".cache/x.go", make: inNewDir(sandbox.Fifo), hidden: true},
		{name: "a_pipe_in_the_object_store_of_a_repository", entry: ".git/objects/x.go", make: inRepository(sandbox.Fifo)},
		{name: "a_pipe_below_the_object_store_of_a_repository", entry: ".git/objects/ab/x.go", make: inRepository(sandbox.Fifo)},
		{name: "a_pipe_in_the_object_store_of_a_repository_in_a_package", entry: "p/.git/objects/x.go", make: inRepository(sandbox.Fifo)},
		{name: "a_pipe_in_a_directory_named_objects_in_a_dot_git_directory_without_head_or_refs", entry: ".git/objects/x.go", make: inNewDir(sandbox.Fifo)},
		{name: "a_pipe_in_the_object_store_of_a_submodule", entry: ".git/modules/sub/objects/x.go", make: inRepository(sandbox.Fifo), hidden: true},
		{name: "a_pipe_in_the_object_store_of_a_bare_repository", entry: "store.git/objects/x.go", make: inRepository(sandbox.Fifo), want: leftOut, hidden: true},
		{name: "a_pipe_in_a_package_named_objects_beside_a_head_file_and_refs", entry: "store/objects/x.go", make: inRepository(sandbox.Fifo), want: leftOut, hidden: true},
		{name: "a_pipe_in_a_directory_named_objects_beside_no_repository", entry: ".data/objects/x.go", make: inNewDir(sandbox.Fifo), hidden: true},
		{name: "a_link_to_a_device_in_a_package", entry: "p/z.go", make: linkTo("/dev/zero"), want: leftOut, hidden: true},
		{name: "a_socket_named_a_go_file", entry: "data/s.go", make: socket, want: leftOut, hidden: true},
		{name: "a_link_to_a_device", entry: "data/z.go", make: linkTo("/dev/zero"), want: leftOut, hidden: true},
		{name: "a_link_that_loops", entry: "data/loop.go", make: linkTo("loop.go"), want: leftOut},
		{name: "a_link_through_a_file", entry: "p/through.go", make: linkTo("p.go/x.go"), want: leftOut},
		{name: "a_link_to_nothing", entry: "p/broken.go", make: linkTo("missing.go"), want: leftOut},
		{name: "a_link_to_a_directory", entry: "p/linked.go", make: linkTo("../data"), want: leftOut},
		{name: "a_directory_the_user_cannot_list", entry: "data/pg", want: Unread, make: func(t *testing.T, path string) {
			t.Helper()
			mode0(t, path, func(path string) error { return sandbox.Mkdir(path, 0o700) })
		}},
		{name: "a_go_file_the_user_cannot_open", entry: "data/secret.go", want: Unread, make: func(t *testing.T, path string) {
			t.Helper()
			mode0(t, path, func(path string) error { return sandbox.WriteFile(path, []byte("package data\n"), 0o600) })
		}},
		{
			name: "the_directory_of_the_only_go_file", entry: "src", want: Unread,
			tree: map[string]string{"go.mod": "module example.com/m\n\ngo 1.27.1\n", "src/main.go": "package main\n\nfunc main() {}\n"},
			make: func(t *testing.T, path string) {
				t.Helper()
				mode0(t, path, func(string) error { return nil })
			},
		},
		{name: "a_link_to_a_name_too_long_to_stat", entry: "data/long", make: linkTo(strings.Repeat("n", 300)), want: Unread},
		{name: "a_go_file_link_to_a_name_too_long_to_stat", entry: "p/long.go", make: linkTo(strings.Repeat("n", 300)), want: Unread},
		{
			name: "a_module_holding_no_go_file_beside_a_directory_the_user_cannot_list", entry: "b", want: Unread,
			tree: map[string]string{
				"a/go.mod": "module example.com/a\n\ngo 1.27.1\n",
				"b/go.mod": "module example.com/b\n\ngo 1.27.1\n", "b/x.go": "package b\n",
			},
			make: func(t *testing.T, path string) {
				t.Helper()
				mode0(t, path, func(string) error { return nil })
			},
		},
		{
			name: "a_module_holding_no_go_file_beside_a_link_too_long_to_stat", entry: "long", want: Unread,
			tree: map[string]string{"a/go.mod": "module example.com/a\n\ngo 1.27.1\n"},
			make: linkTo(strings.Repeat("n", 300)),
		},
		{
			name: "a_module_file_that_is_a_pipe", entry: "go.mod", make: sandbox.Fifo,
			tree:    map[string]string{"main.go": "package main\n\nfunc main() {}\n"},
			failure: "go.mod: no regular file", class: vocab.SetupUnreadableConfiguration,
		},
		{
			name: "a_workspace_file_that_is_a_pipe", entry: "go.work", make: sandbox.Fifo,
			tree:    map[string]string{"go.mod": "module example.com/m\n\ngo 1.27.1\n", "main.go": "package main\n\nfunc main() {}\n"},
			failure: "go.work: no regular file", class: vocab.SetupUnreadableConfiguration,
		},
		{
			name: "the_directory_of_the_only_module", entry: "svc",
			failure: "the run cannot read svc;",
			tree:    map[string]string{"svc/go.mod": "module example.com/svc\n\ngo 1.27.1\n", "svc/main.go": "package main\n\nfunc main() {}\n"},
			make: func(t *testing.T, path string) {
				t.Helper()
				mode0(t, path, func(string) error { return nil })
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deadline.Row(t, time.Minute, "a walk opened an entry before deciding its kind", tc.discovers)
		})
	}
}

// discovers makes the row's entry in its tree and checks how Discover sorts it.
func (tc entryRow) discovers(t *testing.T) {
	tree := tc.tree
	if tree == nil {
		tree = entryTree
	}
	root := writeTree(t, tree)
	path := filepath.Join(root, filepath.FromSlash(tc.entry))
	tc.make(t, path)

	top, err := Discover(deadline.Context(t), scope.Document{Target: scope.Module{Path: root}})
	switch {
	case tc.failure != "":
		class := cmp.Or(tc.class, vocab.SetupNoProgram)
		if got := failure.Of(err); err == nil || got[0].Class != class || !strings.Contains(got[0].Detail, tc.failure) || strings.Contains(got[0].Detail, root) {
			t.Errorf("Discover(a tree holding %s) = %v, want the %s failure holding %q and naming every path relative to the root", tc.name, err, class, tc.failure)
		}
		return
	case err != nil:
		t.Fatalf("Discover(a tree holding %s) = %v, want no error", tc.name, err)
	}
	if got, want := slices.Contains(top.Unread, path), tc.want == Unread; got != want {
		t.Errorf("Discover(a tree holding %s) recorded the unread entries %q, want %s among them: %t", tc.name, top.Unread, path, want)
	}
	if got, want := slices.Contains(top.LeftOut, path), tc.want == leftOut; got != want {
		t.Errorf("Discover(a tree holding %s) recorded the left-out entries %q, want %s among them: %t", tc.name, top.LeftOut, path, want)
	}
	if got := slices.ContainsFunc(top.hidden, func(h hiddenEntry) bool { return h.path == path }); got != tc.hidden {
		t.Errorf("Discover(a tree holding %s) hid %v, want %s among them: %t", tc.name, top.hidden, path, tc.hidden)
	}
}

// inNewDir makes the directories of path before make makes the entry there.
func inNewDir(make func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := sandbox.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		make(t, path)
	}
}

// inRepository makes the directories of path, one of them named objects, and the
// HEAD file and the refs directory git writes beside its object store, before make
// makes the entry at path.
func inRepository(make func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		store := filepath.Dir(path)
		for filepath.Base(store) != "objects" {
			store = filepath.Dir(store)
		}
		repository := filepath.Dir(store)
		if err := sandbox.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if err := sandbox.MkdirAll(filepath.Join(repository, "refs"), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if err := sandbox.WriteFile(filepath.Join(repository, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		make(t, path)
	}
}

// socket makes a Unix socket at path, an inode no listener holds, else one a
// listener holds until the test ends.
func socket(t *testing.T, path string) {
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
