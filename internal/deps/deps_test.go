package deps

import (
	"go/token"
	"slices"
	"testing"
)

func TestDirectivesReadsEveryRequireDirectiveWithItsLine(t *testing.T) {
	f := moduleFileOf(t, extract(t, "directives.txtar"))

	want := []Requirement{
		{Path: "example.com/direct", Site: at(8, 2)},
		{Path: "example.com/indirect", Indirect: true, Site: at(9, 2)},
		{Path: "example.com/quoted", Site: at(12, 9)},
	}
	if !slices.Equal(f.Requires, want) {
		t.Errorf("Directives(directives.txtar).Requires = %+v, want %+v", f.Requires, want)
	}
}

func TestDirectivesReadsEveryReplaceDirectiveWithItsLine(t *testing.T) {
	f := moduleFileOf(t, extract(t, "directives.txtar"))

	want := []Replacement{
		{
			Old:  Module{Path: "example.com/direct", Version: "v1.2.3"},
			New:  Module{Path: "example.com/fork", Version: "v1.2.4"},
			Site: at(17, 2),
		},
		{
			Old:  Module{Path: "example.com/indirect"},
			New:  Module{Path: "./local"},
			Site: at(18, 2),
		},
		{
			Old:  Module{Path: "example.com/single/v2"},
			New:  Module{Path: "example.com/fork/v2", Version: "v2.0.0"},
			Site: at(21, 9),
		},
	}
	if !slices.Equal(f.Replaces, want) {
		t.Errorf("Directives(directives.txtar).Replaces = %+v, want %+v", f.Replaces, want)
	}
}

func TestDirectivesReadsTheToolDirectivesPackagePath(t *testing.T) {
	f := moduleFileOf(t, extract(t, "directives.txtar"))

	if want := []string{"example.com/direct/cmd/gen"}; !slices.Equal(f.Tools, want) {
		t.Errorf("Directives(directives.txtar).Tools = %v, want %v", f.Tools, want)
	}
}

// at is the position a directive written at one line and column carries.
func at(line, column int) token.Position {
	return token.Position{Filename: modFileName, Line: line, Column: column}
}
