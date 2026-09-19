package kinds

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/analysis/passes/unreachable"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/packages"
)

// The codes of the intra-function kinds.
const (
	unusedParameterCode      = "DS1801"
	unusedReceiverCode       = "DS1802"
	unusedResultCode         = "DS1803"
	unreachableStatementCode = "DS1805"
	deadStoreCode            = "DS1807"
	unreachableCaseCode      = "DS1809"
)

// The Contract's words for the subjects of these kinds. Each names a part of a
// declaration rather than a declaration, so a finding about one carries the
// enclosing declaration's reference and the part's own position.
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

// UnusedParameter reports a named, non-blank parameter of a function or method
// whose body names it nowhere, on a function whose signature is free to change.
//
// The body is the whole evidence: the type checker resolves every identifier of
// the body to the object it denotes, so a parameter object no identifier of the
// body denotes is one the function does not read. A parameter declared with the
// blank identifier, and one the signature leaves unnamed, name nothing and are no
// subject.
//
// Free is what decides whether the finding is raised at all, because a parameter
// is part of the function's type and an edit to it is an edit to every call site.
// A signature is not free when the function is exported from a target whose
// consumer set is not declared complete, when a mechanism no reference names
// reaches the declaration, when the function is used as a value rather than
// called, when the linker or a foreign caller names it, and when the body is a
// stub: freeSignature is the one place those are decided and every kind of this
// group that edits a signature asks it.
//
// A parameter used under one build configuration is used, so a configuration whose
// constraint excludes the body that reads it reports nothing.
func UnusedParameter(in *Input) ([]Finding, error) {
	return in.intraFunc().parameters(unusedParameterCode)
}

// UnusedReceiver reports a named, non-blank method receiver the method body names
// nowhere, under the same free-signature rule as the parameter kind.
//
// The fix deletes an identifier rather than changing a signature, because Go
// permits a method with no receiver name, which is why the kind's fixability is
// the deletable one and the parameter kind's is not.
func UnusedReceiver(in *Input) ([]Finding, error) {
	return in.intraFunc().parameters(unusedReceiverCode)
}

// UnusedResult reports a result of a function that every call site in the loaded
// graph discards.
//
// The call sites are the whole evidence and the kind reports only where they are
// all visible: an unknown caller may consume a result, so the same free-signature
// rule the parameter kind applies is the closed-world precondition here, and a
// function with no call site at all is the subject of an unused-declaration kind
// rather than of this one.
//
// A call whose result the source discards is a call in statement position, a call
// under go or defer, and a call whose value is assigned to the blank identifier at
// that result's own index. Every other context uses the result, including a
// multi-value call passed straight to another call, which is the conservative
// answer: the result reaches a parameter the analysis does not follow.
func UnusedResult(in *Input) ([]Finding, error) {
	return in.intraFunc().results()
}

// UnreachableStatement reports a statement control flow cannot reach.
//
// The rule is the one the toolchain's own unreachable pass applies, run over each
// loaded package's syntax and type information rather than reimplemented: the
// finding's position is the diagnostic's own and its subject is the declaration
// the statement is written in.
func UnreachableStatement(in *Input) ([]Finding, error) {
	return in.intraFunc().unreachableStatements()
}

// DeadStore reports a write to a local variable that no read reaches.
//
// The answer is a liveness walk over the control-flow graph of one function body:
// a store is dead when the variable is not live where the store happens, which is
// when no path from the store reaches a read before the next store to the same
// variable or the end of the body.
//
// The subject is a variable the body declares. A parameter, a result and a
// receiver are not subjects, which is what keeps a named result a deferred
// function assigns out of the population, and neither is a package-level variable
// or a field, which the write-only kind answers for.
//
// Four constructs take a variable out of the population, each because a store to
// it may be read where the graph cannot see: its address is taken, a function
// literal of the body names it, a method with a pointer receiver is selected on
// it, and a selector or an index on it is assigned to, which reads the variable to
// address its part. A call is assumed to return, apart from a call to the panic
// built-in, so a path the graph keeps is a path a store may be read on.
func DeadStore(in *Input) ([]Finding, error) {
	return in.intraFunc().deadStores()
}

