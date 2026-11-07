// Package run is one analysis of a target as a list of typed stages: a request
// loads ([Load]), prepares the exemptions under one mode ([Loaded.Prepare]), sweeps
// under the suppression records ([Prepared.Sweep]), computes the findings
// ([Swept.Findings]) and assembles the report ([Findings.Report]). Each stage holds
// the one before it, so a later stage is reached only through the earlier ones and
// what an earlier stage computed is computed once.
package run

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/matrix"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Request is what a verb asks one analysis for, with the flags already resolved.
type Request struct {
	Provenance config.Provenance

	// Target is the target root as the invocation spelled it, and Scope the scope
	// document it named, empty where it named none.
	Target string
	Scope  string
	Config config.Config
}

// Loaded is the load stage: the scope, the matrix the target builds, one load and
// one set of passes per configuration, merged into the one inventory every later
// stage answers over.
type Loaded struct {
	req *Request

	// Topology is what every configuration loaded, read once.
	Topology *load.Topology

	// Graph is the merged matrix as one graph over every configuration.
	Graph  *liveness.Graph
	Merged *liveness.Merged
	Refs   map[inventory.SymbolID]string

	// Root is the target root every position is rendered against, and Identity
	// the name the scope gives the target, empty where it gives none.
	Root     string
	Identity string

	// Configurations is the matrix the run analyzed, in the order every
	// configuration index names, and loads each one's load.
	Configurations []load.Configuration
	loads          *loads

	// Declared is the consumers the scope declared.
	Declared       []scope.Module
	TestFileRules  []inventory.TestFileRule
	Unmatched      []roots.Unmatched
	TypeErrorSkips []roots.TypeErrorSkip

	// Unbuilt is every derived configuration the target does not build, which the
	// load dropped from the matrix and a verb names.
	Unbuilt []load.Unbuilt
}

