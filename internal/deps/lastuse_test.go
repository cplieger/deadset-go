package deps

import (
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

func TestLastUsesNamesTheModuleOneCandidateAloneUses(t *testing.T) {
	last, named := lastUsesOf(t, "last-use.txtar")

	// A field owns the use written in its own type rather than leaving it with the
	// type that holds it, so the declaration a maintainer deletes is the one named.
	cases := map[string][]string{
		"a function that alone calls into a module": {"onlySolo", "example.com/solo"},
		"a named field of a module's type":          {"holder.thing", "example.com/field"},
		"a field embedding a module's type":         {"embedder.Thing", "example.com/embedded"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			declaration, module := test[0], test[1]
			if got := named[declaration]; !slices.Equal(got, []string{module}) {
				t.Errorf("LastUses(last-use.txtar)[%s] = %v, want [%s]", declaration, got, module)
			}
		})
	}
	if len(last) != len(cases) {
		t.Errorf("LastUses(last-use.txtar) named modules for %v, want %d declarations",
			slices.Sorted(maps.Keys(named)), len(cases))
	}
}

func TestLastUsesNamesNoModuleTwoDeclarationsShare(t *testing.T) {
	_, named := lastUsesOf(t, "last-use.txtar")

	for _, name := range []string{"firstShared", "secondShared"} {
		if got := named[name]; len(got) != 0 {
			t.Errorf("LastUses(last-use.txtar)[%s] = %v, want none, because two declarations use the module",
				name, got)
		}
	}
}

func TestLastUsesNamesNoModuleForAUseOfTheTargetsOwnPackage(t *testing.T) {
	last, named := lastUsesOf(t, "own-module.txtar")

	// The one declaration that uses anything outside its own package uses a second
	// package of the target, whose module is the target's own and is therefore no
	// dependency to lose.
	if len(last) != 0 {
		t.Errorf("LastUses(own-module.txtar) named %v, want none", named)
	}
}

func TestLastUsesNamesNothingWithoutACandidate(t *testing.T) {
	dir := extract(t, "last-use.txtar")
	result := loadDir(t, dir)
	_, resolve := inventoryOf(t, result, dir)

	if got := LastUses(result, resolve, nil); len(got) != 0 {
		t.Errorf("LastUses(last-use.txtar, no candidate) = %v, want none", got)
	}
}

// lastUsesOf runs the whole path from one archive to the join: the load, the
// inventory, the graph, the sweep that answers which declarations are candidates,
// and the join over them. It returns the join and the same answer keyed by
// declaration name, which is what a case asserts on.
func lastUsesOf(t *testing.T, archive string) (map[graph.SymbolID][]string, map[string][]string) {
	t.Helper()

	dir := extract(t, archive)
	result := loadDir(t, dir)
	symbols, resolve := inventoryOf(t, result, dir)

	refs, _, err := graph.References(result, dir, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.References(%s): %v", archive, err)
	}
	roots, _, err := graph.Roots(result, dir, os.ReadFile, symbols, graph.RootOptions{})
	if err != nil {
		t.Fatalf("Setup: graph.Roots(%s): %v", archive, err)
	}
	candidates := graph.New(symbols, refs, roots).Sweep(graph.Mode{}).Candidates
	if len(candidates) == 0 {
		t.Fatalf("Setup: Sweep(%s) returned no candidate, so the join has nothing to answer over", archive)
	}

	last := LastUses(result, resolve, candidates)
	named := make(map[string][]string, len(last))
	for _, s := range symbols {
		if modules, held := last[s.ID]; held {
			named[s.Name] = modules
		}
	}
	return last, named
}

// inventoryOf enumerates one load's declarations and prepares the resolver over
// them.
func inventoryOf(t *testing.T, result *load.Result, dir string) ([]graph.Symbol, *graph.Resolver) {
	t.Helper()

	symbols, err := graph.Symbols(result, dir, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: graph.Symbols(%s): %v", dir, err)
	}
	resolve, err := graph.NewResolver(result, dir, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.NewResolver(%s): %v", dir, err)
	}
	return symbols, resolve
}
