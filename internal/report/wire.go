package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// The document below is the Contract's report schema in Go: one field per member,
// in the order the schema declares the members, with the JSON name the schema
// gives each. Every member the schema requires is written whatever its value, and
// every optional member carries omitempty, so an absent member is one the analysis
// has nothing to say about.
//
// The finding of the findings pass carries no JSON name of its own, so the shapes
// here are what the document rests on. Three optional members of a finding's
// symbol (the container's reference, the visibility, and the path inside the
// package's type information), the member list of a component, and the name of the
// analyzer that carried a record are absent from the shapes below because the
// findings pass carries no field for any of them.
//
// The liveness relation is the one member whose presence is a claim rather than a
// value: a finding about a subject the sweep judged live is a finding no relation
// decided, so the member is absent on exactly those and present on every other. A
// document that named a relation there would say a relation decided what none did.

// wireEnvelope is one report document.
type wireEnvelope struct {
	SchemaVersion          string                      `json:"schema_version"`
	ContractVersion        string                      `json:"contract_version"`
	Analyzer               wireAnalyzer                `json:"analyzer"`
	Target                 wireTarget                  `json:"target"`
	Configurations         []wireConfiguration         `json:"configurations"`
	ConfigurationsNotBuilt []wireConfigurationNotBuilt `json:"configurations_not_built"`
	Consumers              wireConsumers               `json:"consumers"`
	Findings               []wireFinding               `json:"findings"`
	EdgeEvaluations        []wireEvaluation            `json:"edge_evaluations"`
	StaleSuppressions      []wireStaleSuppression      `json:"stale_suppressions"`
	DeclaredGaps           []wireDeclaredGap           `json:"declared_gaps"`
	ExcludedByCgo          []string                    `json:"excluded_by_cgo"`
	TestFileRules          []wireTestFileRule          `json:"test_file_rules"`
	Totals                 wireTotals                  `json:"totals"`
}

//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type wireAnalyzer struct {
	Name                   string          `json:"name"`
	Version                string          `json:"version"`
	Languages              []string        `json:"languages"`
	SchemaVersionsAccepted []string        `json:"schema_versions_accepted"`
	Conformance            wireConformance `json:"conformance"`
}

type wireConformance struct {
	CorpusVersion string `json:"corpus_version"`
	Result        string `json:"result"`
	Digest        string `json:"digest"`
}

type wireTarget struct {
	Kind     string `json:"kind"`
	Root     string `json:"root"`
	Identity string `json:"identity"`
}

type wireConfiguration struct {
	ID   string   `json:"id"`
	OS   string   `json:"os"`
	Arch string   `json:"arch"`
	Tags []string `json:"tags"`
}

//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type wireConfigurationNotBuilt struct {
	ID    string   `json:"id"`
	OS    string   `json:"os"`
	Arch  string   `json:"arch"`
	Tags  []string `json:"tags"`
	Error string   `json:"error"`
}

//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type wireConsumers struct {
	Declared    int                      `json:"declared"`
	Loaded      []wireLoadedConsumer     `json:"loaded"`
	Unavailable []wireUnavailableConsume `json:"unavailable"`
}

type wireLoadedConsumer struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Path string `json:"path"`
}

type wireUnavailableConsume struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Reason string `json:"reason"`
}

// consumerRole is the role every entry of both consumer lists carries: the schema
// admits one value, because each entry of either list was declared as a consumer of
// the target.
const consumerRole = "consumer"

//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type wireFinding struct {
	Code              string        `json:"code"`
	Kind              string        `json:"kind"`
	Language          string        `json:"language"`
	Position          wirePosition  `json:"position"`
	Symbol            wireSubject   `json:"symbol"`
	ReachabilityClass string        `json:"reachability_class"`
	Confidence        string        `json:"confidence"`
	LivenessRelation  string        `json:"liveness_relation,omitempty"`
	TestOnly          bool          `json:"test_only"`
	Generated         bool          `json:"generated"`
	Component         wireComponent `json:"component"`
	RetainedBy        []string      `json:"retained_by"`
	Configurations    []string      `json:"configurations"`
	ConsumersLoaded   []string      `json:"consumers_loaded"`
	Fixability        string        `json:"fixability"`
	Severity          string        `json:"severity"`
	Message           string        `json:"message"`
	Details           wireDetails   `json:"details"`
}

