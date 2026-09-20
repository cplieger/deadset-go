package kinds

import (
	"errors"
	"go/token"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/suppress"
)

// suppressed is the harness input over one archive with the target's three
// suppression documents read and the sweep run again with their marks, which is the
// pipeline a suppression needs: a mark makes its symbol live before the sweep, so
// the sweep the self-check kinds read is not the one a run with no suppression makes.
func suppressed(t *testing.T, archive string, resolved config.Config) *Input {
	t.Helper()

	dir := extract(t, archive)
	in := inputOfDir(t, dir, resolved, Consumers{})
	one := in.Per[0]

	inline, inlineRefused, err := suppress.Inline(one.Result, one.Resolve, one.Symbols)
	if err != nil {
		t.Fatalf("Setup: suppress.Inline(%s): %v", archive, err)
	}
	entries, entryRefused, err := suppress.IgnoreFile(filepath.Join(dir, suppress.IgnoreFileName), one.Symbols)
	if err != nil {
		t.Fatalf("Setup: suppress.IgnoreFile(%s): %v", archive, err)
	}
	rows, rowRefused, err := suppress.Baseline(filepath.Join(dir, suppress.BaselineFileName), one.Symbols)
	if err != nil {
		t.Fatalf("Setup: suppress.Baseline(%s): %v", archive, err)
	}

	in.Marks = slices.Concat(inline, entries, rows)
	in.Refusals = slices.Concat(inlineRefused, entryRefused, rowRefused)
	swept := graph.NewMatrix(in.Merged).Sweep(graph.SweepInput{
		Exempt: in.Exempt,
		Marked: boundMarks(in.Marks),
		Mode:   graph.Mode{Production: true},
	})
	in.Sweep = &swept
	// The index caches the sweep's answers, so the sweep the marks produced is the
	// one every lookup of this input reads.
	in.indexed = nil
	return in
}

// boundMarks is the declaration of every record that bound one, which is what seeds
// the sweep.
func boundMarks(marks []suppress.Record) []graph.SymbolID {
	bound := make([]graph.SymbolID, 0, len(marks))
	for i := range marks {
		if marks[i].Bound != "" {
			bound = append(bound, marks[i].Bound)
		}
	}
	return bound
}

// The three sentences a stale record of these fixtures reports, one per mechanism.
const (
	staleInline   = "inline directive for DS1002 matches no current finding"
	staleIgnore   = "ignore entry for DS1001 matches no current finding"
	staleBaseline = "baseline row for DS1001 matches no current finding"
)

// staleAt is one stale-suppression finding as a test names it: where it is, which
// mechanism held the record, and the sentence it reports.
type staleAt struct {
	path      string
	mechanism string
	message   string
	line      int
}

// staleFindings is every stale suppression one input reports, in the order reported.
func staleFindings(t *testing.T, in *Input) []staleAt {
	t.Helper()

	found, err := StaleSuppressions(in)
	if err != nil {
		t.Fatalf("StaleSuppressions() = _, %v, want the stale records", err)
	}
	named := make([]staleAt, 0, len(found))
	for i := range found {
		named = append(named, staleAt{
			path:      found[i].Position.Path,
			line:      found[i].Position.Line,
			mechanism: found[i].Details.Mechanism,
			message:   found[i].Message,
		})
	}
	return named
}

func TestStaleSuppressionsReportsEveryRecordAtEveryMechanismTheMarkHeldNothingBackFor(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())

	want := []staleAt{
		{path: "main.go", line: 5, mechanism: "inline", message: staleInline},
		{path: "main.go", line: 11, mechanism: "inline", message: staleInline},
		{path: suppress.IgnoreFileName, line: 10, mechanism: "ignore", message: staleIgnore},
		{path: suppress.IgnoreFileName, line: 16, mechanism: "ignore", message: staleIgnore},
		{path: suppress.BaselineFileName, line: 10, mechanism: "baseline", message: staleBaseline},
	}
	if got := staleFindings(t, in); !slices.Equal(got, want) {
		t.Errorf("StaleSuppressions() = %+v, want %+v", got, want)
	}
}

