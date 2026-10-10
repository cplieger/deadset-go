package report

import (
	"bytes"
	"slices"

	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/reportdoc"
)

func wireTypeErrorSkip(s TypeErrorSkip) reportdoc.TypeErrorSkip {
	return reportdoc.TypeErrorSkip{Path: s.Path, Line: s.Line, Message: s.Message}
}

func wireNote(n Note) reportdoc.Note { return reportdoc.Note(n) }

func wireUnansweredQuestion(q UnansweredQuestion) reportdoc.UnansweredQuestion {
	return reportdoc.UnansweredQuestion(q)
}

func wireConventionApplied(c ConventionApplied) reportdoc.ConventionApplied {
	return reportdoc.ConventionApplied(c)
}

// consumerRole is the role every entry of both consumer lists carries: the schema
// admits one value, because each entry of either list was declared as a consumer of
// the target.
const consumerRole = "consumer"

// wireSubjectOf is one finding's subject as a document writes it.
func wireSubjectOf(s *kinds.Subject) reportdoc.Subject {
	return reportdoc.Subject{Ref: s.Ref, Kind: s.Kind, Name: s.Name, Exported: s.Exported, SizeLines: s.SizeLines}
}

// wireComponentOf is one component as a document writes it: the count of the lines
// its deletion removes and not the spans that count was taken over, and the member
// list where the run lists a component in full.
func wireComponentOf(c *kinds.Component) reportdoc.Component {
	held := reportdoc.Component{ID: c.ID, Root: c.Root, SymbolCount: c.SymbolCount, DeletableLines: c.DeletableLines}
	for i := range c.Members {
		one := &c.Members[i]
		held.Members = append(held.Members,
			reportdoc.Positioned{Ref: one.Ref, Name: one.Name, Position: wirePositionOf(&one.Position)})
	}
	return held
}

// wireDeclaredGapOf is one declared gap as a document writes it.
func wireDeclaredGapOf(g *DeclaredGap) reportdoc.DeclaredGap {
	return reportdoc.DeclaredGap{Fixture: g.Fixture, Symbol: g.Symbol, Capability: g.Capability, Reason: g.Reason}
}

// MarshalJSON writes the envelope as the Contract's report document, so a path that
// marshals an envelope writes that document and no other shape.
//
// The method is on the pointer, as every other method of the envelope is, so an
// envelope is marshalled through one.
func (e *Envelope) MarshalJSON() ([]byte, error) {
	written, err := encoded(e.wire(), "")
	return bytes.TrimSuffix(written, []byte("\n")), err
}