type wirePosition struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	EndLine int    `json:"end_line"`
}

type wireSubject struct {
	Ref       string `json:"ref"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	SizeLines int    `json:"size_lines"`
}

type wireComponent struct {
	ID             string `json:"id"`
	Root           bool   `json:"root"`
	SymbolCount    int    `json:"symbol_count"`
	DeletableLines int    `json:"deletable_lines"`
}

// wirePositioned is one symbol a finding names beside its subject: the reference,
// the display name a text line renders, and where the symbol is.
type wirePositioned struct {
	Ref      string       `json:"ref"`
	Name     string       `json:"name"`
	Position wirePosition `json:"position"`
}

// wireDetails is one finding's per-kind members, in the schema's member order.
type wireDetails struct {
	NarrowerVisibility string            `json:"narrower_visibility,omitempty"`
	Implementations    []wirePositioned  `json:"implementations,omitempty"`
	WritePositions     []wirePosition    `json:"write_positions,omitempty"`
	ExcludedBy         string            `json:"excluded_by,omitempty"`
	DependencyClass    string            `json:"dependency_class,omitempty"`
	Replacement        string            `json:"replacement,omitempty"`
	Mechanism          string            `json:"mechanism,omitempty"`
	Entry              *wireDetailsEntry `json:"entry,omitempty"`
	Overlap            []string          `json:"overlap,omitempty"`
	RemovesLastUseOf   []string          `json:"removes_last_use_of,omitempty"`
}

// wireDetailsEntry is the suppression record a finding about one reports, where a
// member the record lacks is absent rather than empty: that absence is what the
// reason-free and the unscoped kinds report, and the schema admits no empty spelling
// of either member. The array of stale records writes the same four keys with the
// path and the reason required, which is why it has a shape of its own.
type wireDetailsEntry struct {
	Code   string `json:"code"`
	Symbol string `json:"symbol,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type wireEvaluation struct {
	Edge    string       `json:"edge"`
	Side    string       `json:"side"`
	Symbol  string       `json:"symbol"`
	State   string       `json:"state"`
	Finding *wireFinding `json:"finding,omitempty"`
}

//nolint:govet // fieldalignment: the field order is the schema's member order, which the document writes
type wireStaleSuppression struct {
	Code      string                  `json:"code"`
	Mechanism string                  `json:"mechanism"`
	Entry     wireSuppressionEntry    `json:"entry"`
	Position  wireSuppressionPosition `json:"position"`
	Symbol    string                  `json:"symbol"`
	Message   string                  `json:"message"`
}

type wireSuppressionEntry struct {
	Code   string `json:"code"`
	Symbol string `json:"symbol,omitempty"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type wireSuppressionPosition struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type wireDeclaredGap struct {
	Fixture    string `json:"fixture"`
	Symbol     string `json:"symbol,omitempty"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type wireTestFileRule struct {
	Rule    string `json:"rule"`
	Matched int    `json:"matched"`
}

type wireTotals struct {
	Findings             int            `json:"findings"`
	BySeverity           wireBySeverity `json:"by_severity"`
	DeletableLines       int            `json:"deletable_lines"`
	SuppressionsInEffect int            `json:"suppressions_in_effect"`
	ReasonsRecorded      int            `json:"reasons_recorded"`
	StaleSuppressions    int            `json:"stale_suppressions"`
	Pending              int            `json:"pending"`
	Omitted              int            `json:"omitted"`
}

type wireBySeverity struct {
	Allow int `json:"allow"`
	Warn  int `json:"warn"`
	Deny  int `json:"deny"`
}

// MarshalJSON writes the envelope as the Contract's report document, so a path that
// marshals an envelope writes that document and no other shape.
//
// The method is on the pointer, as every other method of the envelope is, so an
// envelope is marshalled through one.
func (e *Envelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.wire())
}

// UnmarshalJSON reads one report document this analyzer wrote, refusing a member
// the schema does not declare: an unknown member is a document from a schema
// version or a product this envelope does not model, including the members a
// merged report alone carries, and reading one as though the member were absent
// would lose what it says.
func (e *Envelope) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var read wireEnvelope
	if err := decoder.Decode(&read); err != nil {
		return fmt.Errorf("report: decode a report document: %w", err)
	}
	held, err := read.envelope()
	if err != nil {
		return err
	}
	*e = held
	return nil
}

