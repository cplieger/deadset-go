package load

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// goTree is what one module's directory holds of the Go a build of that module
// can name ([program.4]): every directory below it holding a Go-named entry under
// any build constraint, recorded under the first path that builds it
// ([vendorSpelling]), every link to a directory followed, so a link out of the
// root, a Windows junction and a directory ./... skips are all in it. unloaded is
// each directory whose packages the run leaves out unloaded, and roots each module
// root met below it, link or not, whose own build [consumptionRule] decides.
type goTree struct {
	dirs     []treeDir
	unloaded []LeftOutPackage
	roots    []linkedRoot
}

// linkedRoot is one module root a walk met: the directory it resolves to, where
// its own build runs and reads its relative paths from, and its path through the
// links that reach it, where a note names it.
type linkedRoot struct {
	dir, at string
}

// treeDir is one directory of a [goTree] holding a regular Go file: its pattern
// from the module's directory, through the links that reach it, whether a link is
// on that path, its identity, the name of each regular file a build may compile
// ([buildconstraint.IsGoFile]), which the run's read set judges
// ([fileCandidate]), and of each
// Go-named entry that is no regular file, which a configuration whose build opens
// it may block or fail on ([Configuration.opens]). A directory whose Go-named
// entries are all irregular holds no code the run could read ([program.6]).
type treeDir struct {
	id        dirID
	pattern   string
	files     []string
	irregular []string
	regular   bool
	linked    bool
}

// dirOutcome is what [treeWalk.outcome] decides of one directory the walk meets.
// The walk acts on every value ([treeWalk.meet]); none leaves a directory a path
// builds without a record.
type dirOutcome uint8

const (
	// dirSeen is a directory met before by its identity, under a spelling building
	// at least as much, which that meeting decided.
	dirSeen dirOutcome = iota

	// dirModuleRoot roots another module, whose build [consumptionRule] decides.
	dirModuleRoot

	// dirUnread is a directory inside the root the run cannot stat or list: an
	// [Unread] entry, whose one note carries every effect of it ([program.6]).
	dirUnread

	// dirUnlistable is a directory outside the root the run cannot stat or list:
	// [LeftOutUnlistable].
	dirUnlistable

	// dirUnbounded is a directory a link or a Windows junction leads to that is a
	// strict ancestor of the walk's directory or on another device:
	// [LeftOutUnbounded].
	dirUnbounded

	// dirWalked is a directory the walk lists: its directories are met, and its
	// Go-named entries recorded where [metDir.records] says so.
	dirWalked

	// dirUnbuilt is a directory reached by a path strictly below the module root's
	// vendor directory that would otherwise be [dirUnread], [dirUnlistable] or
	// [dirUnbounded]: the go command builds no package of the module by that path,
	// so it has no record, and a path that builds the directory meets it anew.
	dirUnbuilt
)

// vendorSpelling is what a build naming a directory by one path below its
// module's root compiles of it and below it, by the go command's vendor rule
// ([program.4]): the module root's vendor directory itself builds, a directory
// strictly below it builds no package of the module by that path, and the same
// directory by any other path builds. Below vendor/ the go command refuses a
// directory vendor/modules.txt does not list, and builds one it lists, under
// -mod=vendor, as that dependency's package, which the run reads as a dependency.
// Each value builds no more than the one before it.
type vendorSpelling uint8

const (
	// spelledBuilding builds the directory and every directory below it by this
	// path.
	spelledBuilding vendorSpelling = iota

	// spelledVendorRoot is the module root's vendor directory itself, which builds,
	// and below which no package of the module builds by this path.
	spelledVendorRoot

	// spelledVendored is strictly below the module root's vendor directory, which
	// builds no package of the module by this path, at most a vendored
	// dependency's under -mod=vendor.
	spelledVendored
)

// reach is how the walk got to a directory, which the device bound reads.
type reach uint8

const (
	reachStart reach = iota // the walk's own directory
	reachPlain              // a directory entry of its parent
	reachLink               // a link or a Windows junction
)

// metDir is one directory the walk met: its outcome, its identity, the spelling
// of the path that met it, whether a link is on that path, and for [dirWalked] its
// entries and whether this meeting records it, where its spelling builds it and no
// earlier meeting's did.
type metDir struct {
	id       dirID
	entries  []fs.DirEntry
	outcome  dirOutcome
	spelling vendorSpelling
	records  bool
	linked   bool
}

// goTreeOf is the [goTree] of the module rooted at root whose toolchain runs from
// dir, at or below root.
func (top *Topology) goTreeOf(ctx context.Context, dir, root string) (goTree, error) {
	return top.goTreeWith(ctx, dir, root, hostFS)
}

