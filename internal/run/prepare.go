package run

import (
	"os"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-spec/v8/report"
)

// Prepared is the exemption stage under one mode: one exemption set per
// configuration, over the resolver the load built. No suppression record changes
// it, so the rounds of a baseline write share one.
type Prepared struct {
	*Loaded

	// unseen is every production declaration outside programs may call
	// ([kinds.Unseen]).
	unseen map[inventory.SymbolID]bool

	// filled is every field a callee outside the program fills and no class
	// retains ([exempt.Computed.Filled]).
	filled map[inventory.SymbolID]bool

	per []kinds.Configured

	Exemptions []exempt.Exemption

	// testEvidence is the exemption evidence a test file carries, which the mode
	// does not hold and the sweep counts as test references.
	testEvidence []exempt.Exemption

	// Mode is the reference mode every later stage of the run reads.
	Mode facts.Mode
}

// Prepare runs the exemption classes of every configuration under the production
// mode where production is set and the plain mode otherwise. The classes compute
// under the mode the sweep runs in: a test that marshals a value is evidence of a use
// that test makes, which a production sweep does not count.
func (l *Loaded) Prepare(production bool) (*Prepared, error) {
	p := &Prepared{
		Loaded: l,
		Mode: facts.Mode{
			Production:              production,
			ConsumerTestsProduction: l.req.Config.Analysis.ConsumerTestsProduction,
		},
		per:    make([]kinds.Configured, len(l.Loads)),
		filled: make(map[inventory.SymbolID]bool),
	}
	options := exemptOptions(&l.req.Config)
	for i := range l.Loads {
		one := &l.Loads[i]
		program, err := exempt.NewProgram(&exempt.Loaded{
			Result:  &one.Result,
			Resolve: one.Resolve,
			Facts:   one.Facts,
			Symbols: one.Symbols,
			Root:    l.Root,
			Read:    os.ReadFile,
			Options: options,
			Mode:    p.Mode,
		})
		if err != nil {
			return nil, err
		}
		computed, err := exempt.Compute(program, exempt.Detectors)
		if err != nil {
			return nil, err
		}
		p.Exemptions = append(p.Exemptions, computed.Held...)
		p.testEvidence = append(p.testEvidence, computed.TestEvidence...)
		for j := range computed.Filled {
			p.filled[computed.Filled[j].ID] = true
		}
		p.per[i] = kinds.Configured{Result: &one.Result, Resolve: one.Resolve, Facts: one.Facts, Symbols: one.Symbols}
	}
	p.Exemptions = l.Merged.Uncalled.Withdrawn(p.Exemptions)
	p.testEvidence = l.Merged.Uncalled.Withdrawn(p.testEvidence)
	p.unseen = kinds.Unseen(&kinds.Input{Config: &l.req.Config, Merged: l.Merged, Per: p.per, Root: l.Root, Consumers: l.consumers()})
	return p, nil
}

// exemptOptions is the whole of the configuration that reaches an exemption class.
func exemptOptions(cfg *config.Config) exempt.Options {
	return exempt.Options{
		Disabled: cfg.Exemptions.Disabled,
		TemplateDelimiters: exempt.Delimiters{
			Left:  cfg.Analysis.TemplateDelimiters.Left,
			Right: cfg.Analysis.TemplateDelimiters.Right,
		},
		TemplateDirs:     cfg.Analysis.TemplateDirs,
		IncludeGenerated: cfg.Analysis.IncludeGenerated,
		PublishedAPI:     cfg.Target.Kind == report.TargetKindLibrary,
	}
}
