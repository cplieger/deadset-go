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

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/mod/modfile"
)

// moduleFileName is the file that makes a directory the root of a module.
const moduleFileName = "go.mod"

// ErrModuleFile reports a module file that cannot be read or parsed.
var ErrModuleFile = errors.New("load: unreadable module file")

// goRow is the Go row of the Contract's language table, whose skipped
// directories are the ones discovery never searches.
var goRow, _ = vocab.LanguageRowOf(vocab.Go)

// Topology is what every configuration of one run loads, decided once from the
// scope and the file system: the units of the target, the modules below it that
// build against a unit's directory, the workspace readers, and the declared
// consumers, each with the module file its toolchain reads.
type Topology struct {
	// Root is the target root every position of the run is relative to, and
	// ConsumerWorkspace the GOWORK setting every declared consumer loads under.
	Root              string
	ConsumerWorkspace string

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

	// Walked reports units the search found below a root that holds no module
	// file, of which a unit that does not load is dropped.
	Walked bool
}

// discoveredModule is one module a run loads, with what its load reads.
type discoveredModule struct {
	// Dir is the directory the module's toolchain runs from, and ID the module
	// path a scope document declares for it, empty where none does.
	Dir string
	ID  string

	// Path is the module path the module file declares, empty where none governs
	// Dir.
	Path string

	// Workspace is the GOWORK setting the module loads under.
	Workspace string

	// File is the module file the toolchain run in Dir reads, nil where none governs
	// it or where it cannot be read, which FileErr then says.
	File    *ModuleFile
	FileErr error

	// Consumes is the module path of every unit a nested module or a reader builds
	// against, and Underscored the package pattern of every directory of a unit's
	// own module below an underscore directory.
	Consumes    []string
	Underscored []string
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
	dirs, err := unitDirs(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("load: find the modules under %s: %w", target, err)
	}
	top := &Topology{Root: target, Walked: walkedUnits(target, dirs), ConsumerWorkspace: workspaceOff}
	if top.Units, err = discoverUnits(ctx, dirs); err != nil {
		return nil, err
	}
	if !top.Walked {
		if top.Nested, err = top.nestedModules(ctx, doc.Consumers); err != nil {
			return nil, err
		}
	}
	if top.Readers, err = top.readers(ctx, doc.Consumers); err != nil {
		return nil, err
	}
	if doc.Workspace != "" {
		top.ConsumerWorkspace = doc.Workspace
	}
	if top.Consumers, err = declaredConsumers(ctx, doc.Consumers); err != nil {
		return nil, err
	}
	return top, nil
}

func declaredConsumers(ctx context.Context, declared []scope.Module) ([]discoveredModule, error) {
	var consumers []discoveredModule
	for _, d := range declared {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		consumer := discoveredModule{Dir: d.Path, ID: d.ID}
		consumer.File, consumer.FileErr = readModuleFile(d.Path)
		consumers = append(consumers, consumer)
	}
	return consumers, nil
}

