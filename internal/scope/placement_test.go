package scope

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// moduleRoot is the directory of this module's go.mod, two above this package.
const moduleRoot = "../.."

// placedHere are the directories, relative to [moduleRoot], whose files may place a
// path themselves: this package, the one resolver, and the test support the suites
// share, which places a test's own temporary files and is no product code.
var placedHere = []string{"internal/scope", "internal/testsupport"}

// commentMarkers are the string literals a prefix test may hold that name no path:
// the markers of a Go comment.
var commentMarkers = []string{`"//"`, `"/*"`, `"*/"`}

// No product package places a path but through this one: every non-test Go file
// outside [placedHere] is free of filepath.Rel, os.SameFile, and a strings prefix
// test (HasPrefix, TrimPrefix, CutPrefix) with a path separator in its arguments,
// filepath.Separator, os.PathSeparator or a string literal holding a slash or a
// backslash other than a comment marker. Each such call compares a path by its
// names alone, which places a path spelled through a link outside the directory it
// resolves into: [Root.Place] and [UnderName] are the placements. Nor does one name
// a file it parses: it calls no go/parser ParseFile or ParseDir and adds no file to
// a file set itself, as [Root.Parse] parses a file by the one name the run gives it
// ([Root.Name]) and [ParseText] parses one under no name. A failure names each site.
func TestNoPackageButScopePlacesAPath(t *testing.T) {
	fset := token.NewFileSet()
	var sites []string
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || slices.Contains(placedHere, rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		found, err := placementsIn(fset, path, rel)
		sites = append(sites, found...)
		return err
	})
	if err != nil {
		t.Fatalf("read the module's Go files: %v", err)
	}
	for _, site := range sites {
		t.Error(site)
	}
	if fset.Base() == 1 {
		t.Fatal("the walk read no Go file, so this test pins nothing")
	}
}

// placementsIn is each call in the Go file at path, named rel, that places a path
// by its names or names a file it parses ([TestNoPackageButScopePlacesAPath]), with
// what to call instead.
func placementsIn(fset *token.FileSet, path, rel string) ([]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	imported := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imported[name] = importPath
	}
	var sites []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		pkg, fn := calledFunc(call, imported)
		var instead string
		switch {
		case pkg == "path/filepath" && fn == "Rel", pkg == "os" && fn == "SameFile":
			instead = placeInstead
		case pkg == "strings" && (fn == "HasPrefix" || fn == "TrimPrefix" || fn == "CutPrefix") && holdsSeparator(call.Args, imported):
			instead = placeInstead
		case pkg == "go/parser" && (fn == "ParseFile" || fn == "ParseDir"):
			instead = parseInstead
		case pkg == "" && fn == "AddFile":
			pkg, instead = "token.FileSet", parseInstead
		default:
			return true
		}
		sites = append(sites, fmt.Sprintf("%s:%d: %s.%s: %s", rel, fset.Position(call.Pos()).Line, pkg, fn, instead))
		return true
	})
	return sites, nil
}

// What a site [placementsIn] names calls instead.
const (
	placeInstead = "it places a path by its names; place it through scope (Root.Place, Root.PlaceEntry, UnderName)"
	parseInstead = "it names a file it parses; parse it through scope (Root.Parse by its Root.Name, or ParseText)"
)

// calledFunc is the import path and the name of the function or method call calls
// through a selector: the import path empty where the selector names no import, a
// method of a value.
func calledFunc(call *ast.CallExpr, imported map[string]string) (pkg, fn string) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", ""
	}
	if x, isIdent := sel.X.(*ast.Ident); isIdent {
		return imported[x.Name], sel.Sel.Name
	}
	return "", sel.Sel.Name
}

// holdsSeparator reports whether any of args mentions a path separator:
// filepath.Separator, os.PathSeparator, or a string literal holding a slash or a
// backslash that is no comment marker.
func holdsSeparator(args []ast.Expr, imported map[string]string) bool {
	held := false
	for _, arg := range args {
		ast.Inspect(arg, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.SelectorExpr:
				if x, isIdent := e.X.(*ast.Ident); isIdent {
					pkg := imported[x.Name]
					held = held || pkg == "path/filepath" && e.Sel.Name == "Separator" || pkg == "os" && e.Sel.Name == "PathSeparator"
				}
			case *ast.BasicLit:
				if e.Kind == token.STRING && !slices.Contains(commentMarkers, e.Value) {
					value, err := strconv.Unquote(e.Value)
					held = held || err == nil && strings.ContainsAny(value, `/\`)
				}
			}
			return !held
		})
	}
	return held
}
