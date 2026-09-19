package kinds

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/packages"
)

// The codes of the read-and-write kinds.
const (
	writeOnlyCode     = "DS1301"
	enumMemberCode    = "DS1302"
	typeParameterCode = "DS1303"
)

// enumMemberSubject is the Contract's word for a constant of an enumerated type,
// which is not the word the subject vocabulary gives a constant, so this kind
// names its own subject rather than leaving the framework to.
const enumMemberSubject = "enum-member"

// WriteOnlySymbol reports a symbol the counted references store into and never
// read, under the run's own reference mode: a production mode counts no reference
// a test file made, so a read from a test file is no read and a symbol written in
// production and read only from a test is reported.
//
// The mode and the exemptions are the input's, because this is the one kind whose
// subject is a LIVE declaration: a run's mode is not recoverable from a swept
// graph, and an exemption on a declaration no relation found dead is in what the
// sweep was given rather than in what it held back.
//
// An exemption on the subject is a read, which is why the kind reads the whole set
// rather than the held-back record alone: a class fires because a mechanism the
// analysis cannot see uses the declaration, and for a variable or a field that use
// is a read of its value. A field a marshaller writes out, a template renders or a
// reflective lookup reaches is read by something no reference names.
//
// The subject is a package-level variable or a struct field the counted
// references store into and never read: at least one write, and no reference of
// any other kind, a read, a call, a type use, a conversion, an embedding and an
// assertion alike. A constant cannot be written, and a variable a function
// declares is a subject of the intra-function kinds rather than of this one.
//
// The subject is a LIVE declaration, so the sweep's candidates are not this
// kind's population and a candidate is never reported here. A write is a
// reference, so the sweep, which asks whether anything names the declaration,
// holds a written one live; this kind asks whether anything READS it, which the
// reference kinds answer and the sweep does not. A declaration the sweep did find
// dead is the subject of the unused kinds and of the dead component that carries
// it.
func WriteOnlySymbol(in *Input) ([]Finding, error) {
	exempted := make(map[graph.SymbolID]bool, len(in.Exempt))
	for _, exemption := range in.Exempt {
		exempted[exemption.ID] = true
	}
	counted := writesAndReads(in)

	var found []Finding
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		finding, reports := writeOnly(in, symbol, counted[symbol.ID], exempted)
		if reports {
			found = append(found, finding)
		}
	}
	return found, nil
}

// writeOnly is the write-only kind's answer about one declaration of the
// inventory: whether the counted references store into it and never read it, and
// the finding that says so.
func writeOnly(in *Input, symbol *graph.Symbol, u *uses, exempted map[graph.SymbolID]bool) (Finding, bool) {
	switch {
	case !writableSubject(in, symbol), exempted[symbol.ID], in.candidateOf(symbol.ID) != nil:
		return Finding{}, false
	case u == nil, len(u.writes) == 0, u.reads > 0:
		return Finding{}, false
	}
	finding, names := in.finding(symbol.ID, writeOnlyCode,
		in.word(symbol.ID)+" "+symbol.Name+" is written "+writeCount(len(u.writes))+" and never read")
	if !names {
		return Finding{}, false
	}
	// The kind counts the references made to the declaration and asks what each
	// one does, which is the reference-counting relation narrowed to reads; it
	// walks no closure from a root.
	finding.Relation = graph.ReferenceCounting
	finding.TestOnly = u.production == 0 && u.test > 0
	finding.Details.WritePositions = slices.Clone(u.writes)
	return finding, true
}

// UnusedEnumMember reports a constant of an enumerated type that nothing names.
//
// The enumerated type is the one the enum-group exemption recognises: a defined
// type whose constants an iota group declares. Every member of a type whose
// values can arrive by conversion rather than by name is retained by that
// exemption, so such a member is no candidate of the sweep and never reaches this
// kind.
//
// A member a reference names is not reported, whichever file holds the reference,
// so a member only a test names is the test-only kind's subject and not this
// one's. This kind takes precedence over the unused-exported and the
// unused-unexported kinds for a constant of an enumerated type, which report no
// member of an iota group.
func UnusedEnumMember(in *Input) ([]Finding, error) {
	members := EnumGroupMembers(in)
	referenced := referencedSymbols(in)

	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		owner, isMember := members[candidate.ID]
		if !isMember || referenced[candidate.ID] {
			continue
		}
		symbol := in.symbol(candidate.ID)
		if symbol == nil {
			continue
		}
		finding, names := in.finding(candidate.ID, enumMemberCode,
			"enumerated member "+symbol.Name+" of "+owner+" is named nowhere")
		if !names {
			continue
		}
		finding.Symbol.Kind = enumMemberSubject
		finding.Relation = candidate.Relation
		found = append(found, finding)
	}
	return found, nil
}

