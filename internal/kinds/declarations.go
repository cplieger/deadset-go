package kinds

import (
	"go/ast"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
)

// deprecatedPrefix opens the paragraph of a doc comment that marks a declaration
// deprecated, which is the convention the standard Go tools recognise.
const deprecatedPrefix = "Deprecated:"

// testOfDeadCodeMessage states the rule the test-of-dead-code kind applies, which
// the Contract requires the message to say rather than leave to a reader.
const testOfDeadCodeMessage = "every target symbol this test references is reported dead"

// UnusedExported reports an exported declaration that is not a member of a type
// and that nothing references.
func UnusedExported(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(vocab.UnusedExported), nil
}

// UnusedUnexported reports an unexported declaration that is not a member of a
// type and that nothing references, and a constant or a type a function body
// declares that nothing in the body references.
func UnusedUnexported(in *Input) ([]Finding, error) {
	locals, err := in.intraFunc().unusedLocals()
	if err != nil {
		return nil, err
	}
	return append(in.unusedDeclarations(vocab.UnusedUnexported), locals...), nil
}

// UnusedMember reports a struct field that nothing references and whose container
// is not itself dead, because a member of a dead container falls with it and is
// reported inside its component.
func UnusedMember(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(vocab.UnusedMember), nil
}

// TestOnlyUse reports a declaration that no production file references and at
// least one test file does, which is the symbol and its tests deleted in one
// change rather than an unreferenced declaration.
func TestOnlyUse(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(vocab.TestOnlyUse), nil
}

// DeprecatedAndUnused reports a declaration carrying a deprecation marker that no
// production file references, which is the residue of a finished migration.
func DeprecatedAndUnused(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(vocab.DeprecatedUnused), nil
}

// TestOfDeadCode reports a test declaration whose set of referenced target
// declarations is non-empty and wholly dead.
//
// The subject is the sweep's own admission rather than a rule of this kind: a test
// that references one live declaration is never admitted, and an admitted test is
// in the component of the declarations it references, so a report names the test
// beside the code it exercises.
func TestOfDeadCode(in *Input) ([]Finding, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		if !candidate.TestOfDeadCode {
			continue
		}
		one, held := in.finding(candidate.ID, vocab.TestOfDeadCode, testOfDeadCodeMessage)
		if !held {
			continue
		}
		one.TestOnly = testOnly(candidate)
		found = append(found, one)
	}
	return found, nil
}

// unusedDeclarations returns the candidates one unused-declaration kind reports,
// which are the candidates the precedence rule assigns to its code.
func (in *Input) unusedDeclarations(code vocab.Code) []Finding {
	if in == nil || in.Sweep == nil {
		return nil
	}
	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		if in.codeOf(candidate) != code {
			continue
		}
		one, held := in.finding(candidate.ID, code, in.unusedMessage(candidate, code))
		if !held {
			continue
		}
		one.TestOnly = testOnly(candidate)
		found = append(found, one)
	}
	return found
}

// codeOf is the one code the unused-declaration kinds report a candidate under, or
// the empty string, so their precedence is one rule. A
// deprecation outranks a test reference, which the finding's test-only field still
// records, and a test reference outranks the unreferenced kinds. A struct field is the
// member kind, so a method of a live type is an unused declaration. The populations
// another kind owns are [Input.outsideTheRule]'s, an exported declaration of a package
// nothing outside imports being the unreachable-export kind's.
func (in *Input) codeOf(candidate *graph.Candidate) vocab.Code {
	symbol := in.symbol(candidate.ID)
	if symbol == nil || in.outsideTheRule(candidate, symbol) {
		return ""
	}
	switch {
	case candidate.UnreferencedTest && symbol.Exported && !member(symbol.Kind):
		return ""
	case candidate.UnreferencedTest:
		return in.shapeCode(symbol)
	case candidate.ProductionRefs == 0 && in.deprecations()[candidate.ID]:
		return vocab.DeprecatedUnused
	case testKept(candidate):
		return vocab.TestOnlyUse
	default:
		return in.shapeCode(symbol)
	}
}

