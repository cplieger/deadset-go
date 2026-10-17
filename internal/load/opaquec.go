package load

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"golang.org/x/tools/go/packages"
)

// The settings of the second type check, which reads the original sources of
// every package the load ignored a file of for importing "C".
const (
	// opaqueAttempts is the number of checks one import path gets: the first over
	// every file cgo alone excluded, and the second over the files the first
	// reported no error in. A third would ask the same question again, so what the
	// second still reports an error in falls back whole.
	opaqueAttempts = 2

	// wildcard is what a package pattern spells and an import path never does, so
	// an import spelling one names no package to load.
	wildcard = "..."
)

// checkOpaqueC type-checks again, from its original sources with the C
// pseudo-package opaque, every loaded package that ignored a file solely for
// importing "C", and returns the target-relative slash paths of the files that
// stayed excluded; a package it loads on demand resolves from the unit's directory
// under the unit's workspace setting. An accepted package is updated in place,
// because the load's packages hold one another in their import maps: its syntax,
// types, type information, imports and file lists change, and its identity does
// not. No C declaration becomes a symbol; only the Go half is analyzed.
func checkOpaqueC(ctx context.Context, fset *token.FileSet, target string, from unitDir, pkgs []*packages.Package, cgo map[string]bool, c *Configuration) []string {
	if len(cgo) == 0 {
		return nil
	}
	o := &opaqueCheck{
		fset:     fset,
		imports:  newCgoImporter(ctx, fset, from, c, pkgs),
		parsed:   make(map[string]*ast.File),
		excluded: make(map[string]bool, len(cgo)),
		sizes:    types.SizesFor(buildconstraint.Compiler, c.Arch),
	}
	for _, g := range cgoGroups(pkgs, cgo) {
		o.recheck(g)
	}
	return relativeSorted(target, o.excluded)
}

// opaqueCheck accumulates the second check of one configuration: the files it
// parsed, which every variant of a package shares so that one source site keeps
// one position, and the files that stayed excluded.
type opaqueCheck struct {
	fset     *token.FileSet
	imports  *cgoImporter
	parsed   map[string]*ast.File
	excluded map[string]bool
	sizes    types.Sizes
}

// cgoGroup is one import path of the load: every loaded package that type-checks
// its files, and the files cgo alone excluded from them.
//
// The packages of one import path are the package itself and the in-package test
// variant, which compile the same production files and therefore ignore the same
// files. An external test package is a different import path and ignores none of
// them, and the toolchain refuses a test file that imports "C" outright, so the
// files of a group are production files of that one package.
type cgoGroup struct {
	pkgs []*packages.Package
	own  []string
}

// cgoGroups gathers the loaded packages that ignore a file cgo alone excludes, by
// import path, in the order a path is first met, with the variants of each in the
// order the load reported them and the files of each sorted.
func cgoGroups(pkgs []*packages.Package, cgo map[string]bool) []cgoGroup {
	byPath := make(map[string]*cgoGroup)
	order := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		own := ownCgoFiles(p, cgo)
		if len(own) == 0 {
			continue
		}
		g := byPath[p.PkgPath]
		if g == nil {
			g = &cgoGroup{}
			byPath[p.PkgPath] = g
			order = append(order, p.PkgPath)
		}
		g.pkgs = append(g.pkgs, p)
		g.own = append(g.own, own...)
	}

	groups := make([]cgoGroup, 0, len(order))
	for _, path := range order {
		g := byPath[path]
		slices.Sort(g.own)
		g.own = slices.Compact(g.own)
		groups = append(groups, *g)
	}
	return groups
}

// ownCgoFiles lists the files one package ignores that cgo alone excludes.
func ownCgoFiles(p *packages.Package, cgo map[string]bool) []string {
	own := make([]string, 0, len(p.IgnoredFiles))
	for _, file := range p.IgnoredFiles {
		if cgo[file] {
			own = append(own, file)
		}
	}
	return own
}

