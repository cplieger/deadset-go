package kinds

import (
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

func TestCapFilled_lowersAFindingAboutAFilledFieldToPossible(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Possible
	in := inputOf(t, "filled-field.txtar", resolved, Consumers{})
	for i := range in.Merged.Symbols {
		if in.Merged.Symbols[i].Ref == "go://example.com/filled#request.Extra" {
			in.Filled = map[inventory.SymbolID]bool{in.Merged.Symbols[i].ID: true}
		}
	}
	if len(in.Filled) != 1 {
		t.Fatalf("Setup: filled-field.txtar declares no field Extra")
	}
	result := computed(t, in, packageEmitters())

	for name, want := range map[string]vocab.Class{"request.Extra": vocab.Possible, "request.Spare": vocab.Certain} {
		found := findingOf(t, result.Findings, vocab.UnusedMember, name)
		if found.Class != want || found.Confidence != want {
			t.Errorf("%s %s has class %s and confidence %s, want %s for both", vocab.UnusedMember, name, found.Class, found.Confidence, want)
		}
	}
}
