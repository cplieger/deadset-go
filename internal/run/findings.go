package run

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/deps"
	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-go/v2/internal/load"
)

// Findings is the findings stage: every kind of [kinds.Emitters] over the production
// sweep, completed and evaluated against the declared edges, and the two suppression
// counts a summary prints.
type Findings struct {
	*Swept

	Result      kinds.Result
	Evaluations []kinds.Evaluation

	// inEffect and reasons count the suppression records that bound a would-be
	// finding and those that carry a reason.
	inEffect, reasons int
}

// Findings runs the findings pass. A pass that refuses a finding fails the run,
// because a refusal is a defect in a kind's rule rather than something a report
// could say.
func (s *Swept) Findings(ctx context.Context) (*Findings, error) {
	in, err := s.input(ctx)
	if err != nil {
		return nil, err
	}
	computed, err := kinds.Compute(in, kinds.Emitters)
	if err != nil {
		return nil, err
	}
	// The dependency a deletion orphans completes the findings rather than being a
	// kind of its own, so it runs over what the pass answered.
	kinds.Annotate(in, computed.Findings)

	// A finding about a symbol a declared edge names dead is published inside that
	// edge's evaluation and nowhere else, so the edges are evaluated last; a
	// withheld finding passes the same filter.
	kept, evaluations := kinds.Evaluate(in, computed.Findings)
	computed.Findings = kept
	computed.Withheld, _ = kinds.Evaluate(in, computed.Withheld)

	// The passes after the load read no context, so a run cancelled while they ran
	// ends here rather than writing a report the cancellation outdated.
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	inEffect, reasons := kinds.Totals(in)
	return &Findings{Swept: s, Result: computed, Evaluations: evaluations, inEffect: inEffect, reasons: reasons}, nil
}

// input is everything a kind reads.
func (s *Swept) input(ctx context.Context) (*kinds.Input, error) {
	generated := make(map[string]bool)
	for _, one := range s.per {
		all := one.Facts.Generated()
		for i := range all {
			generated[all[i].Path] = true
		}
	}
	modules, err := s.moduleFiles()
	if err != nil {
		return nil, err
	}
	declared, err := edges.Read(ctx, s.Root)
	if err != nil {
		return nil, err
	}
	return &kinds.Input{
		Config:         &s.req.Config,
		Merged:         s.Merged,
		Sweep:          &s.Liveness,
		Refs:           s.Refs,
		Exempt:         s.Exemptions,
		TestEvidence:   s.testEvidence,
		Filled:         s.filled,
		Generated:      func(path string) bool { return generated[path] },
		TypeErrorSkips: s.TypeErrorSkips,
		Marks:          s.Marks,
		Refusals:       s.refusals,
		Root:           s.Root,
		Modules:        modules,
		Unmatched:      s.Unmatched,
		RootsDocument:  s.rootsDocument(),
		Edges:          declared,
		Matrix:         s.Identifiers(),
		Per:            s.per,
		Consumers:      s.consumers(),
		Mode:           s.Mode,
	}, nil
}

// moduleFiles is the module file of every unit the first configuration loaded
// whose file sits at its root, in unit order. Every configuration loads the same
// units, and a file above the target root has no directive a report can position.
func (l *Loaded) moduleFiles() ([]kinds.ModuleFile, error) {
	if len(l.Loads) == 0 {
		return nil, nil
	}
	var files []kinds.ModuleFile
	for i := range l.Topology.Units {
		unit := &l.Topology.Units[i]
		if !slices.ContainsFunc(l.Loads[0].Result.Units, func(u load.Unit) bool { return u.Dir == unit.Dir }) {
			continue
		}
		if unit.FileErr != nil {
			return nil, unit.FileErr
		}
		if unit.File == nil || filepath.Dir(unit.File.Path) != filepath.Clean(unit.Dir) {
			continue
		}
		files = append(files, kinds.ModuleFile{Dir: unit.Dir, Path: unit.Path, File: deps.Directives(unit.File)})
	}
	return files, nil
}

// rootsDocument is the document that supplied the configured roots, relative to the
// target root, and empty where no document inside the target did: a finding carries
// a target-relative path and has no spelling for a file above it.
//
// It is the label of the setting's origin, not the conventional path, so a run
// reading a document the invocation named reports an unmatched root in that
// document. Both paths are made absolute first, because a relative target beside an
// absolute document is the ordinary invocation.
func (l *Loaded) rootsDocument() string {
	label := l.req.Provenance[rootsPatternsSetting].Label
	if label == "" {
		return ""
	}
	root, rootErr := filepath.Abs(l.req.Target)
	document, documentErr := filepath.Abs(label)
	if rootErr != nil || documentErr != nil {
		return ""
	}
	relative, err := filepath.Rel(root, document)
	if err != nil || !filepath.IsLocal(relative) {
		return ""
	}
	return filepath.ToSlash(relative)
}

// rootsPatternsSetting is the setting the configured roots are resolved under.
const rootsPatternsSetting = "roots.patterns"

// consumers is the declared and loaded consumers, read from the first configuration's
// load: a declared consumer that fails to load under any configuration fails the run,
// so the two sets are the same modules, kept apart because the reachability class
// asks a different question of each.
func (l *Loaded) consumers() kinds.Consumers {
	var loaded []string
	if len(l.Loads) > 0 {
		loaded = make([]string, len(l.Loads[0].Result.Consumers))
		for i, consumer := range l.Loads[0].Result.Consumers {
			loaded[i] = consumer.ID
		}
	}
	return kinds.Consumers{Declared: loaded, Loaded: loaded, Complete: l.req.Config.Consumers.Complete}
}
