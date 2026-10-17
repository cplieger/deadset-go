package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"pgregory.net/rapid"
)

// explainedState is the state one symbol is in as the analysis answers it, computed
// from the answers the sweep and the findings pass produced rather than from the
// explanation's own switch, so the property compares two readings of one analysis.
type explainedState struct {
	found     *kinds.Finding
	retained  []graph.Exemption
	candidate *graph.Candidate
	live      graph.RelationSet
}

// stateOf reads what the analysis holds about one symbol.
func stateOf(set *run.Findings, subject *inventory.Symbol) explainedState {
	return explainedState{
		found:     findingAbout(set.Result.Findings, subject),
		retained:  retentionsOf(set.Liveness.Retained, subject.ID),
		candidate: candidateFor(set.Liveness.Candidates, subject.ID),
		live:      set.Liveness.LiveUnder[subject.ID],
	}
}

// token is the answer the explanation prints for this state.
func (s *explainedState) token() state {
	switch {
	case s.found != nil:
		return stateReported
	case len(s.retained) > 0:
		return stateRetained
	default:
		return livenessState(s.candidate, s.live)
	}
}

// explainedModules is the number of generated modules the property is stated over.
//
// The property is about agreement per symbol over ONE analysis, so an iteration needs
// no analysis of its own: what it reads is a symbol of an inventory and the two
// answers the analysis holds about it. An analysis of a generated module costs about a
// second under the race detector, because the module carries a test file and the
// toolchain's synthesized test main pulls in the testing package's whole dependency
// graph, so the number of analyses is what the property's cost is, and it is this
// number rather than the iteration count.
const explainedModules = 10

// analyzedModules generates the modules the property draws from and analyzes each of
// them once. The draws are the fixed examples of the module generator, one per seed,
// so the set is the same on every run and a failure names a module a re-run produces.
//
// The four reference sites and the interface-satisfying method are why these modules
// are the generated ones rather than a hand-written fixture: between them they put
// symbols in all four states the explanation answers, which no reference structure
// alone produces.
func analyzedModules(t *testing.T) []*run.Findings {
	t.Helper()

	generator := drawnModules(`{"target": {"kind": "application"}}`, false)
	held := make([]*run.Findings, explainedModules)
	for seed := range held {
		set := cachedFindings(t.Context(), t, generator.Example(seed).files)
		held[seed] = set
	}
	return held
}

// TestAnExplanationAgreesWithTheReport is property dead-code-suite/P30: exactly one
// explanation applies to a symbol, and it is consistent with the report.
//
// The four states are disjoint by the analysis rather than by the explanation's own
// ordering, which is what the first half asserts: no symbol is both reported and
// retained, a retained symbol is never a candidate, and a symbol no finding names and
// no exemption held back is a candidate or is live under both relations, never
// neither and never both. The second half reads the printed answer back: the one
// answer line carries the state, a reported symbol's answer carries the finding's own
// code, class, configurations and consumers, a retained symbol's answer names every
// class the sweep recorded, and a live symbol's path is a path of references the graph
// holds, from a symbol the root set names.
//
// An iteration draws one analyzed module and reads every symbol of its inventory,
// which is the same assertion set the property has always made over one module and
// costs nothing beyond rendering an explanation per symbol.
func TestAnExplanationAgreesWithTheReport(t *testing.T) {
	analyzed := analyzedModules(t)

	rapid.Check(t, func(t *rapid.T) {
		set := analyzed[rapid.IntRange(0, explainedModules-1).Draw(t, "the generated module's seed")]

		for i := range set.Merged.Symbols {
			subject := &set.Merged.Symbols[i]
			held := stateOf(set, subject)
			checkDisjoint(t, subject, &held)
			checkExplanation(t, set, subject, &held)
		}
		checkOneFindingPerSymbol(t, set)
	})
}

// checkDisjoint asserts that the four states the analysis answers are disjoint, so
// exactly one explanation applies to the symbol.
func checkDisjoint(t *rapid.T, subject *inventory.Symbol, held *explainedState) {
	if held.found != nil && len(held.retained) > 0 {
		t.Fatalf("%s is reported under %s and held back by %d exemption classes",
			subject.Ref, held.found.Code, len(held.retained))
	}
	if len(held.retained) > 0 && held.candidate != nil {
		t.Fatalf("%s is held back by %d exemption classes and is a candidate of the sweep",
			subject.Ref, len(held.retained))
	}
	if held.found != nil || len(held.retained) > 0 {
		return
	}
	bothHold := held.live.Has(graph.ReferenceCounting) && held.live.Has(graph.Reachability)
	if held.candidate != nil && bothHold {
		t.Fatalf("%s is a candidate of the sweep and live under both relations", subject.Ref)
	}
	// A declaration the sweep judges is a candidate or is live under a relation, so
	// a declaration answering neither is one it judges nothing about, which is the
	// signature the unjudged state reads.
	if held.candidate == nil && !bothHold && held.live != 0 {
		t.Fatalf("%s is no candidate, is not live under both relations, and is live under %s",
			subject.Ref, relationsOf(held.live))
	}
}

// checkOneFindingPerSymbol asserts that no declaration of the inventory is reported
// twice, which is what makes the reported answer one finding's.
func checkOneFindingPerSymbol(t *rapid.T, set *run.Findings) {
	seen := make(map[string]vocab.Code, len(set.Result.Findings))
	for i := range set.Result.Findings {
		found := &set.Result.Findings[i]
		key := positionKey(&found.Position)
		if first, twice := seen[key]; twice {
			t.Fatalf("%s is reported under %s and under %s", key, first, found.Code)
		}
		seen[key] = found.Code
	}
}

