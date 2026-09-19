package graph

import (
	"errors"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPositionsColumnCountsUTF16CodeUnits(t *testing.T) {
	// One line, three declarations after non-ASCII text. The byte column Go
	// reports, the UTF-16 column a report carries and the code-point column all
	// differ once an astral character is in the prefix.
	const line = `var Both = "🎉é"; var AfterBoth = 4` + "\n"

	cases := []struct {
		name       string
		byteColumn int
		want       int
	}{
		{name: "start of line", byteColumn: 1, want: 1},
		{name: "ASCII prefix", byteColumn: 5, want: 5},
		{name: "after an astral character and a two-byte character", byteColumn: 26, want: 23},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPositions(token.NewFileSet(), "/root", func(string) ([]byte, error) {
				return []byte(line), nil
			})
			q := token.Position{Filename: "/root/columns.go", Line: 1, Column: tc.byteColumn, Offset: tc.byteColumn - 1}
			got, err := p.column(q)
			if err != nil {
				t.Fatalf("column(byte %d) error: %v", tc.byteColumn, err)
			}
			if got != tc.want {
				t.Errorf("column(byte %d) = %d, want %d", tc.byteColumn, got, tc.want)
			}
		})
	}
}

func TestColumnCountsUTF16CodeUnitsToTheOffset(t *testing.T) {
	const line = `var Both = "🎉é"; var AfterBoth = 4` + "\n"

	cases := map[string]struct {
		line   string
		offset int
		want   int
	}{
		"the start of a line":                 {line: line, offset: 0, want: 1},
		"an ASCII prefix":                     {line: line, offset: 4, want: 5},
		"after an astral and a two-byte rune": {line: line, offset: 25, want: 23},
		// An offset inside a rune is a position Go never reports, because every
		// position it reports is a rune boundary. The bytes of the cut rune decode
		// as one replacement character each, which is one code unit each.
		"inside a multi-byte rune":     {line: line, offset: 13, want: 14},
		"the end of a line":            {line: "ab\n", offset: 3, want: 4},
		"an empty line":                {line: "", offset: 0, want: 1},
		"an offset past the line":      {line: "abc", offset: 99, want: 4},
		"a negative offset":            {line: "abc", offset: -1, want: 1},
		"a byte no encoding holds":     {line: "a\xffb", offset: 3, want: 4},
		"one astral rune, two units":   {line: "🎉", offset: 4, want: 3},
		"a two-byte rune, one unit":    {line: "é", offset: 2, want: 2},
		"a three-byte rune, one unit":  {line: "€", offset: 3, want: 2},
		"a combining pair, two units":  {line: "éa", offset: 3, want: 3},
		"a tab counts as one unit":     {line: "\t\tx", offset: 2, want: 3},
		"a carriage return is counted": {line: "a\r\n", offset: 2, want: 3},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Column([]byte(test.line), test.offset); got != test.want {
				t.Errorf("Column(%q, %d) = %d, want %d", test.line, test.offset, got, test.want)
			}
		})
	}
}

func TestByPositionOrdersByFileThenLineThenColumn(t *testing.T) {
	at := func(file string, line, column int) token.Position {
		return token.Position{Filename: file, Line: line, Column: column}
	}

	cases := map[string]struct {
		a, b token.Position
		want int
	}{
		"the same position":     {a: at("a.go", 2, 3), b: at("a.go", 2, 3), want: 0},
		"an earlier file":       {a: at("a.go", 9, 9), b: at("b.go", 1, 1), want: -1},
		"a later file":          {a: at("b.go", 1, 1), b: at("a.go", 9, 9), want: 1},
		"an earlier line":       {a: at("a.go", 1, 9), b: at("a.go", 2, 1), want: -1},
		"an earlier column":     {a: at("a.go", 2, 1), b: at("a.go", 2, 2), want: -1},
		"a later column":        {a: at("a.go", 2, 3), b: at("a.go", 2, 2), want: 1},
		"a directory in a path": {a: at("a/z.go", 1, 1), b: at("b/a.go", 1, 1), want: -1},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := ByPosition(test.a, test.b)
			if (got < 0) != (test.want < 0) || (got > 0) != (test.want > 0) {
				t.Errorf("ByPosition(%v, %v) = %d, want a value with the sign of %d", test.a, test.b, got, test.want)
			}
		})
	}
}

func TestPositionsColumnRefusesAPositionOutsideTheFile(t *testing.T) {
	p := newPositions(token.NewFileSet(), "/root", func(string) ([]byte, error) {
		return []byte("package app\n"), nil
	})
	q := token.Position{Filename: "/root/app.go", Line: 1, Column: 4, Offset: 4000}
	if _, err := p.column(q); !errors.Is(err, ErrSource) {
		t.Errorf("column(offset beyond the file) error = %v, want one satisfying errors.Is(err, %v)", err, ErrSource)
	}
}

func TestSymbolsColumnsOnANonASCIILine(t *testing.T) {
	byName := make(map[string]Symbol)
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

	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(variants.txtar) error: %v", err)
	}

	atPosition := make(map[SymbolID][]string)
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
