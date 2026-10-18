package roots

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	spec "github.com/cplieger/deadset-spec/v8"
	"golang.org/x/tools/go/packages"
)

func TestMatchRefAcceptsTwoSpecialCharactersAndNothingElse(t *testing.T) {
	const ref = "go://example.com/app/inner#Catalog.Name"

	cases := map[string]struct {
		pattern string
		want    bool
	}{
		"the reference itself":                 {pattern: ref, want: true},
		"a reference that differs by one rune": {pattern: "go://example.com/app/inner#Catalog.name"},
		"a star for the whole reference":       {pattern: "*", want: true},
		"a star for one member":                {pattern: "go://example.com/app/inner#Catalog.*", want: true},
		"a star crossing the solidus":          {pattern: "go://example.com/*#Catalog.Name", want: true},
		"a star standing for nothing":          {pattern: "go://example.com/app/inner#Catalog.Name*", want: true},
		"two stars":                            {pattern: "go://*app*#*Name", want: true},
		"a question mark for one rune":         {pattern: "go://example.com/app/inner#Catalog.Nam?", want: true},
		"a question mark for the solidus":      {pattern: "go://example.com?app/inner#Catalog.Name", want: true},
		"a question mark for two runes":        {pattern: "go://example.com/app/inner#Catalog.Nam?e"},
		"a character class":                    {pattern: "go://example.com/app/inner#[CD]atalog.Name"},
		"a full stop":                          {pattern: "go://example.com/app/inner#Catalog.Nam."},
		"a regular expression":                 {pattern: "^go://.*Name$"},
		"an empty pattern":                     {pattern: ""},
		"a prefix of the reference":            {pattern: "go://example.com"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := matchRef(test.pattern, ref); got != test.want {
				t.Errorf("matchRef(%q, %q) = %t, want %t", test.pattern, ref, got, test.want)
			}
		})
	}
}

func TestMatchRefOnRunesRatherThanBytes(t *testing.T) {
	const ref = "go://example.com/app#Café"

	cases := map[string]struct {
		pattern string
		want    bool
	}{
		"one question mark for one multi-byte rune":  {pattern: "go://example.com/app#Caf?", want: true},
		"two question marks for one multi-byte rune": {pattern: "go://example.com/app#Caf??"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := matchRef(test.pattern, ref); got != test.want {
				t.Errorf("matchRef(%q, %q) = %t, want %t", test.pattern, ref, got, test.want)
			}
		})
	}
}

// patternCase is one case of the published root-pattern corpus: a configured
// pattern, a symbol reference, and whether the one matches the other.
type patternCase struct {
	Pattern   string `json:"pattern"`
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
	Matches   bool   `json:"matches"`
}

func TestMatchRefAnswersEveryCaseOfThePublishedPatternCorpus(t *testing.T) {
	const path = "contract/grammar/pattern-corpus.json"
	body, err := spec.Contract.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", path, err)
	}
	var cases []patternCase
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cases); err != nil {
		t.Fatalf("Setup: decode %s: %v", path, err)
	}
	if len(cases) == 0 {
		t.Fatalf("Setup: %s holds no case", path)
	}

	for i, test := range cases {
		t.Run(fmt.Sprintf("case-%02d", i), func(t *testing.T) {
			if got := matchRef(test.Pattern, test.Reference); got != test.Matches {
				t.Errorf("matchRef(%q, %q) = %t, want %t: %s", test.Pattern, test.Reference, got, test.Matches, test.Reason)
			}
			if got := matchGlob([]rune(test.Pattern), []rune(test.Reference)); got != test.Matches {
				t.Errorf("matchGlob(%q, %q) = %t, want %t: %s", test.Pattern, test.Reference, got, test.Matches, test.Reason)
			}
		})
	}
}

