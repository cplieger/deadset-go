package liveness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

func TestEmbeddedNameIsTheUnqualifiedNameOfTheEmbeddedType(t *testing.T) {
	cases := map[string]struct {
		source string
		want   string
	}{
		"a type of the same package":        {source: "type T struct { Base }", want: "Base"},
		"a pointer to a type":               {source: "type T struct { *Base }", want: "Base"},
		"a qualified type":                  {source: "type T struct { pkg.Base }", want: "Base"},
		"a pointer to a qualified type":     {source: "type T struct { *pkg.Base }", want: "Base"},
		"a generic type with one argument":  {source: "type T struct { Base[int] }", want: "Base"},
		"a generic type with two arguments": {source: "type T struct { Base[int, string] }", want: "Base"},
		"a qualified generic type":          {source: "type T struct { pkg.Base[int] }", want: "Base"},
		"an embedded interface":             {source: "type T interface { Base }", want: "Base"},
		// An interface element that is not a type name names nothing, which is what
		// the walk of an interface body meets in a type set, and the field is left
		// alone.
		"a type set of two types":  {source: "type T interface { int | string }"},
		"an approximate type term": {source: "type T interface { ~int }"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			field := embeddedFieldOf(t, test.source)
			got := ""
			if named := inventory.EmbeddedName(field); named != nil {
				got = named.Name
			}
			if got != test.want {
				t.Errorf("EmbeddedName(%s) = %q, want %q", test.source, got, test.want)
			}
		})
	}
}

// embeddedFieldOf parses one type declaration and returns the type expression of
// its first member, which the declaration writes with no name: a struct's embedded
// field, or an interface's embedded interface or type-set element.
func embeddedFieldOf(t *testing.T, source string) ast.Expr {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "embedded.go", "package app\n"+source+"\n", parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse %q: %v", source, err)
	}
	decl, ok := file.Decls[0].(*ast.GenDecl)
	if !ok {
		t.Fatalf("Setup: %q is not a type declaration", source)
	}
	spec, ok := decl.Specs[0].(*ast.TypeSpec)
	if !ok {
		t.Fatalf("Setup: %q declares no type", source)
	}
	switch declared := spec.Type.(type) {
	case *ast.StructType:
		return declared.Fields.List[0].Type
	case *ast.InterfaceType:
		return declared.Methods.List[0].Type
	default:
		t.Fatalf("Setup: %q declares neither a struct nor an interface", source)
		return nil
	}
}
