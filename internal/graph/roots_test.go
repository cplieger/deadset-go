package graph

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// detection is one archive's load, enumeration and root set.
type detection struct {
	result    *load.Result
	symbols   []Symbol
	roots     []Root
	unmatched []Unmatched
}

// detect extracts one archive, loads it for one configuration, enumerates it and
// detects its roots.
func detect(t *testing.T, archive string, opts RootOptions) detection {
	t.Helper()

	dir := extract(t, archive)
	result, target := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, target, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(%s): %v", archive, err)
	}
	roots, unmatched, err := Roots(result, target, os.ReadFile, symbols, opts)
	if err != nil {
		t.Fatalf("Roots(%s) = _, _, %v, want no error", archive, err)
	}
	return detection{result: result, symbols: symbols, roots: roots, unmatched: unmatched}
}

// kindsByRef maps each symbol reference to the kinds of root the detection gave a
// symbol carrying it. A blank declaration carries its container's reference rather
// than one of its own, so it is left out and tested by position.
func kindsByRef(d detection) map[string][]RootKind {
	refs := make(map[SymbolID]string, len(d.symbols))
	for _, s := range d.symbols {
		if !s.Blank {
			refs[s.ID] = s.Ref
		}
	}
	kinds := make(map[string][]RootKind)
	for _, r := range d.roots {
		ref, held := refs[r.ID]
		if !held || slices.Contains(kinds[ref], r.Kind) {
			continue
		}
		kinds[ref] = append(kinds[ref], r.Kind)
	}
	for ref := range kinds {
		slices.Sort(kinds[ref])
	}
	return kinds
}

// refsOfKind lists, in the order Roots returned them, the references of the
// symbols one kind of root names.
func refsOfKind(d detection, kind RootKind) []string {
	byID := make(map[SymbolID]Symbol, len(d.symbols))
	for _, s := range d.symbols {
		byID[s.ID] = s
	}
	var refs []string
	for _, r := range d.roots {
		if r.Kind == kind {
			refs = append(refs, byID[r.ID].Ref)
		}
	}
	return refs
}

