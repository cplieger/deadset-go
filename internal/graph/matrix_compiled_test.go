package graph

import (
	"slices"
	"strings"
	"testing"
)

// A declaration dead in every configuration it exists in stays while a configuration
// compiles a reference to it from a declaration another configuration keeps live.
func TestMatrixSweepKeepsADeclarationAKeptCallerCompilesAReferenceTo(t *testing.T) {
	x := matrixOf(t, "matrix-compiled-caller.txtar", RootOptions{}, linuxAmd64(), windowsAmd64())
	r := x.matrix.Sweep(&SweepInput{Mode: Mode{Production: true}}, nil)

	want := []string{"spare reference-counting [windows-amd64] refs=0/0"}
	if got := x.candidatesUnder("go://example.com/compiled#", r); !slices.Equal(got, want) {
		t.Errorf("Sweep(matrix-compiled-caller.txtar) reported\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
