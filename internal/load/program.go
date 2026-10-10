package load

import (
	"context"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"

	"golang.org/x/tools/go/packages"
)

// ignoreTag is the tag the toolchain's convention for a file run by hand
// constrains it on, which no configuration sets.
const ignoreTag = "ignore"

// program is one file of the target that its build constraint keeps out of the
// configuration unless the ignore tag is set, and that declares package main: a
// program of its own, run with go run, that no package of the target compiles.
//
// Info is what checking the file alone against the configuration's packages
// recorded, so every declaration of the target the file names resolves to the
// object the target's own packages declare. The check may have reported errors,
// and what it resolved is recorded all the same.
type program struct {
	Info *types.Info
}

// checkPrograms finds and checks every program of the target's packages under
// configuration c, in the order of their paths.
func checkPrograms(ctx context.Context, fset *token.FileSet, target string, pkgs []*packages.Package, c Configuration) []program {
	paths := programPaths(pkgs, c)
	if len(paths) == 0 {
		return nil
	}
	imports := newCgoImporter(ctx, fset, target, c, pkgs)
	sizes := types.SizesFor(gcCompiler, c.Arch)
	version := mainModuleVersion(pkgs)
	held := make([]program, 0, len(paths))
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
		held = append(held, program{Info: info})
	}
	return held
}

// programPaths lists, sorted, every Go file a main-module package ignores that
// configuration c builds once the ignore tag is set and does not build without
// it. Both answers are read with cgo enabled, so a file ignored for importing "C"
// alone is no program.
func programPaths(pkgs []*packages.Package, c Configuration) []string {
	without := cgoEnabledContext(c)
	with := cgoEnabledContext(c)
	with.BuildTags = append(with.BuildTags, ignoreTag)
	seen := make(map[string]bool)
	var paths []string
	for _, p := range pkgs {
		if p.Module == nil || !p.Module.Main {
			continue
		}
		for _, path := range p.IgnoredFiles {
			if seen[path] || filepath.Ext(path) != ".go" {
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