func TestStaleSuppressionsCarriesTheRecordAsWrittenAtTheFixedSeverity(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())

	// Through the framework, because no emitter completes its own finding: the kind
	// name, the class, the severity, the fixability and the component are the
	// completion's and a reader of the report sees them.
	found := computed(t, in, map[string]Emitter{staleSuppressionCode: StaleSuppressions}).Findings
	if len(found) == 0 {
		t.Fatal("the stale-suppression kind reports nothing, want the fixture's stale records")
	}

	for i := range found {
		one := &found[i]
		switch {
		case one.Code != staleSuppressionCode:
			t.Errorf("StaleSuppressions() reports %s, want every record under %s", one.Code, staleSuppressionCode)
		case one.Kind != "stale-suppression":
			t.Errorf("StaleSuppressions() names the kind %q, want the vocabulary's own name", one.Kind)
		case one.Severity != config.Deny:
			t.Errorf("StaleSuppressions() reports at %s, want %s: the kind is fixed on at deny", one.Severity, config.Deny)
		case one.Confidence != Certain || one.Class != Certain:
			t.Errorf("StaleSuppressions() reports class %s and confidence %s, want both certain", one.Class, one.Confidence)
		case one.Symbol.Kind != suppressionSubject:
			t.Errorf("StaleSuppressions() names the subject %q, want %q", one.Symbol.Kind, suppressionSubject)
		case one.Fixability != "none":
			t.Errorf("StaleSuppressions() reports fixability %q, want none", one.Fixability)
		case one.Details.Entry == nil:
			t.Errorf("StaleSuppressions() carries no entry for the record at %s:%d", one.Position.Path, one.Position.Line)
		case one.Component.SymbolCount != 1 || !one.Component.Root || one.Component.DeletableLines != 0:
			t.Errorf("StaleSuppressions() puts the record in %+v, want a one-member root component with no deletable line",
				one.Component)
		}
	}

	// The record of the entry no declaration answers names the reference the entry
	// spells, and the record of the directive that bound nothing names the file that
	// holds it, because no declaration answered either.
	entry := recordAt(t, found, suppress.IgnoreFileName, 16)
	if entry.Symbol.Ref != "go://example.com/selfcheck#Vanished" {
		t.Errorf("the record of the unanswered entry names %q, want the reference the entry spells", entry.Symbol.Ref)
	}
	if want := (&Entry{
		Code:   "DS1001",
		Symbol: "go://example.com/selfcheck#Vanished",
		Path:   "main.go",
		Reason: "stale: no declaration answers this reference",
	}); *entry.Details.Entry != *want {
		t.Errorf("the record of the unanswered entry carries %+v, want %+v", *entry.Details.Entry, *want)
	}
	directive := recordAt(t, found, "main.go", 11)
	if directive.Symbol.Ref != "go://example.com/selfcheck#main.go:file" {
		t.Errorf("the record of the directive that bound nothing names %q, want the file form of main.go",
			directive.Symbol.Ref)
	}
}

// recordAt is the finding reported at one site, which is how a test names a row of a
// document: the pass returns the findings in the canonical order, which orders the
// rows of several documents by path rather than by the order they were read in.
func recordAt(t *testing.T, found []Finding, path string, line int) Finding {
	t.Helper()

	for i := range found {
		if found[i].Position.Path == path && found[i].Position.Line == line {
			return found[i]
		}
	}
	t.Fatalf("the pass reports nothing at %s:%d: it reports %+v", path, line, found)
	return Finding{}
}

func TestTotalsCountsBoundRecordsInEffectAndEveryReasonOnce(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())

	// In effect: the directive above quiet, the entry naming Dropped and the row
	// naming gap, each bound to a declaration the sweep would otherwise report.
	// Reasons: three directives, three entries and two rows, each carrying one
	// reason whatever the number of records it produced.
	inEffect, reasons := Totals(in)
	if inEffect != 3 || reasons != 8 {
		t.Errorf("Totals() = %d in effect and %d reasons, want 3 and 8", inEffect, reasons)
	}
}

