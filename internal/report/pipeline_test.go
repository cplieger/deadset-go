package report

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	"golang.org/x/tools/txtar"
)

// The fixtures of this package are the findings package's own, read from where they
// live rather than copied, so a rendering is measured over the findings a real pass
// answers. The pipeline below is the composition root's, in the ten lines a rendering
// needs: one load, the three passes, the exemption classes, the merge and the
// production sweep. It is a copy rather than a call, because the findings package's
// own harness is in its test files.
const fixtureDir = "../kinds/testdata"

// The build configuration every fixture is loaded for, stated rather than read, so a
// fixture's configuration identifiers are the same on every machine.
const (
	fixtureOS   = "linux"
	fixtureArch = "amd64"
)

// fixtureDetectors are the exemption classes a findings pass runs behind, which is
// every class the analyzer computes: a rendering measured without them renders
// findings a run never produces.
var fixtureDetectors = map[exempt.Class]exempt.Detector{
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

// fixtureEmitters is every kind this analyzer answers over a fixture of the findings
// package.
func fixtureEmitters() map[string]kinds.Emitter {
	return map[string]kinds.Emitter{
		"DS1001": kinds.UnusedExported,
		"DS1002": kinds.UnusedUnexported,
		"DS1003": kinds.UnusedMember,
		"DS1004": kinds.TestOnlyUse,
		"DS1005": kinds.TestOfDeadCode,
		"DS1006": kinds.DeprecatedAndUnused,
		"DS1101": kinds.UnnecessaryExport,
		"DS1102": kinds.UnnecessaryExposure,
		"DS1103": kinds.UnreachableExport,
		"DS1201": kinds.UnusedInterface,
		"DS1203": kinds.UncalledInterfaceMethod,
		"DS1204": kinds.UnusedSatisfactionAssertion,
		"DS1301": kinds.WriteOnlySymbol,
		"DS1302": kinds.UnusedEnumMember,
		"DS1303": kinds.UnusedTypeParameter,
	}
}

// extract writes one archive of the findings package's fixtures into a directory of
// its own and returns the directory, which is the target root the load resolves.
func extract(t *testing.T, archive string) string {
	t.Helper()

	parsed, err := txtar.ParseFile(filepath.Join(fixtureDir, archive))
	if err != nil {
		t.Fatalf("Setup: parse %s/%s: %v", fixtureDir, archive, err)
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

// envelopeOfDir runs the production pipeline over one directory and assembles the
// envelope of what it answered, with the reader a rendering hashes source lines
// through.
func envelopeOfDir(t *testing.T, dir string, kind config.TargetKind) (Envelope, Options) {
	t.Helper()

	resolved := config.Default()
	resolved.Target.Kind = kind
	configuration := load.Configuration{ID: fixtureOS + "-" + fixtureArch, OS: fixtureOS, Arch: fixtureArch}

	doc, err := scope.ForDir(dir)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	result, err := load.Load(t.Context(), doc, configuration)
	if err != nil {
		t.Fatalf("Setup: load.Load(%s): %v", dir, err)
	}
	root := doc.Target.Path
	symbols, err := graph.Symbols(&result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: graph.Symbols(%s): %v", dir, err)
	}
	references, testFileRules, err := graph.References(&result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.References(%s): %v", dir, err)
	}
	roots, _, err := graph.Roots(&result, root, os.ReadFile, symbols,
		graph.RootOptions{PublishedAPI: kind == config.Library})
	if err != nil {
		t.Fatalf("Setup: graph.Roots(%s): %v", dir, err)
	}
	resolver, err := graph.NewResolver(&result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.NewResolver(%s): %v", dir, err)
	}
	// The pipeline analyses in the production mode, which is the run the analyzer
	// makes, and the classes, the sweep and the kinds all read the one value.
	mode := graph.Mode{Production: true}
	exemptions, err := exempt.Compute(&exempt.Input{
		Result: &result, Resolve: resolver, Symbols: symbols, Root: root, Read: os.ReadFile,
		Options: exempt.Options{
			TemplateDelimiters: exempt.Delimiters{
				Left:  resolved.Analysis.TemplateDelimiters.Left,
				Right: resolved.Analysis.TemplateDelimiters.Right,
			},
		},
		Mode: mode,
	}, fixtureDetectors)
	if err != nil {
		t.Fatalf("Setup: exempt.Compute(%s): %v", dir, err)
	}
	merged, err := graph.Merge([]graph.Configured{{Symbols: symbols, References: references, Roots: roots}})
	if err != nil {
		t.Fatalf("Setup: graph.Merge(%s): %v", dir, err)
	}
	swept := graph.NewMatrix(&merged).Sweep(graph.SweepInput{Exempt: exemptions, Mode: mode})

	refs := make(map[graph.SymbolID]string, len(merged.Symbols))
	for i := range merged.Symbols {
		refs[merged.Symbols[i].ID] = merged.Symbols[i].Ref
	}
	computed, err := kinds.Compute(&kinds.Input{
		Config: &resolved,
		Merged: &merged,
		Sweep:  &swept,
		Refs:   refs,
		Exempt: exemptions,
		Matrix: []string{configuration.ID},
		Per:    []kinds.Configured{{Result: &result, Resolve: resolver, Symbols: symbols}},
		Mode:   mode,
	}, fixtureEmitters())
	if err != nil {
		t.Fatalf("Setup: kinds.Compute(%s): %v", dir, err)
	}

	in := BuildInput{
		Analyzer: analyzerOf(),
		Target: Target{
			Kind:     string(kind),
			Root:     ".",
			Identity: identityOf(t, &result),
		},
		Configurations: []Configuration{{
			ID: configuration.ID, OS: configuration.OS, Arch: configuration.Arch, Tags: []string{},
		}},
		Result:        computed,
		ExcludedByCgo: result.ExcludedByCgo,
		TestFileRules: testFileRules,
	}
	read := func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, path)) }
	return built(t, &in), Options{Read: read}
}

// identityOf is the module path the loaded target publishes itself under, which is
// the identity every report of that target names.
func identityOf(t *testing.T, result *load.Result) string {
	t.Helper()

	for _, p := range result.Packages {
		if p.Module != nil && p.Module.Path != "" {
			return p.Module.Path
		}
	}
	t.Fatal("Setup: the loaded target names no module path")
	return ""
}
