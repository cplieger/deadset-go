package load

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// LeftOutPackage is one package a load compiled with a file no load of the run
// counts ([program.4]), that reaches a package of the target or is a unit's package
// importing another module's: one in a directory no load listed
// ([loader.missed]), one [importedOutside] does not hold, or one only a program or
// a file importing "C" imports. Code there may use any export of each target
// package it reaches, and needs each module it imports. One with no Path is a
// directory whose packages the run does not load, or a module whose references it
// does not count, for its Reason.
type LeftOutPackage struct {
	// Dir is the package's directory as the go command names it, through the
	// links its import path reaches it by, Path its import path and Module the
	// path of its module, empty where none governs it.
	Dir    string
	Path   string
	Module string

	// Imports holds the target's packages it imports directly, and Reaches those
	// it imports directly or through other packages, each once, in import path
	// order. Modules holds the path of each other module it imports a package of
	// directly, in path order, and Built each module providing a package it
	// reaches at any depth, its own among them. A package that did not load has no
	// Path, and may import a package of any module.
	Imports []PackageID
	Reaches []PackageID
	Modules []string
	Built   []*packages.Module

	Reason LeftOutReason
}

// LeftOutReason is why the run leaves out the packages of a [LeftOutPackage]
// unloaded, so that they may use every export of the target.
type LeftOutReason int

const (
	// LeftOutLoaded is a package the load compiled, which names its imports.
	LeftOutLoaded LeftOutReason = iota

	// LeftOutUnbounded is a link or a Windows junction whose tree the run does not
	// walk, since it has no bound: one to a strict ancestor of the loaded module's
	// directory, or one to another device ([treeWalk.outcome]). A plain directory
	// on another device, a mount point, is walked.
	LeftOutUnbounded

	// LeftOutUnlistable is a directory of a loaded module's tree, outside the
	// root, that the run cannot list.
	LeftOutUnlistable

	// LeftOutUnloadable is a directory of a loaded module's tree whose Go files
	// no load of the run compiled ([fileCandidate.unreadBy]), over every
	// configuration of the matrix ([Matrix.All]): one ./... does not name, one of
	// a module that is no unit, and one a configuration's loads did not list,
	// whose packages do not list alone or that holds an entry the configuration's
	// build opens that is no regular file, which no toolchain is started on
	// ([Configuration.opens]).
	LeftOutUnloadable

	// LeftOutConsumer is a module root a walk of a module's tree meets, through a
	// link or not, whose build resolves a unit, which the run does not load.
	LeftOutConsumer

	// LeftOutRenamed is a module whose build resolves a unit under another module
	// path, whose references name no declaration of the target.
	LeftOutRenamed

	// LeftOutUnanswered is a module whose module graph the run could not ask or
	// the go command could not list, whose build may resolve a unit under any path
	// ([consumptionRule]).
	LeftOutUnanswered
)

// leftOutPackages is every [LeftOutPackage] of the graphs r's loads and loaded
// hold and each of unloaded, in import path order, and files the Go files each
// loaded entry's packages compiled, by its import path and directory. A package
// is told apart by its import path and its files' directory, so a link to a read
// package's directory under another import path is a package of its own. An
// unloaded directory has no import path and may import every package of the
// target and of every module; [settlement.leftOut] decides which loaded ones stay.
func (r *Result) leftOutPackages(loaded []*packages.Package, unloaded []LeftOutPackage) (left []LeftOutPackage, files map[PackageID][]string) {
	g, roots := newLeftOutGraph(r)
	byDir := make(map[PackageID]*LeftOutPackage)
	files = make(map[PackageID][]string)
	packages.Visit(append(roots, loaded...), nil, func(p *packages.Package) {
		at := PackageID{Path: p.PkgPath, Dir: packageDir(p)}
		if at.Dir == "" || g.readAt(p) || !g.reaches(p) && len(g.modules(p)) == 0 {
			return
		}
		held := byDir[at]
		if held == nil {
			held = &LeftOutPackage{Dir: p.Dir, Path: p.PkgPath, Module: moduleOf(p)}
			byDir[at] = held
		}
		g.record(held, p)
		key := PackageID{Path: held.Path, Dir: held.Dir}
		for _, file := range p.GoFiles {
			if !scope.Listed(files[key], file) {
				files[key] = append(files[key], file)
			}
		}
	})
	targets := g.targetIDs()
	for i := range unloaded {
		held := &unloaded[i]
		held.Imports, held.Reaches = slices.Clone(targets), slices.Clone(targets)
		byDir[PackageID{Dir: held.Dir}] = held
	}
	left = make([]LeftOutPackage, 0, len(byDir))
	for _, held := range byDir {
		slices.SortFunc(held.Imports, byPackageID)
		slices.SortFunc(held.Reaches, byPackageID)
		slices.Sort(held.Modules)
		left = append(left, *held)
	}
	sortLeftOut(left)
	return left, files
}

// sortLeftOut orders left by import path, then directory.
func sortLeftOut(left []LeftOutPackage) {
	slices.SortFunc(left, func(a, b LeftOutPackage) int {
		return byPackageID(PackageID{Path: a.Path, Dir: a.Dir}, PackageID{Path: b.Path, Dir: b.Dir})
	})
}

