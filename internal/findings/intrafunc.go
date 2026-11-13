package findings

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/analysis/passes/unreachable"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/packages"
)

// The Contract's words for the subjects of these kinds. Each names a part of a
// declaration rather than a declaration, so a finding about one carries the
// enclosing declaration's reference, a parameter's own under it, and the part's own
// position.
const (
	parameterSubject = "parameter"
	receiverSubject  = "receiver"
	resultSubject    = "result"
	statementSubject = "statement"
	caseSubject      = "case"
	storeSubject     = "store"
)

// blankName is the identifier that declares nothing, which is a parameter, a
// receiver and a store the source has already said nothing reads.
const blankName = "_"

// panicBuiltin is the name of the built-in whose call ends a control-flow path.
const panicBuiltin = "panic"

// unusedParameter reports a named, non-blank parameter of a function or method
// whose body names it nowhere, on a function whose signature is free to change.
// The body is the whole evidence, because the type checker resolves every identifier
// of it. [intrafunc.freeSignature] decides free for this kind and the result kind. A
// published declaration of a library is free, because no caller makes a body read a
// parameter it never names, and the breaking edit is what the fixability says. A
// parameter used under one build configuration is used. A function literal is free
// where it is called by name or is the callee of the call it is written in.
func unusedParameter(in *pass) ([]draft, error) {
	return in.intra.parameters()
}

// unusedReceiver reports a named, non-blank method receiver the method body names
// nowhere, whatever fixes the method's signature: Go permits a method with no
// receiver name, so the fix deletes an identifier and changes no signature, which
// is why the kind's fixability is the deletable one and the parameter kind's is not.
func unusedReceiver(in *pass) ([]draft, error) {
	return in.intra.receivers()
}

// unusedResult reports a result of a function that every call site in the loaded
// graph discards, under a closed-world precondition, because an unknown caller may
// consume a result. A function with no call site is an unused declaration's subject.
// A result is discarded by a call in statement position, under go or defer, or
// assigned to the blank identifier at its own index; every other context, a
// multi-value call passed straight to another call among them, uses it.
func unusedResult(in *pass) ([]draft, error) {
	return in.intra.results()
}

// unreachableStatement reports a statement control flow cannot reach.
//
// The rule is the one the toolchain's own unreachable pass applies, run over each
// loaded package's syntax and type information rather than reimplemented: the
// finding's position is the diagnostic's own and its subject is the declaration
// the statement is written in.
func unreachableStatement(in *pass) ([]draft, error) {
	return in.intra.unreachableStatements()
}

// deadStore reports a write to a local variable that no read reaches on any path of
// the function's control-flow graph before the next store or the end of the body,
// in every function body and every function literal, a literal of a package-level
// variable's initializer included. The subject is a variable the body or its
// signature declares, never a package-level variable or a field, and a return naming
// no value reads every named result. A variable a store to which may be read where
// the graph cannot see is no subject ([escaped]). Every call but the panic built-in
// is assumed to return.
func deadStore(in *pass) ([]draft, error) {
	return in.intra.deadStores()
}

// unreachableCase reports a case clause of a type switch that can never match
// because an earlier clause names an interface every value the later one names
// implements. A clause naming nil, the default clause and a type parameter are no
// subject. A switch over values has none either, because the language refuses a
// duplicated constant case.
func unreachableCase(in *pass) ([]draft, error) {
	return in.intra.unreachableCases()
}

// intrafunc is what the kinds of this group read: the free-signature answer, and
// each kind's parts, read in one walk per configuration ([intrafunc.read]).
type intrafunc struct {
	in *pass

	// compiled is every file by the configurations that compiled it, named as the
	// file set names it before any line directive ([inventory.Offset]).
	compiled map[string]inventory.ConfigSet

	// exempted is every declaration an exemption class retained, on evidence in
	// any file. A class stands for a mechanism that reaches the declaration by a
	// name the analysis cannot see, and a mechanism that reaches a function fixes
	// its signature, so a retained function's signature is not free.
	exempted map[inventory.SymbolID]bool

	// foreign is every declaration a caller outside the source names with the
	// signature that caller requires: the linker through a go:linkname directive, C
	// through an export directive, the test driver, which runs every test,
	// benchmark, fuzz test and TestMain the toolchain recognises in a test file, and
	// code a type error left unresolved, whose interface may fix the signature.
	foreign map[inventory.SymbolID]bool

	// programmed is every declaration a program run on its own names, whose call
	// sites in that program are outside the reference set.
	programmed map[inventory.SymbolID]bool

	// valued is every declaration a reference names outside call position, which
	// is a function used as a value: its signature is fixed by the func or
	// interface type the value reaches.
	valued map[inventory.SymbolID]bool

	// held is each kind's parts by its code, the unused-unexported kind's being the
	// local declarations ([pass.localsOf]), and called every declaration some
	// configuration calls.
	held   map[vocab.Code]*parts
	called map[inventory.SymbolID]bool
}

