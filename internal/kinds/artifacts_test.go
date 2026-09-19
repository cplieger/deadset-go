package kinds

import (
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/deps"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

// The two configurations the never-built fixture is analyzed under, neither of
// which names the systems its excluded files name.
func twoConfigurations() []load.Configuration {
	return []load.Configuration{
		{ID: "linux-amd64", OS: "linux", Arch: "amd64"},
		{ID: "darwin-arm64", OS: "darwin", Arch: "arm64"},
	}
}

// completeMatrixConfig is the resolved configuration of a target that lists its
// build configurations and declares the matrix complete, which is the one
// declaration that opens the never-built kind.
func completeMatrixConfig() config.Config {
	resolved := listedMatrixConfig()
	resolved.Analysis.Matrix.Complete = true
	return resolved
}

// listedMatrixConfig is the resolved configuration of a target that lists the
// build configurations it is analyzed under and declares nothing about the matrix
// being complete.
func listedMatrixConfig() config.Config {
	resolved := applicationConfig()
	for _, c := range twoConfigurations() {
		resolved.Analysis.Configurations = append(resolved.Analysis.Configurations,
			config.Configuration{ID: c.ID, OS: c.OS, Arch: c.Arch})
	}
	return resolved
}

// declaredCompleteWithoutConfigurations is the resolved configuration of a target
// that declares the matrix complete and lists no configuration of its own, so the
// matrix the declaration speaks of is one the run derived.
func declaredCompleteWithoutConfigurations() config.Config {
	resolved := applicationConfig()
	resolved.Analysis.Matrix.Complete = true
	return resolved
}

// withModuleFile is one fixture's input with the target's module file read, which
// is what the composition root fills for the two dependency kinds.
func withModuleFile(t *testing.T, archive string, resolved config.Config,
	configurations ...load.Configuration,
) *Input {
	t.Helper()

	dir := extract(t, archive)
	in := inputOfDir(t, dir, resolved, Consumers{}, configurations...)
	file, err := deps.ModuleFile(t.Context(), dir)
	if err != nil {
		t.Fatalf("Setup: deps.ModuleFile(%s): %v", dir, err)
	}
	in.Deps = &file
	return in
}

// emitted is what one kind reports about one input, through the framework, because no
// emitter completes its own finding: a pass over a table holding that one kind is
// what a kind's own test measures. It fails the test where the pass refuses, because
// a refusal is a defect in the emitter rather than an answer.
func emitted(t *testing.T, name, code string, emit Emitter, in *Input) []Finding {
	t.Helper()

	result, err := Compute(in, map[string]Emitter{code: emit})
	if err != nil {
		t.Fatalf("%s() = error %v, want the findings of the kind", name, err)
	}
	return result.Findings
}

func TestFileNeverBuiltReportsEveryFileNoConfigurationCompiles(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-built.txtar", completeMatrixConfig(), Consumers{},
		twoConfigurations()...)

	found := emitted(t, "FileNeverBuilt", fileNeverBuiltCode, FileNeverBuilt, in)
	if slices.ContainsFunc(found, func(one Finding) bool { return one.Symbol.Name == "paired_darwin.go" }) {
		t.Errorf("FileNeverBuilt reports %v, want no finding about paired_darwin.go: a file one configuration of the matrix compiles is built",
			summary(found))
	}
	want := map[string]string{
		"ninth.go":          "plan9",
		"sized_windows.go":  "windows",
		"stated_windows.go": "windows",
	}
	if len(found) != len(want) {
		t.Fatalf("FileNeverBuilt(a target with two files no configuration compiles) reports %v, want %v",
			summary(found), want)
	}
	for i := range found {
		one := &found[i]
		constraint, named := want[one.Symbol.Name]
		if !named {
			t.Errorf("FileNeverBuilt reports %s, want one of %v", one.Symbol.Name, want)
			continue
		}
		if one.Details.ExcludedBy != constraint {
			t.Errorf("FileNeverBuilt(%s).Details.ExcludedBy = %q, want %q",
				one.Symbol.Name, one.Details.ExcludedBy, constraint)
		}
		if one.Symbol.Kind != symbolKinds[graph.KindFile] {
			t.Errorf("FileNeverBuilt(%s).Symbol.Kind = %q, want %q",
				one.Symbol.Name, one.Symbol.Kind, symbolKinds[graph.KindFile])
		}
		wantRef := "go://example.com/app#" + one.Symbol.Name + ":file"
		if one.Symbol.Ref != wantRef {
			t.Errorf("FileNeverBuilt(%s).Symbol.Ref = %q, want %q",
				one.Symbol.Name, one.Symbol.Ref, wantRef)
		}
		if one.Position.Path != one.Symbol.Name || one.Position.Line != 1 {
			t.Errorf("FileNeverBuilt(%s).Position = %+v, want the first line of %s",
				one.Symbol.Name, one.Position, one.Symbol.Name)
		}
		if one.Position.EndLine < one.Position.Line || one.Symbol.SizeLines != one.Position.EndLine {
			t.Errorf("FileNeverBuilt(%s) spans %d lines to line %d, want the file's own length",
				one.Symbol.Name, one.Symbol.SizeLines, one.Position.EndLine)
		}
		if !slices.Equal(one.Configurations, in.Matrix) {
			t.Errorf("FileNeverBuilt(%s).Configurations = %v, want the matrix %v: a file no configuration of the matrix compiles is never built under any of them",
				one.Symbol.Name, one.Configurations, in.Matrix)
		}
	}
}

