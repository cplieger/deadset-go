package main

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
)

// The shape a documentation entry takes, pinned here so that a page and this test
// agree on one form rather than on whatever prose happens to mention a code.
//
// A kind's entry is a section heading opening with the code, or a table row whose
// first cell is the code and nothing else: a kind a run reports carries a section,
// and a kind another product reports carries a row saying which product. A class's
// entry is a section heading naming the class. The contract version is the one a
// sentence states in bold beside the words that name it.
//
// No form reads the prose under it, so a page is reorganised, rewritten or split
// without touching this test, and only the removal of an entry fails it.
var (
	docsKindHeading     = regexp.MustCompile(`(?m)^### (DS[0-9]{4})\b`)
	docsKindRow         = regexp.MustCompile("(?m)^\\| `(DS[0-9]{4})` \\|")
	docsClassHeading    = regexp.MustCompile(`(?m)^### ([a-z][a-z0-9]*(?:-[a-z0-9]+)*)$`)
	docsContractVersion = regexp.MustCompile(`(?is)contract version\s+\*\*([0-9]+\.[0-9]+\.[0-9]+)\*\*`)
	docsCorpusVersion   = regexp.MustCompile(`(?is)corpus version this analyzer answers is\s+\*\*([0-9]+\.[0-9]+\.[0-9]+)\*\*`)
	docsFixtureCount    = regexp.MustCompile(`(?is)the run answers the\s+\*\*([0-9]+)\*\* fixtures`)
)

// TestEveryReportedKindHasADocumentationEntry reads the kinds page and refuses a
// code this command answers that the page says nothing about.
//
// The population is the composition root's own emitters table plus the kinds that
// table names as another product's, which is every code a reader of this analyzer's
// documentation can meet: one in a report it wrote, and one in a merged report the
// edge evaluations it published produced.
//
// A code this analyzer reports needs a section, because what a run does under it is
// what the page exists to state; a code another product reports needs an entry of
// either form, a row saying which product being the whole answer.
func TestEveryReportedKindHasADocumentationEntry(t *testing.T) {
	t.Parallel()

	page := docPage(t, "kinds.md")
	sections := matched(docsKindHeading, page)
	for _, code := range slices.Sorted(maps.Keys(emitters)) {
		if !slices.Contains(sections, code) {
			t.Errorf("docs/kinds.md carries %d kind sections and none for %s, which the emitters table reports",
				len(sections), code)
		}
	}
	entries := docsKindEntries(page)
	for _, code := range goKindsWithoutEmitter {
		if !slices.Contains(entries, code) {
			t.Errorf("docs/kinds.md carries %d kind entries and none for %s, which another product reports",
				len(entries), code)
		}
	}
}

// TestEveryDocumentedKindSectionIsOneThisAnalyzerReports is the other direction: a
// section of the kinds page states what a run does under one code, so a section for
// a code no emitter answers documents a kind this analyzer does not report.
//
// A code this analyzer does not report is a table row instead, which carries which
// product reports it or why it is absent, so the two forms are what separates the
// two claims.
func TestEveryDocumentedKindSectionIsOneThisAnalyzerReports(t *testing.T) {
	t.Parallel()

	sections := matched(docsKindHeading, docPage(t, "kinds.md"))
	for _, code := range sections {
		if emitters[code] == nil {
			t.Errorf("docs/kinds.md carries a section for %s, want a section only for a code the emitters table reports: %v",
				code, slices.Sorted(maps.Keys(emitters)))
		}
	}
}

// TestEveryExemptionClassHasADocumentationEntry reads the exemptions page and
// refuses any difference from the classes this command computes.
//
// The two directions are one assertion because the detectors table is the whole of
// what this analyzer computes: a class absent from the page is an exemption a
// maintainer cannot look up, and a class the page describes that the table does not
// hold is a page promising an exemption no run applies.
func TestEveryExemptionClassHasADocumentationEntry(t *testing.T) {
	t.Parallel()

	documented := matched(docsClassHeading, docPage(t, "exemptions.md"))
	computed := slices.Sorted(maps.Keys(detectors))
	want := make([]string, 0, len(computed))
	for _, class := range computed {
		want = append(want, string(class))
	}
	if !slices.Equal(documented, want) {
		t.Errorf("docs/exemptions.md documents the classes %v, want the %d the detectors table computes: %v",
			documented, len(want), want)
	}
}

// TestEveryDeclaredGapIsDocumented reads the committed declared-gap file and
// refuses a capability the conformance page does not name.
//
// A gap is a capability this analyzer declines, so it is the one thing about the
// corpus run a reader needs and the page cannot derive: the file is the run's
// answer and the page is what is read instead of it.
func TestEveryDeclaredGapIsDocumented(t *testing.T) {
	t.Parallel()

	gaps, declared := docsDeclaredGaps(t)
	if !declared {
		t.Log("no declared-gap file is committed yet, so no gap is asserted against docs/conformance.md")
		return
	}
	page := docPage(t, "conformance.md")
	for _, gap := range gaps {
		if !strings.Contains(page, gap.Capability) {
			t.Errorf("docs/conformance.md names no gap %s, which conformance.json declares for the fixture %s",
				gap.Capability, gap.Fixture)
		}
	}
}

