package scope

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// workspaceTree writes a workspace file listing the named module directories,
// creates each of them, and returns the workspace directory. The entries are
// written as the caller spells them, so a test can stage an absolute entry, a
// relative one, or one the workspace does not hold.
func workspaceTree(t *testing.T, entries ...string) string {
	t.Helper()
	return writeWorkspace(t, t.TempDir(), entries...)
}

// writeWorkspace writes a workspace file in dir listing the named module
// directories, creates each relative one, and returns dir.
func writeWorkspace(t *testing.T, dir string, entries ...string) string {
	t.Helper()
	body := "go 1.27.1\n\nuse (\n"
	for _, entry := range entries {
		body += "\t" + entry + "\n"
		if filepath.IsAbs(entry) {
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, entry), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", entry, err)
		}
	}
	body += ")\n"
	if err := os.WriteFile(filepath.Join(dir, WorkspaceFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("Setup: write the workspace file: %v", err)
	}
	return dir
}

func TestResolveReadsTheWorkspaceTheTargetIsAMemberOf(t *testing.T) {
	dir := workspaceTree(t, "./library", "./consumer", "./other")

	got, err := Resolve(t.Context(), filepath.Join(dir, "library"), "")
	if err != nil {
		t.Fatalf("Resolve(%s/library, no document) = _, %v, want no error", dir, err)
	}

	want := Document{
		Workspace: filepath.Join(dir, WorkspaceFileName),
		Target:    Module{Path: filepath.Join(dir, "library")},
		Consumers: []Module{
			{Path: filepath.Join(dir, "consumer")},
			{Path: filepath.Join(dir, "other")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve(%s/library, no document) = %+v, want %+v", dir, got, want)
	}
}

func TestResolveReadsTheWorkspaceOfAnAncestorDirectory(t *testing.T) {
	dir := workspaceTree(t, "./library", "./consumer")
	nested := filepath.Join(dir, "nest")
	if err := os.MkdirAll(filepath.Join(nested, "library"), 0o750); err != nil {
		t.Fatalf("Setup: create the nested tree: %v", err)
	}
	// The workspace one directory up lists ./library relative to itself, so the
	// nested directory of the same name is a different module and no member.
	target := filepath.Join(nested, "library")

	got, err := Resolve(t.Context(), target, "")
	if err != nil {
		t.Fatalf("Resolve(%s, no document) = _, %v, want no error", target, err)
	}

	want := Document{Target: Module{Path: target}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve(%s, no document) = %+v, want the target alone %+v", target, got, want)
	}
}

func TestResolvePrecedence(t *testing.T) {
	dir := workspaceTree(t, "./library", "./consumer")
	target := filepath.Join(dir, "library")
	document := filepath.Join(dir, "scope.json")
	body := `{"target": {"path": "library"}, "consumers": [{"path": "other"}]}`
	if err := os.WriteFile(document, []byte(body), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", document, err)
	}

	cases := map[string]struct {
		document  string
		wantPaths []string
		// wantWorkspace reports whether the resolved scope names the workspace
		// file, which only the workspace source does here.
		wantWorkspace bool
	}{
		"a document the invocation names outranks the workspace": {
			document:  document,
			wantPaths: []string{filepath.Join(dir, "other")},
		},
		"the workspace answers when no document is named": {
			wantPaths:     []string{filepath.Join(dir, "consumer")},
			wantWorkspace: true,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Resolve(t.Context(), target, test.document)
			if err != nil {
				t.Fatalf("Resolve(%s, %q) = _, %v, want no error", target, test.document, err)
			}
			paths := make([]string, 0, len(got.Consumers))
			for _, consumer := range got.Consumers {
				paths = append(paths, consumer.Path)
			}
			if !reflect.DeepEqual(paths, test.wantPaths) {
				t.Errorf("Resolve(%s, %q) consumer paths = %v, want %v", target, test.document, paths, test.wantPaths)
			}
			if held := got.Workspace != ""; held != test.wantWorkspace {
				t.Errorf("Resolve(%s, %q).Workspace = %q, want a workspace: %t", target, test.document, got.Workspace, test.wantWorkspace)
			}
			if got.Target.Path != target {
				t.Errorf("Resolve(%s, %q).Target.Path = %q, want %q", target, test.document, got.Target.Path, target)
			}
		})
	}
}

func TestResolveFallsBackToTheTargetAlone(t *testing.T) {
	// A directory with no workspace file at or above it: a temporary directory
	// under the system's own, which holds none.
	target := t.TempDir()

	got, err := Resolve(t.Context(), target, "")
	if err != nil {
		t.Fatalf("Resolve(%s, no document) = _, %v, want no error", target, err)
	}

	want := Document{Target: Module{Path: target}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve(%s, no document) = %+v, want %+v", target, got, want)
	}
}

func TestResolveFallsBackWhenTheWorkspaceDoesNotListTheTarget(t *testing.T) {
	dir := workspaceTree(t, "./consumer")
	target := filepath.Join(dir, "elsewhere")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", target, err)
	}

	got, err := Resolve(t.Context(), target, "")
	if err != nil {
		t.Fatalf("Resolve(%s, no document) = _, %v, want no error", target, err)
	}

	want := Document{Target: Module{Path: target}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve(%s, no document) = %+v, want the target alone %+v", target, got, want)
	}
}

