package deps

import (
	"go/token"
	"testing"
)

// The module file every case of the scan reads, holding one require and one
// replace directive in each shape the grammar writes: a block, a single line, a
// quoted path, a comment after a directive, a whole-line comment, a directive
// written twice, and a line the grammar does not give a meaning.
const scanned = `module example.com/app

go 1.27.1

// A comment line, which opens nothing.
require (
	example.com/first v1.0.0 // indirect
	example.com/second v2.0.0
)

require "example.com/quoted" v1.0.0

require example.com/first v1.0.0

replace (
	example.com/first v1.0.0 => ./first
	example.com/second => example.com/fork v2.0.1
)

replace example.com/third => example.com/third v3.0.0

exclude example.com/second v2.0.2
`

func TestScanReadsTheLineOfEveryRequireDirective(t *testing.T) {
	sites := scan([]byte(scanned))

	cases := map[string]struct {
		module Module
		want   token.Position
	}{
		"a block line":                        {Module{"example.com/first", "v1.0.0"}, at(7, 2)},
		"a block line whose path is longer":   {Module{"example.com/second", "v2.0.0"}, at(8, 2)},
		"a single line with a quoted path":    {Module{"example.com/quoted", "v1.0.0"}, at(11, 9)},
		"a directive written twice":           {Module{"example.com/first", "v1.0.0"}, at(7, 2)},
		"a module no require directive names": {Module{"example.com/absent", "v1.0.0"}, at(1, 1)},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sites.require(test.module); got != test.want {
				t.Errorf("scan(...).require(%+v) = %v, want %v", test.module, got, test.want)
			}
		})
	}
}

func TestScanReadsTheLineOfEveryReplaceDirective(t *testing.T) {
	sites := scan([]byte(scanned))

	cases := map[string]struct {
		key  replaceKey
		want token.Position
	}{
		"a block line replacing one version with a directory": {
			replaceKey{Module{"example.com/first", "v1.0.0"}, Module{Path: "./first"}}, at(16, 2),
		},
		"a block line replacing every version with a module": {
			replaceKey{Module{Path: "example.com/second"}, Module{"example.com/fork", "v2.0.1"}}, at(17, 2),
		},
		"a single line": {
			replaceKey{Module{Path: "example.com/third"}, Module{"example.com/third", "v3.0.0"}}, at(20, 9),
		},
		"a replacement of a version the directive does not name": {
			replaceKey{Module{"example.com/first", "v2.0.0"}, Module{Path: "./first"}}, at(1, 1),
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sites.replace(test.key); got != test.want {
				t.Errorf("scan(...).replace(%+v) = %v, want %v", test.key, got, test.want)
			}
		})
	}
}

func TestScanReadsNoDirectiveFromALineTheGrammarClosed(t *testing.T) {
	// An exclude directive names a module the way a require directive does, so a
	// scan that read every block alike would answer the exclude's line for the
	// require the file does not carry.
	excluded := Module{Path: "example.com/second", Version: "v2.0.2"}
	if got, want := scan([]byte(scanned)).require(excluded), at(1, 1); got != want {
		t.Errorf("scan(...).require(%+v) = %v, want %v, the position of a directive the file does not carry",
			excluded, got, want)
	}
}

func TestLineWordsReadsTheWordsAndColumnsOfOneLine(t *testing.T) {
	cases := map[string]struct {
		line string
		want []word
	}{
		"a tab-indented pair": {
			"\texample.com/a v1.0.0",
			[]word{{"example.com/a", 2}, {"v1.0.0", 16}},
		},
		"a comment after the words": {
			"example.com/a v1.0.0 // indirect",
			[]word{{"example.com/a", 1}, {"v1.0.0", 15}},
		},
		"a whole-line comment": {
			"// example.com/a v1.0.0",
			nil,
		},
		"a quoted path": {
			`require "example.com/a" v1.0.0`,
			[]word{{"require", 1}, {"example.com/a", 9}, {"v1.0.0", 25}},
		},
		"a quotation that does not close": {
			`require "example.com/a v1.0.0`,
			[]word{{"require", 1}, {`"example.com/a`, 9}, {"v1.0.0", 24}},
		},
		"a quoted path holding what opens a comment": {
			`require "example.com//a" v1.0.0`,
			[]word{{"require", 1}, {"example.com//a", 9}, {"v1.0.0", 26}},
		},
		"an empty line": {"", nil},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := lineWords(test.line)
			if len(got) != len(test.want) {
				t.Fatalf("lineWords(%q) = %+v, want %+v", test.line, got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("lineWords(%q)[%d] = %+v, want %+v", test.line, i, got[i], test.want[i])
				}
			}
		})
	}
}
