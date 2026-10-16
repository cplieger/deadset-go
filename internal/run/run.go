// Package run is one analysis of a target as a list of typed stages: a request
// loads ([Load]), prepares the exemptions under one mode ([Loaded.Prepare]), sweeps
// under the suppression records ([Prepared.Sweep]), computes the findings
// ([Swept.Findings]) and assembles the report ([Findings.Report]). Each stage holds
// the one before it, so a later stage is reached only through the earlier ones and
// what an earlier stage computed is computed once.
package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/matrix"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/report"
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

// enumerated is one configuration's load and the declarations enumerated from it.
type enumerated struct {
	Symbols []graph.Symbol
	Result  load.Result
}

// Loaded is the load stage: the scope, the matrix the target builds, one load and
// one set of passes per configuration, merged into the one inventory every later
// stage answers over.
type Loaded struct {
	req *Request

	Matrix *graph.Matrix
	Merged *graph.Merged
	Refs   map[graph.SymbolID]string

	// Root is the target root every position is rendered against, and Identity
	// the name the scope gives the target, empty where it gives none.
	Root     string
	Identity string

	// Configurations is the matrix the run analyzed, in the order every
	// configuration index names, and Identifiers the identifier of each.
	Configurations []load.Configuration
	Identifiers    []string
	Loads          []enumerated

	// Declared is the consumers the scope declared.
	Declared       []scope.Module
	TestFileRules  []graph.TestFileRule
	Unmatched      []graph.Unmatched
	TypeErrorSkips []graph.TypeErrorSkip

	// Unbuilt is every derived configuration the target does not build, which the
	// load dropped from the matrix and a verb names.
	Unbuilt []load.Unbuilt
}

// Load runs the load stage. A configuration that does not load fails the run with
// nothing computed from the others, because an intersection missing a
// configuration reports what that configuration uses. A configured string is
// unmatched only where no configuration matched it.
func Load(ctx context.Context, req *Request) (*Loaded, error) {
	document, err := scope.Resolve(ctx, req.Target, req.Scope)
	if err != nil {
		return nil, err
	}
	var readers []scope.Module
	if req.Scope == "" {
		document.Consumers, readers = nameable(document.Consumers)
	}
	configurations, derived, err := matrixOf(ctx, &req.Config, document.Target.Path)
	if err != nil {
		return nil, err
	}
	var guessed []string
	if derived != nil {
		guessed = derived.Guessed
	}
	results, unbuilt, err := load.All(ctx, document, configurations, guessed)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if failed := results[i].AddReaders(ctx, document.Workspace, readers); failed != nil {
			return nil, failed
		}
	}

	l := &Loaded{
		req:      req,
		Root:     document.Target.Path,
		Identity: document.Target.ID,
		Declared: document.Consumers,
		Unbuilt:  unbuilt,
	}
	passes, err := l.enumerate(results, derived != nil)
	if err != nil {
		return nil, err
	}
	merged, err := graph.Merge(passes)
	if err != nil {
		return nil, err
	}
	l.Merged = &merged
	l.Refs = make(map[graph.SymbolID]string, len(merged.Symbols))
	for i := range merged.Symbols {
		l.Refs[merged.Symbols[i].ID] = merged.Symbols[i].Ref
	}
	l.Matrix = graph.NewMatrix(&merged)
	l.Unmatched = l.Matrix.UnmatchedEverywhere()
	return l, nil
}

// enumerate runs the passes over every loaded configuration. A derived matrix
// checks a file against the configurations that compile it elsewhere.
func (l *Loaded) enumerate(results []load.Result, derived bool) ([]graph.Configured, error) {
	options := graph.RootOptions{
		Patterns:     l.req.Config.Roots.Patterns,
		PublishedAPI: l.req.Config.Target.Kind == report.TargetKindLibrary,
	}
	var elsewhere []map[string]bool
	if derived {
		elsewhere = graph.CheckedElsewhere(results)
	}
	l.Loads = make([]enumerated, len(results))
	passes := make([]graph.Configured, len(results))
	for i := range results {
		one := options
		if elsewhere != nil {
			one.CheckedElsewhere = elsewhere[i]
		}
		var rules []graph.TestFileRule
		var err error
		if l.Loads[i], passes[i], rules, err = passesOf(&results[i], l.Root, one); err != nil {
			return nil, err
		}
		l.TestFileRules = greatestPerRule(l.TestFileRules, rules)
		l.TypeErrorSkips = append(l.TypeErrorSkips, graph.TypeErrorSkips(&results[i], l.Root, one.CheckedElsewhere)...)
		l.Configurations = append(l.Configurations, results[i].Configuration)
		l.Identifiers = append(l.Identifiers, results[i].Configuration.ID)
	}
	return passes, nil
}

