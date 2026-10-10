// Package reporttest reads back a report document the report package wrote, which
// is what a test that runs the analyzer end to end and a round trip of the document
// compare against.
package reporttest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/report"
	"github.com/cplieger/deadset-go/internal/reportdoc"
)

// subjectOf is one finding's subject read back from the document.
func subjectOf(w *reportdoc.Subject) kinds.Subject {
	return kinds.Subject{Ref: w.Ref, Kind: w.Kind, Name: w.Name, Exported: w.Exported, SizeLines: w.SizeLines}
}

// Read reads one report document this analyzer wrote, refusing a member the schema
// does not declare: an unknown member is a document from a schema version or a
// product the envelope does not model, including the members a merged report alone
// carries, and reading one as though the member were absent would lose what it says.
// Anything after the document is refused too.
func Read(data []byte) (report.Envelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var read reportdoc.Envelope
	if err := decoder.Decode(&read); err != nil {
		return report.Envelope{}, fmt.Errorf("reporttest: decode a report document: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return report.Envelope{}, errors.New("reporttest: decode a report document: content after the document")
	}
	return envelopeOf(&read)
}

// envelopeOf is the document read back, so a rendering of the result writes the bytes
// it was read from.
func envelopeOf(w *reportdoc.Envelope) (report.Envelope, error) {
	held := report.Envelope{
		SchemaVersion:   w.SchemaVersion,
		ContractVersion: w.ContractVersion,
		Analyzer: report.Analyzer{
			Name:                   w.Analyzer.Name,
			Version:                w.Analyzer.Version,
			Languages:              list(w.Analyzer.Languages),
			SchemaVersionsAccepted: list(w.Analyzer.SchemaVersionsAccepted),
			Conformance:            report.Conformance(w.Analyzer.Conformance),
		},
		Target:                 report.Target(w.Target),
		Configurations:         make([]report.Configuration, 0, len(w.Configurations)),
		ConfigurationsNotBuilt: make([]report.ConfigurationNotBuilt, 0, len(w.ConfigurationsNotBuilt)),
		Findings:               make([]kinds.Finding, 0, len(w.Findings)),
		EdgeEvaluations:        make([]report.EdgeEvaluation, 0, len(w.EdgeEvaluations)),
		StaleSuppressions:      make([]report.StaleSuppression, 0, len(w.StaleSuppressions)),
		DeclaredGaps:           make([]report.DeclaredGap, 0, len(w.DeclaredGaps)),
		ExcludedByCgo:          list(w.ExcludedByCgo),
		TestFileRules:          make([]graph.TestFileRule, 0, len(w.TestFileRules)),
		Totals: report.Totals{
			Findings:             w.Totals.Findings,
			BySeverity:           report.BySeverity(w.Totals.BySeverity),
			DeletableLines:       w.Totals.DeletableLines,
			SuppressionsInEffect: w.Totals.SuppressionsInEffect,
			ReasonsRecorded:      w.Totals.ReasonsRecorded,
			StaleSuppressions:    w.Totals.StaleSuppressions,
			Pending:              w.Totals.Pending,
			Omitted:              w.Totals.Omitted,
			Withheld:             report.Withheld(w.Totals.Withheld),
		},
	}
	held.Consumers = report.Consumers{
		Declared:    w.Consumers.Declared,
		Loaded:      make([]report.LoadedConsumer, 0, len(w.Consumers.Loaded)),
		Unavailable: make([]report.UnavailableConsumer, 0, len(w.Consumers.Unavailable)),
	}
	for _, one := range w.Consumers.Loaded {
		held.Consumers.Loaded = append(held.Consumers.Loaded, report.LoadedConsumer{ID: one.ID, Path: one.Path})
	}
	for _, one := range w.Consumers.Unavailable {
		held.Consumers.Unavailable = append(held.Consumers.Unavailable,
			report.UnavailableConsumer{ID: one.ID, Reason: one.Reason})
	}
	for i := range w.Configurations {
		one := &w.Configurations[i]
		held.Configurations = append(held.Configurations,
			report.Configuration{ID: one.ID, OS: one.OS, Arch: one.Arch, Tags: list(one.Tags)})
	}
	for i := range w.ConfigurationsNotBuilt {
		one := &w.ConfigurationsNotBuilt[i]
		held.ConfigurationsNotBuilt = append(held.ConfigurationsNotBuilt, report.ConfigurationNotBuilt{
			ID: one.ID, OS: one.OS, Arch: one.Arch, Tags: list(one.Tags), Error: one.Error,
		})
	}
	for i := range w.Findings {
		found, err := findingOf(&w.Findings[i])
		if err != nil {
			return report.Envelope{}, err
		}
		held.Findings = append(held.Findings, found)
	}
	for i := range w.EdgeEvaluations {
		evaluated, err := evaluationOf(&w.EdgeEvaluations[i])
		if err != nil {
			return report.Envelope{}, err
		}
		held.EdgeEvaluations = append(held.EdgeEvaluations, evaluated)
	}
	for i := range w.StaleSuppressions {
		held.StaleSuppressions = append(held.StaleSuppressions, staleSuppressionOf(&w.StaleSuppressions[i]))
	}
	for _, one := range w.DeclaredGaps {
		held.DeclaredGaps = append(held.DeclaredGaps, report.DeclaredGap(one))
	}
	for _, rule := range w.TestFileRules {
		held.TestFileRules = append(held.TestFileRules, graph.TestFileRule(rule))
	}
	held.TypeErrorSkips = readBack(w.TypeErrorSkips, func(one reportdoc.TypeErrorSkip) report.TypeErrorSkip {
		return report.TypeErrorSkip{Path: one.Path, Line: one.Line, Message: one.Message}
	})
	held.Notes = readBack(w.Notes, func(one reportdoc.Note) report.Note { return report.Note(one) })
	held.UnansweredQuestions = readBack(w.UnansweredQuestions, func(one reportdoc.UnansweredQuestion) report.UnansweredQuestion {
		return report.UnansweredQuestion(one)
	})
	held.ConventionsApplied = readBack(w.ConventionsApplied, func(one reportdoc.ConventionApplied) report.ConventionApplied {
		return report.ConventionApplied(one)
	})
	return held, nil
}

// readBack converts every record of one array of the document, into an empty
// rather than a nil slice so a document read back writes the array it held.
func readBack[W, T any](records []W, convert func(W) T) []T {
	held := make([]T, 0, len(records))
	for _, one := range records {
		held = append(held, convert(one))
	}
	return held
}

// findingOf is one finding read back from the document.
func findingOf(w *reportdoc.Finding) (kinds.Finding, error) {
	relation, err := relationOf(w.LivenessRelation)
	if err != nil {
		return kinds.Finding{}, err
	}
	return kinds.Finding{
		Code:            w.Code,
		Kind:            w.Kind,
		Language:        w.Language,
		Position:        kinds.Position(w.Position),
		Symbol:          subjectOf(&w.Symbol),
		Class:           kinds.Class(w.ReachabilityClass),
		Confidence:      kinds.Class(w.Confidence),
		Relation:        relation,
		Live:            w.LivenessRelation == "",
		TestOnly:        w.TestOnly,
		Generated:       w.Generated,
		Component:       componentOf(&w.Component),
		RetainedBy:      list(w.RetainedBy),
		Configurations:  list(w.Configurations),
		ConsumersLoaded: list(w.ConsumersLoaded),
		Fixability:      w.Fixability,
		Severity:        config.Severity(w.Severity),
		Message:         w.Message,
		Details:         detailsOf(&w.Details),
	}, nil
}

// componentOf is one finding's component read back from the document, which carries
// no spans.
func componentOf(w *reportdoc.Component) kinds.Component {
	held := kinds.Component{ID: w.ID, Root: w.Root, SymbolCount: w.SymbolCount, DeletableLines: w.DeletableLines}
	for _, member := range w.Members {
		held.Members = append(held.Members, kinds.Positioned{
			Ref: member.Ref, Name: member.Name, Position: kinds.Position(member.Position),
		})
	}
	return held
}

// detailsOf is one finding's per-kind members read back from the document.
func detailsOf(w *reportdoc.Details) kinds.Details {
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
	if w.Implementations != nil {
		held.Implementations = make([]kinds.Positioned, 0, len(w.Implementations))
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

// evaluationOf is one edge evaluation read back from the document.
func evaluationOf(w *reportdoc.Evaluation) (report.EdgeEvaluation, error) {
	held := report.EdgeEvaluation{Edge: w.Edge, Side: w.Side, Symbol: w.Symbol, State: w.State}
	if w.Finding == nil {
		return held, nil
	}
	pending, err := findingOf(w.Finding)
	if err != nil {
		return report.EdgeEvaluation{}, err
	}
	held.Finding = &pending
	return held, nil
}

// staleSuppressionOf is one stale-suppression record read back from the document.
func staleSuppressionOf(w *reportdoc.StaleSuppression) report.StaleSuppression {
	return report.StaleSuppression{
		Code:      w.Code,
		Mechanism: w.Mechanism,
		Entry:     report.SuppressionEntry(w.Entry),
		Position:  report.SuppressionPosition(w.Position),
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
	return 0, fmt.Errorf("reporttest: decode a report document: %q names no liveness relation", spelled)
}

// list is one copy of a string list, and an empty list where it holds none: the
// Contract admits no absent array, so a list read back writes the array it held.
func list(held []string) []string {
	if len(held) == 0 {
		return []string{}
	}
	return slices.Clone(held)
}
