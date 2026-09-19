package exempt

import (
	"fmt"
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"pgregory.net/rapid"
)

// The file a drawn declaration is written in, which is what decides whether the
// language's own rule classifies it as a test declaration.
const (
	drawnFile     = "drawn.go"
	drawnTestFile = "drawn_test.go"
)

// drawnRootKinds are the kinds a drawn root takes: one that names a caller, the
// test kind a production sweep withdraws from the seed, the one kind that seeds
// reachability alone, and one a declaration of any file carries.
func drawnRootKinds() []graph.RootKind {
	return []graph.RootKind{graph.RootMain, graph.RootTest, graph.RootPublishedAPI, graph.RootBlank}
}

// drawnDeclaration is one declaration a draw produced.
type drawnDeclaration struct {
	inTest bool
}

// drawnRoot is one root a draw places on one declaration.
type drawnRoot struct {
	at   int
	kind graph.RootKind
}

// drawnRetention is one declaration one class retains.
type drawnRetention struct {
	class Class
	at    int
}

// drawnSet is one symbol set with exemptions planted across it: the declarations,
// the references between them, the roots, what each class retains, the classes
// disabled and the mode the classes compute and the sweep counts under.
type drawnSet struct {
	declarations []drawnDeclaration
	edges        [][2]int
	roots        []drawnRoot
	retentions   []drawnRetention
	disabled     []Class
	mode         graph.Mode
}

// drawnSets draws one to eight declarations, up to twelve references between them,
// up to three roots, up to six retentions spread over the vocabulary, a subset of
// the vocabulary disabled, and the mode.
func drawnSets() *rapid.Generator[drawnSet] {
	return rapid.Custom(func(t *rapid.T) drawnSet {
		declarations := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) drawnDeclaration {
			return drawnDeclaration{inTest: rapid.Bool().Draw(t, "a test file declares it")}
		}), 1, 8).Draw(t, "the declarations")
		count := len(declarations)

		edges := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) [2]int {
			return [2]int{
				rapid.IntRange(0, count-1).Draw(t, "the referencing declaration"),
				rapid.IntRange(0, count-1).Draw(t, "the referenced declaration"),
			}
		}), 0, 12).Draw(t, "the references")
		roots := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) drawnRoot {
			return drawnRoot{
				at:   rapid.IntRange(0, count-1).Draw(t, "the rooted declaration"),
				kind: rapid.SampledFrom(drawnRootKinds()).Draw(t, "the root's kind"),
			}
		}), 0, 3).Draw(t, "the roots")
		retentions := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) drawnRetention {
			return drawnRetention{
				at:    rapid.IntRange(0, count-1).Draw(t, "the retained declaration"),
				class: rapid.SampledFrom(Classes()).Draw(t, "the class that retains it"),
			}
		}), 0, 6).Draw(t, "the retentions")
		disabled := rapid.SliceOfNDistinct(
			rapid.SampledFrom(Classes()), 0, len(Classes()),
			func(c Class) Class { return c },
		).Draw(t, "the disabled classes")

		return drawnSet{
			declarations: declarations,
			edges:        edges,
			roots:        roots,
			retentions:   retentions,
			disabled:     disabled,
			mode: graph.Mode{
				Production: rapid.Bool().Draw(t, "the sweep counts production references alone"),
			},
		}
	})
}

// drawnName names one drawn declaration.
func drawnName(at int) string { return fmt.Sprintf("d%02d", at) }