// UnreachableCase reports a case clause of a type switch that can never match
// because an earlier clause of the same switch always matches first.
//
// One clause subsumes a later one when the earlier names an interface every value
// the later names implements: clauses are evaluated in source order, so the later
// clause is unreachable. That covers an interface ahead of a concrete type, an
// interface ahead of a wider interface, and two structurally identical
// interfaces. A clause naming nil and the default clause are always reachable and
// are no subject, and a type parameter names no method set to compare.
//
// A switch over values has no population: the language refuses a duplicated
// constant case at compile time, so a case a constant case covers never reaches an
// analysis.
func UnreachableCase(in *Input) ([]Finding, error) {
	return in.intraFunc().unreachableCases()
}

// intrafunc is what the kinds of this group read: the free-signature answer, and
// the syntax and type information of every file the run compiled.
type intrafunc struct {
	in *Input

	// exempted is every declaration an exemption class retained. A class stands
	// for a mechanism that reaches the declaration by a name the analysis cannot
	// see, and a mechanism that reaches a function fixes its signature, so a
	// retained function's signature is not free.
	exempted map[graph.SymbolID]bool

	// foreign is every declaration the linker or a foreign caller names, which is
	// the go:linkname and cgo-export root classes.
	foreign map[graph.SymbolID]bool

	// valued is every declaration a reference names outside call position, which
	// is a function used as a value: its signature is fixed by the func or
	// interface type the value reaches.
	valued map[graph.SymbolID]bool
}

// intraFunc prepares what these kinds read, once per findings pass.
func (in *Input) intraFunc() *intrafunc {
	g := &intrafunc{
		in:       in,
		exempted: make(map[graph.SymbolID]bool, len(in.Exempt)),
		foreign:  make(map[graph.SymbolID]bool),
		valued:   make(map[graph.SymbolID]bool),
	}
	for _, exemption := range in.Exempt {
		g.exempted[exemption.ID] = true
	}
	if in.Merged != nil {
		for i := range in.Merged.Roots {
			root := &in.Merged.Roots[i]
			if root.Kind == graph.RootLinkname || root.Kind == graph.RootCgoExport {
				g.foreign[root.ID] = true
			}
		}
		for i := range in.Merged.References {
			r := &in.Merged.References[i]
			if r.Kind != graph.RefCall {
				g.valued[r.To] = true
			}
		}
	}
	return g
}

// freeSignature reports whether the signature of one declaration is free to
// change, which is the precondition of every kind of this group that reports a
// part of a signature.
//
// The five reasons a signature is not free, each a reason a caller the analysis
// cannot see may supply or consume the part:
//
//   - the declaration is exported from a library whose consumer set the
//     configuration does not declare complete, or one of whose declared consumers
//     did not load, so a caller outside the loaded graph may pass the argument. An
//     application's exported declaration is free, because its callers are all in
//     the graph, and so is one in a package nothing outside the module can import.
//   - an exemption class retained the declaration, which is a mechanism reaching
//     it by name: satisfying an interface, answering a duck-typed contract, being
//     named by a template, a marshaller or a reflective lookup.
//   - a reference names the declaration outside call position, so a value of its
//     type reaches a func or an interface type that fixes the signature.
//   - the linker or a foreign caller names it through a go:linkname or an export
//     directive.
//   - the body is a stub, so the signature exists for the declaration's callers
//     and the body was never written to use it.
func (g *intrafunc) freeSignature(id graph.SymbolID, decl *ast.FuncDecl) bool {
	switch {
	case g.publishedWithOpenWorld(id), g.exempted[id], g.valued[id], g.foreign[id]:
		return false
	case isStub(decl):
		return false
	default:
		return true
	}
}

