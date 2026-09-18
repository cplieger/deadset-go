// Command deadset-go reports unused symbols in a Go module and its declared
// consumers. It implements the deadset Contract published at
// github.com/cplieger/deadset-spec, and no verb edits a source file.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/matrix"
	"github.com/cplieger/deadset-go/internal/scope"
)

// version is this analyzer's own version. It moves independently of the Contract
// version the analyzer implements, which is config.ContractVersion.
const version = "0.1.0-dev"

// name is this analyzer's name wherever a document names a product.
const name = "deadset-go"

// The exit codes contract/exit-codes.json names. The pending verdict belongs to a
// verb that returns a verdict about a merged report, and no such verb is served
// yet.
const (
	exitClean    = 0
	exitFindings = 1
	exitUsage    = 2
	exitFailure  = 3
)

// unmatchedRoot is the issue kind contract/kinds.json gives a configured root or
// root pattern that names no symbol.
const unmatchedRoot = "DS1704"

// repositoryDocument is the repository configuration's name at the target root.
const repositoryDocument = "deadset.json"

// maxDocumentBytes bounds one configuration document. A configuration is one
// instance of a closed key list, so the bound is far above any real document.
const maxDocumentBytes = 1 << 20

// language is the language this analyzer claims.
const language = config.GoLanguage

// schemaVersionsAccepted lists every report schema version this analyzer reads
// and writes. contract.json's schema_versions is the list it must equal.
var schemaVersionsAccepted = []string{"1.0.0"}

// settingFlag is one setting a command-line flag supplies: the dotted path the
// resolved configuration names the setting by, and what the flag's value is.
type settingFlag struct {
	path        string
	description string
}

// settingFlags maps a flag name to the setting it supplies, so one entry per flag
// registers the flag, supplies the document resolution reads, and gives the
// spelling the setting's provenance names.
//
// A flag's name is this command's own vocabulary, chosen for how it reads on a
// command line, and its path is the setting of contract/config.schema.json that
// flag supplies. The two are mapped here and never derived from one another, so
// this table is the authority for both: a setting reaches the command line only
// by an entry, and a path names a key the schema declares.
var settingFlags = map[string]settingFlag{
	"min-confidence": {
		path:        "analysis.min_confidence",
		description: "the lowest reachability class a finding is reported at",
	},
}

const usage = "usage: deadset-go <analyze|explain|print-config|print-roots|print-retained|describe|version> [flags]"

const printConfigUsage = "usage: deadset-go print-config [--target=DIR] [--config=FILE] [--central=FILE] [--min-confidence=CLASS]"

const printRootsUsage = "usage: deadset-go print-roots [--target=DIR] [--config=FILE] [--central=FILE] [--min-confidence=CLASS]"

const printRetainedUsage = "usage: deadset-go print-retained [--target=DIR] [--config=FILE] [--central=FILE] [--min-confidence=CLASS]"

func main() {
	// An interrupt reaches the toolchain a load spawns through the context, so a
	// cancelled run stops rather than waiting for the packages it asked for. The
	// exit path runs no deferred function, so the signal handler is released here.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes one invocation and returns its exit code: 0 for a served verb that
// found nothing to report, 1 for a finding, 2 for a usage error, a requested
// source edit or a configuration this analyzer refuses, 3 for a failure that
// produced no answer.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if flagName, ok := sourceEditFlag(args); ok {
		fmt.Fprintf(stderr, "deadset-go: %s is not supported: deadset-go reports and never edits a source file\n", flagName)
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}

	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprintln(stderr, usage) }
	if err := set.Parse(args); err != nil {
		return exitUsage
	}

	switch verb := set.Arg(0); verb {
	case "version":
		fmt.Fprintf(stdout, "deadset-go %s\ncontract %s\n", version, config.ContractVersion)
		return exitClean
	case "describe":
		return describe(set.Args()[1:], stdout, stderr)
	case "print-config":
		return printConfig(set.Args()[1:], stdout, stderr)
	case "print-roots":
		return printRoots(ctx, set.Args()[1:], stdout, stderr)
	case "print-retained":
		return printRetained(ctx, set.Args()[1:], stdout, stderr)
	case "analyze", "explain":
		fmt.Fprintf(stderr, "deadset-go: %s is not implemented in this version\n", verb)
		set.Usage()
		return exitUsage
	case "":
		set.Usage()
		return exitUsage
	default:
		fmt.Fprintf(stderr, "deadset-go: unknown verb %q\n", verb)
		set.Usage()
		return exitUsage
	}
}