func TestStaleSuppressionsLeavesARecordDormantWhoseCodeTheConfigurationSilences(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]config.Severity{"DS1002": config.Allow}
	in := suppressed(t, "selfcheck-stale.txtar", resolved)

	// The three records naming DS1002 that bound a declaration are dormant: the two
	// inline ones above loud and quiet, and the baseline row naming gap. The
	// directive above a blank line bound nothing, so nothing silenced it and it is
	// stale whatever the severity map holds.
	want := []staleAt{
		{path: "main.go", line: 11, mechanism: "inline", message: staleInline},
		{path: suppress.IgnoreFileName, line: 10, mechanism: "ignore", message: staleIgnore},
		{path: suppress.IgnoreFileName, line: 16, mechanism: "ignore", message: staleIgnore},
		{path: suppress.BaselineFileName, line: 10, mechanism: "baseline", message: staleBaseline},
	}
	if got := staleFindings(t, in); !slices.Equal(got, want) {
		t.Errorf("StaleSuppressions() with DS1002 allowed = %+v, want %+v", got, want)
	}
	if inEffect, _ := Totals(in); inEffect != 1 {
		t.Errorf("Totals() with DS1002 allowed = %d in effect, want 1: a dormant record is in effect for nothing",
			inEffect)
	}
}

func TestStaleSuppressionsLeavesARecordDormantBelowTheConfiguredMinimumConfidence(t *testing.T) {
	for name, held := range map[string]struct {
		minimum  config.Confidence
		inEffect int
	}{
		"a minimum the finding reaches":      {minimum: config.Possible, inEffect: 1},
		"a minimum the finding cannot reach": {minimum: config.Probable, inEffect: 0},
		"the highest minimum there is":       {minimum: config.Certain, inEffect: 0},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := libraryConfig()
			resolved.Analysis.MinConfidence = held.minimum
			in := suppressed(t, "selfcheck-dormant.txtar", resolved)

			if got := staleFindings(t, in); len(got) != 0 {
				t.Errorf("StaleSuppressions() under %s = %+v, want no finding: the record bound a candidate",
					name, got)
			}
			if inEffect, reasons := Totals(in); inEffect != held.inEffect || reasons != 1 {
				t.Errorf("Totals() under %s = %d in effect and %d reasons, want %d and 1",
					name, inEffect, reasons, held.inEffect)
			}
		})
	}
}

func TestStaleSuppressionsKeepsARecordNamingARetiredCodeStale(t *testing.T) {
	resolved := applicationConfig()
	// DS1400 is a retired code, so nothing reports under it and no severity key can
	// name it: a record naming one is silenced by nothing and can match nothing.
	in := suppressed(t, "selfcheck-dormant.txtar", resolved)
	for i := range in.Marks {
		in.Marks[i].Code = "DS1400"
	}
	if got := staleFindings(t, in); len(got) != 1 {
		t.Errorf("StaleSuppressions() over a record naming a retired code = %+v, want one stale record", got)
	}
}

func TestStaleSuppressionsCollapsesTheRecordsAtOneSiteIntoOneFindingNamingEveryCode(t *testing.T) {
	in := suppressed(t, "selfcheck-collapse.txtar", applicationConfig())
	const collapsedCodes = "inline directive for DS1001 and DS1101 matches no current finding"

	want := []staleAt{
		{path: "main.go", line: 5, mechanism: "inline", message: collapsedCodes},
		{path: "main.go", line: 9, mechanism: "inline", message: staleInline},
	}
	if got := staleFindings(t, in); !slices.Equal(got, want) {
		t.Errorf("StaleSuppressions() = %+v, want %+v: four records at two sites are two findings", got, want)
	}
	// A reason is written once per directive whatever the number of records the
	// directive produced, so the four records of this fixture are two reasons.
	if _, reasons := Totals(in); reasons != 2 {
		t.Errorf("Totals() = %d reasons over four records at two sites, want 2", reasons)
	}
}

func TestNoSeverityKeyReducesAStaleSuppressionOrAnUnmatchedRoot(t *testing.T) {
	for _, key := range []string{"DS1703", "DS1704", "DS17"} {
		t.Run(key, func(t *testing.T) {
			document := []byte(`{"target": {"kind": "application"}, "severity": {"` + key + `": "warn"}}`)
			_, _, err := config.Resolve(config.Inputs{Repository: document, RepositoryLabel: "deadset.json"})
			var refusal *config.Error
			if !errors.As(err, &refusal) || refusal.Kind != config.KindUnimplementedKey {
				t.Fatalf("config.Resolve(a severity key naming %s) = %v, want an unimplemented-key refusal", key, err)
			}
		})
	}
}

