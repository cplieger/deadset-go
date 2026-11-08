package load

import (
	"bytes"
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// reachable returns the package with the given import path among everything a
// result's packages import, or nil.
func reachable(r Result, pkgPath string) *packages.Package {
	var found *packages.Package
	packages.Visit(r.Packages, nil, func(p *packages.Package) {
		if p.PkgPath == pkgPath && found == nil {
			found = p
		}
	})
	return found
}

func TestLoadKeepsNoSyntaxOfADependencyCheckedForItsDeclarations(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, filepath.Join("sigdeps", "app"))

	for _, path := range []string{"example.com/sigdep/plain", "strings"} {
		p := reachable(got, path)
		if p == nil {
			t.Errorf("Load(testdata/sigdeps/app) reaches no %s", path)
			continue
		}
		if p.Syntax != nil || p.TypesInfo != nil {
			t.Errorf("Load(testdata/sigdeps/app) %s: Syntax = %d files, TypesInfo = %v, want neither kept",
				path, len(p.Syntax), p.TypesInfo != nil)
		}
		if p.Types == nil || !p.Types.Complete() {
			t.Errorf("Load(testdata/sigdeps/app) %s: Types = %v, want complete types", path, p.Types)
		}
	}
}

func TestLoadChecksWholeEveryPackageThatImportsTheMainModule(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, filepath.Join("sigdeps", "app"))

	// back is a dependency, and it imports a package of the module being loaded.
	for _, path := range []string{"example.com/sigdeps", "example.com/sigdeps/core", "example.com/sigdep/back"} {
		p := reachable(got, path)
		if p == nil {
			t.Errorf("Load(testdata/sigdeps/app) reaches no %s", path)
			continue
		}
		if len(p.Syntax) == 0 || p.TypesInfo == nil {
			t.Errorf("Load(testdata/sigdeps/app) %s: Syntax = %d files, TypesInfo = %v, want both kept",
				path, len(p.Syntax), p.TypesInfo != nil)
		}
	}
}

func TestLoadKeepsTheColumnOfADependencyDeclaration(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, filepath.Join("sigdeps", "app"))

	p := reachable(got, "example.com/sigdep/plain")
	if p == nil {
		t.Fatal("Load(testdata/sigdeps/app) reaches no example.com/sigdep/plain")
	}
	height := p.Types.Scope().Lookup("Height")
	if height == nil {
		t.Fatal("example.com/sigdep/plain declares no Height in its types")
	}
	pos := got.Fset.Position(height.Pos())
	if filepath.Base(pos.Filename) != "plain.go" || pos.Line != 5 || pos.Column != 12 {
		t.Errorf("Height renders at %s through the result's file set, want plain.go:5:12", pos)
	}
}

func TestLoadAcceptsADependencyWhoseOnlyErrorIsInsideAFunctionBody(t *testing.T) {
	stableToolchain(t)
	got, err := loadScope(t.Context(), fixtureScope(t, filepath.Join("sigdeps", "app")), HostConfiguration())
	if err != nil {
		t.Fatalf("Load(testdata/sigdeps/app) = _, %v, want no error: plain.Broken's error is inside its body", err)
	}
	if p := reachable(got, "example.com/sigdep/plain"); p == nil || len(p.Errors) != 0 {
		t.Errorf("Load(testdata/sigdeps/app) example.com/sigdep/plain = %v, want it loaded with no error", p)
	}
}

func TestLoadRefusesAConfigurationOnItsMetadataBeforeCheckingAnything(t *testing.T) {
	stableToolchain(t)
	c := HostConfiguration()

	got, err := loadScope(t.Context(), fixtureScope(t, "metadataerror"), c)

	var loadErr *Error
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load(testdata/metadataerror) = _, %v, want a *load.Error", err)
	}
	if got.Packages != nil {
		t.Errorf("Load(testdata/metadataerror) carries %d packages, want none", len(got.Packages))
	}
	// typed.go does not type-check either, and its diagnostic appears only when
	// the load goes on to type-check after the toolchain reported an error.
	names := false
	for _, d := range loadErr.Diagnostics {
		if strings.Contains(d.Position, "typed.go") {
			t.Errorf("Load(testdata/metadataerror) reported %+v, want no type-check diagnostic", d)
		}
		names = names || strings.Contains(d.Message, "example.com/nowhere")
	}
	if !names {
		t.Errorf("Load(testdata/metadataerror) diagnostics = %+v, want one naming example.com/nowhere", loadErr.Diagnostics)
	}
}

// A package only a file importing "C" imports is checked for its declarations alone,
// as every other dependency is, so an error inside one of its bodies refuses
// nothing and the file that imports it is read.
func TestLoadChecksAPackageOnlyAFileImportingCImportsForItsDeclarations(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, filepath.Join("sigdeps", "app"))

	if len(got.ExcludedByCgo) != 0 {
		t.Errorf("Load(testdata/sigdeps/app).ExcludedByCgo = %v, want empty: the file importing C reads a dependency whose only error is in a body",
			got.ExcludedByCgo)
	}
	pkg := findPackage(got, "example.com/sigdeps")
	if pkg == nil || pkg.Imports["example.com/sigdep/viac"] == nil {
		t.Fatalf("Load(testdata/sigdeps/app) holds no import of example.com/sigdep/viac, ids = %v", packageIDs(got))
	}
	if viac := pkg.Imports["example.com/sigdep/viac"]; viac.TypesInfo != nil || len(viac.Errors) != 0 {
		t.Errorf("example.com/sigdep/viac carries type information %t and errors %v, want neither", viac.TypesInfo != nil, viac.Errors)
	}
}

