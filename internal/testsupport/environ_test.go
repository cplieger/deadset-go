package testsupport_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// environBase is the one function of the module that may build a child's
// environment on os.Environ(): the loader it hands that environment to names the
// load's directory in PWD after it.
const environBase = "internal/load/load.go:loadEnv"

// TestEveryChildOfTheModuleTakesItsEnvironmentFromItsCommand refuses os.Environ()
// in any Go file of the module but [environBase]: a child built on it with a
// directory of its own inherits the caller's PWD, so its working directory reads as
// the path with no link where the caller spells it through one, as macOS spells
// TMPDIR. A command's Environ() names its directory in PWD.
func TestEveryChildOfTheModuleTakesItsEnvironmentFromItsCommand(t *testing.T) {
	root := moduleRoot(t)
	read := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		read++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if isFunc && filepath.ToSlash(rel)+":"+fn.Name.Name == environBase {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, isCall := n.(*ast.CallExpr); isCall && callsOSEnviron(call) {
					t.Errorf("%s:%d: os.Environ(); build a child's environment on its command's Environ(), which names its directory in PWD", rel, fset.Position(call.Pos()).Line)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: walk %s: %v", root, err)
	}
	if read < 100 {
		t.Fatalf("Setup: the walk read %d Go files, want the module's", read)
	}
}

// callsOSEnviron reports whether call is os.Environ().
func callsOSEnviron(call *ast.CallExpr) bool {
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector || selector.Sel.Name != "Environ" {
		return false
	}
	pkg, isIdent := selector.X.(*ast.Ident)
	return isIdent && pkg.Name == "os"
}

// moduleRoot is the directory of the go.mod above this package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Setup: %v", err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("Setup: no go.mod above the package directory")
		}
		dir = parent
	}
}
