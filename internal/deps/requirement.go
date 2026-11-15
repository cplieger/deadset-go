package deps

import (
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/load"
)

// UnusedRequirements returns the require directives of f whose module provides no
// package this configuration's load imports, in the order the module file
// declares them.
//
// A requirement the file marks indirect is never in the population: it carries no
// import by construction and exists to pin a transitive version under module graph
// pruning, so removing it changes the build list. What remains is every direct
// requirement no import in the target needs, which is the whole claim: the
// declaration says the target depends on the module directly and nothing in it
// does. A requirement whose module provides only a package another module's
// package imports is in the population for the same reason, because no package of
// the target imports it either; narrowing such a declaration to an indirect one
// is what makes the requirement honest, and deleting it is not.
func UnusedRequirements(f File, r *load.Result) []Requirement {
	if r == nil {
		return nil
	}
	if unloadedLeftOut(r) {
		return nil
	}
	imported := importedModules(r)
	unused := make([]Requirement, 0, len(f.Requires))
	for _, require := range f.Requires {
		if require.Indirect || imported[require.Path] {
			continue
		}
		unused = append(unused, require)
	}
	return unused
}

// unloadedLeftOut reports whether a package of a unit of r that the run leaves out
// did not load, so it may import a package of any module and need every
// directive of its module file ([load.LeftOutPackage]).
func unloadedLeftOut(r *load.Result) bool {
	return slices.ContainsFunc(r.LeftOut, func(left load.LeftOutPackage) bool {
		return left.Path == "" && unitModule(r, left.Module)
	})
}

// unitModule reports whether module is the module path of a unit of r.
func unitModule(r *load.Result, module string) bool {
	return module != "" && slices.ContainsFunc(r.Units, func(u load.Unit) bool { return u.Path == module })
}

// importedModules is the set of module paths providing a package a package of the
// target imports, one level out of each, test variants included, or one a unit's
// left-out package imports ([load.LeftOutPackage]). A dependency's own imports are
// not followed: a module only they reach provides no package the target imports.
// An import in a file no configuration selects is invisible until one selects it,
// so a caller intersects configurations, and one in a file excluded for reaching C
// is invisible in every configuration.
func importedModules(r *load.Result) map[string]bool {
	imported := make(map[string]bool)
	for _, p := range r.Packages {
		for _, dependency := range p.Imports {
			if dependency.Module != nil {
				imported[dependency.Module.Path] = true
			}
		}
	}
	for i := range r.LeftOut {
		if left := &r.LeftOut[i]; unitModule(r, left.Module) {
			for _, module := range left.Modules {
				imported[module] = true
			}
		}
	}
	return imported
}
