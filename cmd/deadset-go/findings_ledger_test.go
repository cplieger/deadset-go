package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
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
	t.Parallel()
	dir := archiveModule(t, "findings-ledger/parts-per-configuration.txtar")

	if got, want := partsAt(t, dir, vocab.UnreachableCase), []string{"main.go:25 go://example.com/app#always"}; !slices.Equal(got, want) {
		t.Errorf("unreachable cases = %q, want %q: the case of describe is reachable under windows", got, want)
	}
	if got, want := partsAt(t, dir, vocab.DeadStore), []string{"main.go:41 go://example.com/app#twice"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: the store to c in tally is read through bump's pointer under linux", got, want)
	}
}

func TestATypeErrorSkipsOnlyTheDeclarationWhoseSourceHoldsIt(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "findings-ledger/skip-by-declaration.txtar"))

	wantNoFinding(t, about, "the type error skips Broken", "go://example.com/app#Broken")
	wantFinding(t, about, "go://example.com/app#helper", vocab.UnusedUnexported, vocab.Certain)
}

func TestMintedComponentsAreNumberedInTheReportsOrder(t *testing.T) {
	t.Parallel()
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

// reportedAt is every finding the run reports or withholds, then every stale
// suppression, as its code and line.
func reportedAt(t *testing.T, fixture string) []string {
	t.Helper()

	set := findingsOfDir(t, archiveModule(t, fixture))
	var got []string
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		got = append(got, string(found.Code)+" "+found.Position.Path+":"+strconv.Itoa(found.Position.Line))
	}
	for _, stale := range set.Result.Stale {
		got = append(got, string(stale.Code)+" "+stale.Position.Path+":"+strconv.Itoa(stale.Position.Line))
	}
	return got
}

func TestATestEntryPointAnotherTestCallsIsNoUnnecessaryExport(t *testing.T) {
	t.Parallel()
	want := []string{"DS1501 internal/p/p.go:7"}
	if got := reportedAt(t, "findings-ledger/test-entry-points-another-test-calls.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the go test driver runs each entry point by its exported name", got, want)
	}
}

// A package below an underscore directory and one below testdata are both skipped
// by the go tool's wildcard, and a test that imports one compiles it as test code
// alike.
func TestAPackageOnlyATestImportsIsTestCodeBelowAnUnderscoreAsBelowTestdata(t *testing.T) {
	t.Parallel()
	want := []string{"DS1102 p/p.go:8"}
	for _, fixture := range []string{"underscored-package-only-a-test-imports", "testdata-package-only-a-test-imports"} {
		if got := reportedAt(t, "findings-ledger/"+fixture+".txtar"); !slices.Equal(got, want) {
			t.Errorf("%s: findings = %q, want %q: only the test's build compiles x", fixture, got, want)
		}
	}
}

// An assertion in one module of a type another module hands out holds the methods it
// reaches, whichever module's reading of their package each type and interface is.
func TestAnAssertionOfAnotherModulesTypeHoldsWhatItReachesAcrossTheTwoReadings(t *testing.T) {
	t.Parallel()
	if got := reportedAt(t, "findings-ledger/derived-conversion-across-two-readings.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: b's assertions reach every method they name", got)
	}
}

// The site an assertion derives names the interface the assertion writes, so an
// encoder its module calls finds the asserted value's type among the types converted
// to it.
func TestAValueAssertedAcrossTheTwoReadingsAndEncodedHoldsTheFieldsTheEncoderReads(t *testing.T) {
	t.Parallel()
	if got := reportedAt(t, "findings-ledger/derived-conversion-encoded-across-two-readings.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: json.Marshal reads Square.Secret through b's Namer", got)
	}
}

func TestAValueAssertedAcrossTheTwoReadingsAndEncodedInTheTypesReadingHoldsTheFieldsTheEncoderReads(t *testing.T) {
	t.Parallel()
	if got := reportedAt(t, "findings-ledger/derived-conversion-encoded-in-the-types-reading.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: m's json.Marshal reads Square.Secret through the Namer b asserts", got)
	}
}

