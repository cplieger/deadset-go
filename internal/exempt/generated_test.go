package exempt

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// symbolNames maps every symbol of one inventory to the name a message renders.
func symbolNames(in *Input) map[graph.SymbolID]string {
	names := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		names[in.Symbols[i].ID] = in.Symbols[i].Name
	}
	return names
}

// rendered prints one exemption per line as the name of the declaration it
// retains, the class, the site and the clause, so a want list reads as source
// rather than as positions.
func rendered(in *Input, exemptions []graph.Exemption) []string {
	names := symbolNames(in)
	lines := make([]string, 0, len(exemptions))
	for _, e := range exemptions {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s:%d:%d\t%s",
			names[e.ID], e.Class, e.Site.Filename, e.Site.Line, e.Site.Column, e.Detail))
	}
	return lines
}

// loadedFiles indexes one configuration's syntax by file name.
func loadedFiles(in *Input) map[string]*ast.File {
	files := make(map[string]*ast.File)
	for _, p := range in.Result.Packages {
		for _, f := range p.Syntax {
			files[filepath.Base(in.Result.Fset.Position(f.FileStart).Filename)] = f
		}
	}
	return files
}

func TestIsGeneratedFileMatchesTheStandardHeaderOnly(t *testing.T) {
	files := loadedFiles(inputOf(t, "generated.txtar", Options{}))
	tests := []struct {
		file string
		want bool
	}{
		{file: "gen.go", want: true},
		{file: "app.go", want: false},
		{file: "late.go", want: false},
		{file: "nearmiss.go", want: false},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			f, loaded := files[test.file]
			if !loaded {
				t.Fatalf("Setup: %s is not among the loaded files", test.file)
			}
			if got := IsGeneratedFile(f); got != test.want {
				t.Errorf("IsGeneratedFile(%s) = %t, want %t", test.file, got, test.want)
			}
		})
	}
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
	if lines := rendered(in, got); !slices.Equal(lines, want) {
		t.Errorf("GeneratedFileDetector(generated.txtar) = %q, want %q", lines, want)
	}
}

func TestGeneratedFileDetectorRetainsNothingWhenGeneratedFilesAreIncluded(t *testing.T) {
	in := inputOf(t, "generated.txtar", Options{IncludeGenerated: true})

	got, err := GeneratedFileDetector(in)
	if err != nil {
		t.Fatalf("GeneratedFileDetector(generated.txtar, include) error: %v", err)
	}

	if lines := rendered(in, got); len(lines) != 0 {
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
	refs, _, err := graph.References(in.Result, in.Root, os.ReadFile, in.Symbols)
	if err != nil {
		t.Fatalf("Setup: graph.References(%s): %v", archive, err)
	}
	roots, _, err := graph.Roots(in.Result, in.Root, os.ReadFile, in.Symbols, graph.RootOptions{})
	if err != nil {
		t.Fatalf("Setup: graph.Roots(%s): %v", archive, err)
	}

	names := symbolNames(in)
	result := graph.New(in.Symbols, refs, roots).Sweep(graph.Mode{Exempt: exemptions})
	got := make([]string, 0, len(result.Candidates))
	for _, c := range result.Candidates {
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