// newIntrafunc prepares what these kinds read.
func newIntrafunc(in *pass) *intrafunc {
	g := &intrafunc{
		in:         in,
		exempted:   make(map[inventory.SymbolID]bool, len(in.exempted)+len(in.TestEvidence)),
		foreign:    make(map[inventory.SymbolID]bool),
		programmed: make(map[inventory.SymbolID]bool),
		valued:     make(map[inventory.SymbolID]bool),
		compiled:   make(map[string]inventory.ConfigSet),
		held:       make(map[vocab.Code]*parts),
		called:     make(map[inventory.SymbolID]bool),
	}
	for _, code := range []vocab.Code{
		vocab.UnusedParameter, vocab.UnusedReceiver, vocab.UnusedResult, vocab.UnreachableStatement,
		vocab.UnreachableCase, vocab.DeadStore, vocab.UnusedUnexported,
	} {
		g.held[code] = newParts()
	}
	maps.Copy(g.exempted, in.exempted)
	for ix := range in.TestEvidence {
		exemption := &in.TestEvidence[ix]
		g.exempted[exemption.ID] = true
	}
	if in.Merged != nil {
		for i := range in.Merged.Roots {
			root := &in.Merged.Roots[i]
			switch root.Kind {
			case roots.RootLinkname, roots.RootCgoExport, roots.RootTest, roots.RootTypeError, roots.RootFuzz:
				g.foreign[root.ID] = true
			case roots.RootProgram:
				g.programmed[root.ID] = true
			}
		}
		for i := range in.Merged.References {
			r := &in.Merged.References[i]
			if r.Kind != facts.RefCall {
				g.valued[r.To] = true
			}
		}
	}
	return g
}

// freeSignature reports whether one declaration's signature answers only to its
// body, the precondition of the parameter and result kinds: it is not free when the
// declaration is [intrafunc.exempted], [intrafunc.valued] or [intrafunc.foreign],
// when an outside interface fixes it ([readFixed]), or when its
// body is a stub, which exists for its callers. A library's published declaration is
// free whatever the run knows of its consumers, since no caller can make a body read a
// parameter; [intrafunc.callersUnknown] answers for the one kind a caller outside the
// graph decides.
func (g *intrafunc) freeSignature(id inventory.SymbolID, decl *ast.FuncDecl) bool {
	switch {
	case g.exempted[id], g.valued[id], g.foreign[id], g.in.fixed[id]:
		return false
	case isStub(decl):
		return false
	default:
		return true
	}
}

// twinned reports whether another file of the declaration's package declares it
// too, for another build configuration: every configuration's callers pass its
// parameters, and each body is one platform's answer to them. A receiver's name is
// no part of the signature, and a result every visible call discards is decided by
// those calls.
func (g *intrafunc) twinned(id inventory.SymbolID, decl *ast.FuncDecl) bool {
	symbol := g.in.symbol(id)
	return symbol != nil && decl != nil && len(g.in.twins.keys[twinKey(symbol.PkgPath, decl)]) > 1
}

// callersUnknown reports whether a program names one declaration, or it is part of a
// library's importable surface, and so its callers are not all in the loaded graph,
// the unused-result kind's precondition. It reads the narrowing kinds' completeness
// declaration, because a consumer set loaded whole is every caller only where the
// configuration says so.
func (g *intrafunc) callersUnknown(id inventory.SymbolID) bool {
	if g.programmed[id] {
		return true
	}
	symbol := g.in.symbol(id)
	if symbol == nil || !symbol.Exported || !g.in.importable(symbol) {
		return false
	}
	if g.in.Config == nil || g.in.Config.Target.Kind != report.TargetKindLibrary {
		return false
	}
	return !g.in.consumersLoaded()
}

// isStub reports whether one function body is a stub: it holds no statement, or it
// holds nothing but a call to the panic built-in. Such a body was never written to
// read what its signature declares, so the signature answers to its callers alone.
func isStub(decl *ast.FuncDecl) bool {
	return decl == nil || isStubBody(decl.Body)
}

