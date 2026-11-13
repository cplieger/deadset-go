package findings

import (
	"cmp"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-spec/v8/report"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// decide is the suppression ledger over the findings the run would produce were no
// record there (suppression.md "Staleness"): a finding carrying a liveness relation
// is read from the pass over the sweep with every mark withdrawn, any other from
// this pass before any record withholds it. A finding a type error skips is read
// only as withheld, so a record naming it alone is dormant. It returns the ledger and
// the findings it decided against.
func (in *pass) decide(found, skipped []Finding, emitters map[vocab.Code]Emitter) (*core.Ledger, []core.Finding, error) {
	if len(in.Marks) == 0 {
		return core.Decide(nil, nil, nil), nil, nil
	}
	would, wouldSkipped := found, skipped
	from := in
	if in.Unmarked != nil {
		from = in.withdrawn()
		var err error
		if would, wouldSkipped, err = from.phase(livenessKinds(), emitters); err != nil {
			return nil, nil, err
		}
		from.cohere(would)
		from.dialed = from.dial(would)
	}

	var findings []core.Finding
	add := func(p *pass, all []Finding, liveness, skip bool) {
		least := p.Config.Analysis.MinConfidence
		for i := range all {
			one := &all[i]
			if carriesLiveness(one) != liveness {
				continue
			}
			read := p.ledgerFinding(one)
			read.Confidence = one.Confidence
			read.Withheld = core.WithheldByTypeError
			if !skip {
				read.Withheld = p.dialed.withholding(one, least)
			}
			findings = append(findings, *read)
		}
	}
	add(in, found, false, false)
	add(in, skipped, false, true)
	add(from, would, true, false)
	add(from, wouldSkipped, true, true)

	return core.Decide(in.Marks, findings, &core.Dials{
		Allows: func(code vocab.Code) bool {
			return code.Live() && in.Config.EffectiveSeverity(code) == vocab.Allow
		},
	}), findings, nil
}

// withdrawn is the pass over the sweep with every mark withdrawn, sharing every fact
// no sweep decides. It reports a member of a dead container on its own, because a
// record on the member is decided against the member's own finding.
func (in *pass) withdrawn() *pass {
	input := *in.Input
	input.Sweep = in.Unmarked
	p := *in
	p.Input = &input
	p.withdrawnMarks = true
	p.candidates = make(map[inventory.SymbolID]*liveness.Candidate)
	p.components = make(map[inventory.SymbolID]int)
	p.retained = make(map[inventory.SymbolID]bool)
	p.listed = nil
	p.holdSweep(input.Sweep)
	p.unused = unusedByCode(&p)
	return &p
}

// livenessKinds is the published kinds whose finding carries a liveness relation.
func livenessKinds() []vocab.Kind {
	published := vocabularyKinds()
	held := make([]vocab.Kind, 0, len(published))
	for i := range published {
		if row := &published[i]; !row.HoldsLive && row.Code.Subject() == vocab.SubjectDeclaration {
			held = append(held, *row)
		}
	}
	return held
}

// carriesLiveness reports whether a finding is about a declaration of the
// inventory that a liveness relation decided, which the marks change.
func carriesLiveness(found *Finding) bool {
	return !found.local && shapeOf(found.Symbol.Kind) == shapeDeclaration && !holdsLive(found.Code)
}

// ledgerFinding is one finding as the ledger reads it.
func (in *pass) ledgerFinding(found *Finding) *core.Finding {
	holder := ""
	if ref := found.DeclarationRef(); ref != found.Symbol.Ref {
		holder = ref
	}
	decl := cmp.Or(found.id, found.decl)
	file := ""
	if symbol := in.symbols[decl]; symbol != nil {
		file = symbol.File
	}
	alternatives := in.interfaces.named[found.id]
	named := make([]string, 0, len(alternatives))
	for _, id := range alternatives {
		named = append(named, string(id))
	}
	return &core.Finding{
		Code:       found.Code,
		SymbolKind: found.Symbol.Kind,
		Ref:        found.Symbol.Ref,
		Holder:     holder,
		Path:       found.Position.Path,
		File:       file,
		Decl:       string(decl),
		ID:         key(found),
		Named:      named,
	}
}

// stale is one record per site whose every record the ledger found stale, in
// reading order, naming every stale code of the site, because a maintainer edits
// one line, then [core.Beside] over would as the ledger decided it.
func (in *pass) stale(ledger *core.Ledger, would []core.Finding) []report.StaleSuppression {
	type site struct {
		mechanism core.Mechanism
		at        core.Site
	}
	records := core.StaleRecords(in.Marks, ledger)
	by := make(map[site][]core.Record, len(records))
	var order []site
	for i := range records {
		at := site{records[i].Mechanism, records[i].Site}
		if _, held := by[at]; !held {
			order = append(order, at)
		}
		by[at] = append(by[at], records[i])
	}
	stale := make([]report.StaleSuppression, 0, len(order))
	for _, at := range order {
		first := &by[at][0]
		ref := in.suppressionRef(first.Symbol, first.Path)
		message := written(first.Mechanism) + " for " + spellCodes(codesAt(by[at])) + " on " + ref + " matches no current finding" +
			core.Beside(by[at], would, ledger)
		stale = append(stale, report.StaleSuppression{
			Code:      vocab.StaleSuppression,
			Mechanism: string(first.Mechanism),
			Entry:     report.SuppressionEntry{Code: first.Code, Symbol: first.Symbol, Path: first.Path, Reason: first.Reason},
			Position:  report.SuppressionPosition{Path: first.Site.Path, Line: first.Site.Line, Column: first.Site.Column},
			Symbol:    ref,
			Message:   message,
		})
	}
	return stale
}
