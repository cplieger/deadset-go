package load

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// moduleFileName is the file that makes a directory the root of a module.
const moduleFileName = "go.mod"

// The directories no module of a target is discovered under: the toolchain's test
// inputs and vendored sources, and an installed JavaScript dependency tree.
var unsearchedDirs = []string{"testdata", "vendor", "node_modules"}

// Unit is one unit of the target a load reads: a module, from its own directory
// with its own module file and build list, or the target itself where [Units]
// finds no module to read.
type Unit struct {
	Dir  string // the directory the unit's toolchain runs from, at or below the target root
	Path string // the module path the load reports for it, empty before the load

	// Packages holds the unit's own packages and test variants, a subset of
	// [Result.Packages] in the same order.
	Packages []*packages.Package

	// UnanalysedConsumer reports a unit whose module a [droppedModule]'s module
	// file requires, so code the run did not analyze may import its exports.
	UnanalysedConsumer bool
}

// droppedModule is one module found below the target that did not load, a module the
// walk found or a [Result.Nested] consumer: its directory and the error its load
// returned.
type droppedModule struct {
	Err error
	Dir string
}

// Units returns the directory of every module a load of target reads, each the
// directory its toolchain runs from. A module root is that module and every module
// below it the workspace listing it lists; another module nested below it is a
// dependency or nothing. A directory a module file above it governs is its own one
// unit. Any other target is every module rooted below it, in directory order,
// searching no directory [unsearchedDirs] names or whose name opens with "." or
// "_", and is its own one unit where it holds none.
func Units(ctx context.Context, target string) ([]string, error) {
	if isModuleRoot(target) {
		return withMembers(ctx, target)
	}
	if governedFromAbove(target) {
		return []string{target}, nil
	}
	var dirs []string
	err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != target && unsearched(entry.Name()) {
			return fs.SkipDir
		}
		if isModuleRoot(path) {
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
	members, err := scope.Members(ctx, target)
	if err != nil {
		return nil, err
	}
	dirs := []string{target}
	for _, member := range slices.Sorted(slices.Values(members)) {
		if rel, err := filepath.Rel(target, member); err == nil && rel != "." && filepath.IsLocal(rel) {
			dirs = append(dirs, member)
		}
	}
	return dirs, nil
}

// Unit returns r narrowed to the unit rooted at dir: its packages alone, so an
// answer about one module's build list or imports reads that module's load. It
// returns nil where r holds no such unit.
func (r *Result) Unit(dir string) *Result {
	for i := range r.Units {
		if !sameDir(r.Units[i].Dir, dir) {
			continue
		}
		narrowed := *r
		narrowed.Packages = r.Units[i].Packages
		narrowed.Units = r.Units[i : i+1]
		narrowed.Generated = generatedFiles(narrowed.Packages)
		return &narrowed
	}
	return nil
}

// PackageKey names the package of that import path whose files are in dir: the
// import path alone, unless target packages of two directories share it, as two
// modules of the target declaring one module path do, where dir joins it.
func (r *Result) PackageKey(pkgPath, dir string) string {
	if !r.shared[pkgPath] {
		return pkgPath
	}
	return pkgPath + "\x00" + filepath.Clean(dir)
}

// KeyOf is [Result.PackageKey] of p, by the directory of its first file.
func (r *Result) KeyOf(p *packages.Package) string {
	return r.PackageKey(p.PkgPath, packageDir(p))
}

// packageDir is the directory of p's first file, and the empty string for a
// package with none.
func packageDir(p *packages.Package) string {
	if len(p.GoFiles) == 0 {
		return ""
	}
	return filepath.Dir(p.GoFiles[0])
}

// markShared records every import path that target packages of two directories
// declare.
func (r *Result) markShared() {
	dirs := make(map[string]string)
	for _, p := range r.Packages {
		dir := packageDir(p)
		if dir == "" {
			continue
		}
		if held, seen := dirs[p.PkgPath]; !seen {
			dirs[p.PkgPath] = dir
		} else if !sameDir(held, dir) {
			if r.shared == nil {
				r.shared = make(map[string]bool)
			}
			r.shared[p.PkgPath] = true
		}
	}
}

// governedFromAbove reports whether a directory above dir holds a module file,
// which makes dir a directory of that module.
func governedFromAbove(dir string) bool {
	for at := filepath.Dir(dir); ; at = filepath.Dir(at) {
		if isModuleRoot(at) {
			return true
		}
		if filepath.Dir(at) == at {
			return false
		}
	}
}

// isModuleRoot reports whether dir holds a module file.
func isModuleRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, moduleFileName))
	return err == nil && !info.IsDir()
}

// unsearched reports whether a directory of that name holds no module of a target.
func unsearched(name string) bool {
	return name == "" || name[0] == '.' || name[0] == '_' || slices.Contains(unsearchedDirs, name)
}

// unitDir is where one unit's toolchain runs: its directory and its GOWORK
// setting.
type unitDir struct {
	dir       string
	workspace string
}

// unitWorkspace is the GOWORK setting one unit loads under: the workspace file
// that governs dir where it lists dir as a module, which is the file `go build`
// in dir reads, and [workspaceOff] otherwise, the ambient setting included.
func unitWorkspace(ctx context.Context, dir string) (string, error) {
	file, member, err := scope.Governing(ctx, dir)
	if err != nil || !member {
		return workspaceOff, err
	}
	return file, nil
}

