package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/jsondoc"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// repositoryDocument is the repository configuration's name at the target root.
const repositoryDocument = "deadset.json"

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

// configuredFlagSet registers the target root and the two configuration
// documents, and of the options cli.json lists for the verb, the scope document
// and each option supplying a setting. The verb registers the rest of its list.
func configuredFlagSet(verb, verbUsage string, stderr io.Writer) configuredFlags {
	set := flag.NewFlagSet("deadset-go "+verb, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprintln(stderr, verbUsage) }
	held := configuredFlags{
		set:        set,
		target:     set.String("target", ".", "the target root, which holds the repository configuration"),
		repository: set.String("config", "", "the repository configuration, in place of "+repositoryDocument+" at the target root"),
		central:    set.String("central", "", "the central configuration"),
	}
	for _, name := range optionsOf(verb) {
		switch option, _ := vocab.OptionOf(name); {
		case name == "scope":
			held.scope = set.String(name, "", option.Summary)
		case option.Setting != "":
			set.String(name, "", option.Summary)
		}
	}
	return held
}

// optionsOf is the option list cli.json gives this analyzer's verb.
func optionsOf(verb string) []string {
	for _, served := range vocab.AnalyzerVerbs(language) {
		if served.Name == verb {
			return served.Options
		}
	}
	return nil
}

// resolution reads the configuration documents the parsed flags name and resolves
// them. The code is vocab.ExitClean on success, and otherwise the verb's code with
// the message already printed.
func (f *configuredFlags) resolution(ctx context.Context, stderr io.Writer) (resolved *config.Resolved, code int) {
	inputs, err := configInputs(ctx, *f.target, *f.repository, *f.central, f.set)
	if err != nil {
		return nil, f.unread(ctx, stderr, err)
	}
	resolved, err = config.Resolve(inputs)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		refusal := exitCodeFor(err)
		if refusal == vocab.ExitUsage {
			f.set.Usage()
		}
		return nil, refusal
	}
	return resolved, vocab.ExitClean
}

// unread prints why an input the invocation names was not read and returns its
// code: a failure where ctx ended the read, and a usage error otherwise.
func (f *configuredFlags) unread(ctx context.Context, stderr io.Writer, err error) int {
	if ctx.Err() != nil {
		return failed(ctx, stderr, err)
	}
	fmt.Fprintf(stderr, "deadset-go: %v\n", err)
	f.set.Usage()
	return vocab.ExitUsage
}

// request is [configuredFlags.resolution] as the request a verb asks one analysis
// for.
func (f *configuredFlags) request(ctx context.Context, stderr io.Writer) (req *run.Request, code int) {
	resolved, code := f.resolution(ctx, stderr)
	if code != vocab.ExitClean {
		return nil, code
	}
	req = &run.Request{Config: resolved.Config, Provenance: resolved.Provenance, Target: *f.target}
	if f.scope != nil {
		req.Scope = *f.scope
	}
	return req, vocab.ExitClean
}

// parsed parses the flags of a verb that reads a configuration and takes no
// argument.
func parsed(verb, verbUsage string, args []string, stderr io.Writer) (flags *configuredFlags, code int) {
	held := configuredFlagSet(verb, verbUsage, stderr)
	if err := held.set.Parse(args); err != nil {
		return nil, vocab.ExitUsage
	}
	if held.set.NArg() != 0 {
		fmt.Fprintf(stderr, "deadset-go: %s takes no argument, got %q\n", verb, held.set.Arg(0))
		held.set.Usage()
		return nil, vocab.ExitUsage
	}
	return &held, vocab.ExitClean
}

// resolve parses the flags of a verb that reads a configuration and takes no
// argument, and resolves the documents they name.
func resolve(ctx context.Context, verb, verbUsage string, args []string, stderr io.Writer) (req *run.Request, code int) {
	flags, code := parsed(verb, verbUsage, args, stderr)
	if code != vocab.ExitClean {
		return nil, code
	}
	return flags.request(ctx, stderr)
}

// configInputs reads the configuration documents one invocation names. The
// repository configuration at its conventional path is optional; a document the
// invocation named is not.
func configInputs(ctx context.Context, target, repository, central string, set *flag.FlagSet) (*config.Inputs, error) {
	inputs := &config.Inputs{
		Repository: config.Document{Path: repository},
		Central:    config.Document{Path: central},
	}
	if repository == "" {
		inputs.Repository.Path = filepath.Join(target, repositoryDocument)
	}
	var err error
	if inputs.Repository.Data, err = readDocument(ctx, inputs.Repository.Path, repository == ""); err != nil {
		return nil, err
	}
	if central != "" {
		if inputs.Central.Data, err = readDocument(ctx, central, false); err != nil {
			return nil, err
		}
	}
	if inputs.Flags, err = flagSettings(set); err != nil {
		return nil, err
	}
	return inputs, nil
}

// readDocument reads one configuration document, refusing one above the bound
// the Contract sets without reading it whole. An absent optional document is no
// document.
func readDocument(ctx context.Context, path string, optional bool) ([]byte, error) {
	body, err := jsondoc.Read(ctx, path, vocab.DocumentMaxBytes)
	if optional && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the configuration %s: %w", path, err)
	}
	return body, nil
}

// flagSettings is the settings the command line supplied, by key, each as the
// JSON value its option's kind of value requires.
func flagSettings(set *flag.FlagSet) (map[vocab.Key][]byte, error) {
	settings := map[vocab.Key][]byte{}
	var refused error
	set.Visit(func(f *flag.Flag) {
		option, held := vocab.OptionOf(f.Name)
		if !held || option.Setting == "" {
			return
		}
		encoded, err := settingValue(&option, f.Value.String())
		if err != nil {
			refused = errors.Join(refused, fmt.Errorf("--%s: %w", f.Name, err))
			return
		}
		settings[vocab.Key(option.Setting)] = encoded
	})
	return settings, refused
}

// settingValue is one flag's value as the JSON its option's kind of value
// requires: a number for an integer, a string otherwise.
func settingValue(option *vocab.Option, value string) ([]byte, error) {
	if option.Value != vocab.OptionValueInteger {
		return json.Marshal(value)
	}
	held, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("%s is a number, and %q is not one", option.Setting, value)
	}
	return json.Marshal(held)
}
