package findings

import (
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/report"
)

// state is one edge side's verdict, in the spelling a report carries.
type state string

// The three states. A side this analyzer evaluated has one of them; a side of
// another language has no record at all and is not absent, because nothing looked.
const (
	stateLive   state = "live"   // the symbol is enumerated and no finding is held for it
	stateDead   state = "dead"   // the symbol is enumerated and a finding is held for it
	stateAbsent state = "absent" // no symbol is enumerated under the reference
)

// evaluation is one record of a report's edge_evaluations array: which declared
// edge, which side of it, the symbol that side names, and this analyzer's verdict.
//
// Finding is the pending finding, present exactly where the state is dead. It is
// the finding this analyzer would otherwise have reported, and it appears nowhere
// else in the report: the report says the symbol is dead on this side and says
// nothing about whether it should be deleted, because that answer is on the side
// this analyzer never reads.
type evaluation struct {
	Finding *Finding
	Edge    string
	Symbol  string
	Side    edges.Side
	State   state
}

// evaluate publishes one record per declared edge side this analyzer enumerated, by
// edge and then by side, and returns the findings that stay reported, in their order.
// A declared edge stands for a consumer this analysis cannot see, so a finding about
// a symbol an edge names, dead or narrowable, is pending: it moves into the record,
// where the merge resolves it against the paired side. Two edges naming one symbol
// each get a record carrying the finding, which leaves the list once.
func (in *pass) evaluate(findings []Finding) ([]Finding, []evaluation) {
	if in.Edges == nil {
		return findings, nil
	}
	own := in.Edges.Own(string(language))
	if len(own) == 0 {
		return findings, nil
	}

	held := make(map[inventory.SymbolID]int, len(findings))
	for i := range findings {
		held[findings[i].id] = i
	}

	pending := make(map[int]bool, len(own))
	evaluations := make([]evaluation, 0, len(own))
	for _, side := range own {
		record := evaluation{Edge: side.Edge, Side: side.Side, Symbol: side.Symbol, State: stateAbsent}
		id := in.byRef[side.Symbol]
		switch at, reported := held[id]; {
		case id == "":
			// The reference names no declaration this run enumerated, so nothing
			// here can say whether the symbol is live: the merge reports the edge.
		case !reported:
			record.State = stateLive
		default:
			record.State = stateDead
			found := findings[at]
			record.Finding = &found
			pending[at] = true
		}
		evaluations = append(evaluations, record)
	}

	kept := make([]Finding, 0, len(findings))
	for i := range findings {
		if !pending[i] {
			kept = append(kept, findings[i])
		}
	}
	slices.SortStableFunc(evaluations, func(a, b evaluation) int {
		keyA := report.EdgeEvaluation{Edge: a.Edge, Side: report.Side(a.Side)}
		keyB := report.EdgeEvaluation{Edge: b.Edge, Side: report.Side(b.Side)}
		return report.CompareEvaluations(&keyA, &keyB)
	})
	return kept, evaluations
}
