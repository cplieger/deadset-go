package deps

import (
	"errors"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
)

func TestUnusedRequirementsReportsADirectRequirementNoImportNeeds(t *testing.T) {
	f, result := targetOf(t, "unused-requirement.txtar")

	got := UnusedRequirements(f, result)
	want := []Requirement{{Path: "example.com/unused", Version: "v1.0.0", Site: at(6, 2)}}
	if !slices.Equal(got, want) {
		t.Errorf("UnusedRequirements(unused-requirement.txtar) = %+v, want %+v", got, want)
	}
}

func TestUnusedRequirementsEqualsWhatTidyingRemoves(t *testing.T) {
	f, result := targetOf(t, "unused-requirement.txtar")

	got := requirementPaths(UnusedRequirements(f, result))
	slices.Sort(got)
	if want := tidyRemoved(t, "unused-requirement.txtar"); !slices.Equal(got, want) {
		t.Errorf("UnusedRequirements(unused-requirement.txtar) names %v, want %v, the requirements tidying removes",
			got, want)
	}
}

func TestUnusedRequirementsReportsNothingWhereEveryRequirementIsImported(t *testing.T) {
	f, result := targetOf(t, "used-requirements.txtar")

	// One of the two is imported by an external test package alone, which is a
	// package of the target like any other.
	if got := UnusedRequirements(f, result); len(got) != 0 {
		t.Errorf("UnusedRequirements(used-requirements.txtar) = %+v, want none", got)
	}
}

func TestUnusedRequirementsReportsNoIndirectRequirement(t *testing.T) {
	f, result := targetOf(t, "indirect-requirements.txtar")

	indirect := 0
	for _, require := range f.Requires {
		if require.Indirect {
			indirect++
		}
	}
	if indirect != 2 {
		t.Fatalf("ModuleFile(indirect-requirements.txtar) holds %d indirect requirements, want 2", indirect)
	}
	if got := UnusedRequirements(f, result); len(got) != 0 {
		t.Errorf("UnusedRequirements(indirect-requirements.txtar) = %+v, want none, because no import needs either module",
			got)
	}
}

func TestUnusedRequirementsReportsARequirementOnlyAnotherModuleNeeds(t *testing.T) {
	f, result := targetOf(t, "transitive-requirement.txtar")

	// The module provides a package the middle module imports and no package the
	// target imports, so the direct declaration is not backed by an import. Marking
	// it indirect is what makes the file honest; deleting it would change the
	// build list, which is why the kind's fix is not a deletion.
	got := requirementPaths(UnusedRequirements(f, result))
	if want := []string{"example.com/leaf"}; !slices.Equal(got, want) {
		t.Errorf("UnusedRequirements(transitive-requirement.txtar) names %v, want %v", got, want)
	}
	if removed := tidyRemoved(t, "transitive-requirement.txtar"); len(removed) != 0 {
		t.Errorf("tidying transitive-requirement.txtar removed %v, want none, because the module is still needed", removed)
	}
}

func TestUnusedRequirementsReportsARequirementOnlyAToolDirectiveNeeds(t *testing.T) {
	f, result := targetOf(t, "replacements.txtar")

	// A tool directive is not an import, so the requirement that carries its
	// package is a direct declaration no import needs.
	got := requirementPaths(UnusedRequirements(f, result))
	if want := []string{"example.com/gen"}; !slices.Equal(got, want) {
		t.Errorf("UnusedRequirements(replacements.txtar) names %v, want %v", got, want)
	}
}

func TestUnusedRequirementsReportsNothingWithoutALoad(t *testing.T) {
	f := moduleFileOf(t, extract(t, "unused-requirement.txtar"))

	if got := UnusedRequirements(f, nil); len(got) != 0 {
		t.Errorf("UnusedRequirements(unused-requirement.txtar, no load) = %+v, want none", got)
	}
}

func TestTheLoadFailsBeforeADependencyAnswerWhereAnImportResolvesToNoModule(t *testing.T) {
	cases := map[string]string{
		"a module the file does not require": "missing-requirement.txtar",
		"a package the module does not hold": "unresolvable-import.txtar",
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			dir := extract(t, archive)
			doc, err := scope.ForDir(dir)
			if err != nil {
				t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
			}

			_, err = load.Load(t.Context(), doc, load.Configuration{
				ID: fixtureOS + "-" + fixtureArch, OS: fixtureOS, Arch: fixtureArch,
			})
			var diagnosed *load.Error
			if !errors.As(err, &diagnosed) {
				t.Fatalf("load.Load(%s) error = %v, want the load's own diagnostics", archive, err)
			}
			if len(diagnosed.Diagnostics) == 0 {
				t.Errorf("load.Load(%s) returned no diagnostic, want the compile error", archive)
			}
		})
	}
}
