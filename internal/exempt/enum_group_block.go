package exempt

import (
	"cmp"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// egConst is one constant the target declares, blank ones included, read from the
// first package variant that type-checks its block.
type egConst struct {
	obj   *types.Const // nil where the type checker defines no object
	info  *types.Info
	spec  *egSpec
	expr  ast.Expr           // the expression it states or repeats, nil where it has none
	enum  *egEnum            // nil where it is no member of an enumerated type
	id    inventory.SymbolID // empty for a blank constant
	name  string
	at    token.Pos
	blank bool
	local bool // a function body declares it, so it is no symbol of the inventory

	exported bool // an exported constant of an importable package of a library
	nonGo    bool // its package holds a source in another language than Go
}

// egSpec is one specification of a constant block.
type egSpec struct {
	source *egSpec // the specification whose list it states or repeats
	block  []*egSpec
	consts []*egConst
	values []ast.Expr // the list it states or repeats
	index  int

	// unsplittable is rule D: removing one of its names alone breaks a list a
	// specification repeats or leaves a later one with nothing to repeat.
	unsplittable bool
}

// readBlocks reads every constant block of the target, at package level and in
// function bodies, once whichever package variants compile it.
func (s *enumGroupScan) readBlocks() {
	seen := make(map[inventory.Offset]bool)
	for _, p := range s.in.Facts.Packages() {
		if p.TypesInfo == nil {
			continue
		}
		nonGo := s.in.heldByNonGo(p)
		for _, f := range s.in.Facts.Files(p) {
			ast.Inspect(f, func(n ast.Node) bool {
				block, ok := n.(*ast.GenDecl)
				if !ok || block.Tok != token.CONST {
					return true
				}
				if at := s.in.offsetOf(block.Pos()); !seen[at] {
					seen[at] = true
					s.readBlock(p, block, nonGo)
				}
				return false
			})
		}
	}
	slices.SortStableFunc(s.consts, s.byPosition)
}

// byPosition orders two constants by the file and byte they are declared at.
func (s *enumGroupScan) byPosition(a, b *egConst) int {
	x, y := s.in.offsetOf(a.pos()), s.in.offsetOf(b.pos())
	return cmp.Or(scope.Order(x.File, y.File), cmp.Compare(x.At, y.At))
}

// pos is where a constant is declared.
func (c *egConst) pos() token.Pos { return c.at }

// readBlock reads one block's specifications and constants.
func (s *enumGroupScan) readBlock(p *packages.Package, block *ast.GenDecl, nonGo bool) {
	var specs []*egSpec
	var source *egSpec
	for i, raw := range block.Specs {
		value, ok := raw.(*ast.ValueSpec)
		if !ok {
			continue
		}
		spec := &egSpec{index: i}
		if len(value.Values) > 0 || source == nil {
			source = spec
		}
		spec.source = source
		spec.values = source.values
		if source == spec {
			spec.values = value.Values
		}
		for j, name := range value.Names {
			s.readConst(p, spec, name, j, nonGo)
		}
		specs = append(specs, spec)
	}
	for i, spec := range specs {
		spec.block = specs
		repeated := i+1 < len(specs) && specs[i+1].source == spec
		several := len(spec.consts) > 1
		spec.unsplittable = several && (repeated || spec.source != spec) || i == 0 && repeated
		s.specs = append(s.specs, spec)
	}
}

// readConst reads the constant the j-th name of a specification declares.
func (s *enumGroupScan) readConst(p *packages.Package, spec *egSpec, name *ast.Ident, j int, nonGo bool) {
	info := p.TypesInfo
	c := &egConst{name: name.Name, info: info, spec: spec, at: name.Pos(), blank: name.Name == "_", nonGo: nonGo}
	c.obj, _ = info.Defs[name].(*types.Const)
	if j < len(spec.values) {
		c.expr = spec.values[j]
	}
	if !c.blank && c.obj != nil {
		packageLevel := c.obj.Parent() == c.obj.Pkg().Scope()
		c.local = !packageLevel
		c.id, _ = s.in.Resolve.Object(c.obj)
		c.enum = s.enumOf(c.obj.Type())
		c.exported = c.obj.Exported() && packageLevel && s.published(p) &&
			!s.in.Resolve.Positions().InTestFile(c.obj.Pos())
	}
	spec.consts = append(spec.consts, c)
	if c.obj == nil && !c.blank {
		return
	}
	s.consts = append(s.consts, c)
	if c.id != "" {
		s.byID[c.id] = c
	}
	if c.local {
		s.locals[s.in.offsetOf(c.at)] = c
	}
}

// enumOf is the enumerated type t is, or nil.
func (s *enumGroupScan) enumOf(t types.Type) *egEnum {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return nil
	}
	return s.enums.byKey[s.holds.key(named)]
}