func TestFileNeverBuiltReportsNothingWhereTheMatrixIsNotDeclaredComplete(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-built.txtar", listedMatrixConfig(), Consumers{},
		twoConfigurations()...)

	if found := emitted(t, "FileNeverBuilt", fileNeverBuiltCode, FileNeverBuilt, in); len(found) > 0 {
		t.Errorf("FileNeverBuilt(a target listing its configurations and declaring nothing about the matrix) reports %v, want nothing: a matrix no declaration calls complete holds no claim about the configurations it leaves out",
			summary(found))
	}
}

func TestFileNeverBuiltReportsNothingWhereTheCompleteMatrixIsOneTheRunDerived(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-built.txtar", declaredCompleteWithoutConfigurations(),
		Consumers{}, twoConfigurations()...)

	if found := emitted(t, "FileNeverBuilt", fileNeverBuiltCode, FileNeverBuilt, in); len(found) > 0 {
		t.Errorf("FileNeverBuilt(a target declaring complete a matrix it lists no configuration of) reports %v, want nothing: the declaration asserts that the listed configurations are every one the target builds, and a configuration listing none declares it of a derived matrix",
			summary(found))
	}
}

func TestFileNeverBuiltReportsNoFileTheIgnoreTagConstrains(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-built.txtar", completeMatrixConfig(), Consumers{},
		twoConfigurations()...)

	ignored := false
	for _, one := range in.Per {
		for _, p := range one.Result.Packages {
			ignored = ignored || slices.ContainsFunc(p.IgnoredFiles, func(path string) bool {
				return filepath.Base(path) == "generate.go"
			})
		}
	}
	if !ignored {
		t.Fatal("no load of the fixture ignored generate.go, so this test pins nothing")
	}
	found := emitted(t, "FileNeverBuilt", fileNeverBuiltCode, FileNeverBuilt, in)
	if slices.ContainsFunc(found, func(one Finding) bool { return one.Symbol.Name == "generate.go" }) {
		t.Errorf("FileNeverBuilt reports %v, want no finding about generate.go: the ignore tag is the convention for a file run by hand, so no configuration builds one by design",
			summary(found))
	}
}