// shapeCode is the unreferenced kind a declaration's shape selects: the member kind
// for a struct field, then the kind its visibility selects.
func (in *Input) shapeCode(symbol *graph.Symbol) vocab.Code {
	switch {
	case member(symbol.Kind):
		return vocab.UnusedMember
	case !symbol.Exported:
		return vocab.UnusedUnexported
	case !in.importable(symbol):
		return ""
	default:
		return vocab.UnusedExported
	}
}

// outsideTheRule reports whether another rule, or none, judges a candidate: a test
// of dead code, a blank declaration, which falls with what it asserts, or a test
// file's declaration that something reaches, a member of a dead parent, an
// interface's declaration but an interface method only tests call, and a
// read-or-write kind's subject.
func (in *Input) outsideTheRule(candidate *graph.Candidate, symbol *graph.Symbol) bool {
	switch {
	case candidate.TestOfDeadCode || symbol.Blank || (testFile(symbol) && !candidate.UnreferencedTest):
		return true
	case in.deadParent(symbol):
		return true
	case interfaceDeclaration(symbol.Kind):
		return !in.testCalledInterfaceMethod(candidate, symbol)
	default:
		return in.readOrWriteSubject(candidate, symbol)
	}
}

// deadParent reports whether a declaration's container is dead. A test file's
// declaration a test reaches is a candidate of the production sweep alone, so its
// members are judged on their own.
func (in *Input) deadParent(symbol *graph.Symbol) bool {
	parent := in.candidateOf(symbol.Parent)
	if parent == nil {
		return false
	}
	container := in.symbol(symbol.Parent)
	return container == nil || !testFile(container) || parent.UnreferencedTest || parent.TestOfDeadCode
}

// unusedMessage is what one unused-declaration finding says, in the reader's words:
// what the subject is, and what the analysis found about its references.
//
// The relation is what decides the second half for the three unreferenced kinds. A
// candidate found by reference counting has no reference at all; one found by
// reachability has references, every one of them from a declaration that is itself
// dead, and saying "no reference" about it would be false.
func (in *Input) unusedMessage(candidate *graph.Candidate, code vocab.Code) string {
	word := in.word(candidate.ID)
	switch code {
	case vocab.TestOnlyUse:
		if candidate.ProductionRefs > 0 {
			return word + " is referenced only from test files and from declarations that are themselves dead"
		}
		return word + " is referenced only from test files and never from production code"
	case vocab.DeprecatedUnused:
		return "deprecated " + word + " has no production reference"
	}

	visibility := "unexported "
	if symbol := in.symbol(candidate.ID); symbol != nil && symbol.Exported {
		visibility = "exported "
	}
	if candidate.Relation == graph.Reachability {
		return visibility + word + " is referenced only from declarations that are themselves dead"
	}
	if visibility == "exported " {
		return visibility + word + " has no reference in the target and none from any loaded consumer"
	}
	return visibility + word + " has no reference in the target"
}

// member reports whether one kind of declaration is a member of a type rather than
// a declaration of its own, which for Go is a field of a struct.
//
// A method with a receiver is not one: it has its own declaration site and its own
// reference set, and a report names it by its own visibility.
func member(kind graph.SymbolKind) bool {
	return kind == graph.KindField
}

// interfaceDeclaration reports whether one kind of declaration is an interface or a
// method an interface declares, which are the interface kinds' subjects.
func interfaceDeclaration(kind graph.SymbolKind) bool {
	return kind == graph.KindInterface || kind == graph.KindInterfaceMethod
}

