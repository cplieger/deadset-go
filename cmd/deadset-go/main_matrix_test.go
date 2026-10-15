package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/vocab"
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
	if code != vocab.ExitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, vocab.ExitClean)
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
	if code != vocab.ExitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, vocab.ExitClean)
	}

	loaded, err := stagesOf(t.Context(), &resolved)
	var failure *load.Error
	if !errors.As(err, &failure) {
		t.Fatalf("stagesOf(a target declaring the Windows configuration) = %v, want a *load.Error", err)
	}
	if failure.Configuration != unbuildableWindows {
		t.Errorf("stagesOf() named the configuration %q, want %q", failure.Configuration, unbuildableWindows)
	}
	if got := exitCodeFor(err); got != vocab.ExitFailure {
		t.Errorf("exitCodeFor(stagesOf()) = %d, want %d", got, vocab.ExitFailure)
	}
	if len(loaded.identifiers) != 0 || len(loaded.unbuilt) != 0 {
		t.Errorf("stagesOf() answered over %v and dropped %d configurations, want neither: a declared configuration that does not load leaves no answer",
			loaded.identifiers, len(loaded.unbuilt))
	}
}

// The verb names a dropped configuration on stderr, which is the surface a maintainer
// reading the run has, and the report names it in the array the Contract declares
// beside the matrix the analysis ran.
func TestAnalyzeNamesTheDerivedConfigurationItDropped(t *testing.T) {
	dir := unbuildableDerivedModule(t, `{"target": {"kind": "application"}}`)

	run := runAnalyze(t, dir)
	if run.code == vocab.ExitFailure {
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
			t.Errorf("the report names the configuration %q in its matrix, want it absent: the matrix a report carries is the one the analysis ran over",
				one.ID)
		}
	}

	if len(envelope.ConfigurationsNotBuilt) != 1 {
		t.Fatalf("the report names %d configurations the run did not build, want 1",
			len(envelope.ConfigurationsNotBuilt))
	}
	dropped := &envelope.ConfigurationsNotBuilt[0]
	if dropped.ID != unbuildableWindows || dropped.Platform == nil || dropped.Platform.OS != "windows" || dropped.Platform.Arch != runtime.GOARCH {
		t.Errorf("the report names the dropped configuration %q on %+v, want %q on windows/%s",
			dropped.ID, dropped.Platform, unbuildableWindows, runtime.GOARCH)
	}
	if dropped.Platform != nil && len(dropped.Platform.Tags) != 0 {
		t.Errorf("the report names the dropped configuration %q with the tags %v, want none",
			dropped.ID, dropped.Platform.Tags)
	}
	if want := "load " + unbuildableWindows + ": "; !strings.HasPrefix(dropped.Error, want) {
		t.Errorf("the report names the error of %q as %q, want one opening %q",
			dropped.ID, dropped.Error, want)
	}
	if strings.ContainsAny(dropped.Error, "\r\n") || strings.Contains(dropped.Error, dir) {
		t.Errorf("the report names the error of %q as %q, want the load's first line and no host path",
			dropped.ID, dropped.Error)
	}
}

// projectEntries is a build matrix listing compiler configurations alone, declared
// complete: the matrix of another language's analysis, which leaves this one to
// derive its own.
const projectEntries = `{"target": {"kind": "application"}, "analysis": {` +
	`"configurations": [{"id": "tsconfig.json", "project": "tsconfig.json"}], ` +
	`"matrix": {"complete": true}}}`

