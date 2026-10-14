package graph

import (
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
)

// A go-fuzz entry point is a root in every file a fuzz build may compile, one whose
// constraint holds with the gofuzz tag set for some setting of its other tags, and not
// in a file whose constraint holds only without the tag.
func TestRootsRootsAGoFuzzEntryOfEveryFileAFuzzBuildMayCompile(t *testing.T) {
	d := detect(t, "fuzz-entries.txtar", RootOptions{})
	held := make(map[string]bool, len(d.symbols))
	for _, s := range d.symbols {
		held[s.Ref] = true
	}
	const pkg = "go://example.com/fuzzing/fuzz#"
	if !held[pkg+"FuzzExcluded"] {
		t.Errorf("Symbols(fuzz-entries.txtar) holds no symbol whose reference is %sFuzzExcluded", pkg)
	}
	got := refsOfKind(d, RootFuzz)
	slices.Sort(got)
	if want := []string{pkg + "FuzzLinux", pkg + "FuzzNegated", pkg + "FuzzPlain"}; !slices.Equal(got, want) {
		t.Errorf("Roots(fuzz-entries.txtar) roots %v as go-fuzz entries, want %v", got, want)
	}

	tagged := load.Configuration{ID: "linux-amd64-gofuzz", OS: "linux", Arch: "amd64", Tags: []string{goFuzzTag}}
	named, held := archiveRoots(t, "fuzz-entries.txtar", tagged, RootFuzz)
	if !held[pkg+"FuzzMixed"] {
		t.Errorf("Symbols(fuzz-entries.txtar) under the gofuzz tag holds no symbol whose reference is %sFuzzMixed", pkg)
	}
	if got, want := slices.Sorted(maps.Keys(named)), []string{pkg + "FuzzLinux", pkg + "FuzzMixed", pkg + "FuzzPlain"}; !slices.Equal(got, want) {
		t.Errorf("Roots(fuzz-entries.txtar) under the gofuzz tag roots %v as go-fuzz entries, want %v", got, want)
	}
}
