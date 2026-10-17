package graph

import (
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

func TestSymbolsColumnsOnANonASCIILine(t *testing.T) {
	byName := make(map[string]inventory.Symbol)
	for _, s := range symbolsOf(t, "columns.txtar") {
		byName[s.Name] = s
	}

	// Every want below is the column in UTF-16 code units; byteColumn is what Go
	// reports for the same declaration and is here so a failure names the
	// conversion that did not happen.
	cases := []struct {
		name       string
		line       int
		byteColumn int
		want       int
	}{
		{name: "Emoji", line: 3, byteColumn: 5, want: 5},
		{name: "AfterEmoji", line: 3, byteColumn: 25, want: 23},
		{name: "Accent", line: 4, byteColumn: 5, want: 5},
		{name: "AfterAccent", line: 4, byteColumn: 24, want: 23},
		{name: "Both", line: 5, byteColumn: 5, want: 5},
		{name: "AfterBoth", line: 5, byteColumn: 26, want: 23},
		{name: "Ünused", line: 7, byteColumn: 6, want: 6},
		{name: "Ünused.Fïeld", line: 7, byteColumn: 22, want: 21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byName[tc.name]
			if !ok {
				t.Fatalf("Symbols(columns.txtar) holds no declaration named %q", tc.name)
			}
			if got.Pos.Line != tc.line {
				t.Errorf("Symbols(columns.txtar)[%s].Pos.Line = %d, want %d", tc.name, got.Pos.Line, tc.line)
			}
			if got.Pos.Column != tc.want {
				t.Errorf("Symbols(columns.txtar)[%s].Pos.Column = %d, want %d (Go reports byte column %d)",
					tc.name, got.Pos.Column, tc.want, tc.byteColumn)
			}
		})
	}
}

func TestSymbolsPathIsTargetRelativeWithForwardSlashes(t *testing.T) {
	for _, s := range symbolsOf(t, "every-kind.txtar") {
		path := s.Pos.Filename
		switch {
		case filepath.IsAbs(path):
			t.Errorf("Symbols(every-kind.txtar)[%s].Pos.Filename = %q, want a path relative to the target root", s.ID, path)
		case strings.Contains(path, `\`):
			t.Errorf("Symbols(every-kind.txtar)[%s].Pos.Filename = %q, want forward slashes", s.ID, path)
		case strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../"):
			t.Errorf("Symbols(every-kind.txtar)[%s].Pos.Filename = %q, want no relative prefix", s.ID, path)
		}
	}

	if _, ok := namedByRef(symbolsOf(t, "every-kind.txtar"))["go://example.com/app/internal/queue#queue.go:file"]; !ok {
		t.Error("Symbols(every-kind.txtar) holds no file symbol for the nested package, so no nested path was exercised")
	}
}

func TestSymbolsKeepsOneDeclarationPerSourceSiteAcrossVariants(t *testing.T) {
	dir := extract(t, "variants.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")

	// The premise: a package and its in-package test variant type-check
	// catalog.go independently, so the same declaration has two types.Object
	// values and one token.Pos.
	var objects []types.Object
	for _, p := range result.Packages {
		if p.PkgPath != "example.com/variants" || p.Types == nil {
			continue
		}
		if obj := p.Types.Scope().Lookup("normalize"); obj != nil {
			objects = append(objects, obj)
		}
	}
	if len(objects) != 2 {
		t.Fatalf("load(variants.txtar) reached %d variants declaring normalize, want 2 (the package and its in-package test variant)", len(objects))
	}
	if objects[0] == objects[1] {
		t.Fatal("load(variants.txtar) returned one types.Object for normalize across variants, so this fixture no longer exercises the trap it exists for")
	}
	if objects[0].Pos() != objects[1].Pos() {
		t.Fatalf("load(variants.txtar) returned token.Pos %d and %d for normalize, want one shared position", objects[0].Pos(), objects[1].Pos())
	}

	symbols, _, err := inventoryOf(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(variants.txtar) error: %v", err)
	}

	atPosition := make(map[inventory.SymbolID][]string)
	for _, s := range symbols {
		atPosition[s.ID] = append(atPosition[s.ID], s.Name)
	}
	for id, names := range atPosition {
		if len(names) != 1 {
			t.Errorf("Symbols(variants.txtar) holds %d symbols at %s (%v), want 1", len(names), id, names)
		}
	}

	wantRefs := []string{
		"go://example.com/variants#normalize",
		"go://example.com/variants#Resolve",
		"go://example.com/variants#TestNormalize",
		"go://example.com/variants_test#TestResolve",
	}
	byRef := namedByRef(symbols)
	for _, ref := range wantRefs {
		if _, ok := byRef[ref]; !ok {
			t.Errorf("Symbols(variants.txtar) holds no %s", ref)
		}
	}
}
