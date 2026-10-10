// Package reportdoc is the Contract's report document in Go: one field per member,
// in the schema's member order, with the JSON name the schema gives each. A required
// member is written whatever its value; an optional one carries omitempty. A member
// the findings pass carries no field for (the container's reference, the visibility,
// the path inside the package's type information, the analyzer a merged record names)
// has no field here.
//
// The liveness relation is the one member whose presence is a claim: it is absent
// where the Contract excludes it for the finding's subject or code, or where no
// relation decided a declaration, and kinds.LivenessAbsent decides which.
package reportdoc

// Envelope is one report document.
type Envelope struct {
	SchemaVersion          string                  `json:"schema_version"`
	ContractVersion        string                  `json:"contract_version"`
	Analyzer               Analyzer                `json:"analyzer"`
	Target                 Target                  `json:"target"`
	Configurations         []Configuration         `json:"configurations"`
	ConfigurationsNotBuilt []ConfigurationNotBuilt `json:"configurations_not_built"`
	Consumers              Consumers               `json:"consumers"`
	Findings               []Finding               `json:"findings"`
	EdgeEvaluations        []Evaluation            `json:"edge_evaluations"`
	StaleSuppressions      []StaleSuppression      `json:"stale_suppressions"`
	DeclaredGaps           []DeclaredGap           `json:"declared_gaps"`
	ExcludedByCgo          []string                `json:"excluded_by_cgo"`
	TestFileRules          []TestFileRule          `json:"test_file_rules"`
	TypeErrorSkips         []TypeErrorSkip         `json:"type_error_skips"`
	Notes                  []Note                  `json:"notes"`
	UnansweredQuestions    []UnansweredQuestion    `json:"unanswered_questions"`
	ConventionsApplied     []ConventionApplied     `json:"conventions_applied"`
	Totals                 Totals                  `json:"totals"`
}

// TypeErrorSkip is one entry of the type_error_skips array.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type TypeErrorSkip struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Note is one entry of the notes array.
type Note struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// UnansweredQuestion is one entry of the unanswered_questions array.
type UnansweredQuestion struct {
	Configuration string `json:"configuration"`
	Questions     int    `json:"questions"`
	Declarations  int    `json:"declarations"`
}

// ConventionApplied is one entry of the conventions_applied array.
type ConventionApplied struct {
	Name     string `json:"name"`
	Package  string `json:"package"`
	Version  string `json:"version"`
	Manifest string `json:"manifest"`
}

// Analyzer is the analyzer member.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type Analyzer struct {
	Name                   string      `json:"name"`
	Version                string      `json:"version"`
	Languages              []string    `json:"languages"`
	SchemaVersionsAccepted []string    `json:"schema_versions_accepted"`
	Conformance            Conformance `json:"conformance"`
}

// Conformance is the analyzer's conformance record.
type Conformance struct {
	CorpusVersion string `json:"corpus_version"`
	Result        string `json:"result"`
	Digest        string `json:"digest"`
}

// Target is the target member.
type Target struct {
	Kind     string `json:"kind"`
	Root     string `json:"root"`
	Identity string `json:"identity"`
}

// Configuration is one entry of the configurations array.
type Configuration struct {
	ID   string   `json:"id"`
	OS   string   `json:"os"`
	Arch string   `json:"arch"`
	Tags []string `json:"tags"`
}

// ConfigurationNotBuilt is one entry of the configurations_not_built array.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type ConfigurationNotBuilt struct {
	ID    string   `json:"id"`
	OS    string   `json:"os"`
	Arch  string   `json:"arch"`
	Tags  []string `json:"tags"`
	Error string   `json:"error"`
}

// Consumers is the consumers member.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type Consumers struct {
	Declared    int                   `json:"declared"`
	Loaded      []LoadedConsumer      `json:"loaded"`
	Unavailable []UnavailableConsumer `json:"unavailable"`
}

// LoadedConsumer is one entry of the loaded consumer list.
type LoadedConsumer struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Path string `json:"path"`
}

// UnavailableConsumer is one entry of the unavailable consumer list.
type UnavailableConsumer struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Reason string `json:"reason"`
}

// Finding is one finding.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type Finding struct {
	Code              string    `json:"code"`
	Kind              string    `json:"kind"`
	Language          string    `json:"language"`
	Position          Position  `json:"position"`
	Symbol            Subject   `json:"symbol"`
	ReachabilityClass string    `json:"reachability_class"`
	Confidence        string    `json:"confidence"`
	LivenessRelation  string    `json:"liveness_relation,omitempty"`
	TestOnly          bool      `json:"test_only"`
	Generated         bool      `json:"generated"`
	Component         Component `json:"component"`
	RetainedBy        []string  `json:"retained_by"`
	Configurations    []string  `json:"configurations"`
	ConsumersLoaded   []string  `json:"consumers_loaded"`
	Fixability        string    `json:"fixability"`
	Severity          string    `json:"severity"`
	Message           string    `json:"message"`
	Details           Details   `json:"details"`
}

