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
	swept := graph.NewMatrix(in.Merged).Sweep(graph.Mode{
		Exempt:     in.Exempt,
		Production: true,
		Marked:     boundMarks(in.Marks),
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
	found, err := StaleSuppressions(in)
	if err != nil {
		t.Fatalf("StaleSuppressions() = _, %v, want the stale records", err)
	}
	if len(found) == 0 {
		t.Fatal("StaleSuppressions() reports nothing, want the fixture's stale records")
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
	entry := found[3]
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
	directive := found[1]
	if directive.Symbol.Ref != "go://example.com/selfcheck#main.go:file" {
		t.Errorf("the record of the directive that bound nothing names %q, want the file form of main.go",
			directive.Symbol.Ref)
	}
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

func TestSuppressionRefusalsReportsEachRefusalAsTheSuppressionWasWritten(t *testing.T) {
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

	found, err := SuppressionRefusals(in)
	if err != nil {
		t.Fatalf("SuppressionRefusals() = _, %v, want one finding per refusal", err)
	}
	if len(found) != 2 {
		t.Fatalf("SuppressionRefusals() reported %d findings, want 2: %+v", len(found), summary(found))
	}

	reasonless := found[0]
	switch {
	case reasonless.Code != suppressionWithoutReasonCode:
		t.Errorf("SuppressionRefusals() reports %s, want %s", reasonless.Code, suppressionWithoutReasonCode)
	case reasonless.Message != "inline directive for DS1001 carries no reason":
		t.Errorf("SuppressionRefusals() reports %q, want the sentence naming the mechanism and the code",
			reasonless.Message)
	case reasonless.Details.Mechanism != "inline":
		t.Errorf("SuppressionRefusals() names mechanism %q, want inline", reasonless.Details.Mechanism)
	case reasonless.Details.Entry == nil || reasonless.Details.Entry.Reason != "":
		t.Errorf("SuppressionRefusals() carries %+v, want the record with its reason empty", reasonless.Details.Entry)
	case reasonless.Symbol.Ref != "go://example.com/selfcheck#main.go:file":
		t.Errorf("SuppressionRefusals() names %q, want the file form of the file that holds the directive",
			reasonless.Symbol.Ref)
	}

	unscoped := found[1]
	switch {
	case unscoped.Code != unscopedEntryCode:
		t.Errorf("SuppressionRefusals() reports %s, want %s", unscoped.Code, unscopedEntryCode)
	case unscoped.Message != "ignore entry for DS1002 names a symbol and no path":
		t.Errorf("SuppressionRefusals() reports %q, want the sentence naming the mechanism and the code",
			unscoped.Message)
	case unscoped.Details.Entry == nil || unscoped.Details.Entry.Path != "":
		t.Errorf("SuppressionRefusals() carries %+v, want the record with its path empty", unscoped.Details.Entry)
	case unscoped.Symbol.Ref != "go://example.com/selfcheck#Dropped":
		t.Errorf("SuppressionRefusals() names %q, want the reference the entry spells", unscoped.Symbol.Ref)
	}
}

func TestSuppressionRefusalsRefusesARefusalUnderACodeTheGrammarDoesNotReport(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())
	in.Refusals = []suppress.Refusal{{
		Reported:  staleSuppressionCode,
		Code:      "DS1001",
		Path:      "main.go",
		Site:      position("main.go", 5, 1),
		Mechanism: suppress.MechanismInline,
	}}

	if _, err := SuppressionRefusals(in); !errors.Is(err, ErrEmitter) {
		t.Errorf("SuppressionRefusals() over a refusal reported under %s = %v, want an error satisfying errors.Is(err, ErrEmitter)",
			staleSuppressionCode, err)
	}
}

func TestUnmatchedRootsReportsEveryConfiguredStringAsTheConfigurationSpellsIt(t *testing.T) {
	in := suppressed(t, "selfcheck-stale.txtar", applicationConfig())
	in.Unmatched = []graph.Unmatched{
		{Source: "go://example.com/selfcheck#Vanished"},
		{Source: "go://example.com/selfcheck#Vanish*"},
	}

	found, err := UnmatchedRoots(in)
	if err != nil {
		t.Fatalf("UnmatchedRoots() = _, %v, want one finding per configured string", err)
	}
	if len(found) != 2 {
		t.Fatalf("UnmatchedRoots() reported %d findings, want 2: %+v", len(found), summary(found))
	}
	for i, want := range []struct {
		ref     string
		message string
	}{
		{ref: "go://example.com/selfcheck#Vanished", message: "configured root matches no symbol of the inventory"},
		{ref: "go://example.com/selfcheck#Vanish*", message: "configured root pattern matches no symbol of the inventory"},
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
	swept := graph.NewMatrix(in.Merged).Sweep(graph.Mode{Exempt: in.Exempt, Production: true})
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
