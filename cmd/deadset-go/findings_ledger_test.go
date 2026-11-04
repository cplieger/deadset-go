package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// partsAt is every reported finding of one code, as its line and subject.
func partsAt(t *testing.T, dir string, code vocab.Code) []string {
	t.Helper()

	var held []string
	for _, found := range findingsOfDir(t, dir).Result.Findings {
		if found.Code == code {
			held = append(held, found.Position.Path+":"+strconv.Itoa(found.Position.Line)+" "+found.Symbol.Ref)
		}
	}
	return held
}

func TestAPartOneConfigurationOfTwoFindsIsNotReported(t *testing.T) {
	dir := archiveModule(t, "findings-ledger/parts-per-configuration.txtar")

	if got, want := partsAt(t, dir, vocab.UnreachableCase), []string{"main.go:25 go://example.com/app#always"}; !slices.Equal(got, want) {
		t.Errorf("unreachable cases = %q, want %q: the case of describe is reachable under windows", got, want)
	}
	if got, want := partsAt(t, dir, vocab.DeadStore), []string{"main.go:41 go://example.com/app#twice"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: the store to c in tally is read through bump's pointer under linux", got, want)
	}
}

func TestATypeErrorSkipsOnlyTheDeclarationWhoseSourceHoldsIt(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "findings-ledger/skip-by-declaration.txtar"))

	wantNoFinding(t, about, "the type error skips Broken", "go://example.com/app#Broken")
	wantFinding(t, about, "go://example.com/app#helper", vocab.UnusedUnexported, vocab.Certain)
}

func TestMintedComponentsAreNumberedInTheReportsOrder(t *testing.T) {
	set := findingsOfDir(t, archiveModule(t, "findings-ledger/minted-in-report-order.txtar"))

	var got []string
	for _, found := range set.Result.Findings {
		got = append(got, found.Symbol.Ref+" "+found.Component.ID)
	}
	want := []string{
		"go://example.com/app#early(unused) deadset-go/c-1",
		"go://example.com/app#Late deadset-go/c-2",
		"go://example.com/app#later(unused) deadset-go/c-3",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
}

// reportedAt is every finding the run reports or withholds, as its code and line.
func reportedAt(t *testing.T, fixture string) []string {
	t.Helper()

	set := findingsOfDir(t, archiveModule(t, fixture))
	var got []string
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		got = append(got, string(found.Code)+" "+found.Position.Path+":"+strconv.Itoa(found.Position.Line))
	}
	return got
}

func TestARecordOnADeclarationATypeErrorSkipsIsStaleWhenNoFindingNamesIt(t *testing.T) {
	if got, want := reportedAt(t, "findings-ledger/skip-record.txtar"), []string{"DS9003 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the type error roots broken, so no kind finds it unused", got, want)
	}
}

func TestARecordNamingOnlyASkippedFindingIsNeitherInEffectNorStale(t *testing.T) {
	const fixture = "findings-ledger/skip-record-held.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: each record names a finding the type error skips", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 0 {
		t.Errorf("suppressions in effect = %d, want 0: a skipped finding is not evaluated", set.Result.InEffect)
	}
}

func TestARecordNamingNoKindsCodeIsStaleOnADeclarationATypeErrorSkips(t *testing.T) {
	if got, want := reportedAt(t, "findings-ledger/skip-record-unknown-code.txtar"), []string{"DS9003 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: a record naming DS1999 matches nothing whatever the type error hides", got, want)
	}
}

func TestARecordNoKindReportsAboutASkippedDeclarationIsStale(t *testing.T) {
	want := []string{
		"DS9003 lib/lib.go:5", "DS9003 lib/lib.go:8", "DS9003 main.go:7", "DS9003 main.go:12", "DS9003 main.go:15",
		"DS9003 main.go:18", "DS9003 main.go:21", "DS9003 main.go:24", "DS9003 main.go:27", "DS9003 main.go:30",
	}
	if got := reportedAt(t, "findings-ledger/skip-record-population.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
}

func TestARecordNamingAnotherShapeIsStaleOnADeclarationATypeErrorSkips(t *testing.T) {
	if got, want := reportedAt(t, "findings-ledger/skip-record-shape-code.txtar"), []string{"DS9003 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: no finding about unexported broken carries DS1001", got, want)
	}
	if got, want := reportedAt(t, "findings-ledger/skip-record-wrong-shape.txtar"), []string{"DS9003 lib/lib.go:3", "DS9003 lib/lib.go:6"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: no finding about exported Broken carries DS1002, and the type error roots Kept", got, want)
	}
}

func TestARecordOnAUsedDeclarationIsStaleWhenATypeErrorSkipsIt(t *testing.T) {
	if got, want := reportedAt(t, "findings-ledger/skip-record-used.txtar"), []string{"DS9003 lib/lib.go:3"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: main calls Broken outside its body", got, want)
	}
}

