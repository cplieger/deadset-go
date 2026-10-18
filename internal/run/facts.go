package run

import (
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	specfacts "github.com/cplieger/deadset-spec/v8/facts"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Facts is the facts document of the run, identities only: every generated file
// any configuration compiles and every binding identity one publishes, once each.
func (l *Loaded) Facts(name, version string) (*specfacts.Document, error) {
	doc := &specfacts.Document{
		Analyzer: specfacts.Analyzer{Name: name, Version: version},
		Language: vocab.Go,
	}
	files := make(map[string]inventory.SymbolID)
	for i := range l.Merged.Symbols {
		if s := &l.Merged.Symbols[i]; s.Kind == inventory.KindFile {
			files[s.Pos.Filename] = s.ID
		}
	}
	seen := make(map[string]bool)
	tuples := make(map[[4]string]int)
	for i := range l.Loads {
		x := l.Loads[i].Facts
		doc.Generated = generatedEntries(doc.Generated, x.Generated(), seen)
		doc.Bindings = l.bindingRecords(doc.Bindings, x.Bindings(), files, tuples)
	}
	return specfacts.Build(doc)
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
// fallback names its generated file's symbol, which files holds by path.
func (l *Loaded) bindingRecords(held []specfacts.Binding, bindings []facts.Binding, files map[string]inventory.SymbolID, seen map[[4]string]int) []specfacts.Binding {
	for _, b := range bindings {
		record := specfacts.Binding{
			Role: b.Role, SchemaFile: b.SchemaFile, Schema: b.Schema,
			GeneratedFile: b.GeneratedFile, Scope: b.Scope, Symbol: l.Refs[b.Symbol],
		}
		if b.Role == specfacts.RoleFallback {
			record.Symbol, record.State = l.Refs[files[b.GeneratedFile]], specfacts.StateLive
		}
		if record.Symbol == "" {
			continue
		}
		tuple := [4]string{string(b.Role), record.Symbol, b.SchemaFile, b.Schema}
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
