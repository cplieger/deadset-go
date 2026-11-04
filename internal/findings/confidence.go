package findings

import (
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// classify gives one finding its reachability class and its own confidence: the
// class capped by the kind's max_class, then lowered to possible where production
// code only tests use is its subject ([pass.capTestUsed]). The component cap is
// [cohere]'s, once every finding of the pass is classified.
func (in *pass) classify(found *Finding, d *draft, row *vocab.Kind) {
	found.Class = vocab.Certain
	if !found.local && shapeOf(found.Symbol.Kind) == shapeDeclaration {
		found.Class = in.classOf(found.id)
		if d.unseen {
			found.Class = lower(found.Class, vocab.Possible)
		}
	}
	in.capNameLiteral(found)
	in.capFilled(found)
	found.Confidence = lower(found.Class, row.MaxClass)
	found.shared = found.Confidence
	in.capTestUsed(found, row, d.testRead)
}

// capTestUsed lowers a finding about production code only tests use to possible: a
// declaration of [liveness.Result.TestUsed], and a declaration of a package only
// tests reach that a write-only finding finds read only by test files, which the
// sweep holds live. Its class is lowered too unless it is a test of dead code, whose
// class is its own. The lowering is the finding's own: shared keeps the confidence
// without it.
func (in *pass) capTestUsed(found *Finding, row *vocab.Kind, testRead bool) {
	if found.id == "" || found.local {
		return
	}
	symbol := in.symbol(found.id)
	dead := !row.HoldsLive && in.Sweep != nil && in.Sweep.TestUsed[found.id]
	if !dead && (!testRead || symbol == nil || !in.testReached(symbol)) {
		return
	}
	if row.Code != vocab.TestOfDeadCode {
		found.Class = lower(found.Class, vocab.Possible)
	}
	found.Confidence = lower(found.Confidence, vocab.Possible)
}

// cohere caps every finding of a dead component at the lowest shared confidence
// among the findings about the component's root members, whatever their severity
// and whether or not a record withholds them, so every finding of one component
// carries one confidence but those [pass.capTestUsed] lowers on their own. A
// component minted for one subject holds that finding alone.
func cohere(findings []Finding) {
	least := make(map[string]vocab.Class)
	for i := range findings {
		found := &findings[i]
		if !found.Component.Root || found.Component.ID == "" {
			continue
		}
		if floor, seen := least[found.Component.ID]; !seen || rank(found.shared) < rank(floor) {
			least[found.Component.ID] = found.shared
		}
	}
	for i := range findings {
		if floor, seen := least[findings[i].Component.ID]; seen {
			findings[i].Confidence = lower(findings[i].Confidence, floor)
			findings[i].shared = lower(findings[i].shared, floor)
		}
	}
}

// wouldBe is the own confidence of the finding a liveness record's mark kept the
// sweep from producing: the bound declaration's, classified as its kind classifies
// a finding, where a test that references it lowers it as the sweep's test use
// would with the mark withdrawn ([pass.testReachedUse]).
func (in *pass) wouldBe(at int, row *vocab.Kind) vocab.Class {
	mark := &in.Marks[at]
	d, held := in.finding(mark.Bound, mark.Code, "")
	if !held {
		return vocab.Certain
	}
	found := Finding{Code: d.Code, Symbol: in.subjectOf(&d), id: d.id}
	in.classify(&found, &d, row)
	if in.testReachedUse(mark.Bound, mark.Code) {
		return lower(found.Confidence, vocab.Possible)
	}
	return found.Confidence
}
