package findings

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// cascades is, per write-only subject, the declarations that deleting it and its
// writes also deletes: a type parameter of the subject's declaring type that only
// the subject's type names, and an import whose every use in its file is inside the
// spans the writes' deletion deletes. Each is read in one visit per configuration
// ([cascades.read]), from the first configuration whose files answer it.
type cascades struct {
	of map[inventory.SymbolID]*cascade
}

// cascade is what the configurations read so far answer for one subject.
type cascade struct {
	container *inventory.Symbol
	symbol    *inventory.Symbol
	spans     map[string][]inventory.SourceRange // by file
	done      map[string]bool                    // every file of spans read
	params    []string
	imports   []string
	decided   bool // a file declares the container
}

func newCascades(in *pass) *cascades {
	c := &cascades{of: make(map[inventory.SymbolID]*cascade, len(in.writeOnly))}
	for id, u := range in.writeOnly {
		symbol := in.symbol(id)
		if symbol == nil {
			continue
		}
		held := &cascade{symbol: symbol, spans: make(map[string][]inventory.SourceRange), done: make(map[string]bool)}
		if container := in.container(symbol); symbol.Kind == inventory.KindField && container != nil {
			held.container = container
		} else {
			held.decided = true
		}
		for _, span := range u.spans {
			held.spans[span.File] = append(held.spans[span.File], span)
		}
		c.of[id] = held
	}
	return c
}

// writeCascade names what [cascades] answers for one write-only subject.
func writeCascade(in *pass, symbol *inventory.Symbol) []string {
	held := in.cascades.of[symbol.ID]
	if held == nil {
		return nil
	}
	return slices.Concat(held.params, held.imports)
}

// read adds what one configuration's files answer for every subject.
func (c *cascades) read(one *Configured, files []typedFile) {
	if one.Resolve == nil {
		return
	}
	for _, held := range c.of {
		held.readParams(files)
		held.readImports(files)
	}
}

// readParams names every type parameter of a field's declaring type that the
// field's own type expression holds every use of in that declaration, where the
// field is the one name its line declares, read from the first file declaring the
// type.
func (held *cascade) readParams(files []typedFile) {
	for _, typed := range files {
		if held.decided {
			return
		}
		spec := typeSpecAt(typed.file, typed.one, held.container.Pos)
		if spec == nil {
			continue
		}
		held.decided = true
		if field := fieldAt(spec, typed.one, held.symbol.Pos); field != nil {
			held.params = paramsUsedOnlyIn(spec, field.Type, typed.pkg.TypesInfo)
		}
	}
}

// typeSpecAt is the generic type declaration one file writes at a rendered position.
func typeSpecAt(f *ast.File, one *Configured, at token.Position) *ast.TypeSpec {
	for _, decl := range f.Decls {
		group, isGen := decl.(*ast.GenDecl)
		if !isGen || group.Tok != token.TYPE {
			continue
		}
		for _, spec := range group.Specs {
			typeSpec, isType := spec.(*ast.TypeSpec)
			if isType && typeSpec.TypeParams != nil && renderedAt(one, typeSpec.Name.Pos(), at) {
				return typeSpec
			}
		}
	}
	return nil
}

// fieldAt is the struct field of one type declaration written at a rendered
// position, where its line declares that one name alone.
func fieldAt(spec *ast.TypeSpec, one *Configured, at token.Position) *ast.Field {
	st, isStruct := spec.Type.(*ast.StructType)
	if !isStruct {
		return nil
	}
	for _, field := range st.Fields.List {
		if len(field.Names) == 1 && renderedAt(one, field.Names[0].Pos(), at) {
			return field
		}
	}
	return nil
}

// renderedAt reports whether one position renders to a symbol's position.
func renderedAt(one *Configured, pos token.Pos, at token.Position) bool {
	site, err := one.Resolve.Render(pos)
	return err == nil && scope.SameName(site.Filename, at.Filename) && site.Line == at.Line && site.Column == at.Column
}

// paramsUsedOnlyIn names every type parameter of a declaration that some
// expression names and whose every use in the declaration sits inside within.
func paramsUsedOnlyIn(spec *ast.TypeSpec, within ast.Expr, info *types.Info) []string {
	var names []string
	for _, group := range spec.TypeParams.List {
		for _, name := range group.Names {
			declared := info.Defs[name]
			if declared == nil {
				continue
			}
			inside, total := usesOf(within, declared, info), usesOf(spec, declared, info)
			if inside > 0 && inside == total {
				names = append(names, "type parameter "+name.Name)
			}
		}
	}
	return names
}

// usesOf counts the identifiers below one node that use one object.
func usesOf(node ast.Node, object types.Object, info *types.Info) int {
	count := 0
	ast.Inspect(node, func(n ast.Node) bool {
		if id, isIdent := n.(*ast.Ident); isIdent && info.Uses[id] == object {
			count++
		}
		return true
	})
	return count
}

