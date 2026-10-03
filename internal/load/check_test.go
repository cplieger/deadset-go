package load

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// reachable returns the package with the given import path among everything a
// result's packages import, or nil.
func reachable(r Result, pkgPath string) *packages.Package {
	var found *packages.Package
	packages.Visit(r.Packages, nil, func(p *packages.Package) {
		if p.PkgPath == pkgPath && found == nil {
			found = p
		}
	})
	return found
}

func TestLoadKeepsNoSyntaxOfADependencyCheckedForItsDeclarations(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, filepath.Join("sigdeps", "app"))

	for _, path := range []string{"example.com/sigdep/plain", "strings"} {
		p := reachable(got, path)
		if p == nil {
			t.Errorf("Load(testdata/sigdeps/app) reaches no %s", path)
			continue
		}
		if p.Syntax != nil || p.TypesInfo != nil {
			t.Errorf("Load(testdata/sigdeps/app) %s: Syntax = %d files, TypesInfo = %v, want neither kept",
				path, len(p.Syntax), p.TypesInfo != nil)
		}
		if p.Types == nil || !p.Types.Complete() {
			t.Errorf("Load(testdata/sigdeps/app) %s: Types = %v, want complete types", path, p.Types)
		}
	}
}

func TestLoadChecksWholeEveryPackageThatImportsTheMainModule(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, filepath.Join("sigdeps", "app"))

	// back is a dependency, and it imports a package of the module being loaded.
	for _, path := range []string{"example.com/sigdeps", "example.com/sigdeps/core", "example.com/sigdep/back"} {
		p := reachable(got, path)
		if p == nil {
			t.Errorf("Load(testdata/sigdeps/app) reaches no %s", path)
			continue
		}
		if len(p.Syntax) == 0 || p.TypesInfo == nil {
			t.Errorf("Load(testdata/sigdeps/app) %s: Syntax = %d files, TypesInfo = %v, want both kept",
				path, len(p.Syntax), p.TypesInfo != nil)
		}
	}
}

func TestLoadKeepsTheColumnOfADependencyDeclaration(t *testing.T) {
	stableToolchain(t)
	got := loadFixture(t, filepath.Join("sigdeps", "app"))

	p := reachable(got, "example.com/sigdep/plain")
	if p == nil {
		t.Fatal("Load(testdata/sigdeps/app) reaches no example.com/sigdep/plain")
	}
	height := p.Types.Scope().Lookup("Height")
	if height == nil {
		t.Fatal("example.com/sigdep/plain declares no Height in its types")
	}
	pos := got.Fset.Position(height.Pos())
	if filepath.Base(pos.Filename) != "plain.go" || pos.Line != 5 || pos.Column != 12 {
		t.Errorf("Height renders at %s through the result's file set, want plain.go:5:12", pos)
	}
}

func TestLoadAcceptsADependencyWhoseOnlyErrorIsInsideAFunctionBody(t *testing.T) {
	stableToolchain(t)
	got, err := Load(t.Context(), fixtureScope(t, filepath.Join("sigdeps", "app")), HostConfiguration())
	if err != nil {
		t.Fatalf("Load(testdata/sigdeps/app) = _, %v, want no error: plain.Broken's error is inside its body", err)
	}
	if p := reachable(got, "example.com/sigdep/plain"); p == nil || len(p.Errors) != 0 {
		t.Errorf("Load(testdata/sigdeps/app) example.com/sigdep/plain = %v, want it loaded with no error", p)
	}
}

func TestLoadRefusesAConfigurationOnItsMetadataBeforeCheckingAnything(t *testing.T) {
	stableToolchain(t)
	c := HostConfiguration()

	got, err := Load(t.Context(), fixtureScope(t, "metadataerror"), c)

	var loadErr *Error
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load(testdata/metadataerror) = _, %v, want a *load.Error", err)
	}
	if got.Packages != nil {
		t.Errorf("Load(testdata/metadataerror) carries %d packages, want none", len(got.Packages))
	}
	// typed.go does not type-check either, and its diagnostic appears only when
	// the load goes on to type-check after the toolchain reported an error.
	names := false
	for _, d := range loadErr.Diagnostics {
		if strings.Contains(d.Position, "typed.go") {
			t.Errorf("Load(testdata/metadataerror) reported %+v, want no type-check diagnostic", d)
		}
		names = names || strings.Contains(d.Message, "example.com/nowhere")
	}
	if !names {
		t.Errorf("Load(testdata/metadataerror) diagnostics = %+v, want one naming example.com/nowhere", loadErr.Diagnostics)
	}
}
