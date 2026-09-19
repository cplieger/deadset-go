package kinds

import (
	"go/ast"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
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
		one.Relation = candidate.Relation
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
		one.Relation = candidate.Relation
		one.TestOnly = testOnly(candidate)
		found = append(found, one)
	}
	return found
}

// codeOf is the code the unused-declaration kinds report one candidate under, and
// the empty string where none of them does. It is one function because the
// Contract reports every symbol once under the most specific code, so the
// precedence between the six kinds is one rule rather than six agreeing ones.
//
// A deprecation marker is the most specific fact about a declaration nothing in
// production references, so it outranks the test-only kind, which the finding's own
// test-only field still records. A test reference outranks the three unreferenced
// kinds. Between those three, a field of a struct is the member kind and everything
// else is the exported or the unexported kind, which is what makes a method of a
// live type an unused declaration rather than a member.
//
// Six populations are not this rule's, each the subject of a kind whose claim about
// it is the more specific one.
//
// A declaration a test file writes, whose liveness a production sweep cannot judge
// because that sweep drops the only references it can have. A member whose
// container is itself dead, which falls with the container and is reported inside
// its component. An exported declaration of a package nothing outside can import,
// which is the unreachable-export kind. An interface declaration and a method an
// interface declares, which are the interface kinds': an unused interface carries
// the concrete types that implement it, and an interface method nothing invokes
// asks a maintainer rather than a mechanical deletion, so neither is an
// unreferenced declaration and the test-only fact about either travels on the
// finding's own test-only field. And a constant of an enumerated type or a type
// parameter of a function or a method that no reference names at all, which are the
// read-and-write kinds': one is an enumerated member and the other a parameter of a
// signature, and a report names each as what it is. A reference from anywhere,
// including a test file alone, takes those last two out of that population and
// leaves them to this rule, which is what makes such a constant the test-only
// kind's subject.
func (in *Input) codeOf(candidate *graph.Candidate) string {
	symbol := in.symbol(candidate.ID)
	if symbol == nil || candidate.TestOfDeadCode || testFile(symbol) {
		return ""
	}
	if symbol.Parent != "" && in.candidateOf(symbol.Parent) != nil {
		return ""
	}
	if interfaceDeclaration(symbol.Kind) || in.readOrWriteSubject(candidate, symbol) {
		return ""
	}
	switch {
	case candidate.ProductionRefs == 0 && in.deprecations()[candidate.ID]:
		return deprecatedAndUnusedCode
	case candidate.ProductionRefs == 0 && candidate.TestRefs > 0:
		return testOnlyUseCode
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
		held.enumerated = EnumGroupMembers(in)
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
