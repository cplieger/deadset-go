package run

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/deps"
	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// Findings is the findings stage: every kind of [findings.Emitters] over the production
// sweep, as [findings.Compute] finishes it.
type Findings struct {
	*Swept

	Result findings.Result
}

// Findings runs the findings pass. A pass that refuses a finding fails the run,
// because a refusal is a defect in a kind's rule rather than something a report
// could say.
func (s *Swept) Findings(ctx context.Context) (*Findings, error) {
	in, err := s.input(ctx)
	if err != nil {
		return nil, err
	}
	computed, err := findings.Compute(in, findings.Emitters)
	if err != nil {
		return nil, err
	}
	// The passes after the load read no context, so a run cancelled while they ran
	// ends here rather than writing a report the cancellation outdated.
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	return &Findings{Swept: s, Result: computed}, nil
}

// input is everything a kind reads.
func (s *Swept) input(ctx context.Context) (*findings.Input, error) {
	generated := make(map[string]bool)
	for i := range s.loads.Len() {
		one, err := s.loads.At(i)
		if err != nil {
			return nil, err
		}
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
	return &findings.Input{
		Config:         &s.req.Config,
		Merged:         s.Merged,
		Sweep:          &s.Liveness,
		Unmarked:       s.unmarked,
		Refs:           s.Refs,
		Exempt:         s.Exemptions,
		TestEvidence:   s.testEvidence,
		Filled:         s.filled,
		Arriving:       s.arriving,
		RetainedLocals: retainedLocals(s.Locals),
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
		Per:            s.loads,
		Consumers:      s.consumers(),
		Mode:           s.Mode,
	}, nil
}

// moduleFiles is the module file of every unit the first configuration loaded
// whose file sits at its root, in unit order. Every configuration loads the same
// units, and a file above the target root has no directive a report can position.
func (l *Loaded) moduleFiles() ([]findings.ModuleFile, error) {
	if l.loads.Len() == 0 {
		return nil, nil
	}
	var files []findings.ModuleFile
	for i := range l.Topology.Units {
		unit := &l.Topology.Units[i]
		if !slices.ContainsFunc(l.loads.outlines[0].units, func(u load.Unit) bool { return u.Dir == unit.Dir }) {
			continue
		}
		if unit.FileErr != nil {
			return nil, unit.FileErr
		}
		if unit.File == nil || filepath.Dir(unit.File.Path) != filepath.Clean(unit.Dir) {
			continue
		}
		files = append(files, findings.ModuleFile{Dir: unit.Dir, Path: unit.Path, File: deps.Directives(unit.File)})
	}
	return files, nil
}

// rootsDocument is the document that supplied the configured roots, relative to the
// target root, and empty where no document inside the target did: a finding carries
// a target-relative path and has no spelling for a file above it.
//
// It is the label of the setting's origin, not the conventional path, so a run
// reading a document the invocation named reports an unmatched root in that
// document.
func (l *Loaded) rootsDocument() string {
	label := l.req.Provenance[rootsPatternsSetting].Label
	if label == "" {
		return ""
	}
	relative, inside := scope.RootOf(l.Root).PlaceEntry(label)
	if !inside {
		return ""
	}
	return relative
}

// rootsPatternsSetting is the setting the configured roots are resolved under.
const rootsPatternsSetting = "roots.patterns"

// consumers is the declared and loaded consumers, read from the first configuration's
// load: a declared consumer that fails to load under any configuration fails the run,
// so the two sets are the same modules, kept apart because the reachability class
// asks a different question of each.
func (l *Loaded) consumers() findings.Consumers {
	var loaded []string
	if l.loads.Len() > 0 {
		loaded = l.loads.outlines[0].consumers
	}
	return findings.Consumers{Declared: loaded, Loaded: loaded, Complete: l.req.Config.Consumers.Complete}
}

// retainedLocals is where each constant a class retains in a function body is
// declared.
func retainedLocals(locals []exempt.RetainedLocal) map[inventory.Offset]bool {
	held := make(map[inventory.Offset]bool, len(locals))
	for i := range locals {
		held[locals[i].Declared] = true
	}
	return held
}
