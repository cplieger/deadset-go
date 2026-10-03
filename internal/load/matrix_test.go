package load

import (
	"context"
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
	doc := unparsedScope(t, "platformerror")

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

	results, unbuilt, err := All(t.Context(), unparsedScope(t, "platformerror"), matrix(), derivedSecond())
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

	results, unbuilt, err := All(t.Context(), unparsedScope(t, "platformerror"), only, derivedSecond())
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

// A derived configuration whose load meets a setup failure is dropped like one that
// does not build, carrying the setup failure, and the run answers over the rest.
func TestAllDropsADerivedConfigurationThatMeetsASetupFailure(t *testing.T) {
	stableToolchain(t)

	results, unbuilt, err := All(t.Context(), unparsedScope(t, "setupfailure"), matrix(), derivedSecond())
	if err != nil {
		t.Fatalf("All(testdata/setupfailure, windows-amd64 derived) = _, _, %v, want no error", err)
	}
	if got, want := configurationIDs(results), []string{"linux-amd64"}; !slices.Equal(got, want) {
		t.Errorf("All(testdata/setupfailure, windows-amd64 derived) returned the configurations %v, want %v", got, want)
	}
	if got, want := unbuiltIDs(unbuilt), []string{"windows-amd64"}; !slices.Equal(got, want) {
		t.Fatalf("All(testdata/setupfailure, windows-amd64 derived) dropped %v, want %v", got, want)
	}
	setup, ok := errors.AsType[*SetupError](unbuilt[0].Err)
	if !ok || len(setup.Failures) == 0 || setup.Failures[0].Class != MissingModule {
		t.Errorf("All(testdata/setupfailure, windows-amd64 derived) dropped windows-amd64 with %v, want a missing-module setup failure",
			unbuilt[0].Err)
	}
}

// A matrix whose every derived configuration was dropped leaves nothing to analyse,
// and a setup failure among the drops ends the run as a declared configuration's
// does, even where a load error was dropped first.
func TestAllEndsAMatrixWhoseEveryConfigurationIsDroppedOnItsSetupFailure(t *testing.T) {
	stableToolchain(t)
	derived := []string{matrix()[0].ID, matrix()[1].ID}

	results, unbuilt, err := All(t.Context(), unparsedScope(t, "everyderiveddropped"), matrix(), derived)
	setup, ok := errors.AsType[*SetupError](err)
	if !ok || len(setup.Failures) == 0 || setup.Failures[0].Class != MissingModule {
		t.Errorf("All(testdata/everyderiveddropped, both derived) = _, _, %v, want the windows-amd64 missing-module setup failure", err)
	}
	if len(results) != 0 || len(unbuilt) != 0 {
		t.Errorf("All(testdata/everyderiveddropped, both derived) returned %d results and dropped %v, want neither: a run with no configuration loaded has nothing to analyse",
			len(results), unbuiltIDs(unbuilt))
	}
}

// A declared configuration whose load meets a setup failure ends the run with it,
// whatever the other configurations load.
func TestAllEndsTheRunOnADeclaredConfigurationsSetupFailure(t *testing.T) {
	stableToolchain(t)

	results, unbuilt, err := All(t.Context(), unparsedScope(t, "setupfailure"), matrix(), nil)
	setup, ok := errors.AsType[*SetupError](err)
	if !ok || len(setup.Failures) == 0 || setup.Failures[0].Class != MissingModule {
		t.Errorf("All(testdata/setupfailure) = _, _, %v, want a missing-module setup failure", err)
	}
	if len(results) != 0 || len(unbuilt) != 0 {
		t.Errorf("All(testdata/setupfailure) returned %d results and dropped %v, want neither: a declared configuration's setup failure ends the run",
			len(results), unbuiltIDs(unbuilt))
	}
}

// A cancelled load is no answer about what the target builds, so a derived
// configuration whose load the cancellation stopped ends the run with the cause
// rather than being dropped while the matrix goes on.
func TestAllEndsTheRunWithTheCauseWhenTheLoadOfADerivedConfigurationIsCancelled(t *testing.T) {
	cause := errors.New("the run was stopped")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	derived := []string{matrix()[0].ID, matrix()[1].ID}

	results, unbuilt, err := All(ctx, fixtureScope(t, "platform"), matrix(), derived)
	if !errors.Is(err, cause) {
		t.Errorf("All(cancelled, every configuration derived) = _, _, %v, want an error wrapping the cause %q", err, cause)
	}
	if slices.Contains(unbuiltIDs(unbuilt), "linux-amd64") {
		t.Errorf("All(cancelled, every configuration derived) dropped %v, want linux-amd64 kept out of the dropped configurations: a cancelled load is not one the target does not build",
			unbuiltIDs(unbuilt))
	}
	if len(results) != 0 {
		t.Errorf("All(cancelled, every configuration derived) returned the configurations %v, want none", configurationIDs(results))
	}
}
