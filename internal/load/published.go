package load

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Published reports whether a program outside the module can import the package at
// pkgPath: not a main package, not an external test package, and not below an
// internal element.
func Published(pkgPath string, main bool) bool {
	return !main && !strings.HasSuffix(pkgPath, "_test") && !slices.Contains(strings.Split(pkgPath, "/"), "internal")
}

// generatedFiles is the package clause of every file of pkgs carrying the
// generated-code header, once per parse of the file.
func generatedFiles(pkgs []*packages.Package) []token.Pos {
	var clauses []token.Pos
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			if ast.IsGenerated(f) {
				clauses = append(clauses, f.Package)
			}
		}
	}
	return clauses
}
