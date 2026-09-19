// Package report is the document one analysis produces and the five renderings of
// it.
//
// [Envelope] mirrors the Contract's report schema member for member, with the
// finding of the findings pass as the element of its finding list, so the JSON
// rendering marshals the envelope and decides nothing of its own. [Build]
// assembles the envelope from what one run answered and refuses what the Contract
// cannot carry; [Sort] and [Cap] reorder and bound the finding list; the five
// reporters render the same envelope, so no two of them can disagree about which
// findings exist.
//
// The envelope is an analyzer's own report. Two members belong to a merged report
// alone and are absent here: the list of input reports a merge read, and the
// analyzer name a merge keeps on each record, which this document states once in
// [Envelope.Analyzer]. Reading a merged report is the merging product's job, so
// [Envelope.UnmarshalJSON] refuses one.
//
// Nothing in this package validates a rendering against the JSON schema that
// governs it, and the schema is not embedded here: the schema lives with the
// Contract and the committed renderings are validated where it lives.
package report

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// SchemaVersion is the version of the report schema this package writes a
// document to. An analyzer names every version it reads in the analyzer object,
// and that list holds this one.
const SchemaVersion = "4.0.0"

// staleSuppressionCode is the code of a stale suppression, which is the one code
// whose records the envelope carries outside its finding list.
const staleSuppressionCode = "DS1703"

// deadState is the edge-evaluation state that carries a pending finding, and the
// one the pending total counts.
const deadState = "dead"

var (
	// ErrInput reports an assembly the Contract cannot carry: an analyzer, a
	// target or a matrix the schema requires and the input does not supply, a
	// stale-suppression row missing the mechanism or the record the Contract
	// requires of it, or an edge evaluation whose state and pending finding
	// disagree. Each is a defect at the seam rather than something a report
	// could say, so the assembly fails instead of writing a document no reader
	// admits.
	ErrInput = errors.New("report: incomplete input")

	// ErrOptions reports a reporter asked to render without what it needs: the
	// SARIF reporter with no reader for the source lines its fingerprint hashes,
	// and the template reporter with no template or with one that names
	// something the envelope does not carry.
	ErrOptions = errors.New("report: incomplete options")
)

// Analyzer is the product that wrote a report: the name it mints every identifier
// of its own under, the version that ran, the languages it analyzed, every schema
// version it reads, and its result over the conformance corpus.
//
//nolint:govet // fieldalignment: the field order is the Contract's field order, which a rendering writes
type Analyzer struct {
	Name                   string
	Version                string
	Languages              []string
	SchemaVersionsAccepted []string

	// Conformance is the product's result over the conformance corpus. The
	// Contract requires it on every report and a merge refuses a report whose
	// result is not a pass, so an assembly carrying none is refused rather than
	// written.
	Conformance Conformance
}

// Conformance is one product's answer over the conformance corpus: the corpus
// version it answered, its result over the whole corpus, and the digest of the
// results document its corpus runner wrote.
type Conformance struct {
	CorpusVersion string
	Result        string
	Digest        string
}

// Target is what was analyzed: whether its callers are all in the analyzed graph,
// the directory it was analyzed from relative to the directory the run was invoked
// from, and the name it publishes itself under.
type Target struct {
	Kind     string
	Root     string
	Identity string
}

// Configuration is one build configuration of the matrix the analysis ran, under
// the identifier every finding names it by.
type Configuration struct {
	ID   string
	OS   string
	Arch string
	Tags []string
}

// Consumers is what the run knows about the target's consumers: how many the
// configuration declared, the ones that loaded, and the ones it did not load.
type Consumers struct {
	Loaded      []LoadedConsumer
	Unavailable []UnavailableConsumer
	Declared    int
}

// LoadedConsumer is one consumer the analysis loaded beside the target. A
// reference from it keeps a target symbol live.
type LoadedConsumer struct {
	ID   string
	Path string
}

// UnavailableConsumer is one consumer the configuration declared and the analysis
// did not load, with the reason a reader of the report needs.
type UnavailableConsumer struct {
	ID     string
	Reason string
}

// EdgeEvaluation is one declared cross-language edge side the analysis
// enumerated, and the verdict it reached on the symbol that side names.
//
// Finding is the pending finding: the finding the analysis would have reported for
// the symbol, present exactly when the state is dead and absent for every other
// state. A pending finding appears nowhere else in the report.
type EdgeEvaluation struct {
	Finding *kinds.Finding
	Edge    string
	Side    string
	Symbol  string
	State   string
}