// TestTheDocumentedContractVersionIsTheOneImplemented refuses a conformance page
// stating a contract version other than the one this analyzer implements.
//
// The version is what a reader of the page pins against, and it is stated in two
// places that move at different times: a resolved configuration names the one the
// code implements, and the page names the one it was written for.
func TestTheDocumentedContractVersionIsTheOneImplemented(t *testing.T) {
	t.Parallel()

	stated := matched(docsContractVersion, docPage(t, "conformance.md"))
	if len(stated) != 1 {
		t.Fatalf("docs/conformance.md states the contract version %v, want exactly one statement of it", stated)
	}
	if stated[0] != config.ContractVersion {
		t.Errorf("docs/conformance.md states contract version %s, want %s, which this analyzer implements",
			stated[0], config.ContractVersion)
	}
}

// TestTheDocumentedCorpusVersionIsTheOneAnswered refuses a conformance page stating
// a corpus version other than the one this analyzer answers.
//
// It is the second version the page carries, and the one a reader compares a
// published corpus against: a gap declared against one major says nothing about
// another, so a page naming the wrong one misdescribes every gap under it.
func TestTheDocumentedCorpusVersionIsTheOneAnswered(t *testing.T) {
	t.Parallel()

	stated := matched(docsCorpusVersion, docPage(t, "conformance.md"))
	if len(stated) != 1 {
		t.Fatalf("docs/conformance.md states the corpus version %v, want exactly one statement of it", stated)
	}
	if stated[0] != corpusVersion {
		t.Errorf("docs/conformance.md states corpus version %s, want %s, which this analyzer answers",
			stated[0], corpusVersion)
	}
}

// TestTheDocumentedFixtureCountIsTheOneAnswered refuses a conformance page stating a
// fixture count other than the one the committed results document records.
//
// The count is what a reader compares a published corpus against, and it moves every
// time the corpus adds a fixture carrying a Go rendering, which is a release the page
// and the record move in together.
func TestTheDocumentedFixtureCountIsTheOneAnswered(t *testing.T) {
	t.Parallel()

	stated := matched(docsFixtureCount, docPage(t, "conformance.md"))
	if len(stated) != 1 {
		t.Fatalf("docs/conformance.md states the fixture count %v, want exactly one statement of it", stated)
	}
	var document struct {
		Totals struct {
			Fixtures int `json:"fixtures"`
		} `json:"totals"`
	}
	path := filepath.Join("..", "..", conformanceResultsFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", path, err)
	}
	if want := strconv.Itoa(document.Totals.Fixtures); stated[0] != want {
		t.Errorf("docs/conformance.md states %s fixtures, want %s, which %s records",
			stated[0], want, conformanceResultsFile)
	}
}

// docPage is the text of one page under docs. A test binary runs in its own
// package's directory, so the pages sit two levels above this one.
func docPage(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("..", "..", "docs", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	return string(body)
}

// docsKindEntries is every code the kinds page carries an entry for, in ascending
// order and once each, whichever of the two forms the entry takes.
func docsKindEntries(page string) []string {
	found := append(matched(docsKindHeading, page), matched(docsKindRow, page)...)
	slices.Sort(found)
	return slices.Compact(found)
}

// matched is the first capture of every match of one expression, in ascending order
// and once each, so a caller compares sets rather than the order a page happens to
// write them in.
func matched(expression *regexp.Regexp, page string) []string {
	var found []string
	for _, match := range expression.FindAllStringSubmatch(page, -1) {
		found = append(found, match[1])
	}
	slices.Sort(found)
	return slices.Compact(found)
}

// declaredGap is one row of the committed declared-gap file: the capability this
// analyzer declines, and the fixture it is declined for.
type declaredGap struct {
	Capability string `json:"capability"`
	Fixture    string `json:"fixture"`
}

// docsDeclaredGaps reads the declared-gap file at the repository root and returns
// its rows, and whether the file is there at all.
//
// The file is the conformance run's to write and this page's to describe, and the
// two land in either order, so an absent file answers false rather than failing: the
// assertion above binds from the moment the file exists.
func docsDeclaredGaps(t *testing.T) ([]declaredGap, bool) {
	t.Helper()

	// The name the run commits the declarations under, at the repository root
	// beside the module file.
	path := filepath.Join("..", "..", "conformance.json")
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	var document struct {
		Gaps []declaredGap `json:"gaps"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", path, err)
	}
	return document.Gaps, true
}
