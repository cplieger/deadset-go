package main

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/kinds"
)

// cascadeModule is a target holding one dead component of two members: a function
// nothing calls and the helper only it calls, declared after it.
func cascadeModule(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() {}\n\n" +
			"// outer is called by nothing.\nfunc outer() int { return inner() }\n\n" +
			"// inner is called by outer alone.\nfunc inner() int { return 1 }\n",
		repositoryDocument: document,
	})
}

// findingOf is the one finding of a set naming one reference.
func findingOf(t *testing.T, set *findingSet, ref string) *kinds.Finding {
	t.Helper()

	for i := range set.result.Findings {
		if set.result.Findings[i].Symbol.Ref == ref {
			return &set.result.Findings[i]
		}
	}
	t.Fatalf("Setup: the run reports no finding about %s", ref)
	return nil
}

func TestACascadeListedInFullNamesEveryMemberOfTheComponent(t *testing.T) {
	dir := cascadeModule(t, `{"target": {"kind": "application"}, "reporters": {"cascade": "full"}}`)
	set := findingsOfDir(t, dir)
	found := findingOf(t, &set, "go://example.com/app#outer")

	var got []string
	for _, member := range found.Component.Members {
		got = append(got, member.Ref)
	}
	want := []string{"go://example.com/app#outer", "go://example.com/app#inner"}
	if !slices.Equal(got, want) {
		t.Errorf("the finding about outer lists the members %v at cascade full, want %v, every member in position order",
			got, want)
	}
	if found.Component.SymbolCount != len(found.Component.Members) {
		t.Errorf("the finding about outer counts %d symbols and lists %d members, want the two equal",
			found.Component.SymbolCount, len(found.Component.Members))
	}
}

func TestACascadeListedByItsRootsNamesNoMember(t *testing.T) {
	dir := cascadeModule(t, `{"target": {"kind": "application"}}`)
	set := findingsOfDir(t, dir)

	if members := findingOf(t, &set, "go://example.com/app#outer").Component.Members; len(members) != 0 {
		t.Errorf("the finding about outer lists the members %+v at the default cascade, want none", members)
	}
}
