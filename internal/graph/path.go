package graph

import (
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

// Path is a caller-reachability path to a symbol: the root it starts from, or the
// loaded consumer whose reference seeds it, and the references it follows. It never
// starts at a held seed (a mark or an exemption) or crosses an inferred use.
type Path struct {
	// Root is the root the path starts at, nil where a consumer's reference seeds
	// it; that reference is then the first hop.
	Root *Root
	Hops []Reference
}

// Path is the shortest path the mode's caller reachability follows to the symbol,
// and false where it does not reach it: from the sweep's caller seeds (the roots, then
// the symbols loaded consumers reference) over the references the mode follows. It
// does not follow the held seeds (marks and exemptions) or the inferred uses the
// sweep also reads. A symbol that is itself a root is reached by the empty path.
//
// The search is breadth-first from every seed at once, roots first in the merge's
// order, so one tree answers the same path on every run.
func (x *Matrix) Path(to inventory.SymbolID, m Mode) (Path, bool) {
	g := x.union
	target := g.at(to)
	if target == outside {
		return Path{}, false
	}
	steps := make([]walked, 0, len(g.symbols))
	seen := make([]bool, len(g.symbols))
	g.callerSeeds(m, seen, func(at, root int) {
		steps = append(steps, walked{at: at, previous: -1, seed: at, root: root})
	})
	for head := 0; head < len(steps); head++ {
		if steps[head].at == target {
			return x.pathOf(steps, head, m), true
		}
		for _, e := range g.out[steps[head].at] {
			if seen[e.to] || !m.follows(e.test) {
				continue
			}
			seen[e.to] = true
			steps = append(steps, walked{at: e.to, previous: head, seed: steps[head].seed, root: steps[head].root})
		}
	}
	return Path{}, false
}

// walked is one step of [Matrix.Path]'s walk: the symbol reached, the step it was
// reached from, the seed the walk started at, and that seed's place among the roots,
// -1 for a consumer's seed.
type walked struct{ at, previous, seed, root int }

// pathOf is the path the step at head was reached by.
func (x *Matrix) pathOf(steps []walked, head int, m Mode) Path {
	var hops []Reference
	for at := head; steps[at].previous >= 0; at = steps[at].previous {
		hops = append(hops, x.hop(steps[steps[at].previous].at, steps[at].at, m))
	}
	slices.Reverse(hops)
	if steps[head].root < 0 {
		return Path{Hops: slices.Insert(hops, 0, x.consumerHop(steps[head].seed, m))}
	}
	g := x.union
	return Path{Root: &Root{ID: g.symbols[steps[head].seed].ID, Kind: g.rooted[steps[head].root].kind}, Hops: hops}
}

// hop is the reference the walk followed from one symbol to the next: the first the
// merge holds that the mode follows. Every followed edge is built from one, so the
// search always finds it.
func (x *Matrix) hop(from, to int, m Mode) Reference {
	fromID, toID := x.union.symbols[from].ID, x.union.symbols[to].ID
	for i := range x.merged.References {
		r := &x.merged.References[i]
		if r.From == fromID && r.To == toID && m.follows(r.Test) {
			return *r
		}
	}
	return Reference{From: fromID, To: toID}
}

// consumerHop is the first reference a loaded consumer makes to the symbol at at that
// the mode counts, which is what made the symbol a seed, so the search always finds
// it.
func (x *Matrix) consumerHop(at int, m Mode) Reference {
	id := x.union.symbols[at].ID
	for i := range x.merged.References {
		r := &x.merged.References[i]
		if r.To == id && r.Consumer != "" && (!r.Test || m.countsConsumerTests()) {
			return *r
		}
	}
	return Reference{To: id}
}

// TestReference reports whether the mode classifies one reference as a test file's,
// which a loaded consumer's test reference is not where the mode counts it as
// production.
func (m Mode) TestReference(r *Reference) bool {
	return r.Test && (r.Consumer == "" || !m.ConsumerTestsProduction)
}
