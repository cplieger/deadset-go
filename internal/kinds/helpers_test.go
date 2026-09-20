package kinds

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	spec "github.com/cplieger/deadset-spec/v2"
	"golang.org/x/tools/txtar"
)

// The build configuration every fixture of this package is loaded for, stated
// rather than read, so a fixture's expected configuration identifiers are the same
// on every machine.
const (
	fixtureOS   = "linux"
	fixtureArch = "amd64"
)

// detectors are the exemption classes a findings pass runs behind, which is every
// class the analyzer computes: a kind reports what the exemptions did not hold
// back, so a fixture measured without them would report symbols a run never sees.
var detectors = map[exempt.Class]exempt.Detector{
	exempt.InterfaceSatisfaction: exempt.InterfaceSatisfactionDetector,
	exempt.EncodingReflection:    exempt.EncodingReflectionDetector,
	exempt.FormatVerbContract:    exempt.FormatVerbContractDetector,
	exempt.ErrorsDuckTyping:      exempt.ErrorsDuckTypingDetector,
	exempt.EnumGroup:             exempt.EnumGroupDetector,
	exempt.GeneratedFile:         exempt.GeneratedFileDetector,
	exempt.LinknameCgoAsmPlugin:  exempt.LinknameCgoAsmPluginDetector,
	exempt.TemplateField:         exempt.TemplateFieldDetector,
	exempt.ReflectiveLookup:      exempt.ReflectiveLookupDetector,
}

// fixtureConfiguration is the one build configuration a fixture is analyzed under
// unless a test names more.
func fixtureConfiguration() load.Configuration {
	return load.Configuration{ID: fixtureOS + "-" + fixtureArch, OS: fixtureOS, Arch: fixtureArch}
}

// applicationConfig is the resolved configuration of a target whose every caller is
// in the graph.
func applicationConfig() config.Config {
	resolved := config.Default()
	resolved.Target.Kind = config.Application
	return resolved
}

// libraryConfig is the resolved configuration of a target whose published API has
// callers outside the graph.
func libraryConfig() config.Config {
	resolved := config.Default()
	resolved.Target.Kind = config.Library
	return resolved
}

// extract writes one txtar archive of testdata into a directory of its own and
// returns the directory, which is the target root the load resolves.
func extract(t *testing.T, archive string) string {
	t.Helper()

	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	dir := t.TempDir()
	for _, f := range parsed.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	return dir
}

// loadScope resolves one scope through the production load, so the Need bits the
// passes depend on have one owner.
func loadScope(t *testing.T, doc scope.Document, c load.Configuration) *load.Result {
	t.Helper()

	result, err := load.Load(t.Context(), doc, c)
	if err != nil {
		t.Fatalf("Setup: load.Load(%s, %s): %v", doc.Target.Path, c.ID, err)
	}
	return &result
}

// consumerIDs is the module path of every consumer one load resolved, in the order
// the scope declared them.
func consumerIDs(result *load.Result) []string {
	ids := make([]string, 0, len(result.Consumers))
	for _, one := range result.Consumers {
		ids = append(ids, one.ID)
	}
	return ids
}

// inputOf extracts one archive and builds what every kind reads from it, over the
// production path: one load per configuration, the three passes, the exemption
// classes, the merge and the matrix sweep.
//
// The sweep is the production one, for the reason the input's own documentation
// gives: it is the only sweep under which a declaration only a test references is
// dead and a test of dead code is admitted.
func inputOf(t *testing.T, archive string, resolved config.Config, consumers Consumers,
	configurations ...load.Configuration,
) *Input {
	t.Helper()

	return inputOfDir(t, extract(t, archive), resolved, consumers, configurations...)
}

// inputOfDir is inputOf over a directory that is already on disk, which is what a
// measurement over a real module reads.
func inputOfDir(t *testing.T, dir string, resolved config.Config, consumers Consumers,
	configurations ...load.Configuration,
) *Input {
	t.Helper()

	doc, err := scope.ForDir(dir)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	return inputOfScope(t, doc, resolved, consumers, configurations...)
}

