package graph

import (
	"os"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/testsupport"
)

// sharedAnalysis is what one archive's load answers for the linux-amd64
// configuration under one list of configured root patterns, read by every test that
// asks the same question of it: the inventory, the references and the test-file
// rules over it, the roots and the unmatched patterns with and without the
// published-API seed, and the files the load excluded for importing C. No part of the
// load it was computed from is held.
type sharedAnalysis struct {
	symbols       []Symbol
	refs          []Reference
	rules         []TestFileRule
	roots         map[bool][]Root
	unmatched     map[bool][]Unmatched
	excludedByCgo []string
}

// analysisKey is one shared analysis: the archive and its configured root patterns.
type analysisKey struct {
	archive  string
	patterns string
}

// analyses holds every shared analysis the package's tests built.
var analyses testsupport.Memo[analysisKey, *sharedAnalysis]

// analysisOf is the shared analysis of one archive under the configured root
// patterns given, loaded once for every test that reads it.
func analysisOf(t *testing.T, archive string, patterns []string) *sharedAnalysis {
	t.Helper()

	key := analysisKey{archive: archive, patterns: strings.Join(patterns, "\n")}
	return analyses.Of(t, key, func(t *testing.T) *sharedAnalysis {
		return analyzeShared(t, archive, patterns)
	})
}

// analyzeShared loads one archive and runs every pass a shared analysis answers.
func analyzeShared(t *testing.T, archive string, patterns []string) *sharedAnalysis {
	t.Helper()

	result, root := loadDir(t, extract(t, archive), "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(%s) error: %v", archive, err)
	}
	refs, rules, err := References(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("References(%s) error: %v", archive, err)
	}
	held := &sharedAnalysis{
		symbols:       symbols,
		refs:          refs,
		rules:         rules,
		roots:         make(map[bool][]Root),
		unmatched:     make(map[bool][]Unmatched),
		excludedByCgo: result.ExcludedByCgo,
	}
	for _, published := range []bool{false, true} {
		opts := RootOptions{Patterns: patterns, PublishedAPI: published}
		held.roots[published], held.unmatched[published], err = Roots(result, root, os.ReadFile, symbols, opts)
		if err != nil {
			t.Fatalf("Roots(%s, %+v) = _, _, %v, want no error", archive, opts, err)
		}
	}
	return held
}

// inventory is the declarations the archive's load enumerated.
func (a *sharedAnalysis) inventory(t *testing.T) []Symbol {
	t.Helper()

	return testsupport.Detached(t, a.symbols)
}

// references is the references the reference pass walked over the inventory.
func (a *sharedAnalysis) references(t *testing.T) []Reference {
	t.Helper()

	return testsupport.Detached(t, a.refs)
}

// testFileRules is the rule that classified each test file, as the reference pass
// reported them.
func (a *sharedAnalysis) testFileRules(t *testing.T) []TestFileRule {
	t.Helper()

	return testsupport.Detached(t, a.rules)
}

// rootsUnder is the roots and the unmatched patterns one setting of the
// published-API seed detected.
func (a *sharedAnalysis) rootsUnder(t *testing.T, published bool) ([]Root, []Unmatched) {
	t.Helper()

	return testsupport.Detached(t, a.roots[published]), testsupport.Detached(t, a.unmatched[published])
}

// excludedFiles is the files the load excluded for importing C.
func (a *sharedAnalysis) excludedFiles(t *testing.T) []string {
	t.Helper()

	return testsupport.Detached(t, a.excludedByCgo)
}

// configured is the three passes a matrix merges, under one set of root options.
func (a *sharedAnalysis) configured(t *testing.T, published bool) Configured {
	t.Helper()

	roots, _ := a.rootsUnder(t, published)
	return Configured{Symbols: a.inventory(t), References: a.references(t), Roots: roots}
}