// isStubBody is [isStub] for any function body, a literal's among them.
func isStubBody(body *ast.BlockStmt) bool {
	if body == nil {
		return true
	}
	for _, stmt := range body.List {
		switch stmt := stmt.(type) {
		case *ast.EmptyStmt:
		case *ast.ExprStmt:
			if !isPanicCall(stmt.X) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isPanicCall reports whether one expression is a call to the panic built-in,
// which the type checker resolves rather than the spelling.
func isPanicCall(expr ast.Expr) bool {
	call, isCall := expr.(*ast.CallExpr)
	if !isCall {
		return false
	}
	name, isName := call.Fun.(*ast.Ident)
	return isName && name.Name == panicBuiltin
}

// part is one subject of this group a configuration found: the draft about it, the
// file it is written in as [intrafunc.compiled] names it, and every configuration
// that found it.
type part struct {
	file  string
	draft draft
	found inventory.ConfigSet
}

// partKey is a part's identity: its declaration, and the byte of the declaration's
// file its token is written at. A line directive can render two parts at one
// position, never write them at one byte.
type partKey struct {
	decl    inventory.SymbolID
	written int
}

// parts accumulates the subjects of one kind across every build configuration, by
// [partKey].
type parts struct {
	keyed map[partKey]*part
	order []partKey
}

// newParts prepares an accumulator.
func newParts() *parts {
	return &parts{keyed: make(map[partKey]*part)}
}

// hold records that the configuration at config found one part of decl, written at
// pos.
func (p *parts) hold(config int, one *Configured, pos token.Pos, decl inventory.SymbolID, found *draft) {
	at := one.Resolve.Positions().Offset(pos)
	key := partKey{decl: decl, written: at.At}
	if first, seen := p.keyed[key]; seen {
		first.found = first.found.With(config)
		return
	}
	p.order = append(p.order, key)
	found.decl, found.written = decl, at.At
	p.keyed[key] = &part{draft: *found, file: at.File, found: inventory.ConfigSet(0).With(config)}
}

// newPart starts the draft about one part of a declaration, written at at.
func newPart(code vocab.Code, ref, kind, name string, at token.Position, message string) *draft {
	position := positionAt(at)
	found := findingAt(code, Subject{
		Ref:       ref,
		Kind:      kind,
		Name:      name,
		SizeLines: position.EndLine - position.Line + 1,
	}, position, message)
	return &found
}

// comparePositions orders two rendered positions by path, line and column, which
// is the order a report carries findings in.
func comparePositions(a, b Position) int {
	if c := scope.Order(a.Path, b.Path); c != 0 {
		return c
	}
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	return a.Column - b.Column
}

// findings is the draft of every part each configuration that compiled its file
// found, in position order: a part one configuration finds and another that
// compiles the same file does not is no subject. The file is the one written, so a
// line directive moving a part's position elsewhere does not drop it. A part of a
// declaration the sweep judged dead is reported beside the declaration's own
// finding, a part is never pending, and a declaration of test code nothing
// references is the exception unjudged states.
func (g *intrafunc) findings(held *parts) []draft {
	slices.SortFunc(held.order, func(a, b partKey) int {
		return cmp.Or(comparePositions(held.keyed[a].draft.Position, held.keyed[b].draft.Position),
			scope.Order(held.keyed[a].file, held.keyed[b].file), cmp.Compare(a.written, b.written))
	})
	found := make([]draft, 0, len(held.order))
	for _, at := range held.order {
		one := held.keyed[at]
		symbol := g.in.symbol(one.draft.decl)
		if symbol == nil || g.unjudged(symbol, one.draft.decl) || one.found != g.compiled[one.file] {
			continue
		}
		found = append(found, one.draft)
	}
	return found
}

// unjudged reports whether one declaration of test code is outside the reach of
// the intra-function kinds: a declaration of test code is judged while test code
// references it, and one nothing references, which the production sweep cannot
// judge either, has none of its parts reported.
func (g *intrafunc) unjudged(symbol *inventory.Symbol, id inventory.SymbolID) bool {
	candidate := g.in.candidateOf(id)
	if candidate == nil || (!symbol.InTestFile && !symbol.TestSupport) {
		return false
	}
	return candidate.TestRefs == 0 || candidate.UnreferencedTest
}

// walked is one function the run compiled: its declaration, the type information
// of the variant that compiled it, and the identifier of the declaration the
// inventory holds for it. A package-level variable's initializer is walked too,
// with no declaration and the initializer as its value, for the function literals
// it holds.
type walked struct {
	decl  *ast.FuncDecl
	value ast.Expr
	info  *types.Info
	id    inventory.SymbolID
}

// root is the syntax one walked function's parts are read from.
func (fn *walked) root() ast.Node {
	if fn.decl != nil {
		return fn.decl.Body
	}
	return fn.value
}

// walk is what the one walk read of one build configuration: every function
// declaration with a body and every package-level variable initializer holding a
// function literal ([initializersOf]), each read from its [sourcesOf] file, and every
// call of a declaration of the inventory, keyed by the declaration called.
type walk struct {
	one          *Configured
	calls        map[inventory.SymbolID][]*callSite
	functions    []walked
	initializers []walked
}

// read walks one configuration, recording the path of every file it compiled and
// every declaration it calls, and adds what each kind finds there to its parts.
func (g *intrafunc) read(config int, one *Configured, files []typedFile) error {
	w := walk{one: one, calls: make(map[inventory.SymbolID][]*callSite)}
	if one.Resolve == nil {
		return nil
	}
	for _, typed := range files {
		file := one.Resolve.Positions().Offset(typed.file.Package).File
		g.compiled[file] = g.compiled[file].With(config)
		w.read(typed.pkg.TypesInfo, typed.file)
	}
	for id := range w.calls {
		g.called[id] = true
	}
	return g.answer(config, &w)
}

// answer adds what each kind finds in one configuration's walk to its parts. A
// result is held whoever calls its function, and [intrafunc.settle] keeps the
// results of a function some configuration calls.
func (g *intrafunc) answer(c int, w *walk) error {
	for _, fn := range w.functions {
		if err := g.answerDeclared(c, w, &fn); err != nil {
			return err
		}
	}
	for _, fn := range w.initializers {
		if err := g.calledLiterals(c, w, &fn, g.held[vocab.UnusedParameter]); err != nil {
			return err
		}
	}
	if w.one.Facts != nil {
		for _, p := range w.one.Facts.Packages() {
			if err := g.reportUnreachable(c, w.one, p, g.held[vocab.UnreachableStatement]); err != nil {
				return err
			}
		}
	}
	for _, fn := range w.everyFunction() {
		if err := g.answerBodies(c, w, &fn); err != nil {
			return err
		}
	}
	return nil
}

// answerBodies adds what the dead-store kind and the local declarations find in one
// walked function's bodies.
func (g *intrafunc) answerBodies(c int, w *walk, fn *walked) error {
	for _, body := range bodies(fn) {
		if err := g.storesOf(c, w, fn, &body, g.held[vocab.DeadStore]); err != nil {
			return err
		}
	}
	return g.in.localsOf(c, w, fn, g.held[vocab.UnusedUnexported])
}

// answerDeclared adds what the parameter, receiver, result and case kinds find in
// one declared function.
func (g *intrafunc) answerDeclared(c int, w *walk, fn *walked) error {
	if err := g.parametersOf(c, w, fn, g.held[vocab.UnusedParameter]); err != nil {
		return err
	}
	err := g.unread(c, w, fn, fn.decl.Recv, fn.decl.Body, g.held[vocab.UnusedReceiver], vocab.UnusedReceiver, receiverSubject,
		func(ref, _ string) string { return ref })
	if err != nil {
		return err
	}
	if g.freeSignature(fn.id, fn.decl) && !g.callersUnknown(fn.id) {
		if err := g.resultsOf(c, w, fn, g.held[vocab.UnusedResult]); err != nil {
			return err
		}
	}
	return g.casesOf(c, w, fn, g.held[vocab.UnreachableCase])
}

// settle drops the results of every function no configuration calls, once every
// configuration is read: a function with no call site is an unused declaration's
// subject.
func (g *intrafunc) settle() {
	results := g.held[vocab.UnusedResult]
	results.order = slices.DeleteFunc(results.order, func(key partKey) bool {
		if g.called[key.decl] {
			return false
		}
		delete(results.keyed, key)
		return true
	})
}

// read adds what one file holds to the walk.
func (w *walk) read(info *types.Info, f *ast.File) {
	w.functions = append(w.functions, declaredFunctions(w.one, info, f)...)
	for _, decl := range f.Decls {
		if group, isGroup := decl.(*ast.GenDecl); isGroup && group.Tok == token.VAR {
			for _, spec := range group.Specs {
				w.initializers = append(w.initializers, initializersOf(w.one, info, spec)...)
			}
		}
	}
	collectCalls(w.one, info, f, w.calls)
}

// everyFunction is every walked function and initializer of one walk.
func (w *walk) everyFunction() []walked {
	return slices.Concat(w.functions, w.initializers)
}

// initializersOf is every value of one variable specification that holds a
// function literal, each under the name it initializes, or under the first name
// where one call initializes them all.
func initializersOf(one *Configured, info *types.Info, spec ast.Spec) []walked {
	values, isValues := spec.(*ast.ValueSpec)
	if !isValues || len(values.Names) == 0 {
		return nil
	}
	var found []walked
	for i, value := range values.Values {
		if len(literalBodies(value)) == 0 {
			continue
		}
		name := values.Names[0]
		if len(values.Names) == len(values.Values) {
			name = values.Names[i]
		}
		if id, inventoried := one.Resolve.At(name.Pos()); inventoried {
			found = append(found, walked{value: value, info: info, id: id})
		}
	}
	return found
}

// declaredFunctions is every function declaration of one file that carries a body
// and that the inventory holds.
func declaredFunctions(one *Configured, info *types.Info, f *ast.File) []walked {
	var found []walked
	for _, decl := range f.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || fn.Body == nil {
			continue
		}
		id, inventoried := one.Resolve.At(fn.Name.Pos())
		if !inventoried {
			continue
		}
		found = append(found, walked{decl: fn, info: info, id: id})
	}
	return found
}

// parameters is the answer of the unused-parameter kind.
func (g *intrafunc) parameters() ([]draft, error) {
	return g.findings(g.held[vocab.UnusedParameter]), nil
}

// receivers is the answer of the unused-receiver kind.
func (g *intrafunc) receivers() ([]draft, error) {
	return g.findings(g.held[vocab.UnusedReceiver]), nil
}

// literalVariables is every local variable one body declares with a function
// literal that is no stub as its initial value, with that literal.
func literalVariables(info *types.Info, body *ast.BlockStmt) map[types.Object]*ast.FuncLit {
	held := make(map[types.Object]*ast.FuncLit)
	initialize := func(names, values []ast.Expr) {
		if len(names) != len(values) {
			return
		}
		for i, value := range values {
			lit, isLiteral := ast.Unparen(value).(*ast.FuncLit)
			name, isName := names[i].(*ast.Ident)
			if isLiteral && isName && !isStubBody(lit.Body) && info.Defs[name] != nil {
				held[info.Defs[name]] = lit
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				initialize(n.Lhs, n.Rhs)
			}
		case *ast.ValueSpec:
			initialize(nameExprs(n.Names), n.Values)
		}
		return true
	})
	return held
}

// nameExprs is a list of names as expressions.
func nameExprs(names []*ast.Ident) []ast.Expr {
	found := make([]ast.Expr, len(names))
	for i, name := range names {
		found[i] = name
	}
	return found
}

// parametersOf records the unread parameters of one function where its signature is
// free, and those of every literal its body calls by name ([calledByName]) or calls
// where it is written ([calledWhereWritten]), whose signatures are free whatever the
// function's is.
func (g *intrafunc) parametersOf(c int, w *walk, fn *walked, held *parts) error {
	if g.freeSignature(fn.id, fn.decl) && !g.twinned(fn.id, fn.decl) {
		if err := g.parametersIn(c, w, fn, fn.decl.Type.Params, fn.decl.Body, held); err != nil {
			return err
		}
	}
	for _, lit := range calledByName(fn.info, fn.decl.Body) {
		if err := g.parametersIn(c, w, fn, lit.Type.Params, lit.Body, held); err != nil {
			return err
		}
	}
	return g.calledLiterals(c, w, fn, held)
}

// calledLiterals records the unread parameters of every literal one walked function
// calls where it is written.
func (g *intrafunc) calledLiterals(c int, w *walk, fn *walked, held *parts) error {
	for _, lit := range calledWhereWritten(fn.root()) {
		if err := g.parametersIn(c, w, fn, lit.Type.Params, lit.Body, held); err != nil {
			return err
		}
	}
	return nil
}

// parametersIn records the unread parameters of one field list, each under its own
// reference.
func (g *intrafunc) parametersIn(c int, w *walk, fn *walked, fields *ast.FieldList, body *ast.BlockStmt, held *parts) error {
	return g.unread(c, w, fn, fields, body, held, vocab.UnusedParameter, parameterSubject, inventory.ParameterRef)
}

// calledWhereWritten is every function literal inside one node, a stub excepted,
// that is the callee of the call it is written in, the call of a go or a defer
// statement included: no other expression holds the literal, so nothing but its
// body answers for its signature.
func calledWhereWritten(root ast.Node) []*ast.FuncLit {
	var found []*ast.FuncLit
	ast.Inspect(root, func(n ast.Node) bool {
		if call, isCall := n.(*ast.CallExpr); isCall {
			if lit, isLiteral := ast.Unparen(call.Fun).(*ast.FuncLit); isLiteral && !isStubBody(lit.Body) {
				found = append(found, lit)
			}
		}
		return true
	})
	return found
}

// calledByName is every function literal of one body, a stub excepted, that
// initializes a local variable the body only calls: every use of the variable is the
// function of a call, so no use passes, stores, returns or assigns it, and the
// literal's signature answers to its body alone.
func calledByName(info *types.Info, body *ast.BlockStmt) []*ast.FuncLit {
	held := literalVariables(info, body)
	called := make(map[*ast.Ident]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		if call, isCall := n.(*ast.CallExpr); isCall {
			if name, isName := ast.Unparen(call.Fun).(*ast.Ident); isName {
				called[name] = true
			}
		}
		return true
	})
	ast.Inspect(body, func(n ast.Node) bool {
		if name, isName := n.(*ast.Ident); isName && !called[name] {
			delete(held, info.Uses[name])
		}
		return true
	})
	found := make([]*ast.FuncLit, 0, len(held))
	for _, lit := range held {
		found = append(found, lit)
	}
	slices.SortFunc(found, func(a, b *ast.FuncLit) int { return cmp.Compare(a.Pos(), b.Pos()) })
	return found
}