// inputOfScope is inputOf over one resolved scope, which is what a run whose scope
// declares consumers reads.
//
// The consumer set is the load's own where the scope declared consumers, which is
// what the composition root does: a scope document leaves the module path to the
// load, so the declared list and the loaded list are one list and a caller that read
// the declared one from the document would put an empty string in it. A scope that
// declares no consumer leaves the caller's value standing.
func inputOfScope(t *testing.T, doc scope.Document, resolved config.Config, consumers Consumers,
	configurations ...load.Configuration,
) *Input {
	t.Helper()

	if len(configurations) == 0 {
		configurations = []load.Configuration{fixtureConfiguration()}
	}
	options := exempt.Options{
		TemplateDelimiters: exempt.Delimiters{
			Left:  resolved.Analysis.TemplateDelimiters.Left,
			Right: resolved.Analysis.TemplateDelimiters.Right,
		},
		TemplateDirs:     resolved.Analysis.TemplateDirs,
		IncludeGenerated: resolved.Analysis.GeneratedFiles == config.IncludeGenerated,
	}
	// The fixture analyses in the production mode, which is the run the analyzer
	// makes, and every stage of the fixture reads the one value as the composition
	// root hands it to every stage of a run.
	mode := graph.Mode{Production: true}
	rootOptions := graph.RootOptions{
		Patterns:     resolved.Roots.Patterns,
		PublishedAPI: resolved.Target.Kind == config.Library,
	}

	per := make([]Configured, len(configurations))
	passes := make([]graph.Configured, len(configurations))
	var exemptions []graph.Exemption
	var root string
	var loadedConsumers []string
	for i, c := range configurations {
		result := loadScope(t, doc, c)
		root = doc.Target.Path
		loadedConsumers = consumerIDs(result)
		symbols, err := graph.Symbols(result, root, os.ReadFile)
		if err != nil {
			t.Fatalf("Setup: graph.Symbols(%s): %v", c.ID, err)
		}
		references, _, err := graph.References(result, root, os.ReadFile, symbols)
		if err != nil {
			t.Fatalf("Setup: graph.References(%s): %v", c.ID, err)
		}
		roots, _, err := graph.Roots(result, root, os.ReadFile, symbols, rootOptions)
		if err != nil {
			t.Fatalf("Setup: graph.Roots(%s): %v", c.ID, err)
		}
		resolver, err := graph.NewResolver(result, root, os.ReadFile, symbols)
		if err != nil {
			t.Fatalf("Setup: graph.NewResolver(%s): %v", c.ID, err)
		}
		computed, err := exempt.Compute(&exempt.Input{
			Result:  result,
			Resolve: resolver,
			Symbols: symbols,
			Root:    root,
			Read:    os.ReadFile,
			Options: options,
			Mode:    mode,
		}, detectors)
		if err != nil {
			t.Fatalf("Setup: exempt.Compute(%s): %v", c.ID, err)
		}
		exemptions = append(exemptions, computed...)
		per[i] = Configured{Result: result, Resolve: resolver, Symbols: symbols}
		passes[i] = graph.Configured{Symbols: symbols, References: references, Roots: roots}
	}

	merged, err := graph.Merge(passes)
	if err != nil {
		t.Fatalf("Setup: graph.Merge(%s): %v", doc.Target.Path, err)
	}
	swept := graph.NewMatrix(&merged).Sweep(graph.SweepInput{Exempt: exemptions, Mode: mode})

	refs := make(map[graph.SymbolID]string, len(merged.Symbols))
	for i := range merged.Symbols {
		refs[merged.Symbols[i].ID] = merged.Symbols[i].Ref
	}
	if len(loadedConsumers) > 0 {
		consumers = Consumers{
			Declared: loadedConsumers,
			Loaded:   loadedConsumers,
			Complete: resolved.Consumers.Complete,
		}
	}
	generated := generatedPaths(t, per)
	return &Input{
		Config:    &resolved,
		Merged:    &merged,
		Sweep:     &swept,
		Refs:      refs,
		Exempt:    exemptions,
		Generated: func(path string) bool { return generated[path] },
		Matrix:    identifiers(configurations),
		Per:       per,
		Consumers: consumers,
		Mode:      mode,
	}
}

// generatedPaths is every generated file of the load, keyed by the target-relative
// path the inventory spells its positions with. It asks the same owner the
// exemption class asks, so a fixture and a run agree on which files are generated.
func generatedPaths(t *testing.T, per []Configured) map[string]bool {
	t.Helper()

	paths := make(map[string]bool)
	for _, one := range per {
		for _, p := range one.Result.Packages {
			for _, f := range p.Syntax {
				if !exempt.IsGeneratedFile(f) {
					continue
				}
				site, err := one.Resolve.Render(f.Package)
				if err != nil {
					t.Fatalf("Setup: render the package clause of a generated file: %v", err)
				}
				paths[site.Filename] = true
			}
		}
	}
	return paths
}

// identifiers is the identifier of every configuration of a matrix, in order.
func identifiers(configurations []load.Configuration) []string {
	ids := make([]string, len(configurations))
	for i, c := range configurations {
		ids[i] = c.ID
	}
	return ids
}

// declarationEmitters are the emitters of the six unused-declaration kinds, which
// is the table a run over a fixture of this package computes with.
func declarationEmitters() map[string]Emitter {
	return map[string]Emitter{
		unusedExportedCode:      UnusedExported,
		unusedUnexportedCode:    UnusedUnexported,
		unusedMemberCode:        UnusedMember,
		testOnlyUseCode:         TestOnlyUse,
		testOfDeadCodeCode:      TestOfDeadCode,
		deprecatedAndUnusedCode: DeprecatedAndUnused,
	}
}

