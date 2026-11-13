package scope

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// No product package places, compares or orders a path but through this one: a
// non-test Go file outside [placedHere] calls no filepath.Rel, os.SameFile or
// strings prefix test with a path separator, compares no two paths by their names
// ([compared]), and parses no file under a name of its own, as each places a path
// spelled through a link apart from where it resolves. [Root.Place], [UnderName],
// [Order] and [SameName] place and compare, and [Root.Parse] and [ParseText] parse.
// A failure names each site with what to call instead.
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
		if what := compared(n, imported); what != "" {
			sites = append(sites, fmt.Sprintf("%s:%d: %s: %s", rel, fset.Position(n.Pos()).Line, what, compareInstead))
			return true
		}
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
	placeInstead   = "it places a path by its names; place it through scope (Root.Place, Root.PlaceEntry, UnderName)"
	parseInstead   = "it names a file it parses; parse it through scope (Root.Parse by its Root.Name, or ParseText)"
	compareInstead = "it compares paths by their spelling; compare through scope (Order, SameName, Listed for paths the run spelled once, else Same, SameEntry, Root.Is, Root.Below)"
)

// The words a name ends in that make its value a path ([pathValued]): the last word
// of a mixed-caps name, or the whole of a lower-case one, singular for a length.
// Roots are the liveness roots, symbols, so only a single Root is a path.
var (
	pathWord     = regexp.MustCompile(`(^|[a-z0-9_])(Root|Dirs?|Files?|Filenames?|Paths?)$|^(root|dirs?|files?|filenames?|paths?)$`)
	singularPath = regexp.MustCompile(`(^|[a-z0-9_])(Root|Dir|File|Filename|Path)$|^(root|dir|file|filename|path)$`)
	// importPath is a name for an import or module path, which names no file.
	importPath = regexp.MustCompile(`([Pp]kg|[Ii]mport|[Mm]od|[Mm]odule)Path$`)
)

// moduleHolders are the names of the values whose Path field is a module path:
// a module, a requirement, its old and new versions, and a unit of the run.
var moduleHolders = []string{"Mod", "Module", "module", "main", "Old", "New", "Version", "require", "unit", "units", "Units"}

// pathFuncs are the functions [compared] names a call of with a path among its
// first two arguments: each orders, searches or compares by the names.
var pathFuncs = []string{"strings.Compare", "cmp.Compare", "cmp.Less", "slices.Sort", "slices.Contains", "slices.Index", "slices.Compact", "slices.Equal", "slices.BinarySearch", "sort.Strings", "sort.SearchStrings"}

// compared describes n where it compares two paths by their names, empty elsewhere:
// == or != between two paths, an order (<, <=, >, >=) on a path or on a path's
// length, and a call of [pathFuncs] over paths. A comparison with a literal tests
// emptiness or a fixed name, and compares no two paths.
func compared(n ast.Node, imported map[string]string) string {
	switch e := n.(type) {
	case *ast.BinaryExpr:
		x, y := ast.Unparen(e.X), ast.Unparen(e.Y)
		lx, ly := lengthOf(x), lengthOf(y)
		switch {
		case e.Op != token.EQL && e.Op != token.NEQ && e.Op != token.LSS && e.Op != token.LEQ && e.Op != token.GTR && e.Op != token.GEQ,
			literal(x), literal(y):
			return ""
		case lx != nil || ly != nil:
			if lx != nil && pathValued(lx, imported, singularPath) || ly != nil && pathValued(ly, imported, singularPath) {
				return "a path's length " + e.Op.String() + " another value"
			}
		case e.Op == token.EQL || e.Op == token.NEQ:
			if pathValued(x, imported, pathWord) && pathValued(y, imported, pathWord) {
				return "two paths " + e.Op.String()
			}
		case pathValued(x, imported, pathWord) || pathValued(y, imported, pathWord):
			return "a path ordered by " + e.Op.String()
		}
	case *ast.CallExpr:
		pkg, fn := calledFunc(e, imported)
		name := pkg[strings.LastIndex(pkg, "/")+1:] + "." + fn
		if pkg == "" || !slices.Contains(pathFuncs, name) {
			return ""
		}
		for _, arg := range e.Args[:min(2, len(e.Args))] {
			if pathValued(arg, imported, pathWord) {
				return name + " over paths"
			}
		}
	}
	return ""
}

// lengthOf is the argument of e where e is a call of len, else nil.
func lengthOf(e ast.Expr) ast.Expr {
	call, isCall := e.(*ast.CallExpr)
	if !isCall || len(call.Args) != 1 {
		return nil
	}
	if fn, isIdent := call.Fun.(*ast.Ident); !isIdent || fn.Name != "len" {
		return nil
	}
	return ast.Unparen(call.Args[0])
}

// literal reports whether e is a basic literal or nil, true or false.
func literal(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return x.Name == "nil" || x.Name == "true" || x.Name == "false"
	}
	return false
}

// pathValued reports whether e is a path by its name: an identifier, a field, an
// element or a call result whose name ends in a word of words, or a path or
// path/filepath function that makes a path (Join, Clean, Dir, Abs, ToSlash,
// FromSlash). An import or module path is none ([importPath], [moduleHolders]), nor
// is a call of a Path method, the import path of a package.
func pathValued(e ast.Expr, imported map[string]string, words *regexp.Regexp) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SliceExpr:
			e = x.X
		case *ast.Ident:
			return words.MatchString(x.Name) && !importPath.MatchString(x.Name)
		case *ast.SelectorExpr:
			if x.Sel.Name == "Path" && slices.Contains(moduleHolders, lastName(x.X)) {
				return false
			}
			return words.MatchString(x.Sel.Name) && !importPath.MatchString(x.Sel.Name)
		case *ast.CallExpr:
			sel, isSel := x.Fun.(*ast.SelectorExpr)
			if !isSel {
				return false
			}
			if pkg, fn := calledFunc(x, imported); pkg == "path" || pkg == "path/filepath" {
				return slices.Contains([]string{"Join", "Clean", "Dir", "Abs", "ToSlash", "FromSlash"}, fn)
			}
			if sel.Sel.Name == "Path" && len(x.Args) == 0 {
				return false
			}
			e = sel
		default:
			return false
		}
	}
}

// lastName is the last name e spells: an identifier's, a field's or method's, or
// that of the value e indexes or calls.
func lastName(e ast.Expr) string {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return lastName(x.X)
	case *ast.CallExpr:
		return lastName(x.Fun)
	}
	return ""
}

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
