package load

import (
	"context"
	"errors"
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
)

// moduleFileName is the file that makes a directory the root of a module.
const moduleFileName = "go.mod"

// ErrModuleFile reports a module file that cannot be read or parsed.
var ErrModuleFile = errors.New("load: unreadable module file")

// goRow is the Go row of the Contract's language table.
var goRow, _ = vocab.LanguageRowOf(vocab.Go)

// leavesOut reports whether a directory of that name, met below where a search or a
// load starts, is one the Go program leaves out ([program.4]): one the Go row of the
// language table skips.
func leavesOut(name string) bool { return goRow.Skips(name) }

// ModuleTree is the Go of one module ([program.4]): every directory below where a
// walk of it starts but one [leavesOut] names and one the module ignores.
type ModuleTree struct {
	root             string
	rooted, anywhere []string
}

// TreeOf is the tree of the module whose module file is f, which ignores nothing
// where f is nil.
func TreeOf(f *ModuleFile) ModuleTree {
	if f == nil {
		return ModuleTree{}
	}
	t := ModuleTree{root: filepath.Dir(f.Path)}
	for _, ignore := range f.Syntax.Ignore {
		if rel, rooted := strings.CutPrefix(ignore.Path, "./"); rooted {
			t.rooted = append(t.rooted, enclosed(rel))
		} else {
			t.anywhere = append(t.anywhere, enclosed(ignore.Path))
		}
	}
	return t
}

// Skips reports whether a walk of t skips the directory at path, met below where
// it starts.
func (t ModuleTree) Skips(path string) bool {
	return leavesOut(filepath.Base(path)) || t.ignores(path)
}

// outside reports whether dir, strictly below the directory unit loads from, holds
// a package of t's module that is none of unit's packages ([program.4]): a
// directory between them, dir included, is one t skips, and none roots a module.
func (t ModuleTree) outside(unit, dir string) bool {
	if !scope.Below(unit, dir) {
		return false
	}
	skipped := false
	for at := dir; at != unit && filepath.Dir(at) != at; at = filepath.Dir(at) {
		if IsModuleRoot(at) {
			return false
		}
		skipped = skipped || t.Skips(at)
	}
	return skipped
}

// within reports whether dir, at or below the directory unit loads from, is one of
// unit's package directories: no directory between them, dir included, is one t
// skips or roots a module.
func (t ModuleTree) within(unit, dir string) bool {
	for at := dir; at != unit && filepath.Dir(at) != at; at = filepath.Dir(at) {
		if IsModuleRoot(at) || t.Skips(at) {
			return false
		}
	}
	return true
}

// OutsideDir is the directory the import path names in t's module, whose module
// path is module, where it holds a package outside unit's packages that the
// module's build compiles all the same when a package of unit imports it
// ([program.4]), and false for any other path. A path through a vendor directory,
// other than as its last element, is none: the go command refuses it, as a
// vendored package is imported by its own module's path (cmd/go FindVendor).
func (t ModuleTree) OutsideDir(module, unit, importPath string) (string, bool) {
	rest, found := strings.CutPrefix(importPath, module)
	vendored := strings.Contains(importPath, "/vendor/") || strings.HasPrefix(importPath, "vendor/")
	if module == "" || !found || (rest != "" && rest[0] != '/') || vendored {
		return "", false
	}
	dir := filepath.Join(t.root, filepath.FromSlash(rest))
	return dir, t.outside(unit, dir)
}

// ignores reports whether the module file's ignore directive names the directory at
// path, as the go command matches it: a ./ path is a prefix of path's elements below
// the module root, any other path a run of them.
func (t ModuleTree) ignores(path string) bool {
	if t.root == "" || !scope.Below(t.root, path) {
		return false
	}
	dir := enclosed(unitPrefix(t.root, path))
	return slices.ContainsFunc(t.rooted, func(p string) bool { return strings.HasPrefix(dir, p) }) ||
		slices.ContainsFunc(t.anywhere, func(p string) bool { return strings.Contains(dir, p) })
}

