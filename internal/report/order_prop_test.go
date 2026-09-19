package report

import (
	"bytes"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
	"pgregory.net/rapid"
)

// drawnFindings draws one finding set: a few findings over a few files, drawn so that
// two of them can share a path, a line, a code or a component, which is what makes the
// order's later components decide anything.
func drawnFindings(t *rapid.T) []kinds.Finding {
	paths := []string{"a.go", "catalog.go", "internal/store/store.go"}
	codes := []string{"DS1001", "DS1002", "DS1301"}
	severities := []config.Severity{config.Allow, config.Warn, config.Deny}

	count := rapid.IntRange(0, 8).Draw(t, "the number of findings")
	findings := make([]kinds.Finding, count)
	for i := range count {
		label := "finding " + strconv.Itoa(i)
		line := rapid.IntRange(1, 4).Draw(t, label+": the line")
		size := rapid.IntRange(0, 30).Draw(t, label+": the lines the subject spans")
		found := findingOf(
			rapid.SampledFrom(codes).Draw(t, label+": the code"),
			"unused-unexported",
			rapid.SampledFrom(paths).Draw(t, label+": the path"),
			line, line+size,
			rapid.SampledFrom(severities).Draw(t, label+": the severity"),
			"deletable",
			"the declaration has no reference in the target",
		)
		found.Position.Column = rapid.IntRange(1, 3).Draw(t, label+": the column")
		found.Symbol.Ref = "go://example.com/app#decl" + strconv.Itoa(i)
		found.Component = kinds.Component{
			ID:             "deadset-go/c-" + strconv.Itoa(rapid.IntRange(1, 3).Draw(t, label+": the component")),
			Root:           rapid.Bool().Draw(t, label+": the subject is a root of its component"),
			SymbolCount:    1,
			DeletableLines: rapid.IntRange(0, 40).Draw(t, label+": the lines its component removes"),
		}
		findings[i] = found
	}
	return findings
}

// TestReportOrderIsTotalWithNoAmbientInput is property dead-code-suite/P19: one finding
// set presented in any order is one report, in one order, whichever order is configured.
//
// The permutation is the property's whole point: the assembly reads no order from its
// input, so the canonical order and the size order are each a function of the finding
// set alone. The renderings are compared as bytes, because a rendering that read the
// moment or the machine would differ there and nowhere else.
func TestReportOrderIsTotalWithNoAmbientInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		findings := drawnFindings(t)
		shuffled := slices.Clone(findings)
		for i := range shuffled {
			at := rapid.IntRange(0, len(shuffled)-1).Draw(t, "a position to swap "+strconv.Itoa(i)+" with")
			shuffled[i], shuffled[at] = shuffled[at], shuffled[i]
		}

		for _, by := range []config.Sort{config.ByPosition, config.BySize} {
			first := orderedEnvelope(t, findings, by)
			second := orderedEnvelope(t, shuffled, by)
			if !slices.EqualFunc(first.Findings, second.Findings, sameFinding) {
				t.Fatalf("the %s order of one finding set differs between two presentations of it:\n%v\n%v",
					by, keys(&first), keys(&second))
			}
			if !slices.IsSortedFunc(first.Findings, orderOf(by)) {
				t.Fatalf("the %s order is not sorted by the key it names: %v", by, keys(&first))
			}
			one := renderedByProperty(t, "text", &first)
			two := renderedByProperty(t, "text", &second)
			if one != two {
				t.Fatalf("two renderings of one finding set differ:\n%s\n%s", one, two)
			}
		}
	})
}

