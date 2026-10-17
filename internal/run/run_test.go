package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// A workspace member under the run directory is a consumer the report names, and one
// outside it is left to the topology, which counts it as a reader.
func TestNameableKeepsTheWorkspaceMembersTheReportCanName(t *testing.T) {
	invoked, err := os.Getwd()
	if err != nil {
		t.Fatalf("Setup: os.Getwd = _, %v", err)
	}
	inside := scope.Module{Path: filepath.Join(invoked, "member")}
	outside := scope.Module{Path: t.TempDir()}

	if named := nameable([]scope.Module{inside, outside}); !slices.Equal(named, []scope.Module{inside}) {
		t.Errorf("nameable() = %v, want [%v]", named, inside)
	}
}

// A module below the root that does not load is named by its directory; a workspace
// reader outside it is named at the root, and only where its module file cannot be
// read, which makes every export of the target possible.
func TestDroppedNotesNameAReaderWhoseModuleFileCannotBeReadAtTheRoot(t *testing.T) {
	base := t.TempDir()
	l := &Loaded{Root: filepath.Join(base, "app"), Loads: []enumerated{{Result: load.Result{Dropped: []load.Dropped{
		{Dir: filepath.Join(base, "app", "tool"), Err: errors.New("tool does not load")},
		{Dir: filepath.Join(base, "reader"), Err: fmt.Errorf("load: %w", load.ErrModuleFile)},
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
