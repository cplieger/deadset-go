package findings

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
)

// enumMemberSubject is the Contract's word for a constant of an enumerated type,
// which is not the word the subject vocabulary gives a constant, so this kind
// names its own subject rather than leaving the framework to.
const enumMemberSubject = "enum-member"

// writeOnlySymbol reports a package-level variable or a struct field the counted
// references store into and never read: at least one write and no reference of any
// other kind, under the run's mode, so a read only a test file makes is no read in
// a production run. The subject is a LIVE declaration (a write keeps it live in the
// sweep), so the sweep's candidates are never reported here, and the mode and the
// exemptions are the input's: an exemption on the subject is a read by a mechanism
// no reference names, such as a marshaller, a template or a reflective lookup.
func writeOnlySymbol(in *pass) ([]draft, error) {
	if in.Merged == nil {
		return nil, nil
	}
	var found []draft
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if u := in.writeOnly[symbol.ID]; u != nil {
			if finding, reports := writeOnly(in, symbol, u); reports {
				found = append(found, finding)
			}
		}
	}
	return found, nil
}

// writeOnly is the finding about one subject of the write-only kind.
func writeOnly(in *pass, symbol *inventory.Symbol, u *uses) (draft, bool) {
	read := " and never read"
	switch {
	case u.deadReads > 0:
		read = " and read only from declarations nothing references"
	case u.testReads > 0:
		read = " and read only from test files"
	}
	cascade := append(writeCascade(in, symbol, u.spans), in.falling.named[symbol.ID]...)
	finding, names := in.finding(symbol.ID, vocab.WriteOnlySymbol, in.word(symbol.ID)+" "+symbol.Name+
		" is written "+writeCount(len(u.writes))+read+cascadeClause(cascade))
	if !names {
		return draft{}, false
	}
	finding.TestOnly = u.production == 0 && u.test > 0
	finding.Details.WritePositions = slices.Clone(u.writes)
	finding.testRead = u.testReads > 0
	return finding, true
}

// WriteOnlyRetained is every exemption on a declaration the counted references store
// into and never read: the exemption is a read, so it holds back the write-only
// finding about a declaration the sweep judges live and lists nowhere else. One
// symbol, class and detail is one record.
func WriteOnlyRetained(in *Input) []exempt.Exemption {
	type fact struct {
		id            inventory.SymbolID
		class, detail string
	}
	a := classesOf(in)
	counted := writesAndReads(a)
	seen := make(map[fact]bool)
	var held []exempt.Exemption
	for ix := range in.Exempt {
		exemption := &in.Exempt[ix]
		symbol := a.symbol(exemption.ID)
		one := fact{id: exemption.ID, class: exemption.Class, detail: exemption.Detail}
		if symbol == nil || seen[one] || !writeOnlySubject(a, symbol, counted[symbol.ID], nil) {
			continue
		}
		seen[one] = true
		held = append(held, *exemption)
	}
	return held
}

// writeOnlySubject reports whether the counted references store into one
// declaration of the inventory and never read it, where a read from a declaration
// nothing references leaves it a subject only beside a write from one something does.
func writeOnlySubject(in *pass, symbol *inventory.Symbol, u *uses, exempted map[inventory.SymbolID]bool) bool {
	switch {
	case !writableSubject(in, symbol), exempted[symbol.ID], in.candidateOf(symbol.ID) != nil:
		return false
	case u == nil, len(u.writes) == 0, u.reads > 0, u.deadReads > 0 && u.liveWrites == 0:
		return false
	}
	return true
}

// unusedEnumMember reports a constant of an enumerated type (the enum-group
// exemption's: a defined type an iota group declares constants of) that nothing
// names, from any file. A member of a type whose values can arrive by conversion is
// retained by that exemption and never reaches this kind, and one only a test's
// evidence retains is the unused-declaration kinds' ([pass.readOrWriteSubject]),
// which report no other member of an iota group.
func unusedEnumMember(in *pass) ([]draft, error) {
	referenced := in.referenced

	var found []draft
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || !in.enumMember(symbol) || !in.readOrWriteSubject(candidate, symbol) || referenced[candidate.ID] {
			continue
		}
		finding, names := in.finding(candidate.ID, vocab.UnusedEnumMember,
			"enumerated member "+symbol.Name+" of "+in.enumerated[candidate.ID]+" is named nowhere")
		if !names {
			continue
		}
		finding.Symbol.Kind = enumMemberSubject
		found = append(found, finding)
	}
	return found, nil
}

