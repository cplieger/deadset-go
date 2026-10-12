package load

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/internal/scope"
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
		if sameDir(r.Units[i].Dir, dir) {
			narrowed := *r
			narrowed.Packages = r.Units[i].Packages
			narrowed.Units = r.Units[i : i+1]
			return &narrowed
		}
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