// describeDocument is what describe writes: the analyzer, the Contract version it
// implements, the report schema versions it reads and the languages it claims.
//
//nolint:govet // fieldalignment: encoding/json writes fields in declaration order, and this is the order the Contract's analyzer object declares these members in
type describeDocument struct {
	Name                   string            `json:"name"`
	Version                string            `json:"version"`
	ContractVersion        string            `json:"contract_version"`
	SchemaVersionsAccepted []string          `json:"schema_versions_accepted"`
	Languages              []config.Language `json:"languages"`
	// Conformance is the analyzer's result over the conformance corpus. It is
	// null until a corpus run records one, which says that no corpus has been
	// answered rather than naming a result the analyzer never produced, so a
	// reader that requires a pass refuses this analyzer.
	Conformance any `json:"conformance"`
}

// describe writes one JSON object naming this analyzer to stdout and nothing to
// stderr.
func describe(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "deadset-go: describe takes no argument, got %q\n", args[0])
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}

	encoded, err := json.MarshalIndent(describeDocument{
		Name:                   name,
		Version:                version,
		ContractVersion:        config.ContractVersion,
		SchemaVersionsAccepted: schemaVersionsAccepted,
		Languages:              []config.Language{language},
	}, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: describe: %v\n", err)
		return exitFailure
	}
	if _, err := stdout.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintf(stderr, "deadset-go: describe: %v\n", err)
		return exitFailure
	}
	return exitClean
}

// resolution is what one invocation resolved to: the configuration, the
// provenance of every setting, and the target root the documents were read from.
type resolution struct {
	provenance config.Provenance
	target     string
	config     config.Config
}

// resolve parses the flags a verb that reads a configuration takes and resolves
// the documents they name. The returned code is exitClean when the resolution
// succeeded, and otherwise the code the verb returns with the message already
// printed.
func resolve(verb, verbUsage string, args []string, stderr io.Writer) (resolved resolution, code int) {
	set := flag.NewFlagSet("deadset-go "+verb, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprintln(stderr, verbUsage) }
	target := set.String("target", ".", "the target root, which holds the repository configuration")
	repository := set.String("config", "", "the repository configuration, in place of "+repositoryDocument+" at the target root")
	central := set.String("central", "", "the central configuration")
	for flagName, setting := range settingFlags {
		set.String(flagName, "", setting.description)
	}
	if err := set.Parse(args); err != nil {
		return resolution{}, exitUsage
	}
	if set.NArg() != 0 {
		fmt.Fprintf(stderr, "deadset-go: %s takes no argument, got %q\n", verb, set.Arg(0))
		set.Usage()
		return resolution{}, exitUsage
	}

	inputs, err := configInputs(*target, *repository, *central, set)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		set.Usage()
		return resolution{}, exitUsage
	}

	resolvedConfig, provenance, err := config.Resolve(inputs)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		code := exitCodeFor(err)
		if code == exitUsage {
			set.Usage()
		}
		return resolution{}, code
	}
	return resolution{provenance: provenance, target: *target, config: resolvedConfig}, exitClean
}

// printConfig resolves the configuration documents and writes the resolved
// configuration, with the provenance of every setting, to stdout.
func printConfig(args []string, stdout, stderr io.Writer) int {
	resolved, code := resolve("print-config", printConfigUsage, args, stderr)
	if code != exitClean {
		return code
	}

	if err := config.Print(stdout, resolved.config, resolved.provenance); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitFailure
	}
	return exitClean
}

