package findings

import (
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// dialed is the dials' answer over the first phase's findings, which the kinds of
// readsThePass and the suppression counts read: the dead components a withheld root
// finding withholds, by identifier, those the severity dial withholds among them,
// the symbols a suppression record bound to is dormant for, because the record's
// finding would fall in one of those components, and by place in Marks the records
// that withhold a finding, those one of whose findings the dials would report, and
// those that name a finding a type error skips.
type dialed struct {
	components map[string]bool
	allowed    map[string]bool
	dormant    map[inventory.SymbolID]bool
	matched    map[int]bool
	reports    map[int]bool
	skipped    map[int]bool
}

// dial answers which dead components the dials withhold and what each record's
// findings come to, from every finding of the first phase, allowed and
// record-withheld ones included. A member is dead only through its root, so its
// deletion does not compile while the root's finding is withheld: withholding a
// root's finding withholds its whole component. A finding a type error skips is
// read only for the record that names it.
func (in *pass) dial(first, skipped []Finding) *dialed {
	d := &dialed{
		components: make(map[string]bool),
		allowed:    make(map[string]bool),
		dormant:    make(map[inventory.SymbolID]bool),
		matched:    make(map[int]bool),
		reports:    make(map[int]bool),
		skipped:    make(map[int]bool),
	}
	for i := range skipped {
		if at := skipped[i].record - 1; at >= 0 {
			d.skipped[at] = true
		}
	}
	least := in.Config.Analysis.MinConfidence
	for i := range first {
		switch found := &first[i]; {
		case found.Severity == vocab.Allow:
			in.withholdRoot(d, found, true)
		case rank(found.shared) < rank(least):
			in.withholdRoot(d, found, false)
		}
	}
	for i := range first {
		if at := first[i].record - 1; at >= 0 {
			d.matched[at] = true
			d.reports[at] = d.reports[at] || !d.withholds(&first[i], least)
		}
	}
	in.dormantNear(d)
	return d
}

// withholds reports whether the dials withhold one finding: by its code's severity,
// by its own confidence, or with its dead component.
func (d *dialed) withholds(found *Finding, least vocab.Class) bool {
	return found.Severity == vocab.Allow || d.allowed[found.Component.ID] ||
		rank(found.Confidence) < rank(least) || d.components[found.Component.ID]
}

// withholdRoot records the component of one withheld finding when the finding is a
// root of a dead component the sweep computed, and whether the severity dial is
// what withheld it. A component minted for one finding holds that finding alone, so
// withholding it withholds nothing more.
func (in *pass) withholdRoot(d *dialed, found *Finding, allowed bool) {
	if !found.Component.Root || found.id == "" {
		return
	}
	if component, at := in.component(found.id); component != nil {
		id := ComponentID(at + 1)
		d.components[id] = true
		if allowed {
			d.allowed[id] = true
		}
	}
}

// dormantNear records the symbols a suppression record bound to is dormant for:
// every member of a withheld component, and every symbol a record held back that
// a reference or a container joins to one, which without the record would be dead
// and a member of that component. The declarations [liveness.Result.TestUsed] holds, which
// the minimum withholds one by one, count the same way, but only for a symbol one
// of them references or contains, since only that symbol would join the set.
func (in *pass) dormantNear(d *dialed) {
	members, testUsed := in.withheldMembers(d), in.testUsedWithheld()
	if len(members) == 0 && len(testUsed) == 0 {
		return
	}
	for id := range members {
		d.dormant[id] = true
	}
	for id := range testUsed {
		d.dormant[id] = true
	}
	heldBack := in.suppressed
	// joined reads a, held back, against b, which references or contains it.
	joined := func(a, b inventory.SymbolID) {
		if heldBack[a] && (members[b] || testUsed[b]) {
			d.dormant[a] = true
		}
		if heldBack[b] && members[a] {
			d.dormant[b] = true
		}
	}
	in.eachJoin(joined)
}

// eachJoin calls joined with each referenced symbol and its referrer, and each
// contained symbol and its container, of the merged inventory.
func (in *pass) eachJoin(joined func(a, b inventory.SymbolID)) {
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
func (in *pass) testUsedWithheld() map[inventory.SymbolID]bool {
	if !Withholds(in.Config.Analysis.MinConfidence, vocab.Possible) {
		return nil
	}
	return in.Sweep.TestUsed
}

// withheldMembers is every member of a component the dials withhold.
func (in *pass) withheldMembers(d *dialed) map[inventory.SymbolID]bool {
	members := make(map[inventory.SymbolID]bool)
	for id, at := range in.components {
		if d.components[ComponentID(at+1)] {
			members[id] = true
		}
	}
	return members
}

// reportable splits the findings into those no dial withholds, each at or above
// the configured minimum confidence in a component the dials do not withhold, and
// those the minimum alone withholds, which a run at the lowest minimum reports. A
// finding a record withholds is in neither: the suppression counts report it.
func (in *pass) reportable(findings []Finding) (kept, withheld []Finding) {
	least := in.Config.Analysis.MinConfidence
	kept = make([]Finding, 0, len(findings))
	for i := range findings {
		found := &findings[i]
		switch {
		case found.record != 0, found.Severity == vocab.Allow, in.dialed.allowed[found.Component.ID]:
		case in.dialed.withholds(found, least):
			withheld = append(withheld, *found)
		default:
			kept = append(kept, *found)
		}
	}
	return kept, withheld
}
