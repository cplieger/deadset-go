package kinds

import (
	"go/token"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The hand-built component's root is the exported function and the unexported
// helper falls with it, so these emitters report the root under one code, the
// member under another, and one declaration of no dead component under the second.
func rootMemberAndBystander(in *Input) map[vocab.Code]Emitter {
	return map[vocab.Code]Emitter{
		vocab.UnusedExported:   emitterOf(oneFinding(in, vocab.UnusedExported, exportedID)),
		vocab.UnusedUnexported: emitterOf(oneFinding(in, vocab.UnusedUnexported, helperID), oneFinding(in, vocab.UnusedUnexported, internalID)),
	}
}

// reportedIDs names the subject of every finding a pass reported, in its order.
func reportedIDs(result Result) []inventory.SymbolID {
	return idsOf(result.Findings)
}

// idsOf names the subject of every finding of a list, in its order.
func idsOf(findings []Finding) []inventory.SymbolID {
	ids := make([]inventory.SymbolID, 0, len(findings))
	for i := range findings {
		ids = append(ids, findings[i].id)
	}
	return ids
}

func TestComputeWithholdsTheComponentOfARootTheSeverityAllows(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]vocab.Severity{string(vocab.UnusedExported): vocab.Allow}
	in := handInput(resolved)

	result := computed(t, in, rootMemberAndBystander(in))

	// The helper is dead only through the root, so its finding goes with the root's,
	// whatever its own severity, while a declaration outside the component stays.
	if got, want := reportedIDs(result), []inventory.SymbolID{internalID}; !slices.Equal(got, want) {
		t.Errorf("Compute() with the root's kind at allow reported %v, want %v", got, want)
	}
	// The lowest minimum confidence still withholds a component whose root the
	// severity allows, so the minimum withheld none of it.
	if got := idsOf(result.Withheld); len(got) != 0 {
		t.Errorf("Compute() with the root's kind at allow counted %v as withheld by the minimum, want none", got)
	}
}

func TestComputeReportsTheWholeComponentWhenNoDialWithholdsItsRoot(t *testing.T) {
	in := handInput(applicationConfig())

	result := computed(t, in, rootMemberAndBystander(in))

	if got, want := reportedIDs(result), []inventory.SymbolID{exportedID, helperID, internalID}; !slices.Equal(got, want) {
		t.Errorf("Compute() with every kind at its default reported %v, want %v", got, want)
	}
}

func TestComputeWithholdsTheComponentOfARootBelowTheMinimumConfidence(t *testing.T) {
	resolved := applicationConfig()
	resolved.Analysis.MinConfidence = vocab.Certain
	in := handInput(resolved)
	rows := vocab.Kinds()
	for i := range rows {
		if rows[i].Code == vocab.UnusedExported {
			rows[i].MaxClass = vocab.Probable
		}
	}
	swap(t, rows)

	result := computed(t, in, rootMemberAndBystander(in))

	if got, want := reportedIDs(result), []inventory.SymbolID{internalID}; !slices.Equal(got, want) {
		t.Errorf("Compute() with the root below the minimum confidence reported %v, want %v", got, want)
	}
	// The member falls with the root, so the minimum withheld both, each counted.
	got := idsOf(result.Withheld)
	slices.Sort(got)
	want := []inventory.SymbolID{exportedID, helperID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("Compute() with the root below the minimum confidence withheld %v, want %v", got, want)
	}
}

// heldBackMark is a record naming the member's code, bound to one declaration a
// sweep without it judged dead.
func heldBackMark(in *Input, id inventory.SymbolID) {
	in.Marks = []suppress.Record{{
		Code:      vocab.UnusedUnexported,
		Symbol:    in.Refs[id],
		Reason:    "kept for the plugin loader",
		Bound:     id,
		Site:      token.Position{Filename: "catalog.go", Line: 30, Column: 1},
		Mechanism: suppress.MechanismInline,
	}}
	in.Sweep.Suppressed = []inventory.SymbolID{id}
}

func TestARecordOnAMemberOfAWithheldComponentIsDormant(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]vocab.Severity{string(vocab.UnusedExported): vocab.Allow}
	in := handInput(resolved)
	heldBackMark(in, helperID)

	result := computed(t, in, rootMemberAndBystander(in))

	if inEffect, _ := Totals(in); inEffect != 0 {
		t.Errorf("Totals() = %d in effect, want 0: the record's finding falls with a root the severity withholds", inEffect)
	}
	for i := range result.Findings {
		if result.Findings[i].Code == vocab.StaleSuppression {
			t.Errorf("Compute() reported %v, want no stale suppression for a dormant record", summary(result.Findings))
		}
	}
}

func TestARecordOnASymbolThatWouldJoinAWithheldComponentIsDormant(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]vocab.Severity{string(vocab.UnusedExported): vocab.Allow}
	in := handInput(resolved)
	const markedID inventory.SymbolID = "catalog.go:40:6"
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
	resolved.Severity = map[string]vocab.Severity{string(vocab.UnusedExported): vocab.Allow}
	in := handInput(resolved)
	const markedID inventory.SymbolID = "catalog.go:40:6"
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

func TestARecordOnAMethodWhoseTypeIsAMemberOfAWithheldComponentIsDormant(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]vocab.Severity{string(vocab.UnusedExported): vocab.Allow}
	in := handInput(resolved)
	in.Merged.Symbols[1].Kind = inventory.KindType
	const markedID inventory.SymbolID = "catalog.go:40:6"
	method := handSymbol(markedID, "go://example.com/app#resolve.marked", "marked", "example.com/app", "catalog.go", 40, 42, false)
	method.Kind, method.Parent = inventory.KindMethod, helperID
	in.Merged.Symbols = append(in.Merged.Symbols, method)
	in.Refs[markedID] = method.Ref
	heldBackMark(in, markedID)

	computed(t, in, rootMemberAndBystander(in))

	// Without its record the method is dead and its receiver type falls with the
	// withheld root, so the method falls with that root and its record is dormant.
	if inEffect, _ := Totals(in); inEffect != 0 {
		t.Errorf("Totals() = %d in effect, want 0: the record's method belongs to a type the severity withholds", inEffect)
	}
}

func TestARecordOnATypeWhoseMethodIsAMemberOfAWithheldComponentIsDormant(t *testing.T) {
	resolved := applicationConfig()
	resolved.Severity = map[string]vocab.Severity{string(vocab.UnusedExported): vocab.Allow}
	in := handInput(resolved)
	const markedID inventory.SymbolID = "catalog.go:40:6"
	container := handSymbol(markedID, "go://example.com/app#marked", "marked", "example.com/app", "catalog.go", 40, 42, false)
	container.Kind = inventory.KindType
	in.Merged.Symbols = append(in.Merged.Symbols, container)
	in.Merged.Symbols[1].Kind, in.Merged.Symbols[1].Parent = inventory.KindMethod, markedID
	in.Refs[markedID] = container.Ref
	heldBackMark(in, markedID)

	computed(t, in, rootMemberAndBystander(in))

	// Without its record the type is dead and holds a method that falls with the
	// withheld root, so the type joins that component and its record is dormant.
	if inEffect, _ := Totals(in); inEffect != 0 {
		t.Errorf("Totals() = %d in effect, want 0: the record's type holds a method the severity withholds", inEffect)
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
