package facts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestExtensionNameReadsTheNameOfAKeyedLiteralAlone(t *testing.T) {
	const src = `package p

type info struct {
	Extended any
	Name     string
}

var (
	positional = info{nil, "pkg.ext"}
	keyed      = info{Extended: nil, Name: "pkg.ext"}
)
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Types: make(map[ast.Expr]types.TypeAndValue)}
	if _, err := (&types.Config{}).Check("p", fset, []*ast.File{f}, info); err != nil {
		t.Fatal(err)
	}
	values := make(map[string]ast.Expr)
	ast.Inspect(f, func(n ast.Node) bool {
		if v, ok := n.(*ast.ValueSpec); ok {
			values[v.Names[0].Name] = v.Values[0]
		}
		return true
	})
	for name, want := range map[string]string{"positional": "", "keyed": "pkg.ext"} {
		if got := extensionName(info, values[name]); got != want {
			t.Errorf("extensionName(%s) = %q, want %q", name, got, want)
		}
	}
}