// unread records every named, non-blank identifier of one field list that body does
// not read, under the reference ref makes of the declaration's and the name.
func (g *intrafunc) unread(c int, w *walk, fn *walked, fields *ast.FieldList, body *ast.BlockStmt, held *parts,
	code vocab.Code, word string, ref func(declared, name string) string,
) error {
	symbol := g.in.symbol(fn.id)
	if fields == nil || symbol == nil {
		return nil
	}
	read := readObjects(fn.info, body)
	for _, field := range fields.List {
		for _, name := range field.Names {
			object, declared := fn.info.Defs[name].(*types.Var)
			if !declared || name.Name == blankName || read[object] {
				continue
			}
			at, err := w.one.Resolve.Render(name.Pos())
			if err != nil {
				return err
			}
			held.hold(c, w.one, name.Pos(), fn.id, newPart(code, ref(symbol.Ref, name.Name), word, name.Name, at,
				word+" "+name.Name+" of "+namedSymbol(symbol)+" is never read in the body"))
		}
	}
	return nil
}

// readObjects is every object an identifier inside one body denotes, which is what
// the type checker recorded for the uses of that body.
func readObjects(info *types.Info, body *ast.BlockStmt) map[types.Object]bool {
	read := make(map[types.Object]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		if name, isName := n.(*ast.Ident); isName {
			if object := info.Uses[name]; object != nil {
				read[object] = true
			}
		}
		return true
	})
	return read
}

