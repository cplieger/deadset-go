package graph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	"golang.org/x/tools/txtar"
)

// extract writes one txtar archive from testdata into a temporary directory and
// returns that directory.
func extract(t *testing.T, archive string) string {
	t.Helper()
	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	dir := t.TempDir()
	for _, f := range parsed.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	return dir
}

// failureSink is what a loader needs of the test that calls it: mark itself a
// helper, and end that test with a diagnostic. Both *testing.T and *rapid.T
// satisfy it, which is what lets the unit suite and the properties share one
// loader. Neither a temporary directory nor a context is in that method set, so
// the directory and the context arrive as parameters.
type failureSink interface {
	Helper()
	Fatalf(format string, args ...any)
}

// loadDirUnder resolves one directory through the production load, so the Need
// bits the enumeration depends on have one owner.
func loadDirUnder(ctx context.Context, sink failureSink, dir string, c load.Configuration) (*load.Result, string) {
	sink.Helper()

	doc, err := scope.ForDir(dir)
	if err != nil {
		sink.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	result, err := load.Load(ctx, doc, c)
	if err != nil {
		sink.Fatalf("Setup: load.Load(%s, %s): %v", dir, c.ID, err)
	}
	return &result, doc.Target.Path
}

// loadDir resolves one directory under the build configuration goos and goarch
// name.
func loadDir(t *testing.T, dir, goos, goarch string) (*load.Result, string) {
	t.Helper()
	return loadDirUnder(t.Context(), t, dir, load.Configuration{ID: goos + "-" + goarch, OS: goos, Arch: goarch})
}

// symbolsOf extracts one archive, loads it for the host configuration and
// enumerates it.
func symbolsOf(t *testing.T, archive string) []Symbol {
	t.Helper()
	dir := extract(t, archive)
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(%s) error: %v", archive, err)
	}
	return symbols
}

// render prints one symbol per line, tab-separated, for a golden comparison.
func render(symbols []Symbol) string {
	var b strings.Builder
	for _, s := range symbols {
		fmt.Fprintf(&b, "%s:%d:%d-%d\t%s\t%s\t%s\tparent=%s\texported=%t\tblank=%t\n",
			s.Pos.Filename, s.Pos.Line, s.Pos.Column, s.EndLine,
			s.Kind, s.Ref, s.Name, s.Parent, s.Exported, s.Blank)
	}
	return b.String()
}

func TestSymbolsGoldenTableOverEveryKind(t *testing.T) {
	got := render(symbolsOf(t, "every-kind.txtar"))
	golden := filepath.Join("testdata", "every-kind.golden")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", golden, err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("Setup: read %s (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run TestSymbolsGoldenTableOverEveryKind): %v", golden, err)
	}
	if got != string(want) {
		t.Errorf("Symbols(every-kind.txtar) golden mismatch (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run TestSymbolsGoldenTableOverEveryKind)\n--- want\n%s\n+++ got\n%s", want, got)
	}
}

func TestSymbolsReachesEveryKind(t *testing.T) {
	symbols := symbolsOf(t, "every-kind.txtar")
	found := make(map[SymbolKind][]string)
	for _, s := range symbols {
		found[s.Kind] = append(found[s.Kind], s.Ref)
	}

	all := []SymbolKind{
		KindFunc, KindMethod, KindType, KindInterface, KindInterfaceMethod,
		KindField, KindConst, KindVar, KindTypeParam, KindPackage, KindFile,
	}
	for _, kind := range all {
		t.Run(kind.String(), func(t *testing.T) {
			if len(found[kind]) == 0 {
				t.Errorf("Symbols(every-kind.txtar) reached no symbol of kind %s; kinds reached: %v", kind, reached(found))
			}
		})
	}
}

// reached names the kinds an enumeration produced, for a failure message.
func reached(found map[SymbolKind][]string) []string {
	names := make([]string, 0, len(found))
	for kind := range found {
		names = append(names, kind.String())
	}
	return names
}

// namedByRef indexes the symbols that carry a reference of their own, keeping
// the outermost of any that share one. A blank declaration takes its container's
// reference, so several symbols can answer to one reference and the first by
// position is the one it names.
func namedByRef(symbols []Symbol) map[string]Symbol {
	byRef := make(map[string]Symbol, len(symbols))
	for _, s := range symbols {
		if _, taken := byRef[s.Ref]; taken || s.Blank {
			continue
		}
		byRef[s.Ref] = s
	}
	return byRef
}