// UnusedTypeParameter reports a type parameter of a function or a method that no
// part of the declaration's signature and no part of its body names.
//
// A type parameter of a type declaration is never reported: a phantom parameter
// such as the one a defined integer type carries makes two instantiations
// distinct types while naming the parameter nowhere, so deleting it changes the
// program. This kind takes precedence over the unused-exported and the
// unused-unexported kinds for a type parameter, which report none.
func UnusedTypeParameter(in *Input) ([]Finding, error) {
	referenced := referencedSymbols(in)

	var found []Finding
	for i := range in.Sweep.Candidates {
		candidate := &in.Sweep.Candidates[i]
		symbol := in.symbol(candidate.ID)
		if symbol == nil || symbol.Kind != graph.KindTypeParam || referenced[candidate.ID] {
			continue
		}
		if !declaresTypeParameters(in.symbol(symbol.Parent)) {
			continue
		}
		finding, names := in.finding(candidate.ID, typeParameterCode,
			"type parameter "+symbol.Name+" is named in neither its signature nor its body")
		if !names {
			continue
		}
		finding.Relation = candidate.Relation
		found = append(found, finding)
	}
	return found, nil
}

// declaresTypeParameters reports whether a type parameter's container is a
// declaration whose parameters this kind reports, which is a function or a
// method. A type declaration's is exempt.
func declaresTypeParameters(owner *graph.Symbol) bool {
	return owner != nil && (owner.Kind == graph.KindFunc || owner.Kind == graph.KindMethod)
}

// uses is how one declaration's counted references divide for the write-only
// kind: the positions written, in reference order and once per position, how many
// references of every other kind read it, and how the two split by file.
type uses struct {
	writes     []Position
	reads      int
	production int
	test       int
}

// writesAndReads divides every reference the run's mode counts by whether it
// stores into its target. A production mode counts no reference a test file made,
// which is what leaves a declaration read only from a test reported and makes the
// written positions the production writes alone.
//
// One write position is kept once however many build configurations saw it: the
// merged reference set holds one reference per configuration it was seen in, and
// the deletion set the finding names is a set of positions in the source.
func writesAndReads(in *Input) map[graph.SymbolID]*uses {
	counted := make(map[graph.SymbolID]*uses)
	for i := range in.Merged.References {
		r := &in.Merged.References[i]
		if in.Mode.Production && r.Test {
			continue
		}
		held := counted[r.To]
		if held == nil {
			held = &uses{}
			counted[r.To] = held
		}
		if r.Test {
			held.test++
		} else {
			held.production++
		}
		if r.Kind != graph.RefWrite {
			held.reads++
			continue
		}
		if at := positionAt(r.Pos); !slices.Contains(held.writes, at) {
			held.writes = append(held.writes, at)
		}
	}
	return counted
}

// positionAt renders one reference's position. A reference is an identifier, so
// it begins and ends on the one line.
func positionAt(pos token.Position) Position {
	return Position{Path: pos.Filename, Line: pos.Line, Column: pos.Column, EndLine: pos.Line}
}

// writableSubject reports whether one declaration is a subject of the write-only
// kind: a package-level variable, or a struct field, neither declared with the
// blank identifier. A package-level variable is one whose container is the
// package, which is what keeps a variable a function declares out.
func writableSubject(in *Input, symbol *graph.Symbol) bool {
	switch symbol.Kind {
	case graph.KindVar:
		owner := in.symbol(symbol.Parent)
		return !symbol.Blank && (owner == nil || owner.Kind == graph.KindPackage)
	case graph.KindField:
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
func referencedSymbols(in *Input) map[graph.SymbolID]bool {
	named := make(map[graph.SymbolID]bool, len(in.Merged.References))
	for i := range in.Merged.References {
		named[in.Merged.References[i].To] = true
	}
	return named
}

// EnumGroupMembers answers which constants of the inventory are members of an
// enumerated type, each with the name of the type that declares it.
//
// The enumerated type is a defined type whose constants are declared in an iota
// group: a constant block in which at least one specification's value expression
// mentions iota, the specifications that repeat the previous expression included.
// Every constant of such a type is a member, including one a later block
// declares, and a constant declared with the blank identifier names none. It is
// the recognition the enum-group exemption applies, so the kind and the class
// agree on what an enumerated type is, and it is what the unused-exported and
// unused-unexported kinds read to leave such a member to the enumerated-member
// kind.
//
// A constant is a member when one build configuration declares it in such a
// group, because a group a constraint excludes from one configuration is still
// the group the source writes.
func EnumGroupMembers(in *Input) map[graph.SymbolID]string {
	members := make(map[graph.SymbolID]string)
	for i := range in.Per {
		scan := &enumScan{per: &in.Per[i], owners: make(map[string]string), members: members}
		for _, iotaGroups := range []bool{true, false} {
			scan.blocks(iotaGroups)
		}
	}
	return members
}

// enumScan accumulates the enumerated types of one build configuration and their
// members.
type enumScan struct {
	per     *Configured
	owners  map[string]string // the type's key to the name a message carries
	members map[graph.SymbolID]string
}

// blocks walks every constant block of one configuration, registering the types
// an iota group declares on the pass over those groups and keeping the constants
// of an already registered type on the pass over every other block.
func (s *enumScan) blocks(iotaGroups bool) {
	if s.per.Result == nil || s.per.Resolve == nil {
		return
	}
	for _, p := range s.per.Result.Packages {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			for _, decl := range f.Decls {
				block, isBlock := decl.(*ast.GenDecl)
				if !isBlock || block.Tok != token.CONST || mentionsIota(block) != iotaGroups {
					continue
				}
				s.constants(p, block, iotaGroups)
			}
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
	key := types.TypeString(named, nil)
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
