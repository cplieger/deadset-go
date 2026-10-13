package kinds

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/cfg"
)

// deadStoresIn is every store of one function body that no read reaches, in the
// order the body writes them, by a liveness walk over the body's control-flow graph.
// The population is the variables the body and its signature declare, less the ones
// [escaped] names. A return naming no value reads every named result. A function
// literal reads every variable it names where it is written, and a store a literal
// naming its variable may precede on some path is never dead, since a call of the
// literal the graph does not order may read it ([flow.settleCaptures]).
func deadStoresIn(info *types.Info, fn *functionBody) []*ast.Ident {
	subjects := localVariables(info, fn.body)
	for _, name := range fieldNames(fn.recv, fn.signature.Params, fn.signature.Results) {
		if variable, declared := info.Defs[name].(*types.Var); declared && name.Name != blankName {
			subjects[variable] = true
		}
	}
	if len(subjects) == 0 {
		return nil
	}
	for object := range escaped(info, fn.body, subjects) {
		delete(subjects, object)
	}
	if len(subjects) == 0 {
		return nil
	}

	var results []types.Object
	for _, name := range fieldNames(fn.signature.Results) {
		if object := info.Defs[name]; object != nil && subjects[object] {
			results = append(results, object)
		}
	}
	graph := cfg.New(fn.body, mayReturn(info))
	flow := newFlow(info, graph, subjects, results)
	flow.settle()
	flow.settleCaptures()
	return flow.dead()
}

// localVariables is every variable one body declares: not a parameter, not a
// result, not a receiver, not a field, and not the variable a type switch binds
// per clause, each of which is written by a mechanism this kind does not answer
// for.
func localVariables(info *types.Info, body *ast.BlockStmt) map[types.Object]bool {
	bound := make(map[types.Object]bool)
	subjects := make(map[types.Object]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		if object, implicit := info.Implicits[n]; implicit {
			bound[object] = true
		}
		switch n := n.(type) {
		case *ast.FuncType:
			for _, field := range fieldNames(n.Params, n.Results) {
				bound[info.Defs[field]] = true
			}
		case *ast.Ident:
			variable, declared := info.Defs[n].(*types.Var)
			if declared && !variable.IsField() && n.Name != blankName {
				subjects[variable] = true
			}
		}
		return true
	})
	for object := range bound {
		delete(subjects, object)
	}
	return subjects
}

// fieldNames is every declared name of a set of field lists, which is how a
// signature written inside a body declares its own parameters and results.
func fieldNames(lists ...*ast.FieldList) []*ast.Ident {
	var names []*ast.Ident
	for _, list := range lists {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			names = append(names, field.Names...)
		}
	}
	return names
}

// escaped is every variable of one body a store to which may be read where the
// control-flow graph cannot see it.
//
// Two constructs put a variable there. Its address is taken, so any holder of the
// pointer reads it. A method with a pointer receiver is selected on it, which takes
// its address implicitly.
func escaped(info *types.Info, body *ast.BlockStmt, subjects map[types.Object]bool) map[types.Object]bool {
	scan := &escapeScan{info: info, subjects: subjects, out: make(map[types.Object]bool)}
	ast.Inspect(body, scan.visit)
	return scan.out
}

// escapeScan accumulates the variables of one body a store to which may be read
// outside the control-flow graph.
type escapeScan struct {
	info     *types.Info
	subjects map[types.Object]bool
	out      map[types.Object]bool
}

// visit reads one node for the constructs that take a variable out of the
// population.
func (s *escapeScan) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.UnaryExpr:
		if n.Op == token.AND {
			s.escape(addressed(n.X))
		}
	case *ast.SelectorExpr:
		if selectsPointerMethod(s.info, n) {
			s.escape(n.X)
		}
	}
	return true
}

// escape takes the variable one expression names out of the population.
func (s *escapeScan) escape(expr ast.Expr) {
	name, isName := ast.Unparen(expr).(*ast.Ident)
	if !isName {
		return
	}
	if object := s.info.Uses[name]; object != nil && s.subjects[object] {
		s.out[object] = true
	}
}

