package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
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
func stateOf(set *findingSet, subject *graph.Symbol) explainedState {
	return explainedState{
		found:     findingAbout(set.result.Findings, subject),
		retained:  retentionsOf(set.swept.Retained, subject.ID),
		candidate: candidateFor(set.swept.Candidates, subject.ID),
		live:      set.swept.LiveUnder[subject.ID],
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
// One iteration writes a module and loads it, which costs about a third of a second
// under the race detector, so the property is about forty seconds of the package's
// deadline.
func TestAnExplanationAgreesWithTheReport(t *testing.T) {
	base := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		drawn := drawModule(t, `{"target": {"kind": "application"}}`, false)
		dir, err := os.MkdirTemp(base, "module")
		if err != nil {
			t.Fatalf("create a directory for the generated module: %v", err)
		}
		if err := writeFiles(dir, drawn.files); err != nil {
			t.Fatalf("write the generated module: %v", err)
		}
		set := findingsOfModule(t, dir)

		for i := range set.loaded.merged.Symbols {
			subject := &set.loaded.merged.Symbols[i]
			held := stateOf(&set, subject)
			checkDisjoint(t, subject, &held)
			checkExplanation(t, &set, subject, &held)
		}
		checkOneFindingPerSymbol(t, &set)
	})
}

// findingsOfModule is the findings pass over one generated module, inside a property.
func findingsOfModule(t *rapid.T, dir string) findingSet {
	var refused strings.Builder
	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &refused)
	if code != exitClean {
		t.Fatalf("resolve the configuration of the generated module = %d: %s", code, refused.String())
	}
	options, err := exemptOptions(&resolved.config)
	if err != nil {
		t.Fatalf("exemptOptions() = %v, want the options of the generated configuration", err)
	}
	set, err := findingsOf(t.Context(), &resolved, &options)
	if err != nil {
		t.Fatalf("findingsOf(the generated module) = %v, want the findings of the run", err)
	}
	return set
}

// checkDisjoint asserts that the four states the analysis answers are disjoint, so
// exactly one explanation applies to the symbol.
func checkDisjoint(t *rapid.T, subject *graph.Symbol, held *explainedState) {
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
func checkOneFindingPerSymbol(t *rapid.T, set *findingSet) {
	seen := make(map[string]string, len(set.result.Findings))
	for i := range set.result.Findings {
		found := &set.result.Findings[i]
		key := positionKey(&found.Position)
		if first, twice := seen[key]; twice {
			t.Fatalf("%s is reported under %s and under %s", key, first, found.Code)
		}
		seen[key] = found.Code
	}
}

// checkExplanation reads the printed explanation back and asserts it says what the
// analysis holds.
func checkExplanation(t *rapid.T, set *findingSet, subject *graph.Symbol, held *explainedState) {
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
func checkReported(t *rapid.T, subject *graph.Symbol, found *kinds.Finding, text string) {
	for _, want := range []string{
		"code: " + found.Code + " " + found.Kind,
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
func checkRetained(t *rapid.T, subject *graph.Symbol, retained []graph.Exemption, text string) {
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
// holds, and that the path starts at a symbol the root set names.
func checkPath(t *rapid.T, set *findingSet, subject *graph.Symbol, text string) {
	root, path, reached := pathFrom(set.loaded.merged, subject.ID)
	if !reached {
		if !strings.Contains(text, "  unreachable: ") {
			t.Fatalf("the explanation of %s claims a path where no root reaches it:\n%s", subject.Ref, text)
		}
		return
	}
	if len(path) == 0 {
		if len(rootReasons(set.loaded.merged.Roots, subject.ID)) == 0 {
			t.Fatalf("%s is reached by the empty path and the root set does not name it", subject.Ref)
		}
		return
	}
	if !rootNames(set.loaded.merged.Roots, root.ID) {
		t.Fatalf("the path to %s starts at %s, which the root set does not name", subject.Ref, root.ID)
	}
	if path[0].From != root.ID {
		t.Fatalf("the path to %s starts at %s and its first hop is made by %s", subject.Ref, root.ID, path[0].From)
	}
	if path[len(path)-1].To != subject.ID {
		t.Fatalf("the path to %s ends at %s", subject.Ref, path[len(path)-1].To)
	}
	for i := range path {
		if i > 0 && path[i].From != path[i-1].To {
			t.Fatalf("the path to %s has a gap: hop %d reaches %s and hop %d is made by %s",
				subject.Ref, i-1, path[i-1].To, i, path[i].From)
		}
		if !heldReference(set.loaded.merged.References, path[i]) {
			t.Fatalf("hop %d of the path to %s is no reference the graph holds: %+v", i, subject.Ref, path[i])
		}
		if !strings.Contains(text, "  reference: "+path[i].Pos.String()+"\t") {
			t.Fatalf("the explanation of %s does not carry hop %d at %s:\n%s",
				subject.Ref, i, path[i].Pos, text)
		}
	}
}

// rootNames reports whether the root set names one symbol.
func rootNames(roots []graph.Root, id graph.SymbolID) bool {
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