// packageEmitters is every emitter this package implements, which is the table the
// composition root registers and the table the once rule is measured over: one
// position carries one finding, so the kinds agree on precedence or a pass over any
// fixture fails. A kind landed later is added here, and its fixtures are then
// measured against every kind that came before it.
func packageEmitters() map[string]Emitter {
	table := declarationEmitters()
	table[unusedInterfaceCode] = UnusedInterface
	table[uncalledInterfaceMethodCode] = UncalledInterfaceMethod
	table[unusedSatisfactionAssertionCode] = UnusedSatisfactionAssertion
	table[writeOnlyCode] = WriteOnlySymbol
	table[enumMemberCode] = UnusedEnumMember
	table[typeParameterCode] = UnusedTypeParameter
	table[unnecessaryExportCode] = UnnecessaryExport
	table[unnecessaryExposureCode] = UnnecessaryExposure
	table[unreachableExportCode] = UnreachableExport
	table[fileNeverBuiltCode] = FileNeverBuilt
	table[fileNeverImportedCode] = FileNeverImported
	table[unusedDependencyCode] = UnusedDependency
	table[unusedReplaceCode] = UnusedReplace
	table[suppressionWithoutReasonCode] = SuppressionsWithoutReason
	table[unscopedEntryCode] = UnscopedEntries
	table[staleSuppressionCode] = StaleSuppressions
	table[unmatchedRootCode] = UnmatchedRoots
	table[unusedParameterCode] = UnusedParameter
	table[unusedReceiverCode] = UnusedReceiver
	table[unusedResultCode] = UnusedResult
	table[unreachableStatementCode] = UnreachableStatement
	table[deadStoreCode] = DeadStore
	table[unreachableCaseCode] = UnreachableCase
	return table
}

// fixtures is every archive under testdata, which is every fixture of every kind of
// this package.
func fixtures(t *testing.T) []string {
	t.Helper()

	entries, err := filepath.Glob(filepath.Join("testdata", "*.txtar"))
	if err != nil {
		t.Fatalf("Setup: list testdata: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("Setup: testdata holds no archive, so a measurement over every fixture measures nothing")
	}
	archives := make([]string, len(entries))
	for i, entry := range entries {
		archives[i] = filepath.Base(entry)
	}
	return archives
}

// computed runs one findings pass and fails the test where the pass refuses a
// finding, because a refusal is a defect in an emitter rather than an answer.
func computed(t *testing.T, in *Input, emitters map[string]Emitter) Result {
	t.Helper()

	result, err := Compute(in, emitters)
	if err != nil {
		t.Fatalf("Compute() = error %v, want the findings of the pass", err)
	}
	return result
}

// codesOf is the code of every finding, in the order the pass returned them.
func codesOf(findings []Finding) []string {
	codes := make([]string, len(findings))
	for i := range findings {
		codes[i] = findings[i].Code
	}
	return codes
}

// namesUnder is the name of every finding of one code, in the order the pass
// returned them.
func namesUnder(findings []Finding, code string) []string {
	var names []string
	for i := range findings {
		if findings[i].Code == code {
			names = append(names, findings[i].Symbol.Name)
		}
	}
	return names
}

// findingOf is the finding one code reports about the symbol one name spells.
func findingOf(t *testing.T, findings []Finding, code, name string) Finding {
	t.Helper()

	for i := range findings {
		if findings[i].Code == code && findings[i].Symbol.Name == name {
			return findings[i]
		}
	}
	t.Fatalf("the pass reports no %s finding about %s: it reports %v", code, name, summary(findings))
	return Finding{}
}

// summary is one line per finding, for a failure message that has to say what the
// pass did report.
func summary(findings []Finding) []string {
	lines := make([]string, len(findings))
	for i := range findings {
		lines[i] = findings[i].Code + " " + findings[i].Symbol.Name
	}
	return lines
}

// corpusInput builds what every kind reads from one fixture of the pinned corpus,
// with the fixture's consumer section loaded.
//
// A rendering's sections are directories of one archive: target holds the module
// being analyzed and every other top-level directory is a consumer module, which
// reaches the target through the relative replace its own module file carries. So a
// fixture is loaded the way a run with a scope document is, and the consumer set the
// class ladder reads is the load's own.
func corpusInput(t *testing.T, fixture string, resolved config.Config) *Input {
	t.Helper()

	data, err := spec.Corpus.ReadFile("corpus/fixtures/" + fixture + "/go.txtar")
	if err != nil {
		t.Fatalf("Setup: read the Go rendering of %s: %v", fixture, err)
	}
	dir := t.TempDir()
	sections := make(map[string]bool)
	for _, f := range txtar.Parse(data).Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
		if section, _, nested := strings.Cut(f.Name, "/"); nested {
			sections[section] = true
		}
	}
	if !sections[corpusTargetSection] {
		t.Fatalf("Setup: the rendering of %s carries no %s section", fixture, corpusTargetSection)
	}
	doc := scope.Document{Target: scope.Module{Path: filepath.Join(dir, corpusTargetSection)}}
	for _, section := range slices.Sorted(maps.Keys(sections)) {
		if section != corpusTargetSection {
			doc.Consumers = append(doc.Consumers, scope.Module{Path: filepath.Join(dir, section)})
		}
	}
	return inputOfScope(t, doc, resolved, Consumers{})
}

// corpusTargetSection is the directory of a fixture rendering that holds the module
// being analyzed.
const corpusTargetSection = "target"
