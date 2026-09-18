package deps

import (
	"slices"
	"testing"
)

func TestNoopReplacementsReportsOnlyTheDirectiveOverAModuleTheBuildListLacks(t *testing.T) {
	f, result := targetOf(t, "replacements.txtar")

	got := NoopReplacements(f, result)
	want := []Replacement{{
		Old:  Module{Path: "example.com/absent", Version: "v1.0.0"},
		New:  Module{Path: "example.com/other", Version: "v1.0.0"},
		Site: at(12, 9),
	}}
	if !slices.Equal(got, want) {
		t.Errorf("NoopReplacements(replacements.txtar) = %+v, want %+v", got, want)
	}
}

func TestNoopReplacementsReportsNothingForADirectiveOverAReplacedVersion(t *testing.T) {
	f, result := targetOf(t, "replaced-version.txtar")

	// The build list names a replaced module by the identity the directive
	// replaced, with the replacement recorded beside it, so the version the
	// directive names is the version to look for.
	if got := NoopReplacements(f, result); len(got) != 0 {
		t.Errorf("NoopReplacements(replaced-version.txtar) = %+v, want none", got)
	}
}

func TestNoopReplacementsReadsTheBuildListAtEveryDepth(t *testing.T) {
	f, result := targetOf(t, "transitive-requirement.txtar")

	// Neither directive is a no-op: one names the module a package of the target
	// imports, the other the module only that module's package imports.
	if got := NoopReplacements(f, result); len(got) != 0 {
		t.Errorf("NoopReplacements(transitive-requirement.txtar) = %+v, want none", got)
	}
}

func TestNoopReplacementsReportsNothingWithoutALoad(t *testing.T) {
	f := moduleFileOf(t, extract(t, "replacements.txtar"))

	if got := NoopReplacements(f, nil); len(got) != 0 {
		t.Errorf("NoopReplacements(replacements.txtar, no load) = %+v, want none", got)
	}
}

func TestToolModulesNamesTheLongestDeclaredModuleTheToolsPackageLiesUnder(t *testing.T) {
	f := File{
		Requires: []Requirement{
			{Path: "example.com/gen"},
			{Path: "example.com/gen/nested"},
			{Path: "example.com/other"},
		},
		Tools: []string{"example.com/gen/nested/cmd/gen", "example.com/gen/cmd/gen"},
	}

	got := toolModules(f)
	want := map[string]bool{"example.com/gen": true, "example.com/gen/nested": true}
	if len(got) != len(want) {
		t.Fatalf("toolModules(...) = %v, want %v", got, want)
	}
	for path := range want {
		if !got[path] {
			t.Errorf("toolModules(...) does not name %s, want it named", path)
		}
	}
}

func TestWithinReportsWhetherAPackageLiesInsideAModule(t *testing.T) {
	cases := map[string]struct {
		pkg, path string
		want      bool
	}{
		"the module's own package":       {"example.com/gen", "example.com/gen", true},
		"a package below the module":     {"example.com/gen/cmd/gen", "example.com/gen", true},
		"a module sharing a name prefix": {"example.com/generator/cmd", "example.com/gen", false},
		"an unrelated module":            {"example.com/other", "example.com/gen", false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := within(test.pkg, test.path); got != test.want {
				t.Errorf("within(%q, %q) = %t, want %t", test.pkg, test.path, got, test.want)
			}
		})
	}
}