// wire is the envelope as the document declares it.
func (e *Envelope) wire() wireEnvelope {
	held := wireEnvelope{
		SchemaVersion:   e.SchemaVersion,
		ContractVersion: e.ContractVersion,
		Analyzer: wireAnalyzer{
			Name:                   e.Analyzer.Name,
			Version:                e.Analyzer.Version,
			Languages:              list(e.Analyzer.Languages),
			SchemaVersionsAccepted: list(e.Analyzer.SchemaVersionsAccepted),
			Conformance: wireConformance{
				CorpusVersion: e.Analyzer.Conformance.CorpusVersion,
				Result:        e.Analyzer.Conformance.Result,
				Digest:        e.Analyzer.Conformance.Digest,
			},
		},
		Target:                 wireTarget{Kind: e.Target.Kind, Root: e.Target.Root, Identity: e.Target.Identity},
		Configurations:         make([]wireConfiguration, 0, len(e.Configurations)),
		ConfigurationsNotBuilt: make([]wireConfigurationNotBuilt, 0, len(e.ConfigurationsNotBuilt)),
		Consumers:              wireConsumersOf(&e.Consumers),
		Findings:               make([]wireFinding, 0, len(e.Findings)),
		EdgeEvaluations:        make([]wireEvaluation, 0, len(e.EdgeEvaluations)),
		StaleSuppressions:      make([]wireStaleSuppression, 0, len(e.StaleSuppressions)),
		DeclaredGaps:           make([]wireDeclaredGap, 0, len(e.DeclaredGaps)),
		ExcludedByCgo:          list(e.ExcludedByCgo),
		TestFileRules:          make([]wireTestFileRule, 0, len(e.TestFileRules)),
		Totals: wireTotals{
			Findings:             e.Totals.Findings,
			BySeverity:           wireBySeverity(e.Totals.BySeverity),
			DeletableLines:       e.Totals.DeletableLines,
			SuppressionsInEffect: e.Totals.SuppressionsInEffect,
			ReasonsRecorded:      e.Totals.ReasonsRecorded,
			StaleSuppressions:    e.Totals.StaleSuppressions,
			Pending:              e.Totals.Pending,
			Omitted:              e.Totals.Omitted,
		},
	}
	for i := range e.Configurations {
		one := &e.Configurations[i]
		held.Configurations = append(held.Configurations, wireConfiguration{
			ID: one.ID, OS: one.OS, Arch: one.Arch, Tags: list(one.Tags),
		})
	}
	for i := range e.ConfigurationsNotBuilt {
		one := &e.ConfigurationsNotBuilt[i]
		held.ConfigurationsNotBuilt = append(held.ConfigurationsNotBuilt, wireConfigurationNotBuilt{
			ID: one.ID, OS: one.OS, Arch: one.Arch, Tags: list(one.Tags), Error: one.Error,
		})
	}
	for i := range e.Findings {
		held.Findings = append(held.Findings, wireFindingOf(&e.Findings[i]))
	}
	for i := range e.EdgeEvaluations {
		held.EdgeEvaluations = append(held.EdgeEvaluations, wireEvaluationOf(&e.EdgeEvaluations[i]))
	}
	for i := range e.StaleSuppressions {
		held.StaleSuppressions = append(held.StaleSuppressions, wireStaleSuppressionOf(&e.StaleSuppressions[i]))
	}
	for i := range e.DeclaredGaps {
		one := &e.DeclaredGaps[i]
		held.DeclaredGaps = append(held.DeclaredGaps, wireDeclaredGap{
			Fixture: one.Fixture, Symbol: one.Symbol, Capability: one.Capability, Reason: one.Reason,
		})
	}
	for _, rule := range e.TestFileRules {
		held.TestFileRules = append(held.TestFileRules, wireTestFileRule{Rule: rule.Rule, Matched: rule.Matched})
	}
	return held
}