func TestSymbolsNamesEveryMemberWithItsContainer(t *testing.T) {
	byRef := namedByRef(symbolsOf(t, "every-kind.txtar"))

	cases := []struct {
		ref     string
		kind    SymbolKind
		name    string
		parent  string
		blank   bool
		wantHit bool
	}{
		{ref: "go://example.com/app#", kind: KindPackage, name: "example.com/app", wantHit: true},
		{ref: "go://example.com/app#catalog.go:file", kind: KindFile, name: "catalog.go", parent: "go://example.com/app#", wantHit: true},
		{ref: "go://example.com/app#Fetcher", kind: KindInterface, name: "Fetcher", parent: "go://example.com/app#", wantHit: true},
		{ref: "go://example.com/app#Fetcher.Fetch", kind: KindInterfaceMethod, name: "Fetcher.Fetch", parent: "go://example.com/app#Fetcher", wantHit: true},
		{ref: "go://example.com/app#Catalog.entries", kind: KindField, name: "Catalog.entries", parent: "go://example.com/app#Catalog", wantHit: true},
		// A blank declaration carries no reference of its own, so nothing is
		// reachable under the name the source spells.
		{ref: "go://example.com/app#Catalog._", wantHit: false},
		{ref: "go://example.com/app#_", wantHit: false},
		{ref: "go://example.com/app#Catalog.Tools.Name", kind: KindField, name: "Catalog.Tools.Name", parent: "go://example.com/app#Catalog.Tools", wantHit: true},
		{ref: "go://example.com/app#Buffered.Catalog", kind: KindField, name: "Buffered.Catalog", parent: "go://example.com/app#Buffered", wantHit: true},
		{ref: "go://example.com/app#Alias", kind: KindType, name: "Alias", parent: "go://example.com/app#", wantHit: true},
		{ref: "go://example.com/app#Exact", kind: KindConst, name: "Exact", parent: "go://example.com/app#", wantHit: true},
		{ref: "go://example.com/app#ErrNotFound", kind: KindVar, name: "ErrNotFound", parent: "go://example.com/app#", wantHit: true},
		{ref: "go://example.com/app#Catalog.ResolveAlias", kind: KindMethod, name: "(*Catalog).ResolveAlias", parent: "go://example.com/app#Catalog", wantHit: true},
		{ref: "go://example.com/app#Catalog.Size", kind: KindMethod, name: "Catalog.Size", parent: "go://example.com/app#Catalog", wantHit: true},
		{ref: "go://example.com/app#Best[T]", kind: KindTypeParam, name: "Best[T]", parent: "go://example.com/app#Best", wantHit: true},
		{ref: "go://example.com/app/internal/queue#List.Push", kind: KindMethod, name: "(*List).Push", parent: "go://example.com/app/internal/queue#List", wantHit: true},
		{ref: "go://example.com/app/internal/queue#List.Contains", kind: KindMethod, name: "(*List).Contains", parent: "go://example.com/app/internal/queue#List", wantHit: true},
		{ref: "go://example.com/app/internal/queue#List.Contains[U]", kind: KindTypeParam, name: "(*List).Contains[U]", parent: "go://example.com/app/internal/queue#List.Contains", wantHit: true},
		// A type's own type parameters are not subjects.
		{ref: "go://example.com/app/internal/queue#List[T]", wantHit: false},
	}

	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got, ok := byRef[tc.ref]
			if ok != tc.wantHit {
				t.Fatalf("Symbols(every-kind.txtar) holds %s = %t, want %t", tc.ref, ok, tc.wantHit)
			}
			if !tc.wantHit {
				return
			}
			if got.Kind != tc.kind {
				t.Errorf("Symbols(every-kind.txtar)[%s].Kind = %s, want %s", tc.ref, got.Kind, tc.kind)
			}
			if got.Name != tc.name {
				t.Errorf("Symbols(every-kind.txtar)[%s].Name = %q, want %q", tc.ref, got.Name, tc.name)
			}
			if parent := parentRef(byRef, got); tc.parent != "" && parent != tc.parent {
				t.Errorf("Symbols(every-kind.txtar)[%s] parent = %q, want %q", tc.ref, parent, tc.parent)
			}
			if got.Blank != tc.blank {
				t.Errorf("Symbols(every-kind.txtar)[%s].Blank = %t, want %t", tc.ref, got.Blank, tc.blank)
			}
		})
	}
}

