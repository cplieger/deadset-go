package graph

import (
	"slices"
	"testing"
)

func TestIsTestFileAppliesTheLanguageRule(t *testing.T) {
	cases := []struct {
		name string
		path string
		rule string
		want bool
	}{
		{name: "plain", path: "catalog_test.go", rule: "go-test-suffix", want: true},
		{name: "under_a_package_directory", path: "internal/queue/queue_test.go", rule: "go-test-suffix", want: true},
		{name: "external_test_package", path: "external_test.go", rule: "go-test-suffix", want: true},
		{name: "one_letter_stem", path: "x_test.go", rule: "go-test-suffix", want: true},
		{name: "production_file", path: "catalog.go", want: false},
		{name: "named_for_the_testing_package", path: "testing.go", want: false},
		{name: "named_test", path: "test.go", want: false},
		{name: "golden_beside_a_test", path: "catalog_test.golden", want: false},
		{name: "suffix_not_at_the_end", path: "catalog_test.go.txt", want: false},
		// A name the toolchain compiles nothing from is no test file: a leading
		// underscore or full stop takes the file out of the build entirely.
		{name: "bare_suffix", path: "_test.go", want: false},
		{name: "underscore_stem", path: "_x_test.go", want: false},
		{name: "full_stop_stem", path: ".x_test.go", want: false},
		// The rule reads the file's own name, so no directory takes part: one
		// named for tests classifies nothing, one named like a test file
		// classifies nothing, and one the toolchain skips does not unclassify
		// the file inside it.
		{name: "testdata_directory", path: "testdata/catalog.go", want: false},
		{name: "tests_directory", path: "tests/catalog.go", want: false},
		{name: "directory_named_like_a_test_file", path: "x_test.go/catalog.go", want: false},
		{name: "underscore_directory", path: "_scratch/catalog_test.go", rule: "go-test-suffix", want: true},
		{name: "full_stop_directory", path: ".hidden/catalog_test.go", rule: "go-test-suffix", want: true},
		{name: "empty", path: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, ok := IsTestFile(tc.path)
			if ok != tc.want {
				t.Fatalf("IsTestFile(%q) = %q, %t, want %t", tc.path, rule, ok, tc.want)
			}
			if rule != tc.rule {
				t.Errorf("IsTestFile(%q) named rule %q, want %q", tc.path, rule, tc.rule)
			}
		})
	}
}

func TestReferencesReportsTheRuleThatClassifiedEachTestFile(t *testing.T) {
	cases := []struct {
		archive string
		want    []TestFileRule
	}{
		// catalog_test.go and external_test.go.
		{archive: "variants.txtar", want: []TestFileRule{{Rule: "go-test-suffix", Matched: 2}}},
		// A module with no test file reads the rule with a count of zero rather
		// than no rule at all.
		{archive: "reference-kinds.txtar", want: []TestFileRule{{Rule: "go-test-suffix", Matched: 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.archive, func(t *testing.T) {
			if got := analyze(t, tc.archive).rules; !slices.Equal(got, tc.want) {
				t.Errorf("References(%s) reported test-file rules %v, want %v", tc.archive, got, tc.want)
			}
		})
	}
}

func TestReferencesFlagsEveryReferenceATestFileMakes(t *testing.T) {
	a := analyze(t, "variants.txtar")

	for _, r := range a.refs {
		_, isTest := IsTestFile(r.Pos.Filename)
		if r.Test != isTest {
			t.Errorf("References(variants.txtar) reference from %s to %s at %s:%d:%d has Test %t, want %t",
				a.name(r.From), a.name(r.To), r.Pos.Filename, r.Pos.Line, r.Pos.Column, r.Test, isTest)
		}
	}
}

func TestReferencesLeavesProductionModeOneReferenceSet(t *testing.T) {
	a := analyze(t, "variants.txtar")

	// Production mode is the reference set with every test reference dropped,
	// which is the filter a caller applies rather than a mode the walk takes.
	var production []string
	for _, r := range a.refs {
		if r.Test {
			continue
		}
		production = append(production, a.name(r.From)+" to "+a.name(r.To))
	}
	if want := []string{"Resolve to normalize"}; !slices.Equal(production, want) {
		t.Errorf("References(variants.txtar) production references are %v, want %v", production, want)
	}
	// The external test file's import of the target package is one of the four,
	// and it is a test reference the way everything else that file writes is.
	if len(a.refs) != 4 {
		t.Errorf("References(variants.txtar) returned %d references, want the 4 the fixture writes, the external test file's import included",
			len(a.refs))
	}
}
