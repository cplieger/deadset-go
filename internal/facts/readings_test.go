package facts

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// checked type-checks files as one package of path.
func checked(t *testing.T, fset *token.FileSet, path string, files ...*ast.File) *types.Package {
	t.Helper()
	pkg, err := new(types.Config).Check(path, fset, files, nil)
	if err != nil {
		t.Fatalf("Setup: check %s: %v", path, err)
	}
	return pkg
}

// Two readings of one package's files are one package to a key, and a variant that
// compiles one more file, or a package of the same path in another directory, is
// another.
func TestReadingsNameTwoReadingsOfOnePackageAlike(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	parse := func(name, src string) *ast.File {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		return f
	}
	lib := parse("/m/lib/lib.go", "package lib\n\ntype Label struct{}\n")
	test := parse("/m/lib/lib_test.go", "package lib\n\nfunc helper() Label { return Label{} }\n")
	other := parse("/n/lib/lib.go", "package lib\n\ntype Label struct{}\n")

	first, second := checked(t, fset, "example.com/lib", lib), checked(t, fset, "example.com/lib", lib)
	variant := checked(t, fset, "example.com/lib", lib, test)
	elsewhere := checked(t, fset, "example.com/lib", other)
	r := newReadings(fset)
	label := func(p *types.Package) string { return r.identity(p.Scope().Lookup("Label").Type()) }

	if label(first) != label(second) {
		t.Errorf("identity(Label) differs between two readings of one package: %q and %q", label(first), label(second))
	}
	if label(first) == label(variant) {
		t.Errorf("identity(Label) = %q in the package and its test variant, want two keys: the variant compiles another file", label(first))
	}
	if label(first) == label(elsewhere) {
		t.Errorf("identity(Label) = %q for two packages of one path in two directories, want two keys", label(first))
	}
}

// An interface naming one reading's types, through composite types and a generic
// instance, is stated in another reading's, so a type of that reading implements it;
// an interface naming no type of the package comes back as itself.
func TestRestatedStatesAnInterfaceInAnotherReading(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	src := "package lib\n\ntype Label struct{}\n\ntype Box[T any] struct{ v T }\n\n" +
		"type Square struct{}\n\nfunc (Square) Name(map[string][]*Label) (func() Box[Label], chan Label) { return nil, nil }\n"
	lib, err := parser.ParseFile(fset, "/m/lib/lib.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse: %v", err)
	}
	mine, theirs := checked(t, fset, "example.com/lib", lib), checked(t, fset, "example.com/lib", lib)
	asserting := types.NewInterfaceType([]*types.Func{
		types.NewFunc(token.NoPos, nil, "Name", theirs.Scope().Lookup("Square").Type().(*types.Named).Method(0).Signature()),
	}, nil).Complete()
	square := mine.Scope().Lookup("Square").Type()
	if types.Implements(square, asserting) {
		t.Fatalf("Setup: Square implements the other reading's interface before restating")
	}
	into := func(p *types.Package) *types.Package {
		if p.Path() == mine.Path() {
			return mine
		}
		return p
	}
	stated, ok := Restated(asserting, into)
	if !ok {
		t.Fatalf("Restated(%v) = false, want the interface in mine's reading", asserting)
	}
	if iface, _ := stated.(*types.Interface); iface == nil || !types.Implements(square, iface) {
		t.Errorf("Restated(%v) = %v, which Square does not implement", asserting, stated)
	}
	plain := types.NewInterfaceType([]*types.Func{
		types.NewFunc(token.NoPos, nil, "Area", types.NewSignatureType(nil, nil, nil, nil,
			types.NewTuple(types.NewParam(token.NoPos, nil, "", types.Typ[types.Int])), false)),
	}, nil).Complete()
	if again, ok := Restated(plain, into); !ok || again != plain {
		t.Errorf("Restated(%v) = %v, %v, want the interface itself", plain, again, ok)
	}
}

