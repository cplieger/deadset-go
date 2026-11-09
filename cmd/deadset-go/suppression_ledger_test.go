package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// ruled is the report one analyze run over a suppression-ledger fixture writes:
// every finding as its code and line, every stale suppression as its line and
// as its message, and the totals.
type ruled struct {
	findings []string
	stale    []string
	messages []string
	totals   report.Totals
}

func ruledOf(t *testing.T, fixture string, args ...string) ruled {
	t.Helper()
	return ruledIn(t, archiveModule(t, "suppression-ledger/"+fixture), args...)
}

// ruledIn is [ruledOf] over a module already written to dir.
func ruledIn(t *testing.T, dir string, args ...string) ruled {
	t.Helper()

	got := runAnalyze(t, dir, args...)
	if got.code != vocab.ExitClean && got.code != vocab.ExitFindings {
		t.Fatalf("analyze over %s = %d, want a verdict\nstderr: %s", dir, got.code, got.stderr)
	}
	envelope := envelopeAt(t, got.reportPath)
	var r ruled
	for i := range envelope.Findings {
		found := &envelope.Findings[i]
		r.findings = append(r.findings, string(found.Code)+" "+found.Position.Path+":"+strconv.Itoa(found.Position.Line))
	}
	for i := range envelope.StaleSuppressions {
		r.stale = append(r.stale, envelope.StaleSuppressions[i].Position.Path+":"+strconv.Itoa(envelope.StaleSuppressions[i].Position.Line))
		r.messages = append(r.messages, envelope.StaleSuppressions[i].Message)
	}
	r.totals = envelope.Totals
	return r
}

func TestARecordNamingACodeAboutNoDeclarationMarksNothing(t *testing.T) {
	t.Parallel()
	got := ruledOf(t, "record-subject-code-marks-nothing.txtar")

	want := []string{"DS1002 main.go:6", "DS1002 main.go:9", "DS1002 main.go:12", "DS1002 main.go:15", "DS1002 main.go:18"}
	if !slices.Equal(got.findings, want) {
		t.Errorf("findings = %q, want %q: no record keeps its function live", got.findings, want)
	}
	if want := []string{"main.go:5", "main.go:8", "main.go:11", "main.go:14", "main.go:17"}; !slices.Equal(got.stale, want) {
		t.Errorf("stale suppressions = %q, want %q: no finding about a function carries a record, file or import code", got.stale, want)
	}
	want = []string{
		"inline directive for DS9003 on go://example.com/app#record matches no current finding, and its declaration reports DS1002",
		"inline directive for DS9004 on go://example.com/app#root matches no current finding, and its declaration reports DS1002",
		"inline directive for DS1601 on go://example.com/app#built matches no current finding, and its declaration reports DS1002",
		"inline directive for DS1602 on go://example.com/app#imported matches no current finding, and its declaration reports DS1002",
		"inline directive for DS1004 on go://example.com/app#binding matches no current finding, and its declaration reports DS1002",
	}
	if !slices.Equal(got.messages, want) {
		t.Errorf("stale messages = %q, want %q: each names the code its function reports", got.messages, want)
	}
	if got.totals.SuppressionsInEffect != 0 {
		t.Errorf("suppressions in effect = %d, want 0", got.totals.SuppressionsInEffect)
	}
}

func TestALivenessRecordIsInEffectOnlyUnderTheCodeItsDeclarationWouldCarry(t *testing.T) {
	t.Parallel()
	got := ruledOf(t, "wrong-liveness-code.txtar")

	if len(got.findings) != 0 {
		t.Errorf("findings = %q, want none: each mark keeps its declaration live", got.findings)
	}
	want := []string{
		"lib/lib.go:3", "main.go:7", "main.go:12", "main.go:15", "main.go:18",
		"main.go:21", "main.go:24", "main.go:27", "main.go:30", "main.go:33",
	}
	if !slices.Equal(got.stale, want) {
		t.Errorf("stale suppressions = %q, want %q: each record names a code its declaration would not carry", got.stale, want)
	}
	reports := []string{"DS1001", "DS1002", "DS1104", "DS1002", "DS1002", "DS1002", "DS1101", "DS1002", "DS1002", "DS1002"}
	for i, message := range got.messages {
		if i < len(reports) && !strings.HasSuffix(message, " matches no current finding, and its declaration reports "+reports[i]) {
			t.Errorf("stale message at %s = %q, want it to name %s, the code its declaration reports", got.stale[i], message, reports[i])
		}
	}
	if got.totals.SuppressionsInEffect != 0 {
		t.Errorf("suppressions in effect = %d, want 0", got.totals.SuppressionsInEffect)
	}
}