func TestAnAssertedMethodVariadicInATypeParameterKeepsTheInterfaceItWrites(t *testing.T) {
	t.Parallel()
	if got := reportedAt(t, "findings-ledger/assertion-of-a-variadic-type-parameter-method.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: every declaration is used", got)
	}
}

func TestARecordOnADeclarationATypeErrorSkipsIsStaleWhenNoFindingNamesIt(t *testing.T) {
	t.Parallel()
	if got, want := reportedAt(t, "findings-ledger/skip-record.txtar"), []string{"DS9003 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the type error roots broken, so no kind finds it unused", got, want)
	}
}

func TestARecordNamingOnlyASkippedFindingIsNeitherInEffectNorStale(t *testing.T) {
	t.Parallel()
	const fixture = "findings-ledger/skip-record-held.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: each record names a finding the type error skips", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 0 {
		t.Errorf("suppressions in effect = %d, want 0: a skipped finding is not evaluated", set.Result.InEffect)
	}
}

func TestARecordNamingNoKindsCodeIsStaleOnADeclarationATypeErrorSkips(t *testing.T) {
	t.Parallel()
	if got, want := reportedAt(t, "findings-ledger/skip-record-unknown-code.txtar"), []string{"DS9003 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: a record naming DS1999 matches nothing whatever the type error hides", got, want)
	}
}

func TestARecordNoKindReportsAboutASkippedDeclarationIsStale(t *testing.T) {
	t.Parallel()
	want := []string{
		"DS9003 lib/lib.go:5", "DS9003 lib/lib.go:8", "DS9003 main.go:7", "DS9003 main.go:12", "DS9003 main.go:15",
		"DS9003 main.go:18", "DS9003 main.go:21", "DS9003 main.go:24", "DS9003 main.go:27", "DS9003 main.go:30",
	}
	if got := reportedAt(t, "findings-ledger/skip-record-population.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
}

func TestARecordNamingAnotherShapeIsStaleOnADeclarationATypeErrorSkips(t *testing.T) {
	t.Parallel()
	if got, want := reportedAt(t, "findings-ledger/skip-record-shape-code.txtar"), []string{"DS9003 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: no finding about unexported broken carries DS1001", got, want)
	}
	if got, want := reportedAt(t, "findings-ledger/skip-record-wrong-shape.txtar"), []string{"DS9003 lib/lib.go:3", "DS9003 lib/lib.go:6"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: no finding about exported Broken carries DS1002, and the type error roots Kept", got, want)
	}
}

func TestARecordOnAUsedDeclarationIsStaleWhenATypeErrorSkipsIt(t *testing.T) {
	t.Parallel()
	if got, want := reportedAt(t, "findings-ledger/skip-record-used.txtar"), []string{"DS9003 lib/lib.go:3"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: main calls Broken outside its body", got, want)
	}
}

func TestARecordNoFindingAboutAFunctionCanCarryIsStaleWhenATypeErrorSkipsIt(t *testing.T) {
	t.Parallel()
	want := []string{
		"DS9003 main.go:5", "DS9003 main.go:8", "DS9003 main.go:11", "DS9003 main.go:14",
		"DS9003 main.go:17", "DS9003 main.go:20", "DS9003 main.go:23",
	}
	if got := reportedAt(t, "findings-ledger/skip-record-other-subject.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
}

func TestARecordForADependencyKindKeepsNothingLive(t *testing.T) {
	t.Parallel()
	if got, want := reportedAt(t, "findings-ledger/dependency-record-marks-nothing.txtar"), []string{"DS1002 main.go:6", "DS9003 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: a DS1701 record marks nothing live, so helper is reported beside the stale record", got, want)
	}
}

func TestARecordWhoseFindingTheMinimumWithholdsIsDormant(t *testing.T) {
	t.Parallel()
	set := findingsOfDir(t, archiveModule(t, "findings-ledger/dormant-filled.txtar"))

	if got := set.Result.InEffect; got != 0 {
		t.Errorf("suppressions in effect = %d, want 0: the finding the directive on request.Extra matches is possible, which the default minimum withholds", got)
	}
}

