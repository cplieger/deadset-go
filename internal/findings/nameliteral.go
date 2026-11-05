package findings

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// nameLiteralEvidence is the row of the Contract's evidence table that names the
// codes a string literal spelling a package-level declaration's name lowers.
var nameLiteralEvidence, _ = vocab.EvidenceFor("name-literal", language)

// literalSite is one string literal of a production file of the target: where it is
// written, and the package-level declarations whose own declaration node holds it, the
// specification for a name a grouped declaration declares.
type literalSite struct {
	within []inventory.SymbolID
	at     token.Position
}

// capNameLiteral lowers one finding to the class possible, and names the literal,
// where a string literal outside the subject's own declaration spells the name of a
// package-level subject, or of the defined type a package-level constant has: a tool
// reading the source may find the declaration by that name, a generator emitting a
// type's constants among them, which only a text search could rule out.
func (in *pass) capNameLiteral(found *Finding) {
	if at := in.nameLiteral(found.id, found.Code); at != nil {
		found.Class = vocab.Possible
		found.Details.NameLiteral = at
	}
}

// nameLiteral is the literal that lowers one declaration's finding under one code,
// and nil where none does.
func (in *pass) nameLiteral(id inventory.SymbolID, code vocab.Code) *Position {
	symbol := in.symbol(id)
	if !nameLiteralEvidence.Lowers(language, code) || symbol == nil || !in.packageLevel(symbol) {
		return nil
	}
	at, spelled := in.spelling(id, symbol)
	if !spelled && symbol.Kind == inventory.KindConst {
		if typed := in.symbol(in.constTypes[id]); typed != nil && in.packageLevel(typed) {
			at, spelled = in.spelling(typed.ID, typed)
		}
	}
	if !spelled {
		return nil
	}
	return &Position{Path: at.Filename, Line: at.Line, Column: at.Column, EndLine: at.Line}
}

// spelling is the first string literal spelling one declaration's name outside its
// own declaration node.
func (in *pass) spelling(id inventory.SymbolID, symbol *inventory.Symbol) (token.Position, bool) {
	for _, one := range in.literals[symbol.Name] {
		if !slices.Contains(one.within, id) {
			return one.at, true
		}
	}
	return token.Position{}, false
}

// packageLevel reports whether one declaration is declared at package level rather
// than inside a type.
func (in *pass) packageLevel(symbol *inventory.Symbol) bool {
	parent := in.container(symbol)
	return parent != nil && (parent.Kind == inventory.KindPackage || parent.Kind == inventory.KindFile)
}

// nameLiterals is every string literal of the production files of the target the
// run builds, by its value, each value's sites in position order, built on first use.
// An import path and a struct tag are no such literal: no tool finds a declaration by
// either, and counting them would let an import of a package lower a declaration of
// the same name. It returns each constant's defined type beside them.
func nameLiterals(a *pass) (literals map[string][]literalSite, constTypes map[inventory.SymbolID]inventory.SymbolID) {
	literals = make(map[string][]literalSite)
	constTypes = make(map[inventory.SymbolID]inventory.SymbolID)
	seen := make(map[inventory.Offset]bool)
	for _, typed := range a.sources {
		rs := typed.one.Resolve
		for _, decl := range typed.file.Decls {
			keepConstTypes(decl, typed.pkg.TypesInfo, rs, constTypes)
			for _, node := range declarationNodes(decl) {
				keepLiteralsOf(node, rs, seen, literals)
			}
		}
	}
	for value := range literals {
		slices.SortFunc(literals[value], func(a, b literalSite) int { return inventory.ByPosition(a.at, b.at) })
	}
	return literals, constTypes
}

// keepLiteralsOf records every string literal one declaration node holds, as written
// within the declarations that node declares, an import path and a struct tag left out.
func keepLiteralsOf(node ast.Node, rs *resolve.Resolver, seen map[inventory.Offset]bool, literals map[string][]literalSite) {
	within := declaredBy(node, rs)
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.Field:
			if n.Tag != nil {
				ast.Inspect(n.Type, func(inner ast.Node) bool { return keepLiteral(inner, rs, within, seen, literals) })
				return false
			}
		}
		return keepLiteral(n, rs, within, seen, literals)
	})
}

// keepLiteral records one node where it is a string literal of a production file
// of the target, once per written byte however many configurations build the file.
func keepLiteral(n ast.Node, rs *resolve.Resolver, within []inventory.SymbolID,
	seen map[inventory.Offset]bool, literals map[string][]literalSite,
) bool {
	lit, isLit := n.(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return true
	}
	written := rs.Positions().Offset(lit.Pos())
	consumer, at, err := rs.Site(lit.Pos())
	if err != nil || consumer != "" || at.Filename == "" || seen[written] {
		return false
	}
	if rs.Positions().InTestFile(lit.Pos()) {
		return false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	seen[written] = true
	literals[value] = append(literals[value], literalSite{at: at, within: within})
	return false
}

// declarationNodes is the own declaration node of each name one top-level declaration
// declares: the function declaration, and each specification of a grouped one.
func declarationNodes(decl ast.Decl) []ast.Node {
	group, isGroup := decl.(*ast.GenDecl)
	if !isGroup {
		return []ast.Node{decl}
	}
	nodes := make([]ast.Node, 0, len(group.Specs))
	for _, spec := range group.Specs {
		nodes = append(nodes, spec)
	}
	return nodes
}

// declaredBy is the package-level declarations one declaration node declares, which
// is what a literal inside that node is written within.
func declaredBy(node ast.Node, rs *resolve.Resolver) []inventory.SymbolID {
	var names []*ast.Ident
	switch d := node.(type) {
	case *ast.FuncDecl:
		names = append(names, d.Name)
	case *ast.TypeSpec:
		names = append(names, d.Name)
	case *ast.ValueSpec:
		names = append(names, d.Names...)
	}
	var ids []inventory.SymbolID
	for _, name := range names {
		if id, held := rs.At(name.Pos()); held {
			ids = append(ids, id)
		}
	}
	return ids
}

// keepConstTypes records, for each constant one declaration node declares, the
// defined type of the target the constant has.
func keepConstTypes(decl ast.Decl, info *types.Info, rs *resolve.Resolver, held map[inventory.SymbolID]inventory.SymbolID) {
	group, isGroup := decl.(*ast.GenDecl)
	if !isGroup || group.Tok != token.CONST {
		return
	}
	for _, spec := range group.Specs {
		values, isValues := spec.(*ast.ValueSpec)
		if !isValues {
			continue
		}
		for _, name := range values.Names {
			if typeID, typed := definedTypeOf(info.Defs[name], rs); typed {
				if id, declared := rs.At(name.Pos()); declared {
					held[id] = typeID
				}
			}
		}
	}
}

// definedTypeOf is the declaration of the defined type of the target one object has.
func definedTypeOf(object types.Object, rs *resolve.Resolver) (inventory.SymbolID, bool) {
	if object == nil {
		return "", false
	}
	named, isNamed := types.Unalias(object.Type()).(*types.Named)
	if !isNamed {
		return "", false
	}
	return rs.At(named.Obj().Pos())
}
