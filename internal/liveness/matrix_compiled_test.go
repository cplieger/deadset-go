package liveness

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// A declaration dead in every configuration it exists in stays while a configuration
// compiles a reference to it from a declaration another configuration keeps live.
func TestMatrixSweepKeepsADeclarationAKeptCallerCompilesAReferenceTo(t *testing.T) {
	x := matrixOf(t, "matrix-compiled-caller.txtar", roots.RootOptions{}, linuxAmd64(), windowsAmd64())
	r := x.graph.Sweep(&SweepPolicy{Mode: facts.Mode{Production: true}})

	want := []string{"spare reference-counting [windows-amd64] refs=0/0"}
	if got := x.candidatesUnder("go://example.com/compiled#", r); !slices.Equal(got, want) {
		t.Errorf("Sweep(matrix-compiled-caller.txtar) reported\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