// addressed is the expression whose address an operand of the address-of operator
// takes: the base of any chain of selectors, indexes and dereferences, because
// taking the address of a part takes the address of the whole.
func addressed(expr ast.Expr) ast.Expr {
	for {
		switch held := ast.Unparen(expr).(type) {
		case *ast.SelectorExpr:
			expr = held.X
		case *ast.IndexExpr:
			expr = held.X
		case *ast.SliceExpr:
			expr = held.X
		case *ast.StarExpr:
			expr = held.X
		default:
			return expr
		}
	}
}

// selectsPointerMethod reports whether one selector selects a method whose
// receiver is a pointer on a value that is not one, which takes the address of the
// value selected on. A selection through a pointer, the operand's own or an
// embedded one, takes no address. Selection.Indirect also reports true for a
// pointer operand on a pointer method, https://go.dev/issue/8353, which is the
// answer wanted here.
func selectsPointerMethod(info *types.Info, expr *ast.SelectorExpr) bool {
	selection := info.Selections[expr]
	if selection == nil || selection.Kind() != types.MethodVal || selection.Indirect() {
		return false
	}
	signature, isFunc := selection.Obj().Type().(*types.Signature)
	if !isFunc || signature.Recv() == nil {
		return false
	}
	_, byPointer := signature.Recv().Type().(*types.Pointer)
	return byPointer
}

// mayReturn answers the control-flow graph's question about one call: every call
// returns, apart from a call to the panic built-in, whose path ends there.
//
// Assuming a call returns keeps every path the graph would otherwise drop, and a
// path kept is a path a store may be read on, so the answer never turns a live
// store into a dead one.
func mayReturn(info *types.Info) func(*ast.CallExpr) bool {
	return func(call *ast.CallExpr) bool {
		name, isName := ast.Unparen(call.Fun).(*ast.Ident)
		if !isName {
			return true
		}
		builtin, isBuiltin := info.Uses[name].(*types.Builtin)
		return !isBuiltin || builtin.Name() != panicBuiltin
	}
}

// access is one read of or store to a variable, in the order the body evaluates
// them.
type access struct {
	object types.Object
	name   *ast.Ident // the identifier a store is written at, nil for a read

	// capture marks the read a function literal makes of a variable it names.
	capture bool

	// shielded marks a store a capture of its variable may precede, which
	// [flow.settleCaptures] decides.
	shielded bool
}

// flow is the liveness walk over one body's control-flow graph: the accesses of
// each block in evaluation order, and which variables are live entering each block.
type flow struct {
	index    map[*cfg.Block]int
	blocks   []*cfg.Block
	accesses [][]access
	liveIn   []map[types.Object]bool
}

// newFlow reads every block of one graph into the accesses its nodes make. The
// graph places a range statement's key and value before the loop, read once; the
// language stores them at the start of every iteration, so they are stores of the
// range body's first block instead.
func newFlow(info *types.Info, graph *cfg.CFG, subjects map[types.Object]bool, results []types.Object) *flow {
	f := &flow{
		blocks:   graph.Blocks,
		accesses: make([][]access, len(graph.Blocks)),
		liveIn:   make([]map[types.Object]bool, len(graph.Blocks)),
		index:    make(map[*cfg.Block]int, len(graph.Blocks)),
	}
	iterated := make(map[ast.Node]bool)
	for _, block := range graph.Blocks {
		for _, target := range rangeTargets(block) {
			iterated[target] = true
		}
	}
	for at, block := range graph.Blocks {
		f.index[block] = at
		f.liveIn[at] = make(map[types.Object]bool)
		reader := &accessReader{info: info, subjects: subjects, results: results}
		for _, target := range rangeTargets(block) {
			reader.store(target)
		}
		for _, node := range block.Nodes {
			if !iterated[node] {
				reader.node(node)
			}
		}
		f.accesses[at] = reader.made
	}
	return f
}

// rangeTargets is the key and the value one range statement stores, where one
// block is the first block of that statement's body.
func rangeTargets(block *cfg.Block) []ast.Expr {
	loop, isRange := block.Stmt.(*ast.RangeStmt)
	if !isRange || block.Kind != cfg.KindRangeBody {
		return nil
	}
	var targets []ast.Expr
	for _, target := range []ast.Expr{loop.Key, loop.Value} {
		if target != nil {
			targets = append(targets, target)
		}
	}
	return targets
}