// wireConsumersOf is the consumer object as the document declares it.
func wireConsumersOf(held *Consumers) wireConsumers {
	written := wireConsumers{
		Declared:    held.Declared,
		Loaded:      make([]wireLoadedConsumer, 0, len(held.Loaded)),
		Unavailable: make([]wireUnavailableConsume, 0, len(held.Unavailable)),
	}
	for _, one := range held.Loaded {
		written.Loaded = append(written.Loaded,
			wireLoadedConsumer{ID: one.ID, Role: consumerRole, Path: one.Path})
	}
	for _, one := range held.Unavailable {
		written.Unavailable = append(written.Unavailable,
			wireUnavailableConsume{ID: one.ID, Role: consumerRole, Reason: one.Reason})
	}
	return written
}

// wireFindingOf is one finding as the document declares it.
func wireFindingOf(found *kinds.Finding) wireFinding {
	return wireFinding{
		Code:              found.Code,
		Kind:              found.Kind,
		Language:          found.Language,
		Position:          wirePositionOf(&found.Position),
		Symbol:            wireSubject(found.Symbol),
		ReachabilityClass: string(found.Class),
		Confidence:        string(found.Confidence),
		LivenessRelation:  relationWritten(found),
		TestOnly:          found.TestOnly,
		Generated:         found.Generated,
		Component:         wireComponent(found.Component),
		RetainedBy:        list(found.RetainedBy),
		Configurations:    list(found.Configurations),
		ConsumersLoaded:   list(found.ConsumersLoaded),
		Fixability:        found.Fixability,
		Severity:          string(found.Severity),
		Message:           found.Message,
		Details:           wireDetailsOf(&found.Details),
	}
}

// relationWritten is the liveness relation one finding's document names, and no
// spelling at all for a finding the Contract carries no relation on: the subject is
// one no relation over declarations answers for, or a declaration the sweep judged
// live, so the member is absent rather than a relation a reader would take for the
// one that decided the finding.
func relationWritten(found *kinds.Finding) string {
	if kinds.LivenessAbsent(found) {
		return ""
	}
	return found.Relation.String()
}

// wirePositionOf is one position as the document declares it.
func wirePositionOf(at *kinds.Position) wirePosition {
	return wirePosition{Path: at.Path, Line: at.Line, Column: at.Column, EndLine: at.EndLine}
}

// wireDetailsOf is one finding's per-kind members, each written only where the
// kind sets it.
func wireDetailsOf(details *kinds.Details) wireDetails {
	held := wireDetails{
		NarrowerVisibility: details.NarrowerVisibility,
		ExcludedBy:         details.ExcludedBy,
		DependencyClass:    details.DependencyClass,
		Replacement:        details.Replacement,
		Mechanism:          details.Mechanism,
		Overlap:            slices.Clone(details.Overlap),
		RemovesLastUseOf:   slices.Clone(details.RemovesLastUseOf),
	}
	if details.Entry != nil {
		held.Entry = &wireDetailsEntry{
			Code:   details.Entry.Code,
			Symbol: details.Entry.Symbol,
			Path:   details.Entry.Path,
			Reason: details.Entry.Reason,
		}
	}
	for _, one := range details.Implementations {
		held.Implementations = append(held.Implementations,
			wirePositioned{Ref: one.Ref, Name: one.Name, Position: wirePositionOf(&one.Position)})
	}
	for i := range details.WritePositions {
		held.WritePositions = append(held.WritePositions, wirePositionOf(&details.WritePositions[i]))
	}
	return held
}

// wireEvaluationOf is one edge evaluation as the document declares it.
func wireEvaluationOf(held *EdgeEvaluation) wireEvaluation {
	written := wireEvaluation{Edge: held.Edge, Side: held.Side, Symbol: held.Symbol, State: held.State}
	if held.Finding != nil {
		pending := wireFindingOf(held.Finding)
		written.Finding = &pending
	}
	return written
}

// wireStaleSuppressionOf is one stale-suppression record as the document declares
// it.
func wireStaleSuppressionOf(held *StaleSuppression) wireStaleSuppression {
	return wireStaleSuppression{
		Code:      held.Code,
		Mechanism: held.Mechanism,
		Entry: wireSuppressionEntry{
			Code:   held.Entry.Code,
			Symbol: held.Entry.Symbol,
			Path:   held.Entry.Path,
			Reason: held.Entry.Reason,
		},
		Position: wireSuppressionPosition{
			Path:   held.Position.Path,
			Line:   held.Position.Line,
			Column: held.Position.Column,
		},
		Symbol:  held.Symbol,
		Message: held.Message,
	}
}

