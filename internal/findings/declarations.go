package findings

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// deprecatedPrefix opens the paragraph of a doc comment that marks a declaration
// deprecated, which is the convention the standard Go tools recognise.
const deprecatedPrefix = "Deprecated:"

// testOfDeadCodeMessage states, after the test's name, the rule the test-of-dead-code
// kind applies, which the Contract requires the message to say rather than leave to
// a reader.
const testOfDeadCodeMessage = " references only target symbols that are reported dead"

// UnusedExported reports an exported declaration that is not a member of a type
// and that nothing references.
func UnusedExported(in *pass) ([]draft, error) {
	return in.unusedDeclarations(vocab.UnusedExported), nil
}

// unusedUnexported reports an unexported declaration that is not a member of a
// type and that nothing references, and a constant or a type a function body
// declares that nothing in the body references.
func unusedUnexported(in *pass) ([]draft, error) {
	return append(in.unusedDeclarations(vocab.UnusedUnexported), in.unusedLocals()...), nil
}

// unusedMember reports a struct field that nothing references and whose container
// is not itself dead, because a member of a dead container falls with it and is
// reported inside its component.
func unusedMember(in *pass) ([]draft, error) {
	return in.unusedDeclarations(vocab.UnusedMember), nil
}

// testOnlyUse reports a declaration that no production file references and at
// least one test file does, which is the symbol and its tests deleted in one
// change rather than an unreferenced declaration.
func testOnlyUse(in *pass) ([]draft, error) {
	return in.unusedDeclarations(vocab.TestOnlyUse), nil
}

// deprecatedAndUnused reports a declaration carrying a deprecation marker that no
// production file references, which is the residue of a finished migration.
func deprecatedAndUnused(in *pass) ([]draft, error) {
	return in.unusedDeclarations(vocab.DeprecatedUnused), nil
}

// testOfDeadCode reports a test declaration whose set of referenced target
// declarations is non-empty and wholly dead.
//
// The subject is the sweep's own admission rather than a rule of this kind: a test
// that references one live declaration is never admitted, and an admitted test is
// in the component of the declarations it references, so a report names the test
// beside the code it exercises.
func testOfDeadCode(in *pass) ([]draft, error) {
	if in == nil || in.Sweep == nil {
		return nil, nil
	}
	var found []draft
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		if !candidate.TestOfDeadCode {
			continue
		}
		one, held := in.finding(candidate.ID, vocab.TestOfDeadCode, in.named(candidate.ID)+testOfDeadCodeMessage)
		if !held {
			continue
		}
		one.TestOnly = testOnly(candidate)
		found = append(found, one)
	}
	return found, nil
}

// unusedDeclarations returns the candidates one unused-declaration kind reports,
// which are the candidates [pass.codeOf] assigns to its code.
func (in *pass) unusedDeclarations(code vocab.Code) []draft {
	if in == nil {
		return nil
	}
	var found []draft
	for _, candidate := range in.unused[code] {
		one, held := in.finding(candidate.ID, code, in.unusedMessage(candidate, code))
		if !held {
			continue
		}
		one.TestOnly = testOnly(candidate)
		found = append(found, one)
	}
	return found
}

// unusedByCode is every candidate of the sweep by the code [pass.codeOf] assigns it,
// in the sweep's order, the classifier run once for the five kinds it decides between.
func unusedByCode(a *pass) map[vocab.Code][]*liveness.Candidate {
	if a.Sweep == nil {
		return nil
	}
	by := make(map[vocab.Code][]*liveness.Candidate)
	for i := range a.Sweep.Candidates {
		if code := a.codeOf(&a.Sweep.Candidates[i]); code != "" {
			by[code] = append(by[code], &a.Sweep.Candidates[i])
		}
	}
	return by
}

// codeOf is the one code the unused-declaration kinds report a candidate under, or
// the empty string, so their precedence is one rule. A
// deprecation outranks a test reference, which the finding's test-only field still
// records, and a test reference outranks the unreferenced kinds. A struct field is the
// member kind, so a method of a live type is an unused declaration. The populations
// another kind owns are [pass.outsideTheRule]'s, an exported declaration of a package
// nothing outside imports being the unreachable-export kind's.
func (in *pass) codeOf(candidate *liveness.Candidate) vocab.Code {
	symbol := in.symbol(candidate.ID)
	if symbol == nil || in.outsideTheRule(candidate, symbol) {
		return ""
	}
	switch {
	case candidate.UnreferencedTest && symbol.Exported && !member(symbol.Kind):
		return ""
	case candidate.UnreferencedTest:
		return in.shapeCode(symbol)
	case candidate.ProductionRefs == 0 && in.deprecated[candidate.ID]:
		return vocab.DeprecatedUnused
	case testKept(candidate):
		return vocab.TestOnlyUse
	default:
		return in.shapeCode(symbol)
	}
}