// A configuration listing project entries alone declares no configuration this
// analysis builds, so the run derives its matrix from the tree as it does with no
// matrix at all, and drops a derived configuration the target does not build rather
// than failing on it.
func TestStagesOfDerivesTheMatrixWhereTheConfigurationListsProjectsAlone(t *testing.T) {
	dir := unbuildableDerivedModule(t, projectEntries)
	resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &strings.Builder{})
	if code != vocab.ExitClean {
		t.Fatalf("Setup: resolve the configuration of %s = %d, want %d", dir, code, vocab.ExitClean)
	}

	loaded, err := stagesOf(t.Context(), &resolved)
	if err != nil {
		t.Fatalf("stagesOf(a target listing project entries alone) = %v, want the stages of the derived matrix", err)
	}
	if want := []string{load.HostConfiguration().ID}; !slices.Equal(loaded.identifiers, want) {
		t.Errorf("stagesOf() analyzed the matrix %v, want the derived matrix the target builds %v",
			loaded.identifiers, want)
	}
	if len(loaded.unbuilt) != 1 || loaded.unbuilt[0].Configuration.ID != unbuildableWindows {
		t.Errorf("stagesOf() dropped %d configurations, want the one derived configuration %q: only a derived configuration is dropped",
			len(loaded.unbuilt), unbuildableWindows)
	}
}

// The report's matrix is the one the analysis ran, so a configuration listing both
// shapes writes its platform entries and no project entry.
func TestAnalyzeReportsThePlatformEntriesOfAMatrixListingBothShapes(t *testing.T) {
	host := load.HostConfiguration()
	dir := unbuildableDerivedModule(t, `{"target": {"kind": "application"}, "analysis": {"configurations": [`+
		`{"id": "tsconfig.json", "project": "tsconfig.json"},`+
		`{"id": "`+host.ID+`", "os": "`+runtime.GOOS+`", "arch": "`+runtime.GOARCH+`"}]}}`)

	run := runAnalyze(t, dir)
	if run.code == vocab.ExitFailure || run.code == vocab.ExitUsage {
		t.Fatalf("analyze(a matrix listing a project entry beside the host platform) = %d, want a verdict: %s",
			run.code, run.stderr)
	}
	envelope := envelopeAt(t, run.reportPath)
	var named []string
	for _, one := range envelope.Configurations {
		named = append(named, one.ID)
	}
	if want := []string{host.ID}; !slices.Equal(named, want) {
		t.Errorf("the report names the matrix %v, want the platform entries the configuration lists %v", named, want)
	}
	if len(envelope.ConfigurationsNotBuilt) != 0 {
		t.Errorf("the report names %d configurations the run did not build, want none: a declared matrix derives nothing",
			len(envelope.ConfigurationsNotBuilt))
	}
}

// setupFailingWindowsModule writes a target whose file names make derivation answer
// a Windows configuration, and whose only Windows file imports a package of the
// target's own module that nothing provides: a setup failure of that configuration
// alone.
func setupFailingWindowsModule(t *testing.T, document string) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() {}\n",
		"app_windows.go": "package main\n\nimport \"example.com/app/gen\"\n\n" +
			"// generated is what the Windows build reads from the generator's output.\nvar generated = gen.Value\n",
		repositoryDocument: document,
	})
}

// A derived configuration's setup failure drops that configuration, which the report
// names with the setup failure's line, and the run answers over the rest.
func TestAnalyzeReportsADerivedConfigurationsSetupFailureAsNotBuilt(t *testing.T) {
	dir := setupFailingWindowsModule(t, `{"target": {"kind": "application"}}`)

	run := runAnalyze(t, dir)
	if run.code == vocab.ExitFailure || run.code == vocab.ExitUsage {
		t.Fatalf("analyze(a target whose derived Windows configuration meets a setup failure) = %d, want a verdict: %s",
			run.code, run.stderr)
	}
	envelope := envelopeAt(t, run.reportPath)
	if len(envelope.ConfigurationsNotBuilt) != 1 {
		t.Fatalf("the report names %d configurations the run did not build, want 1", len(envelope.ConfigurationsNotBuilt))
	}
	dropped := &envelope.ConfigurationsNotBuilt[0]
	if want := "setup failure: missing-module: "; dropped.ID != unbuildableWindows || !strings.HasPrefix(dropped.Error, want) {
		t.Errorf("the report names %q as not built with %q, want %q with an error opening %q",
			dropped.ID, dropped.Error, unbuildableWindows, want)
	}
}