// replacement is one package's accepted check, held until every package of its
// group has one, so that a group is updated whole or not at all.
type replacement struct {
	pkg     *packages.Package
	types   *types.Package
	info    *types.Info
	imports map[string]*packages.Package
	files   []*ast.File
	added   []string
}

// recheck checks one import path with the files cgo alone excluded from it and
// updates its packages when the check reports nothing about them.
//
// A file the check reports an error in stays excluded and the group is checked
// again without it, so one unreadable file costs its own references alone. An
// error anywhere else contradicts files the load already checked, so the whole
// group falls back and the loaded packages stand.
func (o *opaqueCheck) recheck(g cgoGroup) {
	accept, unparsed := o.parse(g.own)
	o.exclude(unparsed)
	for range opaqueAttempts {
		if len(accept) == 0 {
			return
		}
		reps, offending, outside := o.attempt(g, accept)
		switch {
		case outside:
			o.excludeAll(g.own)
			return
		case len(offending) == 0:
			apply(reps)
			return
		default:
			o.exclude(offending)
			accept = o.without(accept, offending)
		}
	}
	o.excludeAll(g.own)
}

// attempt checks every package of one group over the accepted files, and reports
// the updates it would make, the accepted files it reported an error in, and
// whether it reported one anywhere else.
func (o *opaqueCheck) attempt(g cgoGroup, accept []*ast.File) ([]replacement, map[string]bool, bool) {
	accepted := o.names(accept)
	added := slices.Sorted(maps.Keys(accepted))
	offending := make(map[string]bool)
	reps := make([]replacement, 0, len(g.pkgs))
	for _, p := range g.pkgs {
		checked, info, errs := o.check(p, accept)
		for _, e := range errs {
			at := o.fset.Position(e.Pos).Filename
			switch {
			case o.atOpaqueImport(e.Pos):
			case accepted[at]:
				offending[at] = true
			default:
				return nil, nil, true
			}
		}
		reps = append(reps, replacement{
			pkg:   p,
			types: checked,
			info:  info,
			files: slices.Concat(p.Syntax, accept),
			added: added,
		})
	}
	if len(offending) > 0 {
		return nil, offending, false
	}
	// The imports of the accepted files are resolved by the check itself, so what
	// each one named is known only now.
	imported := o.importsOf(accept)
	for i := range reps {
		reps[i].imports = imported
	}
	return reps, nil, false
}

