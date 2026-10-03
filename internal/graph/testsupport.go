package graph

import (
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// testSupportRule names the classification of a file as test-support code, which
// no file-name pattern makes.
const testSupportRule = "test-support"

// testPackageSuffix ends the import path of an external test package, which
// nothing imports.
const testPackageSuffix = "_test"

// ClassifyTestSupport returns the import paths of the target's test-support
// packages: a non-main package of the target, not a test package by name, that at
// least one test file imports and that nothing but test code imports, directly or
// through other test-support packages. A library's importable package is never
// one, because outside programs may import it; only a package under an internal
// tree is.
func ClassifyTestSupport(r *load.Result, library bool) map[string]bool {
	importers := make(map[string][]importer)
	candidates := make(map[string]bool)
	seen := make(map[string]bool)
	for _, p := range r.Packages {
		if p.Module == nil || !p.Module.Main {
			continue
		}
		if supportCandidate(p, library) {
			candidates[p.PkgPath] = true
		}
		addImporters(importers, seen, r, p)
	}
	return closeSupport(candidates, importers)
}

// importer is one file's import of a package: the importing package, and whether
// the file is a test file.
type importer struct {
	from string
	test bool
}

// supportCandidate reports whether one package of the main module could be test
// support: a non-main, non-test package, under an internal tree for a library.
func supportCandidate(p *packages.Package, library bool) bool {
	return p.ForTest == "" && p.Name != mainPackage && !strings.HasSuffix(p.PkgPath, testPackageSuffix) &&
		(!library || slices.Contains(strings.Split(p.PkgPath, "/"), internalElement))
}

// addImporters records the imports of every file of p not already seen, since a
// file belongs to a package and to its test variant both.
func addImporters(importers map[string][]importer, seen map[string]bool, r *load.Result, p *packages.Package) {
	for _, f := range p.Syntax {
		name := r.Fset.Position(f.FileStart).Filename
		if seen[name] {
			continue
		}
		seen[name] = true
		_, test := IsTestFile(name)
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			importers[path] = append(importers[path], importer{from: p.PkgPath, test: test})
		}
	}
}

// closeSupport is the candidates every importer of which is a test file or a
// package already found to be test support, grown until nothing changes.
func closeSupport(candidates map[string]bool, importers map[string][]importer) map[string]bool {
	support := make(map[string]bool)
	for changed := true; changed; {
		changed = false
		for candidate := range candidates {
			held := importers[candidate]
			if support[candidate] || len(held) == 0 {
				continue
			}
			if slices.ContainsFunc(held, func(one importer) bool { return !one.test && !support[one.from] }) {
				continue
			}
			support[candidate] = true
			changed = true
		}
	}
	return support
}