// constOf is the constant of the target an identifier or selector names, or nil.
func (s *enumGroupScan) constOf(info *types.Info, e ast.Expr) *egConst {
	c, ok := resolveObject(info, e).(*types.Const)
	if !ok || c.Pkg() == nil {
		return nil
	}
	if local := s.locals[s.in.offsetOf(c.Pos())]; local != nil {
		return local
	}
	id, held := s.in.Resolve.Object(c)
	if !held {
		return nil
	}
	return s.byID[id]
}

// egMovers is rule Moved over one round: the removable specifications, and for
// each constant the set R of those whose removal moves it.
type egMovers struct {
	r         map[*egConst]egBits
	scan      *enumGroupScan
	removable egIndex
}

// egIndex numbers the removable specifications of a round.
type egIndex struct {
	at    map[*egSpec]int
	specs []*egSpec
}

// members is the specifications a set holds.
func (x *egIndex) members(b egBits) []*egSpec {
	var found []*egSpec
	for i, spec := range x.specs {
		if b.has(i) {
			found = append(found, spec)
		}
	}
	return found
}

// movers computes the removable specifications and every R of one round.
func (s *enumGroupScan) movers(candidate map[*egConst]bool) *egMovers {
	m := &egMovers{removable: egIndex{at: make(map[*egSpec]int)}, r: make(map[*egConst]egBits), scan: s}
	for _, spec := range s.specs {
		removable := len(spec.consts) > 0
		for _, c := range spec.consts {
			removable = removable && candidate[c]
		}
		if removable {
			m.removable.at[spec] = len(m.removable.specs)
			m.removable.specs = append(m.removable.specs, spec)
		}
	}
	return m
}

// of is R(c): the removable specifications whose removal moves c, closed over the
// constants c's expression names.
func (m *egMovers) of(c *egConst) egBits {
	if held, done := m.r[c]; done {
		return held
	}
	m.r[c] = nil // a cycle go/types would reject moves nothing further
	var found egBits
	spec := c.spec
	if at, removable := m.removable.at[spec.source]; removable && spec.source != spec {
		found = found.with(at)
	}
	if c.expr != nil {
		if exprNamesIota(spec.source.info(), c.expr) {
			found = found.or(m.before(spec))
		}
		for _, d := range m.scan.namedConsts(spec.source.info(), c.expr) {
			found = found.or(m.of(d))
		}
	}
	m.r[c] = found
	return found
}

// before is the removable specifications of a block ahead of one of them: those
// whose removal changes the iota of its expressions.
func (m *egMovers) before(spec *egSpec) egBits {
	var found egBits
	for _, earlier := range spec.block {
		if earlier.index >= spec.index {
			break
		}
		if at, removable := m.removable.at[earlier]; removable {
			found = found.with(at)
		}
	}
	return found
}

// info is the type information of the variant the specification was read from.
func (spec *egSpec) info() *types.Info { return spec.consts[0].info }

// namedConsts is every constant of the target an expression names.
func (s *enumGroupScan) namedConsts(info *types.Info, e ast.Expr) []*egConst {
	var found []*egConst
	ast.Inspect(e, func(n ast.Node) bool {
		var name ast.Expr
		switch n := n.(type) {
		case *ast.Ident:
			name = n
		case *ast.SelectorExpr:
			name = n.Sel
		default:
			return true
		}
		if d := s.constOf(info, name); d != nil && !slices.Contains(found, d) {
			found = append(found, d)
		}
		_, selector := n.(*ast.SelectorExpr)
		return !selector
	})
	return found
}

