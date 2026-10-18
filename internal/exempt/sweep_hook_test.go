package exempt

import (
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// SweptCandidate is one candidate a sweep reported, as this package's tests read it.
type SweptCandidate struct {
	ID             inventory.SymbolID
	Relation       string
	ProductionRefs int
	TestRefs       int
}

// Sweeper sweeps one input's graph with the given exemptions held back. The sweep
// imports this package, so only the external test package can build it, and its
// init sets this.
var Sweeper func(in *Input, rooted roots.RootOptions, held []Exemption) ([]SweptCandidate, error)

// SweepCandidates is what [Sweeper] reports over one input.
func SweepCandidates(t *testing.T, in *Input, rooted roots.RootOptions, held []Exemption) []SweptCandidate {
	t.Helper()
	if Sweeper == nil {
		t.Fatal("Setup: no sweeper is set")
	}
	swept, err := Sweeper(in, rooted, held)
	if err != nil {
		t.Fatalf("Setup: sweep: %v", err)
	}
	return swept
}

// GoClasses is every class this analyzer computes, for the external tests.
func GoClasses() []vocab.Exemption { return goClasses() }