func TestADirectiveInAFileOneConfigurationCompilesIsRead(t *testing.T) {
	t.Parallel()
	got := ruledOf(t, "directive-in-a-second-configuration.txtar")

	if len(got.findings) != 0 || len(got.stale) != 0 {
		t.Errorf("findings = %q, stale suppressions = %q, want neither: each directive withholds its function", got.findings, got.stale)
	}
	if got.totals.SuppressionsInEffect != 2 {
		t.Errorf("suppressions in effect = %d, want 2: one record per directive, whichever configurations read it", got.totals.SuppressionsInEffect)
	}
}

func TestADirectiveBindsTheDeclarationWrittenBelowItWhateverALineDirectiveFoldsOntoItsLine(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"directive-fold-one-file.txtar", "directive-fold-two-files.txtar"} {
		t.Run(fixture, func(t *testing.T) {
			got := ruledOf(t, fixture)

			if want := []string{"DS1002 main.go:20"}; !slices.Equal(got.findings, want) || len(got.stale) != 0 {
				t.Errorf("findings = %q, stale suppressions = %q, want %q and none: the directive withholds alpha alone", got.findings, got.stale, want)
			}
			if got.totals.SuppressionsInEffect != 1 {
				t.Errorf("suppressions in effect = %d, want 1: bravo carries no record", got.totals.SuppressionsInEffect)
			}
		})
	}
}

func TestEachOfTwoDirectivesALineDirectiveRendersAtOneSiteIsItsOwnSuppression(t *testing.T) {
	t.Parallel()
	got := ruledOf(t, "directive-fold-stale.txtar")

	if len(got.findings) != 0 {
		t.Errorf("findings = %q, want none: alpha's directive withholds it", got.findings)
	}
	if want := []string{"main.go:19", "main.go:19"}; !slices.Equal(got.stale, want) {
		t.Errorf("stale suppressions = %q, want %q: the directives above bravo and charlie", got.stale, want)
	}
	if got.totals.SuppressionsInEffect != 1 || got.totals.ReasonsRecorded != 3 {
		t.Errorf("in effect %d, reasons recorded %d, want 1 and 3", got.totals.SuppressionsInEffect, got.totals.ReasonsRecorded)
	}
}

func TestEachOfTwoReasonlessDirectivesALineDirectiveRendersAtOneSiteIsAFinding(t *testing.T) {
	t.Parallel()
	reasonless := ruledOf(t, "reasonless-fold.txtar")
	want := []string{"DS9001 main.go:19", "DS9001 main.go:19", "DS1002 main.go:20", "DS1002 main.go:20"}
	if !slices.Equal(reasonless.findings, want) {
		t.Errorf("findings = %q, want %q: each directive lacking a reason is a finding and binds nothing", reasonless.findings, want)
	}
}

func TestARecordWhoseFindingTheComponentCapWithholdsIsDormant(t *testing.T) {
	t.Parallel()
	dir := archiveModule(t, "suppression-ledger/dormant-through-the-component.txtar")
	got := ruledIn(t, dir)
	if len(got.findings) != 0 || len(got.stale) != 0 || got.totals.SuppressionsInEffect != 0 {
		t.Errorf("findings = %q, stale suppressions = %q, in effect %d, want none of them: alpha's finding would be possible, which the default minimum withholds",
			got.findings, got.stale, got.totals.SuppressionsInEffect)
	}
	if lowest := ruledIn(t, dir, "--min-confidence=possible"); lowest.totals.SuppressionsInEffect != 1 {
		t.Errorf("suppressions in effect at the lowest minimum = %d, want 1", lowest.totals.SuppressionsInEffect)
	}
}

