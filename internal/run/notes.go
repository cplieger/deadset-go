package run

import (
	"errors"
	"path/filepath"
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/report"
	"golang.org/x/tools/go/packages"
)

// notes is every hint the run has about its setup: one module-not-loaded note per
// module some configuration dropped, and, for an application that declares no
// consumers, one published-package note per published package that no other
// package of the target, program or [load.Result.Nested] consumer imports under any
// configuration. A library's packages are its published API already, and a run
// that declares consumers has named the outside programs.
func (l *Loaded) notes() []report.Note {
	notes := l.droppedNotes()
	if l.req.Config.Target.Kind != report.TargetKindApplication || len(l.Declared) > 0 {
		return notes
	}
	imported := imports{ids: make(map[load.PackageID]bool), paths: make(map[string]bool)}
	directories := make(map[load.PackageID]string)
	for i := range l.Loads {
		result := &l.Loads[i].Result
		imported.idOf = result.ID
		for _, p := range result.Packages {
			if p.Module != nil && p.Module.Main {
				imported.add(p)
				addPublishable(directories, l.Root, result, p, &l.Loads[i].Tests)
			}
		}
		imported.addReaders(result)
	}
	for id, directory := range directories {
		if imported.holds(id) {
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

// droppedNotes is one note per module directory some configuration dropped,
// naming the first failure that dropped it. A module below the target root is named
// by its directory; a workspace reader outside it is named at the root, and only
// where its module file cannot be read, which makes every export of the target
// possible.
func (l *Loaded) droppedNotes() []report.Note {
	var notes []report.Note
	seen := make(map[string]bool)
	for i := range l.Loads {
		for _, dropped := range l.Loads[i].Result.Dropped {
			directory := load.RelativeSlash(l.Root, dropped.Dir)
			below := filepath.IsLocal(filepath.FromSlash(directory))
			if seen[directory] || !below && !errors.Is(dropped.Err, load.ErrModuleFile) {
				continue
			}
			seen[directory] = true
			note := report.Note{
				Kind: report.NoteKindModuleNotLoaded,
				Path: directory,
				Message: "module " + directory + " is below the target and does not load, so the run does not analyze it, " +
					"and an export of a module it requires is possible: " + firstLine(dropped.Err.Error()),
			}
			if !below {
				note.Path = "."
				note.Message = "module " + directory + " the workspace lists has a module file the run cannot read, " +
					"so the run does not count its references, and every export of the target is possible"
			}
			notes = append(notes, note)
		}
	}
	return notes
}

// imports is every package the target's own packages, its programs and its nested
// consumers import: by identity where the load resolved the import, and by path
// where it did not or where a program names it.
type imports struct {
	ids   map[load.PackageID]bool
	paths map[string]bool
	idOf  func(*packages.Package) load.PackageID
}

// holds reports whether the package of id is imported.
func (in *imports) holds(id load.PackageID) bool { return in.ids[id] || in.paths[id.Path] }

// add records every package one package's files import, other than the package
// itself and its external test package's subject.
func (in *imports) add(p *packages.Package) {
	for _, f := range p.Syntax {
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil || path == p.PkgPath || path+"_test" == p.PkgPath {
				continue
			}
			if dep := p.Imports[path]; dep != nil {
				in.ids[in.idOf(dep)] = true
			} else {
				in.paths[path] = true
			}
		}
	}
}

// addReaders records every package a nested consumer or a program of result
// imports.
func (in *imports) addReaders(result *load.Result) {
	for _, consumer := range result.Nested {
		for _, p := range consumer.Packages {
			in.add(p)
		}
	}
	for _, program := range result.Programs {
		for _, path := range program.Imports {
			in.paths[path] = true
		}
	}
}

// addPublishable records the target-relative directory of one published package
// with source files that is neither a test variant nor test support.
func addPublishable(directories map[load.PackageID]string, root string, result *load.Result, p *packages.Package, tests *graph.TestClasses) {
	if p.ForTest != "" || len(p.GoFiles) == 0 || tests.Support[result.ID(p)] || !load.Published(p.PkgPath, p.Name == "main") {
		return
	}
	relative, err := filepath.Rel(root, filepath.Dir(p.GoFiles[0]))
	if err == nil && filepath.IsLocal(relative) {
		directories[result.ID(p)] = filepath.ToSlash(relative)
	}
}
