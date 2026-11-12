package load

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

// The names of the files the go command reads outside its overlay, which
// [Topology.meetBlocking] reads the kind of instead ([program.6]).
const (
	workspaceFileName = "go.work"
	vendorListName    = "modules.txt"
)

// hideKind is how every toolchain the run starts reads one hidden entry.
type hideKind uint8

const (
	// absent entries the overlay deletes.
	absent hideKind = iota

	// unparsable module files the overlay replaces with [unparsableModuleFile]:
	// their module keeps its boundary, which the go command finds by the file's
	// presence, and a load that reads the file fails naming it.
	unparsable
)

// hiddenEntry is one entry of a tree a load may read that every toolchain the run
// starts reads as [hideKind] says ([program.6]).
type hiddenEntry struct {
	path string
	kind hideKind

	// viaLink reports an entry at a path through a symbolic link to a directory,
	// which puts it in no module's Go tree: the go command's wildcard follows none.
	viaLink bool
}

// hides reports whether the entry at path, links followed, is neither a regular
// file nor a directory: a pipe or a device, which the go command may block on or
// read without end, or a socket, which it cannot open.
func hides(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.Mode().IsRegular() && !info.IsDir()
}

// dirNode is one directory the scan walked: the path it walked it at, each entry
// there that leads to a directory, and each file there the overlay hides.
type dirNode struct {
	path  string
	edges []dirEdge
	files []hiddenEntry
}

// dirEdge is the entry name of a walked directory that leads to the directory to:
// a subdirectory, or a symbolic link to one.
type dirEdge struct {
	name string
	to   dirID
	link bool
}

// scanRoot is a path the scan walked a tree from, and the directory there.
type scanRoot struct {
	path string
	id   dirID
}

// hideScan is one walk for the entries the run's loads hide. It follows no
// symbolic link, and walks each directory once, by [dirID], however a path
// spells it.
type hideScan struct {
	ctx    context.Context
	top    *Topology
	queued map[string]bool
	read   map[string]bool // the module roots and workspace files whose directives were read
	queue  []string
	local  []scope.Replacement // every module path a load may resolve to a directory of the scan
}

// hide hides from every load of the topology each pipe, device and socket it may
// read ([program.6]). The scan walks the target root, the directory every load
// runs from, every module those loads use and every module a replace directive of
// their module or workspace files names, transitively, with the vendor directory
// beside each workspace file. It reads directives from no other module file, a
// fixture's among them, and keeps each load's own vendor list, which the go
// command reads outside the overlay.
func (top *Topology) hide(ctx context.Context) error {
	top.nodes, top.kept = make(map[dirID]*dirNode), make(map[string]bool)
	s := &hideScan{ctx: ctx, top: top, queued: make(map[string]bool), read: make(map[string]bool)}
	s.enqueue(top.Root)
	loads := top.loads()
	for i := range loads {
		m := &loads[i]
		s.enqueue(m.Dir)
		if m.File != nil {
			s.module(filepath.Dir(m.File.Path))
		}
		for _, workspace := range []string{m.Workspace, workspaceOff} {
			top.kept[vendorList(m.File, m.Dir, workspace)] = true
			if workspace != workspaceOff {
				s.workspace(workspace)
			}
		}
	}
	for len(s.queue) > 0 {
		dir := s.queue[0]
		s.queue = s.queue[1:]
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		id := idOf(dir, info)
		top.roots = append(top.roots, scanRoot{path: dir, id: id})
		if err := s.visit(dir, id); err != nil {
			return fmt.Errorf("load: find the entries the toolchain may block on: %w", err)
		}
	}
	top.local = s.local
	top.hidden = top.spell()
	return nil
}

// loads is every module a load of the topology runs from, each with the GOWORK
// setting it loads under: the units, the nested modules, the readers and the
// declared consumers.
func (top *Topology) loads() []discoveredModule {
	held := slices.Concat(top.Units, top.Nested, top.Readers)
	for i := range top.Consumers {
		c := top.Consumers[i]
		c.Workspace = top.ConsumerWorkspace
		held = append(held, c)
	}
	return held
}

