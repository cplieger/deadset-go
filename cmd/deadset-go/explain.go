package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

const explainUsage = "usage: deadset-go explain [--target=DIR] [--scope=FILE] [--config=FILE] [--central=FILE] " +
	"[--min-confidence=CLASS] [--cascade=DEPTH] SYMBOL"

// whyFlags are the three spellings the Contract's CLI table gives an explanation
// request, each naming the symbol to explain, and each equivalent to naming that
// symbol as the argument.
//
// The four spellings are one request because the analysis answers the state the
// symbol is actually in: exactly one of the four states below holds for a symbol, so
// a question about another state could only be answered with the state that does.
var whyFlags = []string{"why", "why-live", "why-not"}

// maxPartialMatches bounds the partial matches a refused request prints, so a
// misspelling over a large inventory prints a list a maintainer reads rather than the
// inventory.
const maxPartialMatches = 20

// state is what the analysis holds about one symbol, which decides the explanation
// printed. Exactly one holds for a symbol, so one request has one answer.
type state string

const (
	// stateReported is a symbol a finding of the run names.
	stateReported state = "reported"

	// stateRetained is a symbol an exemption held back, which is a symbol the
	// sweep would otherwise have reported.
	stateRetained state = "retained"

	// stateLive is a symbol both liveness relations hold live, so nothing the
	// analysis knows makes it a deletion candidate.
	stateLive state = "live"

	// stateDead is a symbol the sweep judged a candidate that no finding names:
	// it falls with the dead component whose root is reported, or its issue kind
	// is one the configuration silences.
	stateDead state = "dead"

	// stateUnjudged is a declaration neither liveness relation judges, which is a
	// file and a package: what reaches one is the subject of the file kinds, which
	// read the load rather than asking whether anything references the file. The
	// signature is the sweep's own answer rather than a second copy of its rule: a
	// declaration it judges is a candidate or is live under a relation.
	stateUnjudged state = "unjudged"
)

// explain writes what the analysis holds about one symbol to stdout, and exits with
// the usage code where the request names no symbol of the target.
//
// The answer comes from the same analysis the report comes from, so an explanation
// cannot disagree with a finding: the findings pass runs, and which of its answers
// names the symbol is what decides the explanation.
func explain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	named, req, code := explainFlags(ctx, args, stderr)
	if code != vocab.ExitClean {
		return code
	}
	set, err := findingsOf(ctx, req)
	if err != nil {
		return failed(ctx, stderr, err)
	}
	namedUnbuilt(stderr, set.Unbuilt)

	subject, partial := subjectOf(set.Merged, named)
	if subject == nil {
		fmt.Fprintf(stderr, "deadset-go: %q names no one symbol of the target\n", named)
		for _, candidate := range partial {
			fmt.Fprintf(stderr, "  %s\n", candidate)
		}
		if len(partial) == 0 {
			fmt.Fprintln(stderr, "  no symbol of the target partially matches it either")
		}
		return vocab.ExitUsage
	}
	if err := writeExplanation(stdout, set, req.Config.Reporters.CascadeFull, subject); err != nil {
		return unwrittenAnswer(stderr, "explain", err)
	}
	return vocab.ExitClean
}

// explainFlags parses the invocation and resolves the configuration documents it
// names.
func explainFlags(ctx context.Context, args []string, stderr io.Writer) (named string, req *run.Request, code int) {
	flags := configuredFlagSet("explain", explainUsage, stderr)
	asked := make(map[string]*string, len(whyFlags))
	for _, flagName := range whyFlags {
		asked[flagName] = flags.set.String(flagName, "", "the symbol to explain, as the argument names it")
	}
	if err := flags.set.Parse(args); err != nil {
		return "", nil, vocab.ExitUsage
	}

	named, err := namedSymbol(flags.set, asked)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		flags.set.Usage()
		return "", nil, vocab.ExitUsage
	}
	if req, code = flags.request(ctx, stderr); code != vocab.ExitClean {
		return "", nil, code
	}
	return named, req, vocab.ExitClean
}