// detectors is the exemption classes this analyzer computes, each mapped to its
// detection, in vocabulary order. The framework names no class, so the table is
// assembled here, where every other stage of the analysis is assembled, and it
// names every class the vocabulary declares: a class the table does not hold
// retains nothing, so a class missing from it is an exemption the analyzer stops
// computing without anything refusing the configuration that disables it.
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

// configured is one configuration of the matrix: what it loaded and the
// declarations enumerated from it, which is what a stage reading one
// configuration's types rather than the matrix's inventory reads.
type configured struct {
	symbols []graph.Symbol
	result  load.Result
}

// stages is what the matrix produced: the configurations analyzed, one load and
// one enumeration each, the merged inventory every configuration contributed to,
// the target root every position is rendered against, the reference of every
// symbol of that inventory, and every configured string that named no symbol in
// any configuration.
type stages struct {
	refs           map[graph.SymbolID]string
	root           string
	configurations []string
	per            []configured
	unmatched      []graph.Unmatched
	merged         graph.Merged
}

// rootSet is the matrix's root set: every root any configuration detected, each
// naming the configuration it was detected in, the reference of every symbol a
// root names, the configurations analyzed, and every configured string that named
// no symbol in any of them.
type rootSet struct {
	refs           map[graph.SymbolID]string
	configurations []string
	roots          []graph.Root
	unmatched      []graph.Unmatched
}

// retainedSet is the matrix's retained set: every exemption that held back a
// symbol the sweep would otherwise have reported, the reference of every symbol
// one names, and every configured string that named no symbol.
type retainedSet struct {
	refs      map[graph.SymbolID]string
	retained  []graph.Exemption
	unmatched []graph.Unmatched
}

// printRoots writes the root set of every configuration of the matrix to stdout,
// one root per line: the symbol's reference, why it is a root, and the configured
// string that named it where one did, separated by tabs. A matrix of more than one
// configuration adds the configurations that detected the root as a fourth field,
// with the third one present and empty where no configured string named it, so
// every line of one run holds the same fields. The shape is this command's own
// rather than a Contract format, so that sort and cut read it.
//
// A configured string that names no symbol in any configuration is reported on
// stderr by its issue kind, after every root this run did find, and fails the run.
func printRoots(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	resolved, code := resolve("print-roots", printRootsUsage, args, stderr)
	if code != exitClean {
		return code
	}

	set, err := rootsOf(ctx, &resolved)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitCodeFor(err)
	}

	for _, line := range rootLines(set.refs, set.roots, set.configurations) {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			fmt.Fprintf(stderr, "deadset-go: print-roots: %v\n", err)
			return exitFailure
		}
	}
	for _, unmatched := range set.unmatched {
		fmt.Fprintf(stderr, "%s: roots.patterns names nothing: %s\n", unmatchedRoot, unmatched.Source)
	}
	if len(set.unmatched) > 0 {
		return exitFindings
	}
	return exitClean
}

// rootsOf resolves the root set of the matrix.
func rootsOf(ctx context.Context, resolved *resolution) (rootSet, error) {
	loaded, err := stagesOf(ctx, resolved)
	if err != nil {
		return rootSet{}, err
	}
	return rootSet{
		refs:           loaded.refs,
		configurations: loaded.configurations,
		roots:          loaded.merged.Roots,
		unmatched:      loaded.unmatched,
	}, nil
}