// stillBuilds is rule W for a constant with a nonempty R: it moves one step, its
// value is a fixed integer, and lowered once per specification that moves it the
// value stays no less than its type's least value on every target.
func (s *enumGroupScan) stillBuilds(c *egConst, m *egMovers) bool {
	if c.obj == nil || !s.oneStep(c, m, 0) || !s.fixedExpr(c.spec.source.info(), c.expr, map[*egConst]bool{}) {
		return false
	}
	v := constant.ToInt(c.obj.Val())
	if v.Kind() != constant.Int {
		return false
	}
	basic, ok := c.obj.Type().Underlying().(*types.Basic)
	if !ok || basic.Info()&types.IsInteger == 0 {
		return false
	}
	if basic.Info()&types.IsUntyped != 0 {
		return true
	}
	lowered := constant.BinaryOp(v, token.SUB, constant.MakeInt64(int64(m.of(c).count())))
	return !constant.Compare(lowered, token.LSS, leastValue(basic))
}

// leastValue is the least value of an integer type on every target, int, uint and
// uintptr taken at 32 bits.
func leastValue(basic *types.Basic) constant.Value {
	switch basic.Kind() {
	case types.Int8:
		return constant.MakeInt64(math.MinInt8)
	case types.Int16:
		return constant.MakeInt64(math.MinInt16)
	case types.Int32, types.Int:
		return constant.MakeInt64(math.MinInt32)
	case types.Int64:
		return constant.MakeInt64(math.MinInt64)
	}
	return constant.MakeInt64(0)
}

// oneStep is rule M1: c's expression, stated or repeated from a specification
// that is not removable, is T(U) or U with U ::= iota | (U) | U + K | K + U | U - K,
// or the plain name of a constant that moves one step.
func (s *enumGroupScan) oneStep(c *egConst, m *egMovers, depth int) bool {
	if c.expr == nil || depth > len(s.consts) {
		return false
	}
	if _, removable := m.removable.at[c.spec.source]; removable && c.spec.source != c.spec {
		return false
	}
	info := c.spec.source.info()
	if d := s.plainName(info, c.expr, c.obj.Type()); d != nil {
		return d.obj != nil && s.oneStep(d, m, depth+1)
	}
	u := ast.Unparen(c.expr)
	if call, ok := u.(*ast.CallExpr); ok && len(call.Args) == 1 {
		fun, typed := info.Types[call.Fun]
		if !typed || !fun.IsType() || !types.Identical(fun.Type, c.obj.Type()) {
			return false
		}
		u = call.Args[0]
	}
	return s.unitStep(info, u, m)
}

// plainName is the constant of the target an expression names plainly, in
// parentheses or converted to t or not, and nil for any other expression.
func (s *enumGroupScan) plainName(info *types.Info, e ast.Expr, t types.Type) *egConst {
	e = ast.Unparen(e)
	if call, ok := e.(*ast.CallExpr); ok && len(call.Args) == 1 {
		fun, typed := info.Types[call.Fun]
		if !typed || !fun.IsType() || !types.Identical(fun.Type, t) {
			return nil
		}
		e = ast.Unparen(call.Args[0])
	}
	switch e.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return s.constOf(info, e)
	}
	return nil
}

// unitStep is U of rule M1.
func (s *enumGroupScan) unitStep(info *types.Info, e ast.Expr, m *egMovers) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return isIota(info, e)
	case *ast.ParenExpr:
		return s.unitStep(info, e.X, m)
	case *ast.BinaryExpr:
		switch e.Op {
		case token.ADD:
			return s.unitStep(info, e.X, m) && s.offset(info, e.Y, m) || s.offset(info, e.X, m) && s.unitStep(info, e.Y, m)
		case token.SUB:
			return s.unitStep(info, e.X, m) && s.offset(info, e.Y, m)
		}
	}
	return false
}