// enqueue queues dir for the scan, once.
func (s *hideScan) enqueue(dir string) {
	if !s.queued[dir] {
		s.queued[dir] = true
		s.queue = append(s.queue, dir)
	}
}

// module queues the module rooted at dir, which a load uses, and every module a
// replace directive of its module file names, transitively.
func (s *hideScan) module(dir string) {
	if s.read[dir] {
		return
	}
	s.read[dir] = true
	s.enqueue(dir)
	path := filepath.Join(dir, moduleFileName)
	text, err := scope.ReadRegularFile(s.top.Root, path)
	if err != nil {
		return
	}
	f, err := modfile.Parse(path, text, nil)
	if err != nil {
		return
	}
	if f.Module != nil {
		s.local = append(s.local, scope.Replacement{Module: f.Module.Mod.Path, Dir: dir})
	}
	s.replaced(scope.Replacements(dir, f.Replace))
}

// replaced queues every module of replacements: a directory holding a module file,
// which the go command requires of a replacement it reads.
func (s *hideScan) replaced(replacements []scope.Replacement) {
	for _, r := range replacements {
		s.local = append(s.local, r)
		if IsModuleRoot(r.Dir) {
			s.module(r.Dir)
		}
	}
}

// workspace queues every module the workspace file at path lists or a replace
// directive of it names, and the vendor directory beside it.
func (s *hideScan) workspace(path string) {
	if s.read[path] {
		return
	}
	s.read[path] = true
	work, err := scope.ReadWorkspace(s.top.Root, path)
	if err != nil {
		return
	}
	for _, member := range work.Modules {
		if IsModuleRoot(member) {
			s.module(member)
		}
	}
	s.replaced(work.Replacements)
	s.enqueue(filepath.Join(filepath.Dir(path), vendorDirName))
}

// visit walks the directory id at path unless the scan walked it already.
func (s *hideScan) visit(path string, id dirID) error {
	if _, walked := s.top.nodes[id]; walked {
		return nil
	}
	n := &dirNode{path: path}
	s.top.nodes[id] = n
	// The other walks record a directory the run cannot list.
	entries, _ := os.ReadDir(path)
	for _, entry := range entries {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if err := s.entry(n, entry.Name(), entry.Type()); err != nil {
			return err
		}
	}
	return nil
}

