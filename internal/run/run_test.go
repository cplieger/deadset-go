package run

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// A workspace member under the run directory is a consumer the report names, and one
// outside it is left to the topology, which counts it as a reader.
func TestNameableKeepsTheWorkspaceMembersTheReportCanName(t *testing.T) {
	invoked, err := os.Getwd()
	if err != nil {
		t.Fatalf("Setup: os.Getwd = _, %v", err)
	}
	inside := scope.Module{Path: filepath.Join(invoked, "member")}
	outside := scope.Module{Path: sandbox.Dir(t)}

	if named := nameable(invoked, []scope.Module{inside, outside}); !slices.Equal(named, []scope.Module{inside}) {
		t.Errorf("nameable() = %v, want [%v]", named, inside)
	}
}

// A module below the root that does not load is named by its directory; a workspace
// reader outside it is named at the root, and only where its module file cannot be
// read, which makes every export of the target possible.
func TestDroppedNotesNameAReaderWhoseModuleFileCannotBeReadAtTheRoot(t *testing.T) {
	top, base := discoveredTarget(t)
	l := &Loaded{Root: top.Root, Topology: top, loads: &loads{outlines: []outline{{dropped: []load.Dropped{
		{Dir: filepath.Join(base, "app", "tool"), Err: errors.New("tool does not load")},
		{Dir: filepath.Join(base, "reader"), Err: errors.New("load: unreadable module file"), Unreadable: true},
		{Dir: filepath.Join(base, "broken"), Err: errors.New("broken does not load")},
	}}}}}

	var got []string
	for _, note := range l.droppedNotes() {
		got = append(got, note.Path+" "+note.Message)
	}
	want := []string{
		"tool module tool is below the target and does not load, so the run does not analyze it, and an export of a module it requires is possible: tool does not load",
		". module ../reader the workspace lists has a module file the run cannot read, so the run does not count its references, and every export of the target is possible",
	}
	if !slices.Equal(got, want) {
		t.Errorf("droppedNotes() = %q, want %q", got, want)
	}
}

// discoveredTarget is the topology of a module at app below a resolved temporary
// directory, and that directory: the target root a run spells as os.Getwd resolves
// it.
func discoveredTarget(t *testing.T) (*load.Topology, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(sandbox.Dir(t))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	app := filepath.Join(base, "app")
	if err := sandbox.MkdirAll(app, 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for name, body := range map[string]string{"go.mod": "module example.com/app\n\ngo 1.27.1\n", "main.go": "package main\n\nfunc main() {}\n"} {
		if err := sandbox.WriteFile(filepath.Join(app, name), []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	top, err := load.Discover(t.Context(), scope.Document{Target: scope.Module{Path: app}})
	if err != nil {
		t.Fatalf("Setup: load.Discover(%s) = _, %v", app, err)
	}
	return top, base
}

// linkedTarget is [discoveredTarget] with a link to its directory in another one:
// the spelling a scope document, GOENV or an absolute directive gives the same
// tree, as macOS spells /private/var through /var.
func linkedTarget(t *testing.T) (top *load.Topology, base, alias string) {
	t.Helper()
	top, base = discoveredTarget(t)
	alias = filepath.Join(sandbox.Dir(t), "alias")
	if err := sandbox.Symlink(base, alias); err != nil {
		t.Skipf("Setup: this host makes no such symbolic link: %v", err)
	}
	return top, base, alias
}

// Every path a note names that some input spells through a link to the tree is
// named where it resolves, relative to the target root ([program.10]): a dropped
// module, a workspace reader, an unread entry, a hidden entry and the go env file
// GOENV names, whose name a note omits only where it lies outside the root. Three
// spellings of one hidden entry, the root's, one through a link to the tree and
// one through a link inside the root to its directory, make one note.
func TestNotesNameAPathSpelledThroughALinkWhereItResolves(t *testing.T) {
	top, base, alias := linkedTarget(t)
	app := filepath.Join(base, "app")
	if err := sandbox.MkdirAll(filepath.Join(app, "p"), 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := sandbox.Symlink("p", filepath.Join(app, "lp")); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	top.Unread = append(top.Unread, filepath.Join(alias, "app", "data"))
	top.EnvFiles = []load.EnvFile{{Path: filepath.Join(alias, "app", "env"), Named: true}, {Path: filepath.Join(alias, "env"), Named: true}}
	p := &Prepared{Loaded: &Loaded{Root: top.Root, Topology: top, loads: &loads{outlines: []outline{{
		dropped: []load.Dropped{
			{Dir: filepath.Join(alias, "app", "tool"), Err: errors.New("load: unreadable module file"), Unreadable: true},
			{Dir: filepath.Join(alias, "reader"), Err: errors.New("load: unreadable module file"), Unreadable: true},
		},
		hidden: []string{filepath.Join(app, "p", "x.go"), filepath.Join(alias, "app", "p", "x.go"), filepath.Join(app, "lp", "x.go"), filepath.Join(alias, "b", "q", "x.go")},
	}}}}}

	var got []string
	for _, note := range slices.Concat(p.droppedNotes(), p.unreadNotes()) {
		got = append(got, note.Path+" "+note.Message)
	}
	slices.Sort(got)
	want := []string{
		". ../b/q/x.go is a pipe",
		". module ../reader the workspace lists",
		". the go env file GOENV names is no regular file",
		". the go env file GOENV names, env, is no regular file",
		"data data cannot be read",
		"p/x.go p/x.go is a pipe",
		"tool module tool is below the target and does not load, so the run does not analyze it, and every export of the target is possible",
	}
	matched := len(got) == len(want)
	for i := range want {
		matched = matched && strings.HasPrefix(got[i], want[i])
	}
	if !matched {
		t.Errorf("the notes are\n%s\nwant ones opening\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