func TestAPartUnderALineDirectiveIsReportedWhereTheDirectivePutsIt(t *testing.T) {
	t.Parallel()
	dir := archiveModule(t, "findings-ledger/part-under-a-line-directive.txtar")

	want := []string{"main.go:5 go://example.com/app#plain(unused)", "shadow.txt:3 go://example.com/app#shifted(also)"}
	if got := partsAt(t, dir, vocab.UnusedParameter); !slices.Equal(got, want) {
		t.Errorf("unused parameters = %q, want %q: the one configuration compiled main.go and found both", got, want)
	}
}

func TestATypeErrorSkipsNoRefusalOfAnEntryNamingTheDeclaration(t *testing.T) {
	t.Parallel()
	want := []string{"DS9001 deadset-ignore.json:2", "DS9002 deadset-ignore.json:3", "DS9003 deadset-ignore.json:4"}
	if got := reportedAt(t, "findings-ledger/skipped-declaration-entries.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: a refused entry is a row of the ignore file, and the complete entry matches no finding", got, want)
	}
}

func TestANameLiteralUnderALineDirectiveNamingATestFileStillLowers(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "findings-ledger/name-literal-under-a-line-directive.txtar"))

	wantFinding(t, about, "go://example.com/app#helper", vocab.UnusedUnexported, vocab.Possible)
}

func TestEachOfTwoLiteralsALineDirectiveRendersAtOnePositionLowers(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "findings-ledger/literal-fold.txtar"))

	wantFinding(t, about, "go://example.com/app#alpha", vocab.UnusedUnexported, vocab.Possible)
	wantFinding(t, about, "go://example.com/app#bravo", vocab.UnusedUnexported, vocab.Possible)
}

func TestEachOfTwoWritesALineDirectiveRendersAtOnePositionCounts(t *testing.T) {
	t.Parallel()
	found := findingsAbout(t, archiveModule(t, "findings-ledger/write-fold.txtar"))["go://example.com/app#settings.level"]
	if found == nil {
		t.Fatal("no finding about settings.level")
	}
	if !strings.Contains(found.Message, "is written at 2 positions") || len(found.Details.WritePositions) != 2 {
		t.Errorf("message %q with write positions %v, want two writes", found.Message, found.Details.WritePositions)
	}
}

func TestAColumnAfterAnInlineLineDirectiveCountsOnlyTheBytesAfterIt(t *testing.T) {
	t.Parallel()
	found := findingsAbout(t, archiveModule(t, "findings-ledger/column-after-an-inline-line-directive.txtar"))["go://example.com/app#t"]
	if found == nil {
		t.Fatal("no finding about t")
	}
	if got := found.Position.Path + ":" + strconv.Itoa(found.Position.Line) + ":" + strconv.Itoa(found.Position.Column); got != "main.go:30:45" {
		t.Errorf("t is reported at %s, want main.go:30:45", got)
	}
}

func TestATypeErrorInOneInitSkipsThatInitsPartsAlone(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	const fixture = "findings-ledger/skip-init-record.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: the record withholds the healthy init's dead store", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 1 {
		t.Errorf("suppressions in effect = %d, want 1", set.Result.InEffect)
	}
}

func TestARecordWhoseMarkWithholdsASkippedMembersFindingIsNeitherInEffectNorStale(t *testing.T) {
	t.Parallel()
	const fixture = "findings-ledger/skip-record-member.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: the type error skips broken and its field y", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 0 {
		t.Errorf("suppressions in effect = %d, want 0: the only finding the record names is about a skipped declaration", set.Result.InEffect)
	}
}

func TestARecordOnOneInitWithholdsThatInitsPartsAlone(t *testing.T) {
	t.Parallel()
	const fixture = "findings-ledger/record-on-one-init.txtar"
	if got, want := reportedAt(t, fixture), []string{"DS1404 main.go:6"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the directive binds the second init, not the first", got, want)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 1 {
		t.Errorf("suppressions in effect = %d, want 1", set.Result.InEffect)
	}
}

