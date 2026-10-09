package kinds

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
)

// writeCascade names the declarations that deleting one write-only subject and its
// writes also deletes: a type parameter of the subject's declaring type that only
// the subject's type names, and an import whose every use in its file is inside the
// writes.
func writeCascade(in *Input, symbol *graph.Symbol, writes []Position) []string {
	return append(cascadingTypeParams(in, symbol), cascadingImports(in, writes)...)
}

// cascadingTypeParams names every type parameter of a field's declaring type that
// the field's own type expression holds every use of in that declaration, where
// the field is the one name its line declares.
func cascadingTypeParams(in *Input, symbol *graph.Symbol) []string {
	container := in.symbol(symbol.Parent)
	if symbol.Kind != graph.KindField || container == nil {
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
// statements and literal elements the writes are, read from the first variant that
// compiles each file.
func cascadingImports(in *Input, writes []Position) []string {
	at := make(map[string]map[[2]int]bool)
	for _, w := range writes {
		if at[w.Path] == nil {
			at[w.Path] = make(map[[2]int]bool)
		}
		at[w.Path][[2]int{w.Line, w.Column}] = true
	}
	var names []string
	done := make(map[string]bool)
	for typed := range in.typedFiles() {
		site, err := typed.one.Resolve.Render(typed.file.FileStart)
		if err != nil || done[site.Filename] || at[site.Filename] == nil {
			continue
		}
		done[site.Filename] = true
		nodes := writeNodes(typed.file, typed.one, at[site.Filename])
		names = append(names, importsOnlyIn(typed.file, typed.pkg.TypesInfo, nodes)...)
	}
	return names
}

// writeNodes is every statement and literal element of one file that performs one
// of the writes at the given positions, and that deleting the write deletes whole.
func writeNodes(f *ast.File, one *Configured, at map[[2]int]bool) []ast.Node {
	writes := func(e ast.Expr) bool {
		written := writtenName(e)
		if written == nil {
			return false
		}
		site, err := one.Resolve.Render(written.Pos())
		return err == nil && at[[2]int{site.Line, site.Column}]
	}
	var nodes []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if performsWrite(n, writes) {
			nodes = append(nodes, n)
		}
		return true
	})
	return nodes
}

// performsWrite reports whether one node is a statement or a literal element that
// performs a write writes accepts, and that deleting the write deletes whole.
func performsWrite(n ast.Node, writes func(ast.Expr) bool) bool {
	switch n := n.(type) {
	case *ast.AssignStmt:
		return len(n.Lhs) == 1 && writes(n.Lhs[0])
	case *ast.IncDecStmt:
		return writes(n.X)
	case *ast.KeyValueExpr:
		key, isIdent := n.Key.(*ast.Ident)
		return isIdent && writes(key)
	case *ast.ExprStmt:
		call, isCall := n.X.(*ast.CallExpr)
		return isCall && len(call.Args) > 0 && writes(call.Args[0])
	default:
		return false
	}
}

// writtenName is the identifier a store into one expression writes: the expression's
// own name, a selector's member, and the collection an index stores into.
func writtenName(e ast.Expr) *ast.Ident {
	switch t := ast.Unparen(e).(type) {
	case *ast.Ident:
		return t
	case *ast.SelectorExpr:
		return t.Sel
	case *ast.IndexExpr:
		return writtenName(t.X)
	default:
		return nil
	}
}

// importsOnlyIn names every import of one file whose uses all sit inside nodes.
func importsOnlyIn(f *ast.File, info *types.Info, nodes []ast.Node) []string {
	if len(nodes) == 0 {
		return nil
	}
	inside := make(map[*types.PkgName]int)
	for _, node := range nodes {
		countPackageUses(node, info, inside)
	}
	total := make(map[*types.PkgName]int, len(inside))
	countPackageUses(f, info, total)

	var names []string
	for _, spec := range f.Imports {
		name := info.PkgNameOf(spec)
		if name != nil && inside[name] > 0 && inside[name] == total[name] {
			names = append(names, "import "+strconv.Quote(name.Imported().Path()))
		}
	}
	return names
}

// countPackageUses counts, per imported package name, the identifiers below one node
// that use it.
func countPackageUses(node ast.Node, info *types.Info, into map[*types.PkgName]int) {
	ast.Inspect(node, func(n ast.Node) bool {
		if id, isIdent := n.(*ast.Ident); isIdent {
			if name, isPkg := info.Uses[id].(*types.PkgName); isPkg {
				into[name]++
			}
		}
		return true
	})
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
	named map[graph.SymbolID][]string
	falls map[graph.SymbolID]bool
}

// renderedSpan is the run of rendered positions one node of a file covers, its end
// excluded.
type renderedSpan struct {
	path     string
	from, to [2]int
}

// holds reports whether one rendered position is inside the renderedSpan.
func (s renderedSpan) holds(at token.Position) bool {
	p := [2]int{at.Line, at.Column}
	return at.Filename == s.path && !before(p, s.from) && before(p, s.to)
}