// parentRef resolves one symbol's container to that container's reference.
func parentRef(byRef map[string]Symbol, s Symbol) string {
	for _, candidate := range byRef {
		if candidate.ID == s.Parent {
			return candidate.Ref
		}
	}
	return ""
}

func TestSymbolsCountsTheBlankDeclarations(t *testing.T) {
	var blanks []string
	for _, s := range symbolsOf(t, "every-kind.txtar") {
		if s.Blank {
			blanks = append(blanks, s.Name)
		}
	}
	if want := []string{"Catalog._", "_"}; !slices.Equal(blanks, want) {
		t.Errorf("Symbols(every-kind.txtar) found blank declarations %v, want %v", blanks, want)
	}
}

func TestSymbolsRecordsExportedness(t *testing.T) {
	byName := make(map[string]Symbol)
	for _, s := range symbolsOf(t, "every-kind.txtar") {
		byName[s.Name] = s
	}

	cases := []struct {
		name string
		want bool
	}{
		{name: "Resolve", want: true},
		{name: "normalize", want: false},
		{name: "Catalog", want: true},
		{name: "Catalog.entries", want: false},
		{name: "Catalog.Tools", want: true},
		{name: "Fetcher.Fetch", want: true},
		{name: "Fetcher.fetchInternal", want: false},
		{name: "(*Catalog).ResolveAlias", want: true},
		{name: "init", want: false},
		{name: "Best[T]", want: true},
		// A file and a package are not exported, whatever their names look like.
		{name: "catalog.go", want: false},
		{name: "example.com/app", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byName[tc.name]
			if !ok {
				t.Fatalf("Symbols(every-kind.txtar) holds no declaration named %q", tc.name)
			}
			if got.Exported != tc.want {
				t.Errorf("Symbols(every-kind.txtar)[%s].Exported = %t, want %t", tc.name, got.Exported, tc.want)
			}
		})
	}
}

func TestSymbolsRecordsSizeInSourceLines(t *testing.T) {
	byRef := namedByRef(symbolsOf(t, "every-kind.txtar"))

	cases := []struct {
		ref   string
		lines int
	}{
		{"go://example.com/app#Best", 8},
		{"go://example.com/app#Resolve", 1},
		{"go://example.com/app#Catalog", 5},
		{"go://example.com/app#Fetcher.Fetch", 1},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			s, ok := byRef[tc.ref]
			if !ok {
				t.Fatalf("Symbols(every-kind.txtar) holds no %s", tc.ref)
			}
			if lines := s.EndLine - s.Pos.Line + 1; lines != tc.lines {
				t.Errorf("Symbols(every-kind.txtar)[%s] spans %d lines (%d to %d), want %d",
					tc.ref, lines, s.Pos.Line, s.EndLine, tc.lines)
			}
		})
	}
}

func TestSymbolsOrderIsTheSameOnEveryCall(t *testing.T) {
	dir := extract(t, "every-kind.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")

	first, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols first call error: %v", err)
	}
	second, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols second call error: %v", err)
	}
	if got, want := render(second), render(first); got != want {
		t.Errorf("Symbols returned a different order on the second call\n--- first\n%s\n+++ second\n%s", want, got)
	}

	for i := 1; i < len(first); i++ {
		if bySite(first[i-1], first[i]) >= 0 {
			t.Fatalf("Symbols order is not strictly increasing at %d: %s then %s",
				i, first[i-1].ID, first[i].ID)
		}
	}
}

func TestSymbolsIDIsTheRenderedPosition(t *testing.T) {
	for _, s := range symbolsOf(t, "every-kind.txtar") {
		want := SymbolID(fmt.Sprintf("%s:%d:%d", s.Pos.Filename, s.Pos.Line, s.Pos.Column))
		if s.ID != want {
			t.Errorf("Symbols(every-kind.txtar)[%s].ID = %q, want %q", s.Ref, s.ID, want)
		}
	}
}

func TestSymbolsRefusesAnIncompleteRequest(t *testing.T) {
	dir := extract(t, "every-kind.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")

	cases := []struct {
		name    string
		result  *load.Result
		root    string
		read    ReadFile
		wantErr error
	}{
		{name: "no result", result: nil, root: root, read: os.ReadFile, wantErr: ErrIncompleteLoad},
		{name: "no file set", result: &load.Result{Packages: result.Packages}, root: root, read: os.ReadFile, wantErr: ErrIncompleteLoad},
		{name: "no reader", result: result, root: root, read: nil, wantErr: ErrIncompleteLoad},
		{name: "root outside the load", result: result, root: t.TempDir(), read: os.ReadFile, wantErr: ErrNoTargetPackage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Symbols(tc.result, tc.root, tc.read)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Symbols(%s) error = %v, want one satisfying errors.Is(err, %v)", tc.name, err, tc.wantErr)
			}
			if got != nil {
				t.Errorf("Symbols(%s) returned %d symbols, want none", tc.name, len(got))
			}
		})
	}
}