// passesOf runs the passes over one loaded configuration. The test-support
// classification reads the roots and decides which references are test references,
// so it runs between the two.
func passesOf(result *load.Result, root string, options graph.RootOptions) (enumerated, graph.Configured, []graph.TestFileRule, error) {
	symbols, err := graph.Symbols(result, root, os.ReadFile)
	if err != nil {
		return enumerated{}, graph.Configured{}, nil, err
	}
	roots, unmatched, err := graph.Roots(result, root, os.ReadFile, symbols, options)
	if err != nil {
		return enumerated{}, graph.Configured{}, nil, err
	}
	graph.ClassifyTestSupport(result, root, symbols, roots, options.PublishedAPI)
	references, rules, err := graph.References(result, root, os.ReadFile, symbols)
	if err != nil {
		return enumerated{}, graph.Configured{}, nil, err
	}
	return enumerated{Symbols: symbols, Result: *result},
		graph.Configured{Symbols: symbols, References: references, Roots: roots, Unmatched: unmatched},
		rules, nil
}

// greatestPerRule folds one configuration's test-file rules into the run's. Every
// configuration classifies the same files by the same rule, so the greater count is
// the one the whole run classified.
func greatestPerRule(held, next []graph.TestFileRule) []graph.TestFileRule {
	for _, rule := range next {
		at := slices.IndexFunc(held, func(r graph.TestFileRule) bool { return r.Rule == rule.Rule })
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
// implies where it lists none, with that derivation; a derived matrix is never
// complete, which only the configuration asserts.
func matrixOf(ctx context.Context, cfg *config.Config, root string) ([]load.Configuration, *matrix.Derived, error) {
	if platforms := cfg.Analysis.Platforms; len(platforms) > 0 {
		configurations := make([]load.Configuration, len(platforms))
		for i, p := range platforms {
			configurations[i] = load.Configuration{ID: p.ID, OS: p.OS, Arch: p.Arch, Tags: p.Tags}
		}
		return configurations, nil, nil
	}
	derived, err := matrix.Derive(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	return derived.Configurations, &derived, nil
}

// nameable splits the consumers a workspace lists into those a report can name, the
// ones under the run directory, and the others, whose references the run still
// counts as readers.
func nameable(consumers []scope.Module) (named, readers []scope.Module) {
	for _, consumer := range consumers {
		if _, err := relativeToRunDirectory(consumer.Path); err == nil {
			named = append(named, consumer)
		} else {
			readers = append(readers, consumer)
		}
	}
	return named, readers
}

// ErrReportPath reports a path of the run no report can name: one the directory the
// run was invoked from does not contain.
var ErrReportPath = errors.New("deadset-go: the report cannot name a path outside the run directory")

// relativeToRunDirectory is one path relative to the directory the run was invoked
// from, with forward slashes, so two runs over one tree on two machines write the
// same bytes; a path outside that directory has no spelling a report carries.
func relativeToRunDirectory(path string) (string, error) {
	invoked, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve the directory the run was invoked from: %w", err)
	}
	relative, err := filepath.Rel(invoked, path)
	if err != nil {
		return "", fmt.Errorf("resolve %s against the directory the run was invoked from: %w", path, err)
	}
	if relative != "." && !filepath.IsLocal(relative) {
		return "", fmt.Errorf("%w: a report names %s relative to the directory the run was invoked from, and %s does not contain it: run from a directory that does",
			ErrReportPath, path, invoked)
	}
	return filepath.ToSlash(relative), nil
}
