package liveness

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// A function or a method whose doc comment holds a line opening with the swag route
// annotation is a root, and one whose comment holds only another annotation, a longer
// word or the annotation inside a line is not.
func TestRootsRootsWhatASwagRouteAnnotationDocuments(t *testing.T) {
	d := detect(t, "swag.txtar", roots.RootOptions{})
	held := make(map[string]bool, len(d.symbols))
	for _, s := range d.symbols {
		held[s.Ref] = true
	}
	const pkg = "go://example.com/swag#"
	for _, ref := range []string{pkg + "Handler.summarized", pkg + "Handler.longer", pkg + "Handler.inline"} {
		if !held[ref] {
			t.Errorf("Symbols(swag.txtar) holds no symbol whose reference is %s", ref)
		}
	}
	if got, want := refsOfKind(d, roots.RootSwagRoute), []string{pkg + "Handler.postAlias", pkg + "listAll"}; !slices.Equal(got, want) {
		t.Errorf("Roots(swag.txtar) roots %v as swag routes, want %v", got, want)
	}
}
