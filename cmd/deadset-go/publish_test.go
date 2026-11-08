package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// modeAsks are the filesafe calls that restrict a file's readers. Each one returns
// ErrModeUnsupported on Windows before it writes anything, and no document this
// module writes needs restricted readers, so a call to one is a run that fails on
// Windows.
var modeAsks = map[string]bool{
	"WithMode":         true,
	"WithMkdirMode":    true,
	"EnforceMode":      true,
	"EnsurePrivateDir": true,
}

// TestNoWriteAsksFilesafeForAMode reads every production file of the module and
// refuses a call to a filesafe mode option, whatever name the import takes.
func TestNoWriteAsksFilesafeForAMode(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	walked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || (path != root && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		walked++
		for _, call := range modeAsksIn(file) {
			t.Errorf("%s calls filesafe.%s, which fails on Windows", fset.Position(call.Pos()), call.Sel.Name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	if walked == 0 {
		t.Fatal("walked no production file of the module")
	}
}

// modeAsksIn returns every selector in file that names a mode option of the
// filesafe package under the name file imports it as.
func modeAsksIn(file *ast.File) []*ast.SelectorExpr {
	name := ""
	for _, spec := range file.Imports {
		if p, _ := strconv.Unquote(spec.Path.Value); p == "github.com/cplieger/go/filesafe" {
			name = "filesafe"
			if spec.Name != nil {
				name = spec.Name.Name
			}
		}
	}
	if name == "" {
		return nil
	}
	var asks []*ast.SelectorExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == name && modeAsks[sel.Sel.Name] {
				asks = append(asks, sel)
			}
		}
		return true
	})
	return asks
}

// createdMode is the mode os.Create gives a new file in dir: the mode a document
// written with no mode option carries.
func createdMode(t *testing.T, dir string) fs.FileMode {
	t.Helper()
	probe, err := os.OpenFile(filepath.Join(dir, "mode-probe"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatalf("create a probe file in %s: %v", dir, err)
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("close the probe file: %v", err)
	}
	info, err := os.Stat(probe.Name())
	if err != nil {
		t.Fatalf("stat the probe file: %v", err)
	}
	if err := os.Remove(probe.Name()); err != nil {
		t.Fatalf("remove the probe file: %v", err)
	}
	return info.Mode().Perm()
}