// results is the answer of the unused-result kind: one finding per result every
// call site in the loaded graph discards. A configuration that compiles no call of a
// function some configuration calls discards each of its results at every site.
func (g *intrafunc) results() ([]draft, error) {
	return g.findings(g.held[vocab.UnusedResult]), nil
}

// resultsOf records every result of one function no call site of the walk uses.
func (g *intrafunc) resultsOf(c int, w *walk, fn *walked, held *parts) error {
	results, symbol := fn.decl.Type.Results, g.in.symbol(fn.id)
	if results == nil || symbol == nil {
		return nil
	}
	at, count := 0, results.NumFields()
	for _, field := range results.List {
		names := max(len(field.Names), 1)
		for offset := range names {
			index := at + offset
			if usedAtAnySite(w.calls[fn.id], index, count) {
				continue
			}
			position := field.Type.Pos()
			if offset < len(field.Names) {
				position = field.Names[offset].Pos()
			}
			site, err := w.one.Resolve.Render(position)
			if err != nil {
				return err
			}
			name, described := resultName(field, offset, index, count)
			held.hold(c, w.one, position, fn.id, newPart(vocab.UnusedResult, symbol.Ref, resultSubject, name, site,
				resultSubject+" "+described+" of "+namedSymbol(symbol)+" is discarded at every call site"))
		}
		at += names
	}
	return nil
}

