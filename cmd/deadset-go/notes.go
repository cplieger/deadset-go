package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/report"
	"golang.org/x/tools/go/packages"
)

// The note this analyzer writes about a package an application may publish.
const (
	publishedPackageNote = "published-package"
	publishedPackageKey  = "roots.patterns"
)

// The package name nothing imports, the import path suffix of an external test
// package, and the path element that keeps a package inside its module tree.
const (
	mainPackageName   = "main"
	testPackageSuffix = "_test"
	internalElement   = "internal"
)

// notesOf is every hint the run has about its setup: one published-package note per
// package of an application that is not a main package, not under an internal tree,
// and that no file of another package of the target imports, under any
// configuration. Outside programs may import such a package, and only the user knows
// whether they do. A library's packages are its published API already, and a run
// that declares consumers has stated which outside programs there are, so neither
// carries such a note.
func notesOf(kind config.TargetKind, loaded *stages) []report.Note {
	if kind != config.Application || len(loaded.declared) > 0 {
		return nil
	}
	imported := make(map[string]bool)
	directories := make(map[string]string)
	for i := range loaded.per {
		result := &loaded.per[i].result
		for _, p := range result.Packages {
			if p.Module != nil && p.Module.Main {
				addImports(imported, p)
				addPublishable(directories, loaded.root, p, result)
			}
		}
	}
	var notes []report.Note
	for path, directory := range directories {
		if imported[path] {
			continue
		}
		notes = append(notes, report.Note{
			Kind: publishedPackageNote,
			Path: directory,
			Key:  publishedPackageKey,
			Message: "package " + directory + " is imported by no other package of the target, and outside programs may import it: " +
				publishedPackageKey + " roots its published declarations if they do",
		})
	}
	return notes
}

// addImports records every package one package's files import, other than the
// package itself and its external test package's subject.
func addImports(imported map[string]bool, p *packages.Package) {
	for _, f := range p.Syntax {
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err == nil && path != p.PkgPath && path+testPackageSuffix != p.PkgPath {
				imported[path] = true
			}
		}
	}
}

// addPublishable records the directory of one package outside programs could
// import, relative to the target's root.
func addPublishable(directories map[string]string, root string, p *packages.Package, result *load.Result) {
	if !publishable(p, result) {
		return
	}
	relative, err := filepath.Rel(root, filepath.Dir(p.GoFiles[0]))
	if err == nil && filepath.IsLocal(relative) {
		directories[p.PkgPath] = filepath.ToSlash(relative)
	}
}

// publishable reports whether outside programs could import one package of the
// main module: a non-main package with source files, outside an internal tree, that
// is neither a test variant nor test support.
func publishable(p *packages.Package, result *load.Result) bool {
	return p.ForTest == "" && p.Name != mainPackageName && len(p.GoFiles) > 0 &&
		!strings.HasSuffix(p.PkgPath, testPackageSuffix) && !result.TestSupport[p.PkgPath] &&
		!slices.Contains(strings.Split(p.PkgPath, "/"), internalElement)
}