// readOrWriteSubject reports whether one candidate is a subject of the
// read-and-write kinds rather than an unreferenced declaration: a constant of an
// enumerated type, or a type parameter of a function or a method, that no reference
// of any file names.
//
// The reference count is what divides the two populations. A constant of an
// enumerated type that a test file names is referenced, so it is not an enumerated
// member nothing names and this rule reports it; one nothing names at all is the
// enumerated-member kind's. The same reading gives a type parameter to the
// type-parameter kind, whose one subject is a parameter its own signature and body
// leave unnamed.
func (in *Input) readOrWriteSubject(candidate *graph.Candidate, symbol *graph.Symbol) bool {
	if candidate.ProductionRefs > 0 || candidate.TestRefs > 0 {
		return false
	}
	switch symbol.Kind {
	case graph.KindConst:
		_, enumerated := in.enumMembers()[candidate.ID]
		return enumerated
	case graph.KindTypeParam:
		return declaresTypeParameters(in.symbol(symbol.Parent))
	default:
		return false
	}
}

// enumMembers is every constant of the inventory that is a member of an enumerated
// type, read from the syntax on first use. It is the recognition the
// enumerated-member kind and the enum-group exemption apply, so the three kinds
// that read it agree on what an enumerated type is.
func (in *Input) enumMembers() map[graph.SymbolID]string {
	held := in.index()
	if held.enumerated == nil {
		held.enumerated = enumGroupMembers(in)
	}
	return held.enumerated
}

// testOnly reports whether every reference to one candidate comes from a test file.
func testOnly(candidate *graph.Candidate) bool {
	return candidate.ProductionRefs == 0 && candidate.TestRefs > 0
}

// testKept reports whether a candidate is a test-only use: only tests reference it,
// or every production reference is made by dead code and a test that stays keeps it.
func testKept(candidate *graph.Candidate) bool {
	return testOnly(candidate) || (candidate.TestRefs > 0 && candidate.Relation == graph.Reachability && candidate.KeptByTests)
}

// testFile reports whether a test file declares one symbol, by the language's own
// rule over the file's name.
func testFile(symbol *graph.Symbol) bool {
	_, test := graph.IsTestFile(symbol.Pos.Filename)
	return test
}

// testSupport reports whether a test-support package declares one symbol: every
// configuration of the run that loads the package classified it as one. A
// production import under one configuration makes the package production code,
// whatever another configuration that compiles none of its importers says.
func (in *Input) testSupport(symbol *graph.Symbol) bool {
	return in.classifiedEverywhere(symbol, func(r *load.Result, key string) bool { return r.TestSupport[key] })
}

// testReached reports whether production code only tests reach declares one symbol:
// every configuration that loads its package found only tests reaching it, and not
// every one found it test-support code.
func (in *Input) testReached(symbol *graph.Symbol) bool {
	return !in.testSupport(symbol) && in.classifiedEverywhere(symbol, func(r *load.Result, key string) bool {
		return r.TestSupport[key] || r.TestReached[key]
	})
}

// classifiedEverywhere reports whether some configuration of the run classified the
// package declaring one symbol by classified and every configuration that loads it did.
func (in *Input) classifiedEverywhere(symbol *graph.Symbol, classified func(r *load.Result, key string) bool) bool {
	held := false
	dir := filepath.Join(in.Root, filepath.FromSlash(path.Dir(symbol.Pos.Filename)))
	for _, one := range in.Per {
		switch {
		case one.Result == nil:
		case classified(one.Result, one.Result.PackageKey(symbol.PkgPath, dir)):
			held = true
		case loads(one.Result, one.Result.PackageKey(symbol.PkgPath, dir)):
			return false
		}
	}
	return held
}

// testReachedEvidence is the row of the Contract's evidence table that names the
// codes a finding about production code only tests reach is possible under.
var testReachedEvidence, _ = vocab.EvidenceFor("test-reached", language)

// testReachedUse reports whether a record under code holds a declaration of production
// code only tests reach that a test references, whose finding is possible whether or
// not the record keeps the declaration live.
func (in *Input) testReachedUse(id graph.SymbolID, code vocab.Code) bool {
	symbol := in.symbol(id)
	return testReachedEvidence.Lowers(language, code) && symbol != nil && in.testReached(symbol) && in.testUse().referenced[id]
}