// envelope is the document read back, so a rendering of the result writes the bytes
// it was read from.
func (w *wireEnvelope) envelope() (Envelope, error) {
	held := Envelope{
		SchemaVersion:   w.SchemaVersion,
		ContractVersion: w.ContractVersion,
		Analyzer: Analyzer{
			Name:                   w.Analyzer.Name,
			Version:                w.Analyzer.Version,
			Languages:              list(w.Analyzer.Languages),
			SchemaVersionsAccepted: list(w.Analyzer.SchemaVersionsAccepted),
			Conformance:            Conformance(w.Analyzer.Conformance),
		},
		Target:                 Target(w.Target),
		Configurations:         make([]Configuration, 0, len(w.Configurations)),
		ConfigurationsNotBuilt: make([]ConfigurationNotBuilt, 0, len(w.ConfigurationsNotBuilt)),
		Findings:               make([]kinds.Finding, 0, len(w.Findings)),
		EdgeEvaluations:        make([]EdgeEvaluation, 0, len(w.EdgeEvaluations)),
		StaleSuppressions:      make([]StaleSuppression, 0, len(w.StaleSuppressions)),
		DeclaredGaps:           make([]DeclaredGap, 0, len(w.DeclaredGaps)),
		ExcludedByCgo:          list(w.ExcludedByCgo),
		TestFileRules:          make([]graph.TestFileRule, 0, len(w.TestFileRules)),
		Totals: Totals{
			Findings:             w.Totals.Findings,
			BySeverity:           BySeverity(w.Totals.BySeverity),
			DeletableLines:       w.Totals.DeletableLines,
			SuppressionsInEffect: w.Totals.SuppressionsInEffect,
			ReasonsRecorded:      w.Totals.ReasonsRecorded,
			StaleSuppressions:    w.Totals.StaleSuppressions,
			Pending:              w.Totals.Pending,
			Omitted:              w.Totals.Omitted,
		},
	}
	held.Consumers = Consumers{
		Declared:    w.Consumers.Declared,
		Loaded:      make([]LoadedConsumer, 0, len(w.Consumers.Loaded)),
		Unavailable: make([]UnavailableConsumer, 0, len(w.Consumers.Unavailable)),
	}
	for _, one := range w.Consumers.Loaded {
		held.Consumers.Loaded = append(held.Consumers.Loaded, LoadedConsumer{ID: one.ID, Path: one.Path})
	}
	for _, one := range w.Consumers.Unavailable {
		held.Consumers.Unavailable = append(held.Consumers.Unavailable,
			UnavailableConsumer{ID: one.ID, Reason: one.Reason})
	}
	for i := range w.Configurations {
		one := &w.Configurations[i]
		held.Configurations = append(held.Configurations,
			Configuration{ID: one.ID, OS: one.OS, Arch: one.Arch, Tags: list(one.Tags)})
	}
	for i := range w.ConfigurationsNotBuilt {
		one := &w.ConfigurationsNotBuilt[i]
		held.ConfigurationsNotBuilt = append(held.ConfigurationsNotBuilt, ConfigurationNotBuilt{
			ID: one.ID, OS: one.OS, Arch: one.Arch, Tags: list(one.Tags), Error: one.Error,
		})
	}
	for i := range w.Findings {
		found, err := w.Findings[i].finding()
		if err != nil {
			return Envelope{}, err
		}
		held.Findings = append(held.Findings, found)
	}
	for i := range w.EdgeEvaluations {
		evaluated, err := w.EdgeEvaluations[i].evaluation()
		if err != nil {
			return Envelope{}, err
		}
		held.EdgeEvaluations = append(held.EdgeEvaluations, evaluated)
	}
	for i := range w.StaleSuppressions {
		held.StaleSuppressions = append(held.StaleSuppressions, w.StaleSuppressions[i].staleSuppression())
	}
	for _, one := range w.DeclaredGaps {
		held.DeclaredGaps = append(held.DeclaredGaps, DeclaredGap(one))
	}
	for _, rule := range w.TestFileRules {
		held.TestFileRules = append(held.TestFileRules, graph.TestFileRule(rule))
	}
	return held, nil
}

