package findings

import (
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// dialed is the dials' answer over the first phase's findings: the dead components
// a withheld root finding withholds, by identifier, and those the severity dial
// withholds among them.
type dialed struct {
	components map[string]bool
	allowed    map[string]bool
}

// dial answers which dead components the dials withhold, from every finding of the
// phase, allowed ones included. A member is dead only through its root, so its
// deletion does not compile while the root's finding is withheld: withholding a
// root's finding withholds its whole component.
func (in *pass) dial(found []Finding) *dialed {
	d := &dialed{components: make(map[string]bool), allowed: make(map[string]bool)}
	least := in.Config.Analysis.MinConfidence
	for i := range found {
		switch one := &found[i]; {
		case one.Severity == vocab.Allow:
			in.withholdRoot(d, one, true)
		case rank(one.shared) < rank(least):
			in.withholdRoot(d, one, false)
		}
	}
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

// reportable splits the findings into those no dial withholds, each at or above
// the configured minimum confidence in a component the dials do not withhold, and
// those the minimum alone withholds, which a run at the lowest minimum reports. A
// finding a record withholds is in neither: the suppression counts report it.
func (in *pass) reportable(findings []Finding, ledger *core.Ledger) (kept, withheld []Finding) {
	least := in.Config.Analysis.MinConfidence
	kept = make([]Finding, 0, len(findings))
	for i := range findings {
		found := &findings[i]
		switch {
		case found.Severity == vocab.Allow, in.dialed.allowed[found.Component.ID], ledger.Withheld(in.ledgerFinding(found)):
		case in.dialed.withholds(found, least):
			withheld = append(withheld, *found)
		default:
			kept = append(kept, *found)
		}
	}
	return kept, withheld
}
