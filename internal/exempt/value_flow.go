package exempt

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"slices"
)

// flowGraph is the value flow of one program the encoding class solves, read by one
// walk of every declaration ([encodingFlow.readFlow]): the places the program stores
// interface values in, the fields it writes, and every value that arrives where
// something reads it by name at run time, with what reads it there.
type flowGraph struct {
	stored *storedTypes
	writes fieldWrites

	// The arrivals by the rule that found them, solved in this order, because a
	// symbol two rules reach at one site keeps the first rule's detail: an argument
	// of the target's calls, a method's return at an interface-typed result, a type
	// argument, and an assertion of a field read by reflection.
	arguments  []argumentSite
	returns    []arrival
	typeArgs   []arrival
	assertions []fieldAssertion
}

// arrival is one value that reaches a destination: where, what reads it there, and
// every type it carries, a type parameter's followed by each type argument the
// program instantiates the parameter with ([bindings]).
type arrival struct {
	types []types.Type
	d     destination
	at    token.Pos
}

// argumentSite is one argument a call hands to something that reads it. Which types
// it carries depends on the stores, so it is read once every store is linked
// ([encodingFlow.carried]).
type argumentSite struct {
	info      *types.Info
	enclosing *types.Func
	arg       ast.Expr
	d         destination
	rule      argumentRule
}

// argumentRule is what an argument carries to the callee that reads it.
type argumentRule uint8

const (
	// readArgument is a destination's: its own type, the types stored in a
	// container it names and its literals write ([encodingFlow.ownTypes]).
	readArgument argumentRule = iota
	// decodedArgument is a parameter's that only decodes: its own type.
	decodedArgument
	// crossingArgument is a parameter's a value leaves the analysis through: the
	// struct values it carries.
	crossingArgument
)

// fieldAssertion is one assertion to an interface type of what Interface returns
// for a reflect.Value holding a field of a struct ([assertedField]).
type fieldAssertion struct {
	st    *types.Struct
	iface *types.Interface
	name  string
	at    token.Pos
}

// fieldWrites is every field the program writes, keyed by where it is declared.
type fieldWrites struct {
	// assigned is by a composite literal, keyed or positional, an assignment or an
	// increment, in any file.
	assigned map[token.Position]bool
	// every adds each field of a struct type a conversion produces, and of each
	// struct a field of it holds by value; production is every's production code.
	every, production map[token.Position]bool
}

// readFlow reads the flow graph: the program's calls, returns, instantiations,
// assertions, stores and writes, each package of the program in order and each
// declaration in source order. A loaded consumer's calls are read as the
// target's are, because a consumer that encodes a target type reads its fields.
func (f *encodingFlow) readFlow() {
	in := f.kept.in
	f.graph = flowGraph{
		stored: newStoredTypes(f.edge.sites),
		writes: fieldWrites{
			assigned:   make(map[token.Position]bool),
			every:      make(map[token.Position]bool),
			production: make(map[token.Position]bool),
		},
	}
	imports := sourceImports(in.Facts.Program())
	for _, p := range in.Facts.Program() {
		if p.TypesInfo != nil {
			f.readInstances(p.TypesInfo, imports)
		}
		for _, file := range in.Facts.Files(p) {
			f.readFile(p.TypesInfo, file)
		}
	}
	f.graph.stored.link(in)
}

// readInstances reads the type arguments of one package's instantiations, in
// position order.
func (f *encodingFlow) readInstances(info *types.Info, imports map[string][]string) {
	ids := slices.SortedFunc(maps.Keys(info.Instances), func(a, b *ast.Ident) int { return cmp.Compare(a.Pos(), b.Pos()) })
	for _, id := range ids {
		if a, reaches := f.typeArgumentArrival(info, id, imports); reaches {
			f.graph.typeArgs = append(f.graph.typeArgs, a)
		}
	}
}

// readFile reads every declaration of one source file. A file several package
// variants type-check is read once per variant, and every variant resolves the
// same positions, so the repeated records collapse on the site.
func (f *encodingFlow) readFile(info *types.Info, file *ast.File) {
	name := f.kept.in.Result.Fset.Position(file.FileStart).Filename
	test := f.kept.in.Resolve.Build(file.FileStart).TestCode(f.kept.in.Resolve.Positions().InTestFile(file.FileStart))
	for _, decl := range file.Decls {
		enclosing := enclosingFunc(info, decl)
		r := &declarationReader{
			flow: f, info: info, decl: decl, enclosing: enclosing, dir: filepath.Dir(name), test: test,
			store: &storeWalk{storedTypes: f.graph.stored, info: info, fn: enclosing},
		}
		r.returns, r.answered = f.returnDestination(decl, enclosing)
		ast.PreorderStack(decl, nil, r.visit)
	}
}

