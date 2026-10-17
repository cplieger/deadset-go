package graph

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"golang.org/x/tools/go/packages"
)

// literalStructs indexes every struct type literal the walked files write outside a
// type declaration, by its type string, so a field selected through one is found in
// each identical literal: identical unnamed struct types are one type
// (https://go.dev/ref/spec#Type_identity), and the checker gives each spelling its
// own field objects.
type literalStructs struct {
	byKey  map[string][]literalStruct
	fields map[token.Pos]fieldAt // by the position a field is written at, which every variant shares
}

// literalStruct is one struct type literal and the type the variant that indexed it
// gives it.
type literalStruct struct {
	node *ast.StructType
	st   *types.Struct
}

// fieldAt is the struct literal one field belongs to and its index there.
type fieldAt struct {
	lit   literalStruct
	index int
}

// add indexes the struct type literals of every source file of the packages, under
// the variant [inventory.Sources] reads each with.
func (l *literalStructs) add(pkgs []*packages.Package, fset *token.FileSet) {
	if l.byKey == nil {
		l.byKey = make(map[string][]literalStruct)
		l.fields = make(map[token.Pos]fieldAt)
	}
	for pkg, f := range inventory.Sources(pkgs, fset) {
		l.addFile(pkg.TypesInfo, f)
	}
}

// addFile indexes the struct type literals of one file, a type declaration's own
// struct type excepted, because a defined type is identical only to itself.
func (l *literalStructs) addFile(info *types.Info, f *ast.File) {
	named := make(map[*ast.StructType]bool)
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.TypeSpec:
			if st, isStruct := n.Type.(*ast.StructType); isStruct && !n.Assign.IsValid() {
				named[st] = true
			}
		case *ast.StructType:
			if st, isStruct := info.TypeOf(n).(*types.Struct); isStruct && !named[n] && st.NumFields() > 0 {
				lit := literalStruct{node: n, st: st}
				key := types.TypeString(st, nil)
				l.byKey[key] = append(l.byKey[key], lit)
				for i := range st.NumFields() {
					l.fields[st.Field(i).Pos()] = fieldAt{lit: lit, index: i}
				}
			}
		}
		return true
	})
}

// twins is every field at the same index of each other struct literal identical to
// the one that declares field, each literal typed as info's variant types it where
// that variant compiles it: a variant's types are identical only to its own.
func (l *literalStructs) twins(info *types.Info, field *types.Var) []*types.Var {
	at, held := l.fields[field.Pos()]
	if !held {
		return nil
	}
	st := typedIn(info, at.lit)
	var found []*types.Var
	for _, other := range l.byKey[types.TypeString(at.lit.st, nil)] {
		if other.node == at.lit.node {
			continue
		}
		if twin := typedIn(info, other); types.Identical(twin, st) {
			found = append(found, twin.Field(at.index))
		}
	}
	return found
}

// typedIn is the type info gives one literal, and the indexing variant's where
// info's variant does not compile it.
func typedIn(info *types.Info, lit literalStruct) *types.Struct {
	if st, isStruct := info.TypeOf(lit.node).(*types.Struct); isStruct {
		return st
	}
	return lit.st
}