func TestFileNeverBuiltReportsNoFileTheToolchainIgnoredForReachingC(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-built.txtar", completeMatrixConfig(), Consumers{},
		twoConfigurations()...)

	// The opaque-C check read the file, so no package of any configuration ignores
	// it any more, which is the premise the kind is being held to here.
	for _, one := range in.Per {
		for _, p := range one.Result.Packages {
			if slices.ContainsFunc(p.IgnoredFiles, func(path string) bool {
				return filepath.Base(path) == "bridge.go"
			}) {
				t.Errorf("the %s load has %s ignoring bridge.go, want the opaque-C check to have read it",
					one.Result.Configuration.ID, p.ID)
			}
		}
	}
	found := emitted(t, "FileNeverBuilt", fileNeverBuiltCode, FileNeverBuilt, in)
	if slices.ContainsFunc(found, func(one Finding) bool { return one.Symbol.Name == "bridge.go" }) {
		t.Errorf("FileNeverBuilt reports %v, want no finding about bridge.go: a file the toolchain ignored for reaching C is read by the opaque-C check rather than decided by a configuration",
			summary(found))
	}
}

func TestFileNeverImportedReportsEveryFileOfAPackageNothingReaches(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-imported.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, map[string]Emitter{fileNeverImportedCode: FileNeverImported})
	got := make([]string, 0, len(result.Findings))
	for i := range result.Findings {
		got = append(got, result.Findings[i].Position.Path)
	}
	slices.Sort(got)
	want := []string{"api/api.go", "internal/orphan/helper.go", "internal/orphan/orphan.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("a pass over a target holding one unreached package reports %v under %s, want %v",
			got, fileNeverImportedCode, want)
	}
	one := result.Findings[0]
	if one.Relation != graph.ReferenceCounting {
		t.Errorf("FileNeverImported(%s).Relation = %v, want %v: the kind decides on the references made to the package the file declares",
			one.Position.Path, one.Relation, graph.ReferenceCounting)
	}
	if one.Symbol.Kind != symbolKinds[graph.KindFile] {
		t.Errorf("FileNeverImported(%s).Symbol.Kind = %q, want %q",
			one.Position.Path, one.Symbol.Kind, symbolKinds[graph.KindFile])
	}
	if one.Fixability != deletableFixability {
		t.Errorf("FileNeverImported(%s).Fixability = %q, want %q",
			one.Position.Path, one.Fixability, deletableFixability)
	}
	if one.Class != Certain {
		t.Errorf("FileNeverImported(%s).Class = %q, want %q", one.Position.Path, one.Class, Certain)
	}
}

func TestFileNeverImportedReportsNoFileOfALibrarysPublishedPackage(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-imported.txtar", libraryConfig(), Consumers{})

	found := emitted(t, "FileNeverImported", fileNeverImportedCode, FileNeverImported, in)
	got := make([]string, 0, len(found))
	for i := range found {
		got = append(got, found[i].Position.Path)
	}
	slices.Sort(got)
	want := []string{"internal/orphan/helper.go", "internal/orphan/orphan.go"}
	if !slices.Equal(got, want) {
		t.Errorf("FileNeverImported(a library holding one unreached package) reports %v, want %v: the exported declaration of an importable package is a root of a library, and the internal tree has no importer outside the module",
			got, want)
	}
}

func TestFileNeverImportedReportsNoFileOfAPackageAnImportReaches(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-imported.txtar", applicationConfig(), Consumers{})
	orphan := packageSymbol(t, in, "example.com/app/internal/orphan")
	in.Merged.References = append(in.Merged.References, graph.Reference{
		From: mainFunction(t, in),
		To:   orphan,
		Kind: graph.RefRead,
	})

	found := emitted(t, "FileNeverImported", fileNeverImportedCode, FileNeverImported, in)
	for i := range found {
		if strings.HasPrefix(found[i].Position.Path, "internal/orphan/") {
			t.Errorf("FileNeverImported reports %v, want no finding about a file of the package the synthesized import reaches",
				summary(found))
			break
		}
	}
}