// resultName is the name a finding gives one result of count, and how its message
// describes it: the position, the count and the type as the signature spells them,
// after the result's own name where the signature gives one. A text line renders
// the kind before the name, so an unnamed one reads "result 1 of 2 (Info)".
func resultName(field *ast.Field, offset, index, count int) (name, described string) {
	place := strconv.Itoa(index+1) + " of " + strconv.Itoa(count) + " (" + types.ExprString(field.Type) + ")"
	if offset < len(field.Names) && field.Names[offset].Name != blankName {
		return field.Names[offset].Name, field.Names[offset].Name + ", " + place + ","
	}
	return place, place
}

// usedAtAnySite reports whether any call site uses the result at one index.
func usedAtAnySite(sites []*callSite, index, count int) bool {
	for _, site := range sites {
		if site.uses(index, count) {
			return true
		}
	}
	return false
}

// callSite is one call of a function, with the node the call's value flows into,
// which is what decides whether a result is used.
type callSite struct {
	parent ast.Node
	call   *ast.CallExpr
}

// uses reports whether the call uses the result at one index of a signature
// declaring count results.
//
// A call in statement position, and one under go or defer, discards every result.
// A call that is the whole right side of an assignment naming one target per
// result uses the result whose target is not the blank identifier. Every other
// context uses the result, which is the conservative answer.
func (s *callSite) uses(index, count int) bool {
	switch parent := s.parent.(type) {
	case *ast.ExprStmt, *ast.GoStmt, *ast.DeferStmt:
		return false
	case *ast.AssignStmt:
		if len(parent.Rhs) != 1 || parent.Rhs[0] != s.call || len(parent.Lhs) != count {
			return true
		}
		name, isName := parent.Lhs[index].(*ast.Ident)
		return !isName || name.Name != blankName
	default:
		return true
	}
}

