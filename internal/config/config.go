// Package config resolves the deadset configuration. It decodes the closed key
// list the Contract declares from the documents one invocation supplies, applies
// a command-line flag over the repository configuration over the central
// configuration over each key's documented default, and records the source of
// every resolved setting so the resolved configuration can be printed and read
// back.
package config

import (
	"fmt"
	"strings"
)

// ContractVersion is the Contract version this package implements. It is the
// resolved value of contract_version when no source supplies one.
const ContractVersion = "1.7.0"

// defaultTestFiles is the documented default of ts.test_files.
const defaultTestFiles = "**/*.test.{ts,tsx,mts,cts}"

// The documented default of analysis.template_delimiters: the template grammar's
// own action delimiters.
const (
	defaultLeftDelimiter  = "{{"
	defaultRightDelimiter = "}}"
)

// TargetKind is whether the target is an application, whose every caller is in
// the analyzed graph, or a library, whose published API has callers outside it.
type TargetKind string

// The target kinds. There is no third value and no default: a resolved
// configuration carrying neither is refused.
const (
	Application TargetKind = "application"
	Library     TargetKind = "library"
)

// Confidence is the lowest reachability class a finding must reach to be
// reported.
type Confidence string

// The reachability classes, from the narrowest to the widest.
const (
	Certain  Confidence = "certain"
	Probable Confidence = "probable"
	Possible Confidence = "possible"
)

// GeneratedFiles is whether declarations in generated files are analyzed.
type GeneratedFiles string

// The generated-file choices.
const (
	ExcludeGenerated GeneratedFiles = "exclude"
	IncludeGenerated GeneratedFiles = "include"
)

// ConsumerTests is how a reference from a consumer's test file counts.
type ConsumerTests string

// The consumer-test choices.
const (
	TestReference       ConsumerTests = "test"
	ProductionReference ConsumerTests = "production"
)

// Language is one language an analysis covers.
type Language string

// The languages.
const (
	GoLanguage Language = "go"
	TSLanguage Language = "ts"
)

// Severity is what a finding of one issue kind does to a run.
type Severity string

// The severities, from the lowest to the highest.
const (
	Allow Severity = "allow"
	Warn  Severity = "warn"
	Deny  Severity = "deny"
)

// Format is one rendering of the finding list.
type Format string

// The formats.
const (
	Text     Format = "text"
	JSON     Format = "json"
	GitHub   Format = "github"
	SARIF    Format = "sarif"
	Template Format = "template"
)

// Sort is the order findings are rendered in.
type Sort string

// The orders.
const (
	ByPosition Sort = "position"
	BySize     Sort = "size"
)

// Cascade is how much of a dead component a rendering names.
type Cascade string

// The cascade renderings.
const (
	CascadeRoots Cascade = "roots"
	CascadeFull  Cascade = "full"
)

// Config is the resolved configuration: the closed key list of the Contract's
// configuration schema, decoded, with every default applied. Field order is the
// schema's key order, which is the order Print writes.
//
//nolint:govet // fieldalignment: the field order is the schema's key order, which Print writes
type Config struct {
	ContractVersion string              `json:"contract_version"`
	Target          Target              `json:"target"`
	Analysis        Analysis            `json:"analysis"`
	Consumers       Consumers           `json:"consumers"`
	Roots           Roots               `json:"roots"`
	Severity        map[string]Severity `json:"severity"`
	Exemptions      Exemptions          `json:"exemptions"`
	Reporters       Reporters           `json:"reporters"`
	Go              Go                  `json:"go"`
	TS              TS                  `json:"ts"`
}

// Target is what is being analyzed.
type Target struct {
	Kind TargetKind `json:"kind"`
}

// Analysis is how the analysis is scoped and filtered.
//
//nolint:govet // fieldalignment: the field order is the schema's key order, which Print writes
type Analysis struct {
	Languages      []Language      `json:"languages"`
	MinConfidence  Confidence      `json:"min_confidence"`
	GeneratedFiles GeneratedFiles  `json:"generated_files"`
	ConsumerTests  ConsumerTests   `json:"consumer_tests"`
	Configurations []Configuration `json:"configurations"`
	Matrix         Matrix          `json:"matrix"`
	TemplateDirs   []string        `json:"template_dirs"`

	TemplateDelimiters TemplateDelimiters `json:"template_delimiters"`
}

// TemplateDelimiters is the pair of action delimiters a template is parsed with:
// Left opens an action and Right closes it. Both members are required once a
// document names the object, so the pair is one setting rather than two: a
// higher-ranked source replaces both members together and a resolved
// configuration carries one provenance entry for it.
type TemplateDelimiters struct {
	Left  string `json:"left"`
	Right string `json:"right"`
}

// Configuration is one entry of the build matrix: a configuration the target
// builds under, named by the identifier every finding carries.
type Configuration struct {
	ID   string   `json:"id"`
	OS   string   `json:"os"`
	Arch string   `json:"arch"`
	Tags []string `json:"tags"`
}

// Matrix carries the declarations about the build matrix.
type Matrix struct {
	Complete bool `json:"complete"`
}

// Consumers carries the declarations about the consumer set.
type Consumers struct {
	Complete bool `json:"complete"`
}

// Roots names symbols the analysis treats as live without a reference.
type Roots struct {
	Patterns []string `json:"patterns"`
}

// Exemptions switches computed exemption classes off.
type Exemptions struct {
	Disabled []string `json:"disabled"`
}

