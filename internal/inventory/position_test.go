package inventory

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
			p := NewPositions(token.NewFileSet(), "/root", func(string) ([]byte, error) {
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
	p := NewPositions(token.NewFileSet(), "/root", func(string) ([]byte, error) {
		return []byte("package app\n"), nil
	})
	q := token.Position{Filename: "/root/app.go", Line: 1, Column: 4, Offset: 4000}
	if _, err := p.column(q); !errors.Is(err, ErrSource) {
		t.Errorf("column(offset beyond the file) error = %v, want one satisfying errors.Is(err, %v)", err, ErrSource)
	}
}

func TestRenderCountsUTF16UnitsFromWhereTheColumnsDirectiveTakesEffect(t *testing.T) {
	for name, test := range map[string]struct{ line, want string }{
		"a two-byte rune before an inline directive":  {line: `var s = "é"; /*line main.go:30:40*/ var t = 1`, want: "main.go:30:45"},
		"a two-byte rune after an inline directive":   {line: `var s = 1; /*line main.go:30:40*/ var u = "é"; var t = 1`, want: "main.go:30:58"},
		"a two-byte rune in an inline directive":      {line: `/*line é.go:30:40*/ var t = 1`, want: "é.go:30:45"},
		"a directive's text in a string literal":      {line: `var s = "é /*line x.go:1:1*/"; var t = 1`, want: "main.go:3:36"},
		"a directive on the line above":               {line: "//line main.go:30:40\nvar s = \"é\"; var t = 1", want: "main.go:30:57"},
		"a directive naming a file outside the root":  {line: `var s = "é"; /*line /elsewhere/x.go:30:40*/ var t = 1`, want: "main.go:3:49"},
		"an inline directive with no column":          {line: `var s = "é"; /*line main.go:30*/ var t = 1`, want: "main.go:30:1"},
		"a line after an inline directive's own line": {line: "var s = \"é\"; /*line main.go:30:40*/\nvar t = 1", want: "main.go:31:5"},
	} {
		t.Run(name, func(t *testing.T) {
			src := "package p\n\n" + test.line + "\n"
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "/root/main.go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			var at token.Pos
			ast.Inspect(file, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "t" {
					at = id.Pos()
				}
				return true
			})
			p := NewPositions(fset, "/root", func(string) ([]byte, error) { return []byte(src), nil })
			q, err := p.Render(at)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%s:%d:%d", q.Filename, q.Line, q.Column); got != test.want {
				t.Errorf("Render(t) = %s, want %s", got, test.want)
			}
		})
	}
}

func TestRenderedShiftsADirectivesColumnByTheWrittenLinesUnits(t *testing.T) {
	for name, test := range map[string]struct {
		at   int
		run  string
		want int
	}{
		"an unknown column counts as the first":       {at: 0, run: "ab🎉", want: 1},
		"a line no directive shifts":                  {at: 9, run: "ab🎉", want: 7},
		"a directive's own line":                      {at: 14, run: "ab🎉", want: 12},
		"a shift past the line start counts as first": {at: 2, run: "ab🎉", want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := rendered(test.at, []byte(test.run)); got != test.want {
				t.Errorf("rendered(%d, %q) = %d, want %d", test.at, test.run, got, test.want)
			}
		})
	}
}
