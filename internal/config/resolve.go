package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/jsondoc"
	"github.com/cplieger/deadset-spec/v8/document"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Resolved is a resolved configuration: every setting the Go analysis reads,
// each with the source that supplied it.
type Resolved struct {
	values     map[vocab.Key]any
	keyed      map[vocab.Key]map[string]any
	Provenance Provenance
	Config     Config
}

// source is one configuration source a resolution reads: what it supplies and
// where it came from.
type source struct {
	*supplied
	kind  sourceKind
	label string
}

// originOf is the origin a setting at key carries when this source supplies it.
func (s *source) originOf(key vocab.Key) origin {
	if s.kind == sourceFlag {
		return origin{source: sourceFlag, Label: flagLabel(key)}
	}
	return origin{source: s.kind, Label: s.label}
}

// flagLabel names the flag that supplies key.
func flagLabel(key vocab.Key) string {
	row, _ := vocab.ConfigKeyOf(key)
	return "--" + row.Option
}

// Resolve applies the command-line flags over the repository configuration over
// the central configuration over each key's default, and records the source of
// every resolved setting. It returns an [*Error] for a document the closed key
// list refuses, for a flag supplying a key no option names, and for a
// resolution no source supplied a required setting for, which for the target
// kind is every resolution no source supplies one for: it is never inferred.
// A document carried with no path is [ErrNoSourceLabel].
func Resolve(in *Inputs) (*Resolved, error) {
	sources, err := readSources(in)
	if err != nil {
		return nil, err
	}
	r := &Resolved{values: map[vocab.Key]any{}, keyed: map[vocab.Key]map[string]any{}, Provenance: Provenance{}}
	rows := vocab.ConfigKeys()
	for i := range rows {
		row := &rows[i]
		if !row.ReadIn(vocab.Go) {
			continue
		}
		switch row.Role {
		case vocab.RoleSetting:
			if !r.resolve(row, sources) {
				return nil, missing(row.Key, in)
			}
		case vocab.RoleKeyed:
			r.resolveKeyed(row.Key, sources)
		case vocab.RoleSection, vocab.RoleIgnored:
		}
	}
	if r.Config, err = readConfig(r.values, r.keyed); err != nil {
		return nil, fmt.Errorf("read the resolved configuration: %w", err)
	}
	return r, nil
}

// readSources reads and checks the sources one resolution carries, in the order
// they outrank each other, skipping each it does not carry. A flag is checked
// as the one-setting document it supplies.
func readSources(in *Inputs) ([]source, error) {
	var sources []source
	if len(in.Flags) > 0 {
		flags, err := flagSource(in.Flags)
		if err != nil {
			return nil, err
		}
		sources = append(sources, flags)
	}
	for _, held := range []struct {
		kind     sourceKind
		document Document
	}{{sourceRepository, in.Repository}, {sourceCentral, in.Central}} {
		if held.document.Data == nil {
			continue
		}
		if held.document.Path == "" {
			return nil, fmt.Errorf("%w: the %s configuration", ErrNoSourceLabel, held.kind)
		}
		one, err := check(held.document.Data, held.document.Path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source{supplied: one, kind: held.kind, label: held.document.Path})
	}
	return sources, nil
}

// flagSource is the command line as a source: each flag checked as the
// one-setting document it supplies.
func flagSource(flags map[vocab.Key][]byte) (source, error) {
	held := source{supplied: &supplied{values: map[vocab.Key]any{}, keyed: map[vocab.Key]map[string]any{}}, kind: sourceFlag}
	for _, key := range slices.Sorted(maps.Keys(flags)) {
		if row, _ := vocab.ConfigKeyOf(key); row.Option == "" {
			return source{}, unimplemented("the command line", string(key), "no flag supplies it")
		}
		one, err := check(nested(key, flags[key]), flagLabel(key))
		if err != nil {
			return source{}, err
		}
		maps.Copy(held.values, one.values)
	}
	return held, nil
}

// nested is the document holding one value at its key's dotted path.
func nested(key vocab.Key, value []byte) []byte {
	nesting := value
	for _, name := range slices.Backward(strings.Split(string(key), ".")) {
		quoted, _ := json.Marshal(name) // a string always encodes
		nesting = slices.Concat([]byte("{"), quoted, []byte(":"), nesting, []byte("}"))
	}
	return nesting
}

// resolve assigns one setting the value of the highest-ranked source carrying
// one, or its default, and records where it came from. It reports false for a
// setting with no default that no source supplies.
func (r *Resolved) resolve(row *vocab.ConfigKey, sources []source) bool {
	for i := range sources {
		if value, held := sources[i].values[row.Key]; held {
			r.values[row.Key] = value
			r.Provenance[string(row.Key)] = sources[i].originOf(row.Key)
			return true
		}
	}
	if row.Default == "" {
		return false
	}
	r.values[row.Key] = defaultOf(row)
	r.Provenance[string(row.Key)] = origin{source: sourceDefault}
	return true
}