// demoteOtherMembers leaves the module rooted at dir the one main module of a
// workspace load. Every other member is a dependency of that unit, as a module a
// replace directive names is, so no stage reads it as the unit's own code.
func demoteOtherMembers(pkgs []*packages.Package, dir string) {
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Module == nil || !p.Module.Main || sameDir(p.Module.Dir, dir) {
			return
		}
		dependency := *p.Module
		dependency.Main = false
		p.Module = &dependency
	})
}

// walkedUnits reports whether the units of target are modules the walk found below
// it, rather than its own module and the members its workspace lists, which the
// target names.
func walkedUnits(target string, dirs []string) bool {
	return !isModuleRoot(target) && !governedFromAbove(target) && !slices.ContainsFunc(dirs, func(dir string) bool {
		return sameDir(dir, target)
	})
}

// droppable reports whether one unit's load error is about the project, a setup
// failure, the diagnostics of a load or the go command refusing to list it, rather
// than about the run.
func droppable(err error) bool {
	_, setup := errors.AsType[*SetupError](err)
	_, diagnosed := errors.AsType[*Error](err)
	_, listed := errors.AsType[*listError](err)
	return setup || diagnosed || listed
}

// unitPrefix is dir relative to target with forward slashes, empty for the target
// itself.
func unitPrefix(target, dir string) string {
	rel, err := filepath.Rel(target, dir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// markUnanalysedConsumers flags every unit whose module the module file of a dropped
// module requires, and every unit where that file cannot be read, because then it
// may require any of them.
func (r *Result) markUnanalysedConsumers(ctx context.Context) {
	for _, dropped := range r.Dropped {
		required, readable := requiredModules(ctx, dropped.Dir)
		for i := range r.Units {
			if !readable || slices.Contains(required, r.Units[i].Path) {
				r.Units[i].UnanalysedConsumer = true
			}
		}
	}
}

// requiredModules is the path of every module the module file in dir requires, and
// false where the file cannot be read.
func requiredModules(ctx context.Context, dir string) ([]string, bool) {
	file, readable := printedModuleFile(ctx, dir, moduleFileEnv())
	required := make([]string, 0, len(file.Require))
	for _, one := range file.Require {
		required = append(required, one.Path)
	}
	return required, readable
}

// moduleFileEnv is the environment a module file outside any configuration's load is
// printed under, pinned as [loadEnv] pins it.
func moduleFileEnv() []string {
	return append(os.Environ(), "GOWORK="+workspaceOff, "GOFLAGS=")
}

// nestedModules is the directory of every module rooted below target, under the
// directories [Units] searches and those an underscore starts, that is not a unit of
// the load, in directory order.
func nestedModules(target string, units []Unit) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		// The go tool's wildcard skips an underscore directory, and a module of its own
		// below one, an example module among them, still builds against the target.
		if name := entry.Name(); path != target && !strings.HasPrefix(name, "_") && unsearched(name) {
			return fs.SkipDir
		}
		if path != target && isModuleRoot(path) && !slices.ContainsFunc(units, func(u Unit) bool { return sameDir(u.Dir, path) }) {
			dirs = append(dirs, path)
		}
		return nil
	})
	return dirs, err
}

// underscoredPackages is the pattern of every directory of the module rooted at dir
// that holds a non-test Go file below a directory an underscore starts, outside every
// module below dir, in directory order. The go tool's wildcard matches none of them, even
// with the underscore directory as its root, so each is named by its own path.
func underscoredPackages(dir string) ([]string, error) {
	var patterns []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return skippedBelowUnderscore(entry.Name(), path)
		}
		if !underscoredSource(rel) {
			return nil
		}
		if pattern := "./" + filepath.ToSlash(filepath.Dir(rel)); !slices.Contains(patterns, pattern) {
			patterns = append(patterns, pattern)
		}
		return nil
	})
	return patterns, err
}

// skippedBelowUnderscore is [fs.SkipDir] for a directory [underscoredPackages] does not
// enter: one no module of a target lives in, unless an underscore starts its name,
// and the root of a module of its own.
func skippedBelowUnderscore(name, path string) error {
	if !strings.HasPrefix(name, "_") && unsearched(name) || isModuleRoot(path) {
		return fs.SkipDir
	}
	return nil
}

// underscoredSource reports whether the relative path rel names a non-test Go file
// some directory of which an underscore starts.
func underscoredSource(rel string) bool {
	if filepath.Ext(rel) != ".go" || strings.HasSuffix(rel, "_test.go") {
		return false
	}
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/"), func(element string) bool {
		return strings.HasPrefix(element, "_")
	})
}

// replacedUnits is every unit whose module the module file in dir requires and
// replaces with that unit's own directory, which is how a module below the target
// builds against the target's tree rather than a published version of it.
func replacedUnits(ctx context.Context, dir string, units []Unit) []Unit {
	file, readable := printedModuleFile(ctx, dir, moduleFileEnv())
	if !readable {
		return nil
	}
	var held []Unit
	for _, unit := range units {
		required := slices.ContainsFunc(file.Require, func(r moduleRequire) bool { return r.Path == unit.Path })
		replaced := slices.ContainsFunc(file.Replace, func(r moduleReplace) bool {
			replacement := r.New.Path
			if !filepath.IsAbs(replacement) {
				replacement = filepath.Join(dir, replacement)
			}
			return r.Old.Path == unit.Path && r.New.Version == "" && sameDir(replacement, unit.Dir)
		})
		if unit.Path != "" && required && replaced {
			held = append(held, unit)
		}
	}
	return held
}
