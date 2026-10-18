package kinds

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// writeCascade names the declarations that deleting one write-only subject and its
// writes also deletes: a type parameter of the subject's declaring type that only
// the subject's type names, and an import whose every use in its file is inside the
// spans the writes' deletion deletes.
func writeCascade(in *Input, symbol *inventory.Symbol, spans []inventory.SourceRange) []string {
	return append(cascadingTypeParams(in, symbol), cascadingImports(in, spans)...)
}

// cascadingTypeParams names every type parameter of a field's declaring type that
// the field's own type expression holds every use of in that declaration, where
// the field is the one name its line declares.
func cascadingTypeParams(in *Input, symbol *inventory.Symbol) []string {
	container := in.symbol(symbol.Parent)
	if symbol.Kind != inventory.KindField || container == nil {
		return nil
	}
	for typed := range in.typedFiles() {
		spec := typeSpecAt(typed.file, typed.one, container.Pos)
		if spec == nil {
			continue
		}
		if field := fieldAt(spec, typed.one, symbol.Pos); field != nil {
			return paramsUsedOnlyIn(spec, field.Type, typed.pkg.TypesInfo)
		}
		return nil
	}
	return nil
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
	return err == nil && site.Filename == at.Filename && site.Line == at.Line && site.Column == at.Column
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

// cascadingImports names every import whose uses in its file all sit inside the
// spans, read from the first variant that compiles each file. A use inside two
// spans counts twice, so an import used in nested spans is not named.
func cascadingImports(in *Input, spans []inventory.SourceRange) []string {
	at := make(map[string][]inventory.SourceRange)
	for _, s := range spans {
		at[s.File] = append(at[s.File], s)
	}
	var names []string
	done := make(map[string]bool)
	for typed := range in.typedFiles() {
		file := typed.one.Resolve.Positions().Offset(typed.file.FileStart).File
		if done[file] || at[file] == nil {
			continue
		}
		done[file] = true
		names = append(names, importsOnlyIn(typed, at[file])...)
	}
	return names
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

// annotationCascade is the declarations that fall with the write-only findings: a
// declaration of the target, certain in class, whose every counted use is in the
// type annotation of a write-only subject or in a write such a finding names. It is
// computed on first use and then held, because every emitter reads it to withhold a
// finding about such a declaration.
func (in *Input) annotationCascade() *annotationFalls {
	held := in.index()
	if held.falling == nil {
		held.falling = computeAnnotationCascade(in)
	}
	return held.falling
}

// computeAnnotationCascade answers annotationCascade over the whole run.
func computeAnnotationCascade(in *Input) *annotationFalls {
	falls := &annotationFalls{named: make(map[inventory.SymbolID][]string), falls: make(map[inventory.SymbolID]bool)}
	if in.Merged == nil {
		return falls
	}
	subjects := writeOnlySubjects(in)
	if len(subjects) == 0 {
		return falls
	}
	holders, outside := annotationHolders(in, subjects, subjectSpans(in, subjects))
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if outside[symbol.ID] || len(holders[symbol.ID]) == 0 || !fallsWithAnnotation(in, symbol) {
			continue
		}
		falls.falls[symbol.ID] = true
		name := in.word(symbol.ID) + " " + symbol.Name
		for holder := range holders[symbol.ID] {
			falls.named[holder] = append(falls.named[holder], name)
		}
	}
	return falls
}

// writeOnlySubjects is every declaration the write-only kind reports, with its uses.
func writeOnlySubjects(in *Input) map[inventory.SymbolID]*uses {
	exempted := make(map[inventory.SymbolID]bool, len(in.Exempt))
	for ix := range in.Exempt {
		exemption := &in.Exempt[ix]
		exempted[exemption.ID] = true
	}
	counted := writesAndReads(in)
	subjects := make(map[inventory.SymbolID]*uses)
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if writeOnlySubject(in, symbol, counted[symbol.ID], exempted) {
			subjects[symbol.ID] = counted[symbol.ID]
		}
	}
	return subjects
}