func TestAnEntryNamingTwoInitsIsARecordInEffectPerInit(t *testing.T) {
	t.Parallel()
	const fixture = "findings-ledger/entry-on-two-inits.txtar"
	if got := reportedAt(t, fixture); len(got) != 0 {
		t.Errorf("findings = %q, want none: each init's record withholds its store", got)
	}
	if set := findingsOfDir(t, archiveModule(t, fixture)); set.Result.InEffect != 2 {
		t.Errorf("suppressions in effect = %d, want 2: the entry binds two inits, one record each", set.Result.InEffect)
	}
}

func TestAPartHoldsUnderTheConfigurationsOfItsOwnDeclaration(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	if got, want := storesAt(t, "findings-ledger/line-folded-inits.txtar"), []string{"main.go:4 v", "main.go:4 w"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: each init writes its own store", got, want)
	}
	if got, want := storesAt(t, "findings-ledger/line-folded-inits-skip.txtar"), []string{"main.go:4 w"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: the type error skips the first init alone", got, want)
	}
	const onto = "findings-ledger/line-folded-onto-a-declaration.txtar"
	if got, want := reportedAt(t, onto), []string{"DS1002 main.go:5", "DS1404 main.go:5"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the store and the function it is rendered onto are two subjects", got, want)
	}
	if got, want := storesAt(t, onto), []string{"main.go:5 v"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q", got, want)
	}
	if got, want := storesAt(t, "findings-ledger/equal-offset.txtar"), []string{"a.go:5 v", "a.go:5 w"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: the two inits are written in two files", got, want)
	}
	if got, want := storesAt(t, "findings-ledger/equal-offset-skip.txtar"), []string{"a.go:5 w"}; !slices.Equal(got, want) {
		t.Errorf("dead stores = %q, want %q: the type error skips a.go's init alone", got, want)
	}
}

// subjectsAt is every finding the run reports or withholds, as its code, line and
// reference, sorted.
func subjectsAt(t *testing.T, fixture string) []string {
	t.Helper()

	set := findingsOfDir(t, archiveModule(t, fixture))
	var got []string
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		got = append(got, string(found.Code)+" "+found.Position.Path+":"+strconv.Itoa(found.Position.Line)+" "+found.Symbol.Ref)
	}
	slices.Sort(got)
	return got
}

func TestDeclarationsALineDirectiveRendersAtOnePositionAreEachReported(t *testing.T) {
	t.Parallel()
	want := []string{"DS1002 main.go:20 go://example.com/app#alpha", "DS1002 main.go:20 go://example.com/app#bravo"}
	if got := subjectsAt(t, "findings-ledger/decl-fold.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
	want = append(want, "DS1404 main.go:21 go://example.com/app#alpha", "DS1404 main.go:21 go://example.com/app#bravo")
	if got := subjectsAt(t, "findings-ledger/decl-fold-two-files.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: each function holds its own store", got, want)
	}
}

func TestADependencysFunctionALineDirectiveRendersOntoAnothersLineKeepsItsOwnBody(t *testing.T) {
	t.Parallel()
	want := []string{"DS1101 app.go:9 go://example.com/app#settings.String"}
	if got := subjectsAt(t, "findings-ledger/decoder-fold.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: Load only decodes, whatever Show's body does", got, want)
	}
}

func TestATypeErrorALineDirectiveRendersOutsideTheTreeIsSkippedWhereItIsWritten(t *testing.T) {
	t.Parallel()
	set := findingsOfDir(t, archiveModule(t, "findings-ledger/skip-under-a-line-directive-outside-the-tree.txtar"))
	var got []string
	for _, skip := range set.TypeErrorSkips {
		got = append(got, skip.Path+":"+strconv.Itoa(skip.Line)+" "+strconv.Itoa(len(skip.Declarations)))
	}
	if want := []string{"main.go:6 1"}; !slices.Equal(got, want) {
		t.Errorf("type error skips (position, declarations) = %q, want %q", got, want)
	}
}

func TestADeclarationIsOfThePackageThatWritesIt(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, filepath.Join(archiveModule(t, "findings-ledger/package-of-a-folded-declaration.txtar"), "target"))

	wantFinding(t, about, "go://example.com/app#Exported", vocab.UnreachableExport, vocab.Certain)
}

