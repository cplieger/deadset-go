package load

import (
	"context"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"golang.org/x/tools/go/packages"
)

// mainFunction is the name of the function a program starts at.
const mainFunction = "main"

// programFile is one file of the target that its build constraint keeps out of the
// configuration unless the ignore tag is set, and that declares package main: a
// program of its own, run with go run, that no package of the target compiles.
//
// Info is what checking the file alone against the configuration's packages
// recorded, so every declaration of the target the file names resolves to the
// object the target's own packages declare. The check may have reported errors,
// and what it resolved is recorded all the same.
type programFile struct {
	Info *types.Info

	// Imports is the path of every package the file imports, a blank or dot import's
	// included, whose initialization runs whenever the program does.
	Imports []string
}

// checkPrograms finds and checks every program of one unit's packages under
// configuration c, in the order of their paths.
func checkPrograms(ctx context.Context, fset *token.FileSet, from unitDir, pkgs []*packages.Package, c *Configuration) []programFile {
	return checkProgramFiles(ctx, fset, from, pkgs, c, programPaths(pkgs, c))
}

// checkScripts checks, each alone against the packages pkgs loaded, every script of
// the dropped module rooted at dir: a file run with go run on its own, which no build
// of its package compiles because a sibling file declares main as well. A package a
// script imports that pkgs do not hold is loaded from dir, so the script's own module
// file decides it.
func checkScripts(ctx context.Context, fset *token.FileSet, dir string, pkgs []*packages.Package, c *Configuration) []programFile {
	return checkProgramFiles(ctx, fset, unitDir{dir: dir, workspace: workspaceOff}, pkgs, c, scriptPaths(dir, c))
}

// scriptPaths lists, sorted, every Go file below the module root dir, in no nested
// module and no directory discovery skips, other than a test file, that
// configuration c builds with or without the ignore tag and that declares a function
// main with no receiver in package main, where another such file shares its directory.
func scriptPaths(dir string, c *Configuration) []string {
	without := cgoEnabledContext(c)
	with := cgoEnabledContext(c)
	with.BuildTags = append(with.BuildTags, buildconstraint.IgnoreTag)
	byDir := make(map[string][]string)
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case entry.IsDir() && path != dir && (goRow.Skips(entry.Name()) || IsModuleRoot(path)):
			return filepath.SkipDir
		case !entry.IsDir() && isScript(&without, &with, path):
			parent := filepath.Dir(path)
			byDir[parent] = append(byDir[parent], path)
		}
		return nil
	})
	var paths []string
	for _, held := range byDir {
		if len(held) > 1 {
			paths = append(paths, held...)
		}
	}
	slices.Sort(paths)
	return paths
}

// isScript reports whether the file at path is a Go file other than a test file that
// the context without or the context with builds and that declares main.
func isScript(without, with *build.Context, path string) bool {
	if filepath.Ext(path) != goFileExt || strings.HasSuffix(path, "_test.go") {
		return false
	}
	parent, name := filepath.Split(path)
	built, err := without.MatchFile(parent, name)
	if err == nil && !built {
		built, err = with.MatchFile(parent, name)
	}
	return err == nil && built && declaresMain(path)
}

// declaresMain reports whether the file at path declares package main and a function
// main with no receiver.
func declaresMain(path string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil || f.Name.Name != mainPackageName {
		return false
	}
	return slices.ContainsFunc(f.Decls, func(decl ast.Decl) bool {
		fn, ok := decl.(*ast.FuncDecl)
		return ok && fn.Recv == nil && fn.Name.Name == mainFunction
	})
}

// checkProgramFiles checks each file of paths that declares package main alone under
// configuration c, against pkgs and what loads from from.
func checkProgramFiles(ctx context.Context, fset *token.FileSet, from unitDir, pkgs []*packages.Package, c *Configuration, paths []string) []programFile {
	if len(paths) == 0 {
		return nil
	}
	imports := newCgoImporter(ctx, fset, from, c, pkgs)
	sizes := types.SizesFor(buildconstraint.Compiler, c.Arch)
	version := mainModuleVersion(pkgs)
	held := make([]programFile, 0, len(paths))
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil || f.Name.Name != mainPackageName {
			continue
		}
		info := &types.Info{
			Types:      make(map[ast.Expr]types.TypeAndValue),
			Uses:       make(map[*ast.Ident]types.Object),
			Selections: make(map[*ast.SelectorExpr]*types.Selection),
		}
		conf := &types.Config{GoVersion: version, Importer: imports, Sizes: sizes, Error: func(error) {}}
		// The errors are dropped by the Error function above: a program the
		// configuration cannot fully check still names what it resolved.
		_, _ = conf.Check(mainPackageName, fset, []*ast.File{f}, info)
		var imported []string
		for _, spec := range f.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil {
				imported = append(imported, path)
			}
		}
		held = append(held, programFile{Info: info, Imports: imported})
	}
	return held
}

// programPaths lists, sorted, every Go file a main-module package ignores that
// configuration c builds once the ignore tag is set and does not build without
// it. Both answers are read with cgo enabled, so a file ignored for importing "C"
// alone is no program.
func programPaths(pkgs []*packages.Package, c *Configuration) []string {
	without := cgoEnabledContext(c)
	with := cgoEnabledContext(c)
	with.BuildTags = append(with.BuildTags, buildconstraint.IgnoreTag)
	seen := make(map[string]bool)
	var paths []string
	for _, p := range pkgs {
		if p.Module == nil || !p.Module.Main {
			continue
		}
		for _, path := range p.IgnoredFiles {
			if seen[path] || filepath.Ext(path) != goFileExt {
				continue
			}
			seen[path] = true
			if onlyWith(&without, &with, path) {
				paths = append(paths, path)
			}
		}
	}
	slices.Sort(paths)
	return paths
}

// onlyWith reports whether the context with builds the file at path and the
// context without does not.
func onlyWith(without, with *build.Context, path string) bool {
	dir, name := filepath.Split(path)
	if built, err := without.MatchFile(dir, name); err != nil || built {
		return false
	}
	run, err := with.MatchFile(dir, name)
	return err == nil && run
}

// mainModuleVersion is the language version the load checked the main module's
// packages under.
func mainModuleVersion(pkgs []*packages.Package) string {
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Main && p.Types != nil {
			return p.Types.GoVersion()
		}
	}
	return ""
}