func TestRootsDetectsEachClassAndDeclinesItsLookAlike(t *testing.T) {
	d := detect(t, "roots.txtar", RootOptions{PublishedAPI: true})
	kinds := kindsByRef(d)

	const pkg = "go://example.com/roots#"
	cases := []struct {
		ref  string
		want []RootKind
	}{
		{ref: "go://example.com/roots/cmd/app#main", want: []RootKind{RootMain}},
		{ref: "go://example.com/roots/notmain#main"},
		{ref: pkg + "init", want: []RootKind{RootInit}},

		{ref: pkg + "TestMain", want: []RootKind{RootTest}},
		{ref: pkg + "TestPublished", want: []RootKind{RootTest}},
		{ref: pkg + "BenchmarkPublished", want: []RootKind{RootTest}},
		{ref: pkg + "FuzzPublished", want: []RootKind{RootTest}},
		{ref: pkg + "ExampleWithOutput", want: []RootKind{RootTest}},
		// The toolchain compiles an example into the test binary whether or not
		// the example declares output.
		{ref: pkg + "ExampleWithoutOutput", want: []RootKind{RootTest}},
		{ref: "go://example.com/roots_test#TestExternal", want: []RootKind{RootTest}},
		{ref: pkg + "ExampleTakesAnArgument"},
		{ref: pkg + "TestingLower"},
		{ref: pkg + "HelperExported"},
		{ref: pkg + "TestOutsideATestFile", want: []RootKind{RootPublishedAPI}},

		{ref: pkg + "pushed", want: []RootKind{RootLinkname}},
		{ref: pkg + "aliased", want: []RootKind{RootLinkname}},
		{ref: pkg + "counted", want: []RootKind{RootLinkname}},
		{ref: pkg + "notLinked"},
		{ref: pkg + "unsafeless"},

		{ref: pkg + "Published", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Catalog", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Catalog.Name", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Catalog.Resolve", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Fetcher", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Fetcher.Fetch", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Best", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Sentinel", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "Named", want: []RootKind{RootPublishedAPI}},
		{ref: pkg + "unpublished"},
		{ref: pkg + "Catalog.size"},
		{ref: pkg + "Catalog.fill"},
		{ref: pkg + "catalog.Resolve"},
		{ref: pkg + "Fetcher.fetch"},
		{ref: pkg + "Best[T]"},
		{ref: "go://example.com/roots/cmd/app#Exported"},
		{ref: "go://example.com/roots/internal/hidden#Exported"},
		{ref: "go://example.com/roots_test#ExportedFromAnExternalTestPackage"},
	}

	held := make(map[string]bool, len(d.symbols))
	for _, s := range d.symbols {
		held[s.Ref] = true
	}
	for _, test := range cases {
		t.Run(test.ref, func(t *testing.T) {
			if !held[test.ref] {
				t.Fatalf("Symbols(roots.txtar) holds no symbol whose reference is %s", test.ref)
			}
			if got := kinds[test.ref]; !slices.Equal(got, test.want) {
				t.Errorf("Roots(roots.txtar)[%s] = %v, want %v", test.ref, got, test.want)
			}
		})
	}
}

func TestRootsRootsABlankDeclarationAndNotTheNamedOneBesideIt(t *testing.T) {
	d := detect(t, "roots.txtar", RootOptions{PublishedAPI: true})

	var blank Symbol
	for _, s := range d.symbols {
		if s.Blank {
			blank = s
		}
	}
	if blank.ID == "" {
		t.Fatalf("Symbols(roots.txtar) holds no blank declaration")
	}

	var kinds []RootKind
	for _, r := range d.roots {
		if r.ID == blank.ID {
			kinds = append(kinds, r.Kind)
		}
	}
	if want := []RootKind{RootBlank}; !slices.Equal(kinds, want) {
		t.Errorf("Roots(roots.txtar)[%s at %s] = %v, want %v", blank.Name, blank.ID, kinds, want)
	}
	if refs := refsOfKind(d, RootBlank); len(refs) != 1 {
		t.Errorf("Roots(roots.txtar) blank roots name %v, want the one blank declaration", refs)
	}
}

func TestRootsReachesNoCgoExportUnderTheLoadsCgoPolicy(t *testing.T) {
	d := detect(t, "roots.txtar", RootOptions{PublishedAPI: true})

	if want := []string{"cgoexport/bridge.go"}; !slices.Equal(d.result.ExcludedByCgo, want) {
		t.Errorf("Load(roots.txtar).ExcludedByCgo = %v, want %v", d.result.ExcludedByCgo, want)
	}
	for _, s := range d.symbols {
		if s.Ref == "go://example.com/roots/cgoexport#Bridge" {
			t.Errorf("Symbols(roots.txtar) holds %s, want it absent: the file importing \"C\" is not loaded", s.Ref)
		}
	}
	if refs := refsOfKind(d, RootCgoExport); len(refs) != 0 {
		t.Errorf("Roots(roots.txtar) cgo-export roots name %v, want none", refs)
	}
	// The sibling directive carries the same name and no import of "C", which is
	// what the class requires.
	kinds := kindsByRef(d)
	const plain = "go://example.com/roots/cgoexport#Plain"
	if want := []RootKind{RootPublishedAPI}; !slices.Equal(kinds[plain], want) {
		t.Errorf("Roots(roots.txtar)[%s] = %v, want %v", plain, kinds[plain], want)
	}
}

func TestRootsPublishedAPIIsTheOnlyDifferenceTheTargetKindMakes(t *testing.T) {
	library := detect(t, "roots.txtar", RootOptions{PublishedAPI: true})
	application := detect(t, "roots.txtar", RootOptions{})

	if refs := refsOfKind(library, RootPublishedAPI); len(refs) == 0 {
		t.Fatalf("Roots(roots.txtar) with PublishedAPI named nothing as published")
	}
	var want []Root
	for _, r := range library.roots {
		if r.Kind != RootPublishedAPI {
			want = append(want, r)
		}
	}
	if !slices.Equal(application.roots, want) {
		t.Errorf("Roots(roots.txtar) without PublishedAPI returned %d roots, want the %d roots of a library run that are not published-api\n--- want\n%v\n+++ got\n%v",
			len(application.roots), len(want), want, application.roots)
	}
	if refs := refsOfKind(application, RootPublishedAPI); len(refs) != 0 {
		t.Errorf("Roots(roots.txtar) without PublishedAPI named %v as published, want none", refs)
	}
}

func TestRootsNamesOneSymbolOncePerReason(t *testing.T) {
	const published = "go://example.com/roots#Published"
	d := detect(t, "roots.txtar", RootOptions{PublishedAPI: true, Patterns: []string{published, "go://example.com/roots#Publish*"}})

	kinds := kindsByRef(d)
	want := []RootKind{RootPublishedAPI, RootConfigured, RootPattern}
	slices.Sort(want)
	if got := kinds[published]; !slices.Equal(got, want) {
		t.Errorf("Roots(roots.txtar)[%s] = %v, want %v", published, got, want)
	}

	var sources []string
	for _, r := range d.roots {
		if r.Kind == RootConfigured || r.Kind == RootPattern {
			sources = append(sources, r.Source)
		}
	}
	if want := []string{published, "go://example.com/roots#Publish*"}; !slices.Equal(sources, want) {
		t.Errorf("Roots(roots.txtar) configured roots name sources %v, want %v", sources, want)
	}
	for _, r := range d.roots {
		if r.Kind != RootConfigured && r.Kind != RootPattern && r.Source != "" {
			t.Errorf("Roots(roots.txtar) root %+v carries a source, want one only on a configured root", r)
		}
	}
}

func TestRootsMatchesAConfiguredStringAgainstASymbolReference(t *testing.T) {
	const pkg = "go://example.com/patterns#"

	cases := map[string]struct {
		pattern string
		kind    RootKind
		want    []string
	}{
		"an exact reference": {
			pattern: pkg + "Alpha",
			kind:    RootConfigured,
			want:    []string{pkg + "Alpha"},
		},
		"a star over the members of one type": {
			pattern: pkg + "Box.*",
			kind:    RootPattern,
			want:    []string{pkg + "Box.Lid", pkg + "Box.Open"},
		},
		"a star crossing the solidus": {
			pattern: "go://example.com/*#Gamma",
			kind:    RootPattern,
			want:    []string{"go://example.com/patterns/inner#Gamma"},
		},
		"a question mark standing for one character": {
			pattern: pkg + "Bo?",
			kind:    RootPattern,
			want:    []string{pkg + "Box"},
		},
		"a star over one package": {
			pattern: pkg + "Alph*",
			kind:    RootPattern,
			want:    []string{pkg + "Alpha"},
		},
		// A blank declaration carries this reference too, and no configured
		// string names a blank declaration.
		"the package, whose reference a blank declaration borrows": {
			pattern: pkg,
			kind:    RootConfigured,
			want:    []string{pkg},
		},
		"an exact reference naming nothing": {
			pattern: pkg + "Absent",
			kind:    RootConfigured,
		},
		"a pattern naming nothing": {
			pattern: pkg + "Zeta*",
			kind:    RootPattern,
		},
		"a character class, which is not syntax": {
			pattern: pkg + "[AB]lpha",
			kind:    RootConfigured,
		},
		"a full stop, which is not syntax": {
			pattern: pkg + "Alph.",
			kind:    RootConfigured,
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			d := detect(t, "root-patterns.txtar", RootOptions{Patterns: []string{test.pattern}})

			if got := refsOfKind(d, test.kind); !slices.Equal(got, test.want) {
				t.Errorf("Roots(root-patterns.txtar, %q) named %v, want %v", test.pattern, got, test.want)
			}
			var wantUnmatched []Unmatched
			if len(test.want) == 0 {
				wantUnmatched = []Unmatched{{Source: test.pattern}}
			}
			if !slices.Equal(d.unmatched, wantUnmatched) {
				t.Errorf("Roots(root-patterns.txtar, %q) unmatched = %v, want %v", test.pattern, d.unmatched, wantUnmatched)
			}
			// The fixture's blank declaration is the one root that is not
			// configured, so any other kind is a string matched as the wrong one.
			for _, r := range d.roots {
				if r.Kind != test.kind && r.Kind != RootBlank {
					t.Errorf("Roots(root-patterns.txtar, %q) returned a root of kind %s, want %s", test.pattern, r.Kind, test.kind)
				}
			}
		})
	}
}