func TestALineDirectiveNamingATestFileMakesNoProductionCodeATest(t *testing.T) {
	t.Parallel()
	want := []string{"DS1102 helper_test.go:3", "DS1103 main_test.go:5"}
	if got := reportedAt(t, "findings-ledger/test-file-under-a-line-directive.txtar"); !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: helper is written in main.go", got, want)
	}
	if got, want := reportedAt(t, "findings-ledger/test-evidence-under-a-line-directive.txtar"), []string{"DS1402 main.go:7"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: main converts the error in a production file", got, want)
	}
}

// renderedAt is every reported finding as its code and full rendered position.
func renderedAt(t *testing.T, fixture string) []string {
	t.Helper()

	var got []string
	for _, found := range findingsOfDir(t, archiveModule(t, fixture)).Result.Findings {
		got = append(got, string(found.Code)+" "+positionKey(&found.Position))
	}
	return got
}

func TestALineDirectiveRendersAPositionAndMovesNothingElse(t *testing.T) {
	t.Parallel()
	if got := renderedAt(t, "findings-ledger/generated-file-under-a-line-directive.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: yyUnused is written in a generated file", got)
	}
	if got, want := renderedAt(t, "findings-ledger/line-directive-naming-a-missing-file.txtar"), []string{"DS1002 grammar.y:10:6"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the directive renders the position, main.go's line counts the column", got, want)
	}
	if got, want := renderedAt(t, "findings-ledger/line-directive-naming-a-file-outside-the-tree.txtar"), []string{"DS1002 main.go:6:6"}; !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: no report can name a file outside the tree", got, want)
	}
}

// scopedReport is every finding a run over one archive reports, the archive's
// scope.json naming the target and its consumers.
func scopedReport(t *testing.T, fixture string) []string {
	t.Helper()

	reportPath := filepath.Join(sandbox.Dir(t), "report.json")
	ran := invokedIn(t, archiveModule(t, fixture),
		"analyze", "--scope=scope.json", "--config=target/"+repositoryDocument, "--report="+reportPath)
	if ran.code != vocab.ExitClean && ran.code != vocab.ExitFindings {
		t.Fatalf("analyze --scope=scope.json = %d, want a verdict\nstderr:\n%s", ran.code, ran.stderr)
	}
	var got []string
	for _, found := range envelopeAt(t, reportPath).Findings {
		got = append(got, string(found.Code)+" "+found.Symbol.Ref)
	}
	return got
}

func TestAConsumersInstantiationReachesTheTypeParameterItInstantiates(t *testing.T) {
	t.Parallel()
	for fixture, why := range map[string]string{
		"layout-through-a-consumers-generic":                                 "the consumer's conversion lays S out",
		"layout-through-a-target-generic-a-consumer-instantiates":            "View lays out the S the consumer gives it",
		"struct-conversion-through-a-target-generic-a-consumer-instantiates": "Wire converts the S the consumer gives it",
	} {
		t.Run(fixture, func(t *testing.T) {
			if got := scopedReport(t, "findings-ledger/"+fixture+".txtar"); len(got) != 0 {
				t.Errorf("findings = %q, want none: %s", got, why)
			}
		})
	}
}

func TestATypeAnotherPackageReachesThroughAnExportedDeclarationIsNoUnnecessaryExport(t *testing.T) {
	t.Parallel()
	var got []string
	for _, found := range findingsOfDir(t, archiveModule(t, "findings-ledger/types-reached-through-an-exported-declaration.txtar")).Result.Findings {
		if found.Code == vocab.UnnecessaryExport {
			got = append(got, found.Symbol.Ref)
		}
	}
	slices.Sort(got)
	want := []string{"go://example.com/app/internal/client#Inner", "go://example.com/app/internal/client#Local"}
	if !slices.Equal(got, want) {
		t.Errorf("%s findings = %q, want %q: package cli reaches every other type through client's exported declarations", vocab.UnnecessaryExport, got, want)
	}
}

func TestAConsumersEncoderReadsTheFieldsOfTheTargetsTypeItEncodes(t *testing.T) {
	t.Parallel()
	if got := scopedReport(t, "findings-ledger/encoded-by-a-consumer.txtar"); len(got) != 0 {
		t.Errorf("findings = %q, want none: the consumer's json.Marshal reads S.X", got)
	}
}

