package kinds

import (
	"fmt"
	"go/token"
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"pgregory.net/rapid"
)

// drawnReach is what an importer outside the target module may do with a drawn
// package.
type drawnReach uint8

// The three reaches a drawn package carries.
const (
	reachPublished drawnReach = iota // an importer outside the module may name it
	reachInternal                    // an internal element guards it
	reachCommand                     // it is a main package, which nothing imports
)

// drawnSite is where one reference to a drawn declaration is written.
type drawnSite uint8

// The four placements a drawn reference takes.
const (
	siteDeclaringFile drawnSite = iota
	siteAnotherFile
	siteAnotherPackage
	siteOutsideModule
)

// drawnDeclaration is one exported declaration and where every reference to it is
// written.
type drawnDeclaration struct {
	sites []drawnSite
	file  int
}

// drawnPackage is one package of a drawn module.
type drawnPackage struct {
	declarations []drawnDeclaration
	reach        drawnReach
}

// drawnModule is one module as it was drawn, with what the run knows about its
// consumers.
type drawnModule struct {
	packages []drawnPackage
	complete bool
}

// drawnSites is the pool one reference placement is drawn from. A placement
// outside the module is drawn less often than the three inside it, because one of
// them in a set answers the whole set and would otherwise crowd out the sets that
// name a scope.
func drawnSites() []drawnSite {
	return []drawnSite{
		siteDeclaringFile, siteDeclaringFile,
		siteAnotherFile, siteAnotherFile,
		siteAnotherPackage, siteAnotherPackage,
		siteOutsideModule,
	}
}

// drawnModules draws two or three packages, one to two exported declarations in
// each, and zero to three reference placements per declaration, against a
// consumer set the run may or may not know to be complete.
func drawnModules() *rapid.Generator[drawnModule] {
	declaration := rapid.Custom(func(t *rapid.T) drawnDeclaration {
		return drawnDeclaration{
			file: rapid.IntRange(0, 1).Draw(t, "the file that declares it"),
			sites: rapid.SliceOfN(rapid.SampledFrom(drawnSites()), 0, 3).
				Draw(t, "where the references are written"),
		}
	})
	pkg := rapid.Custom(func(t *rapid.T) drawnPackage {
		return drawnPackage{
			reach: rapid.SampledFrom([]drawnReach{
				reachPublished, reachInternal, reachCommand,
			}).Draw(t, "what an importer outside the module may do with the package"),
			declarations: rapid.SliceOfN(declaration, 1, 2).Draw(t, "the declarations"),
		}
	})
	return rapid.Custom(func(t *rapid.T) drawnModule {
		return drawnModule{
			packages: rapid.SliceOfN(pkg, 2, 3).Draw(t, "the packages"),
			complete: rapid.Bool().Draw(t, "the consumer set is declared complete and loaded"),
		}
	})
}

// narrowedInventory accumulates the declarations, references and roots one drawn
// module renders to.
type narrowedInventory struct {
	lines   map[string]int
	symbols []graph.Symbol
	refs    []graph.Reference
	roots   []graph.Root
	uses    int
}

// declare appends one declaration at the next line of its file and returns its
// identifier. Every declaration is a root, so reachability holds every one of them
// live and the reference spread is the only thing a narrowing rule decides on.
func (d *narrowedInventory) declare(file, pkgPath, name string, exported bool) graph.SymbolID {
	d.lines[file]++
	line := d.lines[file]
	id := graph.SymbolID(fmt.Sprintf("%s:%d:1", file, line))
	d.symbols = append(d.symbols, graph.Symbol{
		ID:       id,
		Ref:      "go://" + pkgPath + "#" + name,
		Name:     name,
		PkgPath:  pkgPath,
		Pos:      token.Position{Filename: file, Line: line, Column: 1},
		EndLine:  line,
		Kind:     graph.KindFunc,
		Exported: exported,
	})
	d.roots = append(d.roots, graph.Root{ID: id, Kind: graph.RootPublishedAPI})
	return id
}

// refer appends one reference to id, written in file by from.
func (d *narrowedInventory) refer(from graph.SymbolID, file string, id graph.SymbolID) {
	d.lines[file]++
	d.refs = append(d.refs, graph.Reference{
		From: from,
		To:   id,
		Pos:  token.Position{Filename: file, Line: d.lines[file], Column: 3},
		Kind: graph.RefCall,
	})
}

// pathOf is the import path a drawn package sits at.
func pathOf(reach drawnReach, at int) string {
	switch reach {
	case reachInternal:
		return fmt.Sprintf("example.com/app/internal/p%d", at)
	case reachCommand:
		return fmt.Sprintf("example.com/app/cmd/p%d", at)
	case reachPublished:
		return fmt.Sprintf("example.com/app/p%d", at)
	}
	return fmt.Sprintf("example.com/app/p%d", at)
}

// filesOf is the two files a drawn package compiles.
func filesOf(path string) [2]string {
	return [2]string{path + "/a.go", path + "/b.go"}
}

// names is the package clause of every import path a drawn module declares into.
func (m drawnModule) names() map[string]string {
	named := make(map[string]string, len(m.packages))
	for at, pkg := range m.packages {
		name := fmt.Sprintf("p%d", at)
		if pkg.reach == reachCommand {
			name = "main"
		}
		named[pathOf(pkg.reach, at)] = name
	}
	return named
}

