package kinds

import (
	"go/token"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/suppress"
)

// The hand-built component's root is the exported function and the unexported
// helper falls with it, so these emitters report the root under one code, the
// member under another, and one declaration of no dead component under the second.
func rootMemberAndBystander(in *Input) map[string]Emitter {
	return map[string]Emitter{
		unusedExportedCode:   emitterOf(oneFinding(in, unusedExportedCode, exportedID)),
		unusedUnexportedCode: emitterOf(oneFinding(in, unusedUnexportedCode, helperID), oneFinding(in, unusedUnexportedCode, internalID)),
	}
}

// reportedIDs names the subject of every finding a pass reported, in its order.
func reportedIDs(result Result) []graph.SymbolID {
	ids := make([]graph.SymbolID, 0, len(result.Findings))
	for i := range result.Findings {
		ids = append(ids, result.Findings[i].id)
	}
	return ids
}

func TestComputeWithholdsTheComponentOfARootTheSeverityAllows(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]config.Severity{unusedExportedCode: config.Allow}
	in := handInput(resolved)

	result := computed(t, in, rootMemberAndBystander(in))

	// The helper is dead only through the root, so its finding goes with the root's,
	// whatever its own severity, while a declaration outside the component stays.
	if got, want := reportedIDs(result), []graph.SymbolID{internalID}; !slices.Equal(got, want) {
		t.Errorf("Compute() with the root's kind at allow reported %v, want %v", got, want)
	}
}

func TestComputeReportsTheWholeComponentWhenNoDialWithholdsItsRoot(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, rootMemberAndBystander(in))

	if got, want := reportedIDs(result), []graph.SymbolID{exportedID, helperID, internalID}; !slices.Equal(got, want) {
		t.Errorf("Compute() with every kind at its default reported %v, want %v", got, want)
	}
}

func TestComputeWithholdsTheComponentOfARootBelowTheMinimumConfidence(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = config.Certain
	in := handInput(resolved)
	rows := catalog.Kinds()
	for i := range rows {
		if rows[i].Code == unusedExportedCode {
			rows[i].MaxClass = string(Probable)
		}
	}
	swap(t, rows)

	result := computed(t, in, rootMemberAndBystander(in))

	if got, want := reportedIDs(result), []graph.SymbolID{internalID}; !slices.Equal(got, want) {
		t.Errorf("Compute() with the root below the minimum confidence reported %v, want %v", got, want)
	}
}

// heldBackMark is a record naming the member's code, bound to one declaration a
// sweep without it judged dead.
func heldBackMark(in *Input, id graph.SymbolID) {
	in.Marks = []suppress.Record{{
		Code:      unusedUnexportedCode,
		Symbol:    in.Refs[id],
		Reason:    "kept for the plugin loader",
		Bound:     id,
		Site:      token.Position{Filename: "catalog.go", Line: 30, Column: 1},
		Mechanism: suppress.MechanismInline,
	}}
	in.Sweep.Suppressed = []graph.SymbolID{id}
}

func TestARecordOnAMemberOfAWithheldComponentIsDormant(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]config.Severity{unusedExportedCode: config.Allow}
	in := handInput(resolved)
	heldBackMark(in, helperID)

	result := computed(t, in, rootMemberAndBystander(in))

	if inEffect, _ := Totals(in); inEffect != 0 {
		t.Errorf("Totals() = %d in effect, want 0: the record's finding falls with a root the severity withholds", inEffect)
	}
	for i := range result.Findings {
		if result.Findings[i].Code == staleSuppressionCode {
			t.Errorf("Compute() reported %v, want no stale suppression for a dormant record", summary(result.Findings))
		}
	}
}

func TestARecordOnASymbolThatWouldJoinAWithheldComponentIsDormant(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]config.Severity{unusedExportedCode: config.Allow}
	in := handInput(resolved)
	const markedID graph.SymbolID = "catalog.go:40:6"
	in.Merged.Symbols = append(in.Merged.Symbols,
		handSymbol(markedID, "go://example.com/app#marked", "marked", "example.com/app", "catalog.go", 40, 42, false))
	in.Merged.References = append(in.Merged.References, graph.Reference{From: exportedID, To: markedID, Kind: graph.RefCall})
	in.Refs[markedID] = "go://example.com/app#marked"
	heldBackMark(in, markedID)

	computed(t, in, rootMemberAndBystander(in))

	// Without its record the marked declaration is dead and referenced by the
	// withheld root, so it falls with that root and its record is dormant too.
	if inEffect, _ := Totals(in); inEffect != 0 {
		t.Errorf("Totals() = %d in effect, want 0: the record's finding would fall with a root the severity withholds", inEffect)
	}
}

func TestARecordOnASymbolThatReferencesAMemberOfAWithheldComponentIsDormant(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]config.Severity{unusedExportedCode: config.Allow}
	in := handInput(resolved)
	const markedID graph.SymbolID = "catalog.go:40:6"
	in.Merged.Symbols = append(in.Merged.Symbols,
		handSymbol(markedID, "go://example.com/app#marked", "marked", "example.com/app", "catalog.go", 40, 42, false))
	in.Merged.References = append(in.Merged.References, graph.Reference{From: markedID, To: helperID, Kind: graph.RefCall})
	in.Refs[markedID] = "go://example.com/app#marked"
	heldBackMark(in, markedID)

	computed(t, in, rootMemberAndBystander(in))

	// Without its record the marked declaration is dead and reaches the helper the
	// withheld root also reaches, so it is a second root of that one component and
	// its record is dormant too.
	if inEffect, _ := Totals(in); inEffect != 0 {
		t.Errorf("Totals() = %d in effect, want 0: the record's finding would fall in a component whose root the severity withholds", inEffect)
	}
}

func TestARecordOnAMemberIsInEffectWhileItsRootIsReported(t *testing.T) {
	in := handInput(applicationConfig())
	heldBackMark(in, helperID)

	computed(t, in, rootMemberAndBystander(in))

	if inEffect, _ := Totals(in); inEffect != 1 {
		t.Errorf("Totals() = %d in effect, want 1: no dial withholds the root", inEffect)
	}
}