// testUseFacts is what [Input.testUse] computes once per run.
type testUseFacts struct {
	// dead is every dead declaration whose findings are possible because production
	// code only tests reach is: each such declaration a test references, each dead
	// declaration one reaches through a reference or as its container, and each test
	// of dead code every dead target of which is one. No member references a dead
	// declaration outside the set, so reporting the rest of its component without it
	// deletes nothing a withheld member needs.
	dead map[graph.SymbolID]bool
	// referenced is every declaration a test file references.
	referenced map[graph.SymbolID]bool
}

// testUse computes the run's [testUseFacts] on first use.
func (in *Input) testUse() *testUseFacts {
	held := in.index()
	if held.testUse != nil {
		return held.testUse
	}
	facts := &testUseFacts{dead: make(map[graph.SymbolID]bool), referenced: make(map[graph.SymbolID]bool)}
	held.testUse = facts
	if in.Merged == nil || in.Sweep == nil {
		return facts
	}
	out := make(map[graph.SymbolID][]graph.SymbolID)
	for i := range in.Merged.References {
		ref := &in.Merged.References[i]
		if ref.Test {
			facts.referenced[ref.To] = true
		}
		out[ref.From] = append(out[ref.From], ref.To)
	}
	in.walkTestUse(facts, out)
	for admitted := true; admitted; {
		admitted = false
		for i := range in.Sweep.Candidates {
			test := &in.Sweep.Candidates[i]
			if test.TestOfDeadCode && !facts.dead[test.ID] && everyDeadTargetIn(out[test.ID], in.deadProduction, facts.dead) {
				facts.dead[test.ID] = true
				in.walkAdmittedTest(facts, out, test.ID)
				admitted = true
			}
		}
	}
	return facts
}

// walkAdmittedTest adds to facts.dead every dead declaration one admitted test of
// dead code reaches through out or holds as its container, a test file's helpers
// included, so a reported finding is never about a declaration the withheld test
// needs. A production declaration it adds can admit a further test.
func (in *Input) walkAdmittedTest(facts *testUseFacts, out map[graph.SymbolID][]graph.SymbolID, test graph.SymbolID) {
	queue := []graph.SymbolID{test}
	children := in.index().children
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, to := range slices.Concat(out[id], children[id]) {
			if candidate := in.candidateOf(to); candidate != nil && !candidate.TestOfDeadCode && !facts.dead[to] {
				facts.dead[to] = true
				queue = append(queue, to)
			}
		}
	}
}

// deadProduction reports whether one declaration is a dead production declaration:
// a candidate outside the test files that is no test of dead code.
func (in *Input) deadProduction(id graph.SymbolID) bool {
	candidate, symbol := in.candidateOf(id), in.symbol(id)
	return candidate != nil && !candidate.TestOfDeadCode && symbol != nil && !testFile(symbol)
}

// walkTestUse fills facts.dead with the dead declarations of production code only
// tests reach that a test references, and every dead production declaration one
// reaches through out or holds as its container.
func (in *Input) walkTestUse(facts *testUseFacts, out map[graph.SymbolID][]graph.SymbolID) {
	var queue []graph.SymbolID
	add := func(id graph.SymbolID) {
		if !facts.dead[id] && in.deadProduction(id) {
			facts.dead[id] = true
			queue = append(queue, id)
		}
	}
	for i := range in.Sweep.Candidates {
		id := in.Sweep.Candidates[i].ID
		if symbol := in.symbol(id); symbol != nil && facts.referenced[id] && in.testReached(symbol) {
			add(id)
		}
	}
	children := in.index().children
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, to := range out[id] {
			add(to)
		}
		for _, child := range children[id] {
			add(child)
		}
	}
}

