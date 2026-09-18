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
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
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
	case "analyze", "explain", "print-retained":
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

// rootSet is one configuration's root set: every root in the order the detection
// returns them, the reference of every symbol a root names, and every configured
// string that named no symbol.
type rootSet struct {
	refs      map[graph.SymbolID]string
	roots     []graph.Root
	unmatched []graph.Unmatched
}

// printRoots writes the root set of the host build configuration to stdout, one
// root per line: the symbol's reference, why it is a root, and the configured
// string that named it where one did, separated by tabs. The shape is this
// command's own rather than a Contract format, so that sort and cut read it.
//
// A configured string that names no symbol is reported on stderr by its issue
// kind, after every root this run did find, and fails the run.
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

	for _, root := range set.roots {
		if _, err := fmt.Fprintln(stdout, rootLine(set.refs[root.ID], root)); err != nil {
			fmt.Fprintf(stderr, "deadset-go: print-roots: %v\n", err)
			return exitFailure
		}
	}
	for _, unmatched := range set.unmatched {
		fmt.Fprintf(stderr, "%s: roots.patterns names nothing: %s\n", unmatchedRoot, unmatched.Pattern)
	}
	if len(set.unmatched) > 0 {
		return exitFindings
	}
	return exitClean
}

// rootsOf resolves the root set of one configuration: the scope of the target,
// the load of the host build configuration, the enumeration and the detection, in
// the order the analysis runs them.
//
// A library target's published API is a root and an application's is not, which
// is the one setting of the configuration this composition root converts for the
// detection; the patterns pass through as the configuration lists them.
func rootsOf(ctx context.Context, resolved *resolution) (rootSet, error) {
	document, err := scope.ForDir(resolved.target)
	if err != nil {
		return rootSet{}, err
	}
	result, err := load.Load(ctx, document, load.HostConfiguration())
	if err != nil {
		return rootSet{}, err
	}

	targetRoot := document.Target.Path
	symbols, err := graph.Symbols(&result, targetRoot, os.ReadFile)
	if err != nil {
		return rootSet{}, err
	}
	roots, unmatched, err := graph.Roots(&result, targetRoot, os.ReadFile, symbols, graph.RootOptions{
		Patterns:     resolved.config.Roots.Patterns,
		PublishedAPI: resolved.config.Target.Kind == config.Library,
	})
	if err != nil {
		return rootSet{}, err
	}

	refs := make(map[graph.SymbolID]string, len(symbols))
	for i := range symbols {
		refs[symbols[i].ID] = symbols[i].Ref
	}
	return rootSet{refs: refs, roots: roots, unmatched: unmatched}, nil
}

// rootLine renders one root, leaving the third field off a detected class, which
// no configured string named.
func rootLine(ref string, root graph.Root) string {
	line := ref + "\t" + root.Kind.String()
	if root.Source == "" {
		return line
	}
	return line + "\t" + root.Source
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
func exitCodeFor(err error) int {
	if _, refusal := errors.AsType[*config.Error](err); refusal {
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
