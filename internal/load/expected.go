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

	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json")
	cmd.Dir = dir
	cmd.Env = loadEnv(c, workspace)
	printed, err := cmd.Output()
	if err != nil {
		return modules
	}
	var file struct {
		Require []struct{ Path string }
		Replace []struct{ Old struct{ Path string } }
	}
	if json.Unmarshal(printed, &file) != nil {
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