// The same configuration, declared by the maintainer, ends the run with exit code 3
// and the setup failure's line.
func TestAnalyzeEndsTheRunOnADeclaredConfigurationsSetupFailure(t *testing.T) {
	dir := setupFailingWindowsModule(t, `{"target": {"kind": "application"}, "analysis": {"configurations": [`+
		`{"id": "`+load.HostConfiguration().ID+`", "os": "`+runtime.GOOS+`", "arch": "`+runtime.GOARCH+`"},`+
		`{"id": "`+unbuildableWindows+`", "os": "windows", "arch": "`+runtime.GOARCH+`"}]}}`)

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitFailure {
		t.Errorf("analyze(a target declaring a Windows configuration that meets a setup failure) = %d, want %d\nstderr: %s",
			run.code, vocab.ExitFailure, run.stderr)
	}
	if !strings.HasPrefix(run.stderr, "setup failure: missing-module: ") {
		t.Errorf("analyze() wrote to stderr %q, want the setup failure's line", run.stderr)
	}
}

// A method an interface assertion holds under one configuration, and that a file
// constraint removes from another, leaves the other configuration's sweep with a
// fact about a symbol its graph does not hold. That sweep answers over its own
// graph rather than gating an edge the graph shares with the base sweep.
func TestAnalyzeAnswersWhereAnAssertionHoldsAMethodOneConfigurationLacks(t *testing.T) {
	assertion := "\tif c, ok := w.(io.Closer); ok {\n\t\t_ = c.Close()\n\t}\n"
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\n" +
			"func closeOne(w io.Writer) {\n" + assertion + "}\n\n" +
			"func closeOther(w io.Writer) {\n" + assertion + "}\n\n" +
			"func main() {\n\tcloseOne(os.Stdout)\n\tcloseOther(os.Stdout)\n}\n",
		"sys.go": "//go:build !windows\n\npackage main\n\ntype sysWriter struct{}\n\n" +
			"func (sysWriter) Write(p []byte) (int, error) { return len(p), nil }\n\n" +
			"func (sysWriter) Close() error { return nil }\n\n" +
			"func init() { closeOne(sysWriter{}) }\n",
		"app_windows.go":   "package main\n\nfunc windowsOnly() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(a method held under one configuration alone) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	var found []string
	for _, f := range envelopeAt(t, run.reportPath).Findings {
		found = append(found, f.Symbol.Ref)
	}
	if want := []string{"go://example.com/app#windowsOnly"}; !slices.Equal(found, want) {
		t.Errorf("analyze() reported %v, want %v: the assertion holds sysWriter's methods", found, want)
	}
}