func TestAnEntryOfAnotherSchemeIsAcceptedUnread(t *testing.T) {
	t.Parallel()
	got := ruledOf(t, "other-scheme-unread.txtar")

	if len(got.findings) != 0 || len(got.stale) != 0 {
		t.Errorf("findings = %q, stale suppressions = %q, want neither: another analyzer reads those entries", got.findings, got.stale)
	}
	if got.totals.SuppressionsInEffect != 0 || got.totals.ReasonsRecorded != 0 {
		t.Errorf("in effect %d, reasons recorded %d, want 0 and 0: an entry of another scheme counts in neither",
			got.totals.SuppressionsInEffect, got.totals.ReasonsRecorded)
	}
}

func TestAMalformedDirectiveIsAUsageError(t *testing.T) {
	t.Parallel()
	got := runAnalyze(t, archiveModule(t, "suppression-ledger/malformed-directive.txtar"))

	if got.code != vocab.ExitUsage || !strings.Contains(got.stderr, "main.go:5") {
		t.Errorf("analyze = %d, stderr %q, want %d naming main.go:5", got.code, got.stderr, vocab.ExitUsage)
	}
}

func TestARecordOnAMemberOfADeadContainerIsDecidedAgainstTheMembersOwnFinding(t *testing.T) {
	t.Parallel()
	got := ruledOf(t, "record-on-a-member-of-a-dead-container.txtar")

	if want := []string{"DS1002 main.go:5", "DS1002 main.go:11", "DS1201 main.go:16"}; !slices.Equal(got.findings, want) {
		t.Errorf("findings = %q, want %q: each container is reported, and the marked method keeps its type live", got.findings, want)
	}
	if want := []string{"main.go:12"}; !slices.Equal(got.stale, want) {
		t.Errorf("stale suppressions = %q, want %q", got.stale, want)
	}
	if got.totals.SuppressionsInEffect != 3 {
		t.Errorf("suppressions in effect = %d, want 3", got.totals.SuppressionsInEffect)
	}
}

func TestARecordOnASatisfactionAssertionNamesItsFindingByTheInterfacesReference(t *testing.T) {
	t.Parallel()
	got := ruledOf(t, "record-on-a-satisfaction-assertion.txtar")

	for _, found := range got.findings {
		if strings.HasPrefix(found, "DS1203 ") {
			t.Errorf("findings = %q, want no DS1203: both records withhold the assertions' findings", got.findings)
		}
	}
	if len(got.stale) != 0 || got.totals.SuppressionsInEffect != 2 {
		t.Errorf("stale suppressions = %q and %d in effect, want none and 2", got.stale, got.totals.SuppressionsInEffect)
	}
}

func TestARecordOnAnInterfaceNamesTheAssertionAtItsOwnPath(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{
		"entry-on-an-interface-asserted-in-two-files.txtar",
		"directive-on-an-interface-asserted-in-two-files.txtar",
	} {
		t.Run(fixture, func(t *testing.T) {
			got := ruledOf(t, fixture)

			var assertions []string
			for _, found := range got.findings {
				if strings.HasPrefix(found, "DS1203 ") {
					assertions = append(assertions, found)
				}
			}
			if !slices.Equal(assertions, []string{"DS1203 a.go:7"}) {
				t.Errorf("findings = %q, want DS1203 a.go:7 alone: the record names the assertion in z.go", got.findings)
			}
			if len(got.stale) != 0 || got.totals.SuppressionsInEffect != 1 {
				t.Errorf("stale suppressions = %q and %d in effect, want none and 1", got.stale, got.totals.SuppressionsInEffect)
			}
		})
	}
}

// assertionsOf is every DS1203 finding of a report, in report order.
func assertionsOf(r ruled) []string {
	var assertions []string
	for _, found := range r.findings {
		if strings.HasPrefix(found, "DS1203 ") {
			assertions = append(assertions, found)
		}
	}
	return assertions
}