// declarationReader reads one declaration into the flow graph.
type declarationReader struct {
	flow      *encodingFlow
	info      *types.Info
	decl      ast.Decl
	enclosing *types.Func // the function decl declares, nil for any other declaration
	store     *storeWalk
	walk      *reflectWalk // the body's reflect.Value locals, read at its first assertion
	dir       string
	returns   destination // what reads the values the method returns, where answered
	answered  bool
	test      bool
}

// visit reads one node of the declaration, stack holding the nodes above it.
func (r *declarationReader) visit(n ast.Node, stack []ast.Node) bool {
	f := r.flow
	inLiteral := slices.ContainsFunc(stack, isFuncLit)
	r.store.visit(n, inLiteral)
	f.graph.writes.read(f.edge.sites, r.info, n, r.test)
	switch n := n.(type) {
	case *ast.CallExpr:
		f.readCall(r.info, r.decl, r.enclosing, r.dir, n)
	case *ast.ReturnStmt:
		if r.answered && !inLiteral {
			f.returnedValues(r.info, r.enclosing, n, &r.returns)
		}
	case *ast.TypeAssertExpr:
		r.assertion(n)
	}
	return true
}

// assertion reads one type assertion of a function's body ([assertedField]).
func (r *declarationReader) assertion(n *ast.TypeAssertExpr) {
	fd, declares := r.decl.(*ast.FuncDecl)
	if r.enclosing == nil || !declares {
		return
	}
	if r.walk == nil {
		r.walk = &reflectWalk{info: r.info, assigned: reflectValueAssignments(r.info, fd.Body)}
	}
	if a, converts := assertedField(r.walk, n); converts {
		r.flow.graph.assertions = append(r.flow.graph.assertions, a)
	}
}

// isFuncLit reports whether one node is a function literal.
func isFuncLit(n ast.Node) bool {
	_, literal := n.(*ast.FuncLit)
	return literal
}

// read records the fields one node writes: by a composite literal, an assignment
// or an increment or a decrement, and the fields a conversion from a value of
// another type produces.
func (w *fieldWrites) read(sites *declarationSites, info *types.Info, n ast.Node, test bool) {
	var assigned, converted []*types.Var
	selected := func(expr ast.Expr) {
		if sel, isSelector := ast.Unparen(expr).(*ast.SelectorExpr); isSelector {
			assigned = append(assigned, selectedField(info, sel))
		}
	}
	switch n := n.(type) {
	case *ast.CompositeLit:
		assigned = literalFields(info, n)
	case *ast.AssignStmt:
		for _, lhs := range n.Lhs {
			selected(lhs)
		}
	case *ast.IncDecStmt:
		selected(n.X)
	case *ast.CallExpr:
		converted = fieldsConverted(info, n)
	}
	for _, field := range slices.Concat(assigned, converted) {
		if field == nil {
			continue
		}
		at := sites.of(field)
		w.every[at] = true
		if !test {
			w.production[at] = true
		}
	}
	for _, field := range assigned {
		if field != nil {
			w.assigned[sites.of(field)] = true
		}
	}
}

// forSite is every field the code one destination's site stands for writes: the
// program's production code, and its test files too where the site is in a test
// file or the run counts test references.
func (w *fieldWrites) forSite(site *evidence, production bool) map[token.Position]bool {
	if site.testCode() || !production {
		return w.every
	}
	return w.production
}

// fieldsConverted is every field of the struct type one conversion produces from a
// value of another type, and of each struct a field of it holds by value, each of
// which the conversion writes.
func fieldsConverted(info *types.Info, call *ast.CallExpr) []*types.Var {
	if len(call.Args) != 1 {
		return nil
	}
	to, converts := info.Types[call.Fun]
	from := info.TypeOf(call.Args[0])
	if !converts || !to.IsType() || from == nil || types.Identical(to.Type, from) {
		return nil
	}
	return structFields(to.Type, nil, map[*types.Struct]bool{})
}