// entry records or walks the entry name, of type typ, of the directory n. It
// enters every directory the go command may read a package from, one whose name
// starts with a dot or an underscore included, but none that roots another
// module, which the scan walks only where a load uses it, and no [objectStore],
// which git fills with regular files only.
func (s *hideScan) entry(n *dirNode, name string, typ fs.FileMode) error {
	path := filepath.Join(n.path, name)
	switch KindOf(path, typ, os.Stat) {
	case directory:
		if IsModuleRoot(path) || objectStore(path) {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		id := idOf(path, info)
		n.edges = append(n.edges, dirEdge{name: name, to: id})
		return s.visit(path, id)
	case leftOut:
		s.leftOut(n, name, path)
	}
	return nil
}

// objectStore reports whether dir is a directory named objects whose parent is
// named .git. It infers no store from git's layout, a HEAD file and a refs
// directory beside it, since a package named objects may sit beside both.
func objectStore(dir string) bool {
	return filepath.Base(dir) == "objects" && filepath.Base(filepath.Dir(dir)) == ".git"
}

// leftOut records the entry name of n at path, which no walk of the run opens: a
// file [hides] holds for, but a workspace file and a kept vendor list, or a
// symbolic link to a directory that roots no module.
func (s *hideScan) leftOut(n *dirNode, name, path string) {
	if hides(path) {
		switch {
		case name == workspaceFileName || s.top.kept[path]:
		case name == moduleFileName:
			n.files = append(n.files, hiddenEntry{path: path, kind: unparsable})
		default:
			n.files = append(n.files, hiddenEntry{path: path, kind: absent})
		}
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || IsModuleRoot(path) {
		return
	}
	n.edges = append(n.edges, dirEdge{name: name, to: idOf(path, info), link: true})
}

// spell is every entry the topology hides, in path order: each file the scan hid,
// at its path below each root through subdirectories, and at its path through
// each symbolic link met there that leads to a directory the scan walked, one link
// deep ([program.6]). A link to a directory the scan did not walk adds no path,
// and neither does a second link below the first: the go command opens an entry
// it reaches only through such a path, as it does with no run.
func (top *Topology) spell() []hiddenEntry {
	e := &expansion{top: top, held: make(map[spelling][]hiddenEntry), open: make(map[spelling]bool)}
	hidden := make([]hiddenEntry, 0, len(top.roots))
	for _, root := range top.roots {
		hidden = append(hidden, under(root.path, e.of(spelling{id: root.id, links: 1}), false)...)
	}
	slices.SortFunc(hidden, func(a, b hiddenEntry) int { return strings.Compare(a.path, b.path) })
	return slices.CompactFunc(hidden, func(a, b hiddenEntry) bool { return a.path == b.path })
}

// spelling is a walked directory and the number of symbolic links a path below it
// may still pass through.
type spelling struct {
	id    dirID
	links int
}

// expansion is one [Topology.spell]: the hidden entries below each spelling,
// relative to its directory, and the spellings it is expanding.
type expansion struct {
	top  *Topology
	held map[spelling][]hiddenEntry
	open map[spelling]bool
}

// of is every hidden entry at or below the directory of at, at its path relative
// to it through subdirectories and at most at.links links, once per spelling.
func (e *expansion) of(at spelling) []hiddenEntry {
	if held, done := e.held[at]; done {
		return held
	}
	n := e.top.nodes[at.id]
	e.open[at] = true
	held := make([]hiddenEntry, 0, len(n.files))
	for _, f := range n.files {
		held = append(held, hiddenEntry{path: filepath.Base(f.path), kind: f.kind})
	}
	for _, edge := range n.edges {
		_, walked := e.top.nodes[edge.to]
		inner := spelling{id: edge.to, links: at.links}
		switch {
		case !edge.link && e.open[inner]:
			// A directory below itself, through a mount: the walk met it once.
		case !edge.link:
			held = append(held, under(edge.name, e.of(inner), false)...)
		case walked && at.links > 0:
			inner.links--
			held = append(held, under(edge.name, e.of(inner), true)...)
		}
	}
	delete(e.open, at)
	e.held[at] = held
	return held
}

// under is entries at their paths below dir, through a link where viaLink says.
func under(dir string, entries []hiddenEntry, viaLink bool) []hiddenEntry {
	moved := make([]hiddenEntry, len(entries))
	for i, h := range entries {
		h.path = filepath.Join(dir, h.path)
		h.viaLink = h.viaLink || viaLink
		moved[i] = h
	}
	return moved
}

// vendorList is the vendor list the toolchain run in dir under workspace may read
// outside the overlay: beside the workspace file, else beside the module file that
// governs dir (cmd/go modload VendorDir).
func vendorList(file *ModuleFile, dir, workspace string) string {
	switch {
	case workspace != workspaceOff:
		return filepath.Join(filepath.Dir(workspace), vendorDirName, vendorListName)
	case file != nil:
		return filepath.Join(filepath.Dir(file.Path), vendorDirName, vendorListName)
	default:
		return filepath.Join(dir, vendorDirName, vendorListName)
	}
}

// vendorDirName is the directory a build reads its dependencies from in vendor mode.
const vendorDirName = "vendor"

// unparsableModuleFile is the module file an [unparsable] entry reads as: a string
// its first line ends inside, which the go command's parser refuses even in a
// dependency's module file, naming the file it replaces.
var unparsableModuleFile = []byte("module \"\n")

// writeOverlay writes the go command's overlay file that hides every hidden entry,
// with the module file [unparsable] entries read as, and returns its path, empty
// where none is hidden, and the function that removes both.
func (top *Topology) writeOverlay() (path string, remove func(), err error) {
	if len(top.hidden) == 0 {
		return "", func() {}, nil
	}
	dir, err := os.MkdirTemp("", "deadset-overlay-*")
	if err != nil {
		return "", nil, err
	}
	remove = func() { _ = os.RemoveAll(dir) }
	moduleFile := filepath.Join(dir, moduleFileName)
	replace := make(map[string]string, len(top.hidden))
	for _, h := range top.hidden {
		replace[h.path] = ""
		if h.kind == unparsable {
			replace[h.path] = moduleFile
		}
	}
	text, err := json.Marshal(struct{ Replace map[string]string }{replace})
	if err == nil {
		err = os.WriteFile(moduleFile, unparsableModuleFile, 0o600)
	}
	path = filepath.Join(dir, "overlay.json")
	if err == nil {
		err = os.WriteFile(path, text, 0o600)
	}
	if err != nil {
		remove()
		return "", nil, err
	}
	return path, remove, nil
}

// obscured is every hidden entry with a source file's name in a directory that
// holds a package r lists or in a package directory of one of r's units, whose
// code the analysis does not see, and the directory of each such entry and of
// each unopenable Go file there ([program.6]).
func (top *Topology) obscured(r *Result) (hidden, dirs []string) {
	listed := make(map[string]bool)
	for _, held := range [][]*packages.Package{r.Packages, packagesOf(r.Nested), packagesOf(r.Consumers)} {
		packages.Visit(held, nil, func(p *packages.Package) { listed[p.Dir] = true })
	}
	in := func(path string, viaLink bool) bool {
		return listed[filepath.Dir(path)] || !viaLink && r.inUnitTree(filepath.Dir(path))
	}
	for _, h := range top.hidden {
		if buildconstraint.IsSourceFile(filepath.Base(h.path)) && in(h.path, h.viaLink) {
			hidden = append(hidden, h.path)
			dirs = append(dirs, filepath.Dir(h.path))
		}
	}
	for _, path := range top.Unread {
		if buildconstraint.IsSourceFile(filepath.Base(path)) && in(path, false) {
			dirs = append(dirs, filepath.Dir(path))
		}
	}
	slices.Sort(dirs)
	return hidden, slices.Compact(dirs)
}

// inUnitTree reports whether dir is a package directory of the Go tree of one of
// r's units ([program.4]).
func (r *Result) inUnitTree(dir string) bool {
	return slices.ContainsFunc(r.Units, func(u Unit) bool {
		return (sameDir(u.Dir, dir) || scope.Below(u.Dir, dir)) && treeAt(u.Dir).within(u.Dir, dir)
	})
}

// packagesOf is every package of consumers.
func packagesOf(consumers []Consumer) []*packages.Package {
	var held []*packages.Package
	for i := range consumers {
		held = append(held, consumers[i].Packages...)
	}
	return held
}

// missingSum opens every toolchain message about a checksum a module sum file
// lacks, of a package's module or of a module file.
const missingSum = "missing go.sum entry"

// hiddenCause is the failure of a load of spec that failed because of an entry
// the run hid, and nil where it follows from none ([program.6]): text, the go
// command's refusal, or an error of pkgs names a hidden module file; a checksum is
// missing where a module sum file the load reads is hidden; or a package that
// reports an error other than a syntax error, or one it imports, lies in a
// directory holding a hidden source file, a test file only for the package
// itself.
func (top *Topology) hiddenCause(spec *loadSpec, text string, pkgs []*packages.Package) error {
	if top == nil || len(top.hidden) == 0 {
		return nil
	}
	f := top.failureOf(text, pkgs)
	if h, found := top.hiddenRead(spec, f.texts); found {
		return top.hiddenFailure(h)
	}
	if h, found := top.hiddenSource(f.own, f.imported); found {
		return top.hiddenFailure(h)
	}
	return nil
}

// failedLoad is what a failed load tells of its cause: the messages of its
// refusal and of its packages' errors, and the directories of each package that
// reports an error other than a syntax error, own, and of the packages those
// import, imported.
type failedLoad struct {
	texts, own, imported []string
}

// failureOf is what a load that failed with text, the go command's refusal, or
// with the errors of pkgs tells of its cause.
func (top *Topology) failureOf(text string, pkgs []*packages.Package) failedLoad {
	f := failedLoad{texts: []string{text}}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		caused := false
		for _, e := range p.Errors {
			f.texts = append(f.texts, e.Msg)
			if e.Kind != packages.ParseError {
				caused = true
				f.own = append(f.own, top.dirsOf(packageNamed(e.Msg))...)
			}
		}
		if !caused {
			return
		}
		f.own = append(f.own, top.packageDirs(p)...)
		for _, dependency := range p.Imports {
			f.imported = append(f.imported, top.packageDirs(dependency)...)
		}
	})
	return f
}