// treeFS is what a [treeWalk] asks of the file system: Stat and ReadDir, which
// follow links, and the device holding an entry ([deviceOf]).
type treeFS struct {
	stat     func(path string) (fs.FileInfo, error)
	readDir  func(path string) ([]fs.DirEntry, error)
	deviceOf func(path string) any
}

// hostFS is the host's file system.
var hostFS = treeFS{stat: os.Stat, readDir: os.ReadDir, deviceOf: deviceOf}

// goTreeWith is [Topology.goTreeOf] asking fsys.
func (top *Topology) goTreeWith(ctx context.Context, dir, root string, fsys treeFS) (goTree, error) {
	w := &treeWalk{ctx: ctx, top: top, fsys: fsys, device: fsys.deviceOf(dir), seen: make(map[dirID]vendorSpelling), above: ancestorIDs(dir)}
	w.prefix, _ = scope.RootOf(root).Place(dir)
	if err := w.meet(dir, ".", reachStart); err != nil {
		return goTree{}, fmt.Errorf("load: find the Go below %s: %w", top.Rel(dir), err)
	}
	return w.tree, nil
}

// ancestorIDs is the identity of every directory strictly above dir, as the run
// spells it and where its links resolve.
func ancestorIDs(dir string) map[dirID]bool {
	held := make(map[dirID]bool)
	spellings := []string{dir}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		spellings = append(spellings, resolved)
	}
	for _, spelled := range spellings {
		for at := filepath.Dir(spelled); ; at = filepath.Dir(at) {
			if info, err := os.Stat(at); err == nil {
				held[idOf(at, info)] = true
			}
			if filepath.Dir(at) == at {
				break
			}
		}
	}
	return held
}

// treeWalk is one [Topology.goTreeOf]: the file system it asks, the path below
// its module's root of the directory it starts from, that directory's device, the
// identity of every directory entered with the most building spelling that entered
// it, the identity of every directory above it, and what it found.
type treeWalk struct {
	ctx    context.Context
	top    *Topology
	fsys   treeFS
	device any
	seen   map[dirID]vendorSpelling
	above  map[dirID]bool
	prefix string
	tree   goTree
}

// meet decides the directory at path, rel below the walk's directory through the
// links that reach it, and acts on the outcome.
func (w *treeWalk) meet(path, rel string, how reach) error {
	return w.enter(path, rel, how, how == reachLink)
}

// enter is [treeWalk.meet] where linked says whether a link is on the path.
func (w *treeWalk) enter(path, rel string, how reach, linked bool) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	met := w.outcome(path, rel, how)
	met.linked = linked
	return w.apply(path, rel, &met)
}

// apply records the outcome met of the directory at path, rel below the walk's
// directory, and walks it where it is [dirWalked]. [dirSeen] keeps the earlier
// meeting's record, [dirUnbuilt] and a [dirWalked] meeting that does not record
// the directory ([metDir.records]) leave none, and a value of no outcome is an
// error.
func (w *treeWalk) apply(path, rel string, met *metDir) error {
	switch met.outcome {
	case dirSeen:
	case dirModuleRoot:
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		w.tree.roots = append(w.tree.roots, linkedRoot{dir: resolved, at: path})
	case dirUnread:
		w.top.AddUnread(path)
	case dirUnlistable:
		w.tree.unloaded = append(w.tree.unloaded, LeftOutPackage{Dir: path, Reason: LeftOutUnlistable})
	case dirUnbounded:
		w.tree.unloaded = append(w.tree.unloaded, LeftOutPackage{Dir: path, Reason: LeftOutUnbounded})
	case dirWalked:
		return w.walk(path, rel, met)
	case dirUnbuilt:
	default:
		return fmt.Errorf("%w %d for %s", errNoOutcome, met.outcome, w.top.Rel(path))
	}
	return nil
}

// errNoOutcome reports a directory outcome [treeWalk.apply] has no record for.
var errNoOutcome = errors.New("no record for the directory outcome")