// StaleSuppression is one suppression that matches no current finding, which is a
// record of its own rather than a row of the finding list: a text line renders it
// with the fixed kind token, and the stale-suppression total counts it.
//
//nolint:govet // fieldalignment: the field order is the Contract's field order, which a rendering writes
type StaleSuppression struct {
	Code      string
	Mechanism string
	Entry     SuppressionEntry
	Position  SuppressionPosition
	Symbol    string
	Message   string
}

// SuppressionEntry is one suppression in the four-key form the ignore file and the
// baseline share, so one shape carries a record of any mechanism.
type SuppressionEntry struct {
	Code   string
	Symbol string
	Path   string
	Reason string
}

// SuppressionPosition is a suppression's own site, which is where its record is
// reported. A suppression spans no range, so it carries no end line.
type SuppressionPosition struct {
	Path   string
	Line   int
	Column int
}

// DeclaredGap is one capability the product declines, for one fixture and
// optionally one expectation of it, with the reason it declines it.
type DeclaredGap struct {
	Fixture    string
	Symbol     string
	Capability string
	Reason     string
}

// Suppressions is what the run's suppression records amount to: how many bound to
// a symbol that would otherwise have produced a finding, and how many carry a
// reason. Both are counts the summary prints and no rendering recomputes.
type Suppressions struct {
	InEffect        int
	ReasonsRecorded int
}

// Totals are the counts a summary prints, so a reader compares them without
// counting records.
type Totals struct {
	Findings             int
	BySeverity           BySeverity
	DeletableLines       int
	SuppressionsInEffect int
	ReasonsRecorded      int
	StaleSuppressions    int
	Pending              int
	Omitted              int
}

// BySeverity is how many findings of the run carry each severity, over the whole
// finding set rather than the printed subset, so a maximum finding count cannot
// change the verdict a reader draws from the counts.
type BySeverity struct {
	Allow int
	Warn  int
	Deny  int
}

// Envelope is one report: the Contract's report document, member for member.
//
//nolint:govet // fieldalignment: the field order is the Contract's field order, which a rendering writes
type Envelope struct {
	SchemaVersion     string
	ContractVersion   string
	Analyzer          Analyzer
	Target            Target
	Configurations    []Configuration
	Consumers         Consumers
	Findings          []kinds.Finding
	EdgeEvaluations   []EdgeEvaluation
	StaleSuppressions []StaleSuppression
	DeclaredGaps      []DeclaredGap
	ExcludedByCgo     []string
	TestFileRules     []graph.TestFileRule
	Totals            Totals
}

// Options is what a rendering needs beyond the envelope. One shape serves five
// reporters, so a reporter that needs nothing reads nothing.
type Options struct {
	// Read reads one file of the target by the target-relative path a position
	// spells, and is read by [SARIF] alone: the line fingerprint a code-scanning
	// service joins alerts on is a hash of the source line, so a SARIF rendering
	// without a reader is refused rather than written with a key a reader of the
	// document would recompute differently.
	Read graph.ReadFile

	// Template is the text of the user-supplied template, read by [Template]
	// alone.
	Template string
}

// BuildInput is what one run answered, from which an envelope is assembled.
//
//nolint:govet // fieldalignment: the field order is the order the envelope declares the members these fill
type BuildInput struct {
	// Analyzer, Target, Configurations and Consumers are what the invocation
	// resolved: the identity of the product that ran, what it analyzed, the
	// matrix it ran, and what it knows about the target's consumers.
	Analyzer       Analyzer
	Target         Target
	Configurations []Configuration
	Consumers      Consumers

	// Result is the findings pass. Its findings are already in the canonical
	// order and carry every field the Contract requires of a finding, so the
	// assembly reorders them and completes none of them.
	Result kinds.Result

	// EdgeEvaluations and DeclaredGaps are the records the envelope carries
	// beside the findings that the findings pass does not answer.
	//
	// The stale suppressions are not among them: a stale suppression IS answered
	// by the findings pass, under its own code, and the assembly takes those rows
	// out of the finding list into the array the Contract declares for them. So a
	// caller supplies them in Result like any other finding and there is no
	// second way to supply them.
	EdgeEvaluations []EdgeEvaluation
	DeclaredGaps    []DeclaredGap

	// ExcludedByCgo is every source file the toolchain ignored solely for
	// importing the C pseudo-package, unioned over the matrix, and TestFileRules
	// is the rules by which the run classified a file as a test file. Two entries
	// for one rule are one entry carrying the greater count, because every
	// configuration classifies the same files by the same rule.
	ExcludedByCgo []string
	TestFileRules []graph.TestFileRule

	// Suppressions is what the run's suppression records amount to, which no
	// rendering recomputes.
	Suppressions Suppressions
}

