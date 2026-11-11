//go:build unix

package main

import (
	"context"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

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

// TestAnalyzeSortsEveryEntryKindOfTheTree pins what each kind of entry does to a
// run, wherever the tree holds it: the run returns within two minutes, a note
// names each entry it cannot read, and a finding keeps or loses its class only as
// that entry's effect says. A pipe, a device, a socket or a symbolic link no build
// reads is passed over: it changes nothing. A directory or a Go file the run
// cannot read below where the load reads makes every export possible, and an
// entry rule N cannot read keeps every function and variable.
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
		{name: "a_pipe_named_a_go_file_in_an_ignored_directory", entry: "data/x.go", make: makeFifo, code: "findings", findings: certain},
		{name: "a_pipe_named_a_test_file_in_an_ignored_directory", entry: "data/foo_test.go", make: makeFifo, code: "findings", findings: certain},
		{name: "a_pipe_named_a_go_file_in_an_underscore_directory", entry: "_x/y.go", make: makeFifo, code: "findings", findings: certain},
		{name: "a_pipe_named_a_c_header", entry: "testdata/p.h", make: makeFifo, code: "findings", findings: certain},
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

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			reportPath := filepath.Join(t.TempDir(), "report.json")
			got := invokedUnder(ctx, t, dir, "analyze", "--target=.", "--report="+reportPath, "--min-confidence=possible")
			if ctx.Err() != nil {
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
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
}

// makeDir makes path an empty directory.
func makeDir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

// makeFifo makes a named pipe at path.
func makeFifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("Setup: this host makes no named pipe: %v", err)
	}
}

// makeSocket makes a Unix socket at path, an inode no listener holds, else one a
// listener holds until the test ends.
func makeSocket(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mknod(path, syscall.S_IFSOCK|0o600, 0); err == nil {
		return
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("Setup: this host makes no Unix socket at %s: %v", path, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
}

// symlinkTo makes path a symbolic link to target.
func symlinkTo(target string) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("Setup: this host makes no such symbolic link: %v", err)
		}
	}
}

// unlistable makes path a directory the user cannot list until the test ends,
// creating it where it is absent.
func unlistable(t *testing.T, path string) {
	t.Helper()
	asNonRootUser(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

// unopenable makes path a Go file the user cannot open.
func unopenable(t *testing.T, path string) {
	t.Helper()
	asNonRootUser(t)
	if err := os.WriteFile(path, []byte("package data\n"), 0); err != nil {
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
