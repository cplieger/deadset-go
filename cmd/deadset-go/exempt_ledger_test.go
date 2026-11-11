package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/findings"
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
func findingsAbout(t *testing.T, dir string) map[string]*findings.Finding {
	t.Helper()

	set := findingsOfDir(t, dir)
	about := make(map[string]*findings.Finding)
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		about[found.Symbol.Ref] = &found
	}
	return about
}

// wantNoFinding fails for every one of refs a run reports or withholds a finding
// about.
func wantNoFinding(t *testing.T, about map[string]*findings.Finding, why string, refs ...string) {
	t.Helper()

	for _, ref := range refs {
		if found := about[ref]; found != nil {
			t.Errorf("%s is %s %s (%s), want no finding: %s", ref, found.Code, found.Confidence, found.Message, why)
		}
	}
}

// wantFinding fails unless a run reports or withholds a finding about ref under
// code at confidence.
func wantFinding(t *testing.T, about map[string]*findings.Finding, ref string, code vocab.Code, confidence vocab.Class) {
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
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/generic-implementor.txtar"))

	plain, generic := about["go://example.com/app#Plain.Get"], about["go://example.com/app#Box.Get"]
	if plain == nil || generic == nil {
		t.Fatalf("Plain.Get = %v, Box.Get = %v, want a finding about each", plain, generic)
	}
	if strings.Replace(generic.Message, generic.Symbol.Name, plain.Symbol.Name, 1) != plain.Message {
		t.Errorf("Box.Get's message is %q, want Plain.Get's %q under its own name: Box[int] implements Getter as Plain does", generic.Message, plain.Message)
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
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/valueless-on-attribute.txtar"))

	wantFinding(t, about, "go://example.com/app#Widget.Spare", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "the handler after a quoted '>' holds an action, which the engine encodes whole",
		"go://example.com/app#Card.Back")
}

func TestATemplateCallPlacingTheValueInAnEventHandlerReadsEveryField(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/handler-written-by-a-called-template.txtar"))

	wantNoFinding(t, about, "a called template places the value in an event handler, so the set encodes it whole",
		"go://example.com/app#Profile.Secret", "go://example.com/app#Card.Back")
}

func TestATemplateCalledInAnAttributeValueReadsWhatItNamesUnlessItWritesAHandler(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-called-in-an-attribute-value.txtar"))

	wantFinding(t, about, "go://example.com/app#Profile.Secret", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "the template called in the value names it", "go://example.com/app#Profile.Name")
	wantNoFinding(t, about, "the called template closes the value and writes a handler holding the value",
		"go://example.com/app#Card.Back")
}

func TestATemplateEndingInAnotherContextThanItStartsInReadsEveryField(t *testing.T) {
	t.Parallel()
	for range 5 {
		about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-called-twice-ending-elsewhere.txtar"))

		wantNoFinding(t, about, "the page writes its value as JSON whole", "go://example.com/app#Page.Secret")
	}
}

func TestABlockOverriddenInOneParseFilesCallReadsWhatTheOverrideNames(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/block-overridden.txtar"))

	wantFinding(t, about, "go://example.com/app#Home.Unused", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "the overriding template names it", "go://example.com/app#Home.Title")
}

func TestANewNamedParseChainReadsWhatItsTemplatesName(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/new-named-parse-chain.txtar"))

	wantFinding(t, about, "go://example.com/app#Nav.Unused", vocab.WriteOnlySymbol, vocab.Certain)
	wantNoFinding(t, about, "both templates name it", "go://example.com/app#Nav.Title")
}

func TestATemplateFileChosenAtRunTimeReadsEveryField(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-file-chosen-at-run-time.txtar"))

	wantNoFinding(t, about, "the file the program parses may write its value as JSON whole", "go://example.com/app#Page.Secret")
}

func TestEmbeddedTemplatesAreParsedInSortedOrder(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/embedded-templates-in-sorted-order.txtar"))

	wantNoFinding(t, about, "the file sorting last writes its value as JSON whole",
		"go://example.com/app#Part.Secret", "go://example.com/app#Page.Secret")
}

func TestATemplateSetParsingOneOfTwoAlternativesReadsEveryField(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/template-alternatives.txtar"))

	for _, set := range []string{"Text", "File", "Glob", "Pattern", "Root"} {
		wantNoFinding(t, about, "the alternative the program parses may write its value as JSON whole",
			"go://example.com/app#"+set+".Secret")
	}
}

func TestTemplatesOfOneNameFromTwoSetsReadEveryField(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/templates-of-one-name-from-two-sets.txtar"))

	wantNoFinding(t, about, "the set the function executes may write its value as JSON whole", "go://example.com/app#Page.Secret")
}

func TestADecodedValueRelayedWholeToOutsideCodeKeepsItsFields(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/decoded-relay.txtar"))

	wantNoFinding(t, about, "outside code receives the decoded value whole and reads what was decoded",
		"go://example.com/app#Diagnostics.Platform", "go://example.com/app#Diagnostics.Logs", "go://example.com/app#Signal.Name")
}

func TestAFieldOnlyAnOutsideFillerReachedThroughAnInProgramGenericFillsIsPossible(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/generic-client.txtar"))

	wantFinding(t, about, "go://example.com/app#Untyped.Spare", vocab.UnusedMember, vocab.Possible)
	wantFinding(t, about, "go://example.com/app#Typed.Spare", vocab.UnusedMember, vocab.Certain)
}

func TestTheXMLEncoderReadsXMLName(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/xml-name-encoder.txtar"))

	wantNoFinding(t, about, "encoding/xml names the element from XMLName's tag", "go://example.com/app#Feed.XMLName")
}

func TestAnEnumFilledByAnyDecodingEntryPointKeepsAMemberThatRenumbersANamedOne(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/enum-decoded-by-every-decoder.txtar"))

	wantNoFinding(t, about, "a decoder fills the enum from a document, so a value no name gives can arrive and deleting the member renumbers a named one",
		"go://example.com/app#Low", "go://example.com/app#Mid", "go://example.com/app#Off", "go://example.com/app#On")
}

func TestAnEnumALineDirectiveRendersOutsideTheTreeIsTheTargetsOwn(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/enum-under-a-line-directive-outside-the-tree.txtar"))

	wantNoFinding(t, about, "the target writes the enum, so a decoder can fill a value deleting the member renumbers",
		"go://example.com/app#Low", "go://example.com/app#Mid")
}

func TestAGoFlagsCommandFieldIsRead(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/go-flags-command.txtar"))

	wantNoFinding(t, about, "the parser runs the subcommand a command field holds",
		"go://example.com/app#Command.Migrate", "go://example.com/app#migrateCommand", "go://example.com/app#migrateCommand.Execute")
	wantFinding(t, about, "go://example.com/app#Command.Verbose", vocab.UnusedMember, vocab.Possible)
}

func TestTemplatesReadFromAFileSystemReadTheFieldsTheyName(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/templates-read-from-a-file-system.txtar"))

	wantNoFinding(t, about, "projects.tpl.html names them",
		"go://example.com/app#ProjectStats.Project", "go://example.com/app#ProjectStats.TopLanguage")
	wantFinding(t, about, "go://example.com/app#ProjectStats.UserID", vocab.UnusedMember, vocab.Possible)
}

func TestTemplatesParsedFromAFileSystemParameterReadWhatAClosureExecutes(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/templates-parsed-from-a-file-system-parameter.txtar"))

	wantNoFinding(t, about, "the closure executes the value with a template set the analysis cannot list",
		"go://example.com/app#page.Title", "go://example.com/app#page.Rows", "go://example.com/app#row.Why", "go://example.com/app#row.Skip")
}

func TestAFieldOfAStructWithNoNameADecoderFillsIsReadByAnEncoder(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/decoded-into-an-unnamed-struct.txtar"))

	wantNoFinding(t, about, "the decoder fills Note through a pointer to Meta and the encoder reads it",
		"go://example.com/app#document.Meta.Note")
	wantFinding(t, about, "go://example.com/app#document.Spare", vocab.UnusedMember, vocab.Certain)
}

func TestATestThatEncodesADecodedValueAgainReadsItsFields(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/test-re-encodes-a-decoded-value.txtar"))

	wantNoFinding(t, about, "the benchmark encodes what it decoded",
		"go://example.com/lib#example.Title", "go://example.com/lib#example.Integers")
}

func TestKeyEnumeratingReflectionReadsEveryField(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/key-enumerating-reflection.txtar"))

	wantNoFinding(t, about, "a walk over the fields reads every field, written or not",
		"go://example.com/app#HTTPTarget.URI", "go://example.com/app#HTTPTarget.Body")
}

func TestAValueReturnedThroughAnOutsideInterfaceIsSentFromEveryModulesReading(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, filepath.Join(archiveModule(t, "exempt-ledger/returned-through-in-two-modules.txtar"), "target"))

	wantNoFinding(t, about, "the value Resource returns reaches the dependency, whichever module's reading of it the method implements",
		"go://example.com/a#deployment.Replicas", "go://example.com/b#pod.Phase")
}