// discoverUnits is the unit at each of dirs, with the underscored packages of its
// module.
func discoverUnits(ctx context.Context, dirs []string) ([]discoveredModule, error) {
	units := make([]discoveredModule, 0, len(dirs))
	for _, dir := range dirs {
		unit, err := discoverModule(ctx, dir)
		if err != nil {
			return nil, err
		}
		if unit.Path != "" {
			if unit.Underscored, err = underscoredPackages(ctx, dir); err != nil {
				return nil, fmt.Errorf("load: find the packages under %s: %w", dir, err)
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
// directory the Go row of the language table skips, and is its own one unit where
// it holds none.
func unitDirs(ctx context.Context, target string) ([]string, error) {
	if IsModuleRoot(target) {
		return withMembers(ctx, target)
	}
	if governedFromAbove(target) {
		return []string{target}, nil
	}
	var dirs []string
	err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		case !entry.IsDir():
			return nil
		case path != target && goRow.Skips(entry.Name()):
			return fs.SkipDir
		case IsModuleRoot(path):
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return []string{target}, nil
	}
	return dirs, nil
}

// withMembers is the module rooted at target, then every other module below it the
// workspace listing target lists, in directory order.
func withMembers(ctx context.Context, target string) ([]string, error) {
	workspace, err := scope.WorkspaceOf(ctx, target)
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
		if below(target, member) {
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
// in dir reads, and under no workspace otherwise.
func discoverModule(ctx context.Context, dir string) (discoveredModule, error) {
	m := discoveredModule{Dir: dir, Workspace: workspaceOff}
	workspace, err := scope.WorkspaceOf(ctx, dir)
	if err != nil {
		return discoveredModule{}, fmt.Errorf("load: %s: %w", dir, err)
	}
	if workspace.Lists(dir) {
		m.Workspace = workspace.File
	}
	m.File, m.FileErr = readModuleFile(dir)
	if m.File != nil && m.File.Syntax.Module != nil {
		m.Path = m.File.Syntax.Module.Mod.Path
	}
	return m, nil
}

// nestedModules is every module rooted below the target, under the directories
// discovery searches and those an underscore starts, that is no unit and no
// declared consumer and that builds against a unit's directory, in directory order.
func (top *Topology) nestedModules(ctx context.Context, declared []scope.Module) ([]discoveredModule, error) {
	var nested []discoveredModule
	err := filepath.WalkDir(top.Root, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		case !entry.IsDir():
			return nil
		case path != top.Root && !searchedBelowUnderscore(entry.Name()):
			return fs.SkipDir
		case path == top.Root || !IsModuleRoot(path) || top.isUnit(path) || declares(declared, path):
			return nil
		}
		m := discoveredModule{Dir: path, Workspace: workspaceOff}
		m.File, m.FileErr = readModuleFile(path)
		if m.Consumes = top.replacedUnits(&m); len(m.Consumes) > 0 {
			nested = append(nested, m)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load: find the modules under %s: %w", top.Root, err)
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
	workspace, err := scope.WorkspaceOf(ctx, top.Root)
	if err != nil {
		return nil, fmt.Errorf("load: %s: %w", top.Root, err)
	}
	if !workspace.Lists(top.Root) {
		return nil, nil
	}
	var readers []discoveredModule
	for _, member := range workspace.Modules {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		if sameDir(member, top.Root) || below(top.Root, member) || declares(declared, member) {
			continue
		}
		m := discoveredModule{Dir: member, Workspace: workspace.File}
		// A workspace member is a module root of its own: the toolchain reads the module
		// file in its directory and none above it.
		m.File, m.FileErr = parseModuleFile(filepath.Join(member, moduleFileName))
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
// one at or above dir. It returns nil and no error where none governs dir.
func readModuleFile(dir string) (*ModuleFile, error) {
	at := dir
	for !IsModuleRoot(at) {
		if filepath.Dir(at) == at {
			return nil, nil
		}
		at = filepath.Dir(at)
	}
	return parseModuleFile(filepath.Join(at, moduleFileName))
}

// parseModuleFile reads and parses the module file at path.
func parseModuleFile(path string) (*ModuleFile, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModuleFile, err)
	}
	syntax, err := modfile.Parse(path, text, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModuleFile, err)
	}
	return &ModuleFile{Path: path, Text: text, Syntax: syntax}, nil
}

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

// IsModuleRoot reports whether dir holds a module file, which makes it the root of
// a module of its own.
func IsModuleRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, moduleFileName))
	return err == nil && !info.IsDir()
}

// below reports whether path is a directory strictly under dir.
func below(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// unitPrefix is dir relative to target with forward slashes, empty for the target
// itself.
func unitPrefix(target, dir string) string {
	if !below(target, dir) {
		return ""
	}
	rel, _ := filepath.Rel(target, dir)
	return filepath.ToSlash(rel)
}

// underscoredPackages is the pattern of every directory of the module rooted at dir
// that holds a non-test Go file below a directory an underscore starts, outside every
// module below dir, in directory order. The go tool's wildcard matches none of them, even
// with the underscore directory as its root, so each is named by its own path.
func underscoredPackages(ctx context.Context, dir string) ([]string, error) {
	var patterns []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil || path == dir:
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		}
		if entry.IsDir() {
			if !searchedBelowUnderscore(entry.Name()) || IsModuleRoot(path) {
				return fs.SkipDir
			}
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
// searches enter a directory of that name: one the Go row of the language table
// does not skip, or one an underscore starts. The go tool's wildcard skips an
// underscore directory, and a module of its own below one, an example module
// among them, still builds against the target.
func searchedBelowUnderscore(name string) bool {
	return strings.HasPrefix(name, "_") || !goRow.Skips(name)
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
// setting.
type unitDir struct {
	dir       string
	workspace string
}