// A type naming a type parameter or a type declared in a function body has no
// counterpart in another reading, through every constructor that holds one, so
// Restated answers false for it and never builds a type around the part it lacks.
func TestRestatedIsFalseForATypeNamingWhatNoOtherReadingDeclares(t *testing.T) {
	t.Parallel()
	shapes := []string{
		"interface{ M(v ...T) }",
		"interface{ M(n int, v ...T) }",
		"interface{ M(v ...L) }",
		"interface{ M(...int) T }",
		"interface{ M() (int, T) }",
		"func(v ...T)",
		"func(...int) T",
		"*T", "[]T", "[2]T", "chan T", "map[T]int", "map[int]T",
		"struct{ f T }",
		"Box[T]",
		"interface{ M(Box[T]) }",
	}
	src := "package lib\n\ntype Box[E any] struct{ v E }\n\nfunc (b Box[E]) Get(v ...E) E { return b.v }\n\nfunc F[T comparable]() {\n\ttype L struct{}\n"
	for i, shape := range shapes {
		src += fmt.Sprintf("\tvar v%d %s\n\t_ = v%d\n", i, shape, i)
	}
	src += "}\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "/m/lib/lib.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse: %v", err)
	}
	info := &types.Info{Types: make(map[ast.Expr]types.TypeAndValue)}
	mine, err := new(types.Config).Check("example.com/lib", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("Setup: check: %v", err)
	}
	theirs := checked(t, fset, "example.com/lib", file)
	into := func(p *types.Package) *types.Package {
		if p.Path() == mine.Path() {
			return theirs
		}
		return p
	}
	cases := map[string]types.Type{
		"the generic function itself":              mine.Scope().Lookup("F").Type(),
		"a method variadic in its receiver's type": mine.Scope().Lookup("Box").Type().(*types.Named).Method(0).Signature(),
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name.Name == "F" {
			body = fn.Body
		}
	}
	for _, stmt := range body.List {
		decl, isDecl := stmt.(*ast.DeclStmt)
		if !isDecl {
			continue
		}
		if spec, isValue := decl.Decl.(*ast.GenDecl).Specs[0].(*ast.ValueSpec); isValue {
			cases[types.ExprString(spec.Type)] = info.TypeOf(spec.Type)
		}
	}
	if len(cases) != len(shapes)+2 {
		t.Fatalf("Setup: %d cases, want %d", len(cases), len(shapes)+2)
	}
	for name, typ := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("Restated(%v) panicked: %v", typ, p)
				}
			}()
			if stated, ok := Restated(typ, into); ok {
				t.Errorf("Restated(%v) = %v, true, want false: the other reading declares no counterpart", typ, stated)
			}
		})
	}
}

// importing resolves each import path to the one package of that path.
type importing map[string]*types.Package

func (m importing) Import(path string) (*types.Package, error) {
	if p, held := m[path]; held {
		return p, nil
	}
	return nil, fmt.Errorf("no package %s", path)
}

// A type of m, built against one version of lib, meets an interface and a type of
// another version as a build holding both meets them: stated in m's reading of lib.
func TestReadingsStateTheOtherVersionsTypesInTheReadingTheTypeSees(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	parse := func(name, src string) *ast.File {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		return f
	}
	first := checked(t, fset, "example.com/lib", parse("/v1/lib/lib.go", "package lib\n\ntype Key struct{}\n"))
	second := checked(t, fset, "example.com/lib", parse("/v2/lib/lib.go",
		"package lib\n\ntype Key struct{}\n\ntype Getter interface{ Get(Key) int }\n"))
	m, err := (&types.Config{Importer: importing{"example.com/lib": first}}).Check("example.com/m", fset,
		[]*ast.File{parse("/m/m.go", "package m\n\nimport \"example.com/lib\"\n\ntype T struct{}\n\nfunc (T) Get(lib.Key) int { return 0 }\n")}, nil)
	if err != nil {
		t.Fatalf("Setup: check m: %v", err)
	}
	getter := second.Scope().Lookup("Getter").Type().Underlying().(*types.Interface)
	tType := m.Scope().Lookup("T").Type()
	if types.Implements(tType, getter) {
		t.Fatalf("Setup: T implements the second version's Getter before stating it in m's reading")
	}
	r := readingsOver(fset)

	if !r.Implements(tType, getter) {
		t.Errorf("Implements(T, Getter) = false, want true: Getter stated in m's reading of lib is T's")
	}
	if r.Implements(types.Typ[types.Int], getter) {
		t.Errorf("Implements(int, Getter) = true, want false")
	}
	if got := r.Of(m, second); got != first {
		t.Errorf("Of(m, the second lib) = %p, want m's reading %p", got, first)
	}
	firstKey, secondKey := first.Scope().Lookup("Key").Type(), second.Scope().Lookup("Key").Type()
	if !r.Same(firstKey, secondKey) || !r.Same(secondKey, firstKey) {
		t.Errorf("Same(Key, Key) of two versions = false, want true in either order")
	}
	if r.Same(firstKey, tType) {
		t.Errorf("Same(lib.Key, m.T) = true, want false")
	}
	sig := getter.Method(0).Signature()
	stated, ok := r.In(sig, m)
	if !ok || !types.Identical(stated, tType.(*types.Named).Method(0).Signature()) {
		t.Errorf("In(Getter.Get, m) = %v, %v, want T.Get's signature", stated, ok)
	}
}
