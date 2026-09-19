package graph

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
)

// ErrMatrix reports a matrix the merge cannot key a configuration of.
var ErrMatrix = errors.New("graph: unmergeable matrix")

// maxConfigurations is the number of configurations one [ConfigSet] holds. The
// matrix is the set of configurations a project builds, which is the two or three
// a continuous-integration run already covers rather than the product of the
// atoms in its build constraints, so the bound is generous by two orders of
// magnitude; a matrix past it is refused rather than silently narrowed to the
// first sixty-four.
const maxConfigurations = 64

// ConfigSet is the set of build configurations a declaration exists in: one bit
// per configuration, at the configuration's place in the matrix.
type ConfigSet uint64

// Has reports whether the set holds the configuration at index config.
func (s ConfigSet) Has(config int) bool {
	if config < 0 || config >= maxConfigurations {
		return false
	}
	return s&(ConfigSet(1)<<config) != 0
}

// Indexes lists the configurations the set holds, in matrix order.
func (s ConfigSet) Indexes() []int {
	found := make([]int, 0, bits.OnesCount64(uint64(s)))
	for config := range maxConfigurations {
		if s.Has(config) {
			found = append(found, config)
		}
	}
	return found
}

// with returns the set holding config as well. A configuration outside the bound
// is held by no set, which is why the merge refuses a matrix it could not key.
func (s ConfigSet) with(config int) ConfigSet {
	if config < 0 || config >= maxConfigurations {
		return s
	}
	return s | ConfigSet(1)<<config
}

// Configured is what the three passes returned for one build configuration: the
// declarations that configuration holds, the references made to them by that
// configuration's files and by every consumer loaded with it, the roots that seed
// it, and the configured strings its root detection matched nothing with.
type Configured struct {
	Symbols    []Symbol
	References []Reference
	Roots      []Root
	Unmatched  []Unmatched
}

// Merged is one matrix's inventory.
//
// Symbols holds every declaration any configuration declares, once per source
// position, ordered by site the way one configuration's enumeration is, each
// carrying the set of configurations it exists in. References, Roots and Unmatched
// hold every configuration's own, each carrying the configuration it was seen in,
// so the set one configuration contributed is recoverable from the whole.
type Merged struct {
	Symbols        []Symbol
	References     []Reference
	Roots          []Root
	Unmatched      []Unmatched
	Configurations int
}

// Merge combines what the passes returned for each configuration of a matrix into
// one inventory, per matching a configuration's place in per to the index it is
// keyed by everywhere else.
//
// A declaration is keyed across configurations by its identifier, which is the
// rendered source position and so is the same in every configuration that
// compiles the file: the fields of the first configuration that declared it are
// the ones kept, and every later configuration adds its bit. The fields cannot
// disagree, because one position names one declaration written once in one file
// and no configuration changes the bytes of that file; where two configurations
// disagreed all the same, the lowest configuration index would decide.
//
// A declaration one configuration alone holds is in the inventory with that one
// configuration's bit, because a symbol present on one platform is analyzed under
// that platform rather than skipped.
func Merge(per []Configured) (Merged, error) {
	if len(per) == 0 {
		return Merged{}, fmt.Errorf("%w: no configuration", ErrMatrix)
	}
	if len(per) > maxConfigurations {
		return Merged{}, fmt.Errorf("%w: %d configurations, at most %d",
			ErrMatrix, len(per), maxConfigurations)
	}

	m := Merged{Configurations: len(per)}
	at := make(map[SymbolID]int)
	for config, one := range per {
		for i := range one.Symbols {
			if held, ok := at[one.Symbols[i].ID]; ok {
				m.Symbols[held].Configs = m.Symbols[held].Configs.with(config)
				continue
			}
			s := one.Symbols[i]
			s.Configs = ConfigSet(0).with(config)
			at[s.ID] = len(m.Symbols)
			m.Symbols = append(m.Symbols, s)
		}
		for i := range one.References {
			r := one.References[i]
			r.Config = config
			m.References = append(m.References, r)
		}
		for i := range one.Roots {
			r := one.Roots[i]
			r.Config = config
			m.Roots = append(m.Roots, r)
		}
		for i := range one.Unmatched {
			u := one.Unmatched[i]
			u.Config = config
			m.Unmatched = append(m.Unmatched, u)
		}
	}
	slices.SortFunc(m.Symbols, bySite)
	return m, nil
}

// Matrix is one matrix's inventory indexed for the sweep: one graph per
// configuration holding what that configuration holds, and one graph over every
// configuration at once.
//
// The per-configuration graphs are what decides liveness, because liveness is a
// question about one configuration: a file another platform does not compile
// makes no reference there, and a root another platform does not declare seeds
// nothing there. The graph over every configuration is what the components are
// computed on, so that one dead component is one entry in a report rather than
// one per configuration.
type Matrix struct {
	merged *Merged
	union  *Graph
	per    []*Graph
}