// collectCalls records every call of one file against the declaration it calls,
// carrying the node the call's value flows into.
func collectCalls(one *Configured, info *types.Info, f *ast.File,
	calls map[inventory.SymbolID][]*callSite,
) {
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if call, isCall := n.(*ast.CallExpr); isCall {
			if id, held := calledFunction(one, info, call); held {
				calls[id] = append(calls[id], &callSite{parent: enclosing(stack), call: call})
			}
		}
		stack = append(stack, n)
		return true
	})
}

// enclosing is the node one call's value flows into, skipping the parentheses that
// carry no meaning.
func enclosing(stack []ast.Node) ast.Node {
	for _, held := range slices.Backward(stack) {
		if _, parenthesized := held.(*ast.ParenExpr); !parenthesized {
			return held
		}
	}
	return nil
}

// calledFunction is the declaration of the inventory one call calls, and false
// where the callee is no declaration the run enumerated: a built-in, a value of
// func type, a method reached through an interface, and a declaration of another
// module all answer so.
func calledFunction(one *Configured, info *types.Info, call *ast.CallExpr) (inventory.SymbolID, bool) {
	var name *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		name = fun
	case *ast.SelectorExpr:
		name = fun.Sel
	default:
		return "", false
	}
	function, called := info.Uses[name].(*types.Func)
	if !called {
		return "", false
	}
	return one.Resolve.Object(function)
}

// unreachableStatements is the answer of the unreachable-statement kind: the
// toolchain's own pass, run over each package the configuration compiled.
func (g *intrafunc) unreachableStatements() ([]draft, error) {
	return g.findings(g.held[vocab.UnreachableStatement]), nil
}

// reportUnreachable runs the unreachable pass over one package and records one
// subject per diagnostic, at the diagnostic's own position and under the
// declaration the statement is written in.
//
// The pass is driven directly rather than through a checker: the driver supplies
// the file set, the syntax, the package and its type information, and the one
// result the pass requires, which is the inspector over the same syntax.
func (g *intrafunc) reportUnreachable(c int, one *Configured, p *packages.Package, held *parts) error {
	var reported []analysis.Diagnostic
	driven := &analysis.Pass{
		Analyzer:  unreachable.Analyzer,
		Fset:      one.Result.Fset,
		Files:     p.Syntax,
		Pkg:       p.Types,
		TypesInfo: p.TypesInfo,
		ResultOf:  map[*analysis.Analyzer]any{inspect.Analyzer: inspector.New(p.Syntax)},
		Report:    func(d analysis.Diagnostic) { reported = append(reported, d) },
	}
	if _, err := unreachable.Analyzer.Run(driven); err != nil {
		return err
	}
	for _, diagnostic := range reported {
		id, enclosed := one.Resolve.Holder(diagnostic.Pos)
		if !enclosed {
			continue
		}
		at, err := one.Resolve.Render(diagnostic.Pos)
		if err != nil {
			return err
		}
		if symbol := g.in.symbol(id); symbol != nil {
			held.hold(c, one, diagnostic.Pos, id, newPart(vocab.UnreachableStatement, symbol.Ref, statementSubject, symbol.Name, at,
				"statement of "+namedSymbol(symbol)+" is unreachable"))
		}
	}
	return nil
}

// unreachableCases is the answer of the unreachable-case kind.
func (g *intrafunc) unreachableCases() ([]draft, error) {
	return g.findings(g.held[vocab.UnreachableCase]), nil
}

