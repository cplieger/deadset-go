package kinds

import (
	"go/ast"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// The codes of the unused-declaration kinds.
const (
	unusedExportedCode      = "DS1001"
	unusedUnexportedCode    = "DS1002"
	unusedMemberCode        = "DS1003"
	testOnlyUseCode         = "DS1004"
	testOfDeadCodeCode      = "DS1005"
	deprecatedAndUnusedCode = "DS1006"
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
	return in.unusedDeclarations(unusedExportedCode), nil
}

// UnusedUnexported reports an unexported declaration that is not a member of a
// type and that nothing references.
func UnusedUnexported(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(unusedUnexportedCode), nil
}

// UnusedMember reports a struct field that nothing references and whose container
// is not itself dead, because a member of a dead container falls with it and is
// reported inside its component.
func UnusedMember(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(unusedMemberCode), nil
}

// TestOnlyUse reports a declaration that no production file references and at
// least one test file does, which is the symbol and its tests deleted in one
// change rather than an unreferenced declaration.
func TestOnlyUse(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(testOnlyUseCode), nil
}

// DeprecatedAndUnused reports a declaration carrying a deprecation marker that no
// production file references, which is the residue of a finished migration.
func DeprecatedAndUnused(in *Input) ([]Finding, error) {
	return in.unusedDeclarations(deprecatedAndUnusedCode), nil
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
		one, held := in.finding(candidate.ID, testOfDeadCodeCode, testOfDeadCodeMessage)
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
func (in *Input) unusedDeclarations(code string) []Finding {
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
// the empty string, so their precedence is one rule. Test-support code test code
// references is test-only at the class [Input.classOf] gives it, deprecated or not. A
// deprecation outranks a test reference, which the finding's test-only field still
// records, and a test reference outranks the unreferenced kinds. A struct field is the
// member kind, so a method of a live type is an unused declaration. The populations
// another kind owns are [Input.outsideTheRule]'s, an exported declaration of a package
// nothing outside imports being the unreachable-export kind's.
func (in *Input) codeOf(candidate *graph.Candidate) string {
	symbol := in.symbol(candidate.ID)
	if symbol == nil || in.outsideTheRule(candidate, symbol) {
		return ""
	}
	switch {
	case candidate.UnreferencedTest && symbol.Exported && !member(symbol.Kind):
		return ""
	case candidate.UnreferencedTest:
		return in.shapeCode(symbol)
	case in.supportReferenced(candidate, symbol):
		return testOnlyUseCode
	case candidate.ProductionRefs == 0 && in.deprecations()[candidate.ID]:
		return deprecatedAndUnusedCode
	case candidate.ProductionRefs == 0 && candidate.TestRefs > 0:
		return testOnlyUseCode
	default:
		return in.shapeCode(symbol)
	}
}

// shapeCode is the unreferenced kind a declaration's shape selects: the member kind
// for a struct field, then the kind its visibility selects.
func (in *Input) shapeCode(symbol *graph.Symbol) string {
	switch {
	case member(symbol.Kind):
		return unusedMemberCode
	case !symbol.Exported:
		return unusedUnexportedCode
	case !in.importable(symbol.PkgPath):
		return ""
	default:
		return unusedExportedCode
	}
}

// outsideTheRule reports whether another rule, or none, judges a candidate: a test
// of dead code, a blank declaration, which falls with what it asserts, or a test
// file's declaration that something reaches, a member of a dead parent, an
// interface's declaration, and a read-or-write kind's subject.
func (in *Input) outsideTheRule(candidate *graph.Candidate, symbol *graph.Symbol) bool {
	switch {
	case candidate.TestOfDeadCode || symbol.Blank || (testFile(symbol) && !candidate.UnreferencedTest):
		return true
	case in.deadParent(symbol):
		return true
	default:
		return interfaceDeclaration(symbol.Kind) || in.readOrWriteSubject(candidate, symbol)
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

// supportReferenced reports whether one candidate is a declaration of test-support
// code that test code references and that the test-of-dead-code kind does not
// report. Such a declaration belongs to no dead component.
func (in *Input) supportReferenced(candidate *graph.Candidate, symbol *graph.Symbol) bool {
	return !candidate.TestOfDeadCode && candidate.TestRefs > 0 && !testFile(symbol) && in.testSupport(symbol)
}

// unusedMessage is what one unused-declaration finding says, in the reader's words:
// what the subject is, and what the analysis found about its references.
//
// The relation is what decides the second half for the three unreferenced kinds. A
// candidate found by reference counting has no reference at all; one found by
// reachability has references, every one of them from a declaration that is itself
// dead, and saying "no reference" about it would be false.
func (in *Input) unusedMessage(candidate *graph.Candidate, code string) string {
	word := in.word(candidate.ID)
	switch code {
	case testOnlyUseCode:
		return word + " is referenced only from test files and never from production code"
	case deprecatedAndUnusedCode:
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
	support := false
	for _, one := range in.Per {
		switch {
		case one.Result == nil:
		case one.Result.TestSupport[symbol.PkgPath]:
			support = true
		case loads(one.Result, symbol.PkgPath):
			return false
		}
	}
	return support
}

// loads reports whether one configuration loaded the package at path.
func loads(r *load.Result, path string) bool {
	return slices.ContainsFunc(r.Packages, func(p *packages.Package) bool { return p.PkgPath == path })
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
