package exempt

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
)

// The spellings the plugin mechanism is written with.
const (
	mainPackage  = "main"
	mainFunction = "main"
)

// The clause each mechanism records. One class covers them, so the class name
// alone does not say which one retained a symbol.
const (
	detailNonGoPackage = "declared in a package that holds a source in another language than Go"
	detailNonGoName    = "named by a source in another language than Go"
	detailNonGoUnread  = "named by an entry of the tree the analysis cannot read, which counts as a source in another language than Go naming every function and variable"
	detailPluginMain   = "exported from a plugin's main package"
)

// LinknameCgoAsmPluginDetector retains a symbol another compilation unit reaches
// by a name the type checker never records: every declaration of a package holding
// a non-Go source and every function or variable such a source of the tree names
// (rule N, [nonGoSources]), and every exported function and variable of a main
// package declaring no main function, which is how a plugin is built. A
// //go:linkname or //export name is a root, not an exemption, and a plugin lookup
// string belongs to reflective-lookup.
func LinknameCgoAsmPluginDetector(in *Program) ([]Exemption, error) {
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
	in     *Program
	found  map[linknameFact]struct{}
	loaded []*packages.Package
}

// linknameFact is one retention the class records: the symbol, the rendered site of
// the evidence and the clause.
type linknameFact struct {
	id     inventory.SymbolID
	detail string
	file   string // [Exemption.File]
	site   token.Position
}

// newLinkname keeps every type-checked package of one configuration, in one
// order.
func newLinkname(in *Program) *linkname {
	d := &linkname{
		in:     in,
		found:  make(map[linknameFact]struct{}),
		loaded: make([]*packages.Package, 0, len(in.Result.Packages)),
	}
	d.loaded = append(d.loaded, slices.Collect(in.Facts.Typed())...)
	return d
}

// mechanisms runs the mechanisms of the class over one package.
func (d *linkname) mechanisms(p *packages.Package) error {
	for _, mechanism := range []func(*packages.Package) error{d.nonGo, d.plugin} {
		if err := mechanism(p); err != nil {
			return err
		}
	}
	return nil
}

// exemptions returns what the mechanisms found, ordered by site and then by
// the symbol and the clause, and once per symbol, site and clause.
func (d *linkname) exemptions() []Exemption {
	found := make([]Exemption, 0, len(d.found))
	for f := range d.found {
		found = append(found, Exemption{ID: f.id, Class: string(vocab.LinknameCgoAsmPlugin), File: f.file, Site: f.site, Detail: f.detail})
	}
	slices.SortFunc(found, func(a, b Exemption) int {
		return cmp.Or(
			inventory.ByPosition(a.Site, b.Site),
			strings.Compare(a.File, b.File),
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
	d.found[linknameFact{id: id, site: rendered, file: d.in.Resolve.File(site), detail: detail}] = struct{}{}
	return nil
}

// linkable returns the identifier of the declaration obj is written at, when the
// inventory holds it and it is a kind this class can retain.
//
// A function and a variable are those two kinds: they are what a plugin lookup
// and a symbol of another compilation unit name, so a name denoting any other
// kind of declaration is not evidence of a caller outside the type checker's
// view.
func (d *linkname) linkable(obj types.Object) (inventory.SymbolID, bool) {
	switch obj.(type) {
	case *types.Func, *types.Var:
		return d.in.Resolve.Object(obj)
	default:
		return "", false
	}
}

// at retains one symbol against a site that is already rendered, which is how a
// non-Go source is named: the file is not parsed, so it has no position in the
// file set.
func (d *linkname) at(id inventory.SymbolID, site token.Position, detail string) {
	d.found[linknameFact{id: id, site: site, file: site.Filename, detail: detail}] = struct{}{}
}

// nonGo applies rule N to one package of the target: every declaration of a
// package holding a non-Go source, at that source's first line, and otherwise
// every function and variable of it whose name a non-Go source of the tree
// writes, at that file's first line.
func (d *linkname) nonGo(p *packages.Package) error {
	if p.TypesInfo == nil || !d.in.Resolve.Inside(packageStart(p)) {
		return nil
	}
	rules := d.in.nonGo()
	if file := rules.evidence(p); file != "" {
		return d.nonGoPackage(p, file)
	}
	declared := p.Types.Scope()
	for _, name := range declared.Names() {
		if err := d.nonGoName(rules, declared.Lookup(name)); err != nil {
			return err
		}
	}
	return nil
}

// nonGoPackage retains every declaration of a package holding a non-Go source, at
// the first line of that source.
func (d *linkname) nonGoPackage(p *packages.Package, file string) error {
	site, err := d.firstLine(file)
	if err != nil {
		return err
	}
	for _, obj := range p.TypesInfo.Defs {
		if id, held := declared(d.in, obj); held {
			d.at(id, site, detailNonGoPackage)
		}
	}
	return nil
}

// nonGoName retains one package-level function or variable a non-Go source of the
// tree writes the name of, at the first line of the first such file, or that an
// entry the search could not read may write, at that entry.
func (d *linkname) nonGoName(rules *nonGoSources, obj types.Object) error {
	id, linkable := d.linkable(obj)
	if !linkable {
		return nil
	}
	file := rules.namer(obj.Name())
	if file == "" {
		return nil
	}
	site, err := d.firstLine(file)
	if err != nil {
		return err
	}
	detail := detailNonGoName
	if rules.tokens[obj.Name()] == "" {
		detail = detailNonGoUnread
	}
	d.at(id, site, detail)
	return nil
}

// firstLine is the site rule N records against one entry of the tree, a file it
// read or an entry it could not read: its first line, rendered against the target
// root.
func (d *linkname) firstLine(path string) (token.Position, error) {
	rel, err := targetRelative(d.in.Root, path)
	if err != nil {
		return token.Position{}, err
	}
	return token.Position{Filename: rel, Line: 1, Column: 1}, nil
}

// declared is the inventory symbol one definition of a package makes, when it is a
// constant, a type, a variable or struct field, or a function or method.
func declared(in *Program, obj types.Object) (inventory.SymbolID, bool) {
	switch obj.(type) {
	case *types.Const, *types.TypeName, *types.Var, *types.Func:
		return in.Resolve.Object(obj)
	default:
		return "", false
	}
}

// plugin retains every exported function and variable of a main package that
// declares no main function.
func (d *linkname) plugin(p *packages.Package) error {
	declared := p.Types.Scope()
	if p.Types.Name() != mainPackage || declared.Lookup(mainFunction) != nil {
		return nil
	}
	for _, name := range declared.Names() {
		obj := declared.Lookup(name)
		if !obj.Exported() {
			continue
		}
		f := declaringFile(p, obj.Pos())
		if f == nil {
			continue
		}
		if d.in.Resolve.Positions().InTestFile(f.FileStart) {
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
// carries, the solidus as separator whatever the platform, placed where both
// resolve ([scope.Root.PlaceEntry]).
//
// A file the load compiled from outside the target root is a failure of the run,
// the answer the resolver gives for a position it cannot render.
func targetRelative(root, path string) (string, error) {
	rel, inside := scope.RootOf(root).PlaceEntry(path)
	if !inside {
		return "", fmt.Errorf("%w: %s is outside %s", inventory.ErrSource, path, root)
	}
	return rel, nil
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
