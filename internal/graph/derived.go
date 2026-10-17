package graph

import (
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

// The bounds of a chain search and of the sites one assertion derives. Past
// maxRoutes or maxDepth a search answers one route with no site of its own, and
// past maxDerived a type and an asserted interface keep one further site held by
// the assertion alone: each drops holders it could not enumerate, which retains
// more and never less.
const (
	maxRoutes  = 32
	maxDepth   = 8
	maxDerived = 16
)

// Chains answers by which chains of the sites of one set a value of one interface
// type reaches another: each site of a chain converts a value of the interface type
// the previous site converts to, the first a value of the type the search starts
// from. Interfaces are compared by method set.
type Chains struct {
	hops   map[string][]int // by the method set of the interface a site's value has
	routes map[string][][]int
	keys   map[*types.Interface]string // methodSetKey's answers, which spell every signature
	sites  []Conversion
}

// NewChains indexes the sites of one set that convert an interface value. holder
// names the declaration holding the site at an index: two sites one declaration
// holds between the same two interfaces are one hop.
func NewChains(sites []Conversion, holder func(i int) string) *Chains {
	c := &Chains{sites: sites, hops: make(map[string][]int), routes: make(map[string][][]int), keys: make(map[*types.Interface]string)}
	seen := make(map[string]bool)
	for i := range sites {
		from := interfaceOf(sites[i].From)
		if from == nil || sites[i].To == nil {
			continue
		}
		key := c.key(from)
		hop := key + "\x00" + c.key(sites[i].To) + "\x00" + holder(i)
		if !seen[hop] {
			seen[hop] = true
			c.hops[key] = append(c.hops[key], i)
		}
	}
	return c
}

// Routes is every chain from one interface to another, each the positions of its
// sites in order, one empty route where the two have one method set, and none where
// no chain leads there.
func (c *Chains) Routes(from, to *types.Interface) [][]token.Pos {
	indices := c.indexRoutes(from, to)
	routes := make([][]token.Pos, len(indices))
	for i, route := range indices {
		routes[i] = make([]token.Pos, len(route))
		for j, at := range route {
			routes[i][j] = c.sites[at].Site
		}
	}
	return routes
}

// indexRoutes is [Chains.Routes] as indices of the set, memoized per pair.
func (c *Chains) indexRoutes(from, to *types.Interface) [][]int {
	start, goal := c.key(from), c.key(to)
	pair := start + "\x00\x00" + goal
	if held, known := c.routes[pair]; known {
		return held
	}
	var found [][]int
	if start == goal {
		found = [][]int{{}}
	} else {
		search := chainSearch{chains: c, goal: goal, onPath: map[string]bool{start: true}}
		search.from(start, nil)
		found = search.found
		if search.capped {
			found = [][]int{{}}
		}
	}
	c.routes[pair] = found
	return found
}

// chainSearch is one depth-first walk over simple chains toward one interface.
type chainSearch struct {
	chains *Chains
	onPath map[string]bool
	goal   string
	found  [][]int
	capped bool
}

// from extends the chain path, whose value has the interface keyed at, by every
// hop out of it.
func (s *chainSearch) from(at string, path []int) {
	for _, hop := range s.chains.hops[at] {
		if s.capped {
			return
		}
		next := s.chains.key(s.chains.sites[hop].To)
		extended := append(slices.Clip(path), hop)
		switch {
		case next == s.goal:
			s.found = append(s.found, extended)
			s.capped = len(s.found) > maxRoutes
		case s.onPath[next]:
		case len(extended) >= maxDepth:
			s.capped = true
		default:
			s.onPath[next] = true
			s.from(next, extended)
			delete(s.onPath, next)
		}
	}
}

// key is [methodSetKey] of iface, computed once per interface.
func (c *Chains) key(iface *types.Interface) string {
	if held, known := c.keys[iface]; known {
		return held
	}
	spelled := methodSetKey(iface)
	c.keys[iface] = spelled
	return spelled
}

// methodSetKey spells an interface's method set, so two interfaces share a key
// exactly when their method sets are identical, however each is written.
func methodSetKey(iface *types.Interface) string {
	methods := make([]string, 0, iface.NumMethods())
	for m := range iface.Methods() {
		name := m.Name()
		if !m.Exported() && m.Pkg() != nil {
			name = m.Pkg().Path() + "." + name
		}
		methods = append(methods, name+" "+identity(m.Signature()))
	}
	slices.Sort(methods)
	return strings.Join(methods, ";")
}

// derivable is one site of the set as an assertion reads it: a concrete type, the
// interface its value reaches, and the holders whose declarations hold it, each
// declaration once, with the position of a site of each.
type derivable struct {
	from  types.Type
	to    *types.Interface
	key   string      // the identity of from
	held  []token.Pos // one position per declaration of decls, in decls' order
	decls []token.Pos
}

// joined is e reached at assertion a through one chain of hops: every declaration
// holding e, a or a hop holds the derived site.
func (s *conversionScan) joined(e *derivable, a *assertion, hops []int) derivable {
	d := derivable{
		from:  e.from,
		key:   e.key,
		to:    interfaceOf(a.to),
		held:  slices.Clone(e.held),
		decls: slices.Clone(e.decls),
	}
	d.add(a.holder, a.site)
	for _, hop := range hops {
		d.add(s.holders[hop], s.sites[hop].Site)
	}
	return d
}

// add holds d by one more declaration, at one site it holds.
func (d *derivable) add(decl, site token.Pos) {
	if !slices.Contains(d.decls, decl) {
		d.decls = append(d.decls, decl)
		d.held = append(d.held, site)
	}
}

// declsKey spells the set of declarations holding a derived site.
func (d *derivable) declsKey() string {
	sorted := slices.Clone(d.decls)
	slices.Sort(sorted)
	parts := make([]string, len(sorted))
	for i, decl := range sorted {
		parts[i] = strconv.Itoa(int(decl))
	}
	return strings.Join(parts, ",")
}

// asserted derives the sites the scan's assertions reach: at each assertion of J on
// an operand of interface K, every concrete type T that implements J and whose
// value reaches K, through a site converting it to K or to an interface a chain of
// sites leads from to K, converts to J, held by the assertion's declaration and by
// every declaration holding a site it derives from. A derived site is a site of the
// set, so the derivation runs until no site joins.
func (s *conversionScan) asserted() []Conversion {
	chains := NewChains(s.sites, func(i int) string { return strconv.Itoa(int(s.holders[i])) })
	var queue []derivable
	for i := range s.sites {
		c := &s.sites[i]
		if types.IsInterface(c.From) {
			continue
		}
		queue = append(queue, derivable{
			from:  c.From,
			to:    c.To,
			key:   identity(c.From),
			held:  []token.Pos{c.Site},
			decls: []token.Pos{s.holders[i]},
		})
	}
	targets := make([]*types.Interface, len(s.assertions))
	names := make([]string, len(s.assertions))
	for i := range s.assertions {
		targets[i] = interfaceOf(s.assertions[i].to)
		names[i] = identity(s.assertions[i].to)
	}
	run := derivation{
		scan: s, chains: chains, names: names,
		implements: make(map[implementsPair]bool), seen: make(map[string]bool), counts: make(map[string]int),
	}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		for i := range s.assertions {
			if targets[i].NumMethods() == 0 || !implementsOnce(run.implements, e.key, i, e.from, targets[i]) {
				continue
			}
			queue = append(queue, run.derive(&e, i)...)
		}
	}
	return run.derived
}

