package kinds

import (
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/graph"
)

func TestCapFilled_lowersAFindingAboutAFilledFieldToPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = config.Possible
	in := inputOf(t, "filled-field.txtar", resolved, Consumers{})
	for i := range in.Merged.Symbols {
		if in.Merged.Symbols[i].Ref == "go://example.com/filled#request.Extra" {
			in.Filled = map[graph.SymbolID]bool{in.Merged.Symbols[i].ID: true}
		}
	}
	if len(in.Filled) != 1 {
		t.Fatalf("Setup: filled-field.txtar declares no field Extra")
	}
	result := computed(t, in, packageEmitters())

	for name, want := range map[string]Class{"request.Extra": Possible, "request.Spare": Certain} {
		found := findingOf(t, result.Findings, unusedMemberCode, name)
		if found.Class != want || found.Confidence != want {
			t.Errorf("%s %s has class %s and confidence %s, want %s for both", unusedMemberCode, name, found.Class, found.Confidence, want)
		}
	}
}