// wire is the envelope as the document declares it.
func (e *Envelope) wire() reportdoc.Envelope {
	held := reportdoc.Envelope{
		SchemaVersion:   e.SchemaVersion,
		ContractVersion: e.ContractVersion,
		Analyzer: reportdoc.Analyzer{
			Name:                   e.Analyzer.Name,
			Version:                e.Analyzer.Version,
			Languages:              list(e.Analyzer.Languages),
			SchemaVersionsAccepted: list(e.Analyzer.SchemaVersionsAccepted),
			Conformance: reportdoc.Conformance{
				CorpusVersion: e.Analyzer.Conformance.CorpusVersion,
				Result:        e.Analyzer.Conformance.Result,
				Digest:        e.Analyzer.Conformance.Digest,
			},
		},
		Target:                 reportdoc.Target{Kind: e.Target.Kind, Root: e.Target.Root, Identity: e.Target.Identity},
		Configurations:         make([]reportdoc.Configuration, 0, len(e.Configurations)),
		ConfigurationsNotBuilt: make([]reportdoc.ConfigurationNotBuilt, 0, len(e.ConfigurationsNotBuilt)),
		Consumers:              wireConsumersOf(&e.Consumers),
		Findings:               make([]reportdoc.Finding, 0, len(e.Findings)),
		EdgeEvaluations:        make([]reportdoc.Evaluation, 0, len(e.EdgeEvaluations)),
		StaleSuppressions:      make([]reportdoc.StaleSuppression, 0, len(e.StaleSuppressions)),
		DeclaredGaps:           make([]reportdoc.DeclaredGap, 0, len(e.DeclaredGaps)),
		ExcludedByCgo:          list(e.ExcludedByCgo),
		TestFileRules:          make([]reportdoc.TestFileRule, 0, len(e.TestFileRules)),
		TypeErrorSkips:         make([]reportdoc.TypeErrorSkip, 0, len(e.TypeErrorSkips)),
		Notes:                  make([]reportdoc.Note, 0, len(e.Notes)),
		UnansweredQuestions:    make([]reportdoc.UnansweredQuestion, 0, len(e.UnansweredQuestions)),
		ConventionsApplied:     make([]reportdoc.ConventionApplied, 0, len(e.ConventionsApplied)),
		Totals: reportdoc.Totals{
			Findings:             e.Totals.Findings,
			BySeverity:           reportdoc.BySeverity(e.Totals.BySeverity),
			DeletableLines:       e.Totals.DeletableLines,
			SuppressionsInEffect: e.Totals.SuppressionsInEffect,
			ReasonsRecorded:      e.Totals.ReasonsRecorded,
			StaleSuppressions:    e.Totals.StaleSuppressions,
			Pending:              e.Totals.Pending,
			Omitted:              e.Totals.Omitted,
			Withheld:             reportdoc.Withheld(e.Totals.Withheld),
		},
	}
	for i := range e.Configurations {
		one := &e.Configurations[i]
		held.Configurations = append(held.Configurations, reportdoc.Configuration{
			ID: one.ID, OS: one.OS, Arch: one.Arch, Tags: list(one.Tags),
		})
	}
	for i := range e.ConfigurationsNotBuilt {
		one := &e.ConfigurationsNotBuilt[i]
		held.ConfigurationsNotBuilt = append(held.ConfigurationsNotBuilt, reportdoc.ConfigurationNotBuilt{
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
		held.DeclaredGaps = append(held.DeclaredGaps, wireDeclaredGapOf(one))
	}
	for _, rule := range e.TestFileRules {
		held.TestFileRules = append(held.TestFileRules, reportdoc.TestFileRule{Rule: rule.Rule, Matched: rule.Matched})
	}
	for _, one := range e.TypeErrorSkips {
		held.TypeErrorSkips = append(held.TypeErrorSkips, wireTypeErrorSkip(one))
	}
	for _, one := range e.Notes {
		held.Notes = append(held.Notes, wireNote(one))
	}
	for _, one := range e.UnansweredQuestions {
		held.UnansweredQuestions = append(held.UnansweredQuestions, wireUnansweredQuestion(one))
	}
	for _, one := range e.ConventionsApplied {
		held.ConventionsApplied = append(held.ConventionsApplied, wireConventionApplied(one))
	}
	return held
}

// wireConsumersOf is the consumer object as the document declares it.
func wireConsumersOf(held *Consumers) reportdoc.Consumers {
	written := reportdoc.Consumers{
		Declared:    held.Declared,
		Loaded:      make([]reportdoc.LoadedConsumer, 0, len(held.Loaded)),
		Unavailable: make([]reportdoc.UnavailableConsumer, 0, len(held.Unavailable)),
	}
	for _, one := range held.Loaded {
		written.Loaded = append(written.Loaded,
			reportdoc.LoadedConsumer{ID: one.ID, Role: consumerRole, Path: one.Path})
	}
	for _, one := range held.Unavailable {
		written.Unavailable = append(written.Unavailable,
			reportdoc.UnavailableConsumer{ID: one.ID, Role: consumerRole, Reason: one.Reason})
	}
	return written
}

// wireFindingOf is one finding as the document declares it.
func wireFindingOf(found *kinds.Finding) reportdoc.Finding {
	return reportdoc.Finding{
		Code:              found.Code,
		Kind:              found.Kind,
		Language:          found.Language,
		Position:          wirePositionOf(&found.Position),
		Symbol:            wireSubjectOf(&found.Symbol),
		ReachabilityClass: string(found.Class),
		Confidence:        string(found.Confidence),
		LivenessRelation:  relationWritten(found),
		TestOnly:          found.TestOnly,
		Generated:         found.Generated,
		Component:         wireComponentOf(&found.Component),
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
func wirePositionOf(at *kinds.Position) reportdoc.Position {
	return reportdoc.Position{Path: at.Path, Line: at.Line, Column: at.Column, EndLine: at.EndLine}
}

// wireDetailsOf is one finding's per-kind members, each written only where the
// kind sets it.
func wireDetailsOf(details *kinds.Details) reportdoc.Details {
	held := reportdoc.Details{
		NarrowerVisibility: details.NarrowerVisibility,
		ExcludedBy:         details.ExcludedBy,
		DependencyClass:    details.DependencyClass,
		Replacement:        details.Replacement,
		Mechanism:          details.Mechanism,
		Overlap:            slices.Clone(details.Overlap),
		RemovesLastUseOf:   slices.Clone(details.RemovesLastUseOf),
	}
	if details.Entry != nil {
		held.Entry = &reportdoc.DetailsEntry{
			Code:   details.Entry.Code,
			Symbol: details.Entry.Symbol,
			Path:   details.Entry.Path,
			Reason: details.Entry.Reason,
		}
	}
	if details.Implementations != nil {
		held.Implementations = make([]reportdoc.Positioned, 0, len(details.Implementations))
	}
	for _, one := range details.Implementations {
		held.Implementations = append(held.Implementations,
			reportdoc.Positioned{Ref: one.Ref, Name: one.Name, Position: wirePositionOf(&one.Position)})
	}
	for i := range details.WritePositions {
		held.WritePositions = append(held.WritePositions, wirePositionOf(&details.WritePositions[i]))
	}
	return held
}

// wireEvaluationOf is one edge evaluation as the document declares it.
func wireEvaluationOf(held *EdgeEvaluation) reportdoc.Evaluation {
	written := reportdoc.Evaluation{Edge: held.Edge, Side: held.Side, Symbol: held.Symbol, State: held.State}
	if held.Finding != nil {
		pending := wireFindingOf(held.Finding)
		written.Finding = &pending
	}
	return written
}

// wireStaleSuppressionOf is one stale-suppression record as the document declares
// it.
func wireStaleSuppressionOf(held *StaleSuppression) reportdoc.StaleSuppression {
	return reportdoc.StaleSuppression{
		Code:      held.Code,
		Mechanism: held.Mechanism,
		Entry: reportdoc.SuppressionEntry{
			Code:   held.Entry.Code,
			Symbol: held.Entry.Symbol,
			Path:   held.Entry.Path,
			Reason: held.Entry.Reason,
		},
		Position: reportdoc.SuppressionPosition{
			Path:   held.Position.Path,
			Line:   held.Position.Line,
			Column: held.Position.Column,
		},
		Symbol:  held.Symbol,
		Message: held.Message,
	}
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
