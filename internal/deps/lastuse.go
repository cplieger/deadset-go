package deps

import (
	"go/ast"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// LastUses names, per deletion candidate, the modules whose last use inside the
// target is written within that candidate's declaration, each module path sorted.
// A candidate that holds no module's last use has no entry.
//
// A module is named only where no other declaration of the target uses any package
// of it, because deleting one declaration of two that use a module leaves the
// module used. Every declaration counts as a user, whatever its own liveness and
// whichever file declares it, so a module a dead declaration shares with another
// dead one is named for neither, and one a test declaration also uses is named for
// neither. A use belongs to the innermost declaration the inventory holds that
// contains it, so the type of a struct field is the field's use of a module rather
// than the enclosing type's.
func LastUses(r *load.Result, resolve *graph.Resolver, candidates []graph.Candidate) map[graph.SymbolID][]string {
	if r == nil || resolve == nil || len(candidates) == 0 {
		return nil
	}

	used := usedModules(r, resolve)
	users := make(map[string]int)
	for _, modules := range used {
		for module := range modules {
			users[module]++
		}
	}

	last := make(map[graph.SymbolID][]string)
	for _, candidate := range candidates {
		if sole := soleUses(used[candidate.ID], users); len(sole) > 0 {
			last[candidate.ID] = sole
		}
	}
	return last
}

// soleUses lists, sorted, the modules of one declaration's use set that no other
// declaration uses.
func soleUses(modules map[string]bool, users map[string]int) []string {
	sole := make([]string, 0, len(modules))
	for module := range modules {
		if users[module] == 1 {
			sole = append(sole, module)
		}
	}
	slices.Sort(sole)
	return sole
}

// usedModules walks every file of every loaded package of the target once and
// returns, per declaration the inventory holds, the dependency modules it uses.
func usedModules(r *load.Result, resolve *graph.Resolver) map[graph.SymbolID]map[string]bool {
	u := &uses{
		resolve: resolve,
		modules: dependencyModules(r),
		used:    make(map[graph.SymbolID]map[string]bool),
	}
	for _, p := range r.Packages {
		if p.TypesInfo == nil {
			continue
		}
		u.info = p.TypesInfo
		for _, file := range p.Syntax {
			u.walk(file, nil)
		}
	}
	return u.used
}

// dependencyModules maps the import path of every package the load reached
// outside the target's own module onto the module that provides it. A package of
// the standard library belongs to no module and is in no entry.
func dependencyModules(r *load.Result) map[string]string {
	modules := make(map[string]string)
	packages.Visit(r.Packages, nil, func(p *packages.Package) {
		if p.Module == nil || p.Module.Main || p.PkgPath == "" {
			return
		}
		modules[p.PkgPath] = p.Module.Path
	})
	return modules
}

// uses accumulates, per declaration of the target, the dependency modules whose
// packages the declaration uses.
type uses struct {
	info    *types.Info
	resolve *graph.Resolver
	modules map[string]string
	used    map[graph.SymbolID]map[string]bool
}

// walk records every use written inside n against owner, except inside a
// declaration the inventory holds, which owns the uses written inside it. A
// declaration the inventory does not hold, which a local variable, a parameter and
// a result all are, leaves its uses with the declaration around it.
func (u *uses) walk(n ast.Node, owner []graph.SymbolID) {
	ast.Inspect(n, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		if node == n {
			return true
		}
		if inner := u.declared(node); len(inner) > 0 {
			u.walk(node, inner)
			return false
		}
		if ident, ok := node.(*ast.Ident); ok {
			u.record(ident, owner)
		}
		return true
	})
}

// declared returns the identifiers of the declarations the inventory holds at the
// names one node declares. A group declaring several names gives all of them, so a
// module the group uses is not named for one name of the group alone.
func (u *uses) declared(node ast.Node) []graph.SymbolID {
	switch d := node.(type) {
	case *ast.FuncDecl:
		return u.heldAt(d.Name)
	case *ast.TypeSpec:
		return u.heldAt(d.Name)
	case *ast.ValueSpec:
		return u.heldAt(d.Names...)
	case *ast.Field:
		if len(d.Names) > 0 {
			return u.heldAt(d.Names...)
		}
		if embedded := embeddedName(d.Type); embedded != nil {
			return u.heldAt(embedded)
		}
	}
	return nil
}

// heldAt returns the identifiers of the declarations the inventory holds at the
// positions the given names are written at.
func (u *uses) heldAt(names ...*ast.Ident) []graph.SymbolID {
	held := make([]graph.SymbolID, 0, len(names))
	for _, name := range names {
		if id, holds := u.resolve.At(name.Pos()); holds {
			held = append(held, id)
		}
	}
	return held
}

// record keeps one identifier's use of a dependency module against the
// declarations that own it. An identifier denoting no object, a predeclared one
// and one of the target's own packages carry no such use.
func (u *uses) record(ident *ast.Ident, owner []graph.SymbolID) {
	if len(owner) == 0 {
		return
	}
	obj := u.info.Uses[ident]
	if obj == nil || obj.Pkg() == nil {
		return
	}
	module, outside := u.modules[obj.Pkg().Path()]
	if !outside {
		return
	}
	for _, id := range owner {
		if u.used[id] == nil {
			u.used[id] = make(map[string]bool)
		}
		u.used[id][module] = true
	}
}

// embeddedName returns the identifier that names an embedded field, which the
// language defines as the unqualified name of the type embedded.
func embeddedName(expr ast.Expr) *ast.Ident {
	switch t := expr.(type) {
	case *ast.Ident:
		return t
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	}
	return nil
}