// stagesOf runs the stages every verb that reads the target runs: the scope of the
// target, the matrix the configuration names or the target tree implies, one load
// per configuration of it, then the enumeration, the reference pass and the root
// detection per configuration, merged into the one inventory the sweep answers
// over. A configuration that does not load fails the run with nothing computed
// from the others, because an intersection missing a configuration reports what
// that configuration uses.
//
// A library target's published API is a root and an application's is not, which
// is the one setting of the configuration this composition root converts for the
// detection; the patterns pass through as the configuration lists them.
//
// A configured string is unmatched when no configuration matched it: a pattern
// that names a symbol one platform declares names something, so intersecting the
// unmatched sets is what keeps a matrix from reporting a root pattern as naming
// nothing because another platform lacks the file.
func stagesOf(ctx context.Context, resolved *resolution) (stages, error) {
	document, err := scope.ForDir(resolved.target)
	if err != nil {
		return stages{}, err
	}
	configurations, err := matrixOf(&resolved.config, document.Target.Path)
	if err != nil {
		return stages{}, err
	}
	results, err := load.All(ctx, document, configurations)
	if err != nil {
		return stages{}, err
	}

	targetRoot := document.Target.Path
	rootOptions := graph.RootOptions{
		Patterns:     resolved.config.Roots.Patterns,
		PublishedAPI: resolved.config.Target.Kind == config.Library,
	}
	per := make([]configured, len(results))
	passes := make([]graph.Configured, len(results))
	unmatched := make([][]graph.Unmatched, len(results))
	for i := range results {
		if per[i], passes[i], unmatched[i], err = passesOf(&results[i], targetRoot, rootOptions); err != nil {
			return stages{}, err
		}
	}

	merged, err := graph.Merge(passes)
	if err != nil {
		return stages{}, err
	}
	refs := make(map[graph.SymbolID]string, len(merged.Symbols))
	for i := range merged.Symbols {
		refs[merged.Symbols[i].ID] = merged.Symbols[i].Ref
	}
	return stages{
		refs:           refs,
		root:           targetRoot,
		configurations: identifiers(configurations),
		per:            per,
		unmatched:      unmatchedEverywhere(unmatched),
		merged:         merged,
	}, nil
}

// passesOf runs the three passes over one loaded configuration.
func passesOf(result *load.Result, targetRoot string, rootOptions graph.RootOptions) (configured, graph.Configured, []graph.Unmatched, error) {
	symbols, err := graph.Symbols(result, targetRoot, os.ReadFile)
	if err != nil {
		return configured{}, graph.Configured{}, nil, err
	}
	references, _, err := graph.References(result, targetRoot, os.ReadFile, symbols)
	if err != nil {
		return configured{}, graph.Configured{}, nil, err
	}
	roots, unmatched, err := graph.Roots(result, targetRoot, os.ReadFile, symbols, rootOptions)
	if err != nil {
		return configured{}, graph.Configured{}, nil, err
	}
	return configured{symbols: symbols, result: *result},
		graph.Configured{Symbols: symbols, References: references, Roots: roots},
		unmatched, nil
}

// matrixOf resolves the build matrix one run analyzes: the configurations the
// configuration lists, or the matrix the target tree implies where it lists none.
//
// A derived matrix is the atoms the tree names plus the host, never the product of
// them, and it is incomplete by definition, which is why nothing here sets
// analysis.matrix.complete: completeness is the configuration's own assertion.
func matrixOf(cfg *config.Config, targetRoot string) ([]load.Configuration, error) {
	if len(cfg.Analysis.Configurations) > 0 {
		configurations := make([]load.Configuration, len(cfg.Analysis.Configurations))
		for i, c := range cfg.Analysis.Configurations {
			configurations[i] = load.Configuration{ID: c.ID, OS: c.OS, Arch: c.Arch, Tags: c.Tags}
		}
		return configurations, nil
	}
	derived, err := matrix.Derive(targetRoot)
	if err != nil {
		return nil, err
	}
	return derived.Configurations, nil
}

// identifiers lists the identifier of every configuration of the matrix, in the
// order the matrix keys them by, which is the order every configuration index
// reaches this command in.
func identifiers(configurations []load.Configuration) []string {
	named := make([]string, len(configurations))
	for i, c := range configurations {
		named[i] = c.ID
	}
	return named
}

// unmatchedEverywhere returns the configured strings no configuration matched, in
// the order the first configuration lists them.
func unmatchedEverywhere(per [][]graph.Unmatched) []graph.Unmatched {
	if len(per) == 0 {
		return nil
	}
	count := make(map[string]int)
	for _, one := range per {
		for _, unmatched := range one {
			count[unmatched.Source]++
		}
	}
	everywhere := make([]graph.Unmatched, 0, len(per[0]))
	for _, unmatched := range per[0] {
		if count[unmatched.Source] == len(per) {
			everywhere = append(everywhere, unmatched)
		}
	}
	return everywhere
}

