// Command deadset-go reports unused symbols in a Go module and its declared
// consumers. It implements the deadset Contract published at
// github.com/cplieger/deadset-spec, and no verb edits a source file.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
)

// version is this analyzer's own version. It moves independently of the Contract
// version the analyzer implements, which is config.ContractVersion.
const version = "0.1.0-dev"

// name is this analyzer's name wherever a document names a product.
const name = "deadset-go"

// The exit codes contract/exit-codes.json names. The two verdict codes, findings
// and pending, belong to a verb that returns a verdict about a report, and no
// such verb is served yet.
const (
	exitClean   = 0
	exitUsage   = 2
	exitFailure = 3
)

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

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one invocation and returns its exit code: 0 for a served verb, 2
// for a usage error, a requested source edit or a configuration this analyzer
// refuses, 3 for a failure that produced no answer.
func run(args []string, stdout, stderr io.Writer) int {
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
	case "analyze", "explain", "print-roots", "print-retained":
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

// printConfig resolves the configuration documents and writes the resolved
// configuration, with the provenance of every setting, to stdout.
func printConfig(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("deadset-go print-config", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprintln(stderr, printConfigUsage) }
	target := set.String("target", ".", "the target root, which holds the repository configuration")
	repository := set.String("config", "", "the repository configuration, in place of "+repositoryDocument+" at the target root")
	central := set.String("central", "", "the central configuration")
	for flagName, setting := range settingFlags {
		set.String(flagName, "", setting.description)
	}
	if err := set.Parse(args); err != nil {
		return exitUsage
	}
	if set.NArg() != 0 {
		fmt.Fprintf(stderr, "deadset-go: print-config takes no argument, got %q\n", set.Arg(0))
		set.Usage()
		return exitUsage
	}

	inputs, err := configInputs(*target, *repository, *central, set)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		set.Usage()
		return exitUsage
	}

	resolved, provenance, err := config.Resolve(inputs)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		code := exitCodeFor(err)
		if code == exitUsage {
			set.Usage()
		}
		return code
	}

	if err := config.Print(stdout, resolved, provenance); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitFailure
	}
	return exitClean
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