func TestCgoExportedReadsTheDirectiveOverTheFunctionItNames(t *testing.T) {
	const source = `package bridge

import "C"

//export Bridge
func Bridge() {}

//export Other
func Renamed() {}

// Documented is documented and not exported.
func Documented() {}

func Bare() {}

func Inner() {
	//export Nested
	_ = 0
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "bridge.go", source, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse the fixture source: %v", err)
	}
	byName := make(map[string]*ast.FuncDecl)
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			byName[fn.Name.Name] = fn
		}
	}

	cases := map[string]bool{
		"Bridge":     true,
		"Renamed":    false,
		"Documented": false,
		"Bare":       false,
		"Inner":      false,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			fn, held := byName[name]
			if !held {
				t.Fatalf("the fixture source declares no function named %s", name)
			}
			if got := cgoExported(fn); got != want {
				t.Errorf("cgoExported(%s) = %t, want %t", name, got, want)
			}
		})
	}
}

// fakePackages answers an import with a package the test type-checked itself, so
// a fixture source can name package testing without a toolchain.
type fakePackages map[string]*types.Package

func (f fakePackages) Import(path string) (*types.Package, error) {
	if pkg, held := f[path]; held {
		return pkg, nil
	}
	return nil, fmt.Errorf("fake importer: no package %q", path)
}

// typeCheck type-checks one source file as the package at path and returns it
// with the identifiers it defines and its syntax.
func typeCheck(t *testing.T, path, source string, imports fakePackages) (*types.Package, *types.Info, *ast.File) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse the source of %s: %v", path, err)
	}
	info := &types.Info{Defs: make(map[*ast.Ident]types.Object)}
	pkg, err := (&types.Config{Importer: imports}).Check(path, fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("Setup: type-check %s: %v", path, err)
	}
	return pkg, info, file
}

// A tree the loader accepts holds none of these shapes: the generated test main
// assigns each function to a field of its family's type, so the load refuses the
// configuration with a type error before a root set exists. The rule is pinned
// here instead, over two packages the test type-checks itself.
func TestTestSignatureNamesTheFamilysTypeOfPackageTesting(t *testing.T) {
	testingPackage, _, _ := typeCheck(t, "testing", "package testing\n\ntype T struct{}\n\ntype B struct{}\n", nil)
	_, info, file := typeCheck(t, "example.com/fixture", `package fixture

import "testing"

type T struct{}

func TestFamily(b *testing.B) {}

func TestLocalType(t *T) {}

func TestTwoParameters(t *testing.T, size int) {}

func TestResult(t *testing.T) error { return nil }

func TestValue(t testing.T) {}
`, fakePackages{"testing": testingPackage})

	byName := make(map[string]*ast.FuncDecl)
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			byName[fn.Name.Name] = fn
		}
	}
	p := &packages.Package{TypesInfo: info}

	cases := []struct {
		function string
		param    string
		want     bool
	}{
		{function: "TestFamily", param: "B", want: true},
		{function: "TestFamily", param: "T"},
		{function: "TestLocalType", param: "T"},
		{function: "TestTwoParameters", param: "T"},
		{function: "TestResult", param: "T"},
		{function: "TestValue", param: "T"},
	}
	for _, test := range cases {
		t.Run(test.function+"_"+test.param, func(t *testing.T) {
			fn, held := byName[test.function]
			if !held {
				t.Fatalf("the fixture source declares no function named %s", test.function)
			}
			if got := testSignature(p, fn, test.param); got != test.want {
				t.Errorf("testSignature(%s, %s) = %t, want %t", test.function, test.param, got, test.want)
			}
		})
	}
}

func TestDeclaredRootsOnlyAFunctionNamedMainOrInit(t *testing.T) {
	const target = "example.com/app"
	d := &rootDetection{
		classes: map[string][]packageClass{target: {{main: true}}},
		found:   make(map[Root]struct{}),
	}

	d.declared([]inventory.Symbol{
		{ID: "variable", Kind: inventory.KindVar, Name: mainFunction, PkgPath: target},
		{ID: "type", Kind: inventory.KindType, Name: initFunction, PkgPath: target},
		{ID: "entry point", Kind: inventory.KindFunc, Name: mainFunction, PkgPath: target},
		{ID: "library function", Kind: inventory.KindFunc, Name: mainFunction, PkgPath: "example.com/lib"},
		{ID: "initializer", Kind: inventory.KindFunc, Name: initFunction, PkgPath: "example.com/lib"},
	})

	want := map[Root]struct{}{
		{ID: "entry point", Kind: RootMain}: {},
		{ID: "initializer", Kind: RootInit}: {},
	}
	if !maps.Equal(d.found, want) {
		t.Errorf("declared() found %v, want %v", d.found, want)
	}
}
