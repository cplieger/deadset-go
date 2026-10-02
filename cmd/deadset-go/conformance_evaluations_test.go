package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/edges"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// corpusEdgeEvaluation is one entry of a fixture's edge_evaluations member: the edge
// and the side the report must publish one record for, the state that record carries
// and, for a dead side, the logical name of the declaration the pending finding is
// about and that finding's code.
type corpusEdgeEvaluation struct {
	Edge   string `json:"edge"`
	Side   string `json:"side"`
	State  string `json:"state"`
	Symbol string `json:"symbol,omitempty"`
	Report string `json:"report,omitempty"`
}

// corpusConfiguredDeclaration is one member of a fixture's configured_declarations:
// the configuration key the runner writes the entry into, the entry, and the members
// of the lifecycle contract an entry of that key belongs to.
type corpusConfiguredDeclaration struct {
	Entry   corpusDeclarationEntry `json:"entry"`
	Key     string                 `json:"key"`
	Members []string               `json:"members"`
}

// corpusDeclarationEntry is one entry of a key that names a declaration, in each of
// the shapes the configuration schema declares for such an entry.
type corpusDeclarationEntry struct {
	Symbol string `json:"symbol"`
	Module string `json:"module"`
	Name   string `json:"name"`
	Global string `json:"global"`
}

// evaluationDifferences is one line per way the report's edge evaluations differ from
// the ones the fixture declares, and none where they agree.
//
// The declared list is exhaustive over the report's array: every record names an edge
// and a side an entry names, every entry is answered by exactly one record carrying
// its state, and the record of a dead side carries a pending finding under the entry's
// code at the position the entry's logical name resolves to. A fixture that declares
// no evaluation expects the array empty. A logical name the manifest does not carry is
// a defect in the corpus, which the error names.
func evaluationDifferences(declared []corpusEdgeEvaluation, manifest *corpusManifest,
	published []kinds.Evaluation,
) ([]string, error) {
	type sideOf struct{ edge, side string }
	byside := make(map[sideOf][]*kinds.Evaluation, len(published))
	for i := range published {
		key := sideOf{edge: published[i].Edge, side: string(published[i].Side)}
		byside[key] = append(byside[key], &published[i])
	}

	var differ []string
	named := make(map[sideOf]bool, len(declared))
	for i := range declared {
		want := &declared[i]
		key := sideOf{edge: want.Edge, side: want.Side}
		named[key] = true
		records := byside[key]
		if len(records) != 1 {
			differ = append(differ, fmt.Sprintf("want one evaluation of %s %s, got %d", want.Edge, want.Side, len(records)))
			continue
		}
		got := records[0]
		if string(got.State) != want.State {
			differ = append(differ, fmt.Sprintf("evaluation of %s %s: state want %q got %q",
				want.Edge, want.Side, want.State, got.State))
			continue
		}
		if want.State != string(kinds.StateDead) {
			continue
		}
		at, err := evaluationSite(want, manifest)
		if err != nil {
			return nil, err
		}
		pending := got.Finding
		switch {
		case pending == nil:
			differ = append(differ, fmt.Sprintf("evaluation of %s %s is dead and carries no finding", want.Edge, want.Side))
		case pending.Code != want.Report || pending.Position.Path != at.File || pending.Position.Line != at.Line:
			differ = append(differ, fmt.Sprintf("evaluation of %s %s: pending finding want %s at %s:%d got %s at %s:%d",
				want.Edge, want.Side, want.Report, at.File, at.Line,
				pending.Code, pending.Position.Path, pending.Position.Line))
		}
	}
	for i := range published {
		key := sideOf{edge: published[i].Edge, side: string(published[i].Side)}
		if !named[key] {
			differ = append(differ, fmt.Sprintf("evaluation of %s %s (%s) is named by no entry: the edge_evaluations member is exhaustive",
				published[i].Edge, published[i].Side, published[i].State))
		}
	}
	return differ, nil
}

