package graph

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
)

// generatorRoots is the reference of every symbol of generators.txtar one kind of
// root names under one configuration, and the reference of every symbol loaded.
func generatorRoots(t *testing.T, c load.Configuration, kind RootKind) (named, held map[string]bool) {
	t.Helper()
	return archiveRoots(t, "generators.txtar", c, kind)
}

// archiveRoots is the references of the roots of one kind of an archive loaded under
// one configuration, and the references of every symbol it holds.
func archiveRoots(t *testing.T, archive string, c load.Configuration, kind RootKind) (named, held map[string]bool) {
	t.Helper()
	result, root := loadDirUnder(t.Context(), t, extract(t, archive), c)
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(%s) error: %v", archive, err)
	}
	roots, _, err := Roots(result, root, os.ReadFile, symbols, RootOptions{})
	if err != nil {
		t.Fatalf("Roots(%s) error: %v", archive, err)
	}
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
	named, held := generatorRoots(t, load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}, RootGenerate)

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
	tagged := load.Configuration{ID: "linux-amd64-gofuzz", OS: "linux", Arch: "amd64", Tags: []string{goFuzzTag}}
	named, held := generatorRoots(t, tagged, RootFuzz)
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

	untagged, held := generatorRoots(t, load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}, RootFuzz)
	if negated := "go://example.com/gen/fuzz#FuzzNegated"; !held[negated] || len(untagged) != 0 {
		t.Errorf("Roots(generators.txtar) without the tag roots %v as go-fuzz entries, want none beside %s", untagged, negated)
	}
}

func TestRootsRootsADeclarationASwaggerDirectiveNamesWithItsFields(t *testing.T) {
	named, held := generatorRoots(t, load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}, RootSwagger)

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

func TestGeneratorOfNamesTheProgramADirectiveRuns(t *testing.T) {
	for _, test := range []struct {
		words []string
		name  string
		args  []string
	}{
		{words: []string{"stringer", "-type=A"}, name: "stringer", args: []string{"-type=A"}},
		{words: []string{"go", "run", "-mod=mod", "go.uber.org/mock/mockgen@v0.5.0", "x"}, name: "mockgen", args: []string{"x"}},
		{words: []string{"go", "tool", "enumer", "-type", "B"}, name: "enumer", args: []string{"-type", "B"}},
		{words: []string{"$GOBIN/mockgen"}, name: "mockgen", args: []string{}},
		{words: []string{"go", "run"}, name: "go", args: []string{"run"}},
		{words: []string{"go", "run", "-x"}, name: "", args: nil},
		{words: []string{"go", "run", "-mod", "mod", "-race", "golang.org/x/tools/cmd/stringer", "-type=A"}, name: "stringer", args: []string{"-type=A"}},
		{words: nil, name: "", args: nil},
	} {
		name, args := generatorOf(test.words)
		if name != test.name || !slices.Equal(args, test.args) {
			t.Errorf("generatorOf(%q) = %q, %q, want %q, %q", test.words, name, args, test.name, test.args)
		}
	}
}

func TestFlagValueAndPositionalArgsReadEachFlagForm(t *testing.T) {
	args := []string{"-typed", "-destination", "mock.go", "-package=app", ".", "Store,Cache"}
	if got := flagValue(args, "destination", mockgenBooleanFlags); got != "mock.go" {
		t.Errorf("flagValue(%q, destination) = %q, want mock.go", args, got)
	}
	if got := flagValue(args, "package", mockgenBooleanFlags); got != "app" {
		t.Errorf("flagValue(%q, package) = %q, want app", args, got)
	}
	if got := flagValue(args, "source", mockgenBooleanFlags); got != "" {
		t.Errorf("flagValue(%q, source) = %q, want none", args, got)
	}
	if got, want := positionalArgs(args, mockgenBooleanFlags), []string{".", "Store,Cache"}; !slices.Equal(got, want) {
		t.Errorf("positionalArgs(%q) = %q, want %q", args, got, want)
	}
	boolean := []string{"-linecomment", "-type", "Kind", "-json", "-output=x.go"}
	if got := flagValue(boolean, "type", nil); got != "Kind" {
		t.Errorf("flagValue(%q, type) = %q, want Kind: a flag before another flag takes no value", boolean, got)
	}
	unlisted := []string{"-self_package_check", "-destination", "mock.go", ".", "Store"}
	if got, want := positionalArgs(unlisted, mockgenBooleanFlags), []string{".", "Store"}; !slices.Equal(got, want) {
		t.Errorf("positionalArgs(%q) = %q, want %q: a flag before another flag takes no value", unlisted, got, want)
	}
}

func TestGenerateWordsKeepsAQuotedStringOneWordAndExpandsEachWord(t *testing.T) {
	got := generateWords(`mockgen  -source "my file.go" -destination=mock_$GOFILE	-package=$GOPACKAGE`, "f.go", "p")
	want := []string{"mockgen", "-source", "my file.go", "-destination=mock_f.go", "-package=p"}
	if !slices.Equal(got, want) {
		t.Errorf("generateWords() = %q, want %q", got, want)
	}
}

func TestUnlinkedPackagesIsWhatNoBuildLinksOfAModuleLoadedWhole(t *testing.T) {
	dir := extract(t, "unlinked.txtar")
	host := load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}
	result, _ := loadDirUnder(t.Context(), t, dir, host)
	var got []string
	for id := range UnlinkedPackages(result) {
		got = append(got, id.Path)
	}
	slices.Sort(got)
	want := []string{"example.com/unlinked/leaf", "example.com/unlinked/orphan", "example.com/unlinked/unused"}
	if !slices.Equal(got, want) {
		t.Errorf("UnlinkedPackages(unlinked.txtar) = %v, want %v", got, want)
	}

	below, _ := loadDirUnder(t.Context(), t, filepath.Join(dir, "unused"), host)
	if got := UnlinkedPackages(below); len(got) != 0 {
		t.Errorf("UnlinkedPackages(unlinked.txtar/unused) = %v, want none: a package outside the load may import it", got)
	}
}

func TestRootsLeavesTheInitAndBlankDeclarationsOfAnUnlinkedApplicationPackageUnrooted(t *testing.T) {
	for _, published := range []bool{false, true} {
		d := detect(t, "unlinked.txtar", RootOptions{PublishedAPI: published})
		var inits []string
		blanks := 0
		for _, r := range d.roots {
			switch r.Kind {
			case RootInit:
				inits = append(inits, string(r.ID))
			case RootBlank:
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
