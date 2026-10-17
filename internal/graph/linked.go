package graph

import (
	"path/filepath"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"golang.org/x/tools/go/packages"
)

// UnlinkedPackages is every package of the target
// that no build the load sees links: no main package, test binary, program or
// consumer imports it, directly or through another package. A package of a module
// the load did not read whole is never one, because a package outside the load may
// import it.
func UnlinkedPackages(r *load.Result) map[load.PackageID]bool {
	whole := make(map[string]bool, len(r.Units))
	for i := range r.Units {
		whole[filepath.Clean(r.Units[i].Dir)] = true
	}
	var seeds []*packages.Package
	used := usedByPrograms(r)
	for _, p := range r.Packages {
		if p.Name == mainPackage || p.ForTest != "" || used[p.PkgPath] {
			seeds = append(seeds, p)
		}
	}
	for _, consumer := range r.Referencing() {
		seeds = append(seeds, consumer.Packages...)
	}
	linked := make(map[load.PackageID]bool)
	packages.Visit(seeds, nil, func(p *packages.Package) { linked[r.ID(p)] = true })

	unlinked := make(map[load.PackageID]bool)
	for _, p := range r.Packages {
		if p.Module == nil || !p.Module.Main || !whole[filepath.Clean(p.Module.Dir)] || len(p.GoFiles) == 0 {
			continue
		}
		if key := r.ID(p); !linked[key] {
			unlinked[key] = true
		}
	}
	return unlinked
}

// usedByPrograms is the import path of every package a program of the target imports
// or names a declaration of.
func usedByPrograms(r *load.Result) map[string]bool {
	used := make(map[string]bool)
	for _, program := range r.Programs {
		for _, path := range program.Imports {
			used[path] = true
		}
		for _, obj := range program.Info.Uses {
			if obj.Pkg() != nil {
				used[obj.Pkg().Path()] = true
			}
		}
	}
	return used
}