// unusedTypeParameter reports a type parameter of a function or a method that
// neither the signature nor the body names. A type declaration's parameter is never
// reported: a phantom parameter makes two instantiations distinct types while
// naming it nowhere. One of a declaration the sweep judged dead falls with it. It
// takes precedence over the unused-exported and unused-unexported kinds.
func unusedTypeParameter(in *pass) ([]draft, error) {
	referenced := in.referenced

	var found []draft
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || !in.typeParameterSubject(symbol) || referenced[candidate.ID] || in.deadParent(symbol) {
			continue
		}
		finding, names := in.finding(candidate.ID, vocab.UnusedTypeParameter,
			"type parameter "+symbol.Name+" is named in neither its signature nor its body")
		if !names {
			continue
		}
		found = append(found, finding)
	}
	return found, nil
}

// enumMember reports whether one declaration is a constant of an enumerated type,
// the enum-member kind's subject.
func (in *pass) enumMember(symbol *inventory.Symbol) bool {
	_, held := in.enumerated[symbol.ID]
	return held
}

// typeParameterSubject reports whether one declaration is a type parameter of a
// declaration whose parameters the type-parameter kind reports.
func (in *pass) typeParameterSubject(symbol *inventory.Symbol) bool {
	return symbol.Kind == inventory.KindTypeParam && declaresTypeParameters(in.container(symbol))
}

// declaresTypeParameters reports whether a type parameter's container is a
// declaration whose parameters this kind reports, which is a function or a
// method. A type declaration's is exempt.
func declaresTypeParameters(owner *inventory.Symbol) bool {
	return owner != nil && (owner.Kind == inventory.KindFunc || owner.Kind == inventory.KindMethod)
}

// uses is how one declaration's counted references divide for the write-only
// kind: the positions written, in reference order and once per position, the
// statement or element each of those writes' deletion deletes, how many
// references of every other kind read it, and how the two split by file. testReads
// counts the reads of test files a production mode does not count.
type uses struct {
	writes     []Position
	sites      []inventory.Offset // where each of writes is written
	spans      []inventory.SourceRange
	reads      int
	production int
	test       int
	testReads  int
	deadReads  int
	liveWrites int
}

// writesAndReads divides every reference the run's mode counts by whether it
// stores into its target. A production mode counts no reference a test file made,
// which is what leaves a declaration read only from a test reported and makes the
// written positions the production writes alone.
//
// One write is kept once by the byte that writes it, however many build
// configurations saw it or whichever line directive renders it at another's
// position: the deletion set the finding names is a set of writes in the source.
func writesAndReads(in *pass) map[inventory.SymbolID]*uses {
	counted := make(map[inventory.SymbolID]*uses)
	for i := range in.Merged.References {
		r := &in.Merged.References[i]
		held := counted[r.To]
		if held == nil {
			held = &uses{}
			counted[r.To] = held
		}
		held.count(in, r)
	}
	return counted
}

// count adds one reference the run's mode classifies.
func (u *uses) count(in *pass, r *facts.Reference) {
	if in.Mode.Uncounted(r) {
		u.testReads += readCount(r)
		return
	}
	if in.Mode.TestReference(r) {
		u.test++
	} else {
		u.production++
	}
	// A loaded consumer's write counts as a read, because deleting the
	// subject and its writes would break code the target does not own.
	switch {
	case r.Kind != facts.RefWrite && in.deadReader(r):
		u.deadReads++
		return
	case r.Kind != facts.RefWrite || r.Build != load.TargetBuild:
		u.reads++
		return
	}
	if !in.deadReader(r) {
		u.liveWrites++
	}
	if !slices.Contains(u.sites, r.Site) {
		u.sites = append(u.sites, r.Site)
		u.writes = append(u.writes, positionAt(r.Pos))
		if r.Span != (inventory.SourceRange{}) {
			u.spans = append(u.spans, r.Span)
		}
	}
}

// deadReader reports whether one reference comes from a declaration of the target
// that nothing references, and that is dead at the class certain: the finding's
// relation is reference counting, under which that declaration is dead, so its
// read is no read. A reader dead at a weaker class may be live, so its read counts.
func (in *pass) deadReader(r *facts.Reference) bool {
	if r.Build != load.TargetBuild || r.From == "" || r.From == r.To {
		return false
	}
	reader := in.candidateOf(r.From)
	return reader != nil && reader.Relation == liveness.ReferenceCounting && in.classOf(r.From) == vocab.Certain
}

// readCount is one for a reference that reads its target and zero for a store.
func readCount(r *facts.Reference) int {
	if r.Kind == facts.RefWrite {
		return 0
	}
	return 1
}