// Reporters is how findings are rendered.
//
//nolint:govet // fieldalignment: the field order is the schema's key order, which Print writes
type Reporters struct {
	Formats     []Format `json:"formats"`
	Sort        Sort     `json:"sort"`
	Cascade     Cascade  `json:"cascade"`
	MaxFindings int      `json:"max_findings"`
	FailOn      Severity `json:"fail_on"`
}

// Go is the section the Go analyzer owns. It declares no key in this Contract
// version and resolves to the empty object.
type Go struct{}

// TS is the section the TypeScript analyzer owns. The Go analyzer passes it
// through unread.
type TS struct {
	TestFiles  []string `json:"test_files"`
	EntryFiles []string `json:"entry_files"`
}

// Default returns the configuration every documented default states. Target.Kind
// is empty: the field has no default and is never inferred.
func Default() Config {
	return Config{
		ContractVersion: ContractVersion,
		Analysis: Analysis{
			Languages:      []Language{},
			MinConfidence:  Possible,
			GeneratedFiles: ExcludeGenerated,
			ConsumerTests:  TestReference,
			Configurations: []Configuration{},
			Matrix:         Matrix{Complete: false},
			TemplateDirs:   []string{},
			TemplateDelimiters: TemplateDelimiters{
				Left:  defaultLeftDelimiter,
				Right: defaultRightDelimiter,
			},
		},
		Consumers:  Consumers{Complete: false},
		Roots:      Roots{Patterns: []string{}},
		Severity:   map[string]Severity{},
		Exemptions: Exemptions{Disabled: []string{}},
		Reporters: Reporters{
			Formats:     []Format{Text},
			Sort:        ByPosition,
			Cascade:     CascadeRoots,
			MaxFindings: 0,
			FailOn:      Deny,
		},
		TS: TS{
			TestFiles:  []string{defaultTestFiles},
			EntryFiles: []string{},
		},
	}
}

// Source names where a resolved setting came from.
type Source string

// The four sources, in the order they outrank each other.
const (
	SourceFlag       Source = "flag"
	SourceRepository Source = "repository"
	SourceCentral    Source = "central"
	SourceDefault    Source = "default"
)

// Origin is one setting's source together with the file or flag that supplied
// it. Label is empty exactly when Source is SourceDefault.
type Origin struct {
	Label  string
	Source Source
}

// String renders the origin as the provenance value the Contract declares:
// default, or the source, a colon, a space and the file or flag.
func (o Origin) String() string {
	if o.Source == SourceDefault {
		return string(SourceDefault)
	}
	return string(o.Source) + ": " + o.Label
}

// Provenance maps a setting's dotted path to the origin that supplied it. A
// section holds no value of its own and has no entry; the severity object
// carries one entry per code it names, or the entry severity alone when it is
// empty.
type Provenance map[string]Origin

// Inputs are the configuration documents one invocation reads, each JSON or nil
// when absent, with the file or flag that supplied each: RepositoryLabel names
// the document Repository holds, CentralLabel the one Central holds, and
// FlagLabels the flag that supplied each setting Flags names. Flags is keyed by
// dotted setting path, the form a resolved configuration names a setting by.
type Inputs struct {
	FlagLabels      map[string]string
	RepositoryLabel string
	CentralLabel    string
	Flags           []byte
	Repository      []byte
	Central         []byte
}

// ErrorKind names which refusal an Error carries.
type ErrorKind uint8

// The refusals. Every one exits with the usage code.
const (
	// KindMalformed is a document that is not one JSON instance of the closed
	// key list: a syntax error, a value of the wrong type, a value outside the
	// closed set a key declares, or a member written twice at one level.
	KindMalformed ErrorKind = iota
	// KindUnimplementedKey is a key the closed key list does not declare, or a
	// severity key naming a kind whose severity the Contract fixes.
	KindUnimplementedKey
	// KindMissingTargetKind is a resolved configuration no source supplied a
	// target kind for.
	KindMissingTargetKind
)

// String names the kind.
func (k ErrorKind) String() string {
	switch k {
	case KindMalformed:
		return "malformed"
	case KindUnimplementedKey:
		return "unimplemented key"
	case KindMissingTargetKind:
		return "missing target kind"
	default:
		return "unknown"
	}
}

// Error is every refusal this package makes. Key is the key or field the message
// names, spelled as the document spells it, and is empty when the refusal names
// none.
type Error struct {
	Key     string
	Message string
	Kind    ErrorKind
}

// Error returns the message, which names the key and the source that carried it.
func (e *Error) Error() string { return e.Message }

// malformed refuses a document that is not one instance of the closed key list.
func malformed(label, path, format string, args ...any) *Error {
	detail := fmt.Sprintf(format, args...)
	message := label + ": " + detail
	if path != "" {
		message = fmt.Sprintf("%s: %s: %s", label, path, detail)
	}
	return &Error{Kind: KindMalformed, Key: path, Message: message}
}

// unimplementedKey refuses a key the closed key list does not declare, naming
// the nearest key it does.
func unimplementedKey(label, path string) *Error {
	var hint strings.Builder
	switch nearest := nearestKey(path); nearest {
	case "":
	case path:
		fmt.Fprintf(&hint, "; %q is one setting written as nested objects, not as one key", path)
	default:
		fmt.Fprintf(&hint, "; the nearest implemented key is %q", nearest)
	}
	return &Error{
		Kind:    KindUnimplementedKey,
		Key:     path,
		Message: fmt.Sprintf("%s: key %q is not implemented%s", label, path, hint.String()),
	}
}