func TestARecordNoFindingAboutAFunctionCanCarryIsStaleWhenATypeErrorSkipsIt(t *testing.T) {
	want := []string{
		"DS9003 main.go:5", "DS9003 main.go:8", "DS9003 main.go:11", "DS9003 main.go:14",
		"DS9003 main.go:17", "DS9003 main.go:20", "DS9003 main.go:23",
	}
	if got := reportedAt(t, "findings-ledger/skip-record-other-subject.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
}

func TestARecordForADependencyKindKeepsNothingLive(t *testing.T) {
	if got, want := reportedAt(t, "findings-ledger/dependency-record-marks-nothing.txtar"), []string{"DS9003 main.go:5", "DS1002 main.go:6"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: a DS1701 record marks nothing live, so helper is reported beside the stale record", got, want)
	}
}

func TestARecordWhoseFindingTheMinimumWithholdsIsDormant(t *testing.T) {
	set := findingsOfDir(t, archiveModule(t, "findings-ledger/dormant-filled.txtar"))

	if got := set.Result.InEffect; got != 0 {
		t.Errorf("suppressions in effect = %d, want 0: the finding the directive on request.Extra matches is possible, which the default minimum withholds", got)
	}
}

func TestAPartUnderALineDirectiveIsReportedWhereTheDirectivePutsIt(t *testing.T) {
	dir := archiveModule(t, "findings-ledger/part-under-a-line-directive.txtar")

	want := []string{"main.go:5 go://example.com/app#plain(unused)", "shadow.txt:3 go://example.com/app#shifted(also)"}
	if got := partsAt(t, dir, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("unused parameters = %q, want %q: the one configuration compiled main.go and found both", got, want)
	}
}

func TestATypeErrorSkipsNoRefusalOfAnEntryNamingTheDeclaration(t *testing.T) {
	want := []string{"DS9001 deadset-ignore.json:2", "DS9002 deadset-ignore.json:3", "DS9003 deadset-ignore.json:4"}
	if got := reportedAt(t, "findings-ledger/skipped-declaration-entries.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: a refused entry is a row of the ignore file, and the complete entry matches no finding", got, want)
	}
}

func TestANameLiteralUnderALineDirectiveNamingATestFileStillLowers(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "findings-ledger/name-literal-under-a-line-directive.txtar"))

	wantFinding(t, about, "go://example.com/app#helper", vocab.UnusedUnexported, vocab.Possible)
}

func TestATypeErrorInOneInitSkipsThatInitsPartsAlone(t *testing.T) {
	if got := reportedAt(t, "findings-ledger/skip-init-later.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: the dead store is in the init the type error skips", got)
	}
	if got, want := reportedAt(t, "findings-ledger/skip-init-first.txtar"), []string{"DS1404 main.go:10"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the dead store is in the init that type-checks", got, want)
	}
	if got := reportedAt(t, "findings-ledger/skip-blank-function.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: the type error skips the blank function", got)
	}
}

func TestARecordOnAPartOfTheHealthyInitIsInEffect(t *testing.T) {
	const fixture = "findings-ledger/skip-init-record.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: the record withholds the healthy init's dead store", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 1 {
		t.Errorf("suppressions in effect = %d, want 1", set.Result.InEffect)
	}
}

func TestARecordWhoseMarkWithholdsASkippedMembersFindingIsNeitherInEffectNorStale(t *testing.T) {
	const fixture = "findings-ledger/skip-record-member.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: the type error skips broken and its field y", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 0 {
		t.Errorf("suppressions in effect = %d, want 0: the only finding the record names is about a skipped declaration", set.Result.InEffect)
	}
}

func TestARecordOnOneInitWithholdsThatInitsPartsAlone(t *testing.T) {
	const fixture = "findings-ledger/record-on-one-init.txtar"
	if got, want := reportedAt(t, fixture), []string{"DS1404 main.go:6"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the directive binds the second init, not the first", got, want)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 1 {
		t.Errorf("suppressions in effect = %d, want 1", set.Result.InEffect)
	}
}

func TestAnEntryNamingTwoInitsIsARecordInEffectPerInit(t *testing.T) {
	const fixture = "findings-ledger/entry-on-two-inits.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: each init's record withholds its store", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 2 {
		t.Errorf("suppressions in effect = %d, want 2: the entry binds two inits, one record each", set.Result.InEffect)
	}
}

func TestAPartHoldsUnderTheConfigurationsOfItsOwnDeclaration(t *testing.T) {
	set := findingsOfDir(t, archiveModule(t, "findings-ledger/init-per-configuration.txtar"))

	var got []string
	for _, found := range set.Result.Findings {
		got = append(got, string(found.Code)+" "+found.Position.Path+" "+strings.Join(found.Configurations, ","))
	}
	if want := []string{"DS1404 b_windows.go windows-amd64"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: only windows compiles the init that holds the store", got, want)
	}
}

func TestBlankFunctionsAreNoPlatformTwins(t *testing.T) {
	dir := archiveModule(t, "findings-ledger/blank-functions-are-no-twins.txtar")

	want := []string{"main.go:5 go://example.com/app#(unused)", "other.go:3 go://example.com/app#(spare)"}
	if got := partsAt(t, dir, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("unused parameters = %q, want %q: nothing calls either blank function", got, want)
	}
}

// storesAt is every reported dead store, as its line and the variable stored to.
func storesAt(t *testing.T, fixture string) []string {
	t.Helper()

	var got []string
	for _, found := range findingsOfDir(t, archiveModule(t, fixture)).Result.Findings {
		if found.Code == vocab.DeadStore {
			got = append(got, found.Position.Path+":"+strconv.Itoa(found.Position.Line)+" "+found.Symbol.Name)
		}
	}
	return got
}

func TestPartsALineDirectiveRendersAtOnePositionAreEachReported(t *testing.T) {
	if got, want := storesAt(t, "findings-ledger/line-folded-inits.txtar"), []string{"main.go:4 v", "main.go:4 w"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: each init writes its own store", got, want)
	}
	if got, want := storesAt(t, "findings-ledger/line-folded-inits-skip.txtar"), []string{"main.go:4 w"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: the type error skips the first init alone", got, want)
	}
}