// render writes one drawn module as an inventory the graph passes would have
// produced.
func (m drawnModule) render() *narrowedInventory {
	d := &narrowedInventory{lines: make(map[string]int)}
	held := make(map[string]graph.SymbolID)
	for at, pkg := range m.packages {
		path := pathOf(pkg.reach, at)
		files := filesOf(path)
		if pkg.reach == reachCommand {
			main := d.declare(files[0], path, "main", false)
			d.roots = append(d.roots, graph.Root{ID: main, Kind: graph.RootMain})
		}
		for which, declaration := range pkg.declarations {
			name := fmt.Sprintf("S%d%d", at, which)
			id := d.declare(files[declaration.file], path, name, true)
			held["go://"+path+"#"+name] = id
		}
	}
	for at, pkg := range m.packages {
		path := pathOf(pkg.reach, at)
		files := filesOf(path)
		for which, declaration := range pkg.declarations {
			id := held[fmt.Sprintf("go://%s#S%d%d", path, at, which)]
			for _, site := range declaration.sites {
				d.place(m, at, files[declaration.file], files[1-declaration.file], site, id)
			}
		}
	}
	return d
}

// place writes the one reference site names, declaring the referrer it needs.
func (d *narrowedInventory) place(
	m drawnModule, at int, own, other string, site drawnSite, id graph.SymbolID,
) {
	d.uses++
	name := fmt.Sprintf("use%d", d.uses)
	switch site {
	case siteDeclaringFile:
		d.refer(d.declare(own, pathOf(m.packages[at].reach, at), name, false), own, id)
	case siteAnotherFile:
		d.refer(d.declare(other, pathOf(m.packages[at].reach, at), name, false), other, id)
	case siteAnotherPackage:
		beside := (at + 1) % len(m.packages)
		path := pathOf(m.packages[beside].reach, beside)
		file := filesOf(path)[0]
		d.refer(d.declare(file, path, name, false), file, id)
	case siteOutsideModule:
		file := "consumer/use.go"
		d.refer(graph.SymbolID(fmt.Sprintf("%s:%d:1", file, d.uses)), file, id)
	}
}

// want is the finding each drawn declaration is owed, as a code and the narrower
// visibility it names, by symbol reference. A declaration owed none is absent.
func (m drawnModule) want() map[string]string {
	owed := make(map[string]string)
	for at, pkg := range m.packages {
		path := pathOf(pkg.reach, at)
		for which, declaration := range pkg.declarations {
			if row, ok := owedFor(pkg.reach, m.complete, declaration.sites); ok {
				owed[fmt.Sprintf("go://%s#S%d%d", path, at, which)] = row
			}
		}
	}
	return owed
}

// owedFor answers which finding one declaration's reference placements are owed.
func owedFor(reach drawnReach, complete bool, sites []drawnSite) (string, bool) {
	if len(sites) == 0 || slices.Contains(sites, siteOutsideModule) {
		return "", false
	}
	if reach == reachPublished && !complete {
		return "", false
	}
	switch {
	case !slices.Contains(sites, siteAnotherPackage) && !slices.Contains(sites, siteAnotherFile):
		return unnecessaryExportCode + " " + visibilityFile, true
	case !slices.Contains(sites, siteAnotherPackage):
		return unnecessaryExportCode + " " + visibilityPackage, true
	case reach == reachPublished:
		return unnecessaryExposureCode + " " + visibilityModule, true
	default:
		return "", false
	}
}

// Property dead-code-suite/P10: over generated declarations and reference
// placements across files, packages and modules, a declaration whose every
// reference lies inside a scope narrower than its declared visibility is reported
// under the narrowing kind for that scope, the narrower visibility named is the
// narrowest scope containing every reference, and one reference from outside the
// module leaves the declaration unreported.
//
// The oracle is the placements a draw produced, which decide the answer without
// the spread the rules fold.
func TestNarrowingNamesTheNarrowestScopeContainingEveryReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawnModules().Draw(t, "the module")
		inventory := m.render()
		consumers := Consumers{}
		if m.complete {
			consumers = Consumers{
				Declared: []string{theConsumer},
				Loaded:   []string{theConsumer},
				Complete: true,
			}
		}
		in := narrowingInput(t, graph.Configured{
			Symbols:    inventory.symbols,
			References: inventory.refs,
			Roots:      inventory.roots,
		}, m.names(), consumers)

		exported, err := UnnecessaryExport(in)
		if err != nil {
			t.Fatalf("UnnecessaryExport = error %v, want the findings", err)
		}
		exposed, err := UnnecessaryExposure(in)
		if err != nil {
			t.Fatalf("UnnecessaryExposure = error %v, want the findings", err)
		}

		got := make(map[string]string, len(exported)+len(exposed))
		for _, f := range append(exported, exposed...) {
			got[f.Symbol.Ref] = f.Code + " " + f.Details.NarrowerVisibility
		}
		if want := m.want(); !maps.Equal(got, want) {
			t.Fatalf("the narrowing kinds reported %v, want %v", got, want)
		}
	})
}
