package run

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/report"
	"golang.org/x/tools/go/packages"
)

// notes is every hint the run has about its setup: one module-not-loaded note per
// module some configuration dropped, and, for an application that declares no
// consumers, one published-package note per published package that no other
// package of the target, program or [load.Result.Nested] consumer imports under any
// configuration. A library's packages are its published API already, and a run
// that declares consumers has named the outside programs.
func (l *Loaded) notes() ([]report.Note, error) {
	notes := l.droppedNotes()
	if l.req.Config.Target.Kind != report.TargetKindApplication || len(l.Declared) > 0 {
		return notes, nil
	}
	imported := imports{ids: make(map[load.PackageID]bool), paths: make(map[string]bool)}
	directories := make(map[load.PackageID]string)
	for i := range l.loads.Len() {
		one, err := l.loads.At(i)
		if err != nil {
			return nil, err
		}
		result := one.Result
		imported.idOf = result.ID
		for _, p := range result.Packages {
			if p.Module != nil && p.Module.Main {
				imported.add(p)
				addPublishable(directories, l.Topology, result, p, &l.Merged.Tests)
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
	return notes, nil
}

// droppedNotes is one note per module directory some configuration dropped,
// naming the first failure that dropped it. A module below the target root, where
// both resolve ([load.Topology.Holds]), is named by its directory; a workspace
// reader outside it is named at the root, and only where its module file cannot be
// read. A module whose module or workspace file cannot be read makes every export
// of the target possible, and any other an export of a module it requires.
func (l *Loaded) droppedNotes() []report.Note {
	var notes []report.Note
	seen := make(map[string]bool)
	for i := range l.loads.outlines {
		for _, dropped := range l.loads.outlines[i].dropped {
			directory := l.Topology.Rel(dropped.Dir)
			below := l.Topology.Holds(dropped.Dir)
			if seen[directory] || !below && !dropped.Unreadable {
				continue
			}
			seen[directory] = true
			possible := "an export of a module it requires is possible"
			if dropped.Unreadable {
				possible = "every export of the target is possible"
			}
			note := report.Note{
				Kind: report.NoteKindModuleNotLoaded,
				Path: directory,
				Message: "module " + directory + " is below the target and does not load, so the run does not analyze it, " +
					"and " + possible + ": " + firstLine(dropped.Err.Error()),
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

// unreadNotes is one unread-entry note per entry of the tree the run could not
// read, named where its directory resolves ([load.Topology.Rel]), saying what
// follows: code the load could not read may use any export of the target, and an
// entry rule N could not read keeps every function and variable.
func (p *Prepared) unreadNotes() []report.Note {
	var entries []string
	unloaded, unscanned := make(map[string]bool), make(map[string]bool)
	for _, held := range []struct {
		by    map[string]bool
		paths []string
	}{{unloaded, p.Topology.Unread}, {unscanned, p.unread}} {
		for _, path := range held.paths {
			entry := p.Topology.Rel(path)
			if !unloaded[entry] && !unscanned[entry] {
				entries = append(entries, entry)
			}
			held.by[entry] = true
		}
	}
	notes := make([]report.Note, 0, len(entries))
	for _, entry := range entries {
		var effects []string
		if unloaded[entry] {
			effects = append(effects, "the run does not analyze code there, which may use any export of the target, so every export is possible")
		}
		if unscanned[entry] {
			effects = append(effects, "it may be a source in another language than Go naming any function or variable of the target, so every one is kept")
		}
		notes = append(notes, report.Note{
			Kind:    report.NoteKindUnreadEntry,
			Path:    entry,
			Message: entry + " cannot be read: " + strings.Join(effects, ", and ") + ", until the run can read it",
		})
	}
	return append(notes, p.hiddenNotes()...)
}

// hiddenNotes is one unread-entry note per entry some configuration's toolchain
// read as absent where code may hide ([load.Result.Hidden]), named at the root
// where it lies outside it, and one per go env file the toolchain runs without.
// Each entry is named where its directory resolves ([load.Topology.Rel]), so two
// spellings of one entry, the root's and one through a link inside the root or
// one a replace directive or a consumer names, make one note.
func (p *Prepared) hiddenNotes() []report.Note {
	var hidden []string
	for i := range p.loads.outlines {
		hidden = append(hidden, p.loads.outlines[i].hidden...)
	}
	slices.Sort(hidden)
	var notes []report.Note
	for _, path := range slices.Compact(hidden) {
		entry, at := p.notedAt(path)
		note := report.Note{
			Kind: report.NoteKindUnreadEntry,
			Path: at,
			Message: entry + " is a pipe, a device or a socket, which the go command may block on, so the run hides it from the go command " +
				"and does not analyze code there, which may use any export of the target and any declaration of its package, so every one is possible, " +
				"until it is a regular file or gone",
		}
		if !slices.Contains(notes, note) {
			notes = append(notes, note)
		}
	}
	for _, file := range p.Topology.EnvFiles {
		which := "the go env file in the user's configuration directory"
		if file.Named {
			which = "the go env file GOENV names"
		}
		// One outside the root is the user's, whose path the report does not name.
		if entry, at := p.notedAt(file.Path); at != "." {
			which += ", " + entry + ","
		}
		note := report.Note{
			Kind:    report.NoteKindUnreadEntry,
			Path:    ".",
			Message: which + " is no regular file, so every toolchain the run starts where it would read it runs with GOENV=off, without the settings it holds, until it is a regular file",
		}
		if !slices.Contains(notes, note) {
			notes = append(notes, note)
		}
	}
	return notes
}

// notedAt is path relative to the root where both resolve ([load.Topology.Rel]),
// and the path a note about it names: that one where the root holds path
// ([load.Topology.Holds]), else the root.
func (p *Prepared) notedAt(path string) (entry, at string) {
	entry = p.Topology.Rel(path)
	if !p.Topology.Holds(path) {
		return entry, "."
	}
	return entry, entry
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
// with source files that is neither a test variant nor test support, below the
// root where both resolve.
func addPublishable(directories map[load.PackageID]string, top *load.Topology, result *load.Result, p *packages.Package, tests *roots.TestClasses) {
	if p.ForTest != "" || len(p.GoFiles) == 0 || tests.Support[result.ID(p)] || !load.Published(p.PkgPath, p.Name == "main") {
		return
	}
	if relative, inside := top.Place(filepath.Dir(p.GoFiles[0])); inside {
		directories[result.ID(p)] = relative
	}
}
