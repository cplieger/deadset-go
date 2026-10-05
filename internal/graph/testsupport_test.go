package graph

import (
	"maps"
	"os"
	"slices"
	"testing"
)

// classifiedSupport loads the test-support archive and returns the packages
// ClassifyTestSupport classifies, with the declaration Kept configured as a root,
// and the functions outside test files it marked.
func classifiedSupport(t *testing.T, library bool) (support, marked []string) {
	t.Helper()

	result, root := loadDir(t, extract(t, "test-support.txtar"), "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(test-support.txtar): %v", err)
	}
	kept := slices.IndexFunc(symbols, func(s Symbol) bool { return s.Name == "Kept" })
	if kept < 0 {
		t.Fatalf("Setup: test-support.txtar declares no Kept")
	}
	opts := RootOptions{Patterns: []string{symbols[kept].Ref}, PublishedAPI: library}
	roots, _, err := Roots(result, root, os.ReadFile, symbols, opts)
	if err != nil {
		t.Fatalf("Setup: Roots(test-support.txtar): %v", err)
	}
	ClassifyTestSupport(result, symbols, roots, library)
	for i := range symbols {
		_, inTestFile := IsTestFile(symbols[i].Pos.Filename)
		if symbols[i].TestSupport && !inTestFile && symbols[i].Kind == KindFunc {
			marked = append(marked, symbols[i].Name)
		}
	}
	slices.Sort(marked)
	return slices.Sorted(maps.Keys(result.TestSupport)), marked
}

// Test-support code is the largest set of packages only tests reach: a fake a test
// imports, though production code calls the method it implements through an
// interface; a package only the fake imports; a package only its own test file
// uses; a package whose init function and blank declaration run when it is loaded;
// and a package a test imports for its init alone. A configured root, a production
// import, a production blank import and a package no test reaches each keep a
// package out.
func TestClassifyTestSupportIsTheLargestSetOnlyTestsReach(t *testing.T) {
	support, marked := classifiedSupport(t, false)

	want := []string{
		"example.com/support/chain",
		"example.com/support/fake",
		"example.com/support/internal/own",
		"example.com/support/register",
		"example.com/support/withinit",
	}
	if !slices.Equal(support, want) {
		t.Errorf("ClassifyTestSupport(test-support.txtar, application) = %v, want %v", support, want)
	}
	if wantMarked := []string{"Helper", "New", "One", "Started", "init", "init"}; !slices.Equal(marked, wantMarked) {
		t.Errorf("ClassifyTestSupport(test-support.txtar, application) marked the functions %v, want %v", marked, wantMarked)
	}
}

// A library's package outside an internal tree is importable by outside programs,
// so only the internal one stays test-support code.
func TestClassifyTestSupportKeepsALibrarysImportablePackagesOut(t *testing.T) {
	support, _ := classifiedSupport(t, true)

	if want := []string{"example.com/support/internal/own"}; !slices.Equal(support, want) {
		t.Errorf("ClassifyTestSupport(test-support.txtar, library) = %v, want %v", support, want)
	}
}
