package report

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// sized is a finding set whose components and subjects differ in size, so an order by
// size is distinguishable from the canonical one.
func sized() []kinds.Finding {
	first := findingOf("DS1002", "unused-unexported", "a.go", 10, 12,
		config.Deny, "deletable", "the function has no reference in the target")
	first.Component = kinds.Component{ID: "deadset-go/c-1", Root: true, SymbolCount: 1, DeletableLines: 3}
	second := findingOf("DS1002", "unused-unexported", "b.go", 20, 60,
		config.Deny, "deletable", "the function has no reference in the target")
	second.Symbol.Ref = "go://example.com/app#second"
	second.Component = kinds.Component{ID: "deadset-go/c-2", Root: true, SymbolCount: 4, DeletableLines: 41}
	third := findingOf("DS1002", "unused-unexported", "c.go", 30, 33,
		config.Deny, "deletable", "the function has no reference in the target")
	third.Symbol.Ref = "go://example.com/app#third"
	third.Component = kinds.Component{ID: "deadset-go/c-3", Root: true, SymbolCount: 1, DeletableLines: 4}
	return []kinds.Finding{first, second, third}
}

// TestSortOrdersBySizeThenByTheCanonicalKey pins the order a configuration asks for
// when it asks for the largest deletion first.
func TestSortOrdersBySizeThenByTheCanonicalKey(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = sized()
	envelope := built(t, &in)

	Sort(&envelope, config.BySize)
	want := []string{"b.go", "c.go", "a.go"}
	if got := paths(&envelope); !slices.Equal(got, want) {
		t.Errorf("Sort(size) = %v, want %v", got, want)
	}

	Sort(&envelope, config.ByPosition)
	want = []string{"a.go", "b.go", "c.go"}
	if got := paths(&envelope); !slices.Equal(got, want) {
		t.Errorf("Sort(position) = %v, want %v", got, want)
	}
}

// TestSortByAnUnnamedOrderIsTheCanonicalOne pins that an order the configuration does
// not admit is the canonical order rather than an order of its own.
func TestSortByAnUnnamedOrderIsTheCanonicalOne(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = sized()
	envelope := built(t, &in)
	Sort(&envelope, config.BySize)

	Sort(&envelope, config.Sort("invented"))
	want := []string{"a.go", "b.go", "c.go"}
	if got := paths(&envelope); !slices.Equal(got, want) {
		t.Errorf("Sort(an order the configuration does not admit) = %v, want the canonical order %v", got, want)
	}
}

// TestSortBySizeFallsBackToTheCanonicalKey pins the last component of the size order,
// against an envelope whose findings are not already in the canonical order: two
// findings that remove the same number of lines and span the same number of lines are
// ordered by the canonical key rather than by the order they arrived in.
func TestSortBySizeFallsBackToTheCanonicalKey(t *testing.T) {
	findings := sized()
	findings[2].Component = findings[0].Component
	findings[2].Position.Line, findings[2].Position.EndLine = 10, 12
	findings[2].Symbol.SizeLines = findings[0].Symbol.SizeLines

	in := minimalInput()
	in.Result.Findings = findings
	envelope := built(t, &in)
	slices.Reverse(envelope.Findings)

	Sort(&envelope, config.BySize)
	want := []string{"b.go", "a.go", "c.go"}
	if got := paths(&envelope); !slices.Equal(got, want) {
		t.Errorf("Sort(size) over findings of equal size = %v, want the canonical order among them %v",
			got, want)
	}
}