// outcome decides the directory at path, rel below the walk's directory, which
// the walk reached as how says: [dirUnread] inside the root and [dirUnlistable]
// outside it where Stat ([statKind]) or the listing fails, [dirModuleRoot] for
// another module's root, before the bound, and [dirUnbounded] where a link or a
// junction leads to a strict ancestor or another device ([treeWalk.unbounded]).
// By a path strictly below the module root's vendor directory, an unread,
// unlistable or unbounded directory is [dirUnbuilt]. One met before by a path
// building as much is [dirSeen], so a vendored path claims no directory.
func (w *treeWalk) outcome(path, rel string, how reach) metDir {
	spelled := w.spellingOf(rel)
	info, err := w.fsys.stat(path)
	if statKind(info, err) != directory {
		return w.unlisted(path, spelled)
	}
	met := metDir{id: idOf(path, info), spelling: spelled}
	switch {
	case how != reachStart && IsModuleRoot(path):
		met.outcome = dirModuleRoot
		return met
	case spelled == spelledVendored && w.unbounded(path, met.id, how):
		met.outcome = dirUnbuilt
		return met
	case w.unbounded(path, met.id, how):
		met.outcome = dirUnbounded
		return met
	}
	earlier, entered := w.seen[met.id]
	if entered && earlier <= met.spelling {
		met.outcome = dirSeen
		return met
	}
	w.seen[met.id] = met.spelling
	met.records = met.spelling != spelledVendored && (!entered || earlier == spelledVendored)
	if met.entries, err = w.fsys.readDir(path); err != nil {
		return w.unlisted(path, spelled)
	}
	met.outcome = dirWalked
	return met
}

// unbounded reports whether the directory at path of identity id, reached as how
// says, has no bound to walk: a strict ancestor of the walk's directory, or on
// another device where a link or a junction leads to it.
func (w *treeWalk) unbounded(path string, id dirID, how reach) bool {
	return w.above[id] || how == reachLink && w.fsys.deviceOf(path) != w.device
}

// unlisted is the outcome of a directory at path, reached by a path of spelling
// spelled, that the run cannot stat or list.
func (w *treeWalk) unlisted(path string, spelled vendorSpelling) metDir {
	switch {
	case spelled == spelledVendored:
		return metDir{outcome: dirUnbuilt, spelling: spelled}
	case w.top.Holds(path):
		return metDir{outcome: dirUnread, spelling: spelled}
	}
	return metDir{outcome: dirUnlistable, spelling: spelled}
}

// walk records the Go-named entries met lists of the directory at path, rel below
// the walk's directory, where met records it, and meets each directory it holds.
func (w *treeWalk) walk(path, rel string, met *metDir) error {
	d := treeDir{id: met.id, pattern: "./" + rel, linked: met.linked}
	if rel == "." {
		d.pattern = "."
	}
	for _, entry := range met.entries {
		child := filepath.Join(path, entry.Name())
		switch mode, how := w.kindOfEntry(child, entry); {
		case mode.IsDir():
			if err := w.enter(child, pathpkg.Join(rel, entry.Name()), how, met.linked || how == reachLink); err != nil {
				return err
			}
		case mode.IsRegular():
			d.regular = d.regular || filepath.Ext(entry.Name()) == goFileExt
			if buildconstraint.IsGoFile(entry.Name()) {
				d.files = append(d.files, entry.Name())
			}
		case goRow.Sources(entry.Name()):
			d.irregular = append(d.irregular, entry.Name())
		}
	}
	if d.regular && met.records {
		w.tree.dirs = append(w.tree.dirs, d)
	}
	return nil
}

// kindOfEntry is the type of the entry at path a listing gave as entry, and how a
// walk reaches it where it is a directory. Where the listing gives no directory
// and no regular file (a link, a Windows junction, which Go 1.23 reports as
// irregular, a pipe, a device or a socket), Stat, which follows links and opens
// nothing, answers, classified by [statKind]: a link to nothing, through a file,
// that loops or whose name is too long to look up is irregular, and any other
// error makes the entry a directory [treeWalk.outcome] cannot stat. A listed
// junction is a link ([listedLink]).
func (w *treeWalk) kindOfEntry(path string, entry fs.DirEntry) (fs.FileMode, reach) {
	mode := entry.Type()
	switch {
	case mode.IsDir() && listedLink(path):
		return mode, reachLink
	case mode.IsDir():
		return mode, reachPlain
	case mode.IsRegular():
		return mode, reachPlain
	}
	switch statKind(w.fsys.stat(path)) {
	case File:
		return 0, reachLink
	case directory, Unread:
		return fs.ModeDir, reachLink
	}
	return fs.ModeIrregular, reachLink
}

// spellingOf is the spelling of the path rel below the walk's directory: below
// the module root's vendor directory, that directory itself or neither. A vendor
// directory deeper in the module builds where a pattern names it.
func (w *treeWalk) spellingOf(rel string) vendorSpelling {
	switch below, under := scope.UnderName(vendorDirName, pathpkg.Join(w.prefix, rel)); {
	case !under:
		return spelledBuilding
	case below == "":
		return spelledVendorRoot
	}
	return spelledVendored
}