// publishedWithOpenWorld reports whether one declaration is part of a library's
// importable surface whose callers are not all in the loaded graph.
func (g *intrafunc) publishedWithOpenWorld(id graph.SymbolID) bool {
	symbol := g.in.symbol(id)
	if symbol == nil || !symbol.Exported || !g.in.importable(symbol.PkgPath) {
		return false
	}
	if g.in.Config == nil || g.in.Config.Target.Kind != config.Library {
		return false
	}
	return !g.in.consumersLoaded()
}

// isStub reports whether one function body is a stub: it holds no statement, or it
// holds nothing but a call to the panic built-in. Such a body was never written to
// read what its signature declares, so the signature answers to its callers alone.
func isStub(decl *ast.FuncDecl) bool {
	if decl == nil || decl.Body == nil {
		return true
	}
	for _, stmt := range decl.Body.List {
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

// suppressed reports whether a suppression record bound to one declaration names
// one code, which silences the findings that code reports about the declaration's
// parts the way a record bound to a declaration silences the finding about the
// declaration itself.
func (in *Input) suppressed(id graph.SymbolID, code string) bool {
	for i := range in.Marks {
		if in.Marks[i].Bound == id && in.Marks[i].Code == code {
			return true
		}
	}
	return false
}

// part is one subject of this group: a part of a declaration, with the position it
// is written at and what a message calls it.
type part struct {
	id       graph.SymbolID
	kind     string
	name     string
	message  string
	position Position

	// used reports that some configuration found the part in use, which is what
	// takes a parameter, a receiver and a result out of the answer.
	used bool
}

// parts accumulates the subjects of one kind across every build configuration,
// keyed by where each is written, so a file two configurations compile answers
// once.
type parts struct {
	keyed map[Position]*part
	order []Position
}

// newParts prepares an accumulator.
func newParts() *parts {
	return &parts{keyed: make(map[Position]*part)}
}

// hold records one subject, keeping the first record of a part two configurations
// compiled and carrying a use either of them found.
func (p *parts) hold(held *part) {
	if first, seen := p.keyed[held.position]; seen {
		first.used = first.used || held.used
		return
	}
	p.order = append(p.order, held.position)
	p.keyed[held.position] = held
}

// unused is every part no configuration found in use, in position order.
func (p *parts) unused() []*part {
	ordered := make([]*part, 0, len(p.order))
	for _, at := range p.order {
		if held := p.keyed[at]; !held.used {
			ordered = append(ordered, held)
		}
	}
	slices.SortFunc(ordered, func(a, b *part) int { return comparePositions(a.position, b.position) })
	return ordered
}

// comparePositions orders two rendered positions by path, line and column, which
// is the order a report carries findings in.
func comparePositions(a, b Position) int {
	if a.Path != b.Path {
		return strings.Compare(a.Path, b.Path)
	}
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	return a.Column - b.Column
}

// findings completes one finding per part, each about the part's own position and
// the enclosing declaration's reference.
//
// Two things take a part out of the answer. A declaration the sweep judged dead is
// reported under an unused-declaration kind and falls whole, so a part of it is
// the less specific answer and is not reported: every kind of this group claims
// something about a declaration the analysis keeps. And a suppression record bound
// to the declaration and naming this code silences the part the way it silences
// the declaration's own finding.
func (g *intrafunc) findings(code string, held []*part) ([]Finding, error) {
	found := make([]Finding, 0, len(held))
	for _, one := range held {
		if g.in.candidateOf(one.id) != nil || g.in.suppressed(one.id, code) {
			continue
		}
		symbol := g.in.symbol(one.id)
		if symbol == nil {
			continue
		}
		finding := findingAt(code, Subject{
			Ref:       symbol.Ref,
			Kind:      one.kind,
			Name:      one.name,
			SizeLines: one.position.EndLine - one.position.Line + 1,
		}, one.position, one.message)
		if code == deadStoreCode {
			finding.Details.WritePositions = []Position{one.position}
		}
		found = append(found, finding)
	}
	return found, nil
}

// walked is one function the run compiled: its declaration, the type information
// of the variant that compiled it, and the identifier of the declaration the
// inventory holds for it.
type walked struct {
	decl *ast.FuncDecl
	info *types.Info
	id   graph.SymbolID
}

// functions is every function declaration with a body that one build configuration
// compiled, each read from one variant of its package, in the order the walk met
// them.
//
// A file several package variants compile is walked once, because the bodies are
// the same bytes and the answer is about the body. The variant the walk reads is
// the first by package identifier, which is the choice the exemption classes make
// over the same question.
func functions(one *Configured) []walked {
	if one.Result == nil || one.Resolve == nil {
		return nil
	}
	var found []walked
	seen := make(map[string]bool)
	for _, p := range sortedPackages(one.Result.Packages) {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			name := one.Result.Fset.Position(f.FileStart).Filename
			if seen[name] {
				continue
			}
			seen[name] = true
			found = append(found, declaredFunctions(one, p.TypesInfo, f)...)
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

// parameters is the answer of the unused-parameter and the unused-receiver kinds,
// which differ only in which part of the signature they read.
func (g *intrafunc) parameters(code string) ([]Finding, error) {
	held := newParts()
	for i := range g.in.Per {
		one := &g.in.Per[i]
		for _, fn := range functions(one) {
			if !g.freeSignature(fn.id, fn.decl) {
				continue
			}
			fields := fn.decl.Type.Params
			if code == unusedReceiverCode {
				fields = fn.decl.Recv
			}
			if err := g.signature(one, &fn, fields, code, held); err != nil {
				return nil, err
			}
		}
	}
	return g.findings(code, held.unused())
}

// signature records every named, non-blank identifier of one field list and
// whether the body reads it.
func (g *intrafunc) signature(one *Configured, fn *walked, fields *ast.FieldList,
	code string, held *parts,
) error {
	if fields == nil {
		return nil
	}
	read := readObjects(fn.info, fn.decl.Body)
	word := parameterSubject
	if code == unusedReceiverCode {
		word = receiverSubject
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			object, declared := fn.info.Defs[name].(*types.Var)
			if !declared || name.Name == blankName {
				continue
			}
			at, err := one.Resolve.Render(name.Pos())
			if err != nil {
				return err
			}
			held.hold(&part{
				id:       fn.id,
				kind:     word,
				name:     name.Name,
				message:  word + " " + name.Name + " is never read in the body",
				position: positionAt(at),
				used:     read[object],
			})
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
// call site in the loaded graph discards.
func (g *intrafunc) results() ([]Finding, error) {
	held := newParts()
	for i := range g.in.Per {
		one := &g.in.Per[i]
		calls := callsPerFunction(one)
		for _, fn := range functions(one) {
			sites := calls[fn.id]
			if len(sites) == 0 || !g.freeSignature(fn.id, fn.decl) {
				continue
			}
			if err := g.resultsOf(one, &fn, sites, held); err != nil {
				return nil, err
			}
		}
	}
	return g.findings(unusedResultCode, held.unused())
}

// resultsOf records every result of one function and whether any call site of it
// uses that result.
func (g *intrafunc) resultsOf(one *Configured, fn *walked, sites []*callSite, held *parts) error {
	results := fn.decl.Type.Results
	if results == nil {
		return nil
	}
	at := 0
	for _, field := range results.List {
		names := max(len(field.Names), 1)
		for offset := range names {
			index := at + offset
			position := field.Type.Pos()
			name := resultName(field, offset, index)
			if offset < len(field.Names) {
				position = field.Names[offset].Pos()
			}
			site, err := one.Resolve.Render(position)
			if err != nil {
				return err
			}
			held.hold(&part{
				id:       fn.id,
				kind:     resultSubject,
				name:     name,
				message:  resultSubject + " " + name + " is discarded at every call site",
				position: positionAt(site),
				used:     usedAtAnySite(sites, index, results),
			})
		}
		at += names
	}
	return nil
}

// resultName is what a message calls one result: the name the signature gives it,
// and its one-based ordinal where the signature leaves it unnamed.
func resultName(field *ast.Field, offset, index int) string {
	if offset < len(field.Names) && field.Names[offset].Name != blankName {
		return field.Names[offset].Name
	}
	return resultSubject + " " + strconv.Itoa(index+1)
}

// usedAtAnySite reports whether any call site uses the result at one index.
func usedAtAnySite(sites []*callSite, index int, results *ast.FieldList) bool {
	count := 0
	for _, field := range results.List {
		count += max(len(field.Names), 1)
	}
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

// callsPerFunction is every call of every declaration of the inventory that one
// build configuration compiled, keyed by the declaration called.
//
// A file several package variants compile is walked once, for the reason functions
// gives.
func callsPerFunction(one *Configured) map[graph.SymbolID][]*callSite {
	calls := make(map[graph.SymbolID][]*callSite)
	if one.Result == nil || one.Resolve == nil {
		return calls
	}
	seen := make(map[string]bool)
	for _, p := range sortedPackages(one.Result.Packages) {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			name := one.Result.Fset.Position(f.FileStart).Filename
			if seen[name] {
				continue
			}
			seen[name] = true
			collectCalls(one, p.TypesInfo, f, calls)
		}
	}
	return calls
}

// collectCalls records every call of one file against the declaration it calls,
// carrying the node the call's value flows into.
func collectCalls(one *Configured, info *types.Info, f *ast.File,
	calls map[graph.SymbolID][]*callSite,
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
func calledFunction(one *Configured, info *types.Info, call *ast.CallExpr) (graph.SymbolID, bool) {
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
func (g *intrafunc) unreachableStatements() ([]Finding, error) {
	held := newParts()
	for i := range g.in.Per {
		one := &g.in.Per[i]
		if one.Result == nil || one.Resolve == nil {
			continue
		}
		for _, p := range sortedPackages(one.Result.Packages) {
			if p.TypesInfo == nil || len(p.Syntax) == 0 {
				continue
			}
			if err := g.reportUnreachable(one, p, held); err != nil {
				return nil, err
			}
		}
	}
	return g.findings(unreachableStatementCode, held.unused())
}

// reportUnreachable runs the unreachable pass over one package and records one
// subject per diagnostic, at the diagnostic's own position and under the
// declaration the statement is written in.
//
// The pass is driven directly rather than through a checker: the driver supplies
// the file set, the syntax, the package and its type information, and the one
// result the pass requires, which is the inspector over the same syntax.
func (g *intrafunc) reportUnreachable(one *Configured, p *packages.Package, held *parts) error {
	var reported []analysis.Diagnostic
	pass := &analysis.Pass{
		Analyzer:  unreachable.Analyzer,
		Fset:      one.Result.Fset,
		Files:     p.Syntax,
		Pkg:       p.Types,
		TypesInfo: p.TypesInfo,
		ResultOf:  map[*analysis.Analyzer]any{inspect.Analyzer: inspector.New(p.Syntax)},
		Report:    func(d analysis.Diagnostic) { reported = append(reported, d) },
	}
	if _, err := unreachable.Analyzer.Run(pass); err != nil {
		return err
	}
	inside := enclosingDeclarations(one, p.Syntax)
	for _, diagnostic := range reported {
		id, enclosed := declarationAt(inside, diagnostic.Pos)
		if !enclosed {
			continue
		}
		at, err := one.Resolve.Render(diagnostic.Pos)
		if err != nil {
			return err
		}
		symbol := g.in.symbol(id)
		if symbol == nil {
			continue
		}
		held.hold(&part{
			id:       id,
			kind:     statementSubject,
			name:     symbol.Name,
			message:  "statement of " + symbol.Name + " is unreachable",
			position: positionAt(at),
		})
	}
	return nil
}

// span is one function declaration's extent and the identifier of the declaration
// the inventory holds for it.
type span struct {
	id    graph.SymbolID
	start token.Pos
	end   token.Pos
}

// enclosingDeclarations is the extent of every function declaration of one
// package's syntax, ordered so that a position inside one is found by search.
func enclosingDeclarations(one *Configured, files []*ast.File) []span {
	var spans []span
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			id, inventoried := one.Resolve.At(fn.Name.Pos())
			if !inventoried {
				continue
			}
			spans = append(spans, span{id: id, start: fn.Pos(), end: fn.End()})
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return int(a.start - b.start) })
	return spans
}

// declarationAt is the declaration one position is written inside, and false where
// no declaration of the inventory holds it.
func declarationAt(spans []span, pos token.Pos) (graph.SymbolID, bool) {
	at, _ := slices.BinarySearchFunc(spans, pos, func(s span, p token.Pos) int {
		return int(s.start - p)
	})
	for back := min(at, len(spans)-1); back >= 0; back-- {
		if spans[back].start <= pos && pos < spans[back].end {
			return spans[back].id, true
		}
	}
	return "", false
}

// unreachableCases is the answer of the unreachable-case kind.
func (g *intrafunc) unreachableCases() ([]Finding, error) {
	held := newParts()
	for i := range g.in.Per {
		one := &g.in.Per[i]
		if one.Result == nil || one.Resolve == nil {
			continue
		}
		for _, fn := range functions(one) {
			if err := g.casesOf(one, &fn, held); err != nil {
				return nil, err
			}
		}
	}
	return g.findings(unreachableCaseCode, held.unused())
}

// casesOf records every unreachable case clause of every type switch of one body.
func (g *intrafunc) casesOf(one *Configured, fn *walked, held *parts) error {
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
			site, err := one.Resolve.Render(later.clause.Case)
			if err != nil {
				failed = err
				return false
			}
			symbol := g.in.symbol(fn.id)
			if symbol == nil {
				continue
			}
			held.hold(&part{
				id:   fn.id,
				kind: caseSubject,
				name: symbol.Name,
				message: "case of the type switch in " + symbol.Name +
					" can never match, because an earlier case always matches first",
				position: positionAt(site),
			})
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
func (g *intrafunc) deadStores() ([]Finding, error) {
	held := newParts()
	for i := range g.in.Per {
		one := &g.in.Per[i]
		if one.Result == nil || one.Resolve == nil {
			continue
		}
		for _, fn := range functions(one) {
			for _, body := range bodies(fn.decl) {
				if err := g.storesOf(one, &fn, body, held); err != nil {
					return nil, err
				}
			}
		}
	}
	return g.findings(deadStoreCode, held.unused())
}

// bodies is every body one function declaration holds: its own, and the body of
// each function literal written inside it. A literal has a control flow of its
// own, so its stores are answered over a graph of its own.
func bodies(decl *ast.FuncDecl) []*ast.BlockStmt {
	found := []*ast.BlockStmt{decl.Body}
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if literal, isLiteral := n.(*ast.FuncLit); isLiteral {
			found = append(found, literal.Body)
		}
		return true
	})
	return found
}

// storesOf records every dead store of one body.
func (g *intrafunc) storesOf(one *Configured, fn *walked, body *ast.BlockStmt, held *parts) error {
	symbol := g.in.symbol(fn.id)
	if symbol == nil {
		return nil
	}
	for _, name := range deadStoresIn(fn.info, body) {
		at, err := one.Resolve.Render(name.Pos())
		if err != nil {
			return err
		}
		held.hold(&part{
			id:       fn.id,
			kind:     storeSubject,
			name:     name.Name,
			message:  "store to " + name.Name + " is never read",
			position: positionAt(at),
		})
	}
	return nil
}
