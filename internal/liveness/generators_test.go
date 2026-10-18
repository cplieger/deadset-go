package liveness

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/roots"
)

// generatorRoots is the reference of every symbol of generators.txtar one kind of
// root names under one configuration, and the reference of every symbol loaded.
func generatorRoots(t *testing.T, c load.Configuration, kind roots.RootKind) (named, held map[string]bool) {
	t.Helper()
	return archiveRoots(t, "generators.txtar", c, kind)
}

// archiveRoots is the references of the roots of one kind of an archive loaded under
// one configuration, and the references of every symbol it holds.
func archiveRoots(t *testing.T, archive string, c load.Configuration, kind roots.RootKind) (named, held map[string]bool) {
	t.Helper()
	result, root := loadDirUnder(t.Context(), t, extract(t, archive), c)
	symbols, rs, err := inventoryOf(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(%s) error: %v", archive, err)
	}
	roots, _ := roots.Roots(result, rs, symbols, roots.RootOptions{})
	d := detection{symbols: symbols, roots: roots}
	named = make(map[string]bool)
	for _, ref := range refsOfKind(d, kind) {
		named[ref] = true
	}
	held = make(map[string]bool, len(symbols))
	for _, s := range symbols {
		held[s.Ref] = true
	}
	return named, held
}

func TestRootsRootsTheInputsAGenerateDirectiveNames(t *testing.T) {
	named, held := generatorRoots(t, load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}, roots.RootGenerate)

	for ref, want := range map[string]bool{
		"go://example.com/gen/app#Color":      true,
		"go://example.com/gen/app#Shape":      true,
		"go://example.com/gen/app#Size":       true,
		"go://example.com/gen/app#Store":      true,
		"go://example.com/gen/app#Store.Get":  true,
		"go://example.com/gen/app#Cache":      true,
		"go://example.com/gen/app#Port":       true,
		"go://example.com/gen/other#Remote":   true,
		"go://example.com/gen/app#Kind":       true,
		"go://example.com/gen/app#Level":      true,
		"go://example.com/gen/app#Mode":       true,
		"go://example.com/gen/app#Spaced":     true,
		"go://example.com/gen/app#Unlisted":   false,
		"go://example.com/gen/app#Spare":      false,
		"go://example.com/gen/app#Spare.Drop": false,
		"go://example.com/gen/app#Adapter":    false,
		"go://example.com/gen/other#Local":    false,
	} {
		t.Run(ref, func(t *testing.T) {
			if !held[ref] {
				t.Fatalf("Symbols(generators.txtar) holds no symbol whose reference is %s", ref)
			}
			if named[ref] != want {
				t.Errorf("Roots(generators.txtar) roots %s as a generator input: %t, want %t", ref, named[ref], want)
			}
		})
	}
}

func TestRootsRootsAGoFuzzEntryOfAFileTheGofuzzTagSelects(t *testing.T) {
	tagged := load.Configuration{ID: "linux-amd64-gofuzz", OS: "linux", Arch: "amd64", Tags: []string{"gofuzz"}}
	named, held := generatorRoots(t, tagged, roots.RootFuzz)
	got := slices.Sorted(maps.Keys(named))
	want := []string{"go://example.com/gen/fuzz#Fuzz", "go://example.com/gen/fuzz#FuzzParse"}
	if !slices.Equal(got, want) {
		t.Errorf("Roots(generators.txtar) under the gofuzz tag roots %v as go-fuzz entries, want %v", got, want)
	}
	for _, ref := range []string{"go://example.com/gen/fuzz#FuzzText", "go://example.com/gen/fuzz#Fuzzy"} {
		if !held[ref] {
			t.Errorf("Symbols(generators.txtar) under the gofuzz tag holds no symbol whose reference is %s", ref)
		}
	}

	untagged, held := generatorRoots(t, load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}, roots.RootFuzz)
	if negated := "go://example.com/gen/fuzz#FuzzNegated"; !held[negated] || len(untagged) != 0 {
		t.Errorf("Roots(generators.txtar) without the tag roots %v as go-fuzz entries, want none beside %s", untagged, negated)
	}
}

func TestRootsRootsADeclarationASwaggerDirectiveNamesWithItsFields(t *testing.T) {
	named, held := generatorRoots(t, load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}, roots.RootSwagger)

	const pkg = "go://example.com/gen/api#"
	for ref, want := range map[string]bool{
		pkg + "notFoundResponse":              true,
		pkg + "notFoundResponse.Body":         true,
		pkg + "notFoundResponse.Body.Message": true,
		pkg + "item":                          true,
		pkg + "item.ID":                       true,
		pkg + "handler":                       true,
		pkg + "plain":                         false,
		pkg + "plain.Field":                   false,
		pkg + "Serve":                         false,
	} {
		t.Run(ref, func(t *testing.T) {
			if !held[ref] {
				t.Fatalf("Symbols(generators.txtar) holds no symbol whose reference is %s", ref)
			}
			if named[ref] != want {
				t.Errorf("Roots(generators.txtar) roots %s by a swagger directive: %t, want %t", ref, named[ref], want)
			}
		})
	}
}

func TestUnlinkedPackagesIsWhatNoBuildLinksOfAModuleLoadedWhole(t *testing.T) {
	dir := extract(t, "unlinked.txtar")
	host := load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}
	result, _ := loadDirUnder(t.Context(), t, dir, host)
	var got []string
	for id := range roots.UnlinkedPackages(result) {
		got = append(got, id.Path)
	}
	slices.Sort(got)
	want := []string{"example.com/unlinked/leaf", "example.com/unlinked/orphan", "example.com/unlinked/unused"}
	if !slices.Equal(got, want) {
		t.Errorf("UnlinkedPackages(unlinked.txtar) = %v, want %v", got, want)
	}

	below, _ := loadDirUnder(t.Context(), t, filepath.Join(dir, "unused"), host)
	if got := roots.UnlinkedPackages(below); len(got) != 0 {
		t.Errorf("UnlinkedPackages(unlinked.txtar/unused) = %v, want none: a package outside the load may import it", got)
	}
}

func TestRootsLeavesTheInitAndBlankDeclarationsOfAnUnlinkedApplicationPackageUnrooted(t *testing.T) {
	for _, published := range []bool{false, true} {
		d := detect(t, "unlinked.txtar", roots.RootOptions{PublishedAPI: published})
		var inits []string
		blanks := 0
		for _, r := range d.roots {
			switch r.Kind {
			case roots.RootInit:
				inits = append(inits, string(r.ID))
			case roots.RootBlank:
				blanks++
			}
		}
		slices.Sort(inits)
		want := []string{"driver/driver.go:3:6", "tested/tested.go:3:6", "used/used.go:3:6"}
		if published {
			want = []string{"driver/driver.go:3:6", "leaf/leaf.go:3:6", "orphan/orphan.go:5:6", "tested/tested.go:3:6", "unused/unused.go:3:6", "used/used.go:3:6"}
		}
		if !slices.Equal(inits, want) {
			t.Errorf("Roots(unlinked.txtar, published=%t) roots the init functions %v, want %v", published, inits, want)
		}
		if wantBlanks := map[bool]int{false: 0, true: 1}[published]; blanks != wantBlanks {
			t.Errorf("Roots(unlinked.txtar, published=%t) roots %d blank declarations, want %d", published, blanks, wantBlanks)
		}
	}
}
