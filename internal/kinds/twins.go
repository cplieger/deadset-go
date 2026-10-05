package kinds

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// platformTwins keys every function and method of the target by its package,
// receiver type and name, to the files that declare it. A key two files declare is
// one declaration written once per build configuration, which every
// configuration's callers call with one signature.
//
// Ignored files count too: a twin for a platform the matrix does not build still
// fixes the signature its callers there use.
func platformTwins(in *Input) map[string]map[string]bool {
	twins := make(map[string]map[string]bool)
	fset := token.NewFileSet()
	parsed := make(map[string]bool)
	for one, p := range in.typedPackages() {
		for _, f := range p.Syntax {
			holdFuncs(twins, p.PkgPath, one.Result.Fset.Position(f.FileStart).Filename, f)
		}
		for _, path := range p.IgnoredFiles {
			if parsed[path] || filepath.Ext(path) != goSuffix {
				continue
			}
			parsed[path] = true
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err == nil && f.Name.Name == p.Name {
				holdFuncs(twins, p.PkgPath, path, f)
			}
		}
	}
	return twins
}

// holdFuncs adds the file to the entry of every function and method it declares.
func holdFuncs(twins map[string]map[string]bool, pkgPath, file string, f *ast.File) {
	for _, d := range f.Decls {
		decl, ok := d.(*ast.FuncDecl)
		if !ok {
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
