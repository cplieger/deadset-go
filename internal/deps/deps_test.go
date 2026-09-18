package deps

import (
	"go/token"
	"slices"
	"testing"
)

func TestModuleFileReadsEveryRequireDirectiveWithItsLine(t *testing.T) {
	f := moduleFileOf(t, extract(t, "directives.txtar"))

	want := []Requirement{
		{Path: "example.com/direct", Version: "v1.2.3", Site: at(8, 2)},
		{Path: "example.com/indirect", Version: "v0.4.0", Indirect: true, Site: at(9, 2)},
		{Path: "example.com/quoted", Version: "v1.0.0", Site: at(12, 9)},
	}
	if !slices.Equal(f.Requires, want) {
		t.Errorf("ModuleFile(directives.txtar).Requires = %+v, want %+v", f.Requires, want)
	}
}

func TestModuleFileReadsEveryReplaceDirectiveWithItsLine(t *testing.T) {
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
		t.Errorf("ModuleFile(directives.txtar).Replaces = %+v, want %+v", f.Replaces, want)
	}
}

func TestModuleFileReadsTheToolDirectivesPackagePath(t *testing.T) {
	f := moduleFileOf(t, extract(t, "directives.txtar"))

	if want := []string{"example.com/direct/cmd/gen"}; !slices.Equal(f.Tools, want) {
		t.Errorf("ModuleFile(directives.txtar).Tools = %v, want %v", f.Tools, want)
	}
}

func TestModuleFileFailsWhereTheDirectoryHoldsNoModuleFile(t *testing.T) {
	if _, err := ModuleFile(t.Context(), t.TempDir()); err == nil {
		t.Error("ModuleFile(an empty directory) returned no error, want one")
	}
}

// at is the position a directive written at one line and column carries.
func at(line, column int) token.Position {
	return token.Position{Filename: modFileName, Line: line, Column: column}
}
