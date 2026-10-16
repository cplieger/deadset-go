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
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// repositoryDocument is the repository configuration's name at the target root.
const repositoryDocument = "deadset.json"

// maxDocumentBytes bounds one configuration document. A configuration is one
// instance of a closed key list, so the bound is far above any real document.
const maxDocumentBytes = 1 << 20

// settingFlag is one setting a command-line flag supplies: the dotted path the
// resolved configuration names the setting by, and what the flag's value is.
type settingFlag struct {
	path        string
	description string

	// number says the setting is declared a number, so the document this flag
	// supplies carries a JSON number: a number written as a string is refused by the
	// same decode a file goes through.
	number bool
}

// settingFlags maps a flag name, chosen for how it reads on a command line, to the
// setting of contract/config.schema.json it supplies. A setting reaches the command
// line only by an entry, and the path is what its provenance names.
var settingFlags = map[string]settingFlag{
	"min-confidence": {
		path:        "analysis.min_confidence",
		description: "the lowest reachability class a finding is reported at",
	},
	"sort": {
		path:        "reporters.sort",
		description: "the order findings are rendered in",
	},
	"cascade": {
		path:        "reporters.cascade",
		description: "how much of a dead component a rendering names",
	},
	"max-findings": {
		path:        "reporters.max_findings",
		description: "the greatest number of findings a rendering prints, with the rest counted as omitted",
		number:      true,
	},
	"fail-on": {
		path:        "reporters.fail_on",
		description: "the lowest severity that fails the run",
	},
}

// configuredFlags are the flags every verb that reads a configuration registers,
// and the values they parsed into. scope is nil for a verb that registers no flag
// for a scope document.
type configuredFlags struct {
	set        *flag.FlagSet
	target     *string
	repository *string
	central    *string
	scope      *string
}

// configuredFlagSet registers the target root, the two configuration documents, one
// flag per setting the command line supplies, and the scope document where the verb
// reads one.
func configuredFlagSet(verb, verbUsage string, stderr io.Writer, scoped bool) configuredFlags {
	set := flag.NewFlagSet("deadset-go "+verb, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprintln(stderr, verbUsage) }
	held := configuredFlags{
		set:        set,
		target:     set.String("target", ".", "the target root, which holds the repository configuration"),
		repository: set.String("config", "", "the repository configuration, in place of "+repositoryDocument+" at the target root"),
		central:    set.String("central", "", "the central configuration"),
	}
	if scoped {
		held.scope = set.String("scope", "", "the scope document naming the target and its declared consumers")
	}
	for flagName, setting := range settingFlags {
		set.String(flagName, "", setting.description)
	}
	return held
}

// request reads the configuration documents the parsed flags name and resolves
// them. The code is vocab.ExitClean on success, and otherwise the verb's code with
// the message already printed.
func (f *configuredFlags) request(stderr io.Writer) (req *run.Request, code int) {
	inputs, err := configInputs(*f.target, *f.repository, *f.central, f.set)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		f.set.Usage()
		return nil, vocab.ExitUsage
	}
	resolved, provenance, err := config.Resolve(inputs)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		refusal := exitCodeFor(err)
		if refusal == vocab.ExitUsage {
			f.set.Usage()
		}
		return nil, refusal
	}
	req = &run.Request{Config: resolved, Provenance: provenance, Target: *f.target}
	if f.scope != nil {
		req.Scope = *f.scope
	}
	return req, vocab.ExitClean
}

// resolve parses the flags of a verb that reads a configuration and takes no
// argument, and resolves the documents they name.
func resolve(verb, verbUsage string, args []string, stderr io.Writer) (req *run.Request, code int) {
	flags := configuredFlagSet(verb, verbUsage, stderr, false)
	if err := flags.set.Parse(args); err != nil {
		return nil, vocab.ExitUsage
	}
	if flags.set.NArg() != 0 {
		fmt.Fprintf(stderr, "deadset-go: %s takes no argument, got %q\n", verb, flags.set.Arg(0))
		flags.set.Usage()
		return nil, vocab.ExitUsage
	}
	return flags.request(stderr)
}

// configInputs reads the configuration documents one invocation names. The
// repository configuration at its conventional path is optional; a document the
// invocation named is not.
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

// readDocument reads one document, refusing one above the size bound without
// reading it whole. An absent optional document is no document.
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

// applyFlagSettings records the settings the command line supplied as one document
// keyed by dotted setting path, with the flag that supplied each as its label.
func applyFlagSettings(set *flag.FlagSet, inputs *config.Inputs) error {
	settings := make(map[string]json.RawMessage)
	labels := make(map[string]string)
	var refused error
	set.Visit(func(f *flag.Flag) {
		setting, ok := settingFlags[f.Name]
		if !ok {
			return
		}
		encoded, err := settingValue(&setting, f.Value.String())
		if err != nil {
			refused = errors.Join(refused, fmt.Errorf("--%s: %w", f.Name, err))
			return
		}
		settings[setting.path] = encoded
		labels[setting.path] = "--" + f.Name
	})
	if refused != nil {
		return refused
	}
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

// settingValue is one flag's value as the JSON the setting's declared type requires.
func settingValue(setting *settingFlag, value string) (json.RawMessage, error) {
	if !setting.number {
		return json.Marshal(value)
	}
	held, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("%s is a number, and %q is not one", setting.path, value)
	}
	return json.Marshal(held)
}
