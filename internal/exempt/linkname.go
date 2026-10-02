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

// The spellings the two mechanisms are written with.
const (
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

// The clause each mechanism records. One class covers both, so the class name
// alone does not say which one retained a symbol.
const (
	detailAssembly   = "named by an assembly TEXT directive"
	detailPluginMain = "exported from a plugin's main package"
)

// LinknameCgoAsmPluginDetector retains a symbol another compilation unit reaches
// by a name the type checker never records, which is two mechanisms:
//
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
// A name a //go:linkname directive joins and a function an //export directive
// gives C are roots of the analysis, so neither is retained here. The string a
// plugin lookup call names is the other side of the plugin mechanism and belongs
// to the reflective-lookup class, whose rule is a string literal beside a call;
// nothing here reads a string literal.
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
	found  map[graph.Exemption]struct{}
	loaded []*packages.Package
}

// newLinkname keeps every type-checked package of one configuration, in one
// order.
func newLinkname(in *Input) *linkname {
	d := &linkname{
		in:     in,
		found:  make(map[graph.Exemption]struct{}),
		loaded: make([]*packages.Package, 0, len(in.Result.Packages)),
	}
	for _, p := range in.Result.Packages {
		if p.Types != nil && p.TypesInfo != nil {
			d.loaded = append(d.loaded, p)
		}
	}
	slices.SortFunc(d.loaded, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
	return d
}

// mechanisms runs the two mechanisms of the class over one package.
func (d *linkname) mechanisms(p *packages.Package) error {
	for _, mechanism := range []func(*packages.Package) error{d.assembly, d.plugin} {
		if err := mechanism(p); err != nil {
			return err
		}
	}
	return nil
}

// exemptions returns what the two mechanisms found, ordered by site and then by
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
// A function and a variable are those two kinds: they are what an assembly
// definition and a plugin lookup name, so a name denoting any other kind
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
