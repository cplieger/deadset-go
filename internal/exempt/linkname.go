package exempt

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/packages"
)

// The spellings the four mechanisms are written with.
const (
	unsafeImport     = `"unsafe"`
	cgoImport        = `"C"`
	exportDirective  = "//export "
	mainPackage      = "main"
	mainFunction     = "main"
	asmExtension     = ".s"
	asmTextDirective = "TEXT"
	asmTextSuffix    = "(SB)"
	asmFileLocal     = "<>"

	// asmSymbolPrefix is the middle dot, U+00B7, the assembler writes between a
	// package and a symbol name.
	asmSymbolPrefix = "·"
)

// The clause each mechanism records. One class covers four of them, so the class
// name alone does not say which one retained a symbol.
const (
	detailLinkname   = "named by a go:linkname directive"
	detailCgoExport  = "exported to C by an export directive"
	detailAssembly   = "named by an assembly TEXT directive"
	detailPluginMain = "exported from a plugin's main package"
)

// LinknameCgoAsmPluginDetector retains a symbol another compilation unit or the
// runtime reaches by a name the type checker never records, which is four
// mechanisms:
//
//   - A function or variable named on either side of a //go:linkname or
//     //go:linknamestd directive, in a file importing "unsafe" as the toolchain
//     requires of the directive. The local name is resolved in the declaring
//     package's scope rather than by adjacency, and the qualified name the
//     directive joins it to is resolved in the loaded package whose import path it
//     spells, because a directive in one package names a symbol of another.
//   - A function carrying an //export directive in a file importing "C". The load
//     compiles with cgo disabled, so such a file is not among the syntax this
//     walks and the mechanism retains nothing until it is; the file is recorded as
//     a declared limit of the run instead.
//   - The declaration a TEXT directive of an assembly file of the same package
//     names. The directive is a line whose first word is TEXT, followed by ·name
//     or ·name<> and then (SB), the middle dot first: a qualified form, pkg·name,
//     names a symbol of another package and retains nothing here.
//   - Every exported function and variable of a main package declaring no main
//     function. A plugin is built from a main package by a build flag the load
//     cannot see, and a main package with no main function is the honest signal of
//     one, because such a package cannot be linked as a program. A plugin resolves
//     a function or a variable by name, so no other kind is retained, and a
//     declaration of a test file is not part of the plugin.
//
// The string a plugin lookup call names is the other side of the last mechanism
// and belongs to the reflective-lookup class, whose rule is a string literal
// beside a call; nothing here reads a string literal.
func LinknameCgoAsmPluginDetector(in *Input) ([]graph.Exemption, error) {
	d := newLinkname(in)
	for _, p := range d.loaded {
		if err := d.mechanisms(p); err != nil {
			return nil, err
		}
	}
	return d.exemptions(), nil
}

// linkname accumulates one configuration's exemptions of the class.
type linkname struct {
	in     *Input
	byPath map[string][]*packages.Package // import path to the variants that spell it
	found  map[graph.Exemption]struct{}
	loaded []*packages.Package
}