// shapeCode is the unreferenced kind a declaration's shape selects: the member kind
// for a struct field, then the kind its visibility selects.
func (in *pass) shapeCode(symbol *inventory.Symbol) vocab.Code {
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
func (in *pass) outsideTheRule(candidate *liveness.Candidate, symbol *inventory.Symbol) bool {
	switch {
	case candidate.TestOfDeadCode || symbol.Blank || (symbol.InTestFile && !candidate.UnreferencedTest):
		return true
	case in.deadParent(symbol):
		return true
	case interfaceDeclaration(symbol.Kind):
		return !in.testCalledInterfaceMethod(candidate, symbol)
	default:
		return in.readOrWriteSubject(candidate, symbol)
	}
}

// deadParent reports whether every container of a declaration is dead, so the
// containers' findings stand for it: a method of a type one configuration keeps
// is judged on its own. The pass with every mark withdrawn reports no dead parent,
// so a member is judged on its own there; so are the members of a test file's
// declaration a test reaches, a candidate of the production sweep alone.
func (in *pass) deadParent(symbol *inventory.Symbol) bool {
	if len(symbol.Parents) == 0 || in.withdrawnMarks {
		return false
	}
	for _, id := range symbol.Parents {
		parent := in.candidateOf(id)
		if parent == nil {
			return false
		}
		if container := in.symbol(id); container != nil && container.InTestFile && !parent.UnreferencedTest && !parent.TestOfDeadCode {
			return false
		}
	}
	return true
}

// unusedMessage is what one unused-declaration finding says, in the reader's words:
// what the subject is, and what the analysis found about its references.
//
// The relation is what decides the second half for the three unreferenced kinds. A
// candidate found by reference counting has no reference at all; one found by
// reachability has references, every one of them from a declaration that is itself
// dead, and saying "no reference" about it would be false.
func (in *pass) unusedMessage(candidate *liveness.Candidate, code vocab.Code) string {
	word := in.named(candidate.ID)
	switch code {
	case vocab.TestOnlyUse:
		evidenceOnly := candidate.TestRefs == candidate.TestEvidence
		switch {
		case candidate.ProductionRefs > 0 && evidenceOnly:
			return word + " is referenced only from declarations that are themselves dead and kept only by evidence in test files"
		case candidate.ProductionRefs > 0:
			return word + " is referenced only from test files and from declarations that are themselves dead"
		case evidenceOnly:
			return word + " is referenced from no file and kept only by evidence in test files"
		}
		return word + " is referenced only from test files and never from production code"
	case vocab.DeprecatedUnused:
		return "deprecated " + word + " has no production reference"
	}

	visibility := "unexported "
	if symbol := in.symbol(candidate.ID); symbol != nil && symbol.Exported {
		visibility = "exported "
	}
	if candidate.Relation == liveness.Reachability {
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
func member(kind inventory.SymbolKind) bool {
	return kind == inventory.KindField
}

// interfaceDeclaration reports whether one kind of declaration is an interface or a
// method an interface declares, which are the interface kinds' subjects.
func interfaceDeclaration(kind inventory.SymbolKind) bool {
	return kind == inventory.KindInterface || kind == inventory.KindInterfaceMethod
}

// readOrWriteSubject reports whether one candidate is a subject of the
// read-and-write kinds rather than an unreferenced declaration: a constant of an
// enumerated type, or a type parameter of a function or a method, that no reference
// of any file names. A test file's reference counts, so an enumerated constant only
// a test names stays an unreferenced declaration.
func (in *pass) readOrWriteSubject(candidate *liveness.Candidate, symbol *inventory.Symbol) bool {
	if candidate.ProductionRefs > 0 || candidate.TestRefs > 0 {
		return false
	}
	return in.enumMember(symbol) || in.typeParameterSubject(symbol)
}

// testOnly reports whether every reference to one candidate comes from a test file.
func testOnly(candidate *liveness.Candidate) bool {
	return candidate.ProductionRefs == 0 && candidate.TestRefs > 0
}

// testKept reports whether a candidate is a test-only use: only tests reference it,
// or every production reference is made by dead code and a test that stays keeps it.
func testKept(candidate *liveness.Candidate) bool {
	return testOnly(candidate) || (candidate.TestRefs > 0 && candidate.Relation == liveness.Reachability && candidate.KeptByTests)
}

// testReached reports whether production code only tests reach declares one symbol
// of the merged inventory ([roots.MatrixTests]).
func (in *pass) testReached(symbol *inventory.Symbol) bool {
	return in.Merged.Tests.Reached[symbol.Package]
}

// markDeprecated records every declaration of one file whose doc comment carries
// the deprecation paragraph.
//
// A declaration group's own comment marks every declaration in it, which is how
// the convention reads for a grouped constant or variable, and a member carries its
// own. A name the resolver does not answer for is a declaration the inventory does
// not hold, a local variable above all, and marks nothing.
func markDeprecated(file *ast.File, rs *resolve.Resolver, marked map[inventory.SymbolID]bool) {
	ast.Inspect(file, func(n ast.Node) bool {
		for _, name := range deprecatedNames(n) {
			if id, held := rs.At(name.Pos()); held {
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

// localDeclaration is one constant or type a function body declares, with the
// specification that declares it and whether the body references it outside it.
type localDeclaration struct {
	name *ast.Ident
	spec ast.Spec
	kind string
	used bool
}

// unusedLocals is the unused-unexported kind's answer about the constants and types
// function bodies declare, a function literal of a package-level variable's
// initializer included: one finding per such declaration nothing in its body
// references outside its own specification, under the enclosing declaration's
// reference.
func (in *pass) unusedLocals() []draft {
	found := in.intra.findings(in.intra.held[vocab.UnusedUnexported])
	for i := range found {
		found[i].local = true
	}
	return found
}

// localsOf records every local declaration of one walked function its body does not
// reference.
func (in *pass) localsOf(c int, w *walk, fn *walked, held *parts) error {
	symbol := in.symbol(fn.id)
	if symbol == nil {
		return nil
	}
	for _, local := range localDeclarations(fn.info, fn.root()) {
		if local.used {
			continue
		}
		at, err := w.one.Resolve.Render(local.name.Pos())
		if err != nil {
			return err
		}
		held.hold(c, w.one, local.name.Pos(), fn.id, newPart(vocab.UnusedUnexported, symbol.Ref, local.kind, local.name.Name, at,
			local.kind+" "+local.name.Name+" of "+namedSymbol(symbol)+" is never referenced in the body that declares it"))
	}
	return nil
}

// localDeclarations is every named constant and type one body declares, in source
// order, each marked used where an identifier outside its specification denotes it.
func localDeclarations(info *types.Info, root ast.Node) []*localDeclaration {
	declared := declaredLocals(info, root)
	if len(declared) == 0 {
		return nil
	}
	byObject := make(map[types.Object]*localDeclaration, len(declared))
	for _, local := range declared {
		byObject[info.Defs[local.name]] = local
	}
	ast.Inspect(root, func(n ast.Node) bool {
		name, isName := n.(*ast.Ident)
		if !isName {
			return true
		}
		local := byObject[info.Uses[name]]
		if local != nil && (name.Pos() < local.spec.Pos() || name.Pos() >= local.spec.End()) {
			local.used = true
		}
		return true
	})
	return declared
}

// declaredLocals is every named constant and type the declaration statements of
// one body declare, in source order.
func declaredLocals(info *types.Info, root ast.Node) []*localDeclaration {
	var declared []*localDeclaration
	ast.Inspect(root, func(n ast.Node) bool {
		if group := localGroup(n); group != nil {
			for _, spec := range group.Specs {
				declared = append(declared, specLocals(info, spec)...)
			}
		}
		return true
	})
	return declared
}

// localGroup is the constant or type declaration one statement makes, and nil for
// any other node.
func localGroup(n ast.Node) *ast.GenDecl {
	statement, isDecl := n.(*ast.DeclStmt)
	if !isDecl {
		return nil
	}
	group, isGroup := statement.Decl.(*ast.GenDecl)
	if !isGroup || group.Tok != token.CONST && group.Tok != token.TYPE {
		return nil
	}
	return group
}

// specLocals is every named declaration one constant or type specification makes.
func specLocals(info *types.Info, spec ast.Spec) []*localDeclaration {
	var declared []*localDeclaration
	for _, name := range specNames(spec) {
		if object := info.Defs[name]; object != nil && name.Name != blankName {
			declared = append(declared, &localDeclaration{name: name, spec: spec, kind: localKind(object)})
		}
	}
	return declared
}

func localKind(object types.Object) string {
	if _, isConst := object.(*types.Const); isConst {
		return symbolKinds[inventory.KindConst]
	}
	return symbolKinds[inventory.KindType]
}