// everyDeadTargetIn reports whether a test's references name at least one dead
// production declaration and every one they name is in set.
func everyDeadTargetIn(targets []graph.SymbolID, production func(graph.SymbolID) bool, set map[graph.SymbolID]bool) bool {
	named := false
	for _, to := range targets {
		if !production(to) {
			continue
		}
		if !set[to] {
			return false
		}
		named = true
	}
	return named
}

// loads reports whether one configuration loaded the package of that
// [load.Result.PackageKey].
func loads(r *load.Result, key string) bool {
	return slices.ContainsFunc(r.Packages, func(p *packages.Package) bool { return r.KeyOf(p) == key })
}

// deprecations is every declaration of the inventory carrying a deprecation
// marker, read from the syntax of every configuration and built on first use.
func (in *Input) deprecations() map[graph.SymbolID]bool {
	held := in.index()
	if held.deprecated != nil {
		return held.deprecated
	}
	held.deprecated = make(map[graph.SymbolID]bool)
	for _, one := range in.Per {
		if one.Result == nil || one.Resolve == nil {
			continue
		}
		for _, p := range one.Result.Packages {
			for _, file := range p.Syntax {
				markDeprecated(file, one.Resolve, held.deprecated)
			}
		}
	}
	return held.deprecated
}

// markDeprecated records every declaration of one file whose doc comment carries
// the deprecation paragraph.
//
// A declaration group's own comment marks every declaration in it, which is how
// the convention reads for a grouped constant or variable, and a member carries its
// own. A name the resolver does not answer for is a declaration the inventory does
// not hold, a local variable above all, and marks nothing.
func markDeprecated(file *ast.File, resolve *graph.Resolver, marked map[graph.SymbolID]bool) {
	ast.Inspect(file, func(n ast.Node) bool {
		for _, name := range deprecatedNames(n) {
			if id, held := resolve.At(name.Pos()); held {
				marked[id] = true
			}
		}
		return true
	})
}

// deprecatedNames is every name one node of the syntax declares as deprecated, and
// nothing for a node whose doc comment carries no marker.
func deprecatedNames(n ast.Node) []*ast.Ident {
	switch d := n.(type) {
	case *ast.FuncDecl:
		return named(d.Doc, d.Name)
	case *ast.GenDecl:
		if !deprecatedDoc(d.Doc) {
			return nil
		}
		var names []*ast.Ident
		for _, spec := range d.Specs {
			names = append(names, specNames(spec)...)
		}
		return names
	case *ast.TypeSpec:
		return named(d.Doc, d.Name)
	case *ast.ValueSpec:
		return group(d.Doc, d.Names)
	case *ast.Field:
		return group(d.Doc, d.Names)
	default:
		return nil
	}
}

// named is one name where its own doc comment carries the marker.
func named(doc *ast.CommentGroup, name *ast.Ident) []*ast.Ident {
	if !deprecatedDoc(doc) {
		return nil
	}
	return []*ast.Ident{name}
}

// group is every name of one declaration where its doc comment carries the marker.
func group(doc *ast.CommentGroup, names []*ast.Ident) []*ast.Ident {
	if !deprecatedDoc(doc) {
		return nil
	}
	return names
}

// specNames is every name one specification of a declaration group declares.
func specNames(spec ast.Spec) []*ast.Ident {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return []*ast.Ident{s.Name}
	case *ast.ValueSpec:
		return s.Names
	default:
		return nil
	}
}

// deprecatedDoc reports whether one doc comment carries the deprecation marker: a
// paragraph beginning with the marker, anywhere in the comment rather than in its
// last paragraph alone.
func deprecatedDoc(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	paragraph := true
	for line := range strings.SplitSeq(doc.Text(), "\n") {
		if strings.TrimSpace(line) == "" {
			paragraph = true
			continue
		}
		if paragraph && strings.HasPrefix(line, deprecatedPrefix) {
			return true
		}
		paragraph = false
	}
	return false
}
