package kinds

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// A test that calls published API outside programs may call is no test of dead
// code, though that API stays reported at possible; a test of an unexported dead
// declaration alone still is.
func TestATestOfPublishedAPIAtPossibleIsNoTestOfDeadCode(t *testing.T) {
	result := computed(t, inputOf(t, "declarations-test-published.txtar", libraryConfig(), Consumers{}), declarationEmitters())

	if got := namesUnder(result.Findings, vocab.TestOfDeadCode); !slices.Equal(got, []string{"TestSpare"}) {
		t.Errorf("%s reports %v, want [TestSpare]", vocab.TestOfDeadCode, got)
	}
	if found := findingOf(t, result.Findings, vocab.TestOnlyUse, "Unmarshal"); found.Confidence != vocab.Possible {
		t.Errorf("%s Unmarshal has confidence %s, want possible", vocab.TestOnlyUse, found.Confidence)
	}
}
