package graph

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// literalStructs indexes every struct type literal the walked packages write
// outside a type declaration, by its type string, so a field selected through one
// is found in each identical literal: identical unnamed struct types are one type
// (https://go.dev/ref/spec#Type_identity), and the checker gives each spelling its
// own field objects.
type literalStructs struct {
	byKey  map[string][]*types.Struct
	fields map[*types.Var]fieldAt
}

// fieldAt is the struct literal one field belongs to and its index there.
type fieldAt struct {
	st    *types.Struct
	index int
}

// add indexes the struct type literals of every file of the packages.
func (l *literalStructs) add(pkgs []*packages.Package) {
	if l.byKey == nil {
		l.byKey = make(map[string][]*types.Struct)
		l.fields = make(map[*types.Var]fieldAt)
	}
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, f := range pkg.Syntax {
			l.addFile(pkg.TypesInfo, f)
		}
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
			if st, isStruct := info.TypeOf(n).(*types.Struct); isStruct && !named[n] {
				l.keep(st)
			}
		}
		return true
	})
}

// keep indexes one struct type literal, once however many files spell it.
func (l *literalStructs) keep(st *types.Struct) {
	if st.NumFields() == 0 {
		return
	}
	if _, seen := l.fields[st.Field(0)]; seen {
		return
	}
	key := types.TypeString(st, nil)
	l.byKey[key] = append(l.byKey[key], st)
	for i := range st.NumFields() {
		l.fields[st.Field(i)] = fieldAt{st: st, index: i}
	}
}

// twins is every field at the same index of each other struct literal identical to
// the one that declares field.
func (l *literalStructs) twins(field *types.Var) []*types.Var {
	at, held := l.fields[field]
	if !held {
		return nil
	}
	var found []*types.Var
	for _, other := range l.byKey[types.TypeString(at.st, nil)] {
		if other != at.st && types.Identical(other, at.st) {
			found = append(found, other.Field(at.index))
		}
	}
	return found
}
