package inventory

import (
	"cmp"
	"go/ast"
	"go/token"
	"iter"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Variants holds every loaded package that shares one import path and one
// directory: the package itself and the test variants that type-check its files
// again. Two modules of the target can declare one module path, so the import path
// alone names two packages.
type Variants struct {
	Anchor   *ast.File
	PkgPath  string
	Dir      string
	Packages []*packages.Package
}

// Groups gathers the loaded packages by import path and directory, and orders the
// groups and the variants inside each, which is the order its callers, [Packages]
// and [Sources] among them, walk a configuration in. A package the type checker did
// not check and a package with no syntax tree carry no declaration and are left out.
// The anchor is the file whose name sorts first across every variant, so the
// position of the package itself does not depend on which variant reached it.
func Groups(pkgs []*packages.Package, fset *token.FileSet) []Variants {
	byPath := make(map[string]*Variants)
	for _, p := range pkgs {
		if p.PkgPath == "" || p.TypesInfo == nil {
			continue
		}
		files := Files(p, fset)
		if len(files) == 0 {
			continue
		}
		dir := filepath.Dir(fset.PositionFor(files[0].FileStart, false).Filename)
		key := p.PkgPath + "\x00" + dir
		g := byPath[key]
		if g == nil {
			g = &Variants{PkgPath: p.PkgPath, Dir: dir}
			byPath[key] = g
		}
		g.Packages = append(g.Packages, p)
		for _, f := range files {
			if g.Anchor == nil || base(fset, f) < base(fset, g.Anchor) {
				g.Anchor = f
			}
		}
	}

	groups := make([]Variants, 0, len(byPath))
	for _, g := range byPath {
		slices.SortFunc(g.Packages, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
		groups = append(groups, *g)
	}
	slices.SortFunc(groups, func(a, b Variants) int {
		return cmp.Or(strings.Compare(a.PkgPath, b.PkgPath), strings.Compare(a.Dir, b.Dir))
	})
	return groups
}

// Files returns the package's syntax trees ordered by file name, so one
// configuration is walked in one order whatever order the load reported.
func Files(p *packages.Package, fset *token.FileSet) []*ast.File {
	files := slices.Clone(p.Syntax)
	slices.SortFunc(files, func(a, b *ast.File) int { return strings.Compare(base(fset, a), base(fset, b)) })
	return files
}

// Packages is every package [Groups] keeps, in the walk order.
func Packages(pkgs []*packages.Package, fset *token.FileSet) []*packages.Package {
	var ordered []*packages.Package
	for _, g := range Groups(pkgs, fset) {
		ordered = append(ordered, g.Packages...)
	}
	return ordered
}

// Sources yields every source file of pkgs once, in the walk order, with the first
// variant that compiles it, so a question about a file's syntax is answered once
// whatever the number of variants that type-check it.
func Sources(pkgs []*packages.Package, fset *token.FileSet) iter.Seq2[*packages.Package, *ast.File] {
	return func(yield func(*packages.Package, *ast.File) bool) {
		seen := make(map[string]bool)
		for _, p := range Packages(pkgs, fset) {
			for _, f := range Files(p, fset) {
				name := fset.Position(f.FileStart).Filename
				if seen[name] {
					continue
				}
				seen[name] = true
				if !yield(p, f) {
					return
				}
			}
		}
	}
}

// base is the file name of the file f, without its directory.
func base(fset *token.FileSet, f *ast.File) string {
	return filepath.Base(fset.Position(f.FileStart).Filename)
}
