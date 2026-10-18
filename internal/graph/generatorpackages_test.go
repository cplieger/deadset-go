package graph

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// A package directory a go:generate directive passes by a relative path to a
// generator its named inputs do not cover has every exported declaration outside its
// test files, whose containers are exported, rooted as a generator input. The
// directive's own directory, a directory a type generator receives and a word with no
// relative path root nothing.
func TestRootsRootsTheExportsOfAPackageAGeneratorReceives(t *testing.T) {
	d := detect(t, "generator-packages.txtar", roots.RootOptions{})
	held := make(map[string]bool, len(d.symbols))
	for _, s := range d.symbols {
		held[s.Ref] = true
	}
	const schema = "go://example.com/genpkg/store/schema#"
	for _, ref := range []string{
		schema + "User.note", schema + "User.fields", schema + "hidden.Edges", schema + "Exported",
		"go://example.com/genpkg/store#Open", "go://example.com/genpkg/kinds#Spare", "go://example.com/genpkg/named#Named",
		"go://example.com/genpkg/store/other#Other", "go://example.com/genpkg/store/generated#Dead",
	} {
		if !held[ref] {
			t.Errorf("Symbols(generator-packages.txtar) holds no symbol whose reference is %s", ref)
		}
	}
	got := refsOfKind(d, roots.RootGenerate)
	slices.Sort(got)
	want := []string{schema + "Build", schema + "User", schema + "User.Fields", schema + "User.Name"}
	if !slices.Equal(got, want) {
		t.Errorf("Roots(generator-packages.txtar) roots %v as generator inputs, want %v", got, want)
	}
}