// annotationHolders is, per declaration a counted reference outside the subjects
// names, the subjects whose annotation or writes hold those references, and the
// declarations some such reference outside every span names.
func annotationHolders(in *Input, subjects map[inventory.SymbolID]*uses, spans map[inventory.SymbolID][]inventory.SourceRange,
) (holders map[inventory.SymbolID]map[inventory.SymbolID]bool, outside map[inventory.SymbolID]bool) {
	holders = make(map[inventory.SymbolID]map[inventory.SymbolID]bool)
	outside = make(map[inventory.SymbolID]bool)
	for i := range in.Merged.References {
		r := &in.Merged.References[i]
		if in.uncounted(r) || subjects[r.To] != nil || outside[r.To] {
			continue
		}
		holder, inside := holderOf(spans, r)
		if !inside {
			outside[r.To] = true
			continue
		}
		if holders[r.To] == nil {
			holders[r.To] = make(map[inventory.SymbolID]bool)
		}
		holders[r.To][holder] = true
	}
	return holders, outside
}

// fallsWithAnnotation reports whether one declaration is of a shape the annotation
// cascade deletes: a declaration of its own, not a member, a type parameter, which
// the type-parameter clause names, a package or a file, and one no code outside the
// analysis can name.
func fallsWithAnnotation(in *Input, symbol *inventory.Symbol) bool {
	switch symbol.Kind {
	case inventory.KindPackage, inventory.KindFile, inventory.KindField, inventory.KindTypeParam, inventory.KindMethod, inventory.KindInterfaceMethod:
		return false
	}
	return in.classOf(symbol.ID) == vocab.Certain
}

// holderOf is the subject whose annotation or writes hold one reference's position.
func holderOf(spans map[inventory.SymbolID][]inventory.SourceRange, r *facts.Reference) (inventory.SymbolID, bool) {
	for id, held := range spans {
		for i := range held {
			if held[i].Holds(r.Site) {
				return id, true
			}
		}
	}
	return "", false
}

// subjectSpans is, per write-only subject, the spans of its type annotation, read
// from the first variant that compiles each file, and of every statement and
// literal element its writes' deletion deletes.
func subjectSpans(in *Input, subjects map[inventory.SymbolID]*uses) map[inventory.SymbolID][]inventory.SourceRange {
	at := subjectsByPosition(in, subjects)
	spans := make(map[inventory.SymbolID][]inventory.SourceRange)
	done := make(map[string]bool)
	for typed := range in.typedFiles() {
		positions := typed.one.Resolve.Positions()
		file := positions.Relative(positions.Fset.PositionFor(typed.file.FileStart, false).Filename)
		if done[file] {
			continue
		}
		done[file] = true
		annotationSpans(typed, at[file], spans)
	}
	for id, u := range subjects {
		spans[id] = append(spans[id], u.spans...)
	}
	return spans
}

// subjectsByPosition is every subject keyed by its file, then its line and column.
func subjectsByPosition(in *Input, subjects map[inventory.SymbolID]*uses) map[string]map[[2]int]inventory.SymbolID {
	at := make(map[string]map[[2]int]inventory.SymbolID)
	for id := range subjects {
		symbol := in.symbol(id)
		if at[symbol.Pos.Filename] == nil {
			at[symbol.Pos.Filename] = make(map[[2]int]inventory.SymbolID)
		}
		at[symbol.Pos.Filename][[2]int{symbol.Pos.Line, symbol.Pos.Column}] = id
	}
	return at
}

// annotationSpans adds the inventory.SourceRange of the type annotation of every subject one file
// declares, a field or a package-level variable written with a type.
func annotationSpans(typed typedFile, declared map[[2]int]inventory.SymbolID, spans map[inventory.SymbolID][]inventory.SourceRange) {
	if len(declared) == 0 {
		return
	}
	ast.Inspect(typed.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			annotationSpan(typed, declared, spans, n.Names, n.Type)
		case *ast.ValueSpec:
			annotationSpan(typed, declared, spans, n.Names, n.Type)
		}
		return true
	})
}

// annotationSpan adds the span of one annotation to every subject among the names it
// types.
func annotationSpan(typed typedFile, declared map[[2]int]inventory.SymbolID, spans map[inventory.SymbolID][]inventory.SourceRange,
	names []*ast.Ident, annotation ast.Expr,
) {
	if annotation == nil {
		return
	}
	for _, name := range names {
		site, err := typed.one.Resolve.Render(name.Pos())
		if err != nil {
			continue
		}
		if id, isSubject := declared[[2]int{site.Line, site.Column}]; isSubject {
			spans[id] = append(spans[id], typed.one.Resolve.Positions().Range(annotation))
		}
	}
}