func TestARecordOnAnInterfaceNamesEveryAssertionOfItInItsFile(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{
		"entry-on-an-interface-asserted-twice-in-one-file.txtar",
		"directive-on-an-interface-asserted-twice-in-one-file.txtar",
	} {
		t.Run(fixture, func(t *testing.T) {
			got := ruledOf(t, fixture)

			if assertions := assertionsOf(got); len(assertions) != 0 {
				t.Errorf("findings = %q, want no DS1203: the record names both assertions", got.findings)
			}
			if len(got.stale) != 0 || got.totals.SuppressionsInEffect != 1 {
				t.Errorf("stale suppressions = %q and %d in effect, want none and 1", got.stale, got.totals.SuppressionsInEffect)
			}
		})
	}
}

func TestADirectiveOnAnInterfaceNamesTheAssertionsWrittenInItsFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture string
		want    []string
	}{
		{"directive-on-an-interface-folded-with-an-assertion-elsewhere.txtar", []string{"DS1203 gen.proto:2"}},
		{"directive-on-an-interface-rendered-under-another-path.txtar", nil},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := ruledOf(t, tc.fixture)

			if assertions := assertionsOf(got); !slices.Equal(assertions, tc.want) {
				t.Errorf("findings = %q, want DS1203 %q: the directive names the assertion written in z.go, whatever path renders it", got.findings, tc.want)
			}
			if len(got.stale) != 0 || got.totals.SuppressionsInEffect != 1 {
				t.Errorf("stale suppressions = %q and %d in effect, want none and 1", got.stale, got.totals.SuppressionsInEffect)
			}
		})
	}
}

func TestADirectiveOnOneFoldedAssertionWithholdsThatAssertionAlone(t *testing.T) {
	t.Parallel()
	got := runAnalyze(t, archiveModule(t, "suppression-ledger/directive-on-one-folded-assertion.txtar"))
	envelope := envelopeAt(t, got.reportPath)

	var assertions []string
	for i := range envelope.Findings {
		if found := &envelope.Findings[i]; found.Code == vocab.UnusedSatisfactionAssertion {
			assertions = append(assertions, found.Position.Path+":"+strconv.Itoa(found.Position.Line)+":"+strconv.Itoa(found.Position.Column))
		}
	}
	if !slices.Equal(assertions, []string{"check.txt:10:7"}) {
		t.Errorf("DS1203 findings = %q, want check.txt:10:7, the assertion in z.go: the directive withholds its own assertion alone", assertions)
	}
	if len(envelope.StaleSuppressions) != 0 || envelope.Totals.SuppressionsInEffect != 1 {
		t.Errorf("%d stale and %d in effect, want none stale and 1 in effect",
			len(envelope.StaleSuppressions), envelope.Totals.SuppressionsInEffect)
	}
}

func TestADirectiveOnOneFoldedPartHolderWithholdsThatAlternativesPartAlone(t *testing.T) {
	t.Parallel()
	got := runAnalyze(t, archiveModule(t, "suppression-ledger/directive-on-one-folded-part-holder.txtar"))
	envelope := envelopeAt(t, got.reportPath)

	var findings []string
	for i := range envelope.Findings {
		found := &envelope.Findings[i]
		findings = append(findings, string(found.Code)+" "+strings.Join(found.Configurations, ","))
	}
	if !slices.Equal(findings, []string{"DS1404 windows-amd64"}) {
		t.Errorf("findings = %q, want the windows alternative's DS1404 alone", findings)
	}
	if len(envelope.StaleSuppressions) != 0 || envelope.Totals.SuppressionsInEffect != 1 {
		t.Errorf("%d stale and %d in effect, want none stale and 1 in effect",
			len(envelope.StaleSuppressions), envelope.Totals.SuppressionsInEffect)
	}
}