// derivation is the state of one fixed point of [conversionScan.asserted].
type derivation struct {
	scan       *conversionScan
	chains     *Chains
	implements map[implementsPair]bool
	seen       map[string]bool // the derived sites kept, by type, interface and holders
	counts     map[string]int  // the derived sites kept, by type and interface
	names      []string
	derived    []Conversion
}

// derive adds the sites one site of the set derives at one assertion, one per chain
// from its interface to the operand's, and returns them.
func (r *derivation) derive(e *derivable, assertion int) []derivable {
	a := &r.scan.assertions[assertion]
	pair := e.key + "\x00" + r.names[assertion]
	var added []derivable
	for _, route := range r.chains.indexRoutes(e.to, a.operand) {
		d := r.scan.joined(e, a, route)
		if r.counts[pair] >= maxDerived {
			d.held, d.decls = []token.Pos{a.site}, []token.Pos{a.holder}
		}
		key := pair + "\x00" + d.declsKey()
		if r.seen[key] || r.counts[pair] > maxDerived {
			continue
		}
		r.seen[key] = true
		r.counts[pair]++
		r.derived = append(r.derived, r.scan.derivedSite(&d, a))
		added = append(added, d)
	}
	return added
}

// implementsPair is one type, by its identity, and one assertion, by its index.
type implementsPair struct {
	key       string
	assertion int
}

// implementsOnce answers types.Implements for one type and one assertion's
// interface, each pair once.
func implementsOnce(held map[implementsPair]bool, key string, assertion int, from types.Type, to *types.Interface) bool {
	pair := implementsPair{key: key, assertion: assertion}
	answer, known := held[pair]
	if !known {
		answer = types.Implements(from, to)
		held[pair] = answer
	}
	return answer
}

// derivedSite is the site one derivation adds. Its position is the first site it
// derives from that a test file holds, because the derived site is then a test's
// evidence, and the assertion's otherwise.
func (s *conversionScan) derivedSite(d *derivable, a *assertion) Conversion {
	site := a.site
	for _, at := range d.held {
		if _, test := inventory.IsTestFile(s.fset.Position(at).Filename); test {
			site = at
			break
		}
	}
	return Conversion{From: d.from, Interface: a.to, To: d.to, Name: spell(a.to), Site: site, Held: slices.Clone(d.held)}
}
