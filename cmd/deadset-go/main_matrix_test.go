package main

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
)

// unbuildableWindows is the identifier of the configuration the fixture below
// derives and the target does not build. Derivation pairs a platform atom with the
// host's other axis, so the architecture is the host's.
var unbuildableWindows = "windows-" + runtime.GOARCH

// unbuildableDerivedModule writes the fixture both matrix tests are driven against: a
// target whose file names make derivation answer a Windows configuration, and a
// dependency, reached through a replace so nothing is fetched, whose only file a
// constraint excludes there.
//
// The host's configuration builds the whole program; the derived one has no file of
// the dependency's package at all, which is the load error that drops it.
func unbuildableDerivedModule(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n\n" +
			"require example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n",
		"app.go": "package main\n\nimport \"example.com/dep\"\n\n" +
			"func main() { dep.Used() }\n",
		"app_windows.go":   "package main\n\n// windowsOnly is declared where the guessed configuration would build.\nfunc windowsOnly() {}\n",
		"dep/go.mod":       "module example.com/dep\n\ngo 1.27.1\n",
		"dep/dep.go":       "//go:build !windows\n\npackage dep\n\n// Used is what the entry point calls.\nfunc Used() {}\n",
		repositoryDocument: document,
	})
}

// A configuration the derivation answered that the target does not build is dropped
// from the matrix and returned for the verb to name, so the run answers over the
// configurations the target does build instead of having no answer at all.
func TestStagesOfDropsADerivedConfigurationTheTargetDoesNotBuild(t *testing.T) {
	dir := unbuildableDerivedModule(t, `{"target": {"kind": "application"}}`)
	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, exitClean)
	}

	loaded, err := stagesOf(t.Context(), &resolved)
	if err != nil {
		t.Fatalf("stagesOf(a target whose derived Windows configuration does not build) = %v, want the stages of the run", err)
	}
	if want := []string{load.HostConfiguration().ID}; !slices.Equal(loaded.identifiers, want) {
		t.Errorf("stagesOf() analyzed the matrix %v, want %v: the matrix of the run is what the target builds",
			loaded.identifiers, want)
	}

	if len(loaded.unbuilt) != 1 {
		t.Fatalf("stagesOf() dropped %d configurations, want 1", len(loaded.unbuilt))
	}
	dropped := &loaded.unbuilt[0]
	if dropped.Configuration.ID != unbuildableWindows {
		t.Errorf("stagesOf() dropped %q, want %q", dropped.Configuration.ID, unbuildableWindows)
	}
	var failure *load.Error
	if !errors.As(dropped.Err, &failure) {
		t.Fatalf("stagesOf() dropped %q with %v, want a *load.Error", dropped.Configuration.ID, dropped.Err)
	}
	if len(failure.Diagnostics) == 0 {
		t.Errorf("stagesOf() dropped %q with no diagnostic, want the load's own list", dropped.Configuration.ID)
	}
}

// The same configuration, declared by the maintainer rather than derived, fails the
// run: the declaration asserts that the target builds it, and an answer computed
// without it would report what it uses.
func TestStagesOfFailsTheRunOnADeclaredConfigurationTheTargetDoesNotBuild(t *testing.T) {
	dir := unbuildableDerivedModule(t, `{"target": {"kind": "application"}, "analysis": {"configurations": [`+
		`{"id": "`+load.HostConfiguration().ID+`", "os": "`+runtime.GOOS+`", "arch": "`+runtime.GOARCH+`"},`+
		`{"id": "`+unbuildableWindows+`", "os": "windows", "arch": "`+runtime.GOARCH+`"}]}}`)
	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != exitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, exitClean)
	}

	loaded, err := stagesOf(t.Context(), &resolved)
	var failure *load.Error
	if !errors.As(err, &failure) {
		t.Fatalf("stagesOf(a target declaring the Windows configuration) = %v, want a *load.Error", err)
	}
	if failure.Configuration != unbuildableWindows {
		t.Errorf("stagesOf() named the configuration %q, want %q", failure.Configuration, unbuildableWindows)
	}
	if got := exitCodeFor(err); got != exitFailure {
		t.Errorf("exitCodeFor(stagesOf()) = %d, want %d", got, exitFailure)
	}
	if len(loaded.identifiers) != 0 || len(loaded.unbuilt) != 0 {
		t.Errorf("stagesOf() answered over %v and dropped %d configurations, want neither: a declared configuration that does not load leaves no answer",
			loaded.identifiers, len(loaded.unbuilt))
	}
}

// The verb names a dropped configuration on stderr, which is where the run accounts
// for it: the report's matrix is the one the analysis ran, so a configuration the run
// did not build has no entry in it.
func TestAnalyzeNamesTheDerivedConfigurationItDropped(t *testing.T) {
	dir := unbuildableDerivedModule(t, `{"target": {"kind": "application"}}`)

	run := runAnalyze(t, dir)
	if run.code == exitFailure {
		t.Fatalf("analyze(a target whose derived Windows configuration does not build) = %d, want a verdict rather than a load failure: %s",
			run.code, run.stderr)
	}
	for _, want := range []string{unbuildableWindows, "derived from the target tree", "dropped from the matrix"} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("analyze() wrote to stderr\n%s\nwant a line naming %q", run.stderr, want)
		}
	}

	envelope := envelopeAt(t, run.reportPath)
	for _, one := range envelope.Configurations {
		if one.ID == unbuildableWindows {
			t.Errorf("the report names the configuration %q, want it absent: the matrix a report carries is the one the analysis ran over",
				one.ID)
		}
	}
}
