package load

import (
	"context"
	"encoding/json"
	"os/exec"

	"golang.org/x/tools/go/packages"
)

// expectedModules is every module the project rooted at dir expects to exist: the
// main modules the listing names, which a workspace makes several of and which a
// failed listing still names, and every module the module file requires or
// replaces. A module file the toolchain cannot print contributes nothing, so the
// main modules alone decide.
func expectedModules(ctx context.Context, dir string, c Configuration, workspace string, pkgs []*packages.Package) []string {
	var modules []string
	seen := make(map[string]bool)
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			modules = append(modules, path)
		}
	}
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Main {
			add(p.Module.Path)
		}
	}

	file, readable := printedModuleFile(ctx, dir, loadEnv(c, workspace))
	if !readable {
		return modules
	}
	for _, r := range file.Require {
		add(r.Path)
	}
	for _, r := range file.Replace {
		add(r.Old.Path)
	}
	return modules
}

// moduleFile is the require and replace directives of a module file as the
// toolchain prints them.
type moduleFile struct {
	Require []moduleRequire
	Replace []moduleReplace
}

// moduleRequire is one printed require directive.
type moduleRequire struct{ Path string }

// moduleReplace is one printed replace directive; a replacement with no version is
// a directory.
type moduleReplace struct {
	Old struct{ Path string }
	New struct{ Path, Version string }
}

// printedModuleFile is the module file in dir as the toolchain prints it under env,
// and false where the toolchain cannot print it.
func printedModuleFile(ctx context.Context, dir string, env []string) (moduleFile, bool) {
	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json")
	cmd.Dir = dir
	cmd.Env = env
	printed, err := cmd.Output()
	if err != nil {
		return moduleFile{}, false
	}
	var file moduleFile
	if json.Unmarshal(printed, &file) != nil {
		return moduleFile{}, false
	}
	return file, true
}