func TestTheRefusalKindsReportEachRefusalAsTheSuppressionWasWritten(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())
	in.Refusals = []suppress.Refusal{
		{
			Reported:  suppressionWithoutReasonCode,
			Code:      "DS1001",
			Path:      "main.go",
			Site:      position("main.go", 21, 1),
			Mechanism: suppress.MechanismInline,
		},
		{
			Reported:  unscopedEntryCode,
			Code:      "DS1002",
			Symbol:    "go://example.com/selfcheck#Dropped",
			Reason:    "an entry naming a symbol and no path",
			Site:      position(suppress.IgnoreFileName, 4, 5),
			Mechanism: suppress.MechanismIgnore,
		},
	}

	// Each kind reports the refusals of its own code and no other, which is what
	// lets both stand in the emitters table.
	reasonless := oneRefusal(t, SuppressionsWithoutReason, in, suppressionWithoutReasonCode)
	switch {
	case reasonless.Message != "inline directive for DS1001 carries no reason":
		t.Errorf("SuppressionsWithoutReason() reports %q, want the sentence naming the mechanism and the code",
			reasonless.Message)
	case reasonless.Details.Mechanism != "inline":
		t.Errorf("SuppressionsWithoutReason() names mechanism %q, want inline", reasonless.Details.Mechanism)
	case reasonless.Details.Entry == nil || reasonless.Details.Entry.Reason != "":
		t.Errorf("SuppressionsWithoutReason() carries %+v, want the record with its reason empty", reasonless.Details.Entry)
	case reasonless.Symbol.Ref != "go://example.com/selfcheck#main.go:file":
		t.Errorf("SuppressionsWithoutReason() names %q, want the file form of the file that holds the directive",
			reasonless.Symbol.Ref)
	}

	unscoped := oneRefusal(t, UnscopedEntries, in, unscopedEntryCode)
	switch {
	case unscoped.Message != "ignore entry for DS1002 names a symbol and no path":
		t.Errorf("UnscopedEntries() reports %q, want the sentence naming the mechanism and the code",
			unscoped.Message)
	case unscoped.Details.Entry == nil || unscoped.Details.Entry.Path != "":
		t.Errorf("UnscopedEntries() carries %+v, want the record with its path empty", unscoped.Details.Entry)
	case unscoped.Symbol.Ref != "go://example.com/selfcheck#Dropped":
		t.Errorf("UnscopedEntries() names %q, want the reference the entry spells", unscoped.Symbol.Ref)
	}
}

// oneRefusal is the one finding a refusal kind reports about an input holding one
// refusal of its code, and fails the test where it reports anything else.
func oneRefusal(t *testing.T, emit Emitter, in *Input, code string) Finding {
	t.Helper()

	found, err := emit(in)
	if err != nil {
		t.Fatalf("the %s emitter = _, %v, want one finding per refusal of its code", code, err)
	}
	if len(found) != 1 {
		t.Fatalf("the %s emitter reported %d findings, want 1: %+v", code, len(found), summary(found))
	}
	if found[0].Code != code {
		t.Fatalf("the %s emitter reports %s", code, found[0].Code)
	}
	return found[0]
}

func TestAnEntryLackingBothItsReasonAndItsPathIsTwoFindingsAtOnePosition(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())
	site := position(suppress.IgnoreFileName, 4, 5)
	refusal := suppress.Refusal{
		Code:      "DS1002",
		Symbol:    "go://example.com/selfcheck#Dropped",
		Site:      site,
		Mechanism: suppress.MechanismIgnore,
	}
	refusal.Reported = suppressionWithoutReasonCode
	in.Refusals = []suppress.Refusal{refusal}
	refusal.Reported = unscopedEntryCode
	in.Refusals = append(in.Refusals, refusal)

	result := computed(t, in, map[string]Emitter{
		suppressionWithoutReasonCode: SuppressionsWithoutReason,
		unscopedEntryCode:            UnscopedEntries,
	})

	want := []string{suppressionWithoutReasonCode, unscopedEntryCode}
	if got := codesOf(result.Findings); !slices.Equal(got, want) {
		t.Fatalf("the pass over one entry breaking two rules of the grammar reports %v, want %v: the two checks are separate and both run",
			got, want)
	}
	for i := range result.Findings {
		if at := result.Findings[i].Position; at.Path != site.Filename || at.Line != site.Line || at.Column != site.Column {
			t.Errorf("%s reports at %s:%d:%d, want %s:%d:%d: both findings are about the one record",
				result.Findings[i].Code, at.Path, at.Line, at.Column, site.Filename, site.Line, site.Column)
		}
	}
}