func TestTwoTypesOneNameSpellsInTwoFunctionsAreTwoTypes(t *testing.T) {
	t.Parallel()
	kept := []struct {
		archive string
		why     string
		refs    []string
	}{
		{"local-type-error-assertion", "second's e answers retry's Transient assertion itself", []string{"go://example.com/errs#second.IsTransient"}},
		{"local-type-outside-assertion", "second's e answers sizes' Sizer assertion itself", []string{"go://example.com/app#second.Size"}},
		{"local-type-format", "printing the inner e prints the outer e's field", []string{"go://example.com/app#alpha.String", "go://example.com/app#bravo.String"}},
		{"local-type-encoder", "encoding the inner e reads the outer e's field", []string{"go://example.com/app#alpha.X", "go://example.com/app#bravo.Y"}},
		{"local-type-self-encoder", "only the outer e holds a self-encoder", []string{"go://example.com/app#selfish.MarshalJSON", "go://example.com/app#plain.Y"}},
		{"local-type-interface-store", "box.V holds each function's e", []string{"go://example.com/app#alpha.X", "go://example.com/app#bravo.Y"}},
		{"local-type-layout", "view lays out each function's e", []string{"go://example.com/app#alpha.X", "go://example.com/app#bravo.Y"}},
		{"local-type-derived-assertion", "the second's e implements sizer", []string{"go://example.com/app#second.size"}},
		{"type-parameter-layout", "view lays out each function's T", []string{"go://example.com/app#alpha.X", "go://example.com/app#bravo.Y"}},
	}
	for _, c := range kept {
		t.Run(c.archive, func(t *testing.T) {
			about := findingsAbout(t, archiveModule(t, "exempt-ledger/"+c.archive+".txtar"))
			wantNoFinding(t, about, c.why, c.refs...)
		})
	}
	for _, archive := range []string{"local-type-converted-like-an-enum", "local-type-decoded-like-an-enum"} {
		t.Run(archive, func(t *testing.T) {
			about := findingsAbout(t, archiveModule(t, "exempt-ledger/"+archive+".txtar"))
			wantFinding(t, about, "go://example.com/app#slow", vocab.UnusedEnumMember, vocab.Certain)
		})
	}
}

