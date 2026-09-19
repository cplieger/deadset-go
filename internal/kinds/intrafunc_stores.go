package kinds

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/cfg"
)

// deadStoresIn is every store of one function body that no read reaches, in the
// order the body writes them.
//
// The answer is a liveness walk over the control-flow graph of the body. A
// variable is live at a point when some path from that point reads it before
// writing it again; a store to a variable that is not live where the store happens
// is a store nothing reads, which is the rule the compiler applies to the same
// construct and the one the external linters of this kind's overlap share.
//
// The population is the variables the body declares, and only the ones no
// construct can reach behind the graph's back: escaped names it for the four
// constructs that do, and a variable among them is answered for by nothing here.
func deadStoresIn(info *types.Info, body *ast.BlockStmt) []*ast.Ident {
	subjects := localVariables(info, body)
	if len(subjects) == 0 {
		return nil
	}
	for object := range escaped(info, body, subjects) {
		delete(subjects, object)
	}
	if len(subjects) == 0 {
		return nil
	}

	graph := cfg.New(body, mayReturn(info))
	flow := newFlow(info, graph, subjects)
	flow.settle()
	return flow.dead()
}

// localVariables is every variable one body declares: not a parameter, not a
// result, not a receiver, not a field, and not the variable a type switch binds
// per clause, each of which is written by a mechanism this kind does not answer
// for.
func localVariables(info *types.Info, body *ast.BlockStmt) map[types.Object]bool {
	bound := make(map[types.Object]bool)
	for _, object := range info.Implicits {
		bound[object] = true
	}
	subjects := make(map[types.Object]bool)
	ast.Inspect(body, func(n ast.Node) bool {
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
// Four constructs put a variable there. Its address is taken, so any holder of the
// pointer reads it. A function literal names it, so a call the graph does not
// order reads it. A method with a pointer receiver is selected on it, which takes
// its address implicitly. A selector, an index or a dereference on it is assigned
// to, which reads the variable to address the part being written.
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
	case *ast.FuncLit:
		s.captured(n)
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

// captured takes every variable of the population one function literal names out
// of it: the call the literal becomes is one no graph of the enclosing body orders.
func (s *escapeScan) captured(literal *ast.FuncLit) {
	for object := range readObjects(s.info, literal.Body) {
		if s.subjects[object] {
			s.out[object] = true
		}
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
// receiver is a pointer, which takes the address of the value selected on.
func selectsPointerMethod(info *types.Info, expr *ast.SelectorExpr) bool {
	selection := info.Selections[expr]
	if selection == nil || selection.Kind() != types.MethodVal {
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
}

// flow is the liveness walk over one body's control-flow graph: the accesses of
// each block in evaluation order, and which variables are live entering each block.
type flow struct {
	index    map[*cfg.Block]int
	blocks   []*cfg.Block
	accesses [][]access
	liveIn   []map[types.Object]bool
}

// newFlow reads every block of one graph into the accesses its nodes make.
func newFlow(info *types.Info, graph *cfg.CFG, subjects map[types.Object]bool) *flow {
	f := &flow{
		blocks:   graph.Blocks,
		accesses: make([][]access, len(graph.Blocks)),
		liveIn:   make([]map[types.Object]bool, len(graph.Blocks)),
		index:    make(map[*cfg.Block]int, len(graph.Blocks)),
	}
	for at, block := range graph.Blocks {
		f.index[block] = at
		f.liveIn[at] = make(map[types.Object]bool)
		reader := &accessReader{info: info, subjects: subjects}
		for _, node := range block.Nodes {
			reader.node(node)
		}
		f.accesses[at] = reader.made
	}
	return f
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
			if !live[one.object] {
				found = append(found, one.name)
			}
			delete(live, one.object)
		}
	}
	slices.SortFunc(found, func(a, b *ast.Ident) int { return int(a.Pos() - b.Pos()) })
	return found
}

// sameLive reports whether two liveness sets hold the same variables.
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
	made     []access
}

// node reads one node of a block. A block holds the statements a body writes and
// the subexpressions of the control statements it does not, so a node that stores
// is an assignment, an increment or a declaration with a value, and every other
// node is read for the variables it names.
func (r *accessReader) node(n ast.Node) {
	switch n := n.(type) {
	case *ast.AssignStmt:
		r.assignment(n)
	case *ast.IncDecStmt:
		r.reads(n.X)
		r.store(n.X)
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

// reads records a read of every variable of the population one expression names.
func (r *accessReader) reads(n ast.Node) {
	if n == nil {
		return
	}
	ast.Inspect(n, func(held ast.Node) bool {
		name, isName := held.(*ast.Ident)
		if !isName {
			return true
		}
		if object := r.info.Uses[name]; object != nil && r.subjects[object] {
			r.made = append(r.made, access{object: object})
		}
		return true
	})
}