// settleCaptures marks every store that a capture of its variable precedes on some
// path, by a forward walk to the fixed point of which variables a literal has named
// on entering each block.
func (f *flow) settleCaptures() {
	capturedIn := make([]map[types.Object]bool, len(f.blocks))
	for at := range f.blocks {
		capturedIn[at] = make(map[types.Object]bool)
	}
	for changed := true; changed; {
		changed = false
		for at, block := range f.blocks {
			out := f.marked(at, capturedIn[at])
			for _, next := range block.Succs {
				changed = joined(capturedIn[f.index[next]], out) || changed
			}
		}
	}
}

func joined(into, from map[types.Object]bool) bool {
	grew := false
	for object := range from {
		if !into[object] {
			into[object] = true
			grew = true
		}
	}
	return grew
}

// marked walks one block's accesses forward from the variables captured entering
// it, marks each store a capture precedes, and returns the variables captured
// leaving it.
func (f *flow) marked(at int, in map[types.Object]bool) map[types.Object]bool {
	captured := make(map[types.Object]bool, len(in))
	for object := range in {
		captured[object] = true
	}
	for i := range f.accesses[at] {
		one := &f.accesses[at][i]
		switch {
		case one.capture:
			captured[one.object] = true
		case one.name != nil && captured[one.object]:
			one.shielded = true
		}
	}
	return captured
}

// settle iterates the liveness equations to their fixed point: a variable is live
// entering a block when the block reads it before writing it, or when it is live
// entering some successor and the block does not write it first.
func (f *flow) settle() {
	for changed := true; changed; {
		changed = false
		for at := len(f.blocks) - 1; at >= 0; at-- {
			live := f.transfer(at, f.liveOut(at))
			if !sameLive(live, f.liveIn[at]) {
				f.liveIn[at] = live
				changed = true
			}
		}
	}
}

// liveOut is the set of variables live leaving one block, which is what is live
// entering any of its successors.
func (f *flow) liveOut(at int) map[types.Object]bool {
	live := make(map[types.Object]bool)
	for _, next := range f.blocks[at].Succs {
		for object := range f.liveIn[f.index[next]] {
			live[object] = true
		}
	}
	return live
}

// transfer walks one block's accesses backwards over what is live leaving it and
// returns what is live entering it: a read makes a variable live, a store kills it.
func (f *flow) transfer(at int, live map[types.Object]bool) map[types.Object]bool {
	held := make(map[types.Object]bool, len(live))
	for object := range live {
		held[object] = true
	}
	for _, one := range slices.Backward(f.accesses[at]) {
		if one.name == nil {
			held[one.object] = true
			continue
		}
		delete(held, one.object)
	}
	return held
}

// dead is every store no read reaches, found by walking each block's accesses
// backwards over what the fixed point says is live leaving it.
func (f *flow) dead() []*ast.Ident {
	var found []*ast.Ident
	for at := range f.blocks {
		live := f.liveOut(at)
		for _, one := range slices.Backward(f.accesses[at]) {
			if one.name == nil {
				live[one.object] = true
				continue
			}
			if !live[one.object] && !one.shielded {
				found = append(found, one.name)
			}
			delete(live, one.object)
		}
	}
	slices.SortFunc(found, func(a, b *ast.Ident) int { return int(a.Pos() - b.Pos()) })
	return found
}

func sameLive(a, b map[types.Object]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for object := range a {
		if !b[object] {
			return false
		}
	}
	return true
}

// accessReader turns the nodes of one block into the accesses they make, in the
// order the language evaluates them.
type accessReader struct {
	info     *types.Info
	subjects map[types.Object]bool
	results  []types.Object // the named results of the population
	made     []access
}

