package graph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
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
			if named := EmbeddedName(field); named != nil {
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

func TestLinknameDirectiveReadsBothNames(t *testing.T) {
	cases := map[string]struct {
		text       string
		wantLocal  string
		wantRemote string
		wantOK     bool
	}{
		"one argument":                  {text: "//go:linkname pushed", wantLocal: "pushed", wantOK: true},
		"two arguments":                 {text: "//go:linkname pulled example.com/other.f", wantLocal: "pulled", wantRemote: "example.com/other.f", wantOK: true},
		"the standard library's form":   {text: "//go:linknamestd pushed", wantLocal: "pushed", wantOK: true},
		"a remote name in another form": {text: "//go:linkname local runtime.nanotime", wantLocal: "local", wantRemote: "runtime.nanotime", wantOK: true},
		"a remote name with no path":    {text: "//go:linkname local nanotime", wantLocal: "local", wantRemote: "nanotime", wantOK: true},
		"tabs between the words":        {text: "//go:linkname\tlocal\texample.com/other.f", wantLocal: "local", wantRemote: "example.com/other.f", wantOK: true},
		"three arguments":               {text: "//go:linkname a b c"},
		"no argument":                   {text: "//go:linkname"},
		"another directive":             {text: "//go:embed catalog.json"},
		"a space before the name":       {text: "// go:linkname pushed"},
		// The two spellings are matched whole: a longer word that starts with one
		// of them is a different directive, or prose, and binds nothing.
		"a spelling extended by a letter":    {text: "//go:linknamed local"},
		"a spelling extended by two letters": {text: "//go:linknamestdx local"},
		"a spelling cut short":               {text: "//go:linknam local"},
		"prose naming the directive":         {text: "// The //go:linkname directive names a symbol."},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			local, remote, ok := LinknameDirective(test.text)
			if local != test.wantLocal || remote != test.wantRemote || ok != test.wantOK {
				t.Errorf("LinknameDirective(%q) = %q, %q, %t, want %q, %q, %t",
					test.text, local, remote, ok, test.wantLocal, test.wantRemote, test.wantOK)
			}
		})
	}
}
