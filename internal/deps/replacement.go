package deps

import (
	"strings"

	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// NoopReplacements returns the replace directives of f that redirect nothing, in
// the order the module file declares them.
//
// A replace directive redirects the module it names, so the one case in which it
// is exactly a no-op is the module it names being absent from the build list:
// nothing selects that module, so nothing is redirected and deleting the directive
// leaves every version and every source directory the build uses unchanged. A
// directive whose module the build list holds is not reported, whether the
// replacement is another version or a directory, and neither is one that redirects
// the module a tool directive's package belongs to.
//
// The build list this reads is the modules that provide a package the load
// reached, which is narrower than the module graph in two ways a caller has to
// know. A module only a configuration this load is not reaches is absent, so the
// caller intersects the configurations it ran. A module in the graph that provides
// no package at all is absent too, and a directive over one is reported.
func NoopReplacements(f File, r *load.Result) []Replacement {
	if r == nil {
		return nil
	}
	built := buildList(r)
	tools := toolModules(f)
	noop := make([]Replacement, 0, len(f.Replaces))
	for _, replace := range f.Replaces {
		if tools[replace.Old.Path] || built[replace.Old] {
			continue
		}
		noop = append(noop, replace)
	}
	return noop
}

// buildList is the set of modules providing a package the load reached, the
// target's own module and every dependency at every depth alike.
//
// A module a directive replaced is in the list under the identity the directive
// replaced, with what replaced it recorded beside it, so a directive is answered by
// looking up the module it names. Each module is in the set twice, once with the
// version the build selected and once with no version at all, because a directive
// that names no version replaces the module at every version.
func buildList(r *load.Result) map[Module]bool {
	built := make(map[Module]bool)
	packages.Visit(r.Packages, nil, func(p *packages.Package) {
		if p.Module == nil {
			return
		}
		built[Module{Path: p.Module.Path, Version: p.Module.Version}] = true
		built[Module{Path: p.Module.Path}] = true
	})
	return built
}

// toolModules is the set of required module paths a tool directive's package
// belongs to.
//
// The package a tool directive names is built by the toolchain from the module the
// directives select, so a replace of that module redirects a build even though no
// package of the module is loaded. Which module holds the package is decided the
// way the module system decides it: the longest declared module path the package
// path lies under.
func toolModules(f File) map[string]bool {
	tools := make(map[string]bool)
	for _, tool := range f.Tools {
		longest := ""
		for _, require := range f.Requires {
			if within(tool, require.Path) && len(require.Path) > len(longest) {
				longest = require.Path
			}
		}
		if longest != "" {
			tools[longest] = true
		}
	}
	return tools
}

// within reports whether the package path pkg lies inside the module at path.
func within(pkg, path string) bool {
	return pkg == path || strings.HasPrefix(pkg, path+"/")
}