// offset is K of rule M1: an untyped constant expression naming neither iota nor
// a constant with a nonempty R.
func (s *enumGroupScan) offset(info *types.Info, e ast.Expr, m *egMovers) bool {
	tv, typed := info.Types[e]
	if !typed || tv.Value == nil {
		return false
	}
	basic, ok := tv.Type.(*types.Basic)
	if !ok || basic.Info()&types.IsUntyped == 0 || exprNamesIota(info, e) {
		return false
	}
	for _, d := range s.namedConsts(info, e) {
		if !m.of(d).empty() {
			return false
		}
	}
	return true
}

// fixedName is rule F for a constant's name: it is declared in a file of the
// target no build constraint selects, and its expression has a fixed value.
func (s *enumGroupScan) fixedName(c *egConst) bool {
	switch s.fixed[c] {
	case 1:
		return true
	case -1:
		return false
	}
	s.fixed[c] = -1
	held := c.obj != nil && c.expr != nil && !s.constrainedFile(c.obj.Pos()) &&
		s.fixedExpr(c.spec.source.info(), c.expr, map[*egConst]bool{c: true})
	if held {
		s.fixed[c] = 1
	}
	return held
}

// fixedExpr is rule F for an expression: it names only literals, iota and
// constants of the target with a fixed value declared in files no build
// constraint selects, holds no call but a conversion, min or max, no function of
// unsafe and no ^ on a uint or uintptr operand.
func (s *enumGroupScan) fixedExpr(info *types.Info, e ast.Expr, visiting map[*egConst]bool) bool {
	if e == nil {
		return false
	}
	held := true
	ast.Inspect(e, func(n ast.Node) bool {
		if !held {
			return false
		}
		var descend bool
		held, descend = s.fixedNode(info, n, visiting)
		return held && descend
	})
	return held
}

// fixedNode is rule F for one node of an expression, and whether the walk goes
// on into its operands.
func (s *enumGroupScan) fixedNode(info *types.Info, n ast.Node, visiting map[*egConst]bool) (held, descend bool) {
	switch n := n.(type) {
	case *ast.Ident:
		return s.fixedIdent(info, n, visiting), true
	case *ast.SelectorExpr:
		return s.fixedIdent(info, n.Sel, visiting), false
	case *ast.CallExpr:
		if fun, typed := info.Types[n.Fun]; typed && fun.IsType() {
			return true, true
		}
		b, isBuiltin := info.Uses[calledIdent(n.Fun)].(*types.Builtin)
		return isBuiltin && (b.Name() == builtinMin || b.Name() == builtinMax), true
	case *ast.UnaryExpr:
		if n.Op != token.XOR {
			return true, true
		}
		basic, ok := info.TypeOf(n.X).Underlying().(*types.Basic)
		return !ok || basic.Kind() != types.Uint && basic.Kind() != types.Uintptr, true
	}
	return true, true
}

// fixedIdent is rule F for one name an expression holds.
func (s *enumGroupScan) fixedIdent(info *types.Info, id *ast.Ident, visiting map[*egConst]bool) bool {
	switch obj := info.Uses[id].(type) {
	case nil, *types.TypeName:
		return true
	case *types.Const:
		if obj.Pkg() == nil {
			return true
		}
		d := s.constOf(info, id)
		if d == nil || visiting[d] {
			return false
		}
		visiting[d] = true
		return s.fixedName(d)
	case *types.Builtin:
		return obj.Name() == builtinMin || obj.Name() == builtinMax
	}
	return false
}

// constrainedFile reports whether a build constraint selects the file writing pos:
// a //go:build line or legacy // +build lines, a file-name suffix go/build
// matches, or an import of "C". A file the run cannot read is constrained.
func (s *enumGroupScan) constrainedFile(pos token.Pos) bool {
	name := s.in.Resolve.Positions().Offset(pos).File
	if held, decided := s.constrained[name]; decided {
		return held
	}
	held := true
	if src, err := s.in.Read(name); err == nil {
		expr, err := buildconstraint.Of(filepath.Base(name), src)
		held = err != nil || expr != nil || s.in.nonGo().importsC(name)
	}
	s.constrained[name] = held
	return held
}

// The builtins that pick one of their operands.
const (
	builtinMin    = "min"
	builtinMax    = "max"
	builtinAppend = "append"
)