// TestCapKeepsTheCountsOverTheWholeSet pins that a maximum finding count bounds what is
// printed and nothing else: the omitted count says how many were kept out, and the
// finding count, the severity counts and the deletable-line total still describe the
// whole set.
func TestCapKeepsTheCountsOverTheWholeSet(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = sized()
	envelope := built(t, &in)
	whole := envelope.Totals

	Cap(&envelope, 1)
	switch {
	case len(envelope.Findings) != 1:
		t.Errorf("Cap(1) left %d findings, want 1", len(envelope.Findings))
	case envelope.Totals.Omitted != 2:
		t.Errorf("Cap(1).Totals.Omitted = %d, want 2", envelope.Totals.Omitted)
	case envelope.Totals.Findings != whole.Findings:
		t.Errorf("Cap(1).Totals.Findings = %d, want the whole set's %d",
			envelope.Totals.Findings, whole.Findings)
	case envelope.Totals.DeletableLines != whole.DeletableLines:
		t.Errorf("Cap(1).Totals.DeletableLines = %d, want the whole set's %d",
			envelope.Totals.DeletableLines, whole.DeletableLines)
	case envelope.Totals.BySeverity != whole.BySeverity:
		t.Errorf("Cap(1).Totals.BySeverity = %+v, want the whole set's %+v",
			envelope.Totals.BySeverity, whole.BySeverity)
	}
}

// TestCapKeepsEveryFindingWhereNoMaximumIsConfigured pins the two counts that keep every
// finding: an unconfigured maximum, and one no smaller than the set.
func TestCapKeepsEveryFindingWhereNoMaximumIsConfigured(t *testing.T) {
	for _, most := range []int{0, -1, 3, 4} {
		in := minimalInput()
		in.Result.Findings = sized()
		envelope := built(t, &in)

		Cap(&envelope, most)
		if len(envelope.Findings) != 3 || envelope.Totals.Omitted != 0 {
			t.Errorf("Cap(%d) left %d findings and omitted %d, want 3 and 0",
				most, len(envelope.Findings), envelope.Totals.Omitted)
		}
	}
}

// TestTheDeletableTotalCountsAComponentOnce pins that two findings that are roots of one
// component do not count that component's lines twice.
func TestTheDeletableTotalCountsAComponentOnce(t *testing.T) {
	in := minimalInput()
	findings := sized()
	findings[1].Component = findings[0].Component
	in.Result.Findings = findings
	envelope := built(t, &in)

	if want := 3 + 4; envelope.Totals.DeletableLines != want {
		t.Errorf("Totals.DeletableLines = %d, want %d: a component with two roots falls once",
			envelope.Totals.DeletableLines, want)
	}
}

// TestTheDeletableTotalCountsRootsOnly pins that a member that is not a root of its
// component contributes nothing, because the lines it removes are its component's.
func TestTheDeletableTotalCountsRootsOnly(t *testing.T) {
	in := minimalInput()
	findings := sized()
	findings[1].Component.Root = false
	in.Result.Findings = findings
	envelope := built(t, &in)

	if want := 3 + 4; envelope.Totals.DeletableLines != want {
		t.Errorf("Totals.DeletableLines = %d, want %d", envelope.Totals.DeletableLines, want)
	}
}

// TestTheRenderingsNameTheOmittedCount pins that a truncation is visible in every
// rendering a reader might hold, which is what the omitted count exists for.
func TestTheRenderingsNameTheOmittedCount(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = sized()
	envelope := built(t, &in)
	Cap(&envelope, 1)
	opts := Options{Read: lines()}

	if got := string(rendered(t, "text", &envelope, opts)); !strings.Contains(got, "2 omitted") {
		t.Errorf("the text rendering names no omitted count:\n%s", got)
	}
	for _, name := range []string{"json", "sarif"} {
		if got := string(rendered(t, name, &envelope, opts)); !strings.Contains(got, `"omitted": 2`) {
			t.Errorf("the %s rendering names no omitted count:\n%s", name, got)
		}
	}
}

// paths is the path of every finding of an envelope, in its order.
func paths(e *Envelope) []string {
	held := make([]string, 0, len(e.Findings))
	for i := range e.Findings {
		held = append(held, e.Findings[i].Position.Path)
	}
	return held
}
