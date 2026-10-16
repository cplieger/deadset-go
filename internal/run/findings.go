package run

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/cplieger/deadset-go/v2/internal/deps"
	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
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
		sites, err := exempt.GeneratedSites(one.Result, one.Resolve)
		if err != nil {
			return nil, fmt.Errorf("render the package clause of a generated file: %w", err)
		}
		for path := range sites {
			generated[path] = true
		}
	}
	modules, err := s.moduleFiles(ctx)
	if err != nil {
		return nil, err
	}
	declared, err := edges.Read(filepath.Join(s.Root, edges.FileName))
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
		Matrix:         s.Identifiers,
		Per:            s.per,
		Consumers:      s.consumers(),
		Mode:           s.Mode,
	}, nil
}

// moduleFiles reads the module file of every unit, in unit order. Every
// configuration loads the same units, so the first one names them.
func (l *Loaded) moduleFiles(ctx context.Context) ([]kinds.ModuleFile, error) {
	if len(l.Loads) == 0 {
		return nil, nil
	}
	units := l.Loads[0].Result.Units
	files := make([]kinds.ModuleFile, 0, len(units))
	for i := range units {
		file, err := deps.ModuleFile(ctx, units[i].Dir)
		if err != nil {
			return nil, err
		}
		files = append(files, kinds.ModuleFile{Dir: units[i].Dir, Path: units[i].Path, File: file})
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
