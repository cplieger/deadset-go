package liveness

import (
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Carrier is what the carrier sweep of a facts document reads besides the graph
// (grammar/analysis/bindings.md bindings.11 to bindings.14).
type Carrier struct {
	// Generated holds the path of every generated file, as a symbol's position
	// names it.
	Generated map[string]bool

	// Implementers holds every method of a generated server or handler
	// interface, each with the hand-written methods answering it.
	Implementers map[inventory.SymbolID][]inventory.SymbolID

	// Registrations holds every generated registration function.
	Registrations map[inventory.SymbolID]bool

	// Oneof holds every declaration of a oneof member, with the part of the
	// member it is.
	Oneof map[inventory.SymbolID]facts.OneofPart

	// Exempt is the production sweep's exemptions.
	Exempt []exempt.Exemption

	// Reads is every read a hand-written serializer call, enum name conversion or
	// oneof read telling no member apart makes (bindings.14, bindings.15).
	Reads []facts.SchemaRead
}

// Carriage is one carrier sweep: the edges it follows and what hand-written code
// reaches through them.
type Carriage struct {
	g       *Graph
	servers map[int]bool // the positions of the generated server and handler interfaces
	out     [][]edge
	hand    Reach
}

// Reach is the set of declarations one carrier search reaches.
type Reach struct {
	g       *Graph
	reached []bool
}

// Holds reports whether the search reaches the declaration.
func (r Reach) Holds(id inventory.SymbolID) bool {
	at := r.g.at(id)
	return at != outside && r.reached[at]
}

// Carry runs the carrier sweep over the production references (bindings.11 to
// bindings.14). No declaration of a generated file seeds it, an exemption counts
// only where its evidence site is hand-written and it satisfies no generated
// server or handler interface, no reference of a registration function is
// followed, cut owns which other edges are not, and a hand-written selection of a
// served method reaches every method answering it.
func (g *Graph) Carry(c *Carrier) *Carriage {
	servers := make(map[int]bool)
	for method := range c.Implementers {
		if at := g.at(method); at != outside {
			for _, p := range g.parents[at] {
				servers[p] = true
			}
		}
	}
	probe := &Carriage{g: g, servers: servers}
	var kept []exempt.Exemption
	for ix := range c.Exempt {
		if e := &c.Exempt[ix]; (e.Build != load.TargetBuild || !c.Generated[e.File]) && !probe.Answers(e) {
			kept = append(kept, *e)
		}
	}
	d := g.using(&SweepPolicy{Exempt: kept})
	k := &Carriage{g: d, servers: servers, out: make([][]edge, len(d.symbols))}
	k.follow(c)
	k.dispatch(c)
	k.serialize(c)
	k.hand = k.closed(k.seeds(c, d.exempted(kept)))
	return k
}

// follow keeps every edge of the graph the carrier sweep does not cut.
func (k *Carriage) follow(c *Carrier) {
	for i := range k.g.out {
		if c.Registrations[k.g.symbols[i].ID] {
			continue
		}
		generated := c.Generated[k.g.symbols[i].File]
		for _, e := range k.g.out[i] {
			if !k.cut(i, e, generated, c) {
				k.out[i] = append(k.out[i], e)
			}
		}
	}
}

// dispatch adds a use of each method answering a served method by that method,
// so a hand-written selection of it reaches every implementer.
func (k *Carriage) dispatch(c *Carrier) {
	for method, answering := range c.Implementers {
		from := k.g.at(method)
		if from == outside {
			continue
		}
		for _, id := range answering {
			if to := k.g.at(id); to != outside {
				k.out[from] = append(k.out[from], edge{to: to})
			}
		}
	}
}

// serialize adds each read a serializer call or an enum name conversion of a
// production declaration makes; a loaded consumer's read seeds the sweep.
func (k *Carriage) serialize(c *Carrier) {
	for _, r := range c.Reads {
		from, to := k.g.at(r.From), k.g.at(r.To)
		if r.From != "" && from != outside && to != outside && !k.g.symbols[from].InTestFile {
			k.out[from] = append(k.out[from], edge{to: to})
		}
	}
}

// seeds marks what seeds the carrier sweep: every root outside the generated files
// but a test, a yielded root or a root of a generated file, every symbol a held
// exemption names, and every symbol a loaded consumer's production code references
// or reads through a serializer.
func (k *Carriage) seeds(c *Carrier, held []bool) (reached []bool, queue []int) {
	reached = make([]bool, len(k.g.symbols))
	seed := func(at int) {
		if !reached[at] {
			reached[at] = true
			queue = append(queue, at)
		}
	}
	for _, r := range k.g.rooted {
		if r.root.Kind != roots.RootTest && !r.yielded && !c.Generated[k.g.symbols[r.at].File] {
			seed(r.at)
		}
	}
	production := facts.Mode{Production: true}
	for i := range k.g.symbols {
		if held[i] || k.g.consumedIn(i, production) {
			seed(i)
		}
	}
	for _, r := range c.Reads {
		if at := k.g.at(r.To); r.From == "" && at != outside {
			seed(at)
		}
	}
	return reached, queue
}

// Answers reports whether one exemption is a satisfaction of a generated server or
// handler interface, which the carrier sweep does not follow.
func (k *Carriage) Answers(e *exempt.Exemption) bool {
	if e.Class != string(vocab.InterfaceSatisfaction) || e.Via == "" {
		return false
	}
	at := k.g.at(e.Via)
	return at != outside && slices.ContainsFunc(k.g.parents[at], func(p int) bool { return k.servers[p] })
}

// cut reports whether the carrier sweep leaves one edge of the declaration at i
// unfollowed: a test edge, a write (bindings.14), a generated declaration's
// memory-layout use (the runtime's reflection, bindings.12) or selection of a
// served method, a conversion or blank declaration naming a server interface
// (bindings.9, bindings.13), a generated reference to a oneof member, and a
// hand-written one to a wrapper type that is no assertion (references-go.15).
func (k *Carriage) cut(i int, e edge, generated bool, c *Carrier) bool {
	if e.test {
		return true
	}
	if _, served := c.Implementers[k.g.symbols[e.to].ID]; served && generated {
		return true
	}
	r := k.g.hops[e.hop].Reference
	if part := c.Oneof[k.g.symbols[e.to].ID]; part != facts.NotOneof && (generated || (part == facts.OneofWrapper && r != nil && r.Kind != facts.RefAssert)) {
		return true
	}
	if r == nil {
		return false
	}
	return r.Kind == facts.RefWrite || (generated && r.Layout()) || (k.servers[e.to] && (r.Kind == facts.RefConversion || k.g.symbols[i].Blank))
}

// closed is the reach of the queue's declarations over the carrier sweep's edges.
func (k *Carriage) closed(reached []bool, queue []int) Reach {
	closure(k.out, k.g.gates, reached, queue, func(edge) bool { return false }, nil)
	return Reach{g: k.g, reached: reached}
}

// Hand is what hand-written code reaches.
func (k *Carriage) Hand() Reach { return k.hand }

// Search is what the carrier sweep reaches from the declarations alone, the
// search of an implementer's method bindings.16 and bindings.17 read.
func (k *Carriage) Search(from ...inventory.SymbolID) Reach {
	reached := make([]bool, len(k.g.symbols))
	var queue []int
	for _, id := range from {
		if at := k.g.at(id); at != outside && !reached[at] {
			reached[at] = true
			queue = append(queue, at)
		}
	}
	return k.closed(reached, queue)
}