// twoModuleTree writes a target that is no module root and holds two modules, a
// requiring b through a replace and carrying a replace over a module nothing loads,
// and b carrying a requirement nothing imports, a package nothing imports whose one
// declaration is the last use of a module, and a file no configuration builds. The
// modules b requires sit in a directory the module walk does not search. The run
// reports every class, because no build links the package nothing imports.
func twoModuleTree(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"a/go.mod": "module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\n" +
			"replace example.com/b => ../b\n\nreplace example.com/absent v1.0.0 => ../absent\n",
		"a/main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/b\"\n)\n\n" +
			"func main() {\n\tb.Used()\n\tvar s fmt.Stringer = b.Named{}\n\tprintln(s.String())\n}\n\nfunc unusedA() {}\n",
		"b/go.mod": "module example.com/b\n\ngo 1.27.1\n\nrequire (\n\texample.com/c v0.0.0\n\texample.com/d v0.0.0\n)\n\n" +
			"replace example.com/c => ../_deps/c\n\nreplace example.com/d => ../_deps/d\n",
		"b/b.go": "package b\n\n// Used is what a calls.\nfunc Used() {}\n\n// Unused is called by nothing.\nfunc Unused() {}\n\n" +
			"// Named is what a converts to fmt.Stringer.\ntype Named struct{}\n\n// String is held by that conversion.\nfunc (Named) String() string { return \"\" }\n",
		"b/never.go":       "//go:build never\n\npackage b\n\nfunc never() {}\n",
		"b/extra/extra.go": "package extra\n\nimport \"example.com/c\"\n\n// Dead is called by nothing.\nfunc Dead() { c.F() }\n",
		"_deps/c/go.mod":   "module example.com/c\n\ngo 1.27.1\n",
		"_deps/c/c.go":     "package c\n\n// F is what Dead calls.\nfunc F() {}\n",
		"_deps/d/go.mod":   "module example.com/d\n\ngo 1.27.1\n",
		"_deps/d/d.go":     "package d\n",
		repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`,
	})
}

// A target that is no module root is every module below it: one report names each
// module's declarations relative to the target, a reference from one module to
// another, or a conversion of one module's type in another, holds the declaration
// it names live, and each module file's directives are read against that module's
// own build list.
func TestAnalyzeReportsEveryModuleUnderATargetThatIsNoModuleRoot(t *testing.T) {
	dir := twoModuleTree(t)

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(a tree of two modules) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	envelope := envelopeAt(t, run.reportPath)
	var found []string
	for _, f := range envelope.Findings {
		found = append(found, string(f.Code)+" "+f.Symbol.Ref+" "+f.Position.Path+" "+strings.Join(f.Details.RemovesLastUseOf, ","))
	}
	want := []string{
		"DS1702 go://example.com/a#example.com/absent@v1.0.0:replace a/go.mod ",
		"DS1002 go://example.com/a#unusedA a/main.go ",
		"DS1001 go://example.com/b#Unused b/b.go ",
		"DS1602 go://example.com/b/extra#extra.go:file b/extra/extra.go ",
		"DS1001 go://example.com/b/extra#Dead b/extra/extra.go example.com/c",
		"DS1701 go://example.com/b#example.com/d:require b/go.mod ",
		"DS1702 go://example.com/b#example.com/d:replace b/go.mod ",
		"DS1002 go://example.com/b#never b/never.go ",
	}
	if !slices.Equal(found, want) {
		t.Errorf("analyze() reported\n%q\nwant\n%q", found, want)
	}
	if envelope.Target.Identity != "example.com/a" || envelope.Target.Root != "." {
		t.Errorf("the report names the target %+v, want root . and the first module's path", envelope.Target)
	}
}

// Two modules of one target can declare one module path, so one import path names
// a package of each. Each module is judged as it is alone: an enumerated type, a
// constant of a type the other module enumerates, a method of a type the other
// module also declares, a package nothing imports and a package only a test imports
// keep the findings they would have alone, every class reported.
func TestAnalyzeJudgesTwoModulesOfOnePathEachAsItIsAlone(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"x/go.mod": "module example\n\ngo 1.27.1\n",
		"x/main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example/extra\"\n\t\"example/util\"\n)\n\n" +
			"type Color int\n\nconst (\n\tRed Color = iota\n\tBlue\n)\n\n" +
			"func (c Color) String() string { return \"c\" }\n\n" +
			"type Mode int\n\nconst (\n\tSlow Mode = iota\n\tQuick\n)\n\n" +
			"type Box struct{}\n\nfunc (Box) Spare() {}\n\n" +
			"func main() {\n\tfmt.Println(Red, Slow, Quick, Box{})\n\tutil.Help()\n\textra.Run()\n}\n",
		"x/util/util.go":   "package util\n\n// Help is what main calls.\nfunc Help() {}\n",
		"x/extra/extra.go": "package extra\n\n// Run is what main calls.\nfunc Run() {}\n",
		"y/go.mod":         "module example\n\ngo 1.27.1\n",
		"y/main.go": "package main\n\ntype Color int\n\nconst (\n\tGreen Color = iota\n\tPink\n)\n\n" +
			"type Mode int\n\nconst Fast Mode = 1\n\ntype Box struct{}\n\nfunc main() { println(int(Green)) }\n",
		"y/main_test.go":   "package main\n\nimport (\n\t\"testing\"\n\n\t\"example/util\"\n)\n\nfunc TestHelp(t *testing.T) { util.Help() }\n",
		"y/util/util.go":   "package util\n\n// Help is what a test calls.\nfunc Help() {}\n",
		"y/extra/extra.go": "package extra\n\n// Run is called by nothing.\nfunc Run() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`,
	})

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(two modules of one path) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	envelope := envelopeAt(t, run.reportPath)
	var found []string
	for _, f := range envelope.Findings {
		found = append(found, string(f.Code)+" "+f.Symbol.Ref+" "+f.Position.Path)
	}
	want := []string{
		"DS1501 go://example#Color x/main.go",
		"DS1402 go://example#Color.String x/main.go",
		"DS1501 go://example#Mode x/main.go",
		"DS1501 go://example#Slow x/main.go",
		"DS1501 go://example#Quick x/main.go",
		"DS1501 go://example#Box x/main.go",
		"DS1101 go://example#Box.Spare x/main.go",
		"DS1602 go://example/extra#extra.go:file y/extra/extra.go",
		"DS1001 go://example/extra#Run y/extra/extra.go",
		"DS1501 go://example#Color y/main.go",
		"DS1501 go://example#Green y/main.go",
		"DS1005 go://example#Pink y/main.go",
		"DS1101 go://example#Mode y/main.go",
		"DS1101 go://example#Fast y/main.go",
		"DS1101 go://example#Box y/main.go",
		"DS1103 go://example#TestHelp y/main_test.go",
		"DS1102 go://example/util#Help y/util/util.go",
	}
	if !slices.Equal(found, want) {
		t.Errorf("analyze() reported\n%v\nwant\n%v", found, want)
	}
	var noted []string
	for _, n := range envelope.Notes {
		noted = append(noted, n.Path)
	}
	if want := []string{"y/extra"}; !slices.Equal(noted, want) {
		t.Errorf("analyze() noted %v, want %v: only y's extra is imported by nothing", noted, want)
	}
}

