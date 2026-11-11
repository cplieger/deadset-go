package exempt

import (
	"go/types"
	"math/bits"

	"github.com/cplieger/deadset-go/v2/internal/resolve"
)

// egBits is a set of small non-negative integers.
type egBits []uint64

func (b egBits) has(i int) bool { return i/64 < len(b) && b[i/64]&(1<<(i%64)) != 0 }

func (b egBits) empty() bool {
	for _, w := range b {
		if w != 0 {
			return false
		}
	}
	return true
}

// with is b with i added, b itself left as it was.
func (b egBits) with(i int) egBits {
	out := make(egBits, max(len(b), i/64+1))
	copy(out, b)
	out[i/64] |= 1 << (i % 64)
	return out
}

// or is the union of b and o, neither changed.
func (b egBits) or(o egBits) egBits {
	if o.empty() {
		return b
	}
	if b.empty() {
		return o
	}
	out := make(egBits, max(len(b), len(o)))
	copy(out, b)
	for i, w := range o {
		out[i] |= w
	}
	return out
}

func (b egBits) count() int {
	n := 0
	for _, w := range b {
		n += bits.OnesCount64(w)
	}
	return n
}

// minus is how many members of b o does not hold.
func (b egBits) minus(o egBits) int {
	n := 0
	for i, w := range b {
		if i < len(o) {
			w &^= o[i]
		}
		n += bits.OnesCount64(w)
	}
	return n
}

// each calls f with every member of b in increasing order.
func (b egBits) each(f func(int)) {
	for i, w := range b {
		for w != 0 {
			low := bits.TrailingZeros64(w)
			f(i*64 + low)
			w &^= 1 << low
		}
	}
}

// enumHolds is rule H: which enumerated types a type holds, computed over the type
// graph with Tarjan's strongly connected components and stored only for a
// completed component. A defined type is one node whichever reading of its
// package it comes from.
type enumHolds struct {
	rs    *resolve.Resolver
	enums *enumRecognition
	keys  map[*types.Named]string
	done  map[any]egBits
	index map[any]int
	low   map[any]int
	next  map[any][]types.Type
	on    map[any]bool
	stack []any
	count int
}

func newEnumHolds(rs *resolve.Resolver, enums *enumRecognition) *enumHolds {
	return &enumHolds{
		rs: rs, enums: enums, keys: make(map[*types.Named]string), done: make(map[any]egBits),
		index: make(map[any]int), low: make(map[any]int), next: make(map[any][]types.Type), on: make(map[any]bool),
	}
}

// key is a defined type's identity across the variants of the load.
func (h *enumHolds) key(named *types.Named) string {
	if k, held := h.keys[named]; held {
		return k
	}
	k := h.rs.TypeKey(named)
	h.keys[named] = k
	return k
}

// node is the graph node one type is.
func (h *enumHolds) node(t types.Type) any {
	if named, ok := t.(*types.Named); ok {
		return h.key(named)
	}
	return t
}

// of is every enumerated type t holds.
func (h *enumHolds) of(t types.Type) egBits {
	if t == nil || len(h.enums.keys) == 0 {
		return nil
	}
	t = types.Unalias(t)
	k := h.node(t)
	if held, done := h.done[k]; done {
		return held
	}
	h.strong(t, k)
	return h.done[k]
}

// strong is one step of Tarjan's algorithm.
func (h *enumHolds) strong(t types.Type, k any) {
	h.index[k], h.low[k] = h.count, h.count
	h.count++
	h.stack = append(h.stack, k)
	h.on[k] = true
	next := successors(t)
	h.next[k] = next
	for _, s := range next {
		sk := h.node(s)
		if _, done := h.done[sk]; done {
			continue
		}
		if _, visited := h.index[sk]; !visited {
			h.strong(s, sk)
			h.low[k] = min(h.low[k], h.low[sk])
		} else if h.on[sk] {
			h.low[k] = min(h.low[k], h.index[sk])
		}
	}
	if h.low[k] == h.index[k] {
		h.complete(h.pop(k))
	}
}

// pop takes the component k is the root of off the stack.
func (h *enumHolds) pop(k any) []any {
	var component []any
	for {
		top := h.stack[len(h.stack)-1]
		h.stack = h.stack[:len(h.stack)-1]
		h.on[top] = false
		component = append(component, top)
		if top == k {
			return component
		}
	}
}

// complete stores for every member of a completed component what the component
// holds: its own enumerated types and what every type it holds holds.
func (h *enumHolds) complete(component []any) {
	var held egBits
	for _, member := range component {
		if key, isKey := member.(string); isKey {
			if enum := h.enums.byKey[key]; enum != nil {
				held = held.with(enum.index)
			}
		}
		for _, s := range h.next[member] {
			held = held.or(h.done[h.node(s)])
		}
	}
	for _, member := range component {
		h.done[member] = held
		delete(h.next, member)
	}
}

// successors is every type t holds what they hold of: an element, a key, a field,
// a parameter or result, an interface method's signature, and for a defined type
// its underlying type and its methods' signatures; an instance holds what its
// origin and its type arguments hold.
func successors(t types.Type) []types.Type {
	var next []types.Type
	switch t := t.(type) {
	case *types.Named:
		return namedSuccessors(t)
	case *types.Map:
		next = append(next, types.Unalias(t.Key()), types.Unalias(t.Elem()))
	case interface{ Elem() types.Type }: // a pointer, slice, array or channel
		next = append(next, types.Unalias(t.Elem()))
	case *types.Struct:
		for f := range t.Fields() {
			next = append(next, types.Unalias(f.Type()))
		}
	case *types.Signature:
		next = append(next, t.Params(), t.Results())
	case *types.Tuple:
		for v := range t.Variables() {
			next = append(next, types.Unalias(v.Type()))
		}
	case *types.Interface:
		for m := range t.Methods() {
			next = append(next, m.Signature())
		}
	}
	return next
}

// namedSuccessors is what a defined type holds what they hold of: an instance's
// origin and type arguments, and otherwise the underlying type and the methods'
// signatures.
func namedSuccessors(t *types.Named) []types.Type {
	var next []types.Type
	if args := t.TypeArgs(); args.Len() > 0 {
		next = append(next, t.Origin())
		for a := range args.Types() {
			next = append(next, types.Unalias(a))
		}
		return next
	}
	next = append(next, t.Underlying())
	for m := range t.Methods() {
		next = append(next, m.Signature())
	}
	return next
}
