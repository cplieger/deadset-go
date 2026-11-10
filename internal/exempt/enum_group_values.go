package exempt

import (
	"go/constant"
	"go/token"
	"strconv"
	"strings"
)

// egPoint is one item of rule M2 in one round: its value, whether that value is
// fixed, the specifications that move it, where code holds it, and how a clause
// names it.
type egPoint struct {
	value constant.Value
	site  *egSite
	label string
	r     egBits
	fixed bool
}

// compare is rule M2 for one enumerated type in one view and round: every pair of
// items keeps its relation after the removal of every subset of the removable
// specifications, and a pair that does not retains every candidate of every
// specification that moves exactly one of the two.
func (s *enumGroupScan) compare(enum *egEnum, r *egRound) {
	points := distinctPoints(s.points(enum, r))
	for i := range points {
		for j := i + 1; j < len(points); j++ {
			p, q := &points[i], &points[j]
			if keepsRelation(p, q) {
				continue
			}
			r.retain(symmetricDifference(p.r, q.r), "deleting it changes how "+p.label+" compares with "+q.label, p.site, q.site)
		}
	}
}

// points is the items rule M2 compares for one enumerated type: the zero value,
// every member the view names, and every constant expression of the type the
// view holds whose value at most its one plain name moves.
func (s *enumGroupScan) points(enum *egEnum, r *egRound) []egPoint {
	points := []egPoint{{value: constant.MakeInt64(0), fixed: true, label: "the zero value"}}
	for _, id := range enum.members {
		c := s.byID[id]
		named, sites := s.isNamed(c, r.full)
		if !named || c.obj == nil {
			continue
		}
		v := constant.ToInt(c.obj.Val())
		points = append(points, egPoint{
			value: v, fixed: v.Kind() == constant.Int && s.fixedName(c),
			r: r.m.of(c), site: earliest(sites, r.full), label: c.name,
		})
	}
	for i := range enum.items {
		it := &enum.items[i]
		if it.site.test && !r.full {
			continue
		}
		if p, compared := itemPoint(it, r); compared {
			points = append(points, p)
		}
	}
	return points
}

// itemPoint is the item one constant expression is, and false where the
// expression names a constant the round moves other than its one plain name,
// which retains the movers of every such constant instead.
func itemPoint(it *egItem, r *egRound) (egPoint, bool) {
	var moved []*egConst
	for _, d := range it.names {
		if !r.m.of(d).empty() {
			moved = append(moved, d)
		}
	}
	var movers egBits
	switch {
	case len(moved) == 0:
	case it.plain != nil && len(moved) == 1 && moved[0] == it.plain:
		movers = r.m.of(it.plain)
	default:
		for _, d := range moved {
			r.retain(r.m.of(d), "deleting it changes the constant expression "+it.label+", which names "+d.name, it.site)
		}
		return egPoint{}, false
	}
	return egPoint{value: it.value, fixed: it.fixed, r: movers, site: it.site, label: it.label}, true
}

// keepsRelation is rule M2 for one pair: with d = q - p, d less the removals that
// move q and not p, and d plus those that move p and not q, both have the sign
// of d. A value that is not fixed keeps its relation only with an item the same
// specifications move.
func keepsRelation(p, q *egPoint) bool {
	onlyQ, onlyP := q.r.minus(p.r), p.r.minus(q.r)
	if !p.fixed || !q.fixed {
		return onlyQ == 0 && onlyP == 0
	}
	d := constant.BinaryOp(q.value, token.SUB, p.value)
	lo := constant.BinaryOp(d, token.SUB, constant.MakeInt64(int64(onlyQ)))
	hi := constant.BinaryOp(d, token.ADD, constant.MakeInt64(int64(onlyP)))
	sign := constant.Sign(d)
	return constant.Sign(lo) == sign && constant.Sign(hi) == sign
}

// symmetricDifference is the members of exactly one of a and b.
func symmetricDifference(a, b egBits) egBits {
	var out egBits
	a.each(func(i int) {
		if !b.has(i) {
			out = out.with(i)
		}
	})
	b.each(func(i int) {
		if !a.has(i) {
			out = out.with(i)
		}
	})
	return out
}

// distinctPoints keeps one item per value, fixedness and movers, the first, so a
// value written at many sites is compared once.
func distinctPoints(points []egPoint) []egPoint {
	seen := make(map[string]bool, len(points))
	kept := points[:0]
	for _, p := range points {
		var key strings.Builder
		key.WriteString(p.value.ExactString())
		key.WriteString(strconv.FormatBool(p.fixed))
		p.r.each(func(i int) { key.WriteString("," + strconv.Itoa(i)) })
		if seen[key.String()] {
			continue
		}
		seen[key.String()] = true
		kept = append(kept, p)
	}
	return kept
}