// A library module and a main module of one module path: the library's exported
// declarations are its published API, whatever the main package of that path is.
func TestAnalyzePublishesALibraryBesideAMainPackageOfItsPath(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"x/go.mod":         "module example\n\ngo 1.27.1\n",
		"x/main.go":        "package main\n\nfunc helper() {}\n\nfunc main() { helper() }\n",
		"y/go.mod":         "module example\n\ngo 1.27.1\n",
		"y/lib.go":         "package example\n\n// Exported is published.\nfunc Exported() { internalOnly() }\n\nfunc internalOnly() {}\n\nfunc unused() {}\n",
		"y/lib_test.go":    "package example\n\nimport \"testing\"\n\nfunc TestExported(t *testing.T) { Exported() }\n",
		repositoryDocument: `{"target": {"kind": "library"}}`,
	})

	analyzed := runAnalyze(t, dir)
	if analyzed.code != vocab.ExitFindings {
		t.Fatalf("analyze(a library beside a main package of its path) = %d, want %d: %s", analyzed.code, vocab.ExitFindings, analyzed.stderr)
	}
	var found []string
	for _, f := range envelopeAt(t, analyzed.reportPath).Findings {
		found = append(found, string(f.Code)+" "+f.Symbol.Ref+" "+f.Position.Path)
	}
	if want := []string{"DS1002 go://example#unused y/lib.go"}; !slices.Equal(found, want) {
		t.Errorf("analyze() reported %v, want %v", found, want)
	}

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"print-roots", "--target=."}, &stdout, &stderr); code != vocab.ExitClean {
		t.Fatalf("print-roots = %d, want %d: %s", code, vocab.ExitClean, stderr.String())
	}
	want := []string{"go://example#main\tmain", "go://example#Exported\tpublished-api", "go://example#TestExported\ttest"}
	if got := strings.Split(strings.TrimSpace(stdout.String()), "\n"); !slices.Equal(got, want) {
		t.Errorf("print-roots printed %q, want %q", got, want)
	}
}