func TestFileNeverImportedReportsNoFileOfAPackageHoldingALiveDeclaration(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-imported.txtar", applicationConfig(), Consumers{})
	retainOneDeclaration(t, in, "example.com/app/internal/orphan")

	found := emitted(t, "FileNeverImported", fileNeverImportedCode, FileNeverImported, in)
	for i := range found {
		if strings.HasPrefix(found[i].Position.Path, "internal/orphan/") {
			t.Errorf("FileNeverImported reports %v, want no finding about a file of a package holding a declaration the sweep did not judge dead: a use no reference names reaches that package, so its files do not fall with it",
				summary(found))
			break
		}
	}
}

// retainOneDeclaration takes one declaration of the named package out of the
// sweep's candidate set, which is the state an exemption leaves behind: the
// declaration is live under a use no reference names.
func retainOneDeclaration(t *testing.T, in *Input, pkgPath string) {
	t.Helper()

	for i := range in.Sweep.Candidates {
		symbol := in.symbol(in.Sweep.Candidates[i].ID)
		if symbol == nil || symbol.PkgPath != pkgPath {
			continue
		}
		in.Sweep.Candidates = slices.Delete(in.Sweep.Candidates, i, i+1)
		in.indexed = nil
		return
	}
	t.Fatalf("Setup: the sweep holds no candidate declared in %s", pkgPath)
}

func TestFileNeverImportedReportsNoFileOfTheMainPackage(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-never-imported.txtar", applicationConfig(), Consumers{})

	found := emitted(t, "FileNeverImported", fileNeverImportedCode, FileNeverImported, in)
	for _, path := range []string{"main.go", "plugin/plug.go", "tool/run.go"} {
		if slices.ContainsFunc(found, func(one Finding) bool { return one.Position.Path == path }) {
			t.Errorf("FileNeverImported reports %v, want no finding about %s: the toolchain builds a main package whatever imports it",
				summary(found), path)
		}
	}
}

// packageSymbol is the identifier of the package symbol one import path names.
func packageSymbol(t *testing.T, in *Input, pkgPath string) graph.SymbolID {
	t.Helper()

	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind == graph.KindPackage && symbol.PkgPath == pkgPath {
			return symbol.ID
		}
	}
	t.Fatalf("Setup: the inventory holds no package symbol for %s", pkgPath)
	return ""
}

// mainFunction is the identifier of the target's main function, which is the
// declaration a synthesized import edge is made from.
func mainFunction(t *testing.T, in *Input) graph.SymbolID {
	t.Helper()

	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind == graph.KindFunc && symbol.Name == "main" {
			return symbol.ID
		}
	}
	t.Fatalf("Setup: the inventory holds no main function")
	return ""
}

func TestUnusedDependencyReportsTheRequirementNoImportNeeds(t *testing.T) {
	t.Parallel()

	in := withModuleFile(t, "artifacts-dependency.txtar", applicationConfig(), twoConfigurations()...)

	found := emitted(t, "UnusedDependency", unusedDependencyCode, UnusedDependency, in)
	if slices.ContainsFunc(found, func(one Finding) bool { return one.Symbol.Name == "example.com/platform" }) {
		t.Errorf("UnusedDependency reports %v, want no finding about example.com/platform: a requirement one configuration of the matrix imports is used",
			summary(found))
	}
	if slices.ContainsFunc(found, func(one Finding) bool { return one.Symbol.Name == "golang.org/x/mod" }) {
		t.Errorf("UnusedDependency reports %v, want no finding about golang.org/x/mod: a requirement the file marks indirect pins a transitive version and deleting it changes the build list",
			summary(found))
	}
	if len(found) != 1 {
		t.Fatalf("UnusedDependency(a module file with one unused requirement) reports %v, want one finding",
			summary(found))
	}
	one := found[0]
	if one.Symbol.Name != "golang.org/x/text" {
		t.Errorf("UnusedDependency reports %q, want golang.org/x/text: the requirement one declaration imports is used",
			one.Symbol.Name)
	}
	if want := "go://example.com/app#golang.org/x/text:require"; one.Symbol.Ref != want {
		t.Errorf("UnusedDependency().Symbol.Ref = %q, want %q", one.Symbol.Ref, want)
	}
	if one.Symbol.Kind != dependencySubject {
		t.Errorf("UnusedDependency().Symbol.Kind = %q, want %q", one.Symbol.Kind, dependencySubject)
	}
	if one.Details.DependencyClass != requireSection {
		t.Errorf("UnusedDependency().Details.DependencyClass = %q, want %q",
			one.Details.DependencyClass, requireSection)
	}
	if one.Position.Path != "go.mod" || one.Position.Line != 9 || one.Position.EndLine != 9 {
		t.Errorf("UnusedDependency().Position = %+v, want line 9 of go.mod", one.Position)
	}
	if !slices.Equal(one.Configurations, in.Matrix) {
		t.Errorf("UnusedDependency().Configurations = %v, want the matrix %v",
			one.Configurations, in.Matrix)
	}
}