func TestAConsumersReadOfAStructLiteralsFieldReadsEachIdenticalLiteralsField(t *testing.T) {
	t.Parallel()
	got := scopedReport(t, "findings-ledger/struct-literal-twin-read-from-a-consumer.txtar")
	want := []string{"DS1003 go://example.com/app#P.In.Y", "DS1003 go://example.com/app#Q.In.Y"}
	if !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q: the consumer's read of P.In.X reads Q.In.X", got, want)
	}
}

func TestAMethodOfATypeDeclaredPerPlatformIsJudgedUnderTheTypeOfEachPlatform(t *testing.T) {
	t.Parallel()
	linux := `{"id": "linux-amd64", "os": "linux", "arch": "amd64"}`
	windows := `{"id": "windows-amd64", "os": "windows", "arch": "amd64"}`
	for _, tc := range []struct {
		fixture string
		want    []string
	}{
		{"method-of-a-type-live-on-one-platform", []string{
			"DS1002 impl impl_linux.go linux-amd64 root of 2",
			"DS1002 impl.helper m.go linux-amd64,windows-amd64 root of 2",
		}},
		{"method-of-a-type-live-on-the-other-platform", []string{
			"DS1002 impl impl_windows.go windows-amd64 root of 2",
			"DS1002 impl.helper m.go linux-amd64,windows-amd64 root of 2",
		}},
		{"method-of-a-type-dead-on-every-platform", []string{
			"DS1002 impl impl_linux.go linux-amd64 root of 3",
			"DS1002 impl impl_windows.go windows-amd64 root of 3",
		}},
		{"method-of-a-type-live-on-every-platform", []string{"DS1002 impl.helper m.go linux-amd64,windows-amd64 root of 1"}},
		{"method-of-a-type-a-platform-calls-it-on", nil},
	} {
		for _, order := range [][2]string{{linux, windows}, {windows, linux}} {
			t.Run(tc.fixture+"/"+order[0][8:13], func(t *testing.T) {
				files := archiveFiles(t, "findings-ledger/"+tc.fixture+".txtar")
				files["deadset.json"] = `{"target": {"kind": "application"}, "analysis": {"configurations": [` +
					order[0] + ", " + order[1] + `]}}`
				ran := runAnalyze(t, writeModule(t, files))
				if ran.code != vocab.ExitClean && ran.code != vocab.ExitFindings {
					t.Fatalf("analyze = %d, want a verdict\nstderr: %s", ran.code, ran.stderr)
				}
				var got []string
				for _, found := range envelopeAt(t, ran.reportPath).Findings {
					root := "member"
					if found.Component.Root {
						root = "root"
					}
					got = append(got, fmt.Sprintf("%s %s %s %s %s of %d", found.Code, found.Symbol.Name, found.Position.Path,
						strings.Join(slices.Sorted(slices.Values(found.Configurations)), ","), root, found.Component.SymbolCount))
				}
				if !slices.Equal(got, tc.want) {
					t.Errorf("findings = %q, want %q whatever order the configurations are listed in", got, tc.want)
				}
			})
		}
	}
}

// consumerTestsProduction is the repository document of an application whose
// loaded consumers' test files count as production code.
const consumerTestsProduction = `{"target": {"kind": "application"}, "analysis": {"consumer_tests": "production"}}`

// everyFinding is every finding of the run over dir, reported or withheld by the
// minimum confidence, as its code, confidence and subject.
func everyFinding(t *testing.T, dir string) []string {
	t.Helper()

	set := findingsOfDir(t, dir)
	var got []string
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		got = append(got, string(found.Code)+" "+string(found.Confidence)+" "+found.Symbol.Ref)
	}
	slices.Sort(got)
	return got
}