// Each module of a target reads its imports anew, so the standard library a second
// module sees is a reading of its own: an interface of it fixes the second module's
// method and an assertion in it retains the second module's method, as for the first.
func TestAnalyzeJudgesEachModuleAgainstItsOwnReadingOfItsImports(t *testing.T) {
	source := "package main\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype T struct{}\n\n" +
		"func (T) WriteTo(w io.Writer) (int64, error) { return 0, nil }\n\ntype R struct{}\n\n" +
		"func (R) Read([]byte) (int, error) { return 0, io.EOF }\n\n" +
		"func (R) WriteTo(io.Writer) (int64, error) { return 0, nil }\n\n" +
		"func main() {\n\t_, _ = T{}.WriteTo(nil)\n\tvar r io.Reader = R{}\n\t_, _ = io.Copy(os.Stdout, r)\n}\n"
	dir := writeModule(t, map[string]string{
		"x/go.mod":         "module example.com/x\n\ngo 1.27.1\n",
		"x/main.go":        source,
		"y/go.mod":         "module example.com/y\n\ngo 1.27.1\n",
		"y/main.go":        source,
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitClean {
		t.Fatalf("analyze(two modules reading one standard library) = %d, want %d: %s", run.code, vocab.ExitClean, run.stderr)
	}
	var found []string
	for _, f := range envelopeAt(t, run.reportPath).Findings {
		found = append(found, string(f.Code)+" "+f.Symbol.Ref)
	}
	want := []string{
		"DS1501 go://example.com/x#T", "DS1501 go://example.com/x#R",
		"DS1501 go://example.com/y#T", "DS1501 go://example.com/y#R",
	}
	if !slices.Equal(found, want) {
		t.Errorf("analyze() reported %v, want %v", found, want)
	}
	if got := retainedIn(t, dir); !slices.Contains(got, "go://example.com/y#R.WriteTo\ty/main.go:16:10\tinterface-satisfaction\ty/main.go:20:20\tsatisfies what io asserts through io.Reader") {
		t.Errorf("print-retained printed %q, want y's R.WriteTo retained by what io asserts", got)
	}
}

// workspaceTree writes a module root whose workspace also lists a module below it,
// which the root's main package calls.
func workspaceTree(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.work":                   "go 1.27.1\n\nuse (\n\t.\n\t./sub\n)\n",
		"go.mod":                    "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/sub v0.0.0\n",
		"main.go":                   "package main\n\nimport \"example.com/sub\"\n\nfunc main() { sub.Used() }\n",
		"sub/go.mod":                "module example.com/sub\n\ngo 1.27.1\n",
		"sub/sub.go":                "package sub\n\n// Used is what main calls.\nfunc Used() {}\n\n// Unused is called by nothing.\nfunc Unused() {}\n",
		repositoryDocument:          `{"target": {"kind": "application"}}`,
		"sub/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	})
}

// A module the workspace listing the target lists below the target is a module of
// the target: its declarations are judged with the target's, rather than its
// references counted as a consumer's.
func TestAnalyzeReportsAWorkspaceMemberBelowTheTarget(t *testing.T) {
	dir := workspaceTree(t)

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(a workspace with a member below the target) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	envelope := envelopeAt(t, run.reportPath)
	var found []string
	for _, f := range envelope.Findings {
		found = append(found, f.Symbol.Ref+" "+f.Position.Path)
	}
	if want := []string{"go://example.com/sub#Unused sub/sub.go"}; !slices.Equal(found, want) {
		t.Errorf("analyze() reported %v, want %v", found, want)
	}
	if envelope.Target.Identity != "example.com/app" || envelope.Consumers.Declared != 0 {
		t.Errorf("the report names the target %+v and %d consumers, want example.com/app and none",
			envelope.Target, envelope.Consumers.Declared)
	}
}

// A workspace member run on its own, from its own directory, builds through the
// workspace above it and names no path outside the run directory: the other member
// there is no consumer the report could name.
func TestAnalyzeAWorkspaceMemberFromItsOwnDirectory(t *testing.T) {
	dir := filepath.Join(workspaceTree(t), "sub")

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(a workspace member from its own directory) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	envelope := envelopeAt(t, run.reportPath)
	var found []string
	for _, f := range envelope.Findings {
		found = append(found, f.Symbol.Ref+" "+f.Position.Path)
	}
	if !slices.Contains(found, "go://example.com/sub#Unused sub.go") {
		t.Errorf("analyze() reported %v, want sub#Unused at sub.go", found)
	}
	if envelope.Target.Root != "." || envelope.Target.Identity != "example.com/sub" || envelope.Consumers.Declared != 0 {
		t.Errorf("the report names the target %+v and %d consumers, want root ., example.com/sub and none",
			envelope.Target, envelope.Consumers.Declared)
	}
}

// retainedIn is what print-retained prints for the target at dir, one line each.
func retainedIn(t *testing.T, dir string) []string {
	t.Helper()

	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"print-retained", "--target=."}, &stdout, &stderr); code != vocab.ExitClean {
		t.Fatalf("print-retained = %d, want %d: %s", code, vocab.ExitClean, stderr.String())
	}
	return strings.Split(strings.TrimSpace(stdout.String()), "\n")
}

