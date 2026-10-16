// Package config resolves the deadset configuration the Go analysis reads. It
// reads the closed key list of the Contract's generated setting table from the
// command-line flags, the repository configuration and the central
// configuration, applies them in that rank over each key's default, and records
// the source of every resolved setting so the resolved configuration can be
// printed and read back.
//
// It checks and resolves what the Go analysis reads alone: the keys the table
// says it reads and, of a key written per language, the Go member. Every other
// key the table declares is accepted unread; the orchestrator checks the whole
// document.
package config

import (
	"errors"

	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Config is the resolved configuration as the Go analysis reads it, each field
// the setting of the same name in the closed key list.
type Config struct {
	Severity   map[string]vocab.Severity
	Target     Target
	Roots      Roots
	Exemptions Exemptions
	Analysis   Analysis
	Reporters  Reporters
	Consumers  Consumers
}

// Target is what is being analyzed. Kind is the kind of the Go code, empty in a
// configuration no source has supplied one for.
type Target struct {
	Kind report.TargetKind
}

// Analysis is how the analysis is scoped and filtered.
type Analysis struct {
	MinConfidence      vocab.Class
	TemplateDelimiters TemplateDelimiters
	TemplateDirs       []string

	// Platforms is the platform entries of analysis.configurations, in its
	// order: the matrix the Go analysis builds. A project entry is the matrix
	// of another language's analysis and is not among them.
	Platforms []Platform
	Matrix    Matrix

	IncludeGenerated        bool
	ConsumerTestsProduction bool
}

// TemplateDelimiters is the pair of action delimiters a template is parsed with.
type TemplateDelimiters struct {
	Left  string `json:"left"`
	Right string `json:"right"`
}

// Platform is a build configuration named by an operating system, an
// architecture and the build tags in effect, under the identifier every finding
// carries.
type Platform struct {
	ID   string   `json:"id"`
	OS   string   `json:"os"`
	Arch string   `json:"arch"`
	Tags []string `json:"tags"`
}

// Matrix carries the declarations about the build matrix.
type Matrix struct {
	Complete bool
}

// Consumers carries the declarations about the consumer set.
type Consumers struct {
	Complete bool
}

// Roots names symbols the analysis treats as live without a reference.
type Roots struct {
	Patterns []string
}

// Exemptions switches computed exemption classes off.
type Exemptions struct {
	Disabled []vocab.Exemption
}

// Reporters is how findings are rendered.
type Reporters struct {
	FailOn      vocab.Severity
	Formats     []vocab.Format
	MaxFindings int

	// BySize puts the largest deletion first, and CascadeFull names every
	// member of a dead component rather than its roots.
	BySize      bool
	CascadeFull bool
}

// sourceKind names where a resolved setting came from.
type sourceKind string

// The four sources, in the order they outrank each other.
const (
	sourceFlag       sourceKind = "flag"
	sourceRepository sourceKind = "repository"
	sourceCentral    sourceKind = "central"
	sourceDefault    sourceKind = "default"
)

// origin is one setting's source together with the file or flag that supplied
// it. Label is empty exactly when the setting took its default.
type origin struct {
	Label  string
	source sourceKind
}

// String renders the origin as the provenance value the Contract declares:
// default, or the source, a colon, a space and the file or flag.
func (o origin) String() string {
	if o.source == sourceDefault {
		return string(sourceDefault)
	}
	return string(o.source) + ": " + o.Label
}

// Provenance maps a setting's dotted path to the origin that supplied it. A
// section holds no value of its own and has no entry; the severity object
// carries one entry per code it names, or the entry severity alone when it is
// empty.
type Provenance map[string]origin

// Document is one configuration document: the path it was read from and its
// bytes, nil when the invocation carries none.
type Document struct {
	Path string
	Data []byte
}

// Inputs are what one resolution reads. Flags is the settings the command line
// supplied, by key, each the JSON value of its flag.
type Inputs struct {
	Flags      map[vocab.Key][]byte
	Repository Document
	Central    Document
}

// ErrNoSourceLabel reports a document carried with no path to name it by, which
// the Contract's provenance syntax requires. A caller fixes it by naming every
// document it passes.
var ErrNoSourceLabel = errors.New("a configuration document names no file")

// Error is every refusal of a configuration this package makes. Every one exits
// with the usage code.
type Error struct {
	Message string
}

// Error returns the message, which names the key and the source that carried it.
func (e *Error) Error() string { return e.Message }