// checkExplanation reads the printed explanation back and asserts it says what the
// analysis holds.
func checkExplanation(t *rapid.T, set *run.Findings, subject *inventory.Symbol, held *explainedState) {
	var printed strings.Builder
	if err := writeExplanation(&printed, set, graph.CascadeRoots, subject); err != nil {
		t.Fatalf("writeExplanation(%s) = %v, want the explanation", subject.Ref, err)
	}
	text := printed.String()

	answers := 0
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "answer: ") {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("the explanation of %s carries %d answer lines, want 1:\n%s", subject.Ref, answers, text)
	}
	if want := "answer: " + string(held.token()); !strings.Contains(text, want) {
		t.Fatalf("the explanation of %s does not carry %q:\n%s", subject.Ref, want, text)
	}

	switch held.token() {
	case stateReported:
		checkReported(t, subject, held.found, text)
	case stateRetained:
		checkRetained(t, subject, held.retained, text)
	case stateLive, stateDead:
		checkPath(t, set, subject, text)
	case stateUnjudged:
		if !strings.Contains(text, "  unjudged: ") {
			t.Fatalf("the explanation of the unjudged %s says nothing about why:\n%s", subject.Ref, text)
		}
	}
}

// checkReported asserts the reported answer carries what the finding carries.
func checkReported(t *rapid.T, subject *inventory.Symbol, found *kinds.Finding, text string) {
	for _, want := range []string{
		"code: " + string(found.Code) + " " + found.Kind,
		"message: " + found.Message,
		"class: " + string(found.Class),
		"confidence: " + string(found.Confidence),
		"configurations: " + listed(found.Configurations),
		"loaded consumers: " + listed(found.ConsumersLoaded),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the explanation of the reported %s does not carry %q:\n%s", subject.Ref, want, text)
		}
	}
}

// checkRetained asserts the retained answer names every class the sweep recorded, and
// no other.
func checkRetained(t *rapid.T, subject *inventory.Symbol, retained []graph.Exemption, text string) {
	named := 0
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "  class: ") {
			named++
		}
	}
	if named != len(retained) {
		t.Fatalf("the explanation of the retained %s names %d classes, want %d:\n%s",
			subject.Ref, named, len(retained), text)
	}
	for i := range retained {
		if !strings.Contains(text, "  class: "+retained[i].Class+"\t") {
			t.Fatalf("the explanation of %s does not name the class %s the sweep recorded:\n%s",
				subject.Ref, retained[i].Class, text)
		}
	}
}

// checkPath asserts every hop the liveness answer prints is a reference the graph
// holds, and that the path starts at a symbol the root set names or at a loaded
// consumer's reference.
func checkPath(t *rapid.T, set *run.Findings, subject *inventory.Symbol, text string) {
	path, reached := set.Matrix.Path(subject.ID, set.Mode)
	if !reached {
		path, reached = set.Matrix.Path(subject.ID, graph.Mode{ConsumerTestsProduction: set.Mode.ConsumerTestsProduction})
	}
	if !reached {
		if !strings.Contains(text, "  unreachable: ") {
			t.Fatalf("the explanation of %s claims a path where no root reaches it:\n%s", subject.Ref, text)
		}
		return
	}
	if path.Root != nil && len(path.Hops) == 0 {
		if len(rootReasons(set.Merged.Roots, subject.ID)) == 0 {
			t.Fatalf("%s is reached by the empty path and the root set does not name it", subject.Ref)
		}
		return
	}
	hops := path.Hops
	switch {
	case path.Root == nil:
		if hops[0].Consumer == "" {
			t.Fatalf("the path to %s starts at no root and its first hop is no consumer's reference: %+v", subject.Ref, hops[0])
		}
	case !rootNames(set.Merged.Roots, path.Root.ID):
		t.Fatalf("the path to %s starts at %s, which the root set does not name", subject.Ref, path.Root.ID)
	case hops[0].From != path.Root.ID:
		t.Fatalf("the path to %s starts at %s and its first hop is made by %s", subject.Ref, path.Root.ID, hops[0].From)
	}
	if hops[len(hops)-1].To != subject.ID {
		t.Fatalf("the path to %s ends at %s", subject.Ref, hops[len(hops)-1].To)
	}
	for i := range hops {
		if i > 0 && hops[i].From != hops[i-1].To {
			t.Fatalf("the path to %s has a gap: hop %d reaches %s and hop %d is made by %s",
				subject.Ref, i-1, hops[i-1].To, i, hops[i].From)
		}
		if !heldReference(set.Merged.References, hops[i]) {
			t.Fatalf("hop %d of the path to %s is no reference the graph holds: %+v", i, subject.Ref, hops[i])
		}
		if hops[i].Consumer == "" && !strings.Contains(text, "  reference: "+hops[i].Pos.String()+"\t") {
			t.Fatalf("the explanation of %s does not carry hop %d at %s:\n%s", subject.Ref, i, hops[i].Pos, text)
		}
	}
}

// rootNames reports whether the root set names one symbol.
func rootNames(roots []graph.Root, id inventory.SymbolID) bool {
	for i := range roots {
		if roots[i].ID == id {
			return true
		}
	}
	return false
}

// heldReference reports whether one reference is a reference the graph holds.
func heldReference(references []graph.Reference, one graph.Reference) bool {
	return slices.Contains(references, one)
}
