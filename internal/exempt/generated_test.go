package exempt

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// symbolNames maps every symbol of one inventory to the name a message renders.
func symbolNames(symbols []inventory.Symbol) map[inventory.SymbolID]string {
	names := make(map[inventory.SymbolID]string, len(symbols))
	for i := range symbols {
		names[symbols[i].ID] = symbols[i].Name
	}
	return names
}

// rendered prints one exemption per line as the name of the declaration it
// retains, the class, the site and the clause, so a want list reads as source
// rather than as positions.
func rendered(symbols []inventory.Symbol, exemptions []Exemption) []string {
	names := symbolNames(symbols)
	lines := make([]string, 0, len(exemptions))
	for _, e := range exemptions {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s:%d:%d\t%s",
			names[e.ID], e.Class, e.Site.Filename, e.Site.Line, e.Site.Column, e.Detail))
	}
	return lines
}

func TestGeneratedFileDetectorRetainsEveryDeclarationOfAGeneratedFile(t *testing.T) {
	in := inputOf(t, "generated.txtar", Options{})

	got, err := GeneratedFileDetector(in)
	if err != nil {
		t.Fatalf("GeneratedFileDetector(generated.txtar) error: %v", err)
	}

	want := []string{
		"Table\tgenerated-file\tgen.go:3:1\tdeclared in a generated file",
		"size\tgenerated-file\tgen.go:3:1\tdeclared in a generated file",
	}
	if lines := rendered(in.Symbols, got); !slices.Equal(lines, want) {
		t.Errorf("GeneratedFileDetector(generated.txtar) = %q, want %q", lines, want)
	}
}

func TestGeneratedFileDetectorRetainsNothingWhenGeneratedFilesAreIncluded(t *testing.T) {
	in := inputOf(t, "generated.txtar", Options{IncludeGenerated: true})

	got, err := GeneratedFileDetector(in)
	if err != nil {
		t.Fatalf("GeneratedFileDetector(generated.txtar, include) error: %v", err)
	}

	if lines := rendered(in.Symbols, got); len(lines) != 0 {
		t.Errorf("GeneratedFileDetector(generated.txtar, include) = %q, want no exemption", lines)
	}
}

// candidatesOf sweeps one archive with the exemptions the generated-file class
// computes under opts, and returns the name of every candidate in the order the
// inventory holds them.
func candidatesOf(t *testing.T, archive string, opts Options) []string {
	t.Helper()
	in := inputOf(t, archive, opts)
	exemptions, err := GeneratedFileDetector(in)
	if err != nil {
		t.Fatalf("Setup: GeneratedFileDetector(%s): %v", archive, err)
	}
	names := symbolNames(in.Symbols)
	var got []string
	for _, c := range SweepCandidates(t, in, roots.RootOptions{}, exemptions) {
		got = append(got, names[c.ID])
	}
	return got
}

func TestGeneratedDeclarationsAreCandidatesOnlyWhenGeneratedFilesAreIncluded(t *testing.T) {
	retained := candidatesOf(t, "generated.txtar", Options{})
	wantRetained := []string{"Lookup", "late", "nearMiss"}
	if !slices.Equal(retained, wantRetained) {
		t.Errorf("Sweep(generated.txtar) candidates = %q, want %q", retained, wantRetained)
	}

	included := candidatesOf(t, "generated.txtar", Options{IncludeGenerated: true})
	wantIncluded := []string{"Lookup", "Table", "size", "late", "nearMiss"}
	if !slices.Equal(included, wantIncluded) {
		t.Errorf("Sweep(generated.txtar, include) candidates = %q, want %q", included, wantIncluded)
	}
}