// Build assembles one envelope from what a run answered, ordering every array by
// the canonical key and computing every total.
//
// It refuses an assembly the Contract cannot carry rather than writing a document
// no reader admits: an analyzer, a target or a matrix a report is required to
// name, a schema version the analyzer does not claim to read, a consumer count
// that disagrees with the consumer lists, a stale-suppression row missing the
// mechanism or the record the Contract requires of it, and an edge evaluation
// whose state and pending finding disagree.
func Build(in *BuildInput) (Envelope, error) {
	if err := in.check(); err != nil {
		return Envelope{}, err
	}
	findings, stale, err := partition(in.Result.Findings)
	if err != nil {
		return Envelope{}, err
	}

	built := Envelope{
		SchemaVersion:     SchemaVersion,
		ContractVersion:   config.ContractVersion,
		Analyzer:          in.Analyzer,
		Target:            in.Target,
		Configurations:    slices.Clone(in.Configurations),
		Consumers:         in.Consumers,
		Findings:          findings,
		EdgeEvaluations:   slices.Clone(in.EdgeEvaluations),
		StaleSuppressions: stale,
		DeclaredGaps:      slices.Clone(in.DeclaredGaps),
		ExcludedByCgo:     unique(in.ExcludedByCgo),
		TestFileRules:     testFileRules(in.TestFileRules),
	}
	built.order()
	built.Totals = totalsOf(&built, in.Suppressions)
	return built, nil
}

// check refuses an assembly the Contract cannot carry.
func (in *BuildInput) check() error {
	if err := in.checkAnalyzer(); err != nil {
		return err
	}
	if err := in.checkTarget(); err != nil {
		return err
	}
	if len(in.Configurations) == 0 {
		return fmt.Errorf("%w: the report names no build configuration", ErrInput)
	}
	if want := len(in.Consumers.Loaded) + len(in.Consumers.Unavailable); in.Consumers.Declared != want {
		return fmt.Errorf("%w: the report declares %d consumers and lists %d",
			ErrInput, in.Consumers.Declared, want)
	}
	return in.checkEvaluations()
}

// checkAnalyzer refuses an analyzer object a report cannot name: one missing a
// name, a version, a language or a schema version, one that does not claim to read
// the version it is about to write, and one carrying no conformance result, which
// a merge refuses the report for.
func (in *BuildInput) checkAnalyzer() error {
	held := &in.Analyzer
	switch {
	case held.Name == "":
		return fmt.Errorf("%w: the report names no analyzer", ErrInput)
	case held.Version == "":
		return fmt.Errorf("%w: the analyzer %s names no version", ErrInput, held.Name)
	case len(held.Languages) == 0:
		return fmt.Errorf("%w: the analyzer %s names no language", ErrInput, held.Name)
	case !slices.Contains(held.SchemaVersionsAccepted, SchemaVersion):
		return fmt.Errorf("%w: the analyzer %s writes schema version %s and accepts %v",
			ErrInput, held.Name, SchemaVersion, held.SchemaVersionsAccepted)
	case held.Conformance.Result == "" || held.Conformance.CorpusVersion == "" || held.Conformance.Digest == "":
		return fmt.Errorf("%w: the analyzer %s carries no conformance result, which a merge refuses a report for",
			ErrInput, held.Name)
	}
	return nil
}

// checkTarget refuses a target a report cannot name.
func (in *BuildInput) checkTarget() error {
	switch {
	case in.Target.Kind == "":
		return fmt.Errorf("%w: the report declares no target kind", ErrInput)
	case in.Target.Root == "":
		return fmt.Errorf("%w: the report names no target root", ErrInput)
	case in.Target.Identity == "":
		return fmt.Errorf("%w: the report names no target identity", ErrInput)
	}
	return nil
}

// checkEvaluations refuses an edge evaluation whose state and pending finding
// disagree, which is the one shape the Contract states as a condition rather than
// as a field.
func (in *BuildInput) checkEvaluations() error {
	for i := range in.EdgeEvaluations {
		held := &in.EdgeEvaluations[i]
		switch {
		case held.State == deadState && held.Finding == nil:
			return fmt.Errorf("%w: the %s side of edge %s is dead and carries no pending finding",
				ErrInput, held.Side, held.Edge)
		case held.State != deadState && held.Finding != nil:
			return fmt.Errorf("%w: the %s side of edge %s is %s and carries a pending finding",
				ErrInput, held.Side, held.Edge, held.State)
		}
	}
	return nil
}

