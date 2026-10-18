package graph

import (
	"os"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// detection is one archive's enumeration and root set, and the files its load
// excluded for importing C.
type detection struct {
	excludedByCgo []string
	symbols       []inventory.Symbol
	roots         []roots.Root
	unmatched     []roots.Unmatched
}

// detect is the enumeration and the roots of one archive loaded for one
// configuration.
func detect(t *testing.T, archive string, opts roots.RootOptions) detection {
	t.Helper()

	shared := analysisOf(t, archive, opts.Patterns)
	roots, unmatched := shared.rootsUnder(t, opts.PublishedAPI)
	return detection{
		excludedByCgo: shared.excludedFiles(t),
		symbols:       shared.inventory(t),
		roots:         roots,
		unmatched:     unmatched,
	}
}

// kindsByRef maps each symbol reference to the kinds of root the detection gave a
// symbol carrying it. A blank declaration carries its container's reference rather
// than one of its own, so it is left out and tested by position.
func kindsByRef(d detection) map[string][]roots.RootKind {
	refs := make(map[inventory.SymbolID]string, len(d.symbols))
	for _, s := range d.symbols {
		if !s.Blank {
			refs[s.ID] = s.Ref
		}
	}
	kinds := make(map[string][]roots.RootKind)
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
func refsOfKind(d detection, kind roots.RootKind) []string {
	byID := make(map[inventory.SymbolID]inventory.Symbol, len(d.symbols))
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
	d := detect(t, "roots.txtar", roots.RootOptions{PublishedAPI: true})
	kinds := kindsByRef(d)

	const pkg = "go://example.com/roots#"
	cases := []struct {
		ref  string
		want []roots.RootKind
	}{
		{ref: "go://example.com/roots/cmd/app#main", want: []roots.RootKind{roots.RootMain}},
		{ref: "go://example.com/roots/notmain#main"},
		{ref: pkg + "init", want: []roots.RootKind{roots.RootInit}},

		{ref: pkg + "TestMain", want: []roots.RootKind{roots.RootTest}},
		{ref: pkg + "TestPublished", want: []roots.RootKind{roots.RootTest}},
		{ref: pkg + "BenchmarkPublished", want: []roots.RootKind{roots.RootTest}},
		{ref: pkg + "FuzzPublished", want: []roots.RootKind{roots.RootTest}},
		{ref: pkg + "ExampleWithOutput", want: []roots.RootKind{roots.RootTest}},
		// The toolchain compiles an example into the test binary whether or not
		// the example declares output.
		{ref: pkg + "ExampleWithoutOutput", want: []roots.RootKind{roots.RootTest}},
		{ref: "go://example.com/roots_test#TestExternal", want: []roots.RootKind{roots.RootTest}},
		{ref: pkg + "ExampleTakesAnArgument"},
		{ref: pkg + "TestingLower"},
		{ref: pkg + "HelperExported"},
		{ref: pkg + "TestOutsideATestFile", want: []roots.RootKind{roots.RootPublishedAPI}},

		{ref: pkg + "pushed", want: []roots.RootKind{roots.RootLinkname}},
		{ref: pkg + "aliased", want: []roots.RootKind{roots.RootLinkname}},
		{ref: pkg + "counted", want: []roots.RootKind{roots.RootLinkname}},
		{ref: pkg + "pulled", want: []roots.RootKind{roots.RootLinkname}},
		{ref: pkg + "typed", want: []roots.RootKind{roots.RootLinkname}},
		{ref: "go://example.com/roots/internal/hidden#linked", want: []roots.RootKind{roots.RootLinkname}},
		{ref: "go://example.com/roots/internal/hidden#unlinked"},
		{ref: "go://example.com/roots/internal/hidden#Kind"},
		{ref: pkg + "notLinked"},
		{ref: pkg + "unsafeless"},

		{ref: pkg + "Published", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Catalog", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Catalog.Name", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Catalog.Resolve", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Fetcher", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Fetcher.Fetch", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Best", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Sentinel", want: []roots.RootKind{roots.RootPublishedAPI}},
		{ref: pkg + "Named", want: []roots.RootKind{roots.RootPublishedAPI}},
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
	d := detect(t, "roots.txtar", roots.RootOptions{PublishedAPI: true})

	var blank inventory.Symbol
	for _, s := range d.symbols {
		if s.Blank {
			blank = s
		}
	}
	if blank.ID == "" {
		t.Fatalf("Symbols(roots.txtar) holds no blank declaration")
	}

	var kinds []roots.RootKind
	for _, r := range d.roots {
		if r.ID == blank.ID {
			kinds = append(kinds, r.Kind)
		}
	}
	if want := []roots.RootKind{roots.RootBlank}; !slices.Equal(kinds, want) {
		t.Errorf("Roots(roots.txtar)[%s at %s] = %v, want %v", blank.Name, blank.ID, kinds, want)
	}
	if refs := refsOfKind(d, roots.RootBlank); len(refs) != 1 {
		t.Errorf("Roots(roots.txtar) blank roots name %v, want the one blank declaration", refs)
	}
}

func TestRootsRootsTheMembersOfAValueAPositionOfNoResolvedTypeReceives(t *testing.T) {
	d := detect(t, "type-error-destinations.txtar", roots.RootOptions{})

	const pkg = "go://example.com/app#"
	got := refsOfKind(d, roots.RootTypeError)
	for _, want := range []string{"returned.Write", "passed.Close", "stored.Len", "built.Step", "assigned.Shift", "handed.Send", "made.Build"} {
		if !slices.Contains(got, pkg+want) {
			t.Errorf("Roots(type-error-destinations.txtar) type-error roots %v, want %s: a skipped function hands its value to a type the compiler could not resolve",
				got, want)
		}
	}
	if slices.Contains(got, pkg+"untouched.Reset") {
		t.Errorf("Roots(type-error-destinations.txtar) type-error roots %v, want no untouched.Reset: no skipped function hands the type anywhere", got)
	}
	if slices.Contains(got, pkg+"resolved.Hold") {
		t.Errorf("Roots(type-error-destinations.txtar) type-error roots %v, want no resolved.Hold: its position in the assignment has a resolved type", got)
	}
}

func TestRootsReachesTheCgoExportTheOpaqueCheckRead(t *testing.T) {
	d := detect(t, "roots.txtar", roots.RootOptions{PublishedAPI: true})

	// The file importing "C" is read by the opaque-C check, so nothing about it is
	// a declared limit of the run.
	if len(d.excludedByCgo) != 0 {
		t.Errorf("Load(roots.txtar).ExcludedByCgo = %v, want empty", d.excludedByCgo)
	}
	const bridge = "go://example.com/roots/cgoexport#Bridge"
	kinds := kindsByRef(d)
	if _, held := kinds[bridge]; !held {
		t.Fatalf("Symbols(roots.txtar) holds no %s, want the declaration of the file importing \"C\"", bridge)
	}
	if want := []roots.RootKind{roots.RootCgoExport, roots.RootPublishedAPI}; !slices.Equal(kinds[bridge], want) {
		t.Errorf("Roots(roots.txtar)[%s] = %v, want %v", bridge, kinds[bridge], want)
	}
	if refs := refsOfKind(d, roots.RootCgoExport); !slices.Equal(refs, []string{bridge}) {
		t.Errorf("Roots(roots.txtar) cgo-export roots name %v, want %v", refs, []string{bridge})
	}
	// The sibling directive carries the same name and no import of "C", which is
	// what the class requires.
	const plain = "go://example.com/roots/cgoexport#Plain"
	if want := []roots.RootKind{roots.RootPublishedAPI}; !slices.Equal(kinds[plain], want) {
		t.Errorf("Roots(roots.txtar)[%s] = %v, want %v", plain, kinds[plain], want)
	}
}

func TestRootsPublishedAPIIsTheOnlyDifferenceTheTargetKindMakes(t *testing.T) {
	library := detect(t, "roots.txtar", roots.RootOptions{PublishedAPI: true})
	application := detect(t, "roots.txtar", roots.RootOptions{})

	if refs := refsOfKind(library, roots.RootPublishedAPI); len(refs) == 0 {
		t.Fatalf("Roots(roots.txtar) with PublishedAPI named nothing as published")
	}
	var want []roots.Root
	for _, r := range library.roots {
		if r.Kind != roots.RootPublishedAPI {
			want = append(want, r)
		}
	}
	if !slices.Equal(application.roots, want) {
		t.Errorf("Roots(roots.txtar) without PublishedAPI returned %d roots, want the %d roots of a library run that are not published-api\n--- want\n%v\n+++ got\n%v",
			len(application.roots), len(want), want, application.roots)
	}
	if refs := refsOfKind(application, roots.RootPublishedAPI); len(refs) != 0 {
		t.Errorf("Roots(roots.txtar) without PublishedAPI named %v as published, want none", refs)
	}
}

func TestRootsNamesOneSymbolOncePerReason(t *testing.T) {
	const published = "go://example.com/roots#Published"
	d := detect(t, "roots.txtar", roots.RootOptions{PublishedAPI: true, Patterns: []string{published, "go://example.com/roots#Publish*"}})

	kinds := kindsByRef(d)
	want := []roots.RootKind{roots.RootPublishedAPI, roots.RootConfigured, roots.RootPattern}
	slices.Sort(want)
	if got := kinds[published]; !slices.Equal(got, want) {
		t.Errorf("Roots(roots.txtar)[%s] = %v, want %v", published, got, want)
	}

	var sources []string
	for _, r := range d.roots {
		if r.Kind == roots.RootConfigured || r.Kind == roots.RootPattern {
			sources = append(sources, r.Source)
		}
	}
	if want := []string{published, "go://example.com/roots#Publish*"}; !slices.Equal(sources, want) {
		t.Errorf("Roots(roots.txtar) configured roots name sources %v, want %v", sources, want)
	}
	for _, r := range d.roots {
		if r.Kind != roots.RootConfigured && r.Kind != roots.RootPattern && r.Source != "" {
			t.Errorf("Roots(roots.txtar) root %+v carries a source, want one only on a configured root", r)
		}
	}
}

func TestRootsMatchesAConfiguredStringAgainstASymbolReference(t *testing.T) {
	const pkg = "go://example.com/patterns#"

	cases := map[string]struct {
		pattern string
		kind    roots.RootKind
		want    []string
	}{
		"an exact reference": {
			pattern: pkg + "Alpha",
			kind:    roots.RootConfigured,
			want:    []string{pkg + "Alpha"},
		},
		"a star over the members of one type": {
			pattern: pkg + "Box.*",
			kind:    roots.RootPattern,
			want:    []string{pkg + "Box.Lid", pkg + "Box.Open"},
		},
		"a star crossing the solidus": {
			pattern: "go://example.com/*#Gamma",
			kind:    roots.RootPattern,
			want:    []string{"go://example.com/patterns/inner#Gamma"},
		},
		"a question mark standing for one character": {
			pattern: pkg + "Bo?",
			kind:    roots.RootPattern,
			want:    []string{pkg + "Box"},
		},
		"a star over one package": {
			pattern: pkg + "Alph*",
			kind:    roots.RootPattern,
			want:    []string{pkg + "Alpha"},
		},
		// A blank declaration carries this reference too, and no configured
		// string names a blank declaration.
		"the package, whose reference a blank declaration borrows": {
			pattern: pkg,
			kind:    roots.RootConfigured,
			want:    []string{pkg},
		},
		"an exact reference naming nothing": {
			pattern: pkg + "Absent",
			kind:    roots.RootConfigured,
		},
		"a pattern naming nothing": {
			pattern: pkg + "Zeta*",
			kind:    roots.RootPattern,
		},
		"a character class, which is not syntax": {
			pattern: pkg + "[AB]lpha",
			kind:    roots.RootConfigured,
		},
		"a full stop, which is not syntax": {
			pattern: pkg + "Alph.",
			kind:    roots.RootConfigured,
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			d := detect(t, "root-patterns.txtar", roots.RootOptions{Patterns: []string{test.pattern}})

			if got := refsOfKind(d, test.kind); !slices.Equal(got, test.want) {
				t.Errorf("Roots(root-patterns.txtar, %q) named %v, want %v", test.pattern, got, test.want)
			}
			var wantUnmatched []roots.Unmatched
			if len(test.want) == 0 {
				wantUnmatched = []roots.Unmatched{{Source: test.pattern}}
			}
			if !slices.Equal(d.unmatched, wantUnmatched) {
				t.Errorf("Roots(root-patterns.txtar, %q) unmatched = %v, want %v", test.pattern, d.unmatched, wantUnmatched)
			}
			// The fixture's command and blank declaration are the roots that are
			// not configured, so any other kind is a string matched as the wrong one.
			for _, r := range d.roots {
				if r.Kind != test.kind && r.Kind != roots.RootBlank && r.Kind != roots.RootMain {
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
	d := detect(t, "root-patterns.txtar", roots.RootOptions{Patterns: patterns})

	want := []roots.Unmatched{
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
	symbols, rs, err := inventoryOf(result, target, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(roots.txtar): %v", err)
	}
	opts := roots.RootOptions{PublishedAPI: true, Patterns: []string{"go://example.com/roots#Publish*"}}

	first, _ := roots.Roots(result, rs, symbols, opts)
	second, _ := roots.Roots(result, rs, symbols, opts)
	if !slices.Equal(first, second) {
		t.Errorf("Roots returned a different order on the second call\n--- first\n%v\n+++ second\n%v", first, second)
	}
	if unique := slices.Compact(slices.Clone(first)); len(unique) != len(first) {
		t.Errorf("Roots returned %d roots of which %d are distinct, want each root once", len(first), len(unique))
	}
}

func TestRootsOrdersBySiteRatherThanBySymbolIdentifier(t *testing.T) {
	d := detect(t, "roots.txtar", roots.RootOptions{PublishedAPI: true})

	sites := make(map[inventory.SymbolID]inventory.Symbol, len(d.symbols))
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

func TestRootKindStringNamesEveryKind(t *testing.T) {
	cases := []struct {
		kind roots.RootKind
		want string
	}{
		{roots.RootMain, "main"},
		{roots.RootInit, "init"},
		{roots.RootTest, "test"},
		{roots.RootLinkname, "linkname"},
		{roots.RootCgoExport, "cgo-export"},
		{roots.RootBlank, "blank"},
		{roots.RootPublishedAPI, "published-api"},
		{roots.RootConfigured, "configured"},
		{roots.RootPattern, "pattern"},
		{roots.RootProgram, "program"},
		{roots.RootSwagRoute, "swag-route"},
		{roots.RootKind(200), "RootKind(200)"},
	}
	for _, test := range cases {
		t.Run(test.want, func(t *testing.T) {
			if got := test.kind.String(); got != test.want {
				t.Errorf("RootKind(%d).String() = %q, want %q", test.kind, got, test.want)
			}
		})
	}
}

func TestRootsRootsWhatAProgramUnderTheIgnoreTagNames(t *testing.T) {
	d := analysisOf(t, "programs.txtar", nil)
	rooted, _ := d.rootsUnder(t, false)
	names := make(map[inventory.SymbolID]string)
	for _, s := range d.inventory(t) {
		names[s.ID] = s.Name
	}
	var programs []string
	for _, r := range rooted {
		if r.Kind == roots.RootProgram {
			programs = append(programs, names[r.ID])
		}
	}

	// Generate is what the generator program calls. Ignored is called by an
	// ignored file of a library package, which go run cannot run, and Tooled by a
	// program behind another tag, so neither is a program's.
	if want := []string{"Generate"}; !slices.Equal(programs, want) {
		t.Errorf("Roots(programs.txtar) rooted %v as a program's, want %v", programs, want)
	}
}