func TestUnusedDependencyReportsNothingWithoutAModuleFile(t *testing.T) {
	t.Parallel()

	in := inputOf(t, "artifacts-dependency.txtar", applicationConfig(), Consumers{})

	if found := emitted(t, "UnusedDependency", unusedDependencyCode, UnusedDependency, in); len(found) > 0 {
		t.Errorf("UnusedDependency(a run that read no module file) reports %v, want nothing",
			summary(found))
	}
}

func TestUnusedReplaceReportsTheDirectiveOverAModuleTheBuildListDoesNotHold(t *testing.T) {
	t.Parallel()

	in := withModuleFile(t, "artifacts-dependency.txtar", applicationConfig(), twoConfigurations()...)

	found := emitted(t, "UnusedReplace", unusedReplaceCode, UnusedReplace, in)
	if slices.ContainsFunc(found, func(one Finding) bool { return one.Symbol.Name == "example.com/platform" }) {
		t.Errorf("UnusedReplace reports %v, want no finding about example.com/platform: the directive over a module one configuration of the matrix loads redirects a build",
			summary(found))
	}
	if len(found) != 1 {
		t.Fatalf("UnusedReplace(a module file with one directive over an absent module) reports %v, want one finding",
			summary(found))
	}
	one := found[0]
	if want := "example.com/absent@v1.0.0"; one.Symbol.Name != want {
		t.Errorf("UnusedReplace reports %q, want %q: the directive over a module the load reached redirects a build",
			one.Symbol.Name, want)
	}
	if want := "go://example.com/app#example.com/absent@v1.0.0:replace"; one.Symbol.Ref != want {
		t.Errorf("UnusedReplace().Symbol.Ref = %q, want %q", one.Symbol.Ref, want)
	}
	if one.Symbol.Kind != directiveSubject {
		t.Errorf("UnusedReplace().Symbol.Kind = %q, want %q", one.Symbol.Kind, directiveSubject)
	}
	if one.Position.Path != "go.mod" || one.Position.Line != 20 {
		t.Errorf("UnusedReplace().Position = %+v, want line 20 of go.mod", one.Position)
	}
}

// The reference of a replace directive and the member naming what the directive
// redirects to are two spellings of one module, and each is the spelling of the
// document that reads it. A reference joins a version to a path with an at sign,
// because two directives over one module at two versions are two subjects. The
// member spells the target as the module file writes it, the version after a space,
// and a target that is a directory carries no version at all.
func TestUnusedReplaceSpellsAReferenceAndAReplacementEachItsOwnWay(t *testing.T) {
	t.Parallel()

	in := withModuleFile(t, "artifacts-dependency.txtar", applicationConfig(), twoConfigurations()...)
	in.Deps.Replaces = append(in.Deps.Replaces, deps.Replacement{
		Old:  deps.Module{Path: "example.com/vanished"},
		New:  deps.Module{Path: "../vendored"},
		Site: token.Position{Filename: "go.mod", Line: 30, Column: 1},
	})

	found := emitted(t, "UnusedReplace", unusedReplaceCode, UnusedReplace, in)
	spelled := make(map[string]string, len(found))
	for _, one := range found {
		spelled[one.Symbol.Ref] = one.Details.Replacement
	}
	for _, tc := range []struct {
		name        string
		ref         string
		replacement string
	}{
		{
			name:        "a_module_target_the_directive_gives_a_version",
			ref:         "go://example.com/app#example.com/absent@v1.0.0:replace",
			replacement: "example.com/other v1.2.3",
		},
		{
			name:        "a_directory_target_no_directive_gives_a_version",
			ref:         "go://example.com/app#example.com/vanished:replace",
			replacement: "../vendored",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reported := spelled[tc.ref]
			if !reported {
				t.Fatalf("UnusedReplace reports %v, want a finding at %s", summary(found), tc.ref)
			}
			if got != tc.replacement {
				t.Errorf("UnusedReplace() at %s has Details.Replacement = %q, want %q",
					tc.ref, got, tc.replacement)
			}
		})
	}
}

