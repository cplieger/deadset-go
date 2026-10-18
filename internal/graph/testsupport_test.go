package graph

import (
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// classifiedSupport loads the test-support archive and returns the packages
// ClassifyTestSupport classifies, with the declaration Kept configured as a root,
// and the functions outside test files those packages declare.
func classifiedSupport(t *testing.T, library bool) (support, marked []string) {
	t.Helper()

	result, root := loadDir(t, extract(t, "test-support.txtar"), "linux", "amd64")
	symbols, rs, err := inventoryOf(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(test-support.txtar): %v", err)
	}
	kept := slices.IndexFunc(symbols, func(s inventory.Symbol) bool { return s.Name == "Kept" })
	if kept < 0 {
		t.Fatalf("Setup: test-support.txtar declares no Kept")
	}
	opts := roots.RootOptions{Patterns: []string{symbols[kept].Ref}, PublishedAPI: library}
	rooted, _ := roots.Roots(result, rs, symbols, opts)
	classes := roots.ClassifyTestSupport(result, symbols, rooted, library)
	for i := range symbols {
		if classes.Support[symbols[i].Package] && !symbols[i].InTestFile && symbols[i].Kind == inventory.KindFunc {
			marked = append(marked, symbols[i].Name)
		}
	}
	slices.Sort(marked)
	return paths(classes.Support), marked
}

// Test-support code is the largest set of packages only tests reach that carry test
// evidence: a fake a test imports, though production code calls the method it
// implements through an interface; a package only its own test file uses, one of its
// files importing the testing package; a package whose init function and blank
// declaration run when it is loaded; and a package a test imports for its init. A
// package with no test evidence, which only the fake or only a test imports, a
// configured root, a production import, a production blank import and a package no
// test reaches each keep a package out.
func TestClassifyTestSupportIsTheLargestSetOnlyTestsReach(t *testing.T) {
	support, marked := classifiedSupport(t, false)

	want := []string{
		"example.com/support/fake",
		"example.com/support/internal/own",
		"example.com/support/register",
		"example.com/support/withinit",
	}
	if !slices.Equal(support, want) {
		t.Errorf("ClassifyTestSupport(test-support.txtar, application) = %v, want %v", support, want)
	}
	if wantMarked := []string{"Check", "Helper", "Must", "New", "Registered", "Skip", "Started", "init", "init"}; !slices.Equal(marked, wantMarked) {
		t.Errorf("ClassifyTestSupport(test-support.txtar, application) marked the functions %v, want %v", marked, wantMarked)
	}
}

// An import of a package of a test library is test evidence as the testing
// package's is, and one of a module whose path only starts with a library's is not.
func TestClassifyTestSupportCountsTestLibraryImportsAsEvidence(t *testing.T) {
	result, root := loadDir(t, extract(t, "test-support-libraries.txtar"), "linux", "amd64")
	symbols, rs, err := inventoryOf(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(test-support-libraries.txtar): %v", err)
	}
	rooted, _ := roots.Roots(result, rs, symbols, roots.RootOptions{})
	classes := roots.ClassifyTestSupport(result, symbols, rooted, false)

	if got, want := paths(classes.Support), []string{
		"example.com/libs/asserted", "example.com/libs/matched", "example.com/libs/tooled",
	}; !slices.Equal(got, want) {
		t.Errorf("ClassifyTestSupport(test-support-libraries.txtar) test support = %v, want %v", got, want)
	}
	if got, want := paths(classes.Reached), []string{"example.com/libs/lookalike"}; !slices.Equal(got, want) {
		t.Errorf("ClassifyTestSupport(test-support-libraries.txtar) test reached = %v, want %v", got, want)
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

// A declaration a line directive places in another directory of the target is still
// a declaration of the package whose file holds it, so it is test-support code with
// that package.
func TestClassifyTestSupportKeepsADeclarationALineDirectiveMovesInItsPackage(t *testing.T) {
	result, root := loadDir(t, extract(t, "test-support-line-directive.txtar"), "linux", "amd64")
	symbols, rs, err := inventoryOf(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(test-support-line-directive.txtar): %v", err)
	}
	rooted, _ := roots.Roots(result, rs, symbols, roots.RootOptions{})
	classes := roots.ClassifyTestSupport(result, symbols, rooted, false)

	var marked []string
	for i := range symbols {
		if symbols[i].Kind == inventory.KindFunc && classes.Support[symbols[i].Package] {
			marked = append(marked, symbols[i].Name+" "+symbols[i].Pos.Filename)
		}
	}
	slices.Sort(marked)
	if want := []string{"Check helper/helper.go", "Generated tmpl/gen.tmpl"}; !slices.Equal(marked, want) {
		t.Errorf("ClassifyTestSupport(test-support-line-directive.txtar) marked %v, want %v", marked, want)
	}
}

// paths is the import path of every package of a set, sorted.
func paths(set map[load.PackageID]bool) []string {
	held := make([]string, 0, len(set))
	for id := range set {
		held = append(held, id.Path)
	}
	slices.Sort(held)
	return held
}

// A configuration that loads a package and classifies it otherwise vetoes the class,
// whatever the order of the matrix, and one that does not load it decides nothing.
func TestMatrixTestsKeepsAClassEveryConfigurationLoadingThePackageGives(t *testing.T) {
	support, reached, production, absent := load.PackageID{Path: "s"}, load.PackageID{Path: "r"}, load.PackageID{Path: "p"}, load.PackageID{Path: "a"}
	set := func(keys ...load.PackageID) map[load.PackageID]bool {
		held := make(map[load.PackageID]bool, len(keys))
		for _, key := range keys {
			held[key] = true
		}
		return held
	}
	first := roots.TestClasses{
		Support: set(support, reached, production, absent),
		Loaded:  set(support, reached, production, absent),
	}
	second := roots.TestClasses{
		Support: set(support),
		Reached: set(reached),
		Loaded:  set(support, reached, production),
	}
	for name, per := range map[string][]roots.TestClasses{"in order": {first, second}, "reversed": {second, first}} {
		t.Run(name, func(t *testing.T) {
			got := roots.MatrixTests(per)
			if want := set(support, absent); !maps.Equal(got.Support, want) {
				t.Errorf("MatrixTests(%s).Support = %v, want %v", name, got.Support, want)
			}
			if want := set(reached); !maps.Equal(got.Reached, want) {
				t.Errorf("MatrixTests(%s).Reached = %v, want %v", name, got.Reached, want)
			}
		})
	}
}