// namedSymbol is the symbol one request names, from the argument or from one of the
// three flags the Contract's CLI table spells the request with.
//
// Two spellings naming a symbol is refused rather than resolved by precedence: a
// maintainer who named two meant one of them, and a rule choosing between them would
// silently answer about the other.
func namedSymbol(set *flag.FlagSet, asked map[string]*string) (string, error) {
	if set.NArg() > 1 {
		return "", fmt.Errorf("explain explains one symbol, and %d arguments were given", set.NArg())
	}
	named := make([]string, 0, len(asked)+1)
	spelled := make([]string, 0, len(asked)+1)
	if set.NArg() == 1 {
		named = append(named, set.Arg(0))
		spelled = append(spelled, "the argument")
	}
	for _, flagName := range whyFlags {
		if value := *asked[flagName]; value != "" {
			named = append(named, value)
			spelled = append(spelled, "--"+flagName)
		}
	}

	switch len(named) {
	case 0:
		return "", errors.New("explain explains the symbol the argument names, and no symbol was named")
	case 1:
		return named[0], nil
	default:
		return "", fmt.Errorf("explain explains one symbol, and %s each name one", strings.Join(spelled, ", "))
	}
}

// subjectOf is the one declaration a request names, and the partial matches where it
// names none or names several.
//
// Three keys are tried in order, each an exact match: the stable symbol reference,
// the fragment of it a maintainer types (the Type.Member form or a bare name), and
// the display name the inventory carries. A key several declarations answer is no
// answer, because a run cannot know which was meant, and the candidates are returned
// so the maintainer names one by its reference.
func subjectOf(merged *liveness.Merged, named string) (subject *inventory.Symbol, partial []string) {
	keys := []func(s *inventory.Symbol) string{
		func(s *inventory.Symbol) string { return s.Ref },
		fragmentOf,
		func(s *inventory.Symbol) string { return s.Name },
	}
	for _, key := range keys {
		var matched []int
		for i := range merged.Symbols {
			if key(&merged.Symbols[i]) == named {
				matched = append(matched, i)
			}
		}
		switch len(matched) {
		case 0:
			continue
		case 1:
			return &merged.Symbols[matched[0]], nil
		default:
			return nil, refsOf(merged, matched)
		}
	}
	return nil, partialMatches(merged, named)
}

// fragmentOf is the part of a symbol reference after the separator, which is the
// Type.Member form or the bare name a maintainer types.
func fragmentOf(s *inventory.Symbol) string {
	_, fragment, found := strings.Cut(s.Ref, "#")
	if !found {
		return ""
	}
	return fragment
}

// refsOf is the reference of every named declaration, in the inventory's order.
func refsOf(merged *liveness.Merged, at []int) []string {
	held := make([]string, 0, len(at))
	for _, i := range at {
		held = append(held, merged.Symbols[i].Ref+"\t"+merged.Symbols[i].Pos.String())
	}
	return held
}

// partialMatches is every declaration whose reference or display name holds the
// string asked for, ignoring letter case, ordered by reference and bounded.
func partialMatches(merged *liveness.Merged, named string) []string {
	folded := strings.ToLower(named)
	var held []string
	for i := range merged.Symbols {
		one := &merged.Symbols[i]
		if !strings.Contains(strings.ToLower(one.Ref), folded) && !strings.Contains(strings.ToLower(one.Name), folded) {
			continue
		}
		held = append(held, one.Ref+"\t"+one.Pos.String())
	}
	slices.Sort(held)
	if len(held) > maxPartialMatches {
		held = held[:maxPartialMatches]
	}
	return held
}

// writeExplanation writes the one explanation that applies to the subject.
//
// The whole text is built first and written once, so a failure partway through the
// rendering leaves nothing half-printed and the writer is touched exactly once.
func writeExplanation(w io.Writer, set *run.Findings, full bool, subject *inventory.Symbol) error {
	var held strings.Builder
	fmt.Fprintf(&held, "symbol: %s\n", subject.Ref)
	fmt.Fprintf(&held, "declaration: %s %s\n", subject.Kind, subject.Name)
	fmt.Fprintf(&held, "position: %s\n", subject.Pos)
	fmt.Fprintf(&held, "configurations: %s\n", configurationNames(set.Identifiers(), subject.Configs))

	found := findingAbout(set.Result.Findings, subject)
	retained := retentionsOf(set.Retained(), subject.ID)
	switch {
	case found != nil:
		fmt.Fprintf(&held, "answer: %s\n", stateReported)
		writeReported(&held, found)
	case len(retained) > 0:
		fmt.Fprintf(&held, "answer: %s\n", stateRetained)
		writeRetained(&held, retained)
	default:
		writeLiveness(&held, set, full, subject)
	}
	writeNamedElsewhere(&held, set.Result.Findings, subject)
	_, err := io.WriteString(w, held.String())
	return err
}

