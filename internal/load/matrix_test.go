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

	results, err := All(t.Context(), fixtureScope(t, "platform"), matrix())
	if err != nil {
		t.Fatalf("All(testdata/platform) = _, %v, want no error", err)
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

	results, err := All(t.Context(), doc, matrix())
	if len(results) != 0 {
		t.Errorf("All(testdata/platformerror) returned %d results, want none: an answer computed from the configurations that did load is more permissive than the truth",
			len(results))
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

	results, err := All(t.Context(), fixtureScope(t, "platform"), nil)
	if !errors.Is(err, ErrNoConfiguration) {
		t.Errorf("All(testdata/platform, no configuration) = _, %v, want %v", err, ErrNoConfiguration)
	}
	if len(results) != 0 {
		t.Errorf("All(testdata/platform, no configuration) returned %d results, want none", len(results))
	}
}