// hiddenRead is the hidden file a load of spec failed reading, by the messages
// texts, and whether there is one: a hidden module file one of them names, or, where
// one tells of a missing checksum, a hidden module sum file the load reads.
func (top *Topology) hiddenRead(spec *loadSpec, texts []string) (hiddenEntry, bool) {
	holds := func(part string) bool {
		return slices.ContainsFunc(texts, func(t string) bool { return strings.Contains(t, part) })
	}
	for _, h := range top.hidden {
		if h.kind == unparsable && holds(h.path) {
			return h, true
		}
	}
	if !holds(missingSum) {
		return hiddenEntry{}, false
	}
	for _, sum := range top.sumFiles(spec) {
		if i := slices.IndexFunc(top.hidden, func(h hiddenEntry) bool { return h.path == sum }); i >= 0 {
			return top.hidden[i], true
		}
	}
	return hiddenEntry{}, false
}

// hiddenSource is the hidden source file of a package directory of own or
// imported that a failed load needed, and whether there is one: a test file
// counts only in own.
func (top *Topology) hiddenSource(own, imported []string) (hiddenEntry, bool) {
	for i, dir := range slices.Concat(own, imported) {
		for _, h := range top.hidden {
			name := filepath.Base(h.path)
			if h.kind == absent && filepath.Dir(h.path) == dir && buildconstraint.IsSourceFile(name) &&
				(i < len(own) || !strings.HasSuffix(name, "_test.go")) {
				return h, true
			}
		}
	}
	return hiddenEntry{}, false
}