// finding is one finding read back from the document.
func (w *wireFinding) finding() (kinds.Finding, error) {
	relation, err := relationOf(w.LivenessRelation)
	if err != nil {
		return kinds.Finding{}, err
	}
	return kinds.Finding{
		Code:            w.Code,
		Kind:            w.Kind,
		Language:        w.Language,
		Position:        kinds.Position(w.Position),
		Symbol:          kinds.Subject(w.Symbol),
		Class:           kinds.Class(w.ReachabilityClass),
		Confidence:      kinds.Class(w.Confidence),
		Relation:        relation,
		Live:            w.LivenessRelation == "",
		TestOnly:        w.TestOnly,
		Generated:       w.Generated,
		Component:       kinds.Component(w.Component),
		RetainedBy:      list(w.RetainedBy),
		Configurations:  list(w.Configurations),
		ConsumersLoaded: list(w.ConsumersLoaded),
		Fixability:      w.Fixability,
		Severity:        config.Severity(w.Severity),
		Message:         w.Message,
		Details:         w.Details.details(),
	}, nil
}

// details is one finding's per-kind members read back from the document.
func (w *wireDetails) details() kinds.Details {
	held := kinds.Details{
		NarrowerVisibility: w.NarrowerVisibility,
		ExcludedBy:         w.ExcludedBy,
		DependencyClass:    w.DependencyClass,
		Replacement:        w.Replacement,
		Mechanism:          w.Mechanism,
		Overlap:            slices.Clone(w.Overlap),
		RemovesLastUseOf:   slices.Clone(w.RemovesLastUseOf),
	}
	if w.Entry != nil {
		held.Entry = &kinds.Entry{
			Code:   w.Entry.Code,
			Symbol: w.Entry.Symbol,
			Path:   w.Entry.Path,
			Reason: w.Entry.Reason,
		}
	}
	for _, one := range w.Implementations {
		held.Implementations = append(held.Implementations,
			kinds.Positioned{Ref: one.Ref, Name: one.Name, Position: kinds.Position(one.Position)})
	}
	for _, at := range w.WritePositions {
		held.WritePositions = append(held.WritePositions, kinds.Position(at))
	}
	return held
}

// evaluation is one edge evaluation read back from the document.
func (w *wireEvaluation) evaluation() (EdgeEvaluation, error) {
	held := EdgeEvaluation{Edge: w.Edge, Side: w.Side, Symbol: w.Symbol, State: w.State}
	if w.Finding == nil {
		return held, nil
	}
	pending, err := w.Finding.finding()
	if err != nil {
		return EdgeEvaluation{}, err
	}
	held.Finding = &pending
	return held, nil
}

// staleSuppression is one stale-suppression record read back from the document.
func (w *wireStaleSuppression) staleSuppression() StaleSuppression {
	return StaleSuppression{
		Code:      w.Code,
		Mechanism: w.Mechanism,
		Entry:     SuppressionEntry(w.Entry),
		Position:  SuppressionPosition(w.Position),
		Symbol:    w.Symbol,
		Message:   w.Message,
	}
}

// relationOf is the liveness relation one document spells, and a refusal for a
// spelling the vocabulary does not hold: reading an unknown relation as the first
// of the two would put a relation in the envelope that the document does not name.
//
// A document that names none is a finding about a live subject, which the reader
// marks live rather than giving a relation, so the first of the two is the value
// the field holds and the mark is what says it decided nothing.
func relationOf(spelled string) (graph.Relation, error) {
	if spelled == "" {
		return graph.ReferenceCounting, nil
	}
	for _, relation := range []graph.Relation{graph.ReferenceCounting, graph.Reachability} {
		if relation.String() == spelled {
			return relation, nil
		}
	}
	return 0, fmt.Errorf("report: decode a report document: %q names no liveness relation", spelled)
}

// list is one copy of a string list, and an empty list where it holds none: the
// Contract admits no absent array, so a member a run has nothing for is written
// empty rather than null.
func list(held []string) []string {
	if len(held) == 0 {
		return []string{}
	}
	return slices.Clone(held)
}
