package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/txtar"
)

// archiveModule writes one txtar archive of testdata, named by its path there,
// into a module directory of its own.
func archiveModule(t *testing.T, archive string) string {
	t.Helper()
	return writeModule(t, archiveFiles(t, archive))
}

// archiveFiles is the content of every file of one txtar archive of testdata, by
// its name there.
func archiveFiles(t *testing.T, archive string) map[string]string {
	t.Helper()

	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	files := make(map[string]string, len(parsed.Files))
	for _, f := range parsed.Files {
		files[f.Name] = string(f.Data)
	}
	return files
}

// findingsAbout maps the subject of every finding of a run, reported or withheld
// by the minimum confidence, to the finding.
func findingsAbout(t *testing.T, dir string) map[string]*kinds.Finding {
	t.Helper()

	set := findingsOfDir(t, dir)
	about := make(map[string]*kinds.Finding)
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		about[found.Symbol.Ref] = &found
	}
	return about
}

// wantNoFinding fails for every one of refs a run reports or withholds a finding
// about.
func wantNoFinding(t *testing.T, about map[string]*kinds.Finding, why string, refs ...string) {
	t.Helper()

	for _, ref := range refs {
		if found := about[ref]; found != nil {
			t.Errorf("%s is %s %s (%s), want no finding: %s", ref, found.Code, found.Confidence, found.Message, why)
		}
	}
}

// wantFinding fails unless a run reports or withholds a finding about ref under
// code at confidence.
func wantFinding(t *testing.T, about map[string]*kinds.Finding, ref string, code vocab.Code, confidence vocab.Class) {
	t.Helper()

	found := about[ref]
	switch {
	case found == nil:
		t.Errorf("the run has no finding about %s, want %s %s", ref, code, confidence)
	case found.Code != code || found.Confidence != confidence:
		t.Errorf("%s is %s %s, want %s %s", ref, found.Code, found.Confidence, code, confidence)
	}
}

func TestAGenericImplementationFallsWithTheUncalledMethodItImplements(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/generic-implementor.txtar"))

	plain, generic := about["go://example.com/app#Plain.Get"], about["go://example.com/app#Box.Get"]
	if plain == nil || generic == nil {
		t.Fatalf("Plain.Get = %v, Box.Get = %v, want a finding about each", plain, generic)
	}
	if generic.Message != plain.Message {
		t.Errorf("Box.Get's message is %q, want Plain.Get's %q: Box[int] implements Getter as Plain does", generic.Message, plain.Message)
	}
	method := about["go://example.com/app#Getter.Get"]
	if method == nil {
		t.Fatal("the run has no finding about Getter.Get, want an uncalled interface method")
	}
	var implementations []string
	for _, impl := range method.Details.Implementations {
		implementations = append(implementations, impl.Ref)
	}
	if want := []string{"go://example.com/app#Box", "go://example.com/app#Plain"}; !slices.Equal(implementations, want) {
		t.Errorf("Getter.Get's implementations are %q, want %q", implementations, want)
	}
}

func TestAnEventHandlerEncodesTheValueWholeOnlyInAnActionItHolds(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/valueless-on-attribute.txtar"))

	wantFinding(t, about, "go://example.com/app#Widget.Spare", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "the handler after a quoted '>' holds an action, which the engine encodes whole",
		"go://example.com/app#Card.Back")
}

func TestATemplateCallPlacingTheValueInAnEventHandlerReadsEveryField(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/handler-written-by-a-called-template.txtar"))

	wantNoFinding(t, about, "a called template places the value in an event handler, so the set encodes it whole",
		"go://example.com/app#Profile.Secret", "go://example.com/app#Card.Back")
}

func TestATemplateCalledInAnAttributeValueReadsWhatItNamesUnlessItWritesAHandler(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-called-in-an-attribute-value.txtar"))

	wantFinding(t, about, "go://example.com/app#Profile.Secret", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "the template called in the value names it", "go://example.com/app#Profile.Name")
	wantNoFinding(t, about, "the called template closes the value and writes a handler holding the value",
		"go://example.com/app#Card.Back")
}

func TestATemplateEndingInAnotherContextThanItStartsInReadsEveryField(t *testing.T) {
	for range 5 {
		about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-called-twice-ending-elsewhere.txtar"))

		wantNoFinding(t, about, "the page writes its value as JSON whole", "go://example.com/app#Page.Secret")
	}
}

func TestABlockOverriddenInOneParseFilesCallReadsWhatTheOverrideNames(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/block-overridden.txtar"))

	wantFinding(t, about, "go://example.com/app#Home.Unused", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "the overriding template names it", "go://example.com/app#Home.Title")
}

func TestANewNamedParseChainReadsWhatItsTemplatesName(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/new-named-parse-chain.txtar"))

	wantFinding(t, about, "go://example.com/app#Nav.Unused", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "both templates name it", "go://example.com/app#Nav.Title")
}