// throughVendor reports whether the import path names a package below a vendor
// directory, which no import may name: a vendored package is imported by its own
// module's path (cmd/go FindVendor). A vendor directory itself, the path's last
// element, is no such package.
func throughVendor(importPath string) bool {
	return strings.Contains("/"+importPath, "/vendor/")
}

// treeModule is one module the configuration loaded, with the load its missed
// directories are listed under, and whether it is a unit.
type treeModule struct {
	module *discoveredModule
	spec   *loadSpec
	unit   bool
}

// missed lists, for every module the configuration loaded, each directory of its
// [goTree] that no load of the configuration listed ([program.4]), each package
// that lists going to [Result.leftOutPackages], and leaves out unloaded each
// directory the walk left out. Each directory the run's read set judges is a
// [fileCandidate]: one ./... does not name or of a module that is no unit, judged
// as named where the pattern names it, and one that does not list here, or holds
// an entry the configuration opens that is no regular file, judged by every Go
// file, that entry among them.
func (l *loader) missed(r *Result) error {
	listed := listedPackageDirs(slices.Concat(r.Packages, l.loaded), r.Nested, r.Consumers)
	for _, m := range l.treeModules(r) {
		for i := range m.module.tree.unloaded {
			left := m.module.tree.unloaded[i]
			left.Module = m.module.Path
			l.unloaded = append(l.unloaded, left)
		}
		unlisted := l.unlistedDirs(m, listed)
		listedAlone, err := l.listMissed(m, unlisted)
		if err != nil {
			return err
		}
		for i := range unlisted {
			if d := &unlisted[i]; !listedAlone[i] || m.differs(d) || !m.unit {
				l.candidates = append(l.candidates, l.candidateOf(m, d, listedAlone[i] && !m.differs(d)))
			}
		}
	}
	return nil
}

// unlistedDirs is each directory of m's tree that no load of the configuration
// listed and that holds no entry the configuration opens that is no regular file,
// marking each directory of the tree in listed. It adds to l.candidates each
// listed directory the run's read set judges and each holding such an entry.
func (l *loader) unlistedDirs(m treeModule, listed map[dirID]bool) []treeDir {
	var unlisted []treeDir
	for i := range m.module.tree.dirs {
		d := &m.module.tree.dirs[i]
		switch {
		case listed[d.id] && (m.differs(d) || !m.unit):
			l.candidates = append(l.candidates, l.candidateOf(m, d, !m.differs(d)))
		case listed[d.id]:
		case l.c.opens(d.irregular):
			c := l.candidateOf(m, d, false)
			c.files = append(c.files, d.irregular...)
			l.candidates = append(l.candidates, c)
		default:
			unlisted = append(unlisted, *d)
		}
		listed[d.id] = true
	}
	return unlisted
}

// differs reports whether the module's ./... pattern does not name d: a link is
// on its path, or a directory between the module's and d's, d included, is one the
// pattern skips ([ModuleTree.outside]).
func (m treeModule) differs(d *treeDir) bool {
	return d.linked || TreeOf(m.module.File).outside(scope.RootOf(m.module.Dir), m.dirOf(d.pattern))
}

// candidateOf is the [fileCandidate] of d, a directory of m's tree, judged as
// one a listing names where named says so, each of its Go files a file only the
// ignore tag builds, which no configuration the run derives sets, or one
// [Topology.Unread] notes exempt.
func (l *loader) candidateOf(m treeModule, d *treeDir, named bool) fileCandidate {
	dir := m.dirOf(d.pattern)
	c := fileCandidate{id: d.id, dir: dir, module: m.module.Path, named: named}
	for _, name := range d.files {
		if path := filepath.Join(dir, name); l.top.recordedUnread(path) || onlyUnderIgnore(path) {
			c.exempt = append(c.exempt, name)
		} else {
			c.files = append(c.files, name)
		}
	}
	return c
}

// onlyUnderIgnore reports whether the regular Go file at path builds only under
// the ignore tag ([buildconstraint.OnlyUnderIgnore]); one it cannot read or whose
// constraint does not parse may build.
func onlyUnderIgnore(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	expression, err := buildconstraint.Of(filepath.Base(path), content)
	return err == nil && buildconstraint.OnlyUnderIgnore(expression)
}

