package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/report"
	"golang.org/x/tools/go/packages"
)

// publishedPackageKey is the key that settles a published-package note.
const publishedPackageKey = "roots.patterns"

// The package name nothing imports, the import path suffix of an external test
// package, and the path element that keeps a package inside its module tree.
const (
	mainPackageName   = "main"
	testPackageSuffix = "_test"
	internalElement   = "internal"
)

// notesOf is every hint the run has about its setup: one module-not-loaded note per
// module the walk found below the target that did not load under some configuration,
// and one published-package note per non-main package of an application outside an
// internal tree that no other package of the target, program or [load.Result.Nested]
// consumer imports under any configuration, which outside programs may import. A
// library's packages are its published API already, and a run that declares
// consumers has named the outside programs, so neither carries such a note.
func notesOf(kind config.TargetKind, loaded *stages) []report.Note {
	notes := droppedNotes(loaded)
	if kind != config.Application || len(loaded.declared) > 0 {
		return notes
	}
	imported := make(map[string]bool)
	directories := make(map[string]string)
	for i := range loaded.per {
		result := &loaded.per[i].result
		for _, p := range result.Packages {
			if p.Module != nil && p.Module.Main {
				addImports(imported, p, result)
				addPublishable(directories, loaded.root, p, result)
			}
		}
		addReaderImports(imported, result)
	}
	for key, directory := range directories {
		if imported[key] {
			continue
		}
		notes = append(notes, report.Note{
			Kind: report.NoteKindPublishedPackage,
			Path: directory,
			Key:  publishedPackageKey,
			Message: "package " + directory + " is imported by no other package of the target, and outside programs may import it: " +
				publishedPackageKey + " roots its published declarations if they do",
		})
	}
	return notes
}

// droppedNotes is one note per module directory below the target root some
// configuration dropped, naming the first failure that dropped it. A dropped
// workspace reader outside the root has no path a note can carry.
func droppedNotes(loaded *stages) []report.Note {
	var notes []report.Note
	seen := make(map[string]bool)
	for i := range loaded.per {
		for _, dropped := range loaded.per[i].result.Dropped {
			relative, err := filepath.Rel(loaded.root, dropped.Dir)
			if err != nil || !filepath.IsLocal(relative) || seen[relative] {
				continue
			}
			seen[relative] = true
			directory := filepath.ToSlash(relative)
			notes = append(notes, report.Note{
				Kind: report.NoteKindModuleNotLoaded,
				Path: directory,
				Message: "module " + directory + " is below the target and does not load, so the run does not analyze it, " +
					"and an export of a module it requires is possible: " + firstLine(dropped.Err.Error()),
			})
		}
	}
	return notes
}

// addImports records every package one package's files import, by
// [load.Result.KeyOf], other than the package itself and its external test
// package's subject.
func addImports(imported map[string]bool, p *packages.Package, result *load.Result) {
	for _, f := range p.Syntax {
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil || path == p.PkgPath || path+testPackageSuffix == p.PkgPath {
				continue
			}
			if dep := p.Imports[path]; dep != nil {
				imported[result.KeyOf(dep)] = true
			} else {
				imported[path] = true
			}
		}
	}
}

// addReaderImports records every package a nested consumer or a program of result
// imports.
func addReaderImports(imported map[string]bool, result *load.Result) {
	for _, consumer := range result.Nested {
		for _, p := range consumer.Packages {
			addImports(imported, p, result)
		}
	}
	for _, program := range result.Programs {
		for _, path := range program.Imports {
			imported[path] = true
		}
	}
}

// addPublishable records the directory of one package outside programs could
// import, relative to the target's root, by [load.Result.KeyOf].
func addPublishable(directories map[string]string, root string, p *packages.Package, result *load.Result) {
	if !publishable(p, result) {
		return
	}
	relative, err := filepath.Rel(root, filepath.Dir(p.GoFiles[0]))
	if err == nil && filepath.IsLocal(relative) {
		directories[result.KeyOf(p)] = filepath.ToSlash(relative)
	}
}

// publishable reports whether outside programs could import one package of the
// main module: a non-main package with source files, outside an internal tree, that
// is neither a test variant nor test support.
func publishable(p *packages.Package, result *load.Result) bool {
	return p.ForTest == "" && p.Name != mainPackageName && len(p.GoFiles) > 0 &&
		!strings.HasSuffix(p.PkgPath, testPackageSuffix) && !result.TestSupport[result.KeyOf(p)] &&
		!slices.Contains(strings.Split(p.PkgPath, "/"), internalElement)
}