// A type each of two modules of one module path declares, spelled alike by both,
// keeps the methods each module's own conversions retain.
func TestRetainsForTwoModulesOfOnePathEachTypeItsOwnMethods(t *testing.T) {
	failing := "package main\n\nimport (\n\t\"fmt\"\n\t\"io\"\n\t\"net/http\"\n\t\"os\"\n)\n\ntype E struct{}\n\n" +
		"func (E) Error() string { return \"\" }\n\nfunc (E) Timeout() bool { return true }\n\n" +
		"func (E) Temporary() bool { return true }\n\ntype R struct{}\n\n" +
		"func (R) Read([]byte) (int, error) { return 0, io.EOF }\n\n" +
		"func (R) WriteTo(io.Writer) (int64, error) { return 0, nil }\n\n" +
		"func main() {\n\tvar err error = E{}\n\tvar r io.Reader = R{}\n\tfmt.Println(err)\n\t_, _ = io.Copy(os.Stdout, r)\n\t_ = http.ErrAbortHandler\n}\n"
	dir := writeModule(t, map[string]string{
		"x/go.mod":         "module example\n\ngo 1.27.1\n",
		"x/main.go":        failing,
		"y/go.mod":         "module example\n\ngo 1.27.1\n",
		"y/main.go":        failing,
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	var want []string
	for _, module := range []string{"x", "y"} {
		file := module + "/main.go:"
		at := "\tinterface-satisfaction\t" + file
		want = append(want,
			"go://example#E.Error\t"+file+"12:10"+at+"25:18\tsatisfies error",
			"go://example#E.Timeout\t"+file+"14:10"+at+"25:18\tsatisfies what net asserts through error as Error",
			"go://example#E.Temporary\t"+file+"16:10"+at+"25:18\tsatisfies what net asserts through error as Error",
			"go://example#R.Read\t"+file+"20:10"+at+"26:20\tsatisfies io.Reader",
			"go://example#R.WriteTo\t"+file+"22:10"+at+"26:20\tsatisfies what io asserts through io.Reader",
		)
	}
	if got := retainedIn(t, dir); !slices.Equal(got, want) {
		t.Errorf("print-retained printed\n%q\nwant\n%q", got, want)
	}
}

// A published type of each of two library modules of one module path is held by an
// outside program, each type spelled alike. An outside program may import several
// modules of the target, so a published parameter of one holds the other's type too.
func TestRetainsAPublishedTypeBesideAnotherModuleOfItsPath(t *testing.T) {
	published := "package example\n\nimport \"fmt\"\n\n// T is published.\ntype T struct{}\n\n" +
		"func (T) String() string { return \"\" }\n\n// NewT is published.\nfunc NewT() T { return T{} }\n\n" +
		"// Print is published.\nfunc Print(s fmt.Stringer) { fmt.Println(s) }\n"
	dir := writeModule(t, map[string]string{
		"x/go.mod":         "module example\n\ngo 1.27.1\n",
		"x/x.go":           published,
		"y/go.mod":         "module example\n\ngo 1.27.1\n",
		"y/y.go":           published,
		repositoryDocument: `{"target": {"kind": "library"}}`,
	})

	got := retainedIn(t, dir)
	if len(got) != 2 {
		t.Fatalf("print-retained printed %q, want one T.String of each module", got)
	}
	for i, site := range []string{"x/x.go:14:12", "y/y.go:14:12"} {
		if !strings.HasPrefix(got[i], "go://example#T.String\t") || !strings.Contains(got[i], "\t"+site+"\tsatisfies fmt.Stringer") {
			t.Errorf("print-retained printed %q, want T.String retained at %s", got[i], site)
		}
	}
}

// Under a complete matrix a file of the second module that no configuration builds
// is reported at its path under the target root.
func TestAnalyzeReportsANeverBuiltFileOfTheSecondModule(t *testing.T) {
	dir := twoModuleTree(t)
	host := load.HostConfiguration()
	writeDocument(t, dir, repositoryDocument, `{"target": {"kind": "application"}, "analysis": {"configurations": [`+
		`{"id": "`+host.ID+`", "os": "`+runtime.GOOS+`", "arch": "`+runtime.GOARCH+`"}], "matrix": {"complete": true}}}`)

	run := runAnalyze(t, dir)
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(a tree of two modules under a complete matrix) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	var found []string
	for _, f := range envelopeAt(t, run.reportPath).Findings {
		if f.Code == "DS1601" {
			found = append(found, f.Symbol.Ref+" "+f.Position.Path)
		}
	}
	if want := []string{"go://example.com/b#never.go:file b/never.go"}; !slices.Equal(found, want) {
		t.Errorf("analyze() reported the never-built files %v, want %v", found, want)
	}
}

// The scope's name for a target that no module is rooted at is the identity the
// report carries, so a report of another language over the same target names it
// alike.
func TestAnalyzeNamesATargetNoModuleIsRootedAtAsTheScopeDoes(t *testing.T) {
	dir := twoModuleTree(t)
	writeDocument(t, dir, "scope.json", `{"target": {"id": "whole", "path": "."}}`)

	run := runAnalyze(t, dir, "--scope="+filepath.Join(dir, "scope.json"))
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(a scoped tree of two modules) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	if got := envelopeAt(t, run.reportPath).Target.Identity; got != "whole" {
		t.Errorf("the report names the target %q, want the scope's name %q", got, "whole")
	}
}

// One module of the target can build another of its modules from a copy outside the
// target, as a requirement the module cache resolves does: the copy shares the
// package paths and declares nothing of the target, so the run answers and a
// declaration only the copy's callers name is reported.
func TestAnalyzeReadsAModuleTheTargetAlsoBuildsFromACopyOutsideIt(t *testing.T) {
	root := writeModule(t, map[string]string{
		"target/a/go.mod":              "module example.com/a\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../../copy/b\n",
		"target/a/main.go":             "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.Used() }\n",
		"target/b/go.mod":              "module example.com/b\n\ngo 1.27.1\n",
		"target/b/b.go":                "package b\n\n// Used is what the copy's callers call.\nfunc Used() {}\n",
		"copy/b/go.mod":                "module example.com/b\n\ngo 1.27.1\n",
		"copy/b/b.go":                  "package b\n\n// Used is what a calls.\nfunc Used() {}\n",
		"target/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	run := runAnalyze(t, filepath.Join(root, "target"))
	if run.code != vocab.ExitFindings {
		t.Fatalf("analyze(a module built from a copy outside the target) = %d, want %d: %s", run.code, vocab.ExitFindings, run.stderr)
	}
	var found []string
	for _, f := range envelopeAt(t, run.reportPath).Findings {
		found = append(found, f.Symbol.Ref)
	}
	if want := []string{"go://example.com/b#Used"}; !slices.Equal(found, want) {
		t.Errorf("analyze() reported %v, want %v: a calls the copy's Used", found, want)
	}
}
