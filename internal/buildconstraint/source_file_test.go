package buildconstraint

import "testing"

// IsSourceFile holds for the names go/build opens in a package directory it lists
// and for no other: a Go file, and a C, C++, Objective-C, header, Fortran,
// assembly or SWIG source, never a system object, another extension, or a name an
// underscore or a full stop opens.
func TestIsSourceFileNamesWhatGoBuildOpens(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"x.go": true, "x_test.go": true, "x_windows.go": true,
		"x.c": true, "x.cc": true, "x.cxx": true, "x.cpp": true, "x.m": true,
		"x.h": true, "x.hh": true, "x.hpp": true, "x.hxx": true,
		"x.f": true, "x.F": true, "x.for": true, "x.f90": true,
		"x.s": true, "x.S": true, "x.sx": true, "x.swig": true, "x.swigcxx": true,
		"x.syso": false, "x.txt": false, "x": false, "go.mod": false,
		"_x.go": false, ".x.go": false, "_x.c": false, ".x.c": false, ".c": false, ".go": false,
	} {
		if got := IsSourceFile(name); got != want {
			t.Errorf("IsSourceFile(%q) = %t, want %t", name, got, want)
		}
	}
}
