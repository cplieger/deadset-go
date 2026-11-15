package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/draws"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// explainedState is the state one symbol is in as the analysis answers it, computed
// from the answers the sweep and the findings pass produced rather than from the
// explanation's own switch, so the property compares two readings of one analysis.
type explainedState struct {
	found     *findings.Finding
	retained  []exempt.Exemption
	candidate *liveness.Candidate
	live      liveness.RelationSet
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

// explainedModules is the full count of generated modules the property is stated
// over, of which a pull request analyzes a quarter ([draws.Seeds]). The property
// reads each symbol of ONE analysis and its two answers about it, so the number of
// analyses, each loading a test file's whole test main, is its cost.
const explainedModules = 10

// analyzedModules generates the modules the property draws from and analyzes each of
// them once, side by side: the generator's fixed example per seed, so a failure
// names a module a re-run produces. Their four reference sites and the
// interface-satisfying method put symbols in all four states the explanation
// answers, which a hand-written fixture's reference structure alone does not.
func analyzedModules(t *testing.T) []*run.Findings {
	t.Helper()

	generator := drawnModules(`{"target": {"kind": "application"}}`, false)
	held := make([]*run.Findings, draws.Seeds(explainedModules, draws.Loading))
	t.Run("analyze", func(t *testing.T) {
		for seed := range draws.Seeds(explainedModules, draws.Loading) {
			t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
				t.Parallel()
				held[seed] = cachedFindings(t.Context(), t, generator.Example(int(seed)).files)
			})
		}
	})
	if t.Failed() {
		t.FailNow()
	}
	return held
}

// TestAnExplanationAgreesWithTheReport is property dead-code-suite/P30: exactly one
// explanation applies to a symbol, and it is consistent with the report. First the
// four states are disjoint by the analysis: none both reported and retained, a
// retained symbol never a candidate, any other a candidate or live under both
// relations. Then the printed answer carries the state, a finding's code, class,
// configurations and consumers, every class the sweep recorded for a retained
// symbol, and for a live one a path of the graph from a root. Each module is
// checked once, as its own parallel subtest, over every symbol of its inventory.
func TestAnExplanationAgreesWithTheReport(t *testing.T) {
	t.Parallel()
	analyzed := analyzedModules(t)

	for drawn, set := range analyzed {
		t.Run(fmt.Sprintf("seed_%d", drawn), func(t *testing.T) {
			t.Parallel()
			for i := range set.Merged.Symbols {
				subject := &set.Merged.Symbols[i]
				held := stateOf(set, subject)
				checkDisjoint(t, subject, &held)
				checkExplanation(t, set, subject, &held)
			}
			checkOneFindingPerSymbol(t, set)
		})
	}
}

// checkDisjoint asserts that the four states the analysis answers are disjoint, so
// exactly one explanation applies to the symbol.
func checkDisjoint(t *testing.T, subject *inventory.Symbol, held *explainedState) {
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
	bothHold := held.live.Has(liveness.ReferenceCounting) && held.live.Has(liveness.Reachability)
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
func checkOneFindingPerSymbol(t *testing.T, set *run.Findings) {
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
func checkExplanation(t *testing.T, set *run.Findings, subject *inventory.Symbol, held *explainedState) {
	var printed strings.Builder
	if err := writeExplanation(&printed, set, false, subject); err != nil {
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
func checkReported(t *testing.T, subject *inventory.Symbol, found *findings.Finding, text string) {
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
func checkRetained(t *testing.T, subject *inventory.Symbol, retained []exempt.Exemption, text string) {
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

// checkPath asserts the liveness answer prints where the path begins: a symbol the
// root set names, a loaded consumer's reference, an exemption or a mark, each with
// its seed line; and every hop: a reference the graph holds at its position, and a
// use line for every hop that is no reference.
func checkPath(t *testing.T, set *run.Findings, subject *inventory.Symbol, text string) {
	path, reached := set.Liveness.Path(subject.ID)
	if !reached {
		if !strings.Contains(text, "  unreachable: ") {
			t.Fatalf("the explanation of %s claims a path where no root reaches it:\n%s", subject.Ref, text)
		}
		return
	}
	hops := path.Hops
	start := subject.ID
	if len(hops) > 0 {
		start = hops[0].From
	}
	switch {
	case path.Exemption != nil:
		if start != path.Exemption.ID || !strings.Contains(text, "  reached from: exemption "+path.Exemption.Class+" of "+string(path.Exemption.ID)+"\n") {
			t.Fatalf("the path to %s starts at the exemption of %s and the explanation does not say so from %s:\n%s", subject.Ref, path.Exemption.ID, start, text)
		}
	case path.Mark != "":
		if start != path.Mark || !strings.Contains(text, "  reached from: suppression of "+string(path.Mark)+"\n") {
			t.Fatalf("the path to %s starts at the mark on %s and the explanation does not say so from %s:\n%s", subject.Ref, path.Mark, start, text)
		}
	case path.Root == nil:
		if len(hops) == 0 || hops[0].Reference == nil || hops[0].Reference.Consumer == "" ||
			!strings.Contains(text, "  reached from: loaded consumer "+hops[0].Reference.Consumer+"\n") {
			t.Fatalf("the path to %s starts at no root and the explanation names no loaded consumer's reference:\n%s", subject.Ref, text)
		}
	case !rootNames(set.Merged.Roots, path.Root.ID):
		t.Fatalf("the path to %s starts at %s, which the root set does not name", subject.Ref, path.Root.ID)
	case start != path.Root.ID:
		t.Fatalf("the path to %s starts at %s and its first hop is made by %s", subject.Ref, path.Root.ID, start)
	}
	if len(hops) > 0 && hops[len(hops)-1].To != subject.ID {
		t.Fatalf("the path to %s ends at %s", subject.Ref, hops[len(hops)-1].To)
	}
	for i := range hops {
		if i > 0 && hops[i].From != hops[i-1].To {
			t.Fatalf("the path to %s has a gap: hop %d reaches %s and hop %d is made by %s",
				subject.Ref, i-1, hops[i-1].To, i, hops[i].From)
		}
		checkHop(t, set, subject, &hops[i], text)
	}
}

// checkHop asserts one hop of a path is printed: a reference of the target as the
// reference the graph holds at its position, a use as its use line.
func checkHop(t *testing.T, set *run.Findings, subject *inventory.Symbol, hop *liveness.Hop, text string) {
	r := hop.Reference
	if r == nil {
		if !strings.Contains(text, "  use: ") || !strings.Contains(text, "\t"+string(hop.From)+" -> "+string(hop.To)+"\n") {
			t.Fatalf("the explanation of %s does not carry the use %s -> %s:\n%s", subject.Ref, hop.From, hop.To, text)
		}
		return
	}
	if !slices.ContainsFunc(set.Merged.References, func(held facts.Reference) bool {
		return held.Consumer == r.Consumer && held.From == r.From && held.To == r.To && held.Pos == r.Pos && r.Configs&held.Configs != 0
	}) {
		t.Fatalf("a hop of the path to %s is no reference the graph holds: %+v", subject.Ref, *r)
	}
	if r.Consumer == "" && !strings.Contains(text, "  reference: "+r.Pos.String()+"\t") {
		t.Fatalf("the explanation of %s does not carry the hop at %s:\n%s", subject.Ref, r.Pos, text)
	}
}

// rootNames reports whether the root set names one symbol.
func rootNames(rooted []roots.Root, id inventory.SymbolID) bool {
	for i := range rooted {
		if rooted[i].ID == id {
			return true
		}
	}
	return false
}