func TestSymbolsReportsAnUnreadableSource(t *testing.T) {
	dir := extract(t, "every-kind.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")

	refuse := func(string) ([]byte, error) { return nil, os.ErrPermission }
	got, err := Symbols(result, root, refuse)
	if !errors.Is(err, ErrSource) {
		t.Fatalf("Symbols with a refusing reader error = %v, want one satisfying errors.Is(err, %v)", err, ErrSource)
	}
	if got != nil {
		t.Errorf("Symbols with a refusing reader returned %d symbols, want none", len(got))
	}
}

func TestSymbolKindStringNamesEveryKind(t *testing.T) {
	cases := []struct {
		kind SymbolKind
		want string
	}{
		{KindFunc, "function"},
		{KindMethod, "method"},
		{KindType, "type"},
		{KindInterface, "interface"},
		{KindInterfaceMethod, "interface-method"},
		{KindField, "field"},
		{KindConst, "constant"},
		{KindVar, "variable"},
		{KindTypeParam, "type-parameter"},
		{KindPackage, "package"},
		{KindFile, "file"},
		{SymbolKind(200), "SymbolKind(200)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.kind.String(); got != tc.want {
				t.Errorf("SymbolKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

func TestSymbolsGivesABlankDeclarationItsContainersReference(t *testing.T) {
	symbols := symbolsOf(t, "every-kind.txtar")
	byID := make(map[SymbolID]Symbol, len(symbols))
	for _, s := range symbols {
		byID[s.ID] = s
	}

	cases := []struct {
		name string
		ref  string
	}{
		{name: "Catalog._", ref: "go://example.com/app#Catalog"},
		{name: "_", ref: "go://example.com/app#"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got Symbol
			for _, s := range symbols {
				if s.Blank && s.Name == tc.name {
					got = s
				}
			}
			if got.ID == "" {
				t.Fatalf("Symbols(every-kind.txtar) holds no blank declaration named %q", tc.name)
			}
			if got.Ref != tc.ref {
				t.Errorf("Symbols(every-kind.txtar) blank %q has Ref %q, want %q", tc.name, got.Ref, tc.ref)
			}
			if parent := byID[got.Parent]; parent.Ref != tc.ref {
				t.Errorf("Symbols(every-kind.txtar) blank %q has parent %q, want the symbol whose Ref is %q",
					tc.name, parent.Ref, tc.ref)
			}
		})
	}
}

func TestSymbolsGivesAMethodTypeParameterItsOwnBracketedReference(t *testing.T) {
	const (
		methodRef    = "go://example.com/app/internal/queue#List.Contains"
		parameterRef = "go://example.com/app/internal/queue#List.Contains[U]"
	)

	var method, parameter Symbol
	for _, s := range symbolsOf(t, "every-kind.txtar") {
		switch {
		case s.Kind == KindMethod && s.Ref == methodRef:
			method = s
		case s.Kind == KindTypeParam && s.Name == "(*List).Contains[U]":
			parameter = s
		}
	}
	if method.ID == "" || parameter.ID == "" {
		t.Fatalf("Symbols(every-kind.txtar) found method=%q parameter=%q, want both", method.ID, parameter.ID)
	}
	if parameter.Ref != parameterRef {
		t.Errorf("Symbols(every-kind.txtar) type parameter of %s has Ref %q, want %q", methodRef, parameter.Ref, parameterRef)
	}
	if parameter.Parent != method.ID {
		t.Errorf("Symbols(every-kind.txtar) type parameter of %s has Parent %q, want %q", methodRef, parameter.Parent, method.ID)
	}
	if parameter.Pos.Line != method.Pos.Line || parameter.Pos.Column == method.Pos.Column {
		t.Errorf("Symbols(every-kind.txtar) type parameter is at %s:%d:%d and the method at %s:%d:%d, want one line and two columns",
			parameter.Pos.Filename, parameter.Pos.Line, parameter.Pos.Column,
			method.Pos.Filename, method.Pos.Line, method.Pos.Column)
	}
}