// writeNamedElsewhere names every finding of the run that names this declaration by
// reference and reports at another position, one line each in the report's own order:
// a part of it (a parameter by its own reference under the declaration's), a
// compile-time assertion of an interface, and a suppression record. Each is published
// under the reference asked about, so leaving it out would disagree with the report;
// none changes the answer above, which is about the declaration itself.
func writeNamedElsewhere(held *strings.Builder, findings []kinds.Finding, subject *inventory.Symbol) {
	for i := range findings {
		found := &findings[i]
		if found.DeclarationRef() != subject.Ref || positionKey(&found.Position) == string(subject.ID) {
			continue
		}
		fmt.Fprintf(held, "also reported: %s %s\t%s\t%s %s\n", found.Code, found.Kind,
			positionKey(&found.Position), found.Symbol.Kind, found.Symbol.Name)
	}
}

// writeReported is the explanation of a symbol a finding names: the finding's code
// and kind, its message, the reachability class and confidence, the liveness
// relation that decided it, the configurations it holds under and the consumers the
// run loaded.
func writeReported(held *strings.Builder, found *kinds.Finding) {
	fmt.Fprintf(held, "  code: %s %s\n", found.Code, found.Kind)
	fmt.Fprintf(held, "  message: %s\n", found.Message)
	fmt.Fprintf(held, "  class: %s\n", found.Class)
	fmt.Fprintf(held, "  confidence: %s\n", found.Confidence)
	if !found.Live {
		fmt.Fprintf(held, "  liveness relation: %s\n", found.Relation)
	}
	fmt.Fprintf(held, "  configurations: %s\n", listed(found.Configurations))
	fmt.Fprintf(held, "  loaded consumers: %s\n", listed(found.ConsumersLoaded))
	for _, class := range found.RetainedBy {
		fmt.Fprintf(held, "  retained by: %s\n", class)
	}
}

// writeRetained is the explanation of a symbol an exemption held back: every class
// that held it, with the site the evidence was found at and the clause naming that
// evidence.
func writeRetained(held *strings.Builder, retained []exempt.Exemption) {
	fmt.Fprintf(held, "  held back by: %s\n", counted(len(retained), "exemption class"))
	for i := range retained {
		one := &retained[i]
		fmt.Fprintf(held, "  class: %s\t%s\t%s\n", one.Class, evidenceSite(one), one.Detail)
	}
}

// writeLiveness is the explanation of a symbol no finding names and no exemption held
// back: the relations that hold it live, the root a path of references reaches it
// from, and the dead component it falls with where the sweep judged it a candidate.
func writeLiveness(held *strings.Builder, set *run.Findings, full bool, subject *inventory.Symbol) {
	candidate := candidateFor(set.Liveness.Candidates, subject.ID)
	live := set.Liveness.LiveUnder[subject.ID]
	fmt.Fprintf(held, "answer: %s\n", livenessState(candidate, live))
	if livenessState(candidate, live) == stateUnjudged {
		fmt.Fprintf(held, "  unjudged: neither liveness relation judges a %s\n", subject.Kind)
		return
	}
	fmt.Fprintf(held, "  live under: %s\n", relationsOf(live))

	for _, root := range rootReasons(set.Merged.Roots, subject.ID) {
		fmt.Fprintf(held, "  root: %s%s\n", root.Kind, sourceNote(root.Source))
	}
	writePath(held, set.Swept, subject.ID)
	if candidate != nil {
		writeComponent(held, set, full, subject.ID)
		fmt.Fprintf(held, "  candidate: dead under %s, with %d production and %d test references\n",
			candidate.Relation, candidate.ProductionRefs, candidate.TestRefs)
	}
}

// livenessState is the state one symbol no finding names and no exemption held back
// is in, read from the sweep's own answers.
func livenessState(candidate *liveness.Candidate, live liveness.RelationSet) state {
	switch {
	case candidate != nil:
		return stateDead
	case live.Has(liveness.ReferenceCounting) && live.Has(liveness.Reachability):
		return stateLive
	default:
		return stateUnjudged
	}
}