func TestADirectiveNamingTwoCodesAndNoReasonIsOneFindingPerCodeAtOnePosition(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())
	site := position("main.go", 21, 1)
	refusal := suppress.Refusal{
		Reported:  suppressionWithoutReasonCode,
		Path:      "main.go",
		Site:      site,
		Mechanism: suppress.MechanismInline,
	}
	refusal.Code = "DS1001"
	in.Refusals = []suppress.Refusal{refusal}
	refusal.Code = "DS1002"
	in.Refusals = append(in.Refusals, refusal)

	result := computed(t, in, map[string]Emitter{suppressionWithoutReasonCode: SuppressionsWithoutReason})

	// One directive is one record per code it names, and a reader is told which of
	// them is now unsuppressed, so the two records are two findings at the one
	// position the directive is written on.
	want := []string{"DS1001", "DS1002"}
	got := make([]string, 0, len(result.Findings))
	for i := range result.Findings {
		one := &result.Findings[i]
		if one.Details.Entry == nil {
			t.Fatalf("%s at %s:%d carries no record, want the record the directive names",
				one.Code, one.Position.Path, one.Position.Line)
		}
		got = append(got, one.Details.Entry.Code)
		switch {
		case one.Code != suppressionWithoutReasonCode:
			t.Errorf("the pass reports %s, want every record under %s", one.Code, suppressionWithoutReasonCode)
		case one.Position.Path != site.Filename || one.Position.Line != site.Line ||
			one.Position.Column != site.Column:
			t.Errorf("the record of %s is sited at %s:%d:%d, want %s:%d:%d, the position the directive is written on",
				one.Details.Entry.Code, one.Position.Path, one.Position.Line, one.Position.Column,
				site.Filename, site.Line, site.Column)
		case one.Message != written(suppress.MechanismInline)+" for "+one.Details.Entry.Code+" carries no reason":
			t.Errorf("the record of %s reports %q, want the sentence naming the mechanism and that code",
				one.Details.Entry.Code, one.Message)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("the pass over one reasonless directive naming two codes reports the records %v, want %v: each code the directive names is a record of its own",
			got, want)
	}
}

func TestTheRefusalKindsRefuseARefusalUnderACodeTheGrammarDoesNotReport(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())
	in.Refusals = []suppress.Refusal{{
		Reported:  staleSuppressionCode,
		Code:      "DS1001",
		Path:      "main.go",
		Site:      position("main.go", 5, 1),
		Mechanism: suppress.MechanismInline,
	}}

	if _, err := SuppressionsWithoutReason(in); !errors.Is(err, ErrEmitter) {
		t.Errorf("SuppressionsWithoutReason() over a refusal reported under %s = %v, want an error satisfying errors.Is(err, ErrEmitter)",
			staleSuppressionCode, err)
	}
}

