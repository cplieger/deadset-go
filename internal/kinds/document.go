package kinds

import (
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Document is f as the report's finding: the liveness relation absent where
// [livenessAbsent] says no relation decided it, and the component without the
// spans and members a report carries elsewhere.
func (f *Finding) Document() report.Finding {
	written := report.Finding{
		Code:              f.Code,
		Kind:              f.Kind,
		Language:          f.Language,
		Position:          f.Position.document(),
		Symbol:            report.Symbol{Ref: f.Symbol.Ref, Kind: f.Symbol.Kind, Name: f.Symbol.Name, Exported: f.Symbol.Exported, SizeLines: f.Symbol.SizeLines},
		ReachabilityClass: f.Class,
		Confidence:        f.Confidence,
		TestOnly:          f.TestOnly,
		Generated:         f.Generated,
		Component: report.Component{
			ID: f.Component.ID, Root: f.Component.Root,
			SymbolCount: f.Component.SymbolCount, DeletableLines: f.Component.DeletableLines,
		},
		RetainedBy:      documentList(f.RetainedBy),
		Configurations:  documentList(f.Configurations),
		ConsumersLoaded: documentList(f.ConsumersLoaded),
		Fixability:      f.Fixability,
		Severity:        f.Severity,
		Message:         f.Message,
		Details:         f.Details.document(),
	}
	if !livenessAbsent(f) {
		written.LivenessRelation = report.Relation(f.Relation.String())
	}
	return written
}

// orderKey is the part of f's report finding the report's order reads.
func (f *Finding) orderKey() report.Finding {
	return report.Finding{Position: f.Position.document(), Code: f.Code, Symbol: report.Symbol{Ref: f.Symbol.Ref}}
}

// Document is e as the report's edge evaluation.
func (e *Evaluation) Document() report.EdgeEvaluation {
	written := report.EdgeEvaluation{Edge: e.Edge, Side: report.Side(e.Side), Symbol: e.Symbol, State: report.State(e.State)}
	if e.Finding != nil {
		pending := e.Finding.Document()
		written.Finding = &pending
	}
	return written
}

// Listing is the members of every component the findings and the evaluations'
// findings list in full, by identifier, and nil where none is listed in full.
func Listing(findings []Finding, evaluations []Evaluation) map[string][]report.Positioned {
	var members map[string][]report.Positioned
	add := func(c *findingComponent) {
		if c.Members == nil {
			return
		}
		if members == nil {
			members = map[string][]report.Positioned{}
		}
		held := make([]report.Positioned, len(c.Members))
		for i := range c.Members {
			held[i] = report.Positioned{Ref: c.Members[i].Ref, Name: c.Members[i].Name, Position: c.Members[i].Position.document()}
		}
		members[c.ID] = held
	}
	for i := range findings {
		add(&findings[i].Component)
	}
	for i := range evaluations {
		if evaluations[i].Finding != nil {
			add(&evaluations[i].Finding.Component)
		}
	}
	return members
}

// WithheldOf counts the withheld findings by the confidence each carries.
func WithheldOf(findings []Finding) report.Withheld {
	var counted report.Withheld
	for i := range findings {
		switch findings[i].Confidence {
		case vocab.Certain:
			counted.Certain++
		case vocab.Probable:
			counted.Probable++
		case vocab.Possible:
			counted.Possible++
		}
	}
	return counted
}

func (p *Position) document() report.Position {
	return report.Position{Path: p.Path, Line: p.Line, Column: p.Column, EndLine: p.EndLine}
}

func (d *Details) document() report.Details {
	written := report.Details{
		NarrowerVisibility: d.NarrowerVisibility,
		ExcludedBy:         d.ExcludedBy,
		DependencyClass:    d.DependencyClass,
		Replacement:        d.Replacement,
		Mechanism:          d.Mechanism,
		Overlap:            omitted(d.Overlap),
		RemovesLastUseOf:   omitted(d.RemovesLastUseOf),
	}
	if d.Entry != nil {
		written.Entry = &report.Entry{Code: d.Entry.Code, Symbol: d.Entry.Symbol, Path: d.Entry.Path, Reason: d.Entry.Reason}
	}
	if d.Implementations != nil {
		written.Implementations = make([]report.Positioned, len(d.Implementations))
		for i := range d.Implementations {
			one := &d.Implementations[i]
			written.Implementations[i] = report.Positioned{Ref: one.Ref, Name: one.Name, Position: one.Position.document()}
		}
	}
	for i := range d.WritePositions {
		written.WritePositions = append(written.WritePositions, d.WritePositions[i].document())
	}
	if d.NameLiteral != nil {
		at := d.NameLiteral.document()
		written.NameLiteral = &at
	}
	return written
}

// documentList is items, or an empty list where it holds none: the schemas admit no
// absent array.
func documentList(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// omitted is items, or nil where it holds none, so an optional member with
// nothing to say is left out rather than written empty.
func omitted(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	return items
}