// before orders two line and column pairs.
func before(a, b [2]int) bool {
	return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])
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
	falls := &annotationFalls{named: make(map[graph.SymbolID][]string), falls: make(map[graph.SymbolID]bool)}
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
func writeOnlySubjects(in *Input) map[graph.SymbolID]*uses {
	exempted := make(map[graph.SymbolID]bool, len(in.Exempt))
	for ix := range in.Exempt {
		exemption := &in.Exempt[ix]
		exempted[exemption.ID] = true
	}
	counted := writesAndReads(in)
	subjects := make(map[graph.SymbolID]*uses)
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
func annotationHolders(in *Input, subjects map[graph.SymbolID]*uses, spans map[graph.SymbolID][]renderedSpan,
) (holders map[graph.SymbolID]map[graph.SymbolID]bool, outside map[graph.SymbolID]bool) {
	holders = make(map[graph.SymbolID]map[graph.SymbolID]bool)
	outside = make(map[graph.SymbolID]bool)
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
			holders[r.To] = make(map[graph.SymbolID]bool)
		}
		holders[r.To][holder] = true
	}
	return holders, outside
}

// fallsWithAnnotation reports whether one declaration is of a shape the annotation
// cascade deletes: a declaration of its own, not a member, a type parameter, which
// the type-parameter clause names, a package or a file, and one no code outside the
// analysis can name.
func fallsWithAnnotation(in *Input, symbol *graph.Symbol) bool {
	switch symbol.Kind {
	case graph.KindPackage, graph.KindFile, graph.KindField, graph.KindTypeParam, graph.KindMethod, graph.KindInterfaceMethod:
		return false
	}
	return in.ClassOf(symbol.ID) == Certain
}

// holderOf is the subject whose annotation or writes hold one reference's position.
func holderOf(spans map[graph.SymbolID][]renderedSpan, r *graph.Reference) (graph.SymbolID, bool) {
	for id, held := range spans {
		for _, s := range held {
			if s.holds(r.Pos) {
				return id, true
			}
		}
	}
	return "", false
}

// subjectSpans is, per write-only subject, the spans of its type annotation and of
// every statement and literal element that performs one of its writes, read from the
// first variant that compiles each file.
func subjectSpans(in *Input, subjects map[graph.SymbolID]*uses) map[graph.SymbolID][]renderedSpan {
	at := subjectsByPosition(in, subjects)
	spans := make(map[graph.SymbolID][]renderedSpan)
	done := make(map[string]bool)
	for typed := range in.typedFiles() {
		site, err := typed.one.Resolve.Render(typed.file.FileStart)
		if err != nil || done[site.Filename] {
			continue
		}
		done[site.Filename] = true
		annotationSpans(typed, at[site.Filename], spans)
		for id, u := range subjects {
			spans[id] = append(spans[id], writeSpans(typed, site.Filename, u)...)
		}
	}
	return spans
}

// subjectsByPosition is every subject keyed by its file, then its line and column.
func subjectsByPosition(in *Input, subjects map[graph.SymbolID]*uses) map[string]map[[2]int]graph.SymbolID {
	at := make(map[string]map[[2]int]graph.SymbolID)
	for id := range subjects {
		symbol := in.symbol(id)
		if at[symbol.Pos.Filename] == nil {
			at[symbol.Pos.Filename] = make(map[[2]int]graph.SymbolID)
		}
		at[symbol.Pos.Filename][[2]int{symbol.Pos.Line, symbol.Pos.Column}] = id
	}
	return at
}

// writeSpans is the span of every statement and literal element of one file that
// performs one of a subject's writes.
func writeSpans(typed typedFile, path string, u *uses) []renderedSpan {
	writes := make(map[[2]int]bool)
	for _, w := range u.writes {
		if w.Path == path {
			writes[[2]int{w.Line, w.Column}] = true
		}
	}
	if len(writes) == 0 {
		return nil
	}
	var spans []renderedSpan
	for _, node := range writeNodes(typed.file, typed.one, writes) {
		if s, rendered := renderSpan(typed.one, node); rendered {
			spans = append(spans, s)
		}
	}
	return spans
}

// annotationSpans adds the renderedSpan of the type annotation of every subject one file
// declares, a field or a package-level variable written with a type.
func annotationSpans(typed typedFile, declared map[[2]int]graph.SymbolID, spans map[graph.SymbolID][]renderedSpan) {
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
func annotationSpan(typed typedFile, declared map[[2]int]graph.SymbolID, spans map[graph.SymbolID][]renderedSpan,
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
			if s, rendered := renderSpan(typed.one, annotation); rendered {
				spans[id] = append(spans[id], s)
			}
		}
	}
}

// renderSpan renders the renderedSpan one node covers.
func renderSpan(one *Configured, node ast.Node) (renderedSpan, bool) {
	from, err := one.Resolve.Render(node.Pos())
	if err != nil {
		return renderedSpan{}, false
	}
	to, err := one.Resolve.Render(node.End())
	if err != nil {
		return renderedSpan{}, false
	}
	return renderedSpan{path: from.Filename, from: [2]int{from.Line, from.Column}, to: [2]int{to.Line, to.Column}}, true
}
