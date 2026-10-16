package run

import (
	"path/filepath"
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/report"
	"golang.org/x/tools/go/packages"
)

// notes is every hint the run has about its setup: one module-not-loaded note per
// module below the target some configuration dropped, and, for an application that
// declares no consumers, one published-package note per published package that no
// other package of the target, program or [load.Result.Nested] consumer imports
// under any configuration. A library's packages are its published API already, and
// a run that declares consumers has named the outside programs.
func (l *Loaded) notes() []report.Note {
	notes := l.droppedNotes()
	if l.req.Config.Target.Kind != report.TargetKindApplication || len(l.Declared) > 0 {
		return notes
	}
	imported := make(map[string]bool)
	directories := make(map[string]string)
	for i := range l.Loads {
		result := &l.Loads[i].Result
		for _, p := range result.Packages {
			if p.Module != nil && p.Module.Main {
				addImports(imported, p, result)
				addPublishable(directories, l.Root, p, result)
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
			Key:  rootsPatternsSetting,
			Message: "package " + directory + " is imported by no other package of the target, and outside programs may import it: " +
				rootsPatternsSetting + " roots its published declarations if they do",
		})
	}
	return notes
}

// droppedNotes is one note per module directory below the target root some
// configuration dropped, naming the first failure that dropped it. A dropped
// workspace reader outside the root has no path a note can carry.
func (l *Loaded) droppedNotes() []report.Note {
	var notes []report.Note
	seen := make(map[string]bool)
	for i := range l.Loads {
		for _, dropped := range l.Loads[i].Result.Dropped {
			relative, err := filepath.Rel(l.Root, dropped.Dir)
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
			if err != nil || path == p.PkgPath || path+"_test" == p.PkgPath {
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

// addPublishable records, by [load.Result.KeyOf], the target-relative directory of
// one published package with source files that is neither a test variant nor test
// support.
func addPublishable(directories map[string]string, root string, p *packages.Package, result *load.Result) {
	if p.ForTest != "" || len(p.GoFiles) == 0 || result.TestSupport[result.KeyOf(p)] || !load.Published(p.PkgPath, p.Name == "main") {
		return
	}
	relative, err := filepath.Rel(root, filepath.Dir(p.GoFiles[0]))
	if err == nil && filepath.IsLocal(relative) {
		directories[result.KeyOf(p)] = filepath.ToSlash(relative)
	}
}