func TestRootsReportsEveryUnmatchedStringOnceInTheConfigurationsOrder(t *testing.T) {
	patterns := []string{
		"go://example.com/patterns#Zeta",
		"go://example.com/patterns#Alpha",
		"go://example.com/patterns#Absent*",
		"go://example.com/patterns#Zeta",
	}
	d := detect(t, "root-patterns.txtar", RootOptions{Patterns: patterns})

	want := []Unmatched{
		{Source: "go://example.com/patterns#Zeta"},
		{Source: "go://example.com/patterns#Absent*"},
	}
	if !slices.Equal(d.unmatched, want) {
		t.Errorf("Roots(root-patterns.txtar, %v) unmatched = %v, want %v", patterns, d.unmatched, want)
	}
}

func TestRootsOrderIsTheSameOnEveryCall(t *testing.T) {
	dir := extract(t, "roots.txtar")
	result, target := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, target, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(roots.txtar): %v", err)
	}
	opts := RootOptions{PublishedAPI: true, Patterns: []string{"go://example.com/roots#Publish*"}}

	first, _, err := Roots(result, target, os.ReadFile, symbols, opts)
	if err != nil {
		t.Fatalf("Roots first call error: %v", err)
	}
	second, _, err := Roots(result, target, os.ReadFile, symbols, opts)
	if err != nil {
		t.Fatalf("Roots second call error: %v", err)
	}
	if !slices.Equal(first, second) {
		t.Errorf("Roots returned a different order on the second call\n--- first\n%v\n+++ second\n%v", first, second)
	}
	order := rootOrder(symbols)
	for i := 1; i < len(first); i++ {
		if order(first[i-1], first[i]) >= 0 {
			t.Fatalf("Roots order is not strictly increasing at %d: %+v then %+v", i, first[i-1], first[i])
		}
	}
}