// NewMatrix indexes one merged inventory.
//
// The inventory is held rather than copied, as [New] holds the symbols it is
// given, so a caller that changes it afterwards changes what every later sweep
// answers.
//
// A configuration a declaration's bitset names outside the matrix is no
// configuration of this matrix and decides nothing, which is what the merge's
// refusal of an over-long matrix leaves as the only way such a bit arrives.
func NewMatrix(m *Merged) *Matrix {
	x := &Matrix{merged: m, per: make([]*Graph, m.Configurations)}
	x.union = New(m.Symbols, distinct(m.References), m.Roots)
	for config := range m.Configurations {
		one := restrict(m, config)
		x.per[config] = New(one.Symbols, one.References, one.Roots)
	}
	return x
}

// restrict returns what one configuration of the matrix holds: the declarations
// whose bitset carries it, and the references and roots it was the configuration
// of.
func restrict(m *Merged, config int) Configured {
	var one Configured
	for i := range m.Symbols {
		if m.Symbols[i].Configs.Has(config) {
			one.Symbols = append(one.Symbols, m.Symbols[i])
		}
	}
	for i := range m.References {
		if m.References[i].Config == config {
			one.References = append(one.References, m.References[i])
		}
	}
	for i := range m.Roots {
		if m.Roots[i].Config == config {
			one.Roots = append(one.Roots, m.Roots[i])
		}
	}
	return one
}

// referenceSite keys one reference by what makes it one reference: the module that
// makes it, the declaration that makes it, the declaration it names, and the
// position it is written at. The position's byte offset is not part of the key, so
// the key holds whether or not a rendering carries one.
//
// The module is part of the key because a position is only unique inside one
// module: two consumers hold a file of one name, and a reference each makes at one
// line of it is two references.
type referenceSite struct {
	consumer string
	from     SymbolID
	to       SymbolID
	file     string
	line     int
	col      int
}

// distinct returns the references of every configuration with the duplicates a
// matrix produces removed.
//
// One reference written at one position in a file several configurations compile
// is seen once per configuration, and it is one reference to a kind that reads
// how many references a declaration carries, so the totals the sweep reports
// count it once. Two references at one position in two configurations are the
// same reference; two in one configuration are two, so a configuration's own
// reference set comes through unchanged and a one-configuration matrix counts
// exactly what a single load counts.
func distinct(refs []Reference) []Reference {
	first := make(map[referenceSite]int, len(refs))
	kept := make([]Reference, 0, len(refs))
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
		if config, held := first[site]; held && config != r.Config {
			continue
		} else if !held {
			first[site] = r.Config
		}
		kept = append(kept, *r)
	}
	return kept
}

// Sweep answers which symbols of the matrix are dead in every configuration they
// exist in, which relation found each, which dead component each belongs to, and
// which exemptions and which marks held a symbol back.
//
// One sweep runs per configuration, over that configuration's own graph and under
// the input given, and the answers are combined. A symbol is a candidate when
// every configuration it exists in judged it one: a configuration it does not
// exist in says nothing about it, and one reference under one configuration is a
// reference, so a platform guard the other platform uses is not reported. The
// candidate names the configurations it exists in, which are the ones the finding
// holds under.
//
// The input is the run's, not one configuration's: a suppression names a
// declaration whatever platform compiles it, and SweepInput.Exempt is the union of
// what the exemption classes computed under each configuration, because an
// exemption is evidence of a use the analysis cannot see and a use under one
// configuration is a use. A duplicate, being one symbol, class and detail computed
// under more than one configuration, is one retained record, at the site of the
// first entry the input lists.
//
// A consumer's reference is a reference of the configuration it was seen in, so a
// consumer that compiles its call on one platform alone holds the symbol live
// there, and the intersection is what decides whether the symbol is reported at
// all.
//
// The candidate set intersects and the two held-back records unite, and that is
// not an inconsistency: a symbol is reported only where every configuration agrees
// it is dead, while an exemption or a mark that held a symbol back under any
// configuration is in effect, because a suppression needed on one platform is not
// stale.
func (x *Matrix) Sweep(in SweepInput) Result {
	per := make([]Result, len(x.per))
	held := make([]map[SymbolID]Candidate, len(x.per))
	r := Result{LiveUnder: make(map[SymbolID]RelationSet)}
	for config, g := range x.per {
		per[config] = g.Sweep(in)
		held[config] = make(map[SymbolID]Candidate, len(per[config].Candidates))
		for _, c := range per[config].Candidates {
			held[config][c.ID] = c
		}
		// A symbol live under one configuration and dead under another is live
		// under the union of the two answers, which is what makes the record of
		// why a symbol is live the record of every configuration.
		for id, set := range per[config].LiveUnder {
			r.LiveUnder[id] |= set
		}
	}

	dead := make([]bool, len(x.merged.Symbols))
	testOfDeadCode := make([]bool, len(x.merged.Symbols))
	for i := range x.merged.Symbols {
		c, candidate := x.intersect(&x.merged.Symbols[i], held, in.Mode)
		if !candidate {
			continue
		}
		dead[i] = true
		testOfDeadCode[i] = c.TestOfDeadCode
		r.Candidates = append(r.Candidates, c)
	}

	r.Components = x.union.componentsOf(dead, testOfDeadCode)
	r.Retained = x.retained(in.Exempt, per)
	r.Suppressed = x.suppressed(per)
	return r
}

