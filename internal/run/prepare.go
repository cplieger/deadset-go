package run

import (
	"os"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/report"
)

// Prepared is the exemption stage under one mode: one exemption set per
// configuration, over the resolver the load built. No suppression record changes
// it, so the rounds of a baseline write share one.
type Prepared struct {
	*Loaded

	// unseen is every production declaration outside programs may call
	// ([findings.Unseen]).
	unseen map[inventory.SymbolID]bool

	// filled is every field a class keeps only at the class possible and no class
	// retains ([exempt.Computed.Filled]).
	filled map[inventory.SymbolID]bool

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
		filled: make(map[inventory.SymbolID]bool),
	}
	options := exemptOptions(&l.req.Config)
	for i := range l.loads.Len() {
		one, err := l.loads.At(i)
		if err != nil {
			return nil, err
		}
		program, err := exempt.NewProgram(&exempt.Loaded{
			Result:  one.Result,
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
	}
	p.Exemptions = l.Merged.Uncalled.Withdrawn(p.Exemptions)
	p.testEvidence = l.Merged.Uncalled.Withdrawn(p.testEvidence)
	var err error
	p.unseen, err = findings.Unseen(&findings.Input{Config: &l.req.Config, Merged: l.Merged, Per: l.loads, Root: l.Root, Consumers: l.consumers()})
	if err != nil {
		return nil, err
	}
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