func TestAnnotateNamesTheModuleADeletionRemovesTheLastUseOf(t *testing.T) {
	t.Parallel()

	in := withModuleFile(t, "artifacts-dependency.txtar", applicationConfig(), twoConfigurations()...)

	result := computed(t, in, declarationEmitters())
	Annotate(in, result.Findings)

	paired := findingOf(t, result.Findings, unusedUnexportedCode, "paired").Details.RemovesLastUseOf
	if want := []string{"example.com/platform"}; !slices.Equal(paired, want) {
		t.Errorf("Annotate names %v on paired, want %v: the union over the configurations names what one configuration's deletion removes",
			paired, want)
	}
	one := findingOf(t, result.Findings, unusedUnexportedCode, "parse")
	want := []string{"example.com/used"}
	if !slices.Equal(one.Details.RemovesLastUseOf, want) {
		t.Errorf("Annotate(the pass over a dead declaration holding a module's last use) names %v, want %v",
			one.Details.RemovesLastUseOf, want)
	}
	bundled := findingOf(t, result.Findings, unusedUnexportedCode, "bundled").Details.RemovesLastUseOf
	if want := []string{"example.com/left", "example.com/right"}; !slices.Equal(bundled, want) {
		t.Errorf("Annotate names %v on bundled, want %v: one deletion removes the last use of every module the declaration alone imports, ordered bytewise",
			bundled, want)
	}
	for _, shared := range []string{"first", "second"} {
		named := findingOf(t, result.Findings, unusedUnexportedCode, shared).Details.RemovesLastUseOf
		if len(named) > 0 {
			t.Errorf("Annotate names %v on %s, want nothing: deleting one of two declarations that use a module leaves the module used",
				named, shared)
		}
	}
}

func TestAnnotateNamesNoModuleOnAFindingNoDeletionFixes(t *testing.T) {
	t.Parallel()

	in := withModuleFile(t, "artifacts-dependency.txtar", applicationConfig())

	result := computed(t, in, declarationEmitters())
	for i := range result.Findings {
		result.Findings[i].Fixability = "narrowable"
	}
	Annotate(in, result.Findings)

	for i := range result.Findings {
		if named := result.Findings[i].Details.RemovesLastUseOf; len(named) > 0 {
			t.Errorf("Annotate(%s, a finding no deletion fixes) names %v, want nothing: narrowing a declaration removes no use of anything",
				result.Findings[i].Symbol.Name, named)
		}
	}
}

func TestNameConstraintReadsTheConstraintOneFileNameImplies(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ninth.go":                "",
		"sized_windows.go":        "windows",
		"zsyscall_linux_amd64.go": "linux && amd64",
		"stream_js_wasm.go":       "js && wasm",
		"handler_amd64.go":        "amd64",
		"buffer_amd64_linux.go":   "linux",
		"gate_windows_plan9.go":   "plan9",
		"reader_unix.go":          "",
		"reader_helper.go":        "",
		"render_windows_test.go":  "windows",
		"windows.go":              "",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := ""
			if implied := nameConstraint(name); implied != nil {
				got = implied.String()
			}
			if got != want {
				t.Errorf("nameConstraint(%q) = %q, want %q", name, got, want)
			}
		})
	}
}
