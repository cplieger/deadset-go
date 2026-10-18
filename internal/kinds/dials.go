package kinds

import (
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// dialed is what the two configuration dials withhold beyond the findings they
// name: the dead components a root finding of which they withhold, by identifier,
// those the severity dial withholds among them, the symbols a suppression record
// bound to is dormant for, because the record's finding would fall in one of those
// components, and the places in Marks of the records a finding of a kind the
// severity sets to allow pairs with, an unbound dependency record among them.
type dialed struct {
	components map[string]bool
	allowed    map[string]bool
	dormant    map[inventory.SymbolID]bool
	paired     map[int]bool
}

// withholdComponents answers which dead components the dials withhold, from the
// findings the first phase produced and from the findings each kind the severity
// sets to allow would have produced.
//
// A member never outlives its root: a symbol that falls with a root is dead only
// through it, so a finding about it names a deletion that does not compile while
// the root's finding is withheld. Withholding a root's finding therefore withholds
// every finding of its component, whatever severity and confidence those findings
// carry.
//
// The open-world rule that turns the unreferenced-exported kind off is not a dial.
// It says the analysis cannot see every caller, not that the configuration
// withholds a finding, so a kind it alone silences withholds no component.
func (in *Input) withholdComponents(published []vocab.Kind, emitters map[vocab.Code]Emitter, first []Finding) error {
	in.dialed = dialed{
		components: make(map[string]bool),
		allowed:    make(map[string]bool),
		dormant:    make(map[inventory.SymbolID]bool),
		paired:     make(map[int]bool),
	}
	least := in.Config.Analysis.MinConfidence
	for i := range first {
		if found := &first[i]; rank(found.componentConfidence()) < rank(least) {
			in.withholdRoot(found, false)
		}
	}

	held := in.index()
	minted, marks := held.minted, in.withheld
	in.withheld = make(map[int][]heldFinding, len(in.Marks))
	defer func() {
		for at, found := range in.withheld {
			in.dialed.paired[at] = len(found) > 0
		}
		held.minted, in.withheld = minted, marks
	}()
	for i := range published {
		row := &published[i]
		emit, carried := emitters[row.Code]
		if !carried || readsThePass[row.Code] || !in.dialAllows(row) {
			continue
		}
		produced, err := in.runKind(emit, row, make(map[string]string))
		if err != nil {
			return err
		}
		for j := range produced {
			in.withholdRoot(&produced[j], true)
		}
	}
	in.dormantNear()
	return nil
}

// dialAllows reports whether the severity dial sets one kind of this language to
// allow, read with the published API closed so the open-world rule takes no part.
func (in *Input) dialAllows(row *vocab.Kind) bool {
	return row.AppliesTo(language) && in.Config.EffectiveSeverity(row.Code) == vocab.Allow
}

// withholdRoot records the component of one withheld finding when the finding is a
// root of a dead component the sweep computed, and whether the severity dial is
// what withheld it. A component minted for one finding holds that finding alone, so
// withholding it withholds nothing more.
func (in *Input) withholdRoot(found *Finding, allowed bool) {
	if !found.Component.Root || found.id == "" {
		return
	}
	if component, at := in.index().component(found.id); component != nil {
		id := componentID(at + 1)
		in.dialed.components[id] = true
		if allowed {
			in.dialed.allowed[id] = true
		}
	}
}

// dormantNear records the symbols a suppression record bound to is dormant for:
// every member of a withheld component, and every symbol a record held back that
// a reference or a container joins to one, which without the record would be dead
// and a member of that component. The declarations [liveness.Result.TestUsed] holds, which
// the minimum withholds one by one, count the same way, but only for a symbol one
// of them references or contains, since only that symbol would join the set.
func (in *Input) dormantNear() {
	members, testUsed := in.withheldMembers(), in.testUsedWithheld()
	if len(members) == 0 && len(testUsed) == 0 {
		return
	}
	for id := range members {
		in.dialed.dormant[id] = true
	}
	for id := range testUsed {
		in.dialed.dormant[id] = true
	}
	heldBack := make(map[inventory.SymbolID]bool, len(in.staleInput().Suppressed))
	for _, id := range in.staleInput().Suppressed {
		heldBack[id] = true
	}
	// joined reads a, held back, against b, which references or contains it.
	joined := func(a, b inventory.SymbolID) {
		if heldBack[a] && (members[b] || testUsed[b]) {
			in.dialed.dormant[a] = true
		}
		if heldBack[b] && members[a] {
			in.dialed.dormant[b] = true
		}
	}
	in.eachJoin(joined)
}

// eachJoin calls joined with each referenced symbol and its referrer, and each
// contained symbol and its container, of the merged inventory.
func (in *Input) eachJoin(joined func(a, b inventory.SymbolID)) {
	if in.Merged == nil {
		return
	}
	for i := range in.Merged.References {
		joined(in.Merged.References[i].To, in.Merged.References[i].From)
	}
	for i := range in.Merged.Symbols {
		if symbol := &in.Merged.Symbols[i]; symbol.Parent != "" {
			joined(symbol.ID, symbol.Parent)
		}
	}
}

// testUsedWithheld is [liveness.Result.TestUsed] where the minimum withholds its findings,
// and empty where the minimum is possible.
func (in *Input) testUsedWithheld() map[inventory.SymbolID]bool {
	if rank(in.Config.Analysis.MinConfidence) <= rank(vocab.Possible) {
		return nil
	}
	return in.Sweep.TestUsed
}

// withheldMembers is every member of a component the dials withhold.
func (in *Input) withheldMembers() map[inventory.SymbolID]bool {
	members := make(map[inventory.SymbolID]bool)
	for id, at := range in.index().components {
		if in.dialed.components[componentID(at+1)] {
			members[id] = true
		}
	}
	return members
}

// reportable splits the findings into those no dial withholds, each at or above
// the configured minimum confidence in a component the dials do not withhold, and
// those the minimum alone withholds, which a run at the lowest minimum reports.
func (in *Input) reportable(findings []Finding) (kept, withheld []Finding) {
	least := in.Config.Analysis.MinConfidence
	kept = make([]Finding, 0, len(findings))
	for i := range findings {
		found := &findings[i]
		switch {
		case in.dialed.allowed[found.Component.ID]:
		case rank(found.Confidence) < rank(least) || in.dialed.components[found.Component.ID]:
			withheld = append(withheld, *found)
		default:
			kept = append(kept, *found)
		}
	}
	return kept, withheld
}
