package run

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/report"
)

// Self is what the analyzer states about itself in every document it writes: its
// identity with the corpus result, and every capability it declines.
type Self struct {
	Analyzer report.Analyzer
	Gaps     []report.DeclaredGap
}

// Report assembles the report the Contract declares. An envelope the Contract cannot
// carry fails the run, because a document no reader admits says less than an error
// does; an analyzer naming no conformance result is one.
func (f *Findings) Report(self *Self) (*report.Report, error) {
	target, err := f.target()
	if err != nil {
		return nil, err
	}
	loaded, err := f.consumersReported()
	if err != nil {
		return nil, err
	}
	findings := make([]report.Finding, len(f.Result.Findings))
	for i := range f.Result.Findings {
		findings[i] = f.Result.Findings[i].Document()
	}
	evaluations := make([]report.EdgeEvaluation, len(f.Evaluations))
	for i := range f.Evaluations {
		evaluations[i] = f.Evaluations[i].Document()
	}
	var excluded []string
	for i := range f.Loads {
		excluded = append(excluded, f.Loads[i].Result.ExcludedByCgo...)
	}
	rules := make([]report.TestFileRule, len(f.TestFileRules))
	for i, rule := range f.TestFileRules {
		rules[i] = report.TestFileRule{Rule: rule.Rule, Matched: rule.Matched}
	}
	skips := make([]report.TypeErrorSkip, len(f.TypeErrorSkips))
	for i, skip := range f.TypeErrorSkips {
		skips[i] = report.TypeErrorSkip{Path: skip.Path, Line: skip.Line, Message: skip.Message}
	}
	return report.Build(&report.Input{
		Members:                kinds.Listing(f.Result.Findings, f.Evaluations),
		Analyzer:               self.Analyzer,
		Target:                 target,
		Configurations:         configurationsReported(f.Configurations),
		ConfigurationsNotBuilt: configurationsNotBuilt(f.Unbuilt),
		Loaded:                 loaded,
		Findings:               findings,
		EdgeEvaluations:        evaluations,
		DeclaredGaps:           self.Gaps,
		ExcludedByCgo:          excluded,
		TestFileRules:          rules,
		TypeErrorSkips:         skips,
		Notes:                  f.notes(),
		SuppressionsInEffect:   f.inEffect,
		ReasonsRecorded:        f.reasons,
		Withheld:               kinds.WithheldOf(f.Result.Withheld),
	})
}

// target is what a report says was analyzed: the declared kind, the target's
// directory as a reader resolves it, and the name the target publishes itself under.
func (l *Loaded) target() (report.Target, error) {
	root, err := relativeToRunDirectory(l.Root)
	if err != nil {
		return report.Target{}, err
	}
	identity, err := l.identity()
	if err != nil {
		return report.Target{}, err
	}
	return report.Target{Kind: l.req.Config.Target.Kind, Root: root, Identity: identity}, nil
}

// identity is the path of the module rooted at the target root, else the name the
// scope gives the target, else the path of the first unit in directory order.
func (l *Loaded) identity() (string, error) {
	var units []load.Unit
	if len(l.Loads) > 0 {
		units = l.Loads[0].Result.Units
	}
	for i := range units {
		if filepath.Clean(units[i].Dir) == filepath.Clean(l.Root) && units[i].Path != "" {
			return units[i].Path, nil
		}
	}
	if l.Identity != "" {
		return l.Identity, nil
	}
	for i := range units {
		if units[i].Path != "" {
			return units[i].Path, nil
		}
	}
	return "", errors.New("the loaded target names no module path, which a report identifies it by")
}

// consumersReported is every consumer the load answered, at the directory the scope
// named it. A declared consumer that fails to load ends the run, so none is
// unavailable.
func (l *Loaded) consumersReported() ([]report.LoadedConsumer, error) {
	var held []load.Consumer
	if len(l.Loads) > 0 {
		held = l.Loads[0].Result.Consumers
	}
	named := make([]report.LoadedConsumer, len(held))
	for i := range held {
		if i >= len(l.Declared) {
			return nil, fmt.Errorf("the load answered %d consumers and the scope declared %d", len(held), len(l.Declared))
		}
		path, err := relativeToRunDirectory(l.Declared[i].Path)
		if err != nil {
			return nil, err
		}
		named[i] = report.LoadedConsumer{ID: held[i].ID, Path: path}
	}
	return named, nil
}

// configurationsReported is the build matrix as a report names it.
func configurationsReported(configurations []load.Configuration) []report.Configuration {
	named := make([]report.Configuration, len(configurations))
	for i, c := range configurations {
		named[i] = report.Configuration{ID: c.ID, Platform: &report.Platform{OS: c.OS, Arch: c.Arch, Tags: append([]string{}, c.Tags...)}}
	}
	return named
}

// configurationsNotBuilt is every dropped configuration with the first line of the
// error that dropped it: the rest lists each diagnostic at the absolute path the
// toolchain reported, which is a host detail.
func configurationsNotBuilt(unbuilt []load.Unbuilt) []report.ConfigurationNotBuilt {
	named := make([]report.ConfigurationNotBuilt, len(unbuilt))
	for i := range unbuilt {
		c := &unbuilt[i].Configuration
		named[i] = report.ConfigurationNotBuilt{
			ID:       c.ID,
			Platform: &report.Platform{OS: c.OS, Arch: c.Arch, Tags: append([]string{}, c.Tags...)},
			Error:    firstLine(unbuilt[i].Err.Error()),
		}
	}
	return named
}

// firstLine is the text up to the first line break.
func firstLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return line
}