// printRetained writes the retained set of the matrix to stdout, one line per
// symbol an exemption held back under any configuration: the symbol's reference,
// then the class that held it, the site the evidence was found at and the clause
// naming that evidence, repeated for every further class that held the same
// symbol, all separated by tabs. One symbol, class and clause is one line whatever
// the number of configurations that retained it, so the line does not name a
// configuration. The shape is this command's own rather than a Contract format, so
// that sort and cut read it, and a class recording no site leaves that field empty
// rather than printing a placeholder position.
//
// A matrix that retains nothing prints nothing and is a clean answer: no symbol was
// held back, which is what a run over a target with no exemption looks like. A
// configured string that names no symbol is reported on stderr by its issue kind,
// as the root verb reports it, because the root set is what the sweep decided the
// candidates against.
func printRetained(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	resolved, code := resolve("print-retained", printRetainedUsage, args, stderr)
	if code != exitClean {
		return code
	}

	options, err := exemptOptions(&resolved.config)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitUsage
	}

	set, err := retainedOf(ctx, &resolved, &options)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitCodeFor(err)
	}

	for _, line := range retainedLines(set.refs, set.retained) {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			fmt.Fprintf(stderr, "deadset-go: print-retained: %v\n", err)
			return exitFailure
		}
	}
	for _, unmatched := range set.unmatched {
		fmt.Fprintf(stderr, "%s: roots.patterns names nothing: %s\n", unmatchedRoot, unmatched.Source)
	}
	if len(set.unmatched) > 0 {
		return exitFindings
	}
	return exitClean
}

// retainedOf resolves the retained set of the matrix: the stages the root verb
// runs, then the exemption classes per configuration and one sweep of the matrix,
// in the order the analysis runs them.
//
// The classes read one configuration's own types, so they run once per
// configuration and the mode carries the union of what they found: an exemption is
// evidence of a use the analysis cannot see, and a use under one configuration is a
// use. One symbol, class and detail computed under more than one configuration is
// one retained record, which is what the sweep of the matrix deduplicates.
//
// The sweep counts every reference, a test file's included: which symbols a
// production sweep would report is a question about the findings rather than about
// what an exemption held back, and a symbol held back under one mode is held back
// under the other.
func retainedOf(ctx context.Context, resolved *resolution, options *exempt.Options) (retainedSet, error) {
	loaded, err := stagesOf(ctx, resolved)
	if err != nil {
		return retainedSet{}, err
	}

	var exemptions []graph.Exemption
	for i := range loaded.per {
		computed, err := exemptionsOf(&loaded.per[i], loaded.root, options)
		if err != nil {
			return retainedSet{}, err
		}
		exemptions = append(exemptions, computed...)
	}

	swept := graph.NewMatrix(&loaded.merged).Sweep(graph.Mode{Exempt: exemptions})
	return retainedSet{refs: loaded.refs, retained: swept.Retained, unmatched: loaded.unmatched}, nil
}

// exemptionsOf computes the exemptions of one configuration.
func exemptionsOf(one *configured, targetRoot string, options *exempt.Options) ([]graph.Exemption, error) {
	resolver, err := graph.NewResolver(&one.result, targetRoot, os.ReadFile, one.symbols)
	if err != nil {
		return nil, err
	}
	return exempt.Compute(&exempt.Input{
		Result:  &one.result,
		Resolve: resolver,
		Symbols: one.symbols,
		Root:    targetRoot,
		Read:    os.ReadFile,
		Options: *options,
	}, detectors)
}