// writableSubject reports whether one declaration is a subject of the write-only
// kind: a package-level variable, or a struct field, neither declared with the
// blank identifier. A package-level variable is one whose container is the
// package, which is what keeps a variable a function declares out.
func writableSubject(in *pass, symbol *inventory.Symbol) bool {
	switch symbol.Kind {
	case inventory.KindVar:
		owner := in.container(symbol)
		return !symbol.Blank && (owner == nil || owner.Kind == inventory.KindPackage)
	case inventory.KindField:
		return !symbol.Blank
	default:
		return false
	}
}

// writeCount spells how many positions write one declaration.
func writeCount(writes int) string {
	if writes == 1 {
		return "once"
	}
	return "at " + strconv.Itoa(writes) + " positions"
}

// referencedSymbols reports, per declaration, whether any reference of any build
// configuration and of any file names it.
func referencedSymbols(in *pass) map[inventory.SymbolID]bool {
	named := make(map[inventory.SymbolID]bool, len(in.Merged.References))
	for i := range in.Merged.References {
		named[in.Merged.References[i].To] = true
	}
	return named
}

// enumGroupMembers is every constant of an enumerated type, with the name of the
// type: a defined type with constants in an iota group (a constant block one of
// whose value expressions, repeated ones included, mentions iota). Every constant of
// such a type is a member, one a later block declares included, a blank one none;
// one configuration declaring it in such a group is enough. It is the enum-group
// exemption's recognition, so the kinds and the class agree on it.
func enumGroupMembers(in *pass) map[inventory.SymbolID]string {
	members := make(map[inventory.SymbolID]string)
	for c := range in.Per {
		scan := &enumScan{in: in, config: c, per: &in.Per[c], owners: make(map[string]string), members: members}
		for _, iotaGroups := range []bool{true, false} {
			scan.blocks(iotaGroups)
		}
	}
	return members
}

// enumScan accumulates the enumerated types of one build configuration and their
// members.
type enumScan struct {
	in      *pass
	per     *Configured
	owners  map[string]string // the type's key to the name a message carries
	members map[inventory.SymbolID]string
	config  int
}

// blocks walks every constant block of one configuration, registering the types
// an iota group declares on the pass over those groups and keeping the constants
// of an already registered type on the pass over every other block.
func (s *enumScan) blocks(iotaGroups bool) {
	for _, typed := range s.in.sources {
		if typed.config != s.config {
			continue
		}
		for _, decl := range typed.file.Decls {
			block, isBlock := decl.(*ast.GenDecl)
			if !isBlock || block.Tok != token.CONST || mentionsIota(block) != iotaGroups {
				continue
			}
			s.constants(typed.pkg, block, iotaGroups)
		}
	}
}

// constants keeps every constant of one block whose defined type this scan holds,
// and registers the type a block under an iota group declares.
func (s *enumScan) constants(p *packages.Package, block *ast.GenDecl, register bool) {
	for _, spec := range block.Specs {
		value, isValue := spec.(*ast.ValueSpec)
		if !isValue {
			continue
		}
		for _, name := range value.Names {
			s.constant(p, name, register)
		}
	}
}

// constant keeps one declared constant under the defined type it carries.
func (s *enumScan) constant(p *packages.Package, name *ast.Ident, register bool) {
	constant, isConst := p.TypesInfo.Defs[name].(*types.Const)
	if !isConst || name.Name == "_" {
		return
	}
	named, isNamed := constant.Type().(*types.Named)
	if !isNamed {
		return
	}
	key := s.per.Resolve.TypeKey(named)
	owner, held := s.owners[key]
	if !held {
		if !register {
			return
		}
		owner = named.Obj().Name()
		s.owners[key] = owner
	}
	if id, inInventory := s.per.Resolve.Object(constant); inInventory {
		s.members[id] = owner
	}
}

// mentionsIota reports whether one constant block is an iota group, which is one
// specification naming iota anywhere in a value expression. A specification
// carrying no expression repeats the previous one, so a block with one such
// specification is a group throughout.
func mentionsIota(block *ast.GenDecl) bool {
	found := false
	for _, spec := range block.Specs {
		value, isValue := spec.(*ast.ValueSpec)
		if !isValue {
			continue
		}
		for _, expr := range value.Values {
			ast.Inspect(expr, func(n ast.Node) bool {
				if id, isIdent := n.(*ast.Ident); isIdent && id.Name == "iota" {
					found = true
				}
				return !found
			})
		}
	}
	return found
}