// evaluationSite is the position one dead side's logical name resolves to through the
// rendering's manifest, relative to the target root.
func evaluationSite(want *corpusEdgeEvaluation, manifest *corpusManifest) (site, error) {
	held, known := manifest.Symbols[want.Symbol]
	within, inside := strings.CutPrefix(held.File, targetSection+"/")
	if !known || !inside || held.Line == 0 {
		return site{}, fmt.Errorf("the manifest carries no target file and line for %s, which the evaluation of %s %s names: a defect in the corpus",
			want.Symbol, want.Edge, want.Side)
	}
	return site{File: within, Line: held.Line}, nil
}

func TestEvaluationDifferencesHoldsTheReportToTheDeclaredList(t *testing.T) {
	t.Parallel()

	manifest := corpusManifest{Symbols: map[string]corpusSite{"Uncalled": {File: "target/wire.go", Line: 12}}}
	pending := kinds.Finding{Code: "DS1002", Position: kinds.Position{Path: "wire.go", Line: 12}}
	declared := []corpusEdgeEvaluation{
		{Edge: "wire/decode", Side: "provides", State: "dead", Symbol: "Uncalled", Report: "DS1002"},
		{Edge: "wire/encode", Side: "provides", State: "live"},
	}
	agreeing := []kinds.Evaluation{
		{Edge: "wire/decode", Side: edges.Provides, State: kinds.StateDead, Finding: &pending},
		{Edge: "wire/encode", Side: edges.Provides, State: kinds.StateLive},
	}
	elsewhere := pending
	elsewhere.Position.Line = 13

	tests := []struct {
		name      string
		published []kinds.Evaluation
		want      []string
	}{
		{name: "agreeing", published: agreeing},
		{
			name:      "a_side_no_entry_names",
			published: append(slices.Clone(agreeing), kinds.Evaluation{Edge: "wire/extra", Side: edges.Provides, State: kinds.StateAbsent}),
			want:      []string{"evaluation of wire/extra provides (absent) is named by no entry: the edge_evaluations member is exhaustive"},
		},
		{
			name:      "a_declared_side_missing",
			published: agreeing[:1],
			want:      []string{"want one evaluation of wire/encode provides, got 0"},
		},
		{
			name: "another_state",
			published: []kinds.Evaluation{
				agreeing[0], {Edge: "wire/encode", Side: edges.Provides, State: kinds.StateAbsent},
			},
			want: []string{`evaluation of wire/encode provides: state want "live" got "absent"`},
		},
		{
			name: "a_pending_finding_elsewhere",
			published: []kinds.Evaluation{
				{Edge: "wire/decode", Side: edges.Provides, State: kinds.StateDead, Finding: &elsewhere}, agreeing[1],
			},
			want: []string{"evaluation of wire/decode provides: pending finding want DS1002 at wire.go:12 got DS1002 at wire.go:13"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := evaluationDifferences(declared, &manifest, tc.published)
			if err != nil {
				t.Fatalf("evaluationDifferences(%s) = %v", tc.name, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("evaluationDifferences(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestEvaluationDifferencesExpectsAnEmptyArrayWhereTheFixtureDeclaresNone(t *testing.T) {
	t.Parallel()

	published := []kinds.Evaluation{{Edge: "wire/encode", Side: edges.Provides, State: kinds.StateLive}}
	got, err := evaluationDifferences(nil, &corpusManifest{}, published)
	if err != nil {
		t.Fatalf("evaluationDifferences(none declared) = %v", err)
	}
	if len(got) != 1 {
		t.Errorf("evaluationDifferences(none declared, one published) = %q, want one difference naming the record", got)
	}
}

func TestUnexpectedFindingsIsExhaustiveByPositionAndByCode(t *testing.T) {
	t.Parallel()

	at := site{File: "main.go", Line: 9}
	held := answered{findings: []kinds.Finding{
		{Code: "DS1002", Position: kinds.Position{Path: "main.go", Line: 9}},
		{Code: "DS1801", Position: kinds.Position{Path: "main.go", Line: 9}},
	}}
	expect := []corpusExpectation{{Symbol: "FirstLine", Report: "DS1002"}}

	got := unexpectedFindings(&held, expect, map[string]site{"FirstLine": at}, nil)
	want := []unexpectedFinding{{File: "target/main.go", Line: 9, Report: "DS1801"}}
	if !slices.Equal(got, want) {
		t.Errorf("unexpectedFindings(DS1002 and DS1801 at a row naming DS1002) = %+v, want %+v", got, want)
	}
}
