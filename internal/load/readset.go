package load

import (
	"maps"
	"path/filepath"
	"slices"

	"golang.org/x/tools/go/packages"
)

// readFiles is a set of Go files a load compiled ([program.4]), by its
// directory's identity and its name: a read set, or a counted set. Only
// [compiledFiles] adds a file to one, and [readFiles.merge] joins two, which
// TestOnlyCompiledFileListsEnterTheReadSet holds.
type readFiles struct {
	compiledByDir map[dirID]map[string]bool
}

// newReadFiles is an empty set.
func newReadFiles() readFiles {
	return readFiles{compiledByDir: make(map[dirID]map[string]bool)}
}

// compiledFiles is the read set and the counted set of one configuration. The
// read set is every Go file a package of the graphs of r's loads and of loaded
// compiles. The counted set is every Go file of a package whose references a
// stage counts ([Result.counting]). A package's GoFiles holds the go command's
// GoFiles and CgoFiles, and a test variant's its TestGoFiles or XTestGoFiles too,
// so these are the four lists the go command compiles. Each directory's identity
// is taken once ([dirIDs]).
func compiledFiles(r *Result, loaded []*packages.Package) (read, counted readFiles) {
	read, counted = newReadFiles(), newReadFiles()
	ids := make(dirIDs)
	counting := r.counting()
	packages.Visit(slices.Concat(counting, loaded), nil, func(p *packages.Package) {
		for _, file := range p.GoFiles {
			if id := ids.of(file); id != nil {
				read.addCompiled(*id, file)
			}
		}
	})
	for _, p := range counting {
		for _, file := range p.GoFiles {
			if id := ids.of(file); id != nil {
				counted.addCompiled(*id, file)
			}
		}
	}
	return read, counted
}

// dirIDs is the identity of the directory of each file asked of it, by the
// directory's path, nil where Stat does not answer, each taken once.
type dirIDs map[string]*dirID

// of is the identity of file's directory, nil where Stat does not answer.
func (ids dirIDs) of(file string) *dirID {
	dir := filepath.Dir(file)
	id, stated := ids[dir]
	if !stated {
		if at, ok := dirIDOf(dir); ok {
			id = &at
		}
		ids[dir] = id
	}
	return id
}

// addCompiled records the file at path, which a load compiled in the directory
// id.
func (f readFiles) addCompiled(id dirID, path string) {
	if f.compiledByDir[id] == nil {
		f.compiledByDir[id] = make(map[string]bool)
	}
	f.compiledByDir[id][filepath.Base(path)] = true
}

// merge adds every file of other to f.
func (f readFiles) merge(other readFiles) {
	for id, names := range other.compiledByDir {
		if f.compiledByDir[id] == nil {
			f.compiledByDir[id] = make(map[string]bool, len(names))
		}
		maps.Copy(f.compiledByDir[id], names)
	}
}

// holds reports whether f holds the file name of the directory id.
func (f readFiles) holds(id dirID, name string) bool {
	return f.compiledByDir[id][name]
}

// fileCandidate is one directory of a loaded module's tree whose Go files the
// run's read set judges ([fileCandidate.unreadBy]): its identity, path and module,
// whether it is judged by at least one of its files, which [loader.missed] gives
// only a directory of a module that is no unit that the module's ./... pattern
// names and that lists, the names of its regular Go files a build may compile, and
// of those exempt, which a file only the ignore tag builds ([onlyUnderIgnore]) or
// one [Topology.Unread] notes is, whose own note carries every effect of it
// ([program.6]).
type fileCandidate struct {
	id            dirID
	dir, module   string
	files, exempt []string
	named         bool
}

// unreadBy reports whether the read set read does not read the directory: where
// named, read holds none of its files that are not exempt, and otherwise read
// lacks one of them.
func (c *fileCandidate) unreadBy(read readFiles) bool {
	built, held := len(c.files), 0
	for _, name := range c.files {
		if read.holds(c.id, name) {
			held++
		}
	}
	for _, name := range c.exempt {
		if read.holds(c.id, name) {
			built, held = built+1, held+1
		}
	}
	if c.named {
		return built > 0 && held == 0
	}
	return held < built
}

// unreadDirs is the identity of each directory of candidates that read does not
// read.
func unreadDirs(read readFiles, candidates []fileCandidate) map[dirID]bool {
	unread := make(map[dirID]bool)
	for i := range candidates {
		if c := &candidates[i]; !unread[c.id] && c.unreadBy(read) {
			unread[c.id] = true
		}
	}
	return unread
}

// settlement is what one configuration's load leaves for the run to decide: its
// LeftOut but the directories the run's read set leaves out, the Go files each
// [LeftOutLoaded] entry's packages compiled, by the entry's import path and
// directory, the [fileCandidate] directories, the target's packages with a file,
// which such a directory may import, and the configuration's own read set and
// counted set ([compiledFiles]).
type settlement struct {
	compiled, counted readFiles
	files             map[PackageID][]string
	left              []LeftOutPackage
	candidates        []fileCandidate
	targets           []PackageID
}

// leftOut is the configuration's LeftOut where unread holds the identity of each
// directory the run does not read and counted the files whose references the run
// counts: every entry of s.left but a loaded package's that is in such a
// directory or whose files counted holds all of ([settlement.counts]), and one
// [LeftOutUnloadable] entry for each such directory among its candidates, whose
// packages may import every package of the target.
func (s *settlement) leftOut(unread map[dirID]bool, counted readFiles) []LeftOutPackage {
	left := make([]LeftOutPackage, 0, len(s.left))
	for i := range s.left {
		entry := &s.left[i]
		if entry.Reason != LeftOutLoaded {
			left = append(left, *entry)
			continue
		}
		if id, ok := dirIDOf(entry.Dir); (!ok || !unread[id]) && !s.counts(entry, counted) {
			left = append(left, *entry)
		}
	}
	added := make(map[dirID]bool)
	for i := range s.candidates {
		c := &s.candidates[i]
		if unread[c.id] && !added[c.id] {
			added[c.id] = true
			left = append(left, LeftOutPackage{Dir: c.dir, Module: c.module, Reason: LeftOutUnloadable, Imports: slices.Clone(s.targets), Reaches: slices.Clone(s.targets)})
		}
	}
	sortLeftOut(left)
	return left
}

// counts reports whether counted holds every Go file entry's packages compiled,
// each by its directory's identity and name, as whichever configuration counted
// it resolved it; one with no file known, or one in a directory Stat does not
// answer for, is not counted.
func (s *settlement) counts(entry *LeftOutPackage, counted readFiles) bool {
	files := s.files[PackageID{Path: entry.Path, Dir: entry.Dir}]
	ids := make(dirIDs)
	for _, file := range files {
		if id := ids.of(file); id == nil || !counted.holds(*id, filepath.Base(file)) {
			return false
		}
	}
	return len(files) > 0
}

// settleAlone sets r's LeftOut by r's own read set and counted set, as a run of
// its configuration alone reads them, and drops what the settlement held.
func (r *Result) settleAlone() {
	r.LeftOut = r.settlement.leftOut(unreadDirs(r.settlement.compiled, r.settlement.candidates), r.settlement.counted)
	r.settlement = settlement{}
}