func TestRootsOrdersBySiteRatherThanBySymbolIdentifier(t *testing.T) {
	d := detect(t, "roots.txtar", RootOptions{PublishedAPI: true})

	sites := make(map[SymbolID]Symbol, len(d.symbols))
	for _, s := range d.symbols {
		sites[s.ID] = s
	}

	// A symbol identifier spells its line in decimal, so the identifier order and
	// the file order disagree wherever one root's line shares a leading digit
	// with a shorter line of the same file. The fixture holds such a pair, and
	// without it this test would pass under either order.
	inverted := false
	for i := 1; i < len(d.roots); i++ {
		before, after := sites[d.roots[i-1].ID], sites[d.roots[i].ID]
		if before.Pos.Filename == after.Pos.Filename && before.Pos.Line < after.Pos.Line &&
			string(after.ID) < string(before.ID) {
			inverted = true
		}
		if before.Pos.Filename > after.Pos.Filename ||
			(before.Pos.Filename == after.Pos.Filename && before.Pos.Line > after.Pos.Line) {
			t.Errorf("Roots(roots.txtar) returned %s at %s:%d before %s at %s:%d, want the order the sites read in",
				before.Name, before.Pos.Filename, before.Pos.Line,
				after.Name, after.Pos.Filename, after.Pos.Line)
		}
	}
	if !inverted {
		t.Fatal("roots.txtar holds no two roots of one file whose identifier order differs from their line order, so this test cannot fail")
	}
}