// exemptOptions converts the settings the exemption classes read into the value
// they read them from, which is the whole of the configuration that reaches a
// class.
//
// A name outside the vocabulary is a configuration naming a class that does not
// exist, and it is refused rather than ignored: a maintainer who misspelled a
// class would otherwise be told nothing and keep the exemption they meant to
// switch off.
func exemptOptions(cfg *config.Config) (exempt.Options, error) {
	known := exempt.Classes()
	disabled := make([]exempt.Class, 0, len(cfg.Exemptions.Disabled))
	for _, name := range cfg.Exemptions.Disabled {
		class := exempt.Class(name)
		if !slices.Contains(known, class) {
			return exempt.Options{}, fmt.Errorf("exemptions.disabled: %q is not an exemption class: the classes are %s", name, classNames(known))
		}
		disabled = append(disabled, class)
	}
	return exempt.Options{
		Disabled: disabled,
		TemplateDelimiters: exempt.Delimiters{
			Left:  cfg.Analysis.TemplateDelimiters.Left,
			Right: cfg.Analysis.TemplateDelimiters.Right,
		},
		TemplateDirs:     cfg.Analysis.TemplateDirs,
		IncludeGenerated: cfg.Analysis.GeneratedFiles == config.IncludeGenerated,
	}, nil
}

// classNames renders a class list for a message, in the order it was given.
func classNames(classes []exempt.Class) string {
	names := make([]string, len(classes))
	for i, class := range classes {
		names[i] = string(class)
	}
	return strings.Join(names, ", ")
}

// retainedLines renders one line per retained symbol, reading the record in its
// own order: it groups a symbol's classes adjacently, so one pass over it emits
// one line per symbol and nothing is re-sorted.
func retainedLines(refs map[graph.SymbolID]string, retained []graph.Exemption) []string {
	var lines []string
	for i, held := range retained {
		if i == 0 || held.ID != retained[i-1].ID {
			lines = append(lines, refs[held.ID])
		}
		lines[len(lines)-1] += "\t" + held.Class + "\t" + evidenceSite(held.Site) + "\t" + held.Detail
	}
	return lines
}

// evidenceSite renders the position an exemption recorded, and renders a class
// that recorded none as nothing: an empty field is what says no site exists, where
// a position's own spelling of an empty value would read as a file named "-".
func evidenceSite(site token.Position) string {
	if site.Filename == "" {
		return ""
	}
	return site.String()
}

// rootLines renders one line per root the matrix holds, in the order the merge
// returns them, which is the first configuration's own order followed by what each
// later configuration alone detected.
//
// One root two configurations detect is one line naming both, keyed by the symbol,
// the class and the configured string: a file several configurations compile
// declares one root, and a report of the matrix names it once. The configurations
// are printed only where the matrix holds more than one, so a run over a single
// configuration prints the root set of that configuration and nothing besides.
func rootLines(refs map[graph.SymbolID]string, roots []graph.Root, configurations []string) []string {
	at := make(map[rootKey]int, len(roots))
	lines := make([]string, 0, len(roots))
	detected := make([][]string, 0, len(roots))
	for _, root := range roots {
		key := rootKey{id: root.ID, source: root.Source, kind: root.Kind}
		held, seen := at[key]
		if !seen {
			held = len(lines)
			at[key] = held
			lines = append(lines, rootLine(refs[root.ID], root, len(configurations) > 1))
			detected = append(detected, nil)
		}
		detected[held] = append(detected[held], configurationName(configurations, root.Config))
	}
	if len(configurations) < 2 {
		return lines
	}
	for i := range lines {
		lines[i] += "\t" + strings.Join(detected[i], " ")
	}
	return lines
}

// rootKey is what makes two roots of a matrix one root: the symbol, the class that
// made it one and the configured string that named it, so a symbol that is a root
// for two reasons keeps one line per reason.
type rootKey struct {
	id     graph.SymbolID
	source string
	kind   graph.RootKind
}

// rootLine renders one root. The configured string is left off a detected class,
// which no configured string named, unless a field follows it.
func rootLine(ref string, root graph.Root, sourceField bool) string {
	line := ref + "\t" + root.Kind.String()
	if root.Source == "" && !sourceField {
		return line
	}
	return line + "\t" + root.Source
}

// configurationName names one configuration of the matrix. A configuration outside
// it is named by its place, which is what a root carrying an index the matrix does
// not hold would print, rather than an empty field that reads as no configuration.
func configurationName(configurations []string, at int) string {
	if at < 0 || at >= len(configurations) {
		return strconv.Itoa(at)
	}
	return configurations[at]
}

