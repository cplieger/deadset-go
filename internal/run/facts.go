package run

import (
	"context"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	specfacts "github.com/cplieger/deadset-spec/v8/facts"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Facts is the facts document of the run: every generated file any configuration
// compiles and every binding record one publishes, once each, with the state the
// carrier sweep gives each consumer and implementer.
func (f *Findings) Facts(ctx context.Context, name, version string) (*specfacts.Document, error) {
	doc := &specfacts.Document{
		Analyzer:  specfacts.Analyzer{Name: name, Version: version},
		Language:  vocab.Go,
		Evaluated: true,
	}
	files := make(map[string]inventory.SymbolID)
	for i := range f.Merged.Symbols {
		if s := &f.Merged.Symbols[i]; s.Kind == inventory.KindFile {
			files[s.File] = s.ID
		}
	}
	generated := make(map[string]bool)
	tuples := make(map[[4]string]int)
	served := make(map[inventory.SymbolID][]facts.Binding)
	registrations := make(map[inventory.SymbolID]bool)
	oneof := make(map[inventory.SymbolID]facts.OneofPart)
	caps := make(map[string]facts.Cap)
	var reads []facts.SchemaRead
	for i := range f.Loads {
		x := f.Loads[i].Facts
		doc.Generated = generatedEntries(doc.Generated, x.Generated(), generated)
		doc.Bindings = f.bindingRecords(doc.Bindings, x.Bindings(), files, tuples)
		reads = append(reads, x.SchemaReads()...)
		f.collectCaps(caps, x.Caps())
		for _, b := range x.Bindings() {
			switch {
			case b.Role == facts.Served && !slices.Contains(served[b.Symbol], b):
				served[b.Symbol] = append(served[b.Symbol], b)
			case b.Role == facts.Registration:
				registrations[b.Symbol] = true
			case b.Oneof != facts.NotOneof:
				oneof[b.Symbol] = b.Oneof
			}
		}
	}
	implementers := f.implementers(served, generated)
	doc.Bindings = f.implementerRecords(doc.Bindings, implementers, served, tuples)
	carriage := f.Graph.Carry(&liveness.Carrier{
		Generated: generated, Implementers: implementers, Registrations: registrations, Exempt: f.Exemptions, Reads: reads,
		Oneof: oneof,
	})
	if err := f.states(ctx, doc.Bindings, carriage, generated); err != nil {
		return nil, err
	}
	f.capRecords(doc.Bindings, caps)
	return specfacts.Build(doc)
}

// collectCaps adds each configuration's caps to caps by the record's symbol: a
// cap any configuration's evidence shows applies, the greater [facts.Cap] where
// two do.
func (l *Loaded) collectCaps(caps map[string]facts.Cap, found map[inventory.SymbolID]facts.Cap) {
	for id, capped := range found {
		if ref := l.Refs[id]; ref != "" {
			caps[ref] = max(caps[ref], capped)
		}
	}
}

// capNames is the facts document's name of each cap.
var capNames = map[facts.Cap]specfacts.Cap{
	facts.DescriptorExposure: specfacts.CapDescriptorExposure,
	facts.Persistence:        specfacts.CapPersistence,
}

// capRecords gives each consumer record its cap, none where possible is below
// the minimum confidence, because the merge must not lower a finding below the
// minimum it was published under (grammar/analysis/bindings.md bindings.16).
func (l *Loaded) capRecords(records []specfacts.Binding, caps map[string]facts.Cap) {
	if findings.Withholds(l.req.Config.Analysis.MinConfidence, vocab.Possible) {
		return
	}
	for i := range records {
		if r := &records[i]; r.Role == specfacts.RoleConsumer {
			r.Cap = capNames[caps[r.Symbol]]
		}
	}
}

// generatedEntries appends the entry of each generated file whose path seen does
// not hold yet, and records the path there.
func generatedEntries(held []specfacts.Generated, generated []facts.Generated, seen map[string]bool) []specfacts.Generated {
	for i := range generated {
		g := &generated[i]
		if seen[g.Path] {
			continue
		}
		seen[g.Path] = true
		entry := specfacts.Generated{Path: g.Path, Line: g.Line, Evidence: specfacts.EvidenceHeader, Detail: g.Detail}
		if g.Generator != "" {
			entry.Generator = &specfacts.Generator{Row: g.Generator, Version: g.Version}
			entry.SchemaFiles = slices.Clone(g.SchemaFiles)
		}
		held = append(held, entry)
	}
	return held
}

// bindingRecords appends the record of each binding that names a reported symbol
// and whose tuple (role, symbol, schema file, schema) seen does not hold yet, and
// records the record's index there. Build configurations are alternative views of
// one target, so a tuple two of them bind through different generated files is one
// record naming the bytewise-lowest file, whatever the configuration order. A
// fallback names its generated file's symbol, which files holds by path, and
// stands for its whole schema file.
func (l *Loaded) bindingRecords(held []specfacts.Binding, bindings []facts.Binding, files map[string]inventory.SymbolID, seen map[[4]string]int) []specfacts.Binding {
	for _, b := range bindings {
		record := specfacts.Binding{
			Role: specfacts.RoleConsumer, SchemaFile: b.SchemaFile, Schema: b.Schema,
			GeneratedFile: b.GeneratedFile, Symbol: l.Refs[b.Symbol],
		}
		switch b.Role {
		case facts.Served, facts.Registration:
			continue
		case facts.Fallback:
			record.Role, record.Scope = specfacts.RoleFallback, specfacts.ScopeAll
			record.Symbol, record.State = l.Refs[files[b.GeneratedFile]], specfacts.StateLive
		}
		if record.Symbol == "" {
			continue
		}
		tuple := [4]string{string(record.Role), record.Symbol, b.SchemaFile, b.Schema}
		if at, ok := seen[tuple]; ok {
			if b.GeneratedFile < held[at].GeneratedFile {
				held[at].GeneratedFile = b.GeneratedFile
			}
			continue
		}
		seen[tuple] = len(held)
		held = append(held, record)
	}
	return held
}

// implementers is, per served method, the hand-written methods outside test code
// that the production sweep's interface-satisfaction exemption retains as
// answering it, whatever site converts their type (grammar/analysis/bindings.md
// bindings.8). A method the generated Unimplemented type supplies is declared in a
// generated file, so it is none. Every served method is a key.
func (p *Prepared) implementers(served map[inventory.SymbolID][]facts.Binding, generated map[string]bool) map[inventory.SymbolID][]inventory.SymbolID {
	symbols := make(map[inventory.SymbolID]*inventory.Symbol, len(p.Merged.Symbols))
	for i := range p.Merged.Symbols {
		symbols[p.Merged.Symbols[i].ID] = &p.Merged.Symbols[i]
	}
	answering := make(map[inventory.SymbolID][]inventory.SymbolID, len(served))
	for method := range served {
		answering[method] = nil
	}
	for i := range p.Exemptions {
		e := &p.Exemptions[i]
		method := symbols[e.ID]
		if _, ok := served[e.Via]; !ok || e.Class != string(vocab.InterfaceSatisfaction) || method == nil ||
			method.Kind != inventory.KindMethod || method.InTestFile || method.TestSupport || generated[method.File] ||
			slices.Contains(answering[e.Via], e.ID) {
			continue
		}
		answering[e.Via] = append(answering[e.Via], e.ID)
	}
	return answering
}

// implementerRecords appends one record per implementer of a served method, bound
// to that method's RPC, in the order the exemptions name them.
func (p *Prepared) implementerRecords(held []specfacts.Binding, implementers map[inventory.SymbolID][]inventory.SymbolID, served map[inventory.SymbolID][]facts.Binding, seen map[[4]string]int) []specfacts.Binding {
	for i := range p.Exemptions {
		e := &p.Exemptions[i]
		if !slices.Contains(implementers[e.Via], e.ID) {
			continue
		}
		for _, rpc := range served[e.Via] {
			tuple := [4]string{string(specfacts.RoleImplementer), p.Refs[e.ID], rpc.SchemaFile, rpc.Schema}
			if _, ok := seen[tuple]; ok {
				continue
			}
			seen[tuple] = len(held)
			held = append(held, specfacts.Binding{
				Role: specfacts.RoleImplementer, Symbol: p.Refs[e.ID], SchemaFile: rpc.SchemaFile, Schema: rpc.Schema,
			})
		}
	}
	return held
}

// searches is one carrier sweep as the states read it: what hand-written code
// reaches, and the search of each implementer it does not reach.
type searches struct {
	k         *liveness.Carriage
	ids       map[string][]inventory.SymbolID // per reference, the declarations a record of it names
	from      map[string]liveness.Reach       // per unreached implementer's reference, its search
	generated map[string]bool
	hand      liveness.Reach
	order     []string // the unreached implementers, in record order
}

// states gives each consumer and implementer record its state from the carrier
// sweep (bindings.16 to bindings.18). A record hand-written code reaches is live.
// Otherwise the search from each implementer the sweep does not reach decides it:
// a consumer one reaches is live with via naming them, and is unreferenced where
// none does; an implementer is dead, with via naming the other implementers whose
// search reaches it, carrying its pending finding ([Findings.pending]). An
// implementer with no pending finding, one below the minimum confidence included,
// is live.
func (f *Findings) states(ctx context.Context, records []specfacts.Binding, k *liveness.Carriage, generated map[string]bool) error {
	s := f.searches(records, k, generated)
	pending, err := f.pending(ctx, s)
	if err != nil {
		return err
	}
	for i := range records {
		if r := &records[i]; r.Role == specfacts.RoleConsumer || r.Role == specfacts.RoleImplementer {
			s.decide(r, pending[r.Symbol])
		}
	}
	return nil
}

// searches is the carrier sweep k with the search of every implementer record
// hand-written code does not reach.
func (f *Findings) searches(records []specfacts.Binding, k *liveness.Carriage, generated map[string]bool) *searches {
	s := &searches{
		k: k, hand: k.Hand(), ids: make(map[string][]inventory.SymbolID, len(f.Merged.Symbols)),
		from: make(map[string]liveness.Reach), generated: generated,
	}
	// A record names a reference, which each configuration's alternative of one
	// declaration shares and a blank member shares with its container.
	for i := range f.Merged.Symbols {
		if one := &f.Merged.Symbols[i]; !one.Blank && f.Refs[one.ID] != "" {
			s.ids[f.Refs[one.ID]] = append(s.ids[f.Refs[one.ID]], one.ID)
		}
	}
	for i := range records {
		r := &records[i]
		if _, searched := s.from[r.Symbol]; r.Role == specfacts.RoleImplementer && !searched && !s.holds(s.hand, r.Symbol) {
			s.from[r.Symbol] = k.Search(s.ids[r.Symbol]...)
			s.order = append(s.order, r.Symbol)
		}
	}
	return s
}

// holds reports whether the reach holds a declaration the reference names.
func (s *searches) holds(r liveness.Reach, ref string) bool {
	return slices.ContainsFunc(s.ids[ref], r.Holds)
}

// decide gives one consumer or implementer record its state, its via and, for a
// dead implementer, the pending finding found.
func (s *searches) decide(r *specfacts.Binding, found *implementerFinding) {
	if s.holds(s.hand, r.Symbol) {
		r.State = specfacts.StateLive
		return
	}
	for _, other := range s.order {
		if other != r.Symbol && s.holds(s.from[other], r.Symbol) {
			r.Via = append(r.Via, other)
		}
	}
	switch {
	case r.Role == specfacts.RoleConsumer && len(r.Via) > 0:
		r.State = specfacts.StateLive
	case r.Role == specfacts.RoleConsumer:
		r.State = specfacts.StateUnreferenced
	case found == nil:
		r.State, r.Via = specfacts.StateLive, nil
	default:
		r.State, r.Finding, r.Carried = specfacts.StateDead, &found.finding, found.carried
	}
}

// implementerFinding is a dead implementer's pending finding and the findings it
// carries, all in one component minted past the report's.
type implementerFinding struct {
	finding report.Finding
	carried []report.Finding
}

// pending is, per unreached implementer, the finding a findings pass reports for
// its method once nothing generated holds it: no satisfaction of a generated
// server or handler interface, and no use by a generated interface method it
// answers. It carries the findings of that pass about hand-written declarations
// outside test code its search reaches that the report holds none for, hand-written
// code does not reach, and that are no implementer and no narrowing of a live
// declaration. Each implementer's component is minted after the report's last
// (bindings.18).
func (f *Findings) pending(ctx context.Context, s *searches) (map[string]*implementerFinding, error) {
	if len(s.order) == 0 {
		return nil, nil
	}
	found, err := f.withoutGenerated(ctx, s)
	if err != nil {
		return nil, err
	}
	reported := make(map[string]bool)
	next := 0
	for _, one := range f.reportedFindings() {
		reported[one.Symbol.Ref] = true
		if n, minted := findings.ComponentNumber(one.Component.ID); minted {
			next = max(next, n)
		}
	}
	held := make(map[string]*implementerFinding, len(s.order))
	for _, ref := range s.order {
		at := slices.IndexFunc(found.Result.Findings, func(one findings.Finding) bool { return one.Symbol.Ref == ref })
		if at < 0 {
			continue
		}
		next++
		component := findings.ComponentID(next)
		one := &implementerFinding{finding: found.Result.Findings[at].Document(), carried: s.carried(found.Result.Findings, ref, reported)}
		one.finding.Component.ID = component
		for i := range one.carried {
			one.carried[i].Component.ID = component
		}
		held[ref] = one
	}
	return held, nil
}

// carried is the findings of all that one unreached implementer's search alone
// reaches ([Findings.pending]).
func (s *searches) carried(all []findings.Finding, ref string, reported map[string]bool) []report.Finding {
	var found []report.Finding
	for i := range all {
		c := &all[i]
		id := c.Declaration()
		if _, implementer := s.from[c.Symbol.Ref]; implementer || id == "" || reported[c.Symbol.Ref] || c.Live || c.Generated ||
			c.TestOnly || !s.from[ref].Holds(id) || s.hand.Holds(id) {
			continue
		}
		found = append(found, c.Document())
	}
	return found
}

// withoutGenerated is the findings pass over the production sweep with the
// satisfactions of generated server and handler interfaces withdrawn, and with
// the uses by which a generated interface method nothing calls holds an unreached
// implementer dropped.
func (f *Findings) withoutGenerated(ctx context.Context, s *searches) (*Findings, error) {
	unreached := make(map[inventory.SymbolID]bool, len(s.order))
	for _, ref := range s.order {
		for _, id := range s.ids[ref] {
			unreached[id] = true
		}
	}
	generated := make(map[inventory.SymbolID]bool)
	for i := range f.Merged.Symbols {
		if one := &f.Merged.Symbols[i]; s.generated[one.File] {
			generated[one.ID] = true
		}
	}
	merged := *f.Merged
	merged.Uncalled.Uses = slices.DeleteFunc(slices.Clone(merged.Uncalled.Uses), func(u exempt.Use) bool {
		return unreached[u.To] && generated[u.From]
	})
	loaded := *f.Loaded
	loaded.Merged, loaded.Graph = &merged, liveness.Of(&merged)
	without := *f.Prepared
	without.Loaded = &loaded
	without.Exemptions = slices.DeleteFunc(slices.Clone(f.Exemptions), func(e exempt.Exemption) bool { return s.k.Answers(&e) })
	swept, err := without.sweep(nil)
	if err != nil {
		return nil, err
	}
	return swept.Findings(ctx)
}

// reportedFindings is every finding the report names: its findings and each edge
// evaluation's pending finding.
func (f *Findings) reportedFindings() []*findings.Finding {
	all := make([]*findings.Finding, 0, len(f.Result.Findings))
	for i := range f.Result.Findings {
		all = append(all, &f.Result.Findings[i])
	}
	for i := range f.Result.Evaluations {
		if f.Result.Evaluations[i].Finding != nil {
			all = append(all, f.Result.Evaluations[i].Finding)
		}
	}
	return all
}