// graphOf indexes one drawn set the way the production passes would: one
// declaration per line of its own file, the references between them and the roots.
// No load takes place, so a draw costs no package load and shrinks to the
// declarations that carry a failure.
func (d drawnSet) graphOf() (*graph.Graph, []graph.SymbolID) {
	ids := make([]graph.SymbolID, len(d.declarations))
	symbols := make([]graph.Symbol, 0, len(d.declarations))
	lines := map[string]int{}
	for at, declaration := range d.declarations {
		file := drawnFile
		if declaration.inTest {
			file = drawnTestFile
		}
		lines[file]++
		position := token.Position{Filename: file, Line: lines[file], Column: 1}
		ids[at] = graph.SymbolID(fmt.Sprintf("%s:%d:%d", position.Filename, position.Line, position.Column))
		symbols = append(symbols, graph.Symbol{
			ID:      ids[at],
			Ref:     "drawn://" + drawnName(at),
			Name:    drawnName(at),
			PkgPath: "example.com/drawn",
			Pos:     position,
			EndLine: position.Line,
		})
	}

	refs := make([]graph.Reference, 0, len(d.edges))
	for _, e := range d.edges {
		from := symbols[e[0]]
		refs = append(refs, graph.Reference{
			From: from.ID,
			To:   ids[e[1]],
			Pos:  from.Pos,
			Kind: graph.RefCall,
			Test: d.declarations[e[0]].inTest,
		})
	}
	roots := make([]graph.Root, 0, len(d.roots))
	for _, r := range d.roots {
		roots = append(roots, graph.Root{ID: ids[r.at], Kind: r.kind})
	}

	slices.SortFunc(symbols, func(a, b graph.Symbol) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return graph.New(symbols, refs, roots), ids
}

// detectors is the table Compute runs: one detection per class the draw gave a
// retention, each returning what that class retains and reading nothing, so the
// property drives the framework rather than any class's own rule.
func (d drawnSet) detectors(ids []graph.SymbolID) map[Class]Detector {
	byClass := map[Class][]graph.Exemption{}
	for i, r := range d.retentions {
		byClass[r.class] = append(byClass[r.class], graph.Exemption{
			ID:     ids[r.at],
			Class:  string(r.class),
			Detail: drawnName(r.at) + " retained by " + string(r.class),
			Site:   token.Position{Filename: drawnFile, Line: i + 1, Column: 1},
		})
	}

	table := make(map[Class]Detector, len(byClass))
	for class, retained := range byClass {
		table[class] = func(*Input) ([]graph.Exemption, error) { return retained, nil }
	}
	return table
}

// describeDrawn prints one drawn set so a failure carries the set rather than only
// its size.
func (d drawnSet) describeDrawn() string {
	var out strings.Builder
	for at, declaration := range d.declarations {
		fmt.Fprintf(&out, "declaration %s inTest=%t\n", drawnName(at), declaration.inTest)
	}
	for _, e := range d.edges {
		fmt.Fprintf(&out, "reference %s -> %s\n", drawnName(e[0]), drawnName(e[1]))
	}
	for _, r := range d.roots {
		fmt.Fprintf(&out, "root %s %s\n", drawnName(r.at), r.kind)
	}
	for _, r := range d.retentions {
		fmt.Fprintf(&out, "retention %s %s\n", drawnName(r.at), r.class)
	}
	fmt.Fprintf(&out, "disabled %v\nproduction %t\n", d.disabled, d.mode.Production)
	return out.String()
}

// candidateSet names every symbol one sweep reported.
func candidateSet(r graph.Result) map[graph.SymbolID]bool {
	found := make(map[graph.SymbolID]bool, len(r.Candidates))
	for _, c := range r.Candidates {
		found[c.ID] = true
	}
	return found
}

// retainedRecord names every symbol and class one sweep's retained record holds.
func retainedRecord(r graph.Result) map[string]bool {
	found := make(map[string]bool, len(r.Retained))
	for _, e := range r.Retained {
		found[string(e.ID)+" "+e.Class] = true
	}
	return found
}

// heldBack names every exemption of a union whose symbol one sweep reported, which
// is what the retained record of a sweep carrying that union must hold.
func heldBack(union []graph.Exemption, reported map[graph.SymbolID]bool) map[string]bool {
	found := make(map[string]bool, len(union))
	for _, e := range union {
		if reported[e.ID] {
			found[string(e.ID)+" "+e.Class] = true
		}
	}
	return found
}

// sweptWith computes the union of the classes the table holds under one set of
// disabled classes, sweeps the graph with it, and returns both.
func sweptWith(t *rapid.T, g *graph.Graph, table map[Class]Detector, disabled []Class, mode graph.Mode) ([]graph.Exemption, graph.Result) {
	union, err := Compute(&Input{Options: Options{Disabled: disabled}, Mode: mode}, table)
	if err != nil {
		t.Fatalf("Compute over the drawn retentions error: %v", err)
	}
	return union, g.Sweep(graph.SweepInput{Exempt: union, Mode: mode})
}

// Property dead-code-suite/P4: the reported set and the retained set are
// disjoint.
//
// For any drawn symbol set with exemptions planted across the vocabulary and any
// drawn subset of the vocabulary disabled, no symbol the sweep reported carries an
// exemption, every exemption the union holds over a symbol the same sweep without
// the union would have reported is in the retained record with its class, and
// disabling a class only grows the reported set.
//
// The graph is built by hand and swept through the production passes, so the
// property measures the framework and the sweep rather than any class's own rule:
// the table of detections is what a class contributes and the draw is what it
// found.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a deeper
// local run.
func TestProperty04TheReportedSetAndTheRetainedSetAreDisjoint(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := drawnSets().Draw(t, "the symbol set")
		g, ids := d.graphOf()
		table := d.detectors(ids)

		union, r := sweptWith(t, g, table, d.disabled, d.mode)
		reported := candidateSet(r)

		// A class the draw disabled retains nothing, which is what makes the
		// growth below the consequence of the switch rather than of the draw.
		for _, e := range union {
			if slices.Contains(d.disabled, Class(e.Class)) {
				t.Fatalf("Compute retained %s under the disabled class %s\n%s", e.ID, e.Class, d.describeDrawn())
			}
		}

		// No reported symbol carries an exemption. A report naming a symbol
		// something retains is the finding the whole exemption model exists to
		// withdraw.
		for _, e := range union {
			if reported[e.ID] {
				t.Fatalf("Sweep reported %s, which %s retains\n%s", e.ID, e.Class, d.describeDrawn())
			}
		}

		// The retained record holds exactly the exemptions that held a symbol
		// back, each with its class, so print-retained answers why a symbol is
		// absent from the report and never claims a live symbol was held.
		_, bare := sweptWith(t, g, nil, nil, d.mode)
		want := heldBack(union, candidateSet(bare))
		if got := retainedRecord(r); !maps.Equal(got, want) {
			t.Fatalf("Sweep recorded %v as retained, want %v\n%s",
				slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)), d.describeDrawn())
		}

		// Disabling a class can only grow the reported set: an exemption withdrawn
		// stops holding its own symbol back and stops seeding the closure, and
		// neither withdrawal makes anything live.
		_, enabled := sweptWith(t, g, table, nil, d.mode)
		for id := range candidateSet(enabled) {
			if !reported[id] {
				t.Fatalf("disabling %v withdrew the report of %s, want the reported set to only grow\n%s",
					d.disabled, id, d.describeDrawn())
			}
		}
	})
}