// sameAsControl checks that the leftout variant of an archive finds what its
// control variant finds, in the plain consumer-test mode and in the production
// one, and returns the leftout findings of each mode.
func sameAsControl(t *testing.T, archive, why string) map[string][]string {
	t.Helper()

	found := make(map[string][]string)
	for mode, document := range map[string]string{"consumer_tests_test": "", "consumer_tests_production": consumerTestsProduction} {
		files := archiveFiles(t, archive)
		if document != "" {
			files["control/"+repositoryDocument] = document
			files["leftout/"+repositoryDocument] = document
		}
		dir := writeModule(t, files)
		control, leftout := everyFinding(t, filepath.Join(dir, "control")), everyFinding(t, filepath.Join(dir, "leftout"))
		if !slices.Equal(leftout, control) || len(control) == 0 {
			t.Errorf("%s: findings = %q, want those of control, %q: %s", mode, leftout, control, why)
		}
		found[mode] = leftout
	}
	return found
}

func TestAPackageOnlyATestImportsFromALeftOutDirectoryReferencesAsTheTestDoes(t *testing.T) {
	t.Parallel()
	sameAsControl(t, "findings-ledger/referenced-from-a-test-only-left-out-package.txtar",
		"the packages below node_modules and testdata are test code, so their calls are a test's as the test's own are")
}

func TestATestOnlyLeftOutPackageKeepsTheTestSupportItUsesTestSupport(t *testing.T) {
	t.Parallel()
	for mode, found := range sameAsControl(t, "findings-ledger/test-support-reached-through-a-test-only-left-out-package.txtar",
		"the package below testdata is test code, so testutil stays test support") {
		for _, f := range found {
			if strings.HasSuffix(f, "#Helper") || strings.HasSuffix(f, "#TestRun") {
				t.Errorf("%s: findings hold %q, want none about Helper or TestRun: only test code uses testutil", mode, f)
			}
		}
	}
}

func TestATestOnlyLeftOutPackagesEncoderCallIsATestsEvidence(t *testing.T) {
	t.Parallel()
	sameAsControl(t, "findings-ledger/encoded-by-a-test-only-left-out-package.txtar",
		"the package below testdata is test code, so its json.Marshal is a test's evidence")
}

func TestALeftOutPackageATestAlsoCompilesKeepsItsEncoderCallProductionEvidence(t *testing.T) {
	t.Parallel()
	for mode, found := range sameAsControl(t, "findings-ledger/encoded-by-a-left-out-package-a-test-also-imports.txtar",
		"main imports the package below testdata, so its json.Marshal is production evidence however many test binaries compile it") {
		for _, f := range found {
			if strings.HasSuffix(f, " go://example.com/m/internal/p#S.X") {
				t.Errorf("%s: findings hold %q, want none about S.X: the production encoder reads it", mode, f)
			}
		}
	}
}

func TestATestOnlyLeftOutPackagesCallReachesNoFartherThanTheModule(t *testing.T) {
	t.Parallel()
	findings := map[string][]string{}
	for _, found := range everyFinding(t, archiveModule(t, "findings-ledger/exported-for-a-test-only-left-out-package.txtar")) {
		code, ref, _ := strings.Cut(found, " go://example.com/app/pkg#")
		findings[ref] = append(findings[ref], code)
	}
	if got, want := findings["Helper"], findings["Sibling"]; !slices.Equal(got, want) || !slices.ContainsFunc(want, func(f string) bool {
		return strings.HasPrefix(f, string(vocab.UnnecessaryExposure)+" ")
	}) {
		t.Errorf("findings of Helper = %q, want those of Sibling, %q, among them %s", got, want, vocab.UnnecessaryExposure)
	}
}

func TestALeftOutOrNestedConsumersEncoderReadsTheFieldsOfTheTypeItEncodes(t *testing.T) {
	t.Parallel()
	for fixture, ref := range map[string]string{
		"encoded-by-a-left-out-package": "go://example.com/m/internal/lib#S.X",
		"encoded-by-a-nested-module":    "go://example.com/app/internal/lib#S.X",
	} {
		t.Run(fixture, func(t *testing.T) {
			wantNoFinding(t, findingsAbout(t, archiveModule(t, "findings-ledger/"+fixture+".txtar")),
				"the consumer's json.Marshal reads it", ref)
		})
	}
}

