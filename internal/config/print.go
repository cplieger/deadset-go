package config

import (
	"fmt"
	"io"

	"github.com/cplieger/deadset-go/internal/jsondoc"
)

// printDocument is the resolved configuration as it is written: the closed key
// list in the schema's key order, then the provenance object that names where
// each setting came from. The order is the emitter's own, not the input's, which
// is what makes the output deterministic.
//
//nolint:govet // fieldalignment: the field order is the schema's key order, which this document writes
type printDocument struct {
	Config
	Provenance map[string]string `json:"provenance"`
}

// Print writes the resolved configuration with its provenance, indented, as one
// JSON object ending in a newline. The output is an instance of the closed key
// list, so reading it back as a repository configuration resolves to the same
// configuration: the provenance object is a declared key resolution ignores.
//
//nolint:gocritic // hugeParam: the surface the CLI calls takes the configuration by value
func Print(w io.Writer, c Config, p Provenance) error {
	document := printDocument{Config: written(&c), Provenance: make(map[string]string, len(p))}
	for path, origin := range p {
		document.Provenance[path] = origin.String()
	}
	encoded, err := jsondoc.Encode(document, "  ")
	if err != nil {
		return fmt.Errorf("render the resolved configuration: %w", err)
	}
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("write the resolved configuration: %w", err)
	}
	return nil
}

// written returns the configuration with every absent array and object written as
// the empty one, so the output names a value of the type the closed key list
// declares for every key rather than the JSON null a nil would render as.
func written(c *Config) Config {
	c.Analysis.Languages = orEmpty(c.Analysis.Languages)
	c.Analysis.Configurations = orEmpty(c.Analysis.Configurations)
	c.Analysis.TemplateDirs = orEmpty(c.Analysis.TemplateDirs)
	c.Roots.Patterns = orEmpty(c.Roots.Patterns)
	c.Exemptions.Disabled = orEmpty(c.Exemptions.Disabled)
	c.Reporters.Formats = orEmpty(c.Reporters.Formats)
	c.Providers.Analyzers = orEmpty(c.Providers.Analyzers)
	c.TS.TestFiles = orEmpty(c.TS.TestFiles)
	c.TS.EntryFiles = orEmpty(c.TS.EntryFiles)
	c.TS.InjectionRegistrations = orEmpty(c.TS.InjectionRegistrations)
	c.TS.LifecycleContracts = orEmpty(c.TS.LifecycleContracts)
	c.TS.Serializers = orEmpty(c.TS.Serializers)
	if c.Severity == nil {
		c.Severity = map[string]Severity{}
	}
	return *c
}

// orEmpty returns the slice, or an empty one where it is absent.
func orEmpty[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