// check type-checks one package's compiled syntax together with accept, and
// returns the package it declared, the type information it recorded and every
// error it reported.
//
// The compiled files are the trees the load already parsed and the accepted files
// are parsed once for the whole configuration, so every position the check records
// is the original file's and one source site keeps one position across the
// variants of a package.
func (o *opaqueCheck) check(p *packages.Package, accept []*ast.File) (*types.Package, *types.Info, []types.Error) {
	var errs []types.Error
	conf := &types.Config{
		GoVersion:   goVersionOf(p),
		FakeImportC: true,
		Error: func(err error) {
			if reported, ok := errors.AsType[types.Error](err); ok {
				errs = append(errs, reported)
			}
		},
		Importer: o.imports,
		Sizes:    o.sizes,
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Instances:  make(map[*ast.Ident]types.Instance),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Implicits:  make(map[ast.Node]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}
	checked := types.NewPackage(p.PkgPath, p.Name)
	// The Error function above collects every error the check reports, so the one
	// Files returns is the first of a set already held.
	_ = types.NewChecker(conf, o.fset, checked, info).Files(slices.Concat(p.Syntax, accept))
	return checked, info, errs
}

// atOpaqueImport reports whether pos is the position the mode's own diagnostic at
// an `import "C"` carries: the mode reports one at each such import spec, because
// it records no type of a C expression. The position recognises it, never the
// toolchain's text, and is also where the unused-import diagnostic of a file naming
// no C declaration sits. A renamed or dotted import of "C", which the language
// refuses, is not in this set, so a file spelling one falls back.
func (o *opaqueCheck) atOpaqueImport(pos token.Pos) bool {
	for _, f := range o.parsed {
		for _, imp := range f.Imports {
			if imp.Name == nil && imp.Path != nil && imp.Path.Value == cgoImportPath && imp.Pos() == pos {
				return true
			}
		}
	}
	return false
}

// parse reads the files of one group, once per path for the whole configuration,
// and returns the trees together with the paths that do not parse.
//
// One tree per path keeps a declaration of such a file at one position: a second
// parse into the same file set would give one source site two positions, and so
// two declarations. A file whose body does not parse falls back like any other
// file the check cannot read.
func (o *opaqueCheck) parse(paths []string) (files []*ast.File, unparsed map[string]bool) {
	files = make([]*ast.File, 0, len(paths))
	unparsed = make(map[string]bool)
	for _, path := range paths {
		if f, held := o.parsed[path]; held {
			files = append(files, f)
			continue
		}
		f, err := parser.ParseFile(o.fset, path, nil, parser.AllErrors|parser.ParseComments)
		if err != nil {
			unparsed[path] = true
			continue
		}
		o.parsed[path] = f
		files = append(files, f)
	}
	return files, unparsed
}

// names is the set of paths a tree list holds.
func (o *opaqueCheck) names(files []*ast.File) map[string]bool {
	named := make(map[string]bool, len(files))
	for _, f := range files {
		named[o.fset.Position(f.FileStart).Filename] = true
	}
	return named
}

// importsOf resolves the imports the accepted files write, so the packages they
// name are the importing package's imports the way the compiled files' are. The C
// pseudo-package is not one of them: it is declared by the mode and provides
// nothing. A path nothing provides is left out, because the file that wrote it is
// the one the check reports an error in.
func (o *opaqueCheck) importsOf(files []*ast.File) map[string]*packages.Package {
	imported := make(map[string]*packages.Package)
	for _, f := range files {
		for _, imp := range f.Imports {
			if imp.Path == nil || imp.Path.Value == cgoImportPath {
				continue
			}
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if p, held := o.imports.known[path]; held {
				imported[path] = p
			}
		}
	}
	return imported
}

// exclude keeps one set of files excluded.
func (o *opaqueCheck) exclude(paths map[string]bool) {
	for path := range paths {
		o.excluded[path] = true
	}
}

// excludeAll keeps one list of files excluded.
func (o *opaqueCheck) excludeAll(paths []string) {
	for _, path := range paths {
		o.excluded[path] = true
	}
}

// without returns the trees whose files are not in dropped, in order.
func (o *opaqueCheck) without(files []*ast.File, dropped map[string]bool) []*ast.File {
	kept := make([]*ast.File, 0, len(files))
	for _, f := range files {
		if !dropped[o.fset.Position(f.FileStart).Filename] {
			kept = append(kept, f)
		}
	}
	return kept
}

// apply updates every package of one group with the check it accepted.
func apply(reps []replacement) {
	for _, rep := range reps {
		p := rep.pkg
		p.Syntax = rep.files
		p.Types = rep.types
		p.TypesInfo = rep.info
		p.GoFiles = merged(p.GoFiles, rep.added)
		p.CompiledGoFiles = merged(p.CompiledGoFiles, rep.added)
		p.IgnoredFiles = withoutPaths(p.IgnoredFiles, rep.added)
		if p.Imports == nil {
			p.Imports = make(map[string]*packages.Package, len(rep.imports))
		}
		for path, imported := range rep.imports {
			if _, held := p.Imports[path]; !held {
				p.Imports[path] = imported
			}
		}
	}
}

// merged returns the sorted union of one file list and the files a check added.
func merged(held, added []string) []string {
	all := slices.Concat(held, added)
	slices.Sort(all)
	return slices.Compact(all)
}

// withoutPaths returns one file list without the paths a check added, in order.
func withoutPaths(held, added []string) []string {
	kept := make([]string, 0, len(held))
	for _, path := range held {
		if !slices.Contains(added, path) {
			kept = append(kept, path)
		}
	}
	return kept
}

// goVersionOf is the language version the load type-checked one package under, so
// the second check reads its files under the same rules.
func goVersionOf(p *packages.Package) string {
	if p.Types == nil {
		return ""
	}
	return p.Types.GoVersion()
}

// cgoImporter answers the second check's imports from the packages the load
// already type-checked, loading a package only a file importing "C" imports on
// demand from its metadata and checked for its declarations alone, as every
// dependency of the load is.
//
// The types.Importer interface carries no context, so the run's is held here: an
// on-demand load spawns the toolchain, and it stops when the run does.
type cgoImporter struct {
	ctx     context.Context
	fset    *token.FileSet
	known   map[string]*packages.Package
	refused map[string]bool
	c       *Configuration
	from    unitDir
}

// newCgoImporter indexes every package the load type-checked, by import path, and
// answers anything else from the unit's own directory.
//
// A package and its test variants share one import path and an import of that path
// names the package itself, so a variant is never indexed.
func newCgoImporter(ctx context.Context, fset *token.FileSet, from unitDir, c *Configuration, pkgs []*packages.Package) *cgoImporter {
	im := &cgoImporter{
		ctx:     ctx,
		fset:    fset,
		known:   make(map[string]*packages.Package),
		refused: make(map[string]bool),
		from:    from,
		c:       c,
	}
	packages.Visit(pkgs, nil, im.keep)
	return im
}

// keep indexes one package that carries complete types and no error of its own.
func (im *cgoImporter) keep(p *packages.Package) {
	if p.PkgPath == "" || p.ForTest != "" || len(p.Errors) > 0 {
		return
	}
	if p.Types == nil || !p.Types.Complete() {
		return
	}
	if _, held := im.known[p.PkgPath]; !held {
		im.known[p.PkgPath] = p
	}
}

// Import answers one import path a file importing "C" writes.
func (im *cgoImporter) Import(path string) (*types.Package, error) {
	p, err := im.resolve(path)
	if err != nil {
		return nil, err
	}
	return p.Types, nil
}

// resolve returns the loaded package one import path names, loading it on demand
// when no package of the load provides it.
//
// A path the toolchain cannot resolve is refused once and answered the same way
// afterwards, so the file that wrote it is reported at its own import spec and
// falls back to exclusion, which is a limit of that one file rather than of the
// run.
func (im *cgoImporter) resolve(path string) (*packages.Package, error) {
	if p, held := im.known[path]; held {
		return p, nil
	}
	if im.refused[path] {
		return nil, fmt.Errorf("no package provides %s", path)
	}
	if strings.Contains(path, wildcard) {
		im.refused[path] = true
		return nil, fmt.Errorf("%s is a package pattern rather than an import path", path)
	}

	cfg := &packages.Config{
		Mode:       metadataMode,
		Context:    im.ctx,
		Dir:        im.from.dir,
		Env:        loadEnv(im.c, im.from.workspace),
		BuildFlags: buildFlags(im.c.Tags),
	}
	pkgs, err := packages.Load(cfg, path)
	if err != nil {
		im.refused[path] = true
		return nil, err
	}
	if len(collect(pkgs, "")) == 0 {
		if err := typeCheckDeclarations(im.ctx, im.fset, pkgs, im.c.Arch); err != nil {
			im.refused[path] = true
			return nil, err
		}
		packages.Visit(pkgs, nil, im.keep)
	}
	if p, held := im.known[path]; held {
		return p, nil
	}
	im.refused[path] = true
	return nil, fmt.Errorf("no package provides %s", path)
}