// newLinkname keeps every type-checked package of one configuration, in one
// order, and indexes them by the import path a directive of another package
// spells.
func newLinkname(in *Input) *linkname {
	d := &linkname{
		in:     in,
		byPath: make(map[string][]*packages.Package),
		found:  make(map[graph.Exemption]struct{}),
		loaded: make([]*packages.Package, 0, len(in.Result.Packages)),
	}
	for _, p := range in.Result.Packages {
		if p.Types != nil && p.TypesInfo != nil {
			d.loaded = append(d.loaded, p)
		}
	}
	slices.SortFunc(d.loaded, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
	for _, p := range d.loaded {
		d.byPath[p.PkgPath] = append(d.byPath[p.PkgPath], p)
	}
	return d
}

// mechanisms runs the four mechanisms of the class over one package.
func (d *linkname) mechanisms(p *packages.Package) error {
	for _, mechanism := range []func(*packages.Package) error{
		d.linknames, d.cgoExports, d.assembly, d.plugin,
	} {
		if err := mechanism(p); err != nil {
			return err
		}
	}
	return nil
}

// exemptions returns what the four mechanisms found, ordered by site and then by
// the symbol and the clause, and once per symbol, site and clause.
func (d *linkname) exemptions() []graph.Exemption {
	found := make([]graph.Exemption, 0, len(d.found))
	for e := range d.found {
		found = append(found, e)
	}
	slices.SortFunc(found, func(a, b graph.Exemption) int {
		return cmp.Or(
			graph.ByPosition(a.Site, b.Site),
			cmp.Compare(a.ID, b.ID),
			strings.Compare(a.Detail, b.Detail),
		)
	})
	return found
}

// keep retains the declaration obj is written at, against evidence written at
// site.
func (d *linkname) keep(obj types.Object, site token.Pos, detail string) error {
	id, linkable := d.linkable(obj)
	if !linkable {
		return nil
	}
	rendered, err := d.in.Resolve.Render(site)
	if err != nil {
		return err
	}
	d.at(id, rendered, detail)
	return nil
}

// linkable returns the identifier of the declaration obj is written at, when the
// inventory holds it and it is a kind this class can retain.
//
// A function and a variable are those two kinds: they are what a linker alias, an
// assembly definition and a plugin lookup name, so a name denoting any other kind
// of declaration is not evidence of a caller outside the type checker's view.
func (d *linkname) linkable(obj types.Object) (graph.SymbolID, bool) {
	switch obj.(type) {
	case *types.Func, *types.Var:
		return d.in.Resolve.Object(obj)
	default:
		return "", false
	}
}

// at retains one symbol against a site that is already rendered, which is how an
// assembly file names one: the file is not parsed, so its directives have no
// position in the file set.
func (d *linkname) at(id graph.SymbolID, site token.Position, detail string) {
	d.found[graph.Exemption{
		ID:     id,
		Class:  string(LinknameCgoAsmPlugin),
		Site:   site,
		Detail: detail,
	}] = struct{}{}
}

// linknames retains both sides of every linkname directive of one package.
func (d *linkname) linknames(p *packages.Package) error {
	for _, f := range p.Syntax {
		if !importsPath(f, unsafeImport) {
			continue
		}
		for _, group := range f.Comments {
			if err := d.directives(p, group); err != nil {
				return err
			}
		}
	}
	return nil
}

// directives retains both sides of every linkname directive one comment group
// carries.
func (d *linkname) directives(p *packages.Package, group *ast.CommentGroup) error {
	for _, c := range group.List {
		local, qualified, ok := graph.LinknameDirective(c.Text)
		if !ok {
			continue
		}
		if err := d.keep(p.Types.Scope().Lookup(local), c.Pos(), detailLinkname); err != nil {
			return err
		}
		if err := d.remote(qualified, c.Pos()); err != nil {
			return err
		}
	}
	return nil
}

// remote retains the symbol a directive's qualified name denotes, when the name
// spells a package of this load. The last full stop separates the import path
// from the symbol, because a path element carries one of its own.
func (d *linkname) remote(qualified string, site token.Pos) error {
	at := strings.LastIndex(qualified, ".")
	if at <= 0 || at == len(qualified)-1 {
		return nil
	}
	for _, p := range d.byPath[qualified[:at]] {
		if err := d.keep(p.Types.Scope().Lookup(qualified[at+1:]), site, detailLinkname); err != nil {
			return err
		}
	}
	return nil
}

// cgoExports retains every function an export directive names in a file importing
// "C". The directive names the function it precedes, and a name that disagrees is
// refused by the toolchain rather than exporting something else.
func (d *linkname) cgoExports(p *packages.Package) error {
	for _, f := range p.Syntax {
		if !importsPath(f, cgoImport) {
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			// A method has no receiver for C to call it on.
			if !ok || fn.Recv != nil || fn.Doc == nil {
				continue
			}
			if err := d.exportedToC(p, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// exportedToC retains fn when its documentation carries the directive that gives C
// a name for it.
func (d *linkname) exportedToC(p *packages.Package, fn *ast.FuncDecl) error {
	for _, c := range fn.Doc.List {
		name, found := strings.CutPrefix(c.Text, exportDirective)
		if !found || strings.TrimSpace(name) != fn.Name.Name {
			continue
		}
		if err := d.keep(p.TypesInfo.Defs[fn.Name], c.Pos(), detailCgoExport); err != nil {
			return err
		}
	}
	return nil
}

// assembly retains every declaration a TEXT directive of one of the package's own
// assembly files names, at the file and line of the directive.
func (d *linkname) assembly(p *packages.Package) error {
	for _, path := range p.OtherFiles {
		if filepath.Ext(path) != asmExtension {
			continue
		}
		rel, err := targetRelative(d.in.Root, path)
		if err != nil {
			return err
		}
		src, err := d.in.Read(path)
		if err != nil {
			return fmt.Errorf("exempt: read %s: %w", rel, err)
		}
		for _, t := range textDirectives(src) {
			id, linkable := d.linkable(p.Types.Scope().Lookup(t.name))
			if !linkable {
				continue
			}
			d.at(id, token.Position{
				Filename: rel,
				Offset:   t.offset,
				Line:     t.line,
				Column:   t.column,
			}, detailAssembly)
		}
	}
	return nil
}

// plugin retains every exported function and variable of a main package that
// declares no main function.
func (d *linkname) plugin(p *packages.Package) error {
	scope := p.Types.Scope()
	if p.Types.Name() != mainPackage || scope.Lookup(mainFunction) != nil {
		return nil
	}
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		f := declaringFile(p, obj.Pos())
		if f == nil {
			continue
		}
		if _, test := graph.IsTestFile(d.in.Result.Fset.Position(f.FileStart).Filename); test {
			continue
		}
		if err := d.keep(obj, f.Package, detailPluginMain); err != nil {
			return err
		}
	}
	return nil
}

// targetRelative renders the path of one file the load reported whose positions
// the file set does not hold, which is what an assembly file is: the class names a
// directive in it, so the site needs the target-relative spelling every position
// carries, the solidus as separator whatever the platform, decided lexically.
//
// A file the load compiled from outside the target root is a failure of the run,
// the answer the resolver gives for a position it cannot render.
func targetRelative(root, path string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %s is outside %s", graph.ErrSource, path, root)
	}
	return filepath.ToSlash(rel), nil
}

// declaringFile returns the syntax of the file pos is written in, and nil when
// the package parsed no such file.
func declaringFile(p *packages.Package, pos token.Pos) *ast.File {
	for _, f := range p.Syntax {
		if f.FileStart <= pos && pos < f.FileEnd {
			return f
		}
	}
	return nil
}

// importsPath reports whether the file imports the given quoted path, a blank
// import included.
func importsPath(f *ast.File, quoted string) bool {
	for _, imp := range f.Imports {
		if imp.Path != nil && imp.Path.Value == quoted {
			return true
		}
	}
	return false
}

// textDirective is one TEXT directive of an assembly file: the name it defines in
// the package the file belongs to, and where the directive is written.
type textDirective struct {
	name   string
	offset int
	line   int
	column int
}

// textDirectives returns every TEXT directive one assembly file writes for its own
// package, in the order the file writes them.
func textDirectives(src []byte) []textDirective {
	var found []textDirective
	offset, line := 0, 1
	for raw := range strings.Lines(string(src)) {
		if name, at, ok := textSymbol(strings.TrimRight(raw, "\r\n")); ok {
			found = append(found, textDirective{
				name:   name,
				offset: offset + at,
				line:   line,
				column: at + 1,
			})
		}
		offset += len(raw)
		line++
	}
	return found
}

// textSymbol returns the name one line defines with a TEXT directive and the byte
// the directive starts at, or reports that the line defines none.
//
// Everything before the directive is white space, so the byte the directive starts
// at is also its column in UTF-16 code units, which is the unit a position counts
// in.
func textSymbol(line string) (name string, at int, ok bool) {
	rest := strings.TrimLeft(line, " \t")
	at = len(line) - len(rest)
	rest, ok = strings.CutPrefix(rest, asmTextDirective)
	if !ok {
		return "", 0, false
	}
	operands := strings.TrimLeft(rest, " \t")
	if len(operands) == len(rest) {
		// TEXT begins a longer word, so the line writes no directive.
		return "", 0, false
	}
	// A name carrying anything at all before the middle dot belongs to another
	// package, and a line reaching no middle dot defines nothing.
	rest, ok = strings.CutPrefix(operands, asmSymbolPrefix)
	if !ok {
		return "", 0, false
	}
	name = leadingIdentifier(rest)
	if name == "" {
		return "", 0, false
	}
	rest = strings.TrimPrefix(rest[len(name):], asmFileLocal)
	if !strings.HasPrefix(rest, asmTextSuffix) {
		return "", 0, false
	}
	return name, at, true
}

// leadingIdentifier returns the longest prefix of s that a Go declaration can be
// named, which is what a directive naming a Go declaration writes.
func leadingIdentifier(s string) string {
	for i, r := range s {
		if r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return s[:i]
	}
	return s
}