func TestATestOnlyLeftOutPackagesImportReachesTheFileItImports(t *testing.T) {
	t.Parallel()
	for mode, found := range sameAsControl(t, "findings-ledger/imported-by-a-test-only-left-out-package.txtar",
		"the package below testdata is test code, so its import is a test's") {
		for _, f := range found {
			if strings.HasPrefix(f, string(vocab.FileNeverImported)+" ") {
				t.Errorf("%s: findings hold %q, want no %s: the test-only package imports lib", mode, f, vocab.FileNeverImported)
			}
		}
	}
}

func TestAConsumersImportReachesTheFileItImports(t *testing.T) {
	t.Parallel()
	got := scopedReport(t, "findings-ledger/imported-by-a-consumer.txtar")
	if slices.Contains(got, string(vocab.FileNeverImported)+" go://example.com/app/side#side.go:file") ||
		!slices.ContainsFunc(got, func(f string) bool { return strings.HasSuffix(f, " go://example.com/app/side#Unused") }) {
		t.Errorf("findings = %q, want one about side.Unused and no %s about side.go: the consumer imports side", got, vocab.FileNeverImported)
	}
}

// reportedBelow is [reportedAt] of the target at dir of the fixture, whose other
// directories hold modules outside the target.
func reportedBelow(t *testing.T, fixture, dir string) []string {
	t.Helper()

	set := findingsOfDir(t, filepath.Join(archiveModule(t, fixture), dir))
	var got []string
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		got = append(got, string(found.Code)+" "+found.Position.Path+":"+strconv.Itoa(found.Position.Line))
	}
	return got
}

// Two loads read one package as two sets of types, and a module built against
// another version of a dependency reads that dependency apart: a type of one and an
// interface or a type of the other still meet as the build that holds both meets
// them.
func TestATypeAndAnInterfaceOfTwoReadingsMeetAsOneBuildMeetsThem(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture string
		want    []string
		why     string
	}{
		{"interface-result-of-another-reading", nil, "z's call of Source.Get stands for impl's Get, whose Payload json.Marshal reads"},
		{"returned-through-an-outside-interface-of-another-version", nil, "code outside may call Doc's Value through dep2's Source and read the Payload"},
		{"published-parameter-of-another-reading", []string{"DS1001 z/use/use.go:7"}, "an outside program can hand Use an Impl, so Impl's Get is retained"},
		{"outside-closure-of-another-version", nil, "the dep1 store m's call reaches asserts Doc's Encoder"},
		{"error-holder-of-another-version", nil, "store.CodeOf asserts Coder on m's error"},
		{"outside-function-appending-its-parameter", nil, "append decodes nothing, so doc crosses into dep, whose Encoder it implements"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			if got := reportedBelow(t, "findings-ledger/"+tc.fixture+".txtar", "target"); !slices.Equal(got, tc.want) {
				t.Errorf("findings = %q, want %q: %s", got, tc.want, tc.why)
			}
		})
	}
}

func TestAConsumersReadingOfTheTargetMeetsTheTargetsOwnTypes(t *testing.T) {
	t.Parallel()
	for fixture, why := range map[string]string{
		"outside-interface-fixing-a-signature-in-another-reading": "the consumer's Getter fixes Get's signature",
		"struct-literal-twin-in-another-reading":                  "the consumer's literal is P.In's type, so its read of X reads P.In.X",
	} {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			if got := scopedReport(t, "findings-ledger/"+fixture+".txtar"); len(got) != 0 {
				t.Errorf("findings = %q, want none: %s", got, why)
			}
		})
	}
}

// A call through a function variable reaches only what production code writes into
// it: nothing, where no file writes it, and never a function only a test file
// assigns, so the field the payload never names stays certainly unused.
func TestACallThroughAHookReachesOnlyWhatProductionCodeWrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, fixture, drop string
	}{
		{"no write", "hook-nothing-writes", ""},
		{"a test's write", "hook-only-a-test-reassigns", ""},
		{"no test", "hook-only-a-test-reassigns", "hook_test.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := archiveFiles(t, "findings-ledger/"+tc.fixture+".txtar")
			delete(files, tc.drop)
			wantFinding(t, findingsAbout(t, writeModule(t, files)), "go://example.com/app#payload.Hidden", vocab.UnusedMember, vocab.Certain)
		})
	}
}