// enclosed is the path p in forward slashes between a leading and a trailing slash,
// so a prefix or a substring of one enclosed path in another ends at whole elements.
func enclosed(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// treeAt is the tree of the module the toolchain run in dir reads, the one whose
// module file is in dir or the nearest directory above it, which ignores nothing
// where that module file cannot be read: the load reports that file.
func treeAt(dir string) ModuleTree {
	for at := dir; ; at = filepath.Dir(at) {
		if IsModuleRoot(at) {
			f, _ := readModuleFile(scope.RootOf(at), at)
			return TreeOf(f)
		}
		if filepath.Dir(at) == at {
			return ModuleTree{}
		}
	}
}

// leftOutBelow reports whether path lies strictly below dir inside a directory
// [leavesOut] names, path's own last element included.
func leftOutBelow(dir, path string) bool {
	return slices.ContainsFunc(strings.Split(unitPrefix(dir, path), "/"), leavesOut)
}

// Topology is what every configuration of one run loads, decided once from the
// scope and the file system: the units of the target, the modules below it that
// build against a unit's directory, the workspace readers, and the declared
// consumers, each with the module file its toolchain reads.
type Topology struct {
	// hidden is every entry a load may read that every toolchain the run starts
	// reads as [hideKind] says ([Topology.spell]), in path order, and local every
	// module path a load may resolve to a directory the scan walked.
	hidden []hiddenEntry
	local  []scope.Replacement

	// nodes is every directory the scan walked, from roots, by [dirID], and kept
	// the vendor lists [Topology.meetBlocking] read instead of hiding them.
	nodes map[dirID]*dirNode
	roots []scanRoot
	kept  map[string]bool

	// envOff holds each directory a toolchain of the run starts in whose go env
	// file is in EnvFiles.
	envOff map[string]bool

	// root is Root resolved once, which every path a setup failure or a note names
	// is relative to ([Topology.Rel]).
	root scope.Root

	// Root is the target root every position of the run is relative to, and
	// ConsumerWorkspace the GOWORK setting every declared consumer loads under.
	Root              string
	ConsumerWorkspace string

	// EnvFiles holds each go env file a toolchain of the run would read that is no
	// regular file, in path order: every toolchain that would read one runs with
	// GOENV=off ([program.6]).
	EnvFiles []EnvFile

	// Units holds one module per unit of the target ([program.4]), in directory
	// order.
	Units []discoveredModule

	// Nested holds each module below the target, in directory order, that requires
	// a unit and replaces it with that unit's directory and that no scope declares.
	Nested []discoveredModule

	// Readers holds each module the workspace listing the target lists outside it
	// that no scope declares and whose module file requires a unit or cannot be
	// read, in the workspace's order, whatever named the scope.
	Readers []discoveredModule

	// Consumers holds the declared consumers, in the order the scope declares them.
	Consumers []discoveredModule

	// Unread holds each entry at or below the root a walk of the tree met as
	// [Unread], and each Go file there the search or the derivation of the matrix
	// cannot open, in the order they were met. The run analyzes the tree without
	// them, so code there may use any export of any unit.
	Unread []string

	// LeftOut holds each entry at or below the root a walk met as [leftOut], whose
	// listing error the toolchain reports where its name is a Go file's and the
	// load clears.
	LeftOut []string

	// Walked reports units the search found below a root that holds no module
	// file, of which a unit that does not load is dropped.
	Walked bool
}

// EnvFile is one go env file a toolchain of the run would read, at the absolute
// path Path; Named reports that GOENV names it rather than the user's default.
type EnvFile struct {
	Path  string
	Named bool
}

// discoveredModule is one module a run loads, with what its load reads.
type discoveredModule struct {
	// FileErr says why File, the module file the toolchain run in Dir reads, could
	// not be read; File is nil then and where no module file governs Dir.
	FileErr error

	// Refused drops the module before any toolchain runs in Dir; WorkspaceErr, a searched
	// module's workspace file error, leaves what the module builds against unknown.
	Refused      error
	WorkspaceErr error

	File *ModuleFile

	// Dir is the directory the module's toolchain runs from, and ID the module
	// path a scope document declares for it, empty where none does.
	Dir string
	ID  string

	// Path is the module path the module file declares, empty where none governs
	// Dir.
	Path string

	// Workspace is the GOWORK setting the module loads under.
	Workspace string

	// Consumes is the module path of every unit a nested module or a reader builds
	// against, and Underscored the package pattern of every directory of a unit's
	// own module below an underscore directory.
	Consumes    []string
	Underscored []string

	// scriptsBlock reports that the toolchain run in Dir with no workspace, as a
	// dropped module's scripts resolve, may block on what it reads.
	scriptsBlock bool
}

// ModuleFile is one module file: where it is, its bytes and their parse by x/mod's
// modfile, the parser the toolchain vendors. A directive newer than the x/mod the
// analyzer is built with fails that parse even where the toolchain reads the file.
type ModuleFile struct {
	Syntax *modfile.File
	Path   string
	Text   []byte
}

// Discover reads the topology of doc's target once, before any configuration
// loads; cancelling ctx stops it.
func Discover(ctx context.Context, doc scope.Document) (*Topology, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	target := doc.Target.Path
	if target == "" {
		return nil, scope.ErrNoTarget
	}
	if _, err := os.Stat(target); err != nil {
		return nil, fmt.Errorf("load: target %s: %w", target, err)
	}
	top := &Topology{Root: target, root: scope.RootOf(target), ConsumerWorkspace: workspaceOff}
	dirs, err := top.unitDirs(ctx)
	if err != nil {
		return nil, fmt.Errorf("load: find the modules under the target: %w", err)
	}
	if empty := top.holdsProgram(ctx, dirs); empty != nil {
		return nil, empty
	}
	top.Walked = walkedUnits(target, dirs)
	if top.Units, err = top.discoverUnits(ctx, dirs); err != nil {
		return nil, err
	}
	if err = top.survey(ctx); err != nil {
		return nil, fmt.Errorf("load: find the directories under the target: %w", err)
	}
	if top.Nested, err = top.nestedModules(ctx, doc.Consumers); err != nil {
		return nil, err
	}
	if top.Readers, err = top.readers(ctx, doc.Consumers); err != nil {
		return nil, err
	}
	if doc.Workspace != "" {
		top.ConsumerWorkspace = doc.Workspace
	}
	if top.Consumers, err = declaredConsumers(ctx, top.root, doc.Consumers); err != nil {
		return nil, err
	}
	if err := top.meetBlocking(); err != nil {
		return nil, err
	}
	top.meetEnvFiles()
	if err := top.hide(ctx); err != nil {
		return nil, err
	}
	return top, nil
}

// meetEnvFiles records, for the directory of every load, the go env file a
// toolchain started there would read where it is a pipe, a device or a socket,
// which the toolchain reads outside its overlay.
func (top *Topology) meetEnvFiles() {
	top.envOff = make(map[string]bool)
	loads := top.loads()
	for i := range loads {
		file, ok := irregularEnvFile(loads[i].Dir)
		if !ok {
			continue
		}
		top.envOff[loads[i].Dir] = true
		if !slices.Contains(top.EnvFiles, file) {
			top.EnvFiles = append(top.EnvFiles, file)
		}
	}
	slices.SortFunc(top.EnvFiles, func(a, b EnvFile) int { return strings.Compare(a.Path, b.Path) })
}

// noEnvFile reports whether a toolchain started in dir runs with GOENV=off.
func (top *Topology) noEnvFile(dir string) bool { return top.envOff[dir] }

// irregularEnvFile is the go env file a toolchain started in dir would read, and
// whether one is, where it is no regular file (cmd/go internal/cfg EnvFile and
// readEnvFile): the file GOENV names, a relative one in dir, else the one in the
// user's configuration directory. The null device reads as empty, the common way
// to name no file.
func irregularEnvFile(dir string) (EnvFile, bool) {
	file := EnvFile{Path: os.Getenv("GOENV"), Named: true}
	switch file.Path {
	case "off", os.DevNull:
		return EnvFile{}, false
	case "":
		config, err := os.UserConfigDir()
		if err != nil {
			return EnvFile{}, false
		}
		file = EnvFile{Path: filepath.Join(config, "go", "env")}
	}
	if !filepath.IsAbs(file.Path) {
		file.Path = filepath.Join(dir, file.Path)
	}
	return file, hides(file.Path)
}

func declaredConsumers(ctx context.Context, root scope.Root, declared []scope.Module) ([]discoveredModule, error) {
	var consumers []discoveredModule
	for _, d := range declared {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		consumer := discoveredModule{Dir: d.Path, ID: d.ID}
		consumer.File, consumer.FileErr = readModuleFile(root, d.Path)
		if irregular(consumer.FileErr) {
			return nil, consumer.FileErr
		}
		consumers = append(consumers, consumer)
	}
	return consumers, nil
}

// discoverUnits is the unit at each of dirs, with the underscored packages of its
// module. A unit the search found whose module or workspace file the toolchain
// cannot read is kept with that failure as Refused, so it is dropped ([program.6]);
// any other unit's ends the run.
func (top *Topology) discoverUnits(ctx context.Context, dirs []string) ([]discoveredModule, error) {
	units := make([]discoveredModule, 0, len(dirs))
	for _, dir := range dirs {
		unit, err := discoverModule(ctx, top.root, dir)
		switch {
		case err == nil:
		case top.Walked && failure.Setup(err):
			unit.Refused = err
		default:
			return nil, err
		}
		if unit.Path != "" && unit.Refused == nil {
			if unit.Underscored, err = top.underscoredPackages(ctx, dir, TreeOf(unit.File)); err != nil {
				return nil, fmt.Errorf("load: find the packages under %s: %w", top.Rel(dir), err)
			}
		}
		units = append(units, unit)
	}
	return units, nil
}

// unitDirs is the directory of every unit of target, each the directory its
// toolchain runs from. A module root is that module and every module below it the
// workspace listing it lists; another module nested below it is a dependency or
// nothing. A directory a module file above it governs is its own one unit. Any
// other target is every module rooted below it, in directory order, searching no
// directory [leavesOut] names, and holds no unit where it holds none.
func (top *Topology) unitDirs(ctx context.Context) ([]string, error) {
	target := top.Root
	if IsModuleRoot(target) {
		return withMembers(ctx, target)
	}
	if governedFromAbove(target) {
		return []string{target}, nil
	}
	var dirs []string
	enters := func(dir string) bool { return !leavesOut(filepath.Base(dir)) }
	err := top.Walk(target, enters, func(path string, kind Kind) error {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case kind == directory && IsModuleRoot(path):
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

// holdsProgram is the setup failure no-program where the target holds no unit, or
// no unit holds a Go file [Topology.holdsGoFile] finds, and nil otherwise. A tree
// holding an entry the run cannot read may hold such a file, so where a unit holds
// none it is no failure, and where none is found the failure names the entry.
func (top *Topology) holdsProgram(ctx context.Context, dirs []string) error {
	for _, dir := range dirs {
		found, err := top.holdsGoFile(ctx, dir, treeAt(dir))
		if err != nil {
			return fmt.Errorf("load: find the Go files under %s: %w", top.Rel(dir), err)
		}
		if found {
			return nil
		}
	}
	switch {
	case len(dirs) == 0 && len(top.Unread) > 0:
		unread := make([]string, len(top.Unread))
		for i, path := range top.Unread {
			unread[i] = top.Rel(path)
		}
		return failure.New(vocab.SetupNoProgram, fmt.Sprintf(
			"the target root holds no go.mod file the run can read and none above it governs it, and the run cannot read %s; give the run read access to it, run from the directory that holds a module's go.mod file, or name the languages to analyze",
			strings.Join(unread, ", "),
		))
	case len(dirs) == 0:
		return failure.New(vocab.SetupNoProgram,
			"the target root holds no go.mod file and none above it governs it; run from the directory that holds a module's go.mod file, or name the languages to analyze",
		)
	case len(top.Unread) > 0:
		return nil
	default:
		return failure.New(vocab.SetupNoProgram,
			"the modules of the target hold no .go file; run from the directory that holds the module's Go source files, or name the languages to analyze",
		)
	}
}

// holdsGoFile reports whether the module tree at dir holds a Go source file of the
// Go row, or an entry it cannot read, which may be or hold one, in a directory the
// underscored-package search enters, or a Go-named one in a package directory that
// every toolchain reads as absent ([Topology.hide]). A directory its module ignores
// holds none ([program.4]).
func (top *Topology) holdsGoFile(ctx context.Context, dir string, tree ModuleTree) (bool, error) {
	if tree.ignores(dir) {
		return false, nil
	}
	found := false
	enters := func(dir string) bool { return !underscoredSkips(tree, dir) }
	err := top.Walk(dir, enters, func(path string, kind Kind) error {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case kind == Unread, kind == File && goRow.Sources(filepath.Base(path)),
			kind == leftOut && goRow.Sources(filepath.Base(path)) && tree.within(dir, filepath.Dir(path)) && hides(path):
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

// withMembers is the module rooted at target, then every other module below it the
// workspace listing target lists, in directory order.
func withMembers(ctx context.Context, target string) ([]string, error) {
	workspace, err := scope.WorkspaceOf(ctx, scope.RootOf(target), target)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dirs := []string{target}
	if !workspace.Lists(target) {
		return dirs, nil
	}
	for _, member := range slices.Sorted(slices.Values(workspace.Modules)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if scope.Below(target, member) {
			dirs = append(dirs, member)
		}
	}
	return dirs, nil
}

// walkedUnits reports whether the units of target are modules the walk found below
// it, rather than its own module and the members its workspace lists, which the
// target names.
func walkedUnits(target string, dirs []string) bool {
	return !IsModuleRoot(target) && !governedFromAbove(target) && !slices.ContainsFunc(dirs, func(dir string) bool {
		return sameDir(dir, target)
	})
}

// discoverModule is the module whose toolchain runs from dir, loading under the
// workspace file that governs dir where it lists dir, which is the file `go build`
// in dir reads, and under no workspace otherwise. Where the toolchain cannot read
// its module file or that workspace file, the module comes with the error, which
// names the file relative to root.
func discoverModule(ctx context.Context, root scope.Root, dir string) (discoveredModule, error) {
	m := discoveredModule{Dir: dir, Workspace: workspaceOff}
	m.File, m.FileErr = readModuleFile(root, dir)
	if m.File != nil && m.File.Syntax.Module != nil {
		m.Path = m.File.Syntax.Module.Mod.Path
	}
	if irregular(m.FileErr) {
		return m, m.FileErr
	}
	workspace, err := scope.WorkspaceOf(ctx, root, dir)
	if err != nil {
		m.WorkspaceErr = err
		return m, err
	}
	if workspace.Lists(dir) {
		m.Workspace = workspace.File
	}
	return m, nil
}

// nestedModules is every module rooted below the target, under the directories
// discovery searches and those an underscore starts, that is no unit and no
// declared consumer and that builds against a unit's directory, in directory order.
// One whose module file is no regular file is passed over, as one that does not
// parse is: no load of the target reads it.
func (top *Topology) nestedModules(ctx context.Context, declared []scope.Module) ([]discoveredModule, error) {
	var nested []discoveredModule
	err := top.Walk(top.Root, searchedDir, func(path string, kind Kind) error {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case kind != directory || path == top.Root || !IsModuleRoot(path) || top.isUnit(path) || declares(declared, path):
			return nil
		}
		m := discoveredModule{Dir: path, Workspace: workspaceOff}
		m.File, m.FileErr = readModuleFile(top.root, path)
		if m.Consumes = top.replacedUnits(&m); len(m.Consumes) > 0 {
			nested = append(nested, m)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load: find the modules under the target: %w", err)
	}
	return nested, nil
}

// readers is every module the workspace listing the target lists outside it that
// no scope declares and whose module file requires a unit, in the workspace's
// order, loaded through that workspace ([program.8]). A reader whose module file
// cannot be read is kept with its error: it may require any unit. The toolchain
// refuses every load through a workspace one of whose members' module files it
// cannot read, so such a reader reaches a load only where the toolchain reads a
// file this parser refuses.
func (top *Topology) readers(ctx context.Context, declared []scope.Module) ([]discoveredModule, error) {
	workspace, err := scope.WorkspaceOf(ctx, top.root, top.Root)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	if !workspace.Lists(top.Root) {
		return nil, nil
	}
	var readers []discoveredModule
	for _, member := range workspace.Modules {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		if sameDir(member, top.Root) || scope.Below(top.Root, member) || declares(declared, member) {
			continue
		}
		m := discoveredModule{Dir: member, Workspace: workspace.File}
		// A workspace member is a module root of its own: the toolchain reads the module
		// file in its directory and none above it.
		m.File, m.FileErr = parseModuleFile(top.root, filepath.Join(member, moduleFileName))
		if irregular(m.FileErr) {
			return nil, m.FileErr
		}
		if m.File != nil {
			required := m.File.required()
			m.Consumes = slices.DeleteFunc(top.unitPaths(), func(path string) bool { return !slices.Contains(required, path) })
		}
		if m.FileErr != nil || len(m.Consumes) > 0 {
			readers = append(readers, m)
		}
	}
	return readers, nil
}

// replacedUnits is the module path of every unit m's module file requires and
// replaces with that unit's own directory, which is how a module below the target
// builds against the target's tree rather than a published version of it.
func (top *Topology) replacedUnits(m *discoveredModule) []string {
	if m.File == nil {
		return nil
	}
	var held []string
	for i := range top.Units {
		unit := &top.Units[i]
		required := slices.ContainsFunc(m.File.Syntax.Require, func(r *modfile.Require) bool { return r.Mod.Path == unit.Path })
		replaced := slices.ContainsFunc(m.File.Syntax.Replace, func(r *modfile.Replace) bool {
			replacement := r.New.Path
			if !filepath.IsAbs(replacement) {
				replacement = filepath.Join(m.Dir, replacement)
			}
			return r.Old.Path == unit.Path && r.New.Version == "" && sameDir(replacement, unit.Dir)
		})
		if unit.Path != "" && required && replaced {
			held = append(held, unit.Path)
		}
	}
	return held
}

// unitPaths is the module path of every unit that has one, in unit order.
func (top *Topology) unitPaths() []string {
	paths := make([]string, 0, len(top.Units))
	for i := range top.Units {
		if top.Units[i].Path != "" {
			paths = append(paths, top.Units[i].Path)
		}
	}
	return paths
}

// isUnit reports whether dir is the directory of a unit.
func (top *Topology) isUnit(dir string) bool {
	return slices.ContainsFunc(top.Units, func(u discoveredModule) bool { return sameDir(u.Dir, dir) })
}

// declares reports whether a scope declares the module at dir as a consumer.
func declares(declared []scope.Module, dir string) bool {
	return slices.ContainsFunc(declared, func(m scope.Module) bool { return sameDir(m.Path, dir) })
}

// readModuleFile reads the module file the toolchain run in dir reads: the nearest
// one at or above dir, its error naming it relative to root. It returns nil and no
// error where none governs dir.
func readModuleFile(root scope.Root, dir string) (*ModuleFile, error) {
	at := dir
	for !IsModuleRoot(at) {
		if filepath.Dir(at) == at {
			return nil, nil
		}
		at = filepath.Dir(at)
	}
	return parseModuleFile(root, filepath.Join(at, moduleFileName))
}

// parseModuleFile reads and parses the module file at path, its error naming it
// relative to root ([program.10]). One that is no regular file is never opened, and
// its error is the setup failure unreadable-configuration: the toolchain run where
// it governs may block on it.
func parseModuleFile(root scope.Root, path string) (*ModuleFile, error) {
	text, err := scope.ReadRegularFile(root, path)
	if errors.Is(err, scope.ErrIrregular) {
		return nil, failure.WrapFix(vocab.SetupUnreadableConfiguration, fmt.Errorf("%w: %w", ErrModuleFile, err), scope.IrregularFix)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModuleFile, err)
	}
	syntax, err := modfile.Parse(root.Rel(path), text, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModuleFile, err)
	}
	return &ModuleFile{Path: path, Text: text, Syntax: syntax}, nil
}

// irregular reports whether err is a module file, a workspace file or a vendor list
// that is no regular file, which no toolchain may run under.
func irregular(err error) bool { return errors.Is(err, scope.ErrIrregular) }

// required is the path of every module the file requires.
func (f *ModuleFile) required() []string {
	paths := make([]string, 0, len(f.Syntax.Require))
	for _, r := range f.Syntax.Require {
		paths = append(paths, r.Mod.Path)
	}
	return paths
}

// governedFromAbove reports whether a directory above dir holds a module file,
// which makes dir a directory of that module.
func governedFromAbove(dir string) bool {
	for at := filepath.Dir(dir); ; at = filepath.Dir(at) {
		if IsModuleRoot(at) {
			return true
		}
		if filepath.Dir(at) == at {
			return false
		}
	}
}

// survey walks the directories the module search enters, recording each entry
// there the run cannot read and each regular Go file it cannot open.
func (top *Topology) survey(ctx context.Context) error {
	return top.Walk(top.Root, searchedDir, func(path string, kind Kind) error {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case kind == File && buildconstraint.IsGoFile(filepath.Base(path)) && !openable(path):
			top.AddUnread(path)
		}
		return nil
	})
}

// meetBlocking reads, before any toolchain runs, the kind of every file a load
// reads that the overlay cannot hide ([Topology.irregularRead]): one that is no
// regular file in the read of a module the target names or of a declared consumer
// ends the run, and one in the read of a unit the search found, a nested module or
// a workspace reader drops that module ([program.6]). It also notes, for each
// module the run may drop, whether its scripts' toolchain would read one.
func (top *Topology) meetBlocking() error {
	for i := range top.Units {
		if err := top.meetBlockingInUnit(&top.Units[i]); err != nil {
			return err
		}
	}
	for _, droppable := range [][]discoveredModule{top.Nested, top.Readers} {
		for i := range droppable {
			m := &droppable[i]
			if path := top.irregularRead(m.File, m.Dir, m.Workspace); path != "" && m.Refused == nil {
				m.Refused = top.blockingFailure(path)
			}
			m.scriptsBlock = top.scriptsBlock(m)
		}
	}
	for i := range top.Consumers {
		if path := top.irregularRead(top.Consumers[i].File, top.Consumers[i].Dir, top.ConsumerWorkspace); path != "" {
			return top.blockingFailure(path)
		}
	}
	return nil
}

// meetBlockingInUnit is [Topology.meetBlocking] for one unit, which is dropped
// where the search found it and ends the run otherwise.
func (top *Topology) meetBlockingInUnit(unit *discoveredModule) error {
	if unit.Refused == nil {
		if path := top.irregularRead(unit.File, unit.Dir, unit.Workspace); path != "" {
			if !top.Walked {
				return top.blockingFailure(path)
			}
			unit.Refused = top.blockingFailure(path)
		}
	}
	unit.scriptsBlock = top.Walked && top.scriptsBlock(unit)
	return nil
}

// scriptsBlock reports whether the toolchain run in m's directory with no
// workspace, as a dropped module's scripts resolve, reads a file that is no
// regular file outside the overlay.
func (top *Topology) scriptsBlock(m *discoveredModule) bool {
	return irregular(m.FileErr) || top.irregularRead(m.File, m.Dir, workspaceOff) != ""
}

// irregularRead is the first file the toolchain run in dir under workspace reads,
// with file the module file governing dir, that is no regular file and that the
// run cannot hide from it, and empty where there is none: the workspace file, the
// module files of its members, and the vendor list ([vendorList]). Every
// other module file a load may read, a replaced module's among them, is hidden
// ([Topology.hide]), so the load fails naming it only where it reads it.
func (top *Topology) irregularRead(file *ModuleFile, dir, workspace string) string {
	if workspace != workspaceOff {
		if found := top.workspaceRead(workspace); found != "" {
			return found
		}
	}
	if list := vendorList(file, dir, workspace); hides(list) {
		return list
	}
	return ""
}

// workspaceRead is the workspace file at workspace, or the module file of one of
// its members, where it is no regular file, and empty otherwise. A workspace file
// that does not parse reads as none: the load reports it.
func (top *Topology) workspaceRead(workspace string) string {
	work, err := scope.ReadWorkspace(top.root, workspace)
	switch {
	case irregular(err):
		return workspace
	case err != nil:
		return ""
	}
	for _, member := range work.Modules {
		if path := filepath.Join(member, moduleFileName); hides(path) {
			return path
		}
	}
	return ""
}

// blockingFailure is the setup failure naming, relative to the root ([program.10]),
// the module file, workspace file or vendor list at path that is no regular file,
// which [irregular] reads as a file no toolchain may run under.
func (top *Topology) blockingFailure(path string) error {
	return failure.WrapFix(vocab.SetupUnreadableConfiguration, fmt.Errorf(
		"load: %s: %w, but %s; the go command reads it outside its overlay, as a module file, a workspace file or a vendor list",
		top.Rel(path), scope.ErrIrregular, irregularKind(path),
	), scope.IrregularFix)
}

// irregularKind names what the entry at path, links followed, is where it is no
// regular file, with what the go command's read of it does.
func irregularKind(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "an entry the run cannot read the kind of"
	}
	switch mode := info.Mode(); {
	case mode&fs.ModeNamedPipe != 0:
		return "a pipe, which the go command may block on"
	case mode&fs.ModeDevice != 0:
		return "a device, which the go command may block on or read without end"
	case mode&fs.ModeSocket != 0:
		return "a socket, which the go command cannot open"
	}
	return "an entry that is no regular file"
}

// Rel is path relative to the root, as every path a setup failure line or a note
// names is ([program.10]), placed where both resolve ([scope.Root.Rel]): with `..`
// segments where it is outside the root.
func (top *Topology) Rel(path string) string { return top.root.Rel(path) }

// openable reports whether the run can open the regular file at path.
func openable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// IsModuleRoot reports whether dir holds a module file, which makes it the root of
// a module of its own.
func IsModuleRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, moduleFileName))
	return err == nil && !info.IsDir()
}

// unitPrefix is dir relative to target with forward slashes, empty for the target
// itself.
func unitPrefix(target, dir string) string {
	if !scope.Below(target, dir) {
		return ""
	}
	rel, _ := filepath.Rel(target, dir)
	return filepath.ToSlash(rel)
}

// underscoredPackages is the pattern of every directory of the module rooted at dir
// that holds a non-test Go file below a directory an underscore starts, outside
// every module below dir and every directory the module tree ignores, in directory
// order. The go tool's wildcard matches none of them, even with the underscore
// directory as its root, so each is named by its own path.
func (top *Topology) underscoredPackages(ctx context.Context, dir string, tree ModuleTree) ([]string, error) {
	var patterns []string
	enters := func(dir string) bool { return !underscoredSkips(tree, dir) }
	err := top.Walk(dir, enters, func(path string, kind Kind) error {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case kind != File:
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || !underscoredSource(rel) {
			return err
		}
		if pattern := "./" + filepath.ToSlash(filepath.Dir(rel)); !slices.Contains(patterns, pattern) {
			patterns = append(patterns, pattern)
		}
		return nil
	})
	return patterns, err
}

// searchedBelowUnderscore reports whether the nested-module and underscored-package
// searches enter a directory of that name: one [leavesOut] does not name, or one
// an underscore starts. The go tool's wildcard skips an
// underscore directory, and a module of its own below one, an example module
// among them, still builds against the target.
func searchedBelowUnderscore(name string) bool {
	return strings.HasPrefix(name, "_") || !leavesOut(name)
}

// searchedDir reports whether the nested-module search enters the directory at
// path ([searchedBelowUnderscore]).
func searchedDir(path string) bool { return searchedBelowUnderscore(filepath.Base(path)) }

// underscoredSkips reports whether the walk of one module's underscored packages
// skips the directory at path: one it does not search below an underscore, one the
// module ignores, or another module's root.
func underscoredSkips(tree ModuleTree, path string) bool {
	return !searchedBelowUnderscore(filepath.Base(path)) || tree.ignores(path) || IsModuleRoot(path)
}

// underscoredSource reports whether the relative path rel names a non-test Go file
// some directory of which an underscore starts.
func underscoredSource(rel string) bool {
	if filepath.Ext(rel) != goFileExt || strings.HasSuffix(rel, "_test.go") {
		return false
	}
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/"), func(element string) bool {
		return strings.HasPrefix(element, "_")
	})
}

// unitDir is where one module's toolchain runs: its directory and its GOWORK
// setting, whether no toolchain may run there, because the run refused a file it
// may block on, and how it runs.
type unitDir struct {
	dir       string
	workspace string
	toolchain toolchain
	offline   bool
}
