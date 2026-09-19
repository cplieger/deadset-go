package graph

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// The three declarations every P11 draw plants. The guard is declared under every
// configuration and used by a declaration one configuration alone holds, which is
// the false positive a matrix exists to prevent; the one declared under the last
// configuration alone is the declaration a matrix must analyze rather than skip.
const (
	plantedGuard      = "plantedGuard"
	plantedUser       = "plantedUser"
	plantedOnlyInLast = "plantedOnlyInLast"
)

// drawnMatrix is one matrix a draw produced: the graph its configurations share,
// the number of configurations, and the configurations each drawn declaration
// exists in, as one bit per configuration.
type drawnMatrix struct {
	member  []int
	graph   drawnGraph
	configs int
}

// drawnMatrices draws a matrix of one to four configurations over the graph
// Properties 6 and 7 draw, giving each declaration a non-empty set of the
// configurations it exists in.
func drawnMatrices() *rapid.Generator[drawnMatrix] {
	return rapid.Custom(func(t *rapid.T) drawnMatrix {
		g := drawnGraphs().Draw(t, "the graph")
		configs := rapid.IntRange(1, 4).Draw(t, "the number of configurations")
		member := make([]int, g.symbols)
		for at := range member {
			member[at] = rapid.IntRange(1, 1<<configs-1).
				Draw(t, "the configurations "+drawn(at)+" exists in")
		}
		return drawnMatrix{graph: g, configs: configs, member: member}
	})
}

// build assembles the graph every configuration of one drawn matrix is a
// restriction of, planting the guard its user alone references and the
// declaration the last configuration alone holds.
func (d drawnMatrix) build(t *rapid.T) *graphBuilder {
	b := d.graph.build(t, nil)
	b.add(plantedGuard, plantedUser, plantedOnlyInLast)
	b.ref(plantedUser, plantedGuard)
	b.root(plantedUser, RootMain)
	return b
}

// in reports whether one declaration of the drawn matrix exists in one
// configuration. A declaration the draw did not name exists in every
// configuration, which the planted pair the drawn graph carries is.
func (d drawnMatrix) in(config int, name string) bool {
	switch name {
	case plantedUser:
		return config == 0
	case plantedOnlyInLast:
		return config == d.configs-1
	}
	for at := range d.member {
		if drawn(at) == name {
			return d.member[at]&(1<<config) != 0
		}
	}
	return true
}

// describeDrawnMatrix prints one drawn matrix so a failure carries the matrix
// rather than only its size.
func (d drawnMatrix) describe(b *graphBuilder) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%d configurations\n", d.configs)
	for i := range b.symbols {
		name := b.symbols[i].Name
		var configs []int
		for config := range d.configs {
			if d.in(config, name) {
				configs = append(configs, config)
			}
		}
		fmt.Fprintf(&out, "%s exists in %v\n", name, configs)
	}
	out.WriteString(describeDrawn(d.graph))
	return out.String()
}

// verdictLines names every candidate of one result: the declaration, the relation
// that found it and the configurations it is dead in.
func verdictLines(b *graphBuilder, candidates []Candidate) []string {
	found := make([]string, 0, len(candidates))
	for _, c := range candidates {
		found = append(found, fmt.Sprintf("%s %s %v", b.named[c.ID], c.Relation, c.Configs.Indexes()))
	}
	return found
}

// Property dead-code-suite/P11: over a matrix of any size and any per-configuration
// reference set, a declaration is reported when and only when every configuration
// it exists in reports it, a declaration one configuration alone holds is analyzed
// under that configuration rather than skipped, a reference under any configuration
// keeps the declaration from the stronger claim, and each report names exactly the
// configurations the declaration exists in.
//
// The oracle is one sweep per configuration over the graph of that configuration
// alone, which is the sweep the rest of the suite measures, combined by the rule
// above over the names a draw produced. Neither the restriction the matrix applies
// nor the combination it makes is used to compute it.
//
// The graph is built by hand rather than loaded, so a draw costs no package load
// and shrinks to the one declaration that carries a failure.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a
// deeper local run.
func TestProperty11MatrixIntersectionReportsOnlyWhatIsDeadEverywhere(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := drawnMatrices().Draw(t, "the matrix")
		b := d.build(t)
		in := d.graph.input(b)
		per := b.configured(d.configs, d.in)

		merged, err := Merge(per)
		if err != nil {
			t.Fatalf("Merge over %d configurations = _, %v, want no error", d.configs, err)
		}
		r := NewMatrix(&merged).Sweep(in)

		oracle := make([]map[SymbolID]Candidate, d.configs)
		for config := range d.configs {
			one := per[config]
			answer := New(one.Symbols, one.References, one.Roots).Sweep(in)
			oracle[config] = make(map[SymbolID]Candidate, len(answer.Candidates))
			for _, c := range answer.Candidates {
				oracle[config][c.ID] = c
			}
		}

		want := make([]string, 0, len(merged.Symbols))
		for i := range merged.Symbols {
			name := b.named[merged.Symbols[i].ID]
			var configs []int
			relation := ReferenceCounting
			everywhere := true
			for config := range d.configs {
				if !d.in(config, name) {
					continue
				}
				configs = append(configs, config)
				c, candidate := oracle[config][merged.Symbols[i].ID]
				switch {
				case !candidate:
					everywhere = false
				case c.Relation != ReferenceCounting:
					relation = Reachability
				}
			}
			if everywhere {
				want = append(want, fmt.Sprintf("%s %s %v", name, relation, configs))
			}
		}
		if got := verdictLines(b, r.Candidates); !slices.Equal(got, want) {
			t.Fatalf("Sweep over the drawn matrix reported\n%s\nwant\n%s\n%s",
				strings.Join(got, "\n"), strings.Join(want, "\n"), d.describe(b))
		}

		// The planted guard is used under the one configuration that declares its
		// user and by nothing under the others, so a reference under one
		// configuration is what keeps it out of the report.
		reported := verdictLines(b, r.Candidates)
		for _, line := range reported {
			if strings.HasPrefix(line, plantedGuard+" ") {
				t.Fatalf("Sweep over the drawn matrix reported %q, want the declaration used under one configuration to be reported under none\n%s",
					line, d.describe(b))
			}
		}

		// The declaration the last configuration alone holds is analyzed under
		// that configuration, so it is reported and names it alone.
		last := fmt.Sprintf("%s %s %v", plantedOnlyInLast, ReferenceCounting, []int{d.configs - 1})
		if !slices.Contains(reported, last) {
			t.Fatalf("Sweep over the drawn matrix reported\n%s\nwant it to hold %q\n%s",
				strings.Join(reported, "\n"), last, d.describe(b))
		}

		// A declaration any configuration holds a reference to cannot be reported
		// under the stronger claim, because a reference under any configuration is
		// a reference.
		referenced := make(map[SymbolID]bool, len(merged.References))
		for _, ref := range merged.References {
			referenced[ref.To] = true
		}
		for _, c := range r.Candidates {
			if referenced[c.ID] && c.Relation == ReferenceCounting {
				t.Fatalf("Sweep over the drawn matrix reported %s under %s although a configuration holds a reference to it\n%s",
					b.named[c.ID], c.Relation, d.describe(b))
			}
		}
	})
}