func TestAValueReturnedThroughAnOutsideConstraintIsNotSent(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, filepath.Join(archiveModule(t, "exempt-ledger/returned-through-a-constraint.txtar"), "target"))

	wantFinding(t, about, "go://example.com/app#deployment.Replicas", vocab.WriteOnlySymbol, vocab.Certain)
}

func TestAnOutsideAssertionRetainsTheUnexportedMethodOfItsOwnPackageAlone(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, filepath.Join(archiveModule(t, "exempt-ledger/unexported-method-of-one-name-in-two-packages.txtar"), "target"))

	wantFinding(t, about, "go://example.com/app#T.mark", vocab.UnusedUnexported, vocab.Certain)
	wantNoFinding(t, about, "Take asserts Marker on T", "go://example.com/app#T.Name")
}

func TestAMethodATypeHasByPromotionIsCalledThroughTheEmbeddedField(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ archive, method, why string }{
		{"promoted-self-encoder", "selfish.MarshalJSON", "json.Marshal calls wrap's promoted MarshalJSON"},
		{"promoted-pointer-self-encoder", "selfish.MarshalJSON", "json.Marshal calls *wrap's promoted MarshalJSON"},
		{"promoted-text-marshaler-map-key", "selfish.MarshalText", "json.Marshal spells a wrap key by its promoted MarshalText"},
		{"promoted-xml-marshaler", "selfish.MarshalXML", "xml.Marshal calls wrap's promoted MarshalXML"},
		{"promoted-string-method", "wrap.selfish", "fmt.Println calls wrap's promoted String, which reads the field"},
		{"promoted-through-an-embedded-pointer", "selfish.MarshalJSON", "json.Marshal calls the MarshalJSON wrap's *selfish promotes"},
		{"promoted-through-two-embeddings", "selfish.MarshalJSON", "json.Marshal calls the MarshalJSON mid promotes to wrap"},
		{"promoted-on-a-field-type", "selfish.MarshalJSON", "json.Marshal walks outer.W and calls wrap's promoted MarshalJSON"},
	} {
		t.Run(c.archive, func(t *testing.T) {
			about := findingsAbout(t, archiveModule(t, "exempt-ledger/"+c.archive+".txtar"))
			wantNoFinding(t, about, c.why, "go://example.com/app#"+c.method)
			if c.archive != "promoted-text-marshaler-map-key" {
				wantFinding(t, about, "go://example.com/app#selfish.Z", vocab.UnusedMember, vocab.Certain)
			}
		})
	}
}

func TestAnEnumMemberIsRetainedByTheEvidenceTheRunsModeHolds(t *testing.T) {
	t.Parallel()
	about := findingsAbout(t, archiveModule(t, "exempt-ledger/enum-converted-in-a-test-file-and-in-production.txtar"))
	wantNoFinding(t, about, "main converts to mode", "go://example.com/app#slow")

	about = findingsAbout(t, archiveModule(t, "exempt-ledger/enum-converted-only-in-a-test-file.txtar"))
	wantFinding(t, about, "go://example.com/app#slow", vocab.TestOnlyUse, vocab.Certain)
}