// packageDirs is the directory of p, or, where the load found none, every
// directory of the scan its import path may name.
func (top *Topology) packageDirs(p *packages.Package) []string {
	if p.Dir != "" {
		return []string{p.Dir}
	}
	return top.dirsOf(p.PkgPath)
}

// dirsOf is every directory of the scan the import path names below a module a
// load may resolve it to ([Topology.local]).
func (top *Topology) dirsOf(importPath string) []string {
	if importPath == "" {
		return nil
	}
	var dirs []string
	for _, m := range top.local {
		if rest, found := strings.CutPrefix(importPath, m.Module); found && (rest == "" || rest[0] == '/') {
			dirs = append(dirs, filepath.Join(m.Dir, filepath.FromSlash(rest)))
		}
	}
	return dirs
}

// sumFiles is every module sum file a load of spec reads checksums from: the
// workspace's and each of its modules', else its own module's (cmd/go modload
// WorkspaceGoSumFiles and modfetch GoSumFile).
func (top *Topology) sumFiles(spec *loadSpec) []string {
	if spec.workspace != workspaceOff {
		files := []string{spec.workspace + ".sum"}
		if work, err := scope.ReadWorkspace(top.Root, spec.workspace); err == nil {
			for _, member := range work.Modules {
				files = append(files, filepath.Join(member, "go.sum"))
			}
		}
		return files
	}
	if spec.file != nil {
		return []string{strings.TrimSuffix(spec.file.Path, ".mod") + ".sum"}
	}
	return nil
}

// hiddenFailure is the setup failure of a load that needs the hidden entry h,
// naming it relative to the root ([program.10]) and what to make a regular file.
func (top *Topology) hiddenFailure(h hiddenEntry) error {
	return failure.WrapFix(vocab.SetupUnreadableConfiguration, fmt.Errorf(
		"load: %s: %w, so the run hides it from the go command, and the load needs it",
		top.rel(h.path), scope.ErrIrregular,
	), scope.IrregularFix)
}