// readImports names every import whose uses in its file all sit inside the spans,
// read from the first variant that compiles each file. A use inside two spans counts
// twice, so an import used in nested spans is not named.
func (held *cascade) readImports(files []typedFile) {
	for _, typed := range files {
		file := typed.one.Resolve.Positions().Offset(typed.file.FileStart).File
		if held.done[file] || held.spans[file] == nil {
			continue
		}
		held.done[file] = true
		held.imports = append(held.imports, importsOnlyIn(typed, held.spans[file])...)
	}
}

// importsOnlyIn names every import of one file whose uses all sit inside spans.
func importsOnlyIn(typed typedFile, spans []inventory.SourceRange) []string {
	info := typed.pkg.TypesInfo
	inside := make(map[*types.PkgName]int)
	total := make(map[*types.PkgName]int)
	ast.Inspect(typed.file, func(n ast.Node) bool {
		if id, isIdent := n.(*ast.Ident); isIdent {
			if name, isPkg := info.Uses[id].(*types.PkgName); isPkg {
				total[name]++
				inside[name] += holding(typed.one.Resolve.Positions().Offset(id.Pos()), spans)
			}
		}
		return true
	})

	var names []string
	for _, spec := range typed.file.Imports {
		name := info.PkgNameOf(spec)
		if name != nil && inside[name] > 0 && inside[name] == total[name] {
			names = append(names, "import "+strconv.Quote(name.Imported().Path()))
		}
	}
	return names
}

// holding is the number of spans holding one byte.
func holding(at inventory.Offset, spans []inventory.SourceRange) int {
	held := 0
	for i := range spans {
		if spans[i].Holds(at) {
			held++
		}
	}
	return held
}

// cascadeClause is the clause a write-only message names the cascade in, and empty
// where the deletion removes nothing more.
func cascadeClause(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return ", and deleting it with its writes also deletes " + names[0]
	default:
		return ", and deleting it with its writes also deletes " +
			strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// annotationFalls is what the write-only kind's annotation cascade answers: per
// subject, the declarations its finding names as falling with it, and every
// declaration that falls with some subject.
type annotationFalls struct {
	named map[inventory.SymbolID][]string
	falls map[inventory.SymbolID]bool
}

// writeFalls is the declarations that fall with the write-only findings: a
// declaration of the target, certain in class and of a shape a deletion removes,
// every counted reference to which a write-only subject carries
// ([liveness.Result.Carried]), named under the innermost such subject of each. It
// withholds every kind's finding about such a declaration.
func writeFalls(in *pass) *annotationFalls {
	falls := &annotationFalls{named: make(map[inventory.SymbolID][]string), falls: make(map[inventory.SymbolID]bool)}
	if in.Merged == nil || in.Sweep == nil || len(in.Sweep.Carried) == 0 {
		return falls
	}
	subjects := in.writeOnly
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		chains := in.Sweep.Carried[symbol.ID]
		if len(chains) == 0 || subjects[symbol.ID] != nil || !fallsWithAnnotation(in, symbol) {
			continue
		}
		innermost := innermostSubjects(chains, subjects)
		if innermost == nil {
			continue
		}
		falls.falls[symbol.ID] = true
		name := in.word(symbol.ID) + " " + symbol.Name
		for _, subject := range innermost {
			falls.named[subject] = append(falls.named[subject], name)
		}
	}
	return falls
}

// innermostSubjects is, once each, the innermost write-only subject of every chain,
// and nil where a chain holds none.
func innermostSubjects(chains [][]inventory.SymbolID, subjects map[inventory.SymbolID]*uses) []inventory.SymbolID {
	var innermost []inventory.SymbolID
	for _, chain := range chains {
		at := slices.IndexFunc(chain, func(id inventory.SymbolID) bool { return subjects[id] != nil })
		if at < 0 {
			return nil
		}
		if !slices.Contains(innermost, chain[at]) {
			innermost = append(innermost, chain[at])
		}
	}
	return innermost
}

// writeOnlySubjects is every declaration the write-only kind reports, with its uses.
func writeOnlySubjects(in *pass) map[inventory.SymbolID]*uses {
	subjects := make(map[inventory.SymbolID]*uses)
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if writeOnlySubject(in, symbol, in.counted[symbol.ID], in.exempted) {
			subjects[symbol.ID] = in.counted[symbol.ID]
		}
	}
	return subjects
}

// fallsWithAnnotation reports whether one declaration is of a shape the annotation
// cascade deletes: a declaration of its own, not a member, a type parameter, which
// the type-parameter clause names, a package or a file, and one no code outside the
// analysis can name.
func fallsWithAnnotation(in *pass, symbol *inventory.Symbol) bool {
	switch symbol.Kind {
	case inventory.KindPackage, inventory.KindFile, inventory.KindField, inventory.KindTypeParam, inventory.KindMethod, inventory.KindInterfaceMethod:
		return false
	}
	return in.classOf(symbol.ID) == vocab.Certain
}