// writePath is the path the run's sweep reached the symbol by
// ([liveness.Result.Path]), from the root, the loaded consumer, the exemption or
// the mark it starts at, one line per hop, each hop the mode counts as a test
// file's marked and each a part of the matrix makes named with its configurations;
// else the references to the symbol.
func writePath(held *strings.Builder, set *run.Swept, id inventory.SymbolID) {
	if path, reached := set.Liveness.Path(id); reached {
		writeSeed(held, &path)
		for i := range path.Hops {
			writeHop(held, &path.Hops[i], set.Mode, set.Identifiers())
		}
		return
	}

	references := referencesTo(set.Merged.References, id)
	fmt.Fprintln(held, "  unreachable: no path of references reaches it from a root")
	fmt.Fprintf(held, "  references: %d\n", len(references))
	for i := range references {
		fmt.Fprintf(held, "  reference: %s\t%s\tfrom %s%s\n",
			references[i].Pos, useOf(&references[i]), references[i].From, testFileNote(references[i].Test))
	}
}

// writeSeed is where a path starts: a root, a loaded consumer, an exemption or a
// mark. A root the symbol itself is needs no line: the root lines name it.
func writeSeed(held *strings.Builder, path *liveness.Path) {
	switch {
	case path.Root != nil && len(path.Hops) > 0:
		fmt.Fprintf(held, "  reached from: root %s %s\n", path.Root.Kind, path.Root.ID)
	case path.Exemption != nil:
		fmt.Fprintf(held, "  reached from: exemption %s of %s\n", path.Exemption.Class, path.Exemption.ID)
	case path.Mark != "":
		fmt.Fprintf(held, "  reached from: suppression of %s\n", path.Mark)
	case path.Root == nil && len(path.Hops) > 0:
		fmt.Fprintf(held, "  reached from: loaded consumer %s\n", path.Hops[0].Reference.Consumer)
	}
}

// writeHop is one hop of a path: a reference, which a loaded consumer's is made by
// that consumer, or a use an exemption states or the analysis infers.
func writeHop(held *strings.Builder, hop *liveness.Hop, mode facts.Mode, identifiers []string) {
	r := hop.Reference
	switch {
	case r == nil && hop.Exemption != nil:
		fmt.Fprintf(held, "  use: exemption %s\t%s -> %s\n", hop.Exemption.Class, hop.From, hop.To)
		return
	case r == nil:
		fmt.Fprintf(held, "  use: inferred\t%s -> %s\n", hop.From, hop.To)
		return
	}
	from := string(r.From)
	if r.Consumer != "" {
		from = r.Consumer
	}
	fmt.Fprintf(held, "  reference: %s\t%s\t%s -> %s%s%s\n", consumerSite(r.Consumer, r.Pos), useOf(r), from, r.To,
		testFileNote(mode.TestReference(r)), configurationNote(identifiers, r.Configs))
}

// configurationNote names the configurations one reference is made in where they
// are not every configuration of the matrix.
func configurationNote(identifiers []string, configs inventory.ConfigSet) string {
	if len(configs.Indexes()) == len(identifiers) {
		return ""
	}
	return "\tin " + configurationNames(identifiers, configs)
}

// useOf is how one reference uses its target, and the rule that made it where no
// identifier at the site names the target.
func useOf(r *facts.Reference) string {
	if !r.Via.Synthesized() {
		return r.Kind.String()
	}
	return r.Kind.String() + " via " + r.Via.String()
}

// writeComponent is the dead component one candidate falls with, listed at the depth
// the configuration asks for, and the identifier the report mints for it where the
// report names the component's root.
func writeComponent(held *strings.Builder, set *run.Findings, full bool, id inventory.SymbolID) {
	component := componentOf(set.Liveness.Components, id)
	if component == nil {
		return
	}
	cascade := vocab.ReportersCascadeRoots
	if full {
		cascade = vocab.ReportersCascadeFull
	}
	fmt.Fprintf(held, "  component: %s, %s and %s fall with it, listed at cascade %s\n",
		componentIdentity(set, component.Roots),
		counted(len(component.Members), "symbol"), counted(kinds.DeletableLines(component), "deletable line"), cascade)
	for _, root := range component.Roots {
		fmt.Fprintf(held, "  component root: %s\n", root)
	}
	if full {
		for _, member := range component.Members {
			fmt.Fprintf(held, "  component member: %s\n", member)
		}
	}
}

// componentIdentity is the identifier the report mints for one dead component, read
// from the finding that names one of its roots, and the cascade mode's own spelling
// where the report names no root of it.
//
// The identifier is read from the report rather than minted here, so this command
// holds no second copy of the rule that mints it.
func componentIdentity(set *run.Findings, rooted []inventory.SymbolID) string {
	for i := range set.Result.Findings {
		found := &set.Result.Findings[i]
		if slices.Contains(rooted, inventory.SymbolID(positionKey(&found.Position))) {
			return found.Component.ID
		}
	}
	return "named by no finding of this run"
}