// treeModules is every module the configuration loaded, in load order, each with
// the load of its own directory.
func (l *loader) treeModules(r *Result) []treeModule {
	dropped := func(m *discoveredModule) bool {
		return slices.ContainsFunc(r.Dropped, func(d Dropped) bool { return scope.SameName(d.Dir, m.Dir) })
	}
	var held []treeModule
	for g, group := range [][]discoveredModule{l.top.Units, l.top.Nested, l.top.Readers} {
		for i := range group {
			if m := &group[i]; m.Refused == nil && !dropped(m) {
				held = append(held, treeModule{module: m, spec: specOf(m), unit: g == 0})
			}
		}
	}
	for i := range l.top.Consumers {
		m := &l.top.Consumers[i]
		held = append(held, treeModule{module: m, spec: &loadSpec{dir: m.Dir, workspace: l.top.ConsumerWorkspace, file: m.File}})
	}
	return held
}

// dirOf is the directory pattern names below m's directory.
func (m treeModule) dirOf(pattern string) string {
	return filepath.Join(m.module.Dir, filepath.FromSlash(pattern))
}

// listMissed lists the directories dirs of m together, or each alone where they
// do not list together, adding what lists to l.loaded, and reports for each
// whether it listed alone or with the others.
func (l *loader) listMissed(m treeModule, dirs []treeDir) ([]bool, error) {
	patterns := make([]string, len(dirs))
	listed := make([]bool, len(dirs))
	for i := range dirs {
		patterns[i] = dirs[i].pattern
	}
	if len(patterns) > 1 {
		together, err := l.metadata(m.spec, patterns)
		if err != nil || together {
			for i := range listed {
				listed[i] = together
			}
			return listed, err
		}
	}
	for i := range dirs {
		alone, err := l.metadata(m.spec, patterns[i:i+1])
		if err != nil {
			return nil, err
		}
		listed[i] = alone
	}
	return listed, nil
}

// metadata lists patterns under spec and the configuration, adding the packages to
// l.loaded where the listing reports no error but the one of a directory the
// configuration builds nothing of, and reports whether it did. Such a listing
// reads none of the files the configuration excludes, which the run's read set
// holds only where another configuration compiles them ([fileCandidate.unreadBy]).
// Placement reads the package graph alone, so nothing is type-checked.
func (l *loader) metadata(spec *loadSpec, patterns []string) (bool, error) {
	at := *spec
	at.top, at.toolchain = l.top, l.toolchain
	cfg, err := listConfig(l.ctx, l.c, &at)
	if err != nil {
		return false, err
	}
	pkgs, err := l.trees.list(cfg, patterns)
	switch {
	case l.ctx.Err() != nil:
		return false, fmt.Errorf("load %s: %w", l.c.ID, l.ctx.Err())
	case err != nil:
		return false, nil
	}
	if err := dropUnbuiltErrors(l.top.root, pkgs, l.c); err != nil {
		return false, nil
	}
	pkgs = dropUnreadErrors(pkgs, &at)
	if reportsErrors(pkgs) {
		return false, nil
	}
	l.loaded = append(l.loaded, withoutTestBinaries(pkgs)...)
	return true, nil
}

// dirIDOf is the identity of the directory at path, links followed, and whether
// Stat answered.
func dirIDOf(path string) (dirID, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return dirID{}, false
	}
	return idOf(path, info), true
}

// listedPackageDirs is the identity of the directory of every package the graphs of roots
// and of every consumer's packages hold.
func listedPackageDirs(roots []*packages.Package, consumers ...[]Consumer) map[dirID]bool {
	for _, group := range consumers {
		for i := range group {
			roots = append(roots, group[i].Packages...)
		}
	}
	held := make(map[dirID]bool)
	stated := make(map[string]bool)
	packages.Visit(roots, nil, func(p *packages.Package) {
		for _, dir := range []string{p.Dir, packageDir(p)} {
			if dir == "" || stated[dir] {
				continue
			}
			stated[dir] = true
			if id, ok := dirIDOf(dir); ok {
				held[id] = true
			}
		}
	})
	return held
}

// opens reports whether a build of c opens one of the files names, by its name
// alone, as go/build does before it reads a file's constraints: one whose
// _GOOS, _GOARCH or _GOOS_GOARCH suffix names another platform, or whose name
// starts with an underscore or a dot, is never opened, and a test file is, since
// the go command reads every package's test files. Nothing is opened here.
func (c *Configuration) opens(names []string) bool {
	ctxt := cgoEnabledContext(c)
	ctxt.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("package p\n")), nil
	}
	return slices.ContainsFunc(names, func(name string) bool {
		matched, err := ctxt.MatchFile(".", name)
		return matched || err != nil
	})
}
