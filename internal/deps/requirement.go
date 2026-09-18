package deps

import "github.com/cplieger/deadset-go/internal/load"

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

// importedModules is the set of module paths providing a package a package of the
// target imports.
//
// Every package of the target is in the load, test variants included, so one level
// out of each of them is every import edge the target's own source writes. A
// dependency's own imports are deliberately not followed: a module reached only
// that way provides no package the target imports, which is what the population
// asks.
//
// Two edges are outside what this can see, and both are the load's limit rather
// than this rule's. An import written in a file no configuration of the matrix
// selects is invisible until a configuration that selects it runs, which is why a
// caller intersects configurations. An import written in a file excluded for
// reaching C is invisible in every configuration.
func importedModules(r *load.Result) map[string]bool {
	imported := make(map[string]bool)
	for _, p := range r.Packages {
		for _, dependency := range p.Imports {
			if dependency.Module != nil {
				imported[dependency.Module.Path] = true
			}
		}
	}
	return imported
}
