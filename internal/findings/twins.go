package findings

import (
	"go/ast"
	"go/parser"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// twins keys every function and method of the target by its package, receiver
// type and name, to the files that declare it. A key two files declare is one
// declaration written once per build configuration, which every configuration's
// callers call with one signature.
//
// Ignored files count too: a twin for a platform the matrix does not build still
// fixes the signature its callers there use. A blank function is no key: nothing
// calls it, so no caller fixes its signature.
type twins struct {
	keys   map[string]map[string]bool
	parsed map[string]bool // every ignored file read, by path
}

func newTwins() *twins {
	return &twins{keys: make(map[string]map[string]bool), parsed: make(map[string]bool)}
}

// read adds the functions one configuration's files declare and those of the
// files its packages ignore.
func (t *twins) read(one *Configured, files []typedFile) {
	for _, typed := range files {
		holdFuncs(t.keys, typed.pkg.PkgPath, typed.one.Result.Fset.Position(typed.file.FileStart).Filename, typed.file)
	}
	if one.Facts == nil {
		return
	}
	for p := range one.Facts.Typed() {
		for _, path := range p.IgnoredFiles {
			if t.parsed[path] || filepath.Ext(path) != goSuffix {
				continue
			}
			t.parsed[path] = true
			if f := twinFile(path); f != nil && f.Name.Name == p.Name {
				holdFuncs(t.keys, p.PkgPath, path, f)
			}
		}
	}
}

// twinFile is the declarations of the ignored Go file at path, nil where it
// cannot be read or parsed. No position of it is kept: twins key a file by its path.
func twinFile(path string) *ast.File {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, err := scope.ParseText(src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	return f
}

// holdFuncs adds the file to the entry of every function and method it declares.
func holdFuncs(twins map[string]map[string]bool, pkgPath, file string, f *ast.File) {
	for _, d := range f.Decls {
		decl, ok := d.(*ast.FuncDecl)
		if !ok || decl.Name.Name == blankName {
			continue
		}
		key := twinKey(pkgPath, decl)
		if twins[key] == nil {
			twins[key] = make(map[string]bool)
		}
		twins[key][file] = true
	}
}

// twinKey names one function or method of one package: the receiver's base type,
// pointer and type parameters dropped, then the name.
func twinKey(pkgPath string, decl *ast.FuncDecl) string {
	receiver := ""
	if decl.Recv != nil && len(decl.Recv.List) == 1 {
		receiver = baseTypeName(decl.Recv.List[0].Type)
	}
	return strings.Join([]string{pkgPath, receiver, decl.Name.Name}, " ")
}

// baseTypeName is the name of the type a receiver expression spells.
func baseTypeName(expr ast.Expr) string {
	switch t := ast.Unparen(expr).(type) {
	case *ast.StarExpr:
		return baseTypeName(t.X)
	case *ast.IndexExpr:
		return baseTypeName(t.X)
	case *ast.IndexListExpr:
		return baseTypeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}