// node reads one node of a block. A block holds the statements a body writes and
// the subexpressions of the control statements it does not, so a node that stores
// is an assignment, an increment or a declaration with a value, and every other
// node is read for the variables it names, a return naming no value reading the
// named results as well.
func (r *accessReader) node(n ast.Node) {
	switch n := n.(type) {
	case *ast.AssignStmt:
		r.assignment(n)
	case *ast.IncDecStmt:
		r.reads(n.X)
		r.store(n.X)
	case *ast.ReturnStmt:
		r.reads(n)
		if len(n.Results) == 0 {
			for _, object := range r.results {
				r.made = append(r.made, access{object: object})
			}
		}
	case *ast.ValueSpec:
		if len(n.Values) == 0 {
			return
		}
		for _, value := range n.Values {
			r.reads(value)
		}
		for _, name := range n.Names {
			r.storeAt(name, r.info.Defs[name])
		}
	default:
		r.reads(n)
	}
}

// assignment reads one assignment: the right side first, then the left, which is
// the order the language evaluates them. A compound assignment reads its target
// before storing to it, and the assignment a type switch writes stores nothing,
// because the variable it binds is the switch's own per clause.
func (r *accessReader) assignment(stmt *ast.AssignStmt) {
	if bindsTypeSwitch(stmt) {
		for _, value := range stmt.Rhs {
			r.reads(value)
		}
		return
	}
	if stmt.Tok != token.ASSIGN && stmt.Tok != token.DEFINE {
		for _, target := range stmt.Lhs {
			r.reads(target)
		}
	}
	for _, value := range stmt.Rhs {
		r.reads(value)
	}
	for _, target := range stmt.Lhs {
		r.store(target)
	}
}

// bindsTypeSwitch reports whether one assignment is the guard of a type switch,
// whose single right side is a type assertion naming no type.
func bindsTypeSwitch(stmt *ast.AssignStmt) bool {
	if len(stmt.Rhs) != 1 {
		return false
	}
	assertion, isAssertion := stmt.Rhs[0].(*ast.TypeAssertExpr)
	return isAssertion && assertion.Type == nil
}

// store records a store to one assignment target. A target that is not a bare
// identifier writes a part of a value and so reads the value it writes into.
func (r *accessReader) store(target ast.Expr) {
	name, isName := ast.Unparen(target).(*ast.Ident)
	if !isName {
		r.reads(target)
		return
	}
	object := r.info.Defs[name]
	if object == nil {
		object = r.info.Uses[name]
	}
	r.storeAt(name, object)
}

// storeAt records a store to one variable of the population.
func (r *accessReader) storeAt(name *ast.Ident, object types.Object) {
	if object == nil || !r.subjects[object] {
		return
	}
	r.made = append(r.made, access{object: object, name: name})
}

// reads records a read of every variable of the population one expression names,
// and a capture of every one a function literal inside it names, except a literal
// called where it is written outside a go or defer statement, which runs there.
func (r *accessReader) reads(n ast.Node) {
	if n == nil {
		return
	}
	immediate := immediateLiterals(n)
	var inside []bool
	ast.Inspect(n, func(held ast.Node) bool {
		if held == nil {
			inside = inside[:len(inside)-1]
			return true
		}
		lit, literal := held.(*ast.FuncLit)
		captured := literal && !immediate[lit] || len(inside) > 0 && inside[len(inside)-1]
		inside = append(inside, captured)
		if name, isName := held.(*ast.Ident); isName {
			if object := r.info.Uses[name]; object != nil && r.subjects[object] {
				r.made = append(r.made, access{object: object, capture: captured})
			}
		}
		return true
	})
}

// immediateLiterals is every function literal under n that is the callee of the call
// it is written in, true outside a go or defer statement, where it runs at the call.
func immediateLiterals(n ast.Node) map[*ast.FuncLit]bool {
	immediate := make(map[*ast.FuncLit]bool)
	ast.Inspect(n, func(held ast.Node) bool {
		switch held := held.(type) {
		case *ast.GoStmt:
			immediate[calledLiteral(held.Call)] = false
		case *ast.DeferStmt:
			immediate[calledLiteral(held.Call)] = false
		case *ast.CallExpr:
			if lit := calledLiteral(held); lit != nil {
				if _, seen := immediate[lit]; !seen {
					immediate[lit] = true
				}
			}
		}
		return true
	})
	return immediate
}

func calledLiteral(call *ast.CallExpr) *ast.FuncLit {
	lit, _ := ast.Unparen(call.Fun).(*ast.FuncLit)
	return lit
}