func TestUnmatchedRootsReportsEveryConfiguredStringAsTheConfigurationSpellsIt(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())
	in.Unmatched = []graph.Unmatched{
		{Source: "go://example.com/selfcheck#Vanished"},
		{Source: "go://example.com/selfcheck#Vanish*"},
	}

	// Through the framework, and two strings the configuration spells at one document
	// position are two findings: the run cannot name the line a root is written on,
	// and a row is identified by the record it reports rather than by that position.
	found := computed(t, in, map[string]Emitter{unmatchedRootCode: UnmatchedRoots}).Findings
	if len(found) != 2 {
		t.Fatalf("the unmatched-root kind reported %d findings, want 2: %+v", len(found), summary(found))
	}
	for i, want := range []struct {
		ref     string
		message string
	}{
		// The canonical key orders two findings at one position by the reference each
		// names, which is what tells the rows of one document apart.
		{ref: "go://example.com/selfcheck#Vanish*", message: "configured root pattern matches no symbol of the inventory"},
		{ref: "go://example.com/selfcheck#Vanished", message: "configured root matches no symbol of the inventory"},
	} {
		one := found[i]
		switch {
		case one.Code != unmatchedRootCode:
			t.Errorf("UnmatchedRoots() reports %s, want %s", one.Code, unmatchedRootCode)
		case one.Symbol.Ref != want.ref || one.Symbol.Name != want.ref:
			t.Errorf("UnmatchedRoots() names %q and %q, want the configured string %q twice",
				one.Symbol.Ref, one.Symbol.Name, want.ref)
		case one.Symbol.Kind != rootSubject:
			t.Errorf("UnmatchedRoots() names the subject %q, want %q", one.Symbol.Kind, rootSubject)
		case one.Message != want.message:
			t.Errorf("UnmatchedRoots() reports %q, want %q", one.Message, want.message)
		case one.Severity != config.Deny:
			t.Errorf("UnmatchedRoots() reports at %s, want %s: the kind is fixed on at deny", one.Severity, config.Deny)
		case one.Position.Path != configurationDocument || one.Position.Line != 1 || one.Position.Column != 1:
			t.Errorf("UnmatchedRoots() sites the finding at %s:%d:%d, want %s:1:1",
				one.Position.Path, one.Position.Line, one.Position.Column, configurationDocument)
		}
	}
}

func TestAMarkOnADeadComponentsRootLeavesTheSymbolsOnlyThatRootReferencesUnreported(t *testing.T) {
	in := suppressed(t, "selfcheck-seed.txtar", applicationConfig())
	result := computed(t, in, declarationEmitters())

	for _, name := range []string{"root", "helper", "deeper"} {
		if slices.Contains(namesUnder(result.Findings, unusedUnexportedCode), name) {
			t.Errorf("the pass reports %s, want nothing: the mark on root seeds the closure through it", name)
		}
	}
	if got := staleFindings(t, in); len(got) != 0 {
		t.Errorf("StaleSuppressions() = %+v, want nothing: the mark held the component back", got)
	}
	if inEffect, _ := Totals(in); inEffect != 1 {
		t.Errorf("Totals() = %d in effect, want 1", inEffect)
	}
}

func TestWithoutTheMarkTheDeadComponentsRootIsReportedAndTheComponentFalls(t *testing.T) {
	in := suppressed(t, "selfcheck-seed.txtar", applicationConfig())
	// The same input with the mark withdrawn, which is the run a maintainer makes
	// after deleting the directive.
	in.Marks = nil
	swept := graph.NewMatrix(in.Merged).Sweep(graph.SweepInput{Exempt: in.Exempt, Mode: graph.Mode{Production: true}})
	in.Sweep = &swept
	in.indexed = nil

	result := computed(t, in, declarationEmitters())
	root := findingOf(t, result.Findings, unusedUnexportedCode, "root")
	if !root.Component.Root {
		t.Errorf("the pass reports root outside its component's roots, want it at the root")
	}
	if root.Component.SymbolCount != 3 {
		t.Errorf("the component of root counts %d symbols, want 3: helper and deeper fall with it",
			root.Component.SymbolCount)
	}
}

// position is one rendered site, spelled the way a reader returns it.
func position(path string, line, column int) token.Position {
	return token.Position{Filename: path, Line: line, Column: column}
}