// newLeftOutGraph is the graph of r's reads, and the packages its walk starts from:
// the target's and each [Result.Referencing] consumer's.
func newLeftOutGraph(r *Result) (*leftOutGraph, []*packages.Package) {
	g := &leftOutGraph{r: r, read: make(map[string][]string), target: make(map[PackageID]bool), reaching: make(map[*packages.Package]bool), units: make(map[string]bool)}
	for i := range r.Units {
		g.units[r.Units[i].Path] = r.Units[i].Path != ""
	}
	for _, p := range r.Packages {
		g.target[r.ID(p)] = true
	}
	roots := r.counting()
	for _, p := range roots {
		g.addRead(p)
	}
	return g, roots
}

// counting is every package whose references a stage counts: the target's,
// then each [Result.Referencing] consumer's, the packages [importedOutside]
// holds among them.
func (r *Result) counting() []*packages.Package {
	held := slices.Clone(r.Packages)
	for _, consumer := range r.Referencing() {
		held = append(held, consumer.Packages...)
	}
	return held
}

// leftOutGraph is what [Result.leftOutPackages] reads of one load: the file
// directories a stage reads each import path from, the target's packages by
// [Result.ID], whether each package met reaches one of them, and the units' module
// paths.
type leftOutGraph struct {
	r        *Result
	read     map[string][]string
	target   map[PackageID]bool
	reaching map[*packages.Package]bool
	units    map[string]bool
}

// addRead records that a stage reads p's files.
func (g *leftOutGraph) addRead(p *packages.Package) {
	if dir := packageDir(p); dir != "" && !scope.Listed(g.read[p.PkgPath], dir) {
		g.read[p.PkgPath] = append(g.read[p.PkgPath], dir)
	}
}

// readAt reports whether a stage reads p: p is a package of the target, which
// [Result.ID] gives the target's directory whatever the load listing it spells, or
// a stage reads its import path from a directory p's files resolve to. Two loads
// run from two spellings of one directory, a consumer replacing the target through
// a link, name one package's files under both, and the run reads it once.
func (g *leftOutGraph) readAt(p *packages.Package) bool {
	if g.targets(p) {
		return true
	}
	dir := packageDir(p)
	return slices.ContainsFunc(g.read[p.PkgPath], func(read string) bool { return scope.Same(dir, read) })
}

// targets reports whether p is a package of the target with a file.
func (g *leftOutGraph) targets(p *packages.Package) bool {
	return len(p.GoFiles) > 0 && g.target[g.r.ID(p)]
}

// reaches reports whether p is a package of the target or imports one, directly
// or through other packages. An import graph holds no cycle, so a package met
// again before its answer is known is never one p's answer depends on.
func (g *leftOutGraph) reaches(p *packages.Package) bool {
	if held, known := g.reaching[p]; known {
		return held
	}
	g.reaching[p] = false
	held := g.targets(p)
	for _, imported := range p.Imports {
		held = g.reaches(imported) || held
	}
	g.reaching[p] = held
	return held
}

// modules is the path of each other module a package of a unit imports a package
// of directly, and nil for a package of no unit's module.
func (g *leftOutGraph) modules(p *packages.Package) []string {
	own := moduleOf(p)
	if !g.units[own] {
		return nil
	}
	var modules []string
	for _, imported := range p.Imports {
		if module := moduleOf(imported); module != "" && module != own && !slices.Contains(modules, module) {
			modules = append(modules, module)
		}
	}
	return modules
}

// moduleOf is the path of p's module, empty where none governs it.
func moduleOf(p *packages.Package) string {
	if p.Module == nil {
		return ""
	}
	return p.Module.Path
}

// record adds to held the target's packages p imports, those it reaches and the
// other modules it imports a package of.
func (g *leftOutGraph) record(held *LeftOutPackage, p *packages.Package) {
	for _, module := range g.modules(p) {
		if !slices.Contains(held.Modules, module) {
			held.Modules = append(held.Modules, module)
		}
	}
	packages.Visit([]*packages.Package{p}, nil, func(dep *packages.Package) {
		if dep.Module != nil && !slices.ContainsFunc(held.Built, func(m *packages.Module) bool {
			return m.Path == dep.Module.Path && m.Version == dep.Module.Version
		}) {
			held.Built = append(held.Built, dep.Module)
		}
	})
	for _, imported := range p.Imports {
		if g.targets(imported) {
			held.Imports = appendID(held.Imports, g.r.ID(imported))
		}
	}
	packages.Visit(slices.Collect(maps.Values(p.Imports)), nil, func(dep *packages.Package) {
		if g.targets(dep) {
			held.Reaches = appendID(held.Reaches, g.r.ID(dep))
		}
	})
}

// appendID is ids with id added where it holds none.
func appendID(ids []PackageID, id PackageID) []PackageID {
	if slices.Contains(ids, id) {
		return ids
	}
	return append(ids, id)
}

// byPackageID orders two identities by import path, then directory.
func byPackageID(a, b PackageID) int {
	return cmp.Or(strings.Compare(importPathOf(a), importPathOf(b)), scope.Order(a.Dir, b.Dir))
}

// importPathOf is id's import path, which names no file.
func importPathOf(id PackageID) string { return id.Path }

// targetIDs is the identity of each package of the target with a file, in import
// path order, which a directory whose packages do not load may import.
func (r *Result) targetIDs() []PackageID {
	g, _ := newLeftOutGraph(r)
	return g.targetIDs()
}

// targetIDs is [Result.targetIDs] of g's load.
func (g *leftOutGraph) targetIDs() []PackageID {
	var ids []PackageID
	for _, p := range g.r.Packages {
		if g.targets(p) {
			ids = appendID(ids, g.r.ID(p))
		}
	}
	slices.SortFunc(ids, byPackageID)
	return ids
}
