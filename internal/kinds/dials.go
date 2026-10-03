package kinds

import (
	"slices"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
)

// dialed is what the two configuration dials withhold beyond the findings they
// name: the dead components a root finding of which they withhold, by identifier,
// and the symbols a suppression record bound to is dormant for, because the
// record's finding would fall in one of those components.
type dialed struct {
	components map[string]bool
	dormant    map[graph.SymbolID]bool
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
func (in *Input) withholdComponents(published []catalog.Row, emitters map[string]Emitter, first []Finding) error {
	in.dialed = dialed{components: make(map[string]bool), dormant: make(map[graph.SymbolID]bool)}
	least := Class(in.Config.Analysis.MinConfidence)
	for i := range first {
		if found := &first[i]; found.Confidence.rank() < least.rank() {
			in.withholdRoot(found)
		}
	}

	held := in.index()
	minted, marks := held.minted, in.withheld
	in.withheld = make(map[int]bool, len(in.Marks))
	defer func() { held.minted, in.withheld = minted, marks }()
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
			in.withholdRoot(&produced[j])
		}
	}
	in.dormantNear()
	return nil
}

// dialAllows reports whether the severity dial sets one kind of this language to
// allow, read with the published API closed so the open-world rule takes no part.
func (in *Input) dialAllows(row *catalog.Row) bool {
	return slices.Contains(row.Languages, language) && in.Config.EffectiveSeverity(row.Code) == config.Allow
}

// withholdRoot records the component of one withheld finding when the finding is a
// root of a dead component the sweep computed. A component minted for one finding
// holds that finding alone, so withholding it withholds nothing more.
func (in *Input) withholdRoot(found *Finding) {
	if !found.Component.Root || found.id == "" {
		return
	}
	if component := in.index().components[found.id]; component != nil {
		in.dialed.components[componentID(component.Index+1)] = true
	}
}

// dormantNear records the symbols a suppression record bound to is dormant for:
// every member of a withheld component, and every symbol a record held back that
// a reference or a container joins to one, which without the record would be dead
// and a member of that component.
func (in *Input) dormantNear() {
	if len(in.dialed.components) == 0 {
		return
	}
	members := in.withheldMembers()
	for id := range members {
		in.dialed.dormant[id] = true
	}
	heldBack := make(map[graph.SymbolID]bool, len(in.staleInput().Suppressed))
	for _, id := range in.staleInput().Suppressed {
		heldBack[id] = true
	}
	joined := func(a, b graph.SymbolID) {
		if heldBack[a] && members[b] {
			in.dialed.dormant[a] = true
		}
		if heldBack[b] && members[a] {
			in.dialed.dormant[b] = true
		}
	}
	if in.Merged == nil {
		return
	}
	for i := range in.Merged.References {
		joined(in.Merged.References[i].From, in.Merged.References[i].To)
	}
	for i := range in.Merged.Symbols {
		if symbol := &in.Merged.Symbols[i]; symbol.Parent != "" {
			joined(symbol.ID, symbol.Parent)
		}
	}
}

// withheldMembers is every member of a component the dials withhold.
func (in *Input) withheldMembers() map[graph.SymbolID]bool {
	members := make(map[graph.SymbolID]bool)
	for id, component := range in.index().components {
		if in.dialed.components[componentID(component.Index+1)] {
			members[id] = true
		}
	}
	return members
}

// reportable is the findings no dial withholds: each finding at or above the
// configured minimum confidence whose component the dials do not withhold.
func (in *Input) reportable(findings []Finding) []Finding {
	least := Class(in.Config.Analysis.MinConfidence)
	kept := make([]Finding, 0, len(findings))
	for i := range findings {
		found := &findings[i]
		if found.Confidence.rank() < least.rank() || in.dialed.components[found.Component.ID] {
			continue
		}
		kept = append(kept, *found)
	}
	return kept
}