// Position is where a finding or a symbol it names is.
type Position struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	EndLine int    `json:"end_line"`
}

// Subject is a finding's symbol member.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type Subject struct {
	Ref       string `json:"ref"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Exported  *bool  `json:"exported,omitempty"`
	SizeLines int    `json:"size_lines"`
}

// Component is a finding's dead component.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type Component struct {
	ID             string       `json:"id"`
	Root           bool         `json:"root"`
	SymbolCount    int          `json:"symbol_count"`
	DeletableLines int          `json:"deletable_lines"`
	Members        []Positioned `json:"members,omitempty"`
}

// Positioned is one symbol a finding names beside its subject: the reference,
// the display name a text line renders, and where the symbol is.
type Positioned struct {
	Ref      string   `json:"ref"`
	Name     string   `json:"name"`
	Position Position `json:"position"`
}

// Details is one finding's per-kind members, in the schema's member order.
type Details struct {
	NarrowerVisibility string        `json:"narrower_visibility,omitempty"`
	Implementations    []Positioned  `json:"implementations,omitzero"`
	WritePositions     []Position    `json:"write_positions,omitempty"`
	ExcludedBy         string        `json:"excluded_by,omitempty"`
	DependencyClass    string        `json:"dependency_class,omitempty"`
	Replacement        string        `json:"replacement,omitempty"`
	Mechanism          string        `json:"mechanism,omitempty"`
	Entry              *DetailsEntry `json:"entry,omitempty"`
	Overlap            []string      `json:"overlap,omitempty"`
	RemovesLastUseOf   []string      `json:"removes_last_use_of,omitempty"`
}

// DetailsEntry is the suppression record a finding about one reports, where a
// member the record lacks is absent rather than empty: that absence is what the
// reason-free and the unscoped kinds report, and the schema admits no empty spelling
// of either member. The array of stale records writes the same four keys with the
// path and the reason required, which is why it has a shape of its own.
type DetailsEntry struct {
	Code   string `json:"code"`
	Symbol string `json:"symbol,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Evaluation is one entry of the edge_evaluations array.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type Evaluation struct {
	Edge    string   `json:"edge"`
	Side    string   `json:"side"`
	Symbol  string   `json:"symbol"`
	State   string   `json:"state"`
	Finding *Finding `json:"finding,omitempty"`
}

// StaleSuppression is one entry of the stale_suppressions array.
//
//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type StaleSuppression struct {
	Code      string              `json:"code"`
	Mechanism string              `json:"mechanism"`
	Entry     SuppressionEntry    `json:"entry"`
	Position  SuppressionPosition `json:"position"`
	Symbol    string              `json:"symbol"`
	Message   string              `json:"message"`
}

// SuppressionEntry is the four-key entry a stale-suppression record names.
type SuppressionEntry struct {
	Code   string `json:"code"`
	Symbol string `json:"symbol,omitempty"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// SuppressionPosition is a stale suppression's own site.
type SuppressionPosition struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// DeclaredGap is one entry of the declared_gaps array.
type DeclaredGap struct {
	Fixture    string `json:"fixture"`
	Symbol     string `json:"symbol,omitempty"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

// TestFileRule is one entry of the test_file_rules array.
type TestFileRule struct {
	Rule    string `json:"rule"`
	Matched int    `json:"matched"`
}

// Totals is the totals member.
type Totals struct {
	Findings             int        `json:"findings"`
	BySeverity           BySeverity `json:"by_severity"`
	DeletableLines       int        `json:"deletable_lines"`
	SuppressionsInEffect int        `json:"suppressions_in_effect"`
	ReasonsRecorded      int        `json:"reasons_recorded"`
	StaleSuppressions    int        `json:"stale_suppressions"`
	Pending              int        `json:"pending"`
	Omitted              int        `json:"omitted"`
	Withheld             Withheld   `json:"withheld"`
}

// Withheld is the count of findings the minimum confidence withheld, per class.
type Withheld struct {
	Certain  int `json:"certain"`
	Probable int `json:"probable"`
	Possible int `json:"possible"`
}

// BySeverity is the count of findings per severity.
type BySeverity struct {
	Allow int `json:"allow"`
	Warn  int `json:"warn"`
	Deny  int `json:"deny"`
}