// declarationSites is every identifier of f outside a function declaration's body,
// with its position, which is what a check of the declarations alone reads.
func declarationSites(fset *token.FileSet, f *ast.File) []string {
	var sites []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			ast.Inspect(&ast.FuncDecl{Doc: n.Doc, Recv: n.Recv, Name: n.Name, Type: n.Type}, func(m ast.Node) bool {
				if id, ok := m.(*ast.Ident); ok {
					sites = append(sites, fset.Position(id.Pos()).String()+" "+id.Name)
				}
				return true
			})
			if n.Body != nil {
				sites = append(sites, fset.Position(n.Body.Lbrace).String()+" {", fset.Position(n.Body.Rbrace).String()+" }")
			}
			return false
		case *ast.Ident:
			sites = append(sites, fset.Position(n.Pos()).String()+" "+n.Name)
		}
		return true
	})
	return sites
}

// sameDeclarations fails t unless src and its body-blanked form parse without error to
// the same declarations at the same positions.
func sameDeclarations(t *testing.T, name string, src []byte) (blanked bool) {
	t.Helper()
	without := withoutBodies(src)
	fset := token.NewFileSet()
	whole, err := parser.ParseFile(fset, name, src, signatureParse)
	if err != nil {
		return false
	}
	reduced, err := parser.ParseFile(fset, name, without, signatureParse)
	if err != nil {
		t.Fatalf("parse %s without its bodies = %v, want no error\n%s", name, err, without)
	}
	if got, want := declarationSites(fset, reduced), declarationSites(fset, whole); !slices.Equal(got, want) {
		t.Fatalf("%s without its bodies declares %v, want %v", name, got, want)
	}
	return !bytes.Equal(without, src)
}

func TestWithoutBodiesBlanksEveryFunctionBodyAndMovesNoDeclaration(t *testing.T) {
	t.Parallel()
	const src = "package p\n\nimport \"fmt\"\n\n" +
		"func f() struct{ a int } { return struct{ a int }{fmt.Sprint(1)} }\n\n" +
		"func (r *T) m(x interface{ M() }) (struct{}, error) {\n\tif x != nil {\n\t\tx.M()\n\t}\n\treturn struct{}{}, nil\n}\n\n" +
		"func g[V interface{ ~int }](v V) map[string]struct{} { s := `a\nb {`; _ = s; return nil }\n\n" +
		"func external(n int) int\n\n" +
		"var h = func() { println(\"kept\") }\n\n" +
		"type T struct{ f func() }\n\n" +
		"func after() {}\n"
	if !sameDeclarations(t, "p.go", []byte(src)) {
		t.Fatal("withoutBodies(a file declaring functions with bodies) returned it whole")
	}
	got := string(withoutBodies([]byte(src)))
	for _, gone := range []string{"fmt.Sprint", "x.M()", "return nil", "s := "} {
		if strings.Contains(got, gone) {
			t.Errorf("withoutBodies kept %q of a function body:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"func external(n int) int\n", `println("kept")`, "struct{ f func() }", "map[string]struct{} {"} {
		if !strings.Contains(got, kept) {
			t.Errorf("withoutBodies dropped %q, which no function declaration's body holds:\n%s", kept, got)
		}
	}
	invalid := withoutBodies([]byte("package p\n\nfunc f() { _ = '\\q' }\n"))
	if _, err := parser.ParseFile(token.NewFileSet(), "p.go", invalid, signatureParse); err != nil {
		t.Errorf("parse a body holding an invalid escape without it = %v, want no error: a check of the declarations reads no body", err)
	}
	for _, whole := range []string{
		"package p\n\nfunc f() {\n//line other.go:10\n\tprintln()\n}\n",
		"package p\n\nfunc f() { /*line other.go:10:1*/ println() }\n",
		"package p\n\nfunc f() {\n",
		"package p\n\nfunc f() { s := \"open\n}\n",
		"package p\n\nfunc f() { /* open }\n",
	} {
		if got := withoutBodies([]byte(whole)); string(got) != whole {
			t.Errorf("withoutBodies(%q) = %q, want it whole", whole, got)
		}
	}
}

func TestWithoutBodiesKeepsTheDeclarationsOfTheStandardLibrary(t *testing.T) {
	t.Parallel()
	blanked := 0
	for _, dir := range []string{"fmt", "reflect", "unicode", "testing", "encoding/json", "sync/atomic"} {
		names, err := filepath.Glob(filepath.Join(build.Default.GOROOT, "src", dir, "*.go"))
		if err != nil || len(names) == 0 {
			t.Fatalf("Setup: list GOROOT/src/%s = %d files, %v", dir, len(names), err)
		}
		for _, name := range names {
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			if sameDeclarations(t, name, src) {
				blanked++
			}
		}
	}
	if blanked == 0 {
		t.Fatal("withoutBodies blanked no body of the standard library, so the comparison pins nothing")
	}
}
