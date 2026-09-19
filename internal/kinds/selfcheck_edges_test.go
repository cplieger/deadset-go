package kinds

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/edges"
)

// edged is the harness input over one archive with the target's edges document read,
// which is what the composition root hands a findings pass.
func edged(t *testing.T, archive string, resolved config.Config) *Input {
	t.Helper()

	dir := extract(t, archive)
	in := inputOfDir(t, dir, resolved, Consumers{})
	declared, err := edges.Read(filepath.Join(dir, edges.FileName))
	if err != nil {
		t.Fatalf("Setup: edges.Read(%s): %v", archive, err)
	}
	in.Edges = declared
	return in
}

// evaluated is one evaluation as a test names it.
type evaluated struct {
	edge    string
	side    string
	symbol  string
	state   string
	finding string
}

// asNamed is every evaluation as a test names it, with the code of the pending finding
// where the record carries one.
func asNamed(evaluations []Evaluation) []evaluated {
	held := make([]evaluated, 0, len(evaluations))
	for i := range evaluations {
		one := &evaluations[i]
		code := ""
		if one.Finding != nil {
			code = one.Finding.Code
		}
		held = append(held, evaluated{
			edge:    one.Edge,
			side:    string(one.Side),
			symbol:  one.Symbol,
			state:   string(one.State),
			finding: code,
		})
	}
	return held
}

func TestEvaluatePublishesOneRecordPerOwnSideWithItsState(t *testing.T) {
	in := edged(t, "selfcheck-edges.txtar", applicationConfig())
	result := computed(t, in, map[string]Emitter{unreachableExportCode: UnreachableExport})

	kept, evaluations := Evaluate(in, result.Findings)
	const declaring = "go://example.com/selfcheckedges#"
	want := []evaluated{
		{edge: "wire/Event", side: "provides", symbol: declaring + "Event", state: "dead", finding: unreachableExportCode},
		{edge: "wire/Local", side: "provides", symbol: declaring + "Local", state: "live"},
		{edge: "wire/Vanished", side: "provides", symbol: declaring + "Vanished", state: "absent"},
		{edge: "wire/kept", side: "provides", symbol: declaring + "kept", state: "live"},
	}
	if got := asNamed(evaluations); !slices.Equal(got, want) {
		t.Errorf("Evaluate() published %+v, want %+v", got, want)
	}
	if slices.Contains(namesUnder(kept, unreachableExportCode), "Event") {
		t.Errorf("Evaluate() left the finding about Event reported, want it published inside the evaluation alone: %v",
			summary(kept))
	}
	if !slices.Contains(namesUnder(kept, unreachableExportCode), "Orphan") {
		t.Errorf("Evaluate() dropped the finding about Orphan, want every finding no edge names kept: %v",
			summary(kept))
	}
	if len(kept) != len(result.Findings)-1 {
		t.Errorf("Evaluate() kept %d of %d findings, want one moved into the evaluation",
			len(kept), len(result.Findings))
	}
}

func TestEvaluateCarriesTheFindingTheAnalyzerWouldHaveReported(t *testing.T) {
	in := edged(t, "selfcheck-edges.txtar", applicationConfig())
	result := computed(t, in, map[string]Emitter{unreachableExportCode: UnreachableExport})
	reported := findingOf(t, result.Findings, unreachableExportCode, "Event")

	_, evaluations := Evaluate(in, result.Findings)
	pending := evaluations[0].Finding
	if pending == nil {
		t.Fatalf("Evaluate() published %+v with no pending finding, want the finding about Event", evaluations[0])
	}
	switch {
	case pending.Code != reported.Code || pending.Symbol.Ref != reported.Symbol.Ref:
		t.Errorf("the pending finding is %s about %s, want %s about %s",
			pending.Code, pending.Symbol.Ref, reported.Code, reported.Symbol.Ref)
	case pending.Confidence != reported.Confidence || pending.Severity != reported.Severity:
		t.Errorf("the pending finding carries confidence %s at %s, want %s at %s: a pending finding is not a weaker one",
			pending.Confidence, pending.Severity, reported.Confidence, reported.Severity)
	}
}

func TestEvaluatePublishesANarrowingCandidateWhoseOnlyOutsideReferenceIsAnEdge(t *testing.T) {
	in := edged(t, "selfcheck-edges.txtar", applicationConfig())
	result := computed(t, in, map[string]Emitter{unnecessaryExportCode: UnnecessaryExport})
	if !slices.Contains(namesUnder(result.Findings, unnecessaryExportCode), "Local") {
		t.Fatalf("the narrowing kind reports %v, want a finding about Local for the evaluation to publish",
			summary(result.Findings))
	}

	kept, evaluations := Evaluate(in, result.Findings)
	local := -1
	for i := range evaluations {
		if evaluations[i].Edge == "wire/Local" {
			local = i
		}
	}
	if local < 0 {
		t.Fatalf("Evaluate() published %+v, want a record for wire/Local", asNamed(evaluations))
	}
	one := evaluations[local]
	if one.State != StateDead || one.Finding == nil || one.Finding.Code != unnecessaryExportCode {
		t.Errorf("Evaluate() published %+v for wire/Local, want it dead and carrying the narrowing finding",
			asNamed(evaluations)[local])
	}
	if slices.Contains(namesUnder(kept, unnecessaryExportCode), "Local") {
		t.Errorf("Evaluate() left the narrowing finding about Local reported, want it published pending: %v",
			summary(kept))
	}
}

func TestEvaluateReadsOnlyItsOwnSideOfEveryEdge(t *testing.T) {
	in := edged(t, "selfcheck-edges.txtar", applicationConfig())
	result := computed(t, in, map[string]Emitter{unreachableExportCode: UnreachableExport})

	_, evaluations := Evaluate(in, result.Findings)
	for i := range evaluations {
		if !strings.HasPrefix(evaluations[i].Symbol, "go://") {
			t.Errorf("Evaluate() published %+v, want no record for a side of another language",
				asNamed(evaluations)[i])
		}
	}
	if got, want := len(evaluations), len(in.Edges.Own(language)); got != want {
		t.Errorf("Evaluate() published %d records, want %d, one per own side the document declares", got, want)
	}
}

func TestEvaluateWithNoDeclaredEdgeChangesNothing(t *testing.T) {
	in := edged(t, "selfcheck-edges.txtar", applicationConfig())
	result := computed(t, in, map[string]Emitter{unreachableExportCode: UnreachableExport})
	in.Edges = nil

	kept, evaluations := Evaluate(in, result.Findings)
	if evaluations != nil {
		t.Errorf("Evaluate() over a target with no edges document published %+v, want no record", asNamed(evaluations))
	}
	if !slices.Equal(codesOf(kept), codesOf(result.Findings)) {
		t.Errorf("Evaluate() over a target with no edges document kept %v, want %v",
			codesOf(kept), codesOf(result.Findings))
	}
}