// defaultOf is a key's default, decoded as a document's value is.
func defaultOf(row *vocab.ConfigKey) any {
	value, _ := document.Decode([]byte(row.Default)) // a generated default always decodes
	return value
}

// resolveKeyed resolves a keyed section one member at a time, so a member only a
// lower-ranked source names keeps that source's value. A section that resolves
// empty carries one provenance entry for itself, naming the highest-ranked
// source that wrote one.
func (r *Resolved) resolveKeyed(key vocab.Key, sources []source) {
	resolved := map[string]any{}
	r.Provenance[string(key)] = origin{source: sourceDefault}
	for i := len(sources) - 1; i >= 0; i-- {
		members, held := sources[i].keyed[key]
		if !held {
			continue
		}
		r.Provenance[string(key)] = sources[i].originOf(key)
		for name, value := range members {
			resolved[name] = value
			r.Provenance[string(key)+"."+name] = sources[i].originOf(key)
		}
	}
	if len(resolved) > 0 {
		delete(r.Provenance, string(key))
	}
	r.keyed[key] = resolved
}

// missing refuses a resolution that leaves a required setting unset, naming the
// setting and the two documents searched.
func missing(key vocab.Key, in *Inputs) *Error {
	return &Error{Message: fmt.Sprintf("%s is not set, it has no default and is never inferred: searched %s and %s",
		key, searched("the repository configuration", in.Repository), searched("the central configuration", in.Central))}
}

// searched names one configuration document and whether the invocation carried it.
func searched(named string, d Document) string {
	if d.Data == nil {
		return named + " (not present)"
	}
	return named + " (" + d.Path + ")"
}

// readConfig reads the settings the Go analysis reads out of the resolved
// values, a setting no value is held for reading as its zero value.
func readConfig(values map[vocab.Key]any, keyed map[vocab.Key]map[string]any) (Config, error) {
	var (
		c                                       Config
		generated, consumerTests, sort, cascade vocab.Choice
		configurations                          []configurationEntry
	)
	read := func(key vocab.Key, into any) error {
		value, held := values[key]
		if !held {
			return nil
		}
		return decodeValue(value, into)
	}
	err := errors.Join(
		read(vocab.KeyTargetKind, &c.Target.Kind),
		read(vocab.KeyAnalysisMinConfidence, &c.Analysis.MinConfidence),
		read(vocab.KeyAnalysisGeneratedFiles, &generated),
		read(vocab.KeyAnalysisConsumerTests, &consumerTests),
		read(vocab.KeyAnalysisConfigurations, &configurations),
		read(vocab.KeyAnalysisMatrixComplete, &c.Analysis.Matrix.Complete),
		read(vocab.KeyAnalysisTemplateDirs, &c.Analysis.TemplateDirs),
		read(vocab.KeyAnalysisTemplateDelimiters, &c.Analysis.TemplateDelimiters),
		read(vocab.KeyConsumersComplete, &c.Consumers.Complete),
		read(vocab.KeyRootsPatterns, &c.Roots.Patterns),
		read(vocab.KeyExemptionsDisabled, &c.Exemptions.Disabled),
		read(vocab.KeyReportersFormats, &c.Reporters.Formats),
		read(vocab.KeyReportersSort, &sort),
		read(vocab.KeyReportersCascade, &cascade),
		read(vocab.KeyReportersMaxFindings, &c.Reporters.MaxFindings),
		read(vocab.KeyReportersFailOn, &c.Reporters.FailOn),
		decodeValue(keyed[vocab.KeySeverity], &c.Severity),
	)
	c.Analysis.IncludeGenerated = generated == vocab.AnalysisGeneratedFilesInclude
	c.Analysis.ConsumerTestsProduction = consumerTests == vocab.AnalysisConsumerTestsProduction
	c.Reporters.BySize = sort == vocab.ReportersSortSize
	c.Reporters.CascadeFull = cascade == vocab.ReportersCascadeFull
	for _, entry := range configurations {
		if entry.Project == nil {
			c.Analysis.Platforms = append(c.Analysis.Platforms, entry.Platform)
		}
	}
	return c, err
}

// configurationEntry is one entry of analysis.configurations in either shape: a
// platform, or a project, which names the compiler configuration of another
// language's analysis.
type configurationEntry struct {
	Project *string `json:"project"`
	Platform
}

// decodeValue decodes one resolved value into a Go value. A target kind
// written per language holds the Go member alone.
func decodeValue(value, into any) error {
	encoded, err := encodeValue(value)
	if err != nil {
		return err
	}
	if kind, isKind := into.(*report.TargetKind); isKind && bytes.HasPrefix(encoded, []byte("{")) {
		var members map[vocab.Language]report.TargetKind
		err := json.Unmarshal(encoded, &members)
		*kind = members[vocab.Go]
		return err
	}
	return json.Unmarshal(encoded, into)
}

// encodeValue is one decoded value as compact JSON, every character but those
// strict JSON escapes written as itself.
func encodeValue(value any) ([]byte, error) {
	encoded, err := jsondoc.Encode(value, "")
	return bytes.TrimSuffix(encoded, []byte("\n")), err
}
