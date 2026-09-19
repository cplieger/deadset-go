package load

import (
	"errors"
	"slices"
	"testing"
)

// matrix is the two configurations the platform fixtures are loaded under, in the
// order a matrix lists them.
func matrix() []Configuration {
	return []Configuration{
		{ID: "linux-amd64", OS: "linux", Arch: "amd64"},
		{ID: "windows-amd64", OS: "windows", Arch: "amd64"},
	}
}

// derivedSecond names the second configuration of the matrix as one the analyzer
// derived from the target tree rather than read from a configuration document, which
// is what makes it droppable.
func derivedSecond() []string {
	return []string{matrix()[1].ID}
}

// unbuiltIDs names the configuration of every dropped entry, in the order given.
func unbuiltIDs(dropped []Unbuilt) []string {
	ids := make([]string, 0, len(dropped))
	for _, one := range dropped {
		ids = append(ids, one.Configuration.ID)
	}
	return ids
}

// configurationIDs names the configuration of every result, in the order given.
func configurationIDs(results []Result) []string {
	ids := make([]string, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.Configuration.ID)
	}
	return ids
}

func TestAllResolvesEveryConfigurationInTheOrderTheMatrixLists(t *testing.T) {
	stableToolchain(t)

	results, unbuilt, err := All(t.Context(), fixtureScope(t, "platform"), matrix(), nil)
	if err != nil {
		t.Fatalf("All(testdata/platform) = _, _, %v, want no error", err)
	}
	if len(unbuilt) != 0 {
		t.Errorf("All(testdata/platform) dropped %v, want nothing: the target builds every configuration",
			unbuiltIDs(unbuilt))
	}

	want := []string{"linux-amd64", "windows-amd64"}
	if got := configurationIDs(results); !slices.Equal(got, want) {
		t.Errorf("All(testdata/platform) returned the configurations %v, want %v", got, want)
	}
	// Each configuration selects one of the two constrained files, so the results
	// are genuinely different sets of source rather than one load repeated.
	for i, r := range results {
		names := baseNames(findPackage(r, "example.com/platform").CompiledGoFiles)
		selected := []string{"app.go", "render_" + matrix()[i].OS + ".go"}
		if !slices.Equal(names, selected) {
			t.Errorf("All(testdata/platform)[%s] compiled %v, want %v", r.Configuration.ID, names, selected)
		}
	}
}

func TestAllStopsAtTheConfigurationThatDoesNotLoadAndReturnsNoResult(t *testing.T) {
	stableToolchain(t)
	doc := fixtureScope(t, "platformerror")

	// The premise: the first configuration of the matrix loads on its own, so the
	// refusal below is the second configuration's and not the fixture's.
	if _, err := Load(t.Context(), doc, matrix()[0]); err != nil {
		t.Fatalf("Load(testdata/platformerror, linux-amd64) = _, %v, want no error", err)
	}

	results, unbuilt, err := All(t.Context(), doc, matrix(), nil)
	if len(results) != 0 {
		t.Errorf("All(testdata/platformerror) returned %d results, want none: an answer computed from the configurations that did load is more permissive than the truth",
			len(results))
	}
	if len(unbuilt) != 0 {
		t.Errorf("All(testdata/platformerror) dropped %v, want nothing: a configuration a maintainer declared fails the run rather than being dropped",
			unbuiltIDs(unbuilt))
	}

	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("All(testdata/platformerror) = _, %v, want a *load.Error", err)
	}
	if failure.Configuration != "windows-amd64" {
		t.Errorf("All(testdata/platformerror) named the configuration %q, want %q", failure.Configuration, "windows-amd64")
	}
	if len(failure.Diagnostics) == 0 {
		t.Errorf("All(testdata/platformerror) returned %d diagnostics, want at least one", len(failure.Diagnostics))
	}
}

func TestAllRefusesAMatrixHoldingNoConfiguration(t *testing.T) {
	stableToolchain(t)

	results, unbuilt, err := All(t.Context(), fixtureScope(t, "platform"), nil, nil)
	if !errors.Is(err, ErrNoConfiguration) {
		t.Errorf("All(testdata/platform, no configuration) = _, _, %v, want %v", err, ErrNoConfiguration)
	}
	if len(results) != 0 || len(unbuilt) != 0 {
		t.Errorf("All(testdata/platform, no configuration) returned %d results and %d dropped, want none of either",
			len(results), len(unbuilt))
	}
}

// A configuration the analyzer derived is its own answer about what the target
// builds, so one the target does not build is dropped and reported rather than
// ending the run: the run answers over the configurations the target does build.
func TestAllDropsADerivedConfigurationTheTargetDoesNotBuild(t *testing.T) {
	stableToolchain(t)

	results, unbuilt, err := All(t.Context(), fixtureScope(t, "platformerror"), matrix(), derivedSecond())
	if err != nil {
		t.Fatalf("All(testdata/platformerror, windows-amd64 derived) = _, _, %v, want no error", err)
	}
	if got, want := configurationIDs(results), []string{"linux-amd64"}; !slices.Equal(got, want) {
		t.Errorf("All(testdata/platformerror, windows-amd64 derived) returned the configurations %v, want %v",
			got, want)
	}
	if got, want := unbuiltIDs(unbuilt), []string{"windows-amd64"}; !slices.Equal(got, want) {
		t.Fatalf("All(testdata/platformerror, windows-amd64 derived) dropped %v, want %v", got, want)
	}

	var failure *Error
	if !errors.As(unbuilt[0].Err, &failure) {
		t.Fatalf("All(testdata/platformerror, windows-amd64 derived) dropped windows-amd64 with %v, want a *load.Error",
			unbuilt[0].Err)
	}
	if failure.Configuration != "windows-amd64" || len(failure.Diagnostics) == 0 {
		t.Errorf("All(testdata/platformerror, windows-amd64 derived) dropped windows-amd64 naming configuration %q with %d diagnostics, want %q with at least one",
			failure.Configuration, len(failure.Diagnostics), "windows-amd64")
	}
}

// A matrix every configuration of which was dropped leaves nothing to analyse, so
// the first dropped configuration's error is the run's.
func TestAllRefusesAMatrixWhoseEveryConfigurationIsDropped(t *testing.T) {
	stableToolchain(t)
	only := []Configuration{matrix()[1]}

	results, unbuilt, err := All(t.Context(), fixtureScope(t, "platformerror"), only, derivedSecond())
	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("All(testdata/platformerror, windows-amd64 derived alone) = _, _, %v, want a *load.Error", err)
	}
	if failure.Configuration != "windows-amd64" {
		t.Errorf("All(testdata/platformerror, windows-amd64 derived alone) named the configuration %q, want %q",
			failure.Configuration, "windows-amd64")
	}
	if len(results) != 0 || len(unbuilt) != 0 {
		t.Errorf("All(testdata/platformerror, windows-amd64 derived alone) returned %d results and %d dropped, want none of either: a run with no configuration loaded has nothing to analyse",
			len(results), len(unbuilt))
	}
}
