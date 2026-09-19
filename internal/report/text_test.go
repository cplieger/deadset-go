package report

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
	spec "github.com/cplieger/deadset-spec"
)

// textLinePage is the Contract page that publishes the expression the text format is
// defined by, and expressionPrefix is how that expression starts, so the test compiles
// the Contract's own expression rather than a copy of it.
const (
	textLinePage     = "contract/grammar/text-line.md"
	expressionPrefix = "^(?<path>"
)

// publishedExpression is the expression the Contract publishes for a finding line.
func publishedExpression(t *testing.T) *regexp.Regexp {
	t.Helper()

	body, err := spec.Contract.ReadFile(textLinePage)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", textLinePage, err)
	}
	for line := range strings.Lines(string(body)) {
		held := strings.TrimRight(line, "\n")
		if !strings.HasPrefix(held, expressionPrefix) {
			continue
		}
		compiled, err := regexp.Compile(held)
		if err != nil {
			t.Fatalf("Setup: compile the published expression %q: %v", held, err)
		}
		return compiled
	}
	t.Fatalf("Setup: %s publishes no expression starting %q", textLinePage, expressionPrefix)
	return nil
}

// TestEveryFindingLineMatchesThePublishedExpression pins the text format against the
// expression that defines it, over the findings a real pass answers as well as over a
// hand-built envelope, and checks each captured group against the field it comes from.
func TestEveryFindingLineMatchesThePublishedExpression(t *testing.T) {
	expression := publishedExpression(t)
	in := fullInput()
	envelope := built(t, &in)
	lines := findingLines(t, &envelope)

	if want := len(envelope.Findings) + len(envelope.StaleSuppressions); len(lines) != want {
		t.Fatalf("the text rendering wrote %d lines before its summary, want %d", len(lines), want)
	}
	for i, line := range lines {
		groups := expression.FindStringSubmatch(line)
		if groups == nil {
			t.Errorf("line %d does not match the published expression: %q", i+1, line)
			continue
		}
		if i >= len(envelope.Findings) {
			continue
		}
		found := &envelope.Findings[i]
		want := []string{
			line, found.Position.Path, strconv.Itoa(found.Position.Line), strconv.Itoa(found.Position.Column),
			found.Symbol.Kind, found.Symbol.Name, found.Message, string(found.Confidence), found.Code,
		}
		for at := range want {
			if groups[at] != want[at] {
				t.Errorf("line %d group %s = %q, want %q",
					i+1, expression.SubexpNames()[at], groups[at], want[at])
			}
		}
	}
}

// TestEveryFixtureLineMatchesThePublishedExpression pins the same expression against the
// findings of a real pass over real source, which is where a message or a name that
// cannot be rendered on one line would show.
func TestEveryFixtureLineMatchesThePublishedExpression(t *testing.T) {
	expression := publishedExpression(t)
	for _, fixture := range fixtureGoldens {
		t.Run(fixture.name, func(t *testing.T) {
			envelope, _ := envelopeOfDir(t, extract(t, fixture.archive), fixture.kind)
			for i, line := range findingLines(t, &envelope) {
				if !expression.MatchString(line) {
					t.Errorf("line %d of %s does not match the published expression: %q",
						i+1, fixture.name, line)
				}
			}
		})
	}
}

// TestTheStaleSuppressionLineCarriesTheFixedTokens pins the three values a stale
// suppression's line does not read from a subject: the kind token, the confidence and
// the code.
func TestTheStaleSuppressionLineCarriesTheFixedTokens(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	lines := findingLines(t, &envelope)
	line := lines[len(lines)-1]

	want := "deadset-ignore.json:7:5: suppression go://example.com/app#Catalog.legacyAlias: " +
		"the ignore entry named DS1001 and the symbol it names reports DS1003 [certain] (DS1703)"
	if line != want {
		t.Errorf("the stale-suppression line = %q, want %q", line, want)
	}
}

// TestTheSummaryIsNotAFindingLine pins that the one line the format writes beyond the
// records does not match the expression, so a filter for finding lines yields exactly
// the finding lines.
func TestTheSummaryIsNotAFindingLine(t *testing.T) {
	expression := publishedExpression(t)
	in := fullInput()
	envelope := built(t, &in)
	Cap(&envelope, 2)

	written := strings.Split(strings.TrimRight(string(rendered(t, "text", &envelope, Options{})), "\n"), "\n")
	summary := written[len(written)-1]
	if expression.MatchString(summary) {
		t.Errorf("the summary matches the finding-line expression: %q", summary)
	}
	want := "summary: 6 findings (0 allow, 2 warn, 4 deny), 52 deletable lines, " +
		"4 suppressions in effect, 6 reasons recorded, 1 stale suppression, 1 pending, 4 omitted"
	if summary != want {
		t.Errorf("the summary = %q, want %q", summary, want)
	}
}

// TestTheTextRenderingCarriesNoAmbientDetail pins that nothing of the machine or the
// moment reaches the output, so two runs over an unchanged tree write the same bytes.
func TestTheTextRenderingCarriesNoAmbientDetail(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	first := string(rendered(t, "text", &envelope, Options{}))
	second := string(rendered(t, "text", &envelope, Options{}))

	if first != second {
		t.Errorf("two renderings of one envelope differ:\n%s", diff(first, second))
	}
	for _, absent := range []string{"/tmp", "/workspace", "elapsed", "duration", "seconds", testAnalyzerVersion} {
		if strings.Contains(first, absent) {
			t.Errorf("the text rendering holds %q, which is a detail of the run rather than of a finding:\n%s",
				absent, first)
		}
	}
}

// TestTheTextRenderingReportsAWriteFailure pins that a rendering that cannot write says
// so once rather than returning as though it had written.
func TestTheTextRenderingReportsAWriteFailure(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{findingOf("DS1002", "unused-unexported", "a.go", 1, 1,
		config.Deny, "deletable", "the function has no reference in the target")}
	envelope := built(t, &in)

	if err := Text(refusingWriter{}, &envelope, Options{}); err == nil {
		t.Error("Text(a writer that refuses) = no error, want the failure")
	}
}

// findingLines is every line of a text rendering but its summary.
func findingLines(t *testing.T, e *Envelope) []string {
	t.Helper()

	written := strings.Split(strings.TrimRight(string(rendered(t, "text", e, Options{})), "\n"), "\n")
	if len(written) == 0 {
		t.Fatal("the text rendering wrote nothing, not even a summary")
	}
	return written[:len(written)-1]
}

// refusingWriter is a writer that fails every write, which is what a rendering meets
// when the file it writes to is full or gone.
type refusingWriter struct{}

// errRefused is what a refusing writer returns.
var errRefused = errors.New("the writer refuses")

// Write fails.
func (refusingWriter) Write([]byte) (int, error) { return 0, errRefused }