// partition splits the findings pass into the findings a report carries and the
// stale-suppression records it carries beside them.
//
// A stale suppression is answered by the findings pass under its own code, because it
// is a kind of the vocabulary like any other; the report declares it as a record of
// its own shape, in an array of its own, counted by a total of its own and rendered
// with a fixed kind token. So the move happens once, here, rather than in every
// reporter and in the composition root.
func partition(findings []kinds.Finding) ([]kinds.Finding, []StaleSuppression, error) {
	kept := make([]kinds.Finding, 0, len(findings))
	stale := []StaleSuppression{}
	for i := range findings {
		if findings[i].Code != staleSuppressionCode {
			kept = append(kept, findings[i])
			continue
		}
		record, err := staleSuppressionOf(&findings[i])
		if err != nil {
			return nil, nil, err
		}
		stale = append(stale, record)
	}
	return kept, stale, nil
}

// staleSuppressionOf is one stale-suppression finding as the record the report
// declares: the suppression's own site, the reference it was about, the mechanism that
// held it and the suppression as written.
//
// A finding that names no mechanism or no record is refused rather than written as a
// record with an empty member, because the Contract requires every one of them and a
// maintainer reading the record needs each to find the suppression.
func staleSuppressionOf(found *kinds.Finding) (StaleSuppression, error) {
	entry := found.Details.Entry
	switch {
	case found.Details.Mechanism == "":
		return StaleSuppression{}, fmt.Errorf("%w: %s reports %s and names no mechanism",
			ErrInput, found.Code, found.Symbol.Ref)
	case entry == nil:
		return StaleSuppression{}, fmt.Errorf("%w: %s reports %s and carries no suppression record",
			ErrInput, found.Code, found.Symbol.Ref)
	case entry.Code == "" || entry.Path == "" || entry.Reason == "":
		return StaleSuppression{}, fmt.Errorf("%w: %s reports %s with a record naming code %q, path %q and reason %q",
			ErrInput, found.Code, found.Symbol.Ref, entry.Code, entry.Path, entry.Reason)
	}
	return StaleSuppression{
		Code:      found.Code,
		Mechanism: found.Details.Mechanism,
		Entry:     SuppressionEntry(*entry),
		Position: SuppressionPosition{
			Path:   found.Position.Path,
			Line:   found.Position.Line,
			Column: found.Position.Column,
		},
		Symbol:  found.Symbol.Ref,
		Message: found.Message,
	}, nil
}