// Load runs the load stage. A configuration that does not load fails the run with
// nothing computed from the others, because a union missing a configuration
// reports dead what only that configuration uses. A configured string is
// unmatched only where no configuration matched it.
func Load(ctx context.Context, req *Request) (*Loaded, error) {
	document, err := scope.Resolve(ctx, req.Target, req.Scope)
	if err != nil {
		return nil, err
	}
	if req.Scope == "" {
		document.Consumers = nameable(document.Consumers)
	}
	top, err := load.Discover(ctx, document)
	if err != nil {
		return nil, err
	}
	configurations, derived, err := matrixOf(&req.Config, top)
	if err != nil {
		return nil, err
	}
	l := &Loaded{
		req:      req,
		Topology: top,
		Root:     top.Root,
		Identity: document.Target.ID,
		Declared: document.Consumers,
	}
	loader := load.NewMatrix(top)
	l.loads = newLoads(ctx, loader, top.Root)
	var elsewhere roots.Elsewhere
	l.Configurations, l.Unbuilt, err = loader.All(ctx, configurations, func(r *load.Result) error {
		one, enumerated := configuredOf(r, top.Root)
		if enumerated != nil {
			return enumerated
		}
		elsewhere.Add(r)
		l.loads.add(one)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var checked []map[load.PackageID]bool
	if derived {
		checked = elsewhere.Of()
	}
	passes, tests, err := l.enumerate(checked)
	if err != nil {
		return nil, err
	}
	merged, err := liveness.Merge(passes, tests)
	if err != nil {
		return nil, err
	}
	l.Merged = &merged
	l.Refs = make(map[inventory.SymbolID]string, len(merged.Symbols))
	for i := range merged.Symbols {
		l.Refs[merged.Symbols[i].ID] = merged.Symbols[i].Ref
	}
	l.Graph = liveness.Of(&merged)
	l.Unmatched = merged.UnmatchedEverywhere()
	return l, nil
}

// Identifiers is the identifier of every configuration the run analyzed, in matrix
// order.
func (l *Loaded) Identifiers() []string {
	ids := make([]string, len(l.Configurations))
	for i := range l.Configurations {
		ids[i] = l.Configurations[i].ID
	}
	return ids
}

// enumerate runs the passes over every loaded configuration, and returns them with
// the matrix's test classes the reference pass read. A derived matrix checks a file
// against the configurations that compile it elsewhere, which elsewhere holds per
// configuration and a declared matrix leaves nil.
func (l *Loaded) enumerate(elsewhere []map[load.PackageID]bool) ([]liveness.Configured, roots.TestClasses, error) {
	options := roots.RootOptions{
		Patterns:     l.req.Config.Roots.Patterns,
		PublishedAPI: l.req.Config.Target.Kind == report.TargetKindLibrary,
	}
	passes := make([]liveness.Configured, l.loads.Len())
	classes := make([]roots.TestClasses, l.loads.Len())
	for i := range passes {
		one, err := l.loads.At(i)
		if err != nil {
			return nil, roots.TestClasses{}, err
		}
		options := options
		if elsewhere != nil {
			options.CheckedElsewhere = elsewhere[i]
		}
		passes[i], classes[i] = rootsOf(one, options)
		l.TypeErrorSkips = append(l.TypeErrorSkips, roots.TypeErrorSkips(one.Result, one.Resolve, l.Root, options.CheckedElsewhere)...)
	}
	tests := roots.MatrixTests(classes)
	for i := range passes {
		one, err := l.loads.At(i)
		if err != nil {
			return nil, roots.TestClasses{}, err
		}
		references, rules, err := facts.References(one.Result, one.Resolve, one.Symbols, tests)
		if err != nil {
			return nil, roots.TestClasses{}, err
		}
		passes[i].References = references
		l.TestFileRules = greatestPerRule(l.TestFileRules, rules)
	}
	return passes, tests, nil
}

// rootsOf runs the passes over one loaded configuration that come before the
// matrix's test classes: the roots, and the configuration's own test classes, which
// read the roots.
func rootsOf(one *findings.Configured, options roots.RootOptions) (liveness.Configured, roots.TestClasses) {
	found, unmatched := roots.Roots(one.Result, one.Resolve, one.Symbols, options)
	return liveness.Configured{Symbols: one.Symbols, Roots: found, Unmatched: unmatched, Interfaces: one.Facts.Interfaces()},
		roots.ClassifyTestSupport(one.Result, one.Symbols, found, options.PublishedAPI)
}

// greatestPerRule folds one configuration's test-file rules into the run's. Every
// configuration classifies the same files by the same rule, so the greater count is
// the one the whole run classified.
func greatestPerRule(held, next []inventory.TestFileRule) []inventory.TestFileRule {
	for _, rule := range next {
		at := slices.IndexFunc(held, func(r inventory.TestFileRule) bool { return r.Rule == rule.Rule })
		switch {
		case at < 0:
			held = append(held, rule)
		case rule.Matched > held[at].Matched:
			held[at].Matched = rule.Matched
		}
	}
	return held
}

// matrixOf is the matrix the configuration lists, or the one the target tree
// implies where it lists none, reporting which; a derived matrix is never
// complete, which only the configuration asserts.
func matrixOf(cfg *config.Config, top *load.Topology) ([]load.Configuration, bool, error) {
	if platforms := cfg.Analysis.Platforms; len(platforms) > 0 {
		configurations := make([]load.Configuration, len(platforms))
		for i, p := range platforms {
			configurations[i] = load.Configuration{ID: p.ID, OS: p.OS, Arch: p.Arch, Tags: p.Tags, Origin: load.Declared}
		}
		return configurations, false, nil
	}
	configurations, err := matrix.Derive(top)
	return configurations, true, err
}

// nameable is the consumers a workspace lists that a report can name, the ones
// under the run directory. The others are workspace readers, which the topology
// counts whatever named the scope.
func nameable(consumers []scope.Module) []scope.Module {
	return slices.DeleteFunc(slices.Clone(consumers), func(consumer scope.Module) bool {
		_, err := relativeToRunDirectory(consumer.Path)
		return err != nil
	})
}

// relativeToRunDirectory is one path relative to the directory the run was invoked
// from, with forward slashes, so two runs over one tree on two machines write the
// same bytes; a path outside that directory has no spelling a report carries.
func relativeToRunDirectory(path string) (string, error) {
	invoked, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve the directory the run was invoked from: %w", err)
	}
	relative, inside := scope.Within(invoked, path)
	if !inside {
		return "", failure.Wrap(vocab.RunUnreportablePath, fmt.Errorf("a report names %s relative to the directory the run was invoked from, and %s does not contain it",
			path, invoked))
	}
	return relative, nil
}