func TestADirectiveOnOnePlatformAlternativeWithholdsThatDeclarationAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ fixture, reported string }{
		{"directive-on-one-platform-alternative-linux.txtar", "windows"},
		{"directive-on-one-platform-alternative-windows.txtar", "linux"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := runAnalyze(t, archiveModule(t, "suppression-ledger/"+tc.fixture))
			envelope := envelopeAt(t, got.reportPath)

			var findings []string
			about := true
			for i := range envelope.Findings {
				found := &envelope.Findings[i]
				findings = append(findings, string(found.Code)+" "+strings.Join(found.Configurations, ","))
				for _, configuration := range found.Configurations {
					about = about && found.Code == vocab.UnreachableExport && strings.HasPrefix(configuration, tc.reported+"-")
				}
			}
			if len(findings) != 1 || !about {
				t.Errorf("findings = %q, want one DS1101 about the %s Ping: the directive withholds its own declaration alone", findings, tc.reported)
			}
			if len(envelope.StaleSuppressions) != 0 || envelope.Totals.SuppressionsInEffect != 1 {
				t.Errorf("%d stale and %d in effect, want none stale and 1 in effect",
					len(envelope.StaleSuppressions), envelope.Totals.SuppressionsInEffect)
			}
		})
	}
}

func TestAnAssertionOfAnInterfaceDeclaredPerPlatformIsOneFinding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture    string
		assertions []string
		inEffect   int
	}{
		{"assertion-of-an-interface-declared-per-platform.txtar", []string{"a.go:3:5 linux-amd64,windows-amd64"}, 0},
		{"a-folded-assertion-of-an-interface-declared-per-platform.txtar", []string{"gen.proto:20:5 linux-amd64,windows-amd64"}, 0},
		{"assertion-of-an-interface-declared-per-platform-used-on-one.txtar", nil, 0},
		{"entry-on-an-assertion-of-an-interface-declared-per-platform.txtar", nil, 1},
		{"directive-on-an-assertion-of-an-interface-declared-per-platform.txtar", nil, 1},
		{"entry-on-a-folded-assertion-of-an-interface-declared-per-platform.txtar", nil, 1},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := runAnalyze(t, archiveModule(t, "suppression-ledger/"+tc.fixture))
			if got.code != vocab.ExitClean && got.code != vocab.ExitFindings {
				t.Fatalf("analyze = %d, want a verdict\nstderr: %s", got.code, got.stderr)
			}
			envelope := envelopeAt(t, got.reportPath)

			var assertions []string
			for i := range envelope.Findings {
				if found := &envelope.Findings[i]; found.Code == vocab.UnusedSatisfactionAssertion {
					assertions = append(assertions, found.Position.Path+":"+strconv.Itoa(found.Position.Line)+":"+
						strconv.Itoa(found.Position.Column)+" "+strings.Join(found.Configurations, ","))
				}
			}
			if !slices.Equal(assertions, tc.assertions) {
				t.Errorf("DS1203 findings = %q, want %q", assertions, tc.assertions)
			}
			if len(envelope.StaleSuppressions) != 0 || envelope.Totals.SuppressionsInEffect != tc.inEffect {
				t.Errorf("%d stale and %d in effect, want none stale and %d in effect",
					len(envelope.StaleSuppressions), envelope.Totals.SuppressionsInEffect, tc.inEffect)
			}
		})
	}
}

func TestAnEntryNamesAPartByThePathThePartReports(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture         string
		findings, stale []string
		inEffect        int
	}{
		{"entry-at-the-holders-file-of-a-moved-store.txtar", []string{"DS1404 gen.txt:40"}, []string{"deadset-ignore.json:1"}, 0},
		{"entry-at-the-path-of-a-moved-store.txtar", nil, nil, 1},
		{"entry-at-an-alternatives-path-a-store-is-moved-to.txtar", nil, nil, 1},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := ruledOf(t, tc.fixture)
			if !slices.Equal(got.findings, tc.findings) || !slices.Equal(got.stale, tc.stale) {
				t.Errorf("findings %q and stale %q, want %q and %q", got.findings, got.stale, tc.findings, tc.stale)
			}
			if got.totals.SuppressionsInEffect != tc.inEffect {
				t.Errorf("suppressions in effect = %d, want %d", got.totals.SuppressionsInEffect, tc.inEffect)
			}
		})
	}
}