// order puts every array of the envelope in the order the Contract fixes for it,
// so two runs over an unchanged tree write the same bytes.
func (e *Envelope) order() {
	slices.SortStableFunc(e.Findings, kinds.Compare)
	slices.SortStableFunc(e.StaleSuppressions, compareStaleSuppressions)
	slices.SortStableFunc(e.DeclaredGaps, compareDeclaredGaps)
	slices.SortStableFunc(e.EdgeEvaluations, compareEvaluations)
	slices.SortFunc(e.Configurations, func(a, b Configuration) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(e.Consumers.Loaded, func(a, b LoadedConsumer) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(e.Consumers.Unavailable, func(a, b UnavailableConsumer) int { return cmp.Compare(a.ID, b.ID) })
}

// compareStaleSuppressions orders two stale suppressions by the canonical key,
// each component taken from the field the record carries it in.
//
//nolint:gocritic // hugeParam: the standard library's sort takes the element type by value
func compareStaleSuppressions(a, b StaleSuppression) int {
	return cmp.Or(
		cmp.Compare(a.Position.Path, b.Position.Path),
		cmp.Compare(a.Position.Line, b.Position.Line),
		cmp.Compare(a.Position.Column, b.Position.Column),
		cmp.Compare(a.Code, b.Code),
		cmp.Compare(a.Symbol, b.Symbol),
	)
}

// compareDeclaredGaps orders two declared gaps by the bytewise comparison of their
// compact encodings in the schema's property order, which is what the canonical
// key falls back to for a record supplying none of its components.
func compareDeclaredGaps(a, b DeclaredGap) int {
	return cmp.Or(
		cmp.Compare(a.Fixture, b.Fixture),
		cmp.Compare(a.Symbol, b.Symbol),
		cmp.Compare(a.Capability, b.Capability),
		cmp.Compare(a.Reason, b.Reason),
	)
}

// compareEvaluations orders two edge evaluations by the edge and then the side,
// each compared bytewise. The carrying analyzer's name is the third component and
// is this analyzer's own for every record of its own report.
func compareEvaluations(a, b EdgeEvaluation) int {
	return cmp.Or(
		cmp.Compare(a.Edge, b.Edge),
		cmp.Compare(a.Side, b.Side),
		cmp.Compare(a.Symbol, b.Symbol),
	)
}

// totalsOf counts what a summary prints over the whole finding set, so the counts
// describe the run rather than the printed subset.
func totalsOf(e *Envelope, held Suppressions) Totals {
	counted := Totals{
		Findings:             len(e.Findings),
		DeletableLines:       deletableLines(e.Findings),
		SuppressionsInEffect: held.InEffect,
		ReasonsRecorded:      held.ReasonsRecorded,
		StaleSuppressions:    len(e.StaleSuppressions),
	}
	for i := range e.Findings {
		switch e.Findings[i].Severity {
		case config.Allow:
			counted.BySeverity.Allow++
		case config.Warn:
			counted.BySeverity.Warn++
		case config.Deny:
			counted.BySeverity.Deny++
		}
	}
	for i := range e.EdgeEvaluations {
		if e.EdgeEvaluations[i].State == deadState {
			counted.Pending++
		}
	}
	return counted
}

// deletableLines is the number of source lines the deletion of everything the
// findings name removes, summed over the component of each component root so no
// line is counted twice. A component with several roots contributes once, because
// the members that fall with it are the same members whichever root names it.
func deletableLines(findings []kinds.Finding) int {
	counted := make(map[string]bool)
	total := 0
	for i := range findings {
		component := &findings[i].Component
		if !component.Root || counted[component.ID] {
			continue
		}
		counted[component.ID] = true
		total += component.DeletableLines
	}
	return total
}

// unique is one sorted copy of a path list with no repetition, and an empty list
// where it holds none, because the Contract admits no absent array.
func unique(paths []string) []string {
	held := make(map[string]bool, len(paths))
	for _, path := range paths {
		held[path] = true
	}
	return slices.Sorted(maps.Keys(held))
}

// testFileRules is one entry per rule, ordered by rule, carrying the greatest
// count any configuration reported for it: every configuration classifies the same
// files by the same rule, so the greatest count is the run's.
func testFileRules(rules []graph.TestFileRule) []graph.TestFileRule {
	matched := make(map[string]int, len(rules))
	for _, rule := range rules {
		matched[rule.Rule] = max(matched[rule.Rule], rule.Matched)
	}
	held := make([]graph.TestFileRule, 0, len(matched))
	for _, name := range slices.Sorted(maps.Keys(matched)) {
		held = append(held, graph.TestFileRule{Rule: name, Matched: matched[name]})
	}
	return held
}

// Sort orders the finding list the way a configuration asks for.
//
// The canonical key is the order a report is written in and the order every other
// array keeps; a sort by size orders findings by the lines a deletion removes,
// largest first, then by the size of the subject, then by the canonical key, so
// the order stays total. A sort by position is the canonical order itself, and so
// is any value the configuration does not admit.
func Sort(e *Envelope, by config.Sort) {
	if by != config.BySize {
		slices.SortStableFunc(e.Findings, kinds.Compare)
		return
	}
	slices.SortStableFunc(e.Findings, func(a, b kinds.Finding) int {
		return cmp.Or(
			cmp.Compare(b.Component.DeletableLines, a.Component.DeletableLines),
			cmp.Compare(b.Symbol.SizeLines, a.Symbol.SizeLines),
			kinds.Compare(a, b),
		)
	})
}

// Cap keeps the first findings of the current order and counts the rest as
// omitted, so a truncation a reader cannot see does not happen: the omitted count
// is in the report and in every rendering of it, and the finding count, the
// severity counts and the deletable-line total keep describing the whole set.
//
// A count of zero or less keeps every finding, which is what an unconfigured
// maximum means.
func Cap(e *Envelope, most int) {
	if most <= 0 || len(e.Findings) <= most {
		return
	}
	e.Totals.Omitted += len(e.Findings) - most
	e.Findings = e.Findings[:most]
}

// languageID is the automation identifier a code-scanning service tells one
// language's alerts from another's by: the languages of the run joined with a plus
// sign in bytewise order.
func (e *Envelope) languageID() string {
	return strings.Join(slices.Sorted(slices.Values(e.Analyzer.Languages)), "+")
}
