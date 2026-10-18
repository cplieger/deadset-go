package roots

import (
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/conventions"
)

func TestEveryConventionRowReportsAPublishedKindAndEveryShapeHasARow(t *testing.T) {
	shapes := make(map[string]bool)
	for i, row := range conventionRows {
		if int(row.kind) >= len(rootNames) {
			t.Errorf("convention row %d reports %v, want a published root kind", i, row.kind)
		}
		switch row.rule.(type) {
		case *conventions.DocDirective:
			shapes["doc"] = true
		case *conventions.GenerateInputs:
			shapes["generate"] = true
		case *conventions.PackageGenerator:
			shapes["package"] = true
		case *conventions.SuiteRunner:
			shapes["suite"] = true
		case *conventions.TaggedEntry:
			shapes["tagged"] = true
		}
	}
	if len(shapes) != 5 {
		t.Errorf("the rows use the shapes %v, want all five", shapes)
	}
}