func TestATemplateFileChosenAtRunTimeReadsEveryField(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-file-chosen-at-run-time.txtar"))

	wantNoFinding(t, about, "the file the program parses may write its value as JSON whole", "go://example.com/app#Page.Secret")
}

func TestEmbeddedTemplatesAreParsedInSortedOrder(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/embedded-templates-in-sorted-order.txtar"))

	wantNoFinding(t, about, "the file sorting last writes its value as JSON whole",
		"go://example.com/app#Part.Secret", "go://example.com/app#Page.Secret")
}

func TestATemplateSetParsingOneOfTwoAlternativesReadsEveryField(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-alternatives.txtar"))

	for _, set := range []string{"Text", "File", "Glob", "Pattern", "Root"} {
		wantNoFinding(t, about, "the alternative the program parses may write its value as JSON whole",
			"go://example.com/app#"+set+".Secret")
	}
}

func TestTemplatesOfOneNameFromTwoSetsReadEveryField(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/templates-of-one-name-from-two-sets.txtar"))

	wantNoFinding(t, about, "the set the function executes may write its value as JSON whole", "go://example.com/app#Page.Secret")
}

func TestADecodedValueRelayedWholeToOutsideCodeKeepsItsFields(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/decoded-relay.txtar"))

	wantNoFinding(t, about, "outside code receives the decoded value whole and reads what was decoded",
		"go://example.com/app#Diagnostics.Platform", "go://example.com/app#Diagnostics.Logs", "go://example.com/app#Signal.Name")
}

func TestAFieldOnlyAnOutsideFillerReachedThroughAnInProgramGenericFillsIsPossible(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/generic-client.txtar"))

	wantFinding(t, about, "go://example.com/app#Untyped.Spare", vocab.UnusedMember, vocab.Possible)
	wantFinding(t, about, "go://example.com/app#Typed.Spare", vocab.UnusedMember, vocab.Certain)
}

func TestTheXMLEncoderReadsXMLName(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/xml-name-encoder.txtar"))

	wantNoFinding(t, about, "encoding/xml names the element from XMLName's tag", "go://example.com/app#Feed.XMLName")
}

func TestAnEnumFilledByAnyDecodingEntryPointKeepsEveryMember(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/enum-decoded-by-every-decoder.txtar"))

	wantNoFinding(t, about, "a decoder fills the enum from a document, so any member can arrive",
		"go://example.com/app#Mid", "go://example.com/app#High", "go://example.com/app#On")
}

func TestAGoFlagsCommandFieldIsRead(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/go-flags-command.txtar"))

	wantNoFinding(t, about, "the parser runs the subcommand a command field holds",
		"go://example.com/app#Command.Migrate", "go://example.com/app#migrateCommand", "go://example.com/app#migrateCommand.Execute")
	wantFinding(t, about, "go://example.com/app#Command.Verbose", vocab.UnusedMember, vocab.Possible)
}

func TestTemplatesReadFromAFileSystemReadTheFieldsTheyName(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/templates-read-from-a-file-system.txtar"))

	wantNoFinding(t, about, "projects.tpl.html names them",
		"go://example.com/app#ProjectStats.Project", "go://example.com/app#ProjectStats.TopLanguage")
	wantFinding(t, about, "go://example.com/app#ProjectStats.UserID", vocab.UnusedMember, vocab.Possible)
}

func TestTemplatesParsedFromAFileSystemParameterReadWhatAClosureExecutes(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/templates-parsed-from-a-file-system-parameter.txtar"))

	wantNoFinding(t, about, "the closure executes the value with a template set the analysis cannot list",
		"go://example.com/app#page.Title", "go://example.com/app#page.Rows", "go://example.com/app#row.Why", "go://example.com/app#row.Skip")
}

func TestAFieldOfAStructWithNoNameADecoderFillsIsReadByAnEncoder(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/decoded-into-an-unnamed-struct.txtar"))

	wantNoFinding(t, about, "the decoder fills Note through a pointer to Meta and the encoder reads it",
		"go://example.com/app#document.Meta.Note")
	wantFinding(t, about, "go://example.com/app#document.Spare", vocab.UnusedMember, vocab.Certain)
}

func TestATestThatEncodesADecodedValueAgainReadsItsFields(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/test-re-encodes-a-decoded-value.txtar"))

	wantNoFinding(t, about, "the benchmark encodes what it decoded",
		"go://example.com/lib#example.Title", "go://example.com/lib#example.Integers")
}

func TestKeyEnumeratingReflectionReadsEveryField(t *testing.T) {
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/key-enumerating-reflection.txtar"))

	wantNoFinding(t, about, "a walk over the fields reads every field, written or not",
		"go://example.com/app#HTTPTarget.URI", "go://example.com/app#HTTPTarget.Body")
}