// configInputs reads the configuration documents one invocation names. The
// repository configuration at its conventional path is optional, because each
// source is optional on its own; a document the invocation named is not.
func configInputs(target, repository, central string, set *flag.FlagSet) (config.Inputs, error) {
	inputs := config.Inputs{
		RepositoryLabel: repository,
		CentralLabel:    central,
	}
	if inputs.RepositoryLabel == "" {
		inputs.RepositoryLabel = filepath.Join(target, repositoryDocument)
	}

	var err error
	if inputs.Repository, err = readDocument(inputs.RepositoryLabel, repository == ""); err != nil {
		return config.Inputs{}, err
	}
	if central != "" {
		if inputs.Central, err = readDocument(central, false); err != nil {
			return config.Inputs{}, err
		}
	}
	if err := applyFlagSettings(set, &inputs); err != nil {
		return config.Inputs{}, err
	}
	return inputs, nil
}

// readDocument reads one configuration document, refusing one above the size
// bound without reading it whole. An absent optional document is no document
// rather than a refusal.
func readDocument(path string, optional bool) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		if optional && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the configuration %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	// One byte past the bound tells a document at the limit from one over it.
	body, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the configuration %s: %w", path, err)
	}
	if len(body) > maxDocumentBytes {
		return nil, fmt.Errorf("the configuration %s is larger than the %d bytes a configuration document may hold", path, maxDocumentBytes)
	}
	return body, nil
}

// applyFlagSettings records the settings the command line supplied: one
// configuration document keyed by dotted setting path, and the flag that supplied
// each, which is what a resolved configuration's provenance names.
func applyFlagSettings(set *flag.FlagSet, inputs *config.Inputs) error {
	settings := make(map[string]string)
	labels := make(map[string]string)
	set.Visit(func(f *flag.Flag) {
		setting, ok := settingFlags[f.Name]
		if !ok {
			return
		}
		settings[setting.path] = f.Value.String()
		labels[setting.path] = "--" + f.Name
	})
	if len(settings) == 0 {
		return nil
	}

	document, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("render the command-line settings: %w", err)
	}
	inputs.Flags = document
	inputs.FlagLabels = labels
	return nil
}

// exitCodeFor maps an error to the exit code contract/exit-codes.json gives it: a
// configuration this analyzer refuses is a usage error, and anything that stopped
// the run before a verdict existed, a load failure among them, is a failure.
//
// A configured template directory the scan cannot read is a refusal of the second
// kind: the document resolved, and what it names does not exist, which a stage
// rather than the decode is the first to find out. A matrix holding no
// configuration, or more than the merge can key one of, is the same kind again: the
// documents resolved, and the matrix they name is one no run can analyze.
//
// A configuration that fails to load is not one of those: the matrix is analyzable
// and one of its configurations did not load, so the run is a failure that produced
// no answer, and the load's own error names the configuration.
func exitCodeFor(err error) int {
	if _, refusal := errors.AsType[*config.Error](err); refusal {
		return exitUsage
	}
	if errors.Is(err, exempt.ErrTemplateDir) {
		return exitUsage
	}
	if errors.Is(err, load.ErrNoConfiguration) || errors.Is(err, graph.ErrMatrix) {
		return exitUsage
	}
	return exitFailure
}

// sourceEditTokens are the tokens whose presence in a flag's name makes that flag
// a request to edit a source file. The set is closed, and a token is matched in
// any position of the name.
var sourceEditTokens = []string{"fix", "edit", "delete", "rewrite"}

// sourceEditFlag returns the first flag whose name carries a source-edit token,
// because every verb is report-only. The match is on the name alone, so a flag
// whose value carries a token is not a request.
func sourceEditFlag(args []string) (string, bool) {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		flagName, _, _ := strings.Cut(arg, "=")
		name := strings.TrimLeft(flagName, "-")
		if slices.ContainsFunc(sourceEditTokens, func(token string) bool {
			return strings.Contains(name, token)
		}) {
			return flagName, true
		}
	}
	return "", false
}