// casesOf records every unreachable case clause of every type switch of one body.
func (g *intrafunc) casesOf(c int, w *walk, fn *walked, held *parts) error {
	symbol := g.in.symbol(fn.id)
	if symbol == nil {
		return nil
	}
	var failed error
	ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
		switchStmt, isTypeSwitch := n.(*ast.TypeSwitchStmt)
		if !isTypeSwitch || failed != nil {
			return failed == nil
		}
		clauses := assertedTypes(fn.info, switchStmt)
		for at, later := range clauses {
			if !subsumedByEarlier(clauses[:at], later.types) {
				continue
			}
			site, err := w.one.Resolve.Render(later.clause.Case)
			if err != nil {
				failed = err
				return false
			}
			held.hold(c, w.one, later.clause.Case, fn.id, newPart(vocab.UnreachableCase, symbol.Ref, caseSubject, symbol.Name, site,
				"case of the type switch in "+namedSymbol(symbol)+" can never match, because an earlier case always matches first"))
		}
		return true
	})
	return failed
}

// clause is one case clause of a type switch and the types it asserts, the nil
// value dropped because a clause naming it is always reachable.
type clause struct {
	clause *ast.CaseClause
	types  []types.Type
}

// assertedTypes is the type of every case clause of one type switch, in source
// order, with the default clause absent because it names no type.
func assertedTypes(info *types.Info, switchStmt *ast.TypeSwitchStmt) []clause {
	clauses := make([]clause, 0, len(switchStmt.Body.List))
	for _, stmt := range switchStmt.Body.List {
		one, isClause := stmt.(*ast.CaseClause)
		if !isClause || len(one.List) == 0 {
			continue
		}
		asserted := make([]types.Type, 0, len(one.List))
		for _, expr := range one.List {
			if typ := info.TypeOf(expr); typ != nil && typ != types.Typ[types.UntypedNil] {
				asserted = append(asserted, typ)
			}
		}
		clauses = append(clauses, clause{clause: one, types: asserted})
	}
	return clauses
}

// subsumedByEarlier reports whether an earlier clause always matches before a
// later one: the earlier names an interface that every value the later names
// implements.
func subsumedByEarlier(earlier []clause, later []types.Type) bool {
	for _, before := range earlier {
		for _, first := range before.types {
			for _, second := range later {
				if subsumes(first, second) {
					return true
				}
			}
		}
	}
	return false
}

// subsumes reports whether every value of second matches first, which holds when
// first is an interface second implements. A type parameter names a method set the
// constraint decides and is never one.
func subsumes(first, second types.Type) bool {
	if _, parameterized := types.Unalias(first).(*types.TypeParam); parameterized {
		return false
	}
	iface, isInterface := first.Underlying().(*types.Interface)
	if !isInterface {
		return false
	}
	return types.Implements(second, iface)
}

// deadStores is the answer of the dead-store kind.
func (g *intrafunc) deadStores() ([]draft, error) {
	return g.findings(g.held[vocab.DeadStore]), nil
}

// functionBody is one body and the signature that declares the variables it
// receives: its receiver, its parameters and its results.
type functionBody struct {
	recv      *ast.FieldList
	signature *ast.FuncType
	body      *ast.BlockStmt
}

// bodies is every body one walked function holds: a declaration's own, and the
// body of each function literal written inside it. A literal has a control flow of
// its own, so its stores are answered over a graph of its own.
func bodies(fn *walked) []functionBody {
	var found []functionBody
	if fn.decl != nil {
		found = append(found, functionBody{recv: fn.decl.Recv, signature: fn.decl.Type, body: fn.decl.Body})
	}
	return append(found, literalBodies(fn.root())...)
}

// literalBodies is the body of every function literal written inside one node.
func literalBodies(root ast.Node) []functionBody {
	var found []functionBody
	ast.Inspect(root, func(n ast.Node) bool {
		if literal, isLiteral := n.(*ast.FuncLit); isLiteral {
			found = append(found, functionBody{signature: literal.Type, body: literal.Body})
		}
		return true
	})
	return found
}

// storesOf records every dead store of one body.
func (g *intrafunc) storesOf(c int, w *walk, fn *walked, body *functionBody, held *parts) error {
	symbol := g.in.symbol(fn.id)
	if symbol == nil {
		return nil
	}
	for _, name := range deadStoresIn(fn.info, body) {
		at, err := w.one.Resolve.Render(name.Pos())
		if err != nil {
			return err
		}
		store := newPart(vocab.DeadStore, symbol.Ref, storeSubject, name.Name, at, "store to "+name.Name+" in "+namedSymbol(symbol)+" is never read")
		store.Details.WritePositions = []Position{store.Position}
		held.hold(c, w.one, name.Pos(), fn.id, store)
	}
	return nil
}