// positionKey is the position of one finding as the inventory keys a declaration by
// it: the target-relative path, the line and the column, which is what a symbol
// identifier renders.
func positionKey(at *kinds.Position) string {
	return at.Path + ":" + strconv.Itoa(at.Line) + ":" + strconv.Itoa(at.Column)
}

// findingAbout is the one finding of the run whose subject is this declaration, and
// nil where none is.
//
// The match is on the declaration's position rather than on its reference, because a
// reference is not one declaration's alone: a declaration written with the blank
// identifier carries the reference of its container.
func findingAbout(findings []kinds.Finding, subject *inventory.Symbol) *kinds.Finding {
	key := string(subject.ID)
	for i := range findings {
		if positionKey(&findings[i].Position) == key {
			return &findings[i]
		}
	}
	return nil
}

// retentionsOf is every exemption that held one symbol back, in the order the sweep
// returned them.
func retentionsOf(retained []exempt.Exemption, id inventory.SymbolID) []exempt.Exemption {
	var held []exempt.Exemption
	for i := range retained {
		if retained[i].ID == id {
			held = append(held, retained[i])
		}
	}
	return held
}

// candidateFor is the candidate record of one symbol, and nil where the sweep judged
// the symbol live.
func candidateFor(candidates []liveness.Candidate, id inventory.SymbolID) *liveness.Candidate {
	for i := range candidates {
		if candidates[i].ID == id {
			return &candidates[i]
		}
	}
	return nil
}

// rootReasons is every reason one symbol is a root, in the order the merge returned
// them, with one entry per reason whatever the number of configurations that
// detected it.
func rootReasons(rooted []roots.Root, id inventory.SymbolID) []roots.Root {
	var held []roots.Root
	for i := range rooted {
		if rooted[i].ID != id {
			continue
		}
		if slices.ContainsFunc(held, func(r roots.Root) bool {
			return r.Kind == rooted[i].Kind && r.Source == rooted[i].Source
		}) {
			continue
		}
		held = append(held, rooted[i])
	}
	return held
}

// referencesTo is every reference to one symbol, in the order the merge returned
// them, with one entry per site whatever the number of configurations that compiled
// it.
func referencesTo(references []facts.Reference, id inventory.SymbolID) []facts.Reference {
	var held []facts.Reference
	for i := range references {
		if references[i].To != id {
			continue
		}
		if slices.ContainsFunc(held, func(r facts.Reference) bool { return r.Pos == references[i].Pos }) {
			continue
		}
		held = append(held, references[i])
	}
	return held
}

// componentOf is the dead component one symbol is a member of, and nil where no
// component holds it.
func componentOf(components []liveness.Component, id inventory.SymbolID) *liveness.Component {
	for i := range components {
		if slices.Contains(components[i].Members, id) {
			return &components[i]
		}
	}
	return nil
}

// relationsOf is the relations one symbol is live under, and the statement that none
// is where the set is empty.
func relationsOf(held liveness.RelationSet) string {
	var named []string
	for _, relation := range []liveness.Relation{liveness.ReferenceCounting, liveness.Reachability} {
		if held.Has(relation) {
			named = append(named, relation.String())
		}
	}
	return listed(named)
}

// configurationNames is the identifiers of the configurations one declaration exists
// in, in the matrix's own order.
func configurationNames(identifiers []string, configs inventory.ConfigSet) string {
	named := make([]string, 0, len(identifiers))
	for _, at := range configs.Indexes() {
		named = append(named, configurationName(identifiers, at))
	}
	return listed(named)
}

// listed renders a list for a line a maintainer reads, and says none where the list
// is empty rather than leaving the field blank.
func listed(held []string) string {
	if len(held) == 0 {
		return "none"
	}
	return strings.Join(held, " ")
}

// sourceNote names the configured string that made a symbol a root, and nothing for
// a class no configured string named.
func sourceNote(source string) string {
	if source == "" {
		return ""
	}
	return "\tnamed by " + source
}

// testFileNote marks a reference a test file made, which is a reference a production
// sweep does not count.
func testFileNote(test bool) string {
	if !test {
		return ""
	}
	return "\tfrom a test file"
}