// intersect answers whether one declaration is dead in every configuration of the
// matrix it exists in, and what a report carries for it.
//
// A declaration that exists in no configuration of the matrix is not analyzed at
// all rather than dead in every configuration it exists in, which of an empty set
// of configurations would be vacuously true.
//
// The stronger of the two relations needs every configuration to agree, because a
// reference under any configuration is a reference: reference counting is
// recorded when no configuration held one, and reachability otherwise. The counts
// are the matrix's totals, one per reference rather than one per configuration
// that saw it, so a kind reading them reads how many references the declaration
// carries.
func (x *Matrix) intersect(s *Symbol, held []map[SymbolID]Candidate, m Mode) (Candidate, bool) {
	c := Candidate{ID: s.ID, Relation: ReferenceCounting, Configs: 0, TestOfDeadCode: true}
	for config := range x.merged.Configurations {
		if !s.Configs.Has(config) {
			continue
		}
		found, candidate := held[config][s.ID]
		if !candidate {
			return Candidate{}, false
		}
		c.Configs = c.Configs.with(config)
		if found.Relation != ReferenceCounting {
			c.Relation = Reachability
		}
		if !found.TestOfDeadCode {
			c.TestOfDeadCode = false
		}
	}
	if c.Configs == 0 {
		return Candidate{}, false
	}
	c.ProductionRefs, c.TestRefs = x.union.counted(x.union.at(s.ID), m)
	return c, true
}

// UnmatchedEverywhere returns the configured strings no configuration of the matrix
// matched, in the order the first configuration that reported each lists them.
//
// A string one configuration matched names something, so the answer is the
// intersection and not the union: a pattern naming a symbol one platform declares
// is not a pattern that names nothing, and a matrix of one configuration answers
// that configuration's own set. This is the same rule the candidate intersection
// applies, which is why it is answered here rather than by the caller that reads
// both.
func (x *Matrix) UnmatchedEverywhere() []Unmatched {
	if x.merged.Configurations == 0 {
		return nil
	}
	count := make(map[string]int, len(x.merged.Unmatched))
	for i := range x.merged.Unmatched {
		count[x.merged.Unmatched[i].Source]++
	}
	everywhere := make([]Unmatched, 0, len(count))
	held := make(map[string]bool, len(count))
	for i := range x.merged.Unmatched {
		source := x.merged.Unmatched[i].Source
		if held[source] || count[source] != x.merged.Configurations {
			continue
		}
		held[source] = true
		everywhere = append(everywhere, Unmatched{Source: source})
	}
	return everywhere
}

// retained lists the exemptions that held a symbol back under any configuration,
// one record per symbol, class and detail whatever the number of configurations
// that retained it, at the site of the first entry the mode listed.
//
// The order is the order the merged inventory holds the symbols the exemptions
// name, and for one symbol the order the mode gave them, which is the order one
// configuration's sweep answers in.
func (x *Matrix) retained(exempt []Exemption, per []Result) []Exemption {
	if len(exempt) == 0 {
		return nil
	}
	retained := make(map[exemptionKey]bool)
	for _, r := range per {
		for i := range r.Retained {
			retained[keyOf(&r.Retained[i])] = true
		}
	}
	if len(retained) == 0 {
		return nil
	}

	held := make(map[SymbolID][]Exemption)
	kept := make(map[exemptionKey]bool, len(retained))
	for i := range exempt {
		key := keyOf(&exempt[i])
		if !retained[key] || kept[key] {
			continue
		}
		kept[key] = true
		held[exempt[i].ID] = append(held[exempt[i].ID], exempt[i])
	}
	found := make([]Exemption, 0, len(kept))
	for i := range x.merged.Symbols {
		found = append(found, held[x.merged.Symbols[i].ID]...)
	}
	return found
}

// suppressed lists the marks that held a symbol back under any configuration, each
// once, in the order the merged inventory holds the symbols they name.
//
// The answer is the union and not the intersection, which is the same rule the
// retained record follows: a mark that holds a symbol back on one platform is in
// effect, and withdrawing it would report the symbol there.
func (x *Matrix) suppressed(per []Result) []SymbolID {
	held := make(map[SymbolID]bool)
	for i := range per {
		for _, id := range per[i].Suppressed {
			held[id] = true
		}
	}
	if len(held) == 0 {
		return nil
	}
	found := make([]SymbolID, 0, len(held))
	for i := range x.merged.Symbols {
		if id := x.merged.Symbols[i].ID; held[id] {
			found = append(found, id)
			delete(held, id)
		}
	}
	return found
}

// exemptionKey is what makes two exemptions of a matrix one record: the symbol,
// the class and the detail, which is the key the exemption classes themselves
// deduplicate one configuration's records on.
type exemptionKey struct {
	id     SymbolID
	class  string
	detail string
}

// keyOf keys one exemption.
func keyOf(e *Exemption) exemptionKey {
	return exemptionKey{id: e.ID, class: e.Class, detail: e.Detail}
}