func TestRootsRefusesAnIncompleteRequest(t *testing.T) {
	dir := extract(t, "roots.txtar")
	result, target := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, target, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(roots.txtar): %v", err)
	}

	cases := map[string]struct {
		result  *load.Result
		read    ReadFile
		wantErr error
	}{
		"no result":   {result: nil, read: os.ReadFile, wantErr: ErrIncompleteLoad},
		"no file set": {result: &load.Result{Packages: result.Packages}, read: os.ReadFile, wantErr: ErrIncompleteLoad},
		"no reader":   {result: result, read: nil, wantErr: ErrIncompleteLoad},
		"a reader that refuses": {
			result:  result,
			read:    func(string) ([]byte, error) { return nil, os.ErrPermission },
			wantErr: ErrSource,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			roots, unmatched, err := Roots(test.result, target, test.read, symbols, RootOptions{PublishedAPI: true})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Roots(%s) error = %v, want one satisfying errors.Is(err, %v)", name, err, test.wantErr)
			}
			if roots != nil || unmatched != nil {
				t.Errorf("Roots(%s) = %v, %v, want no root and no unmatched pattern", name, roots, unmatched)
			}
		})
	}
}

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

func TestLinknameLocalReadsTheDirectivesLocalName(t *testing.T) {
	cases := map[string]struct {
		text   string
		want   string
		wantOK bool
	}{
		"one argument":                {text: "//go:linkname pushed", want: "pushed", wantOK: true},
		"two arguments":               {text: "//go:linkname pulled example.com/other.f", want: "pulled", wantOK: true},
		"the standard library's form": {text: "//go:linknamestd pushed", want: "pushed", wantOK: true},
		"three arguments":             {text: "//go:linkname a b c"},
		"no argument":                 {text: "//go:linkname"},
		"another directive":           {text: "//go:embed catalog.json"},
		"a space before the name":     {text: "// go:linkname pushed"},
		"prose naming the directive":  {text: "// The //go:linkname directive names a symbol."},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := linknameLocal(test.text)
			if got != test.want || ok != test.wantOK {
				t.Errorf("linknameLocal(%q) = %q, %t, want %q, %t", test.text, got, ok, test.want, test.wantOK)
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
		mains: map[string]bool{target: true},
		found: make(map[Root]struct{}),
	}

	d.declared([]Symbol{
		{ID: "variable", Kind: KindVar, Name: mainFunction, PkgPath: target},
		{ID: "type", Kind: KindType, Name: initFunction, PkgPath: target},
		{ID: "entry point", Kind: KindFunc, Name: mainFunction, PkgPath: target},
		{ID: "library function", Kind: KindFunc, Name: mainFunction, PkgPath: "example.com/lib"},
		{ID: "initializer", Kind: KindFunc, Name: initFunction, PkgPath: "example.com/lib"},
	})

	want := map[Root]struct{}{
		{ID: "entry point", Kind: RootMain}: {},
		{ID: "initializer", Kind: RootInit}: {},
	}
	if !maps.Equal(d.found, want) {
		t.Errorf("declared() found %v, want %v", d.found, want)
	}
}

func TestRootKindStringNamesEveryKind(t *testing.T) {
	cases := []struct {
		kind RootKind
		want string
	}{
		{RootMain, "main"},
		{RootInit, "init"},
		{RootTest, "test"},
		{RootLinkname, "linkname"},
		{RootCgoExport, "cgo-export"},
		{RootBlank, "blank"},
		{RootPublishedAPI, "published-api"},
		{RootConfigured, "configured"},
		{RootPattern, "pattern"},
		{RootKind(200), "RootKind(200)"},
	}
	for _, test := range cases {
		t.Run(test.want, func(t *testing.T) {
			if got := test.kind.String(); got != test.want {
				t.Errorf("RootKind(%d).String() = %q, want %q", test.kind, got, test.want)
			}
		})
	}
}
