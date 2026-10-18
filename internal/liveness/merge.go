package liveness

import (
	"errors"
	"fmt"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// ErrMatrix reports a matrix the merge cannot key a configuration of.
var ErrMatrix = errors.New("liveness: unmergeable matrix")

// Configured is what the passes returned for one build configuration: the
// declarations that configuration holds, the references made to them by that
// configuration's files and by every consumer loaded with it, the roots that seed
// it, and the configured strings its root detection matched nothing with.
type Configured struct {
	Symbols    []inventory.Symbol
	References []facts.Reference
	Roots      []roots.Root
	Unmatched  []roots.Unmatched
	Interfaces []facts.Interface
}

// Merged is one matrix's inventory. Symbols holds every declaration any
// configuration declares, once per source position and by site, each carrying the
// set of configurations it exists in. References and Roots hold every
// configuration's own, each carrying the configuration it was seen in. Unmatched
// holds one row per configuration that left a string unmatched, with no
// configuration: only how many configurations left it is read.
type Merged struct {
	// InterfaceMethods is every method an interface of the target declares, with
	// what every configuration holds about it ([facts.InterfaceMethods]).
	InterfaceMethods map[inventory.SymbolID]*facts.InterfaceMethod

	Tests          roots.TestClasses // the matrix's, [MatrixTests]
	Symbols        []inventory.Symbol
	References     []facts.Reference
	Roots          []roots.Root
	Unmatched      []roots.Unmatched
	Configurations int
}

// Merge combines what the passes returned for each configuration of a matrix into
// one inventory, per's order being the configuration index, under the matrix's test
// classes ([MatrixTests]). A declaration is keyed across configurations by its
// identifier, the rendered position: the first configuration that declares it gives
// its fields, tests its test support, and every configuration that declares it adds
// its bit. A declaration one configuration alone holds is analyzed under that
// configuration rather than skipped.
func Merge(per []Configured, tests roots.TestClasses) (Merged, error) {
	if len(per) == 0 {
		return Merged{}, fmt.Errorf("%w: no configuration", ErrMatrix)
	}
	if len(per) > inventory.MaxConfigurations {
		return Merged{}, fmt.Errorf("%w: %d configurations, at most %d",
			ErrMatrix, len(per), inventory.MaxConfigurations)
	}

	m := Merged{Configurations: len(per), Tests: tests}
	ifaces := make([][]facts.Interface, len(per))
	at := make(map[inventory.SymbolID]int)
	for config, one := range per {
		for i := range one.Symbols {
			if held, ok := at[one.Symbols[i].ID]; ok {
				m.Symbols[held].Configs = m.Symbols[held].Configs.With(config)
				continue
			}
			s := one.Symbols[i]
			s.Configs = inventory.ConfigSet(0).With(config)
			s.TestSupport = tests.Support[s.Package]
			at[s.ID] = len(m.Symbols)
			m.Symbols = append(m.Symbols, s)
		}
		for i := range one.References {
			r := one.References[i]
			r.Configs = inventory.ConfigSet(0).With(config)
			m.References = append(m.References, r)
		}
		for i := range one.Roots {
			r := one.Roots[i]
			r.Config = config
			m.Roots = append(m.Roots, r)
		}
		m.Unmatched = append(m.Unmatched, one.Unmatched...)
		ifaces[config] = one.Interfaces
	}
	m.InterfaceMethods = facts.InterfaceMethods(ifaces)
	slices.SortFunc(m.Symbols, inventory.BySite)
	return m, nil
}

// referenceSite keys where a reference is made: the module that makes it, the
// declaration that makes it, the declaration it names, and the position it is
// written at. The position's byte offset is not part of the key, so the key holds
// whether or not a rendering carries one.
//
// The module is part of the key because a position is only unique inside one
// module: two consumers hold a file of one name, and a reference each makes at one
// line of it is two references.
type referenceSite struct {
	consumer string
	from     inventory.SymbolID
	to       inventory.SymbolID
	file     string
	line     int
	col      int
}

// distinct returns every configuration's references with the duplicates a matrix
// produces merged: references at one site alike in kind, rule and file class are one
// holding the union of the configurations that made them, whatever their carriers
// (the first's stay on it; [Graph.carrying] keeps each). A reference, whose own
// configuration [Merge] sets, merges into the first alike one kept that no
// configuration of its own made, so two alike in one configuration stay two, each
// paired in order with another configuration's, and a one-configuration matrix
// counts exactly what a single load counts.
func distinct(refs []facts.Reference) []facts.Reference {
	held := make(map[referenceSite][]int, len(refs))
	kept := make([]facts.Reference, 0, len(refs))
	for i := range refs {
		r := &refs[i]
		site := referenceSite{
			consumer: r.Consumer,
			from:     r.From,
			to:       r.To,
			file:     r.Pos.Filename,
			line:     r.Pos.Line,
			col:      r.Pos.Column,
		}
		like := slices.IndexFunc(held[site], func(at int) bool {
			k := &kept[at]
			return k.Configs&r.Configs == 0 && k.Kind == r.Kind && k.Via == r.Via && k.Test == r.Test
		})
		if like >= 0 {
			kept[held[site][like]].Configs |= r.Configs
			continue
		}
		held[site] = append(held[site], len(kept))
		kept = append(kept, *r)
	}
	return kept
}

// UnmatchedEverywhere returns the configured strings no configuration of the matrix
// matched, in the order the first configuration that reported each lists them.
//
// A string one configuration matched names something, so the answer is the
// intersection and not the union: a pattern naming a symbol one platform declares
// is not a pattern that names nothing, and a matrix of one configuration answers
// that configuration's own set.
func (m *Merged) UnmatchedEverywhere() []roots.Unmatched {
	if m.Configurations == 0 {
		return nil
	}
	count := make(map[string]int, len(m.Unmatched))
	for i := range m.Unmatched {
		count[m.Unmatched[i].Source]++
	}
	everywhere := make([]roots.Unmatched, 0, len(count))
	held := make(map[string]bool, len(count))
	for i := range m.Unmatched {
		source := m.Unmatched[i].Source
		if held[source] || count[source] != m.Configurations {
			continue
		}
		held[source] = true
		everywhere = append(everywhere, roots.Unmatched{Source: source})
	}
	return everywhere
}
