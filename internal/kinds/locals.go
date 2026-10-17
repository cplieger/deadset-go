package kinds

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// localDeclaration is one constant or type a function body declares, with the
// specification that declares it and whether the body references it outside it.
type localDeclaration struct {
	name *ast.Ident
	spec ast.Spec
	kind string
	used bool
}

// unusedLocals is the unused-unexported kind's answer about the constants and types
// function bodies declare, a function literal of a package-level variable's
// initializer included: one finding per such declaration nothing in its body
// references outside its own specification, under the enclosing declaration's
// reference. A declaration one configuration finds referenced is referenced.
func (g *intrafunc) unusedLocals() ([]Finding, error) {
	held := newParts()
	for i := range g.in.Per {
		one := &g.in.Per[i]
		for _, fn := range append(functions(one), initializers(one)...) {
			if err := localsOf(one, &fn, held); err != nil {
				return nil, err
			}
		}
	}
	found, err := g.findings(vocab.UnusedUnexported, held.unused())
	for i := range found {
		found[i].local = true
	}
	return found, err
}

// localsOf records every local declaration of one walked function.
func localsOf(one *Configured, fn *walked, held *parts) error {
	for _, local := range localDeclarations(fn.info, fn.root()) {
		at, err := one.Resolve.Render(local.name.Pos())
		if err != nil {
			return err
		}
		held.hold(&part{
			id:       fn.id,
			kind:     local.kind,
			name:     local.name.Name,
			message:  local.kind + " " + local.name.Name + " is never referenced in the body that declares it",
			position: positionAt(at),
			used:     local.used,
		})
	}
	return nil
}

// localDeclarations is every named constant and type one body declares, in source
// order, each marked used where an identifier outside its specification denotes it.
func localDeclarations(info *types.Info, root ast.Node) []*localDeclaration {
	declared := declaredLocals(info, root)
	if len(declared) == 0 {
		return nil
	}
	byObject := make(map[types.Object]*localDeclaration, len(declared))
	for _, local := range declared {
		byObject[info.Defs[local.name]] = local
	}
	ast.Inspect(root, func(n ast.Node) bool {
		name, isName := n.(*ast.Ident)
		if !isName {
			return true
		}
		local := byObject[info.Uses[name]]
		if local != nil && (name.Pos() < local.spec.Pos() || name.Pos() >= local.spec.End()) {
			local.used = true
		}
		return true
	})
	return declared
}

// declaredLocals is every named constant and type the declaration statements of
// one body declare, in source order.
func declaredLocals(info *types.Info, root ast.Node) []*localDeclaration {
	var declared []*localDeclaration
	ast.Inspect(root, func(n ast.Node) bool {
		if group := localGroup(n); group != nil {
			for _, spec := range group.Specs {
				declared = append(declared, specLocals(info, spec)...)
			}
		}
		return true
	})
	return declared
}

// localGroup is the constant or type declaration one statement makes, and nil for
// any other node.
func localGroup(n ast.Node) *ast.GenDecl {
	statement, isDecl := n.(*ast.DeclStmt)
	if !isDecl {
		return nil
	}
	group, isGroup := statement.Decl.(*ast.GenDecl)
	if !isGroup || group.Tok != token.CONST && group.Tok != token.TYPE {
		return nil
	}
	return group
}

// specLocals is every named declaration one constant or type specification makes.
func specLocals(info *types.Info, spec ast.Spec) []*localDeclaration {
	var declared []*localDeclaration
	for _, name := range specNames(spec) {
		if object := info.Defs[name]; object != nil && name.Name != blankName {
			declared = append(declared, &localDeclaration{name: name, spec: spec, kind: localKind(object)})
		}
	}
	return declared
}

func localKind(object types.Object) string {
	if _, isConst := object.(*types.Const); isConst {
		return symbolKinds[inventory.KindConst]
	}
	return symbolKinds[inventory.KindType]
}
