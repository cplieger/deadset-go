package report

import (
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
	"pgregory.net/rapid"
)

// drawnReport is one report the exit code is decided over: the severity of every
// finding it holds, how many stale suppressions it carries, how many of its edge
// evaluations name a dead side, and the failing severity the configuration resolved
// to. The four are the draw, and the verdict the Contract's table names for them is
// computed from the draw rather than from the assembled report, so the expectation
// is not a second reading of the same totals.
type drawnReport struct {
	severities []config.Severity
	failOn     config.Severity
	stale      int
	pending    int
}

// drawReport draws one report's shape.
func drawReport(t *rapid.T) drawnReport {
	severities := []config.Severity{config.Allow, config.Warn, config.Deny}
	count := rapid.IntRange(0, 6).Draw(t, "the number of findings")
	drawn := drawnReport{
		severities: make([]config.Severity, count),
		stale:      rapid.IntRange(0, 3).Draw(t, "the number of stale suppressions"),
		pending:    rapid.IntRange(0, 3).Draw(t, "the number of pending findings"),
		failOn:     rapid.SampledFrom(severities).Draw(t, "the failing severity"),
	}
	for i := range count {
		drawn.severities[i] = rapid.SampledFrom(severities).Draw(t, "finding "+strconv.Itoa(i)+": the severity")
	}
	return drawn
}

// envelope assembles the report one draw describes. Every finding is a distinct
// declaration, so no two of them are one record, and every stale row names a
// distinct adjudication for the same reason.
func (d drawnReport) envelope(t *rapid.T) Envelope {
	in := minimalInput()
	for i, severity := range d.severities {
		found := findingOf("DS1002", "unused-unexported", "catalog.go", 10+i, 10+i, severity,
			"deletable", "the function has no reference in the target")
		found.Symbol.Ref = "go://example.com/app#decl" + strconv.Itoa(i)
		in.Result.Findings = append(in.Result.Findings, found)
	}
	for i := range d.stale {
		in.Result.Findings = append(in.Result.Findings, staleFinding(i))
	}
	for i := range d.pending {
		in.EdgeEvaluations = append(in.EdgeEvaluations, pendingEvaluation(i))
	}
	envelope, err := Build(&in)
	if err != nil {
		t.Fatalf("Build() = error %v, want an envelope", err)
	}
	return envelope
}

// staleFinding is one stale-suppression finding, which the assembly moves out of the
// finding list into the record array the Contract declares for it.
func staleFinding(at int) kinds.Finding {
	ref := "go://example.com/app#Catalog.legacy" + strconv.Itoa(at)
	found := findingOf(staleSuppressionCode, "stale-suppression", "deadset-ignore.json", 7+at, 7+at,
		config.Deny, "none", "the ignore entry named DS1001 and the symbol it names reports nothing")
	found.Symbol.Kind = "suppression"
	found.Symbol.Ref = ref
	found.Symbol.Name = ref
	found.Details.Mechanism = "ignore"
	found.Details.Entry = &kinds.Entry{
		Code:   "DS1001",
		Symbol: ref,
		Path:   "catalog.go",
		Reason: "removal is breaking for the published API",
	}
	return found
}

// pendingEvaluation is one edge evaluation whose side is dead, which is the record
// that makes a report pending.
func pendingEvaluation(at int) EdgeEvaluation {
	ref := "go://example.com/app#Plan" + strconv.Itoa(at)
	found := findingOf("DS1001", "unused-exported", "wire.go", 9+at, 12+at, config.Deny,
		"deletable", "exported function is named by a declared edge and has no other reference")
	found.Symbol.Ref = ref
	return EdgeEvaluation{
		Edge:    "wire/plan" + strconv.Itoa(at),
		Side:    "provides",
		Symbol:  ref,
		State:   "dead",
		Finding: &found,
	}
}

// verdict is the code the Contract's table names for one draw, computed from the
// draw: four for a pending finding, then one for a stale suppression or a finding at
// or above the failing severity, then zero.
func (d drawnReport) verdict() int {
	ranked := map[config.Severity]int{config.Allow: 0, config.Warn: 1, config.Deny: 2}
	failing := 0
	for _, severity := range d.severities {
		if ranked[severity] >= ranked[d.failOn] {
			failing++
		}
	}
	switch {
	case d.pending > 0:
		return verdictPending
	case d.stale > 0 || failing > 0:
		return verdictFindings
	default:
		return verdictClean
	}
}

// TestTheExitCodeIsAFunctionOfTheReportAndTheSeverityMap is property
// dead-code-suite/P21: the code one run exits with is the code the Contract's table
// names for what its report holds, and nothing else decides it.
//
// The expectation is computed from the draw rather than from the assembled report,
// so the property is an oracle over the Contract's table and not a second reading of
// the totals the code under test reads. The three clauses the design singles out are
// asserted on top of it, because each is a rule a reader relies on: a warn finding
// alone does not fail a run, a stale suppression fails one whatever the severity map
// holds, and a pending finding outranks both.
func TestTheExitCodeIsAFunctionOfTheReportAndTheSeverityMap(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		drawn := drawReport(t)
		envelope := drawn.envelope(t)
		cfg := config.Default()
		cfg.Reporters.FailOn = drawn.failOn

		got := ExitCode(&envelope, &cfg, false)
		if want := drawn.verdict(); got != want {
			t.Fatalf("ExitCode(%d findings %v, %d stale, %d pending, fail_on %s) = %d, want %d",
				len(drawn.severities), drawn.severities, drawn.stale, drawn.pending, drawn.failOn, got, want)
		}

		// The code is a function of the report: one report presented twice, and one
		// report whose finding list is permuted, is one verdict.
		permuted := drawnReport{
			severities: shuffledSeverities(t, drawn.severities),
			stale:      drawn.stale,
			pending:    drawn.pending,
			failOn:     drawn.failOn,
		}
		other := permuted.envelope(t)
		if again := ExitCode(&other, &cfg, false); again != got {
			t.Fatalf("one report permuted exits %d and %d", got, again)
		}

		// The code configured off is the clean code for every report, and the
		// verdict above is what the caller names beside it.
		if off := ExitCode(&envelope, &cfg, true); off != verdictClean {
			t.Fatalf("ExitCode(the same report, the code configured off) = %d, want %d", off, verdictClean)
		}

		switch {
		case drawn.pending > 0:
			if got != verdictPending {
				t.Fatalf("a report holding %d pending findings exits %d, want %d", drawn.pending, got, verdictPending)
			}
		case drawn.stale > 0:
			if got != verdictFindings {
				t.Fatalf("a report holding %d stale suppressions and fail_on %s exits %d, want %d",
					drawn.stale, drawn.failOn, got, verdictFindings)
			}
		case drawn.failOn == config.Deny && !slices.Contains(drawn.severities, config.Deny):
			if got != verdictClean {
				t.Fatalf("a report whose findings are %v and whose fail_on is %s exits %d, want %d",
					drawn.severities, drawn.failOn, got, verdictClean)
			}
		}
	})
}

// shuffledSeverities is one severity list in a drawn order, so the permutation is
// part of the search rather than a fixed rotation.
func shuffledSeverities(t *rapid.T, held []config.Severity) []config.Severity {
	shuffled := slices.Clone(held)
	for i := range shuffled {
		at := rapid.IntRange(0, len(shuffled)-1).Draw(t, "a position to swap "+strconv.Itoa(i)+" with")
		shuffled[i], shuffled[at] = shuffled[at], shuffled[i]
	}
	return shuffled
}