// TestACappedReportAccountsForEverythingItOmits is property dead-code-suite/P20: a
// maximum finding count bounds what a rendering prints and nothing else.
//
// The counts are checked against the uncapped report rather than recomputed, so the
// property is that capping does not move them, which is what a reader of a capped report
// rests on.
func TestACappedReportAccountsForEverythingItOmits(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		findings := drawnFindings(t)
		most := rapid.IntRange(-1, 10).Draw(t, "the configured maximum finding count")
		by := rapid.SampledFrom([]config.Sort{config.ByPosition, config.BySize}).Draw(t, "the configured order")

		whole := orderedEnvelope(t, findings, by)
		capped := orderedEnvelope(t, findings, by)
		Cap(&capped, most)

		if printed := len(capped.Findings) + capped.Totals.Omitted; printed != capped.Totals.Findings {
			t.Fatalf("a report of %d findings capped at %d prints %d and omits %d",
				capped.Totals.Findings, most, len(capped.Findings), capped.Totals.Omitted)
		}
		if capped.Totals.Findings != whole.Totals.Findings {
			t.Fatalf("capping at %d moved the finding count from %d to %d",
				most, whole.Totals.Findings, capped.Totals.Findings)
		}
		if capped.Totals.DeletableLines != whole.Totals.DeletableLines {
			t.Fatalf("capping at %d moved the deletable-line total from %d to %d",
				most, whole.Totals.DeletableLines, capped.Totals.DeletableLines)
		}
		if capped.Totals.BySeverity != whole.Totals.BySeverity {
			t.Fatalf("capping at %d moved the severity counts from %+v to %+v",
				most, whole.Totals.BySeverity, capped.Totals.BySeverity)
		}
		if most > 0 && len(capped.Findings) > most {
			t.Fatalf("a report capped at %d prints %d findings", most, len(capped.Findings))
		}
		if !slices.EqualFunc(capped.Findings, whole.Findings[:len(capped.Findings)], sameFinding) {
			t.Fatalf("a capped report prints findings the uncapped one does not begin with")
		}
	})
}

// orderedEnvelope assembles one envelope over a finding set and orders it as configured.
func orderedEnvelope(t *rapid.T, findings []kinds.Finding, by config.Sort) Envelope {
	in := minimalInput()
	in.Result.Findings = slices.Clone(findings)
	envelope, err := Build(&in)
	if err != nil {
		t.Fatalf("Build() = error %v, want an envelope", err)
	}
	Sort(&envelope, by)
	return envelope
}

// renderedByProperty is one rendering of one envelope, inside a property.
func renderedByProperty(t *rapid.T, name string, e *Envelope) string {
	for _, one := range renderings() {
		if one.name != name {
			continue
		}
		var held bytes.Buffer
		if err := one.render(&held, e, Options{}); err != nil {
			t.Fatalf("%s() = error %v, want a rendering", name, err)
		}
		return held.String()
	}
	t.Fatalf("%q names no rendering", name)
	return ""
}

// orderOf is the comparison one configured order is sorted by.
func orderOf(by config.Sort) func(a, b kinds.Finding) int {
	if by != config.BySize {
		return kinds.Compare
	}
	return func(a, b kinds.Finding) int {
		if held := compareSizes(&a, &b); held != 0 {
			return held
		}
		return kinds.Compare(a, b)
	}
}

// compareSizes is the size half of the size order: the lines a deletion removes, then
// the lines the subject spans, each largest first.
func compareSizes(a, b *kinds.Finding) int {
	if a.Component.DeletableLines != b.Component.DeletableLines {
		return b.Component.DeletableLines - a.Component.DeletableLines
	}
	return b.Symbol.SizeLines - a.Symbol.SizeLines
}

// sameFinding reports whether two findings are one record, compared by everything the
// order reads and everything a rendering writes of it.
func sameFinding(a, b kinds.Finding) bool {
	return kinds.Compare(a, b) == 0 &&
		a.Severity == b.Severity &&
		a.Component == b.Component &&
		a.Symbol == b.Symbol
}

// keys is the canonical key of every finding of an envelope, for a failure message that
// has to say what order it found.
func keys(e *Envelope) []string {
	held := make([]string, 0, len(e.Findings))
	for i := range e.Findings {
		found := &e.Findings[i]
		held = append(held, found.Position.Path+":"+strconv.Itoa(found.Position.Line)+":"+
			strconv.Itoa(found.Position.Column)+" "+found.Code+" "+found.Symbol.Ref+
			" lines "+strconv.Itoa(found.Component.DeletableLines))
	}
	return held
}
