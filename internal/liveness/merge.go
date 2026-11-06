package liveness

import (
	"errors"
	"fmt"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
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
// configuration declares, once per identifier and by site, each carrying the
// set of configurations it exists in. References and Roots hold every
// configuration's own, each carrying the configuration it was seen in. Unmatched
// holds one row per configuration that left a string unmatched, with no
// configuration: only how many configurations left it is read.
type Merged struct {
	// InterfaceMethods is every method an interface of the target declares, with
	// what every configuration holds about it ([facts.InterfaceMethods]).
	InterfaceMethods map[inventory.SymbolID]*facts.InterfaceMethod

	// Uncalled is interface satisfaction's rule over the matrix's uncalled
	// interface methods.
	Uncalled exempt.Uncalled

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
// identifier ([inventory.SymbolID]): the first configuration that declares it gives
// its fields, tests its test support, and every configuration that declares it adds
// its bit and its containers. A package's Pos, EndLine, File and Line are the
// exception: they come from the first-sorting of its configurations' anchors
// ([anchoredFirst]). A declaration one configuration alone holds is analyzed under
// that configuration rather than skipped.
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
				m.Symbols[held].Parents = withParents(m.Symbols[held].Parents, one.Symbols[i].Parents)
				anchoredFirst(&m.Symbols[held], &one.Symbols[i])
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
	m.Uncalled = exempt.UncalledMethods(m.InterfaceMethods, m.References, func(id inventory.SymbolID) bool {
		i, held := at[id]
		return held && !m.Symbols[i].InTestFile
	})
	slices.SortFunc(m.Symbols, inventory.BySite)
	return m, nil
}

// anchoredFirst places a package at the first-sorting of the files each
// configuration anchors it at, so its position does not depend on the order the
// matrix lists its configurations in.
func anchoredFirst(held, added *inventory.Symbol) {
	if held.Kind == inventory.KindPackage && inventory.ByPosition(added.Pos, held.Pos) < 0 {
		held.Pos, held.EndLine, held.File, held.Line = added.Pos, added.EndLine, added.File, added.Line
	}
}

// withParents is held with every container of added it lacks, in identifier order,
// so the merge's answer does not depend on the order the matrix lists its
// configurations in.
func withParents(held, added []inventory.SymbolID) []inventory.SymbolID {
	for _, id := range added {
		if !slices.Contains(held, id) {
			held = append(slices.Clone(held), id)
			slices.Sort(held)
		}
	}
	return held
}

// referenceSite keys where a reference is made: the declaration that makes it, the
// declaration it names, and the byte that writes it, which every configuration
// compiling the file agrees on whatever line directive renders it.
type referenceSite struct {
	from inventory.SymbolID
	to   inventory.SymbolID
	site inventory.Offset
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
		site := referenceSite{from: r.From, to: r.To, site: r.Site}
		like := slices.IndexFunc(held[site], func(at int) bool {
			k := &kept[at]
			return k.Configs&r.Configs == 0 && k.Kind == r.Kind && k.Via == r.Via && k.Build == r.Build && k.Test == r.Test
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