func TestResolveResolvesAnAbsoluteWorkspaceEntry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "library")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", target, err)
	}
	writeWorkspace(t, dir, target, "./consumer")

	got, err := Resolve(t.Context(), target, "")
	if err != nil {
		t.Fatalf("Resolve(%s, no document) = _, %v, want no error", target, err)
	}
	if got.Target.Path != target {
		t.Errorf("Resolve(%s, no document).Target.Path = %q, want %q", target, got.Target.Path, target)
	}
	if len(got.Consumers) != 1 || got.Consumers[0].Path != filepath.Join(dir, "consumer") {
		t.Errorf("Resolve(%s, no document).Consumers = %+v, want the workspace's other entry", target, got.Consumers)
	}
}

func TestResolveRefusesAMalformedWorkspace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "library")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", target, err)
	}
	file := filepath.Join(dir, WorkspaceFileName)
	// A use block the file never closes: the toolchain refuses to read it, and
	// the refusal is what a run has to end on, because a workspace it cannot read
	// is a consumer set it cannot know.
	if err := os.WriteFile(file, []byte("go 1.27.1\n\nuse (\n\t./library\n"), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", file, err)
	}

	got, err := Resolve(t.Context(), target, "")

	if !errors.Is(err, ErrWorkspace) {
		t.Fatalf("Resolve(%s, no document) = _, %v, want an error matching ErrWorkspace", target, err)
	}
	if !strings.Contains(err.Error(), file) {
		t.Errorf("Resolve(%s, no document) error = %q, want it to name %s", target, err, file)
	}
	if !reflect.DeepEqual(got, Document{}) {
		t.Errorf("Resolve(%s, no document) = %+v, want the zero Document", target, got)
	}
}

func TestResolveRefusesAnAbsentTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "missing")

	_, err := Resolve(t.Context(), target, "")

	if err == nil || !strings.Contains(err.Error(), target) {
		t.Errorf("Resolve(%s, no document) = _, %v, want an error naming the path", target, err)
	}
}

func TestReadWorkspaceKeepsTheOrderTheFileLists(t *testing.T) {
	dir := workspaceTree(t, "./c", "./a", "./b")

	got, err := readWorkspace(t.Context(), filepath.Join(dir, WorkspaceFileName))
	if err != nil {
		t.Fatalf("readWorkspace(%s) = _, %v, want no error", dir, err)
	}

	want := []string{filepath.Join(dir, "c"), filepath.Join(dir, "a"), filepath.Join(dir, "b")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readWorkspace(%s) = %v, want %v", dir, got, want)
	}
}

func TestWorkspaceFileFindsTheNearestFile(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	deeper := filepath.Join(inner, "deeper")
	if err := os.MkdirAll(deeper, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", deeper, err)
	}
	for _, dir := range []string{outer, inner} {
		if err := os.WriteFile(filepath.Join(dir, WorkspaceFileName), []byte("go 1.27.1\n"), 0o600); err != nil {
			t.Fatalf("Setup: write the workspace file in %s: %v", dir, err)
		}
	}

	got, found := workspaceFile(deeper)

	if !found || got != filepath.Join(inner, WorkspaceFileName) {
		t.Errorf("workspaceFile(%s) = %q, %t, want %q, true", deeper, got, found, filepath.Join(inner, WorkspaceFileName))
	}
}

func TestWorkspaceFileIgnoresADirectoryOfThatName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, WorkspaceFileName), 0o750); err != nil {
		t.Fatalf("Setup: create the directory: %v", err)
	}

	got, found := workspaceFile(dir)

	if found {
		t.Errorf("workspaceFile(%s) = %q, true, want no workspace: a directory is not the file", dir, got)
	}
}