func TestASuppressionWithholdsTheFindingItsCodeWouldHaveProducedWhateverTheKind(t *testing.T) {
	in := suppressed(t, "selfcheck-withheld.txtar", applicationConfig())
	if len(in.Marks) != 4 {
		t.Fatalf("Setup: the fixture's directives read as %d records, want 4: %+v", len(in.Marks), in.Marks)
	}
	for i := range in.Marks {
		if in.Marks[i].Bound == "" {
			t.Fatalf("Setup: the record for %s at %s bound no declaration",
				in.Marks[i].Code, in.Marks[i].Site)
		}
	}

	// The pass with the fixture's directives withdrawn, which is the run a maintainer
	// makes after deleting them: it is what says the two withheld findings exist.
	bare := suppressed(t, "selfcheck-withheld.txtar", applicationConfig())
	bare.Marks = nil
	swept := graph.NewMatrix(bare.Merged).Sweep(graph.SweepInput{Exempt: bare.Exempt, Mode: graph.Mode{Production: true}})
	bare.Sweep = &swept
	bare.indexed = nil
	want := []string{
		"DS1101 Narrowed",
		"DS1801 tag",
	}
	if got := summary(computed(t, bare, packageEmitters()).Findings); !slices.Equal(got, want) {
		t.Fatalf("the pass with no suppression reports %v, want %v", got, want)
	}

	// The same pass with the records: each of the two withholds the finding its code
	// names, the record over a declaration no narrowing reports withheld nothing, and
	// the record naming the stale-suppression code withheld nothing either, because
	// no record binds to a row of a document. The two stale findings are in the
	// canonical order, which is the line each directive is written on.
	result := computed(t, in, packageEmitters())
	if got := summary(result.Findings); !slices.Equal(got, []string{
		"DS1703 go://example.com/app/internal/store#Shared",
		"DS1703 go://example.com/app/internal/store#Rowless",
	}) {
		t.Errorf("the pass with the fixture's directives reports %v, want the two stale records alone", got)
	}
	if inEffect, reasons := Totals(in); inEffect != 2 || reasons != 4 {
		t.Errorf("Totals() = %d in effect and %d reasons, want 2 and 4", inEffect, reasons)
	}
}

func TestTheStaleSuppressionKindReadsWhatTheSamePassWithheldWhateverTheVocabularysOrder(t *testing.T) {
	in := suppressed(t, "selfcheck-withheld.txtar", applicationConfig())

	// The unused-parameter kind runs after the stale-suppression kind in the
	// vocabulary's order, so a pass that answered staleness in that order would call
	// the record over tagged stale. Only these two kinds run, so the pass has nothing
	// else that could withhold a finding.
	found := computed(t, in, map[string]Emitter{
		staleSuppressionCode: StaleSuppressions,
		unusedParameterCode:  UnusedParameter,
	}).Findings

	stale := make([]string, 0, len(found))
	for i := range found {
		if found[i].Code == staleSuppressionCode {
			stale = append(stale, found[i].Symbol.Ref)
		}
	}
	// The narrowing kind does not run in this pass, so the record over Narrowed
	// withheld nothing here and is stale beside the records over Shared and Rowless.
	want := []string{
		"go://example.com/app/internal/store#Narrowed",
		"go://example.com/app/internal/store#Shared",
		"go://example.com/app/internal/store#Rowless",
	}
	if !slices.Equal(stale, want) {
		t.Errorf("the pass reports %v stale, want %v: the record over tagged withheld an unused-parameter finding",
			stale, want)
	}
}

func TestARecordIsDormantRatherThanInEffectWhereTheMinimumConfidenceWithholdsItsFindingToo(t *testing.T) {
	for name, held := range map[string]struct {
		minimum  config.Confidence
		inEffect int
	}{
		"a minimum the withheld finding reaches":      {minimum: config.Possible, inEffect: 1},
		"a minimum the withheld finding cannot reach": {minimum: config.Certain, inEffect: 0},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := libraryConfig()
			resolved.Consumers.Complete = true
			resolved.Analysis.MinConfidence = held.minimum
			in := suppressed(t, "selfcheck-withheld-dormant.txtar", resolved)
			// The narrowing kinds need the declaration the configuration makes and
			// the run's own consumer knowledge, which the harness carries beside the
			// resolved configuration the way the composition root hands both on.
			in.Consumers.Complete = true

			// The narrowing finding is withheld either way, so what the two runs
			// differ in is whether the record that withheld it is counted.
			for _, found := range computed(t, in, packageEmitters()).Findings {
				if found.Code == unnecessaryExportCode || found.Code == staleSuppressionCode {
					t.Errorf("the pass under %s reports %s about %s, want neither: the record withheld the narrowing finding",
						name, found.Code, found.Symbol.Name)
				}
			}
			if inEffect, reasons := Totals(in); inEffect != held.inEffect || reasons != 1 {
				t.Errorf("Totals() under %s = %d in effect and %d reasons, want %d and 1",
					name, inEffect, reasons, held.inEffect)
			}
		})
	}
}
