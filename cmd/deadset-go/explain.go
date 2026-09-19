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

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
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
	named, resolved, code := explainFlags(args, stderr)
	if code != exitClean {
		return code
	}

	options, err := exemptOptions(&resolved.config)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitUsage
	}
	set, err := findingsOf(ctx, &resolved, &options)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitCodeFor(err)
	}
	namedUnbuilt(stderr, set.loaded.unbuilt)

	subject, partial := subjectOf(set.loaded.merged, named)
	if subject == nil {
		fmt.Fprintf(stderr, "deadset-go: %q names no one symbol of the target\n", named)
		for _, candidate := range partial {
			fmt.Fprintf(stderr, "  %s\n", candidate)
		}
		if len(partial) == 0 {
			fmt.Fprintln(stderr, "  no symbol of the target partially matches it either")
		}
		return exitUsage
	}
	if err := writeExplanation(stdout, &set, cascadeOf(resolved.config.Reporters.Cascade), subject); err != nil {
		fmt.Fprintf(stderr, "deadset-go: explain: %v\n", err)
		return exitFailure
	}
	return exitClean
}

// explainFlags parses the invocation and resolves the configuration documents it
// names.
func explainFlags(args []string, stderr io.Writer) (named string, resolved resolution, code int) {
	flags := configuredFlagSet("explain", explainUsage, stderr, true)
	asked := make(map[string]*string, len(whyFlags))
	for _, flagName := range whyFlags {
		asked[flagName] = flags.set.String(flagName, "", "the symbol to explain, as the argument names it")
	}
	if err := flags.set.Parse(args); err != nil {
		return "", resolution{}, exitUsage
	}

	named, err := namedSymbol(flags.set, asked)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		flags.set.Usage()
		return "", resolution{}, exitUsage
	}
	if resolved, code = flags.resolution(stderr); code != exitClean {
		return "", resolution{}, code
	}
	return named, resolved, exitClean
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
func subjectOf(merged *graph.Merged, named string) (subject *graph.Symbol, partial []string) {
	keys := []func(s *graph.Symbol) string{
		func(s *graph.Symbol) string { return s.Ref },
		fragmentOf,
		func(s *graph.Symbol) string { return s.Name },
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
func fragmentOf(s *graph.Symbol) string {
	_, fragment, found := strings.Cut(s.Ref, "#")
	if !found {
		return ""
	}
	return fragment
}

// refsOf is the reference of every named declaration, in the inventory's order.
func refsOf(merged *graph.Merged, at []int) []string {
	held := make([]string, 0, len(at))
	for _, i := range at {
		held = append(held, merged.Symbols[i].Ref+"\t"+merged.Symbols[i].Pos.String())
	}
	return held
}

// partialMatches is every declaration whose reference or display name holds the
// string asked for, ignoring letter case, ordered by reference and bounded.
func partialMatches(merged *graph.Merged, named string) []string {
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
func writeExplanation(w io.Writer, set *findingSet, cascade graph.Cascade, subject *graph.Symbol) error {
	var held strings.Builder
	fmt.Fprintf(&held, "symbol: %s\n", subject.Ref)
	fmt.Fprintf(&held, "declaration: %s %s\n", subject.Kind, subject.Name)
	fmt.Fprintf(&held, "position: %s\n", subject.Pos)
	fmt.Fprintf(&held, "configurations: %s\n", configurationNames(set.loaded.identifiers, subject.Configs))

	found := findingAbout(set.result.Findings, subject)
	retained := retentionsOf(set.swept.Retained, subject.ID)
	switch {
	case found != nil:
		fmt.Fprintf(&held, "answer: %s\n", stateReported)
		writeReported(&held, found)
	case len(retained) > 0:
		fmt.Fprintf(&held, "answer: %s\n", stateRetained)
		writeRetained(&held, retained)
	default:
		writeLiveness(&held, set, cascade, subject)
	}
	_, err := io.WriteString(w, held.String())
	return err
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
func writeRetained(held *strings.Builder, retained []graph.Exemption) {
	fmt.Fprintf(held, "  held back by: %s\n", counted(len(retained), "exemption class"))
	for i := range retained {
		one := &retained[i]
		fmt.Fprintf(held, "  class: %s\t%s\t%s\n", one.Class, evidenceSite(one.Site), one.Detail)
	}
}

// writeLiveness is the explanation of a symbol no finding names and no exemption held
// back: the relations that hold it live, the root a path of references reaches it
// from, and the dead component it falls with where the sweep judged it a candidate.
func writeLiveness(held *strings.Builder, set *findingSet, cascade graph.Cascade, subject *graph.Symbol) {
	candidate := candidateFor(set.swept.Candidates, subject.ID)
	live := set.swept.LiveUnder[subject.ID]
	fmt.Fprintf(held, "answer: %s\n", livenessState(candidate, live))
	if livenessState(candidate, live) == stateUnjudged {
		fmt.Fprintf(held, "  unjudged: neither liveness relation judges a %s\n", subject.Kind)
		return
	}
	fmt.Fprintf(held, "  live under: %s\n", relationsOf(live))

	for _, root := range rootReasons(set.loaded.merged.Roots, subject.ID) {
		fmt.Fprintf(held, "  root: %s%s\n", root.Kind, sourceNote(root.Source))
	}
	writePath(held, set.loaded.merged, subject.ID)
	if candidate != nil {
		writeComponent(held, set, cascade, subject.ID)
		fmt.Fprintf(held, "  candidate: dead under %s, with %d production and %d test references\n",
			candidate.Relation, candidate.ProductionRefs, candidate.TestRefs)
	}
}

// livenessState is the state one symbol no finding names and no exemption held back
// is in, read from the sweep's own answers.
func livenessState(candidate *graph.Candidate, live graph.RelationSet) state {
	switch {
	case candidate != nil:
		return stateDead
	case live.Has(graph.ReferenceCounting) && live.Has(graph.Reachability):
		return stateLive
	default:
		return stateUnjudged
	}
}

// writePath is the shortest path of references from a root to the symbol, one line
// per hop, and the references that reach it where no path from a root does.
func writePath(held *strings.Builder, merged *graph.Merged, id graph.SymbolID) {
	root, path, reached := pathFrom(merged, id)
	if reached {
		if len(path) == 0 {
			return
		}
		fmt.Fprintf(held, "  reached from: root %s %s\n", root.Kind, root.ID)
		for i := range path {
			fmt.Fprintf(held, "  reference: %s\t%s\t%s -> %s%s\n",
				path[i].Pos, path[i].Kind, path[i].From, path[i].To, testFileNote(path[i].Test))
		}
		return
	}

	references := referencesTo(merged.References, id)
	fmt.Fprintln(held, "  unreachable: no path of references reaches it from a root")
	fmt.Fprintf(held, "  references: %d\n", len(references))
	for i := range references {
		fmt.Fprintf(held, "  reference: %s\t%s\tfrom %s%s\n",
			references[i].Pos, references[i].Kind, references[i].From, testFileNote(references[i].Test))
	}
}

// writeComponent is the dead component one candidate falls with, listed at the depth
// the configuration asks for, and the identifier the report mints for it where the
// report names the component's root.
func writeComponent(held *strings.Builder, set *findingSet, cascade graph.Cascade, id graph.SymbolID) {
	component := componentOf(set.swept.Components, id)
	if component == nil {
		return
	}
	listing := component.List(cascade)
	fmt.Fprintf(held, "  component: %s, %s and %s fall with it, listed at cascade %s\n",
		componentIdentity(set, listing.Roots),
		counted(listing.SymbolCount, "symbol"), counted(listing.DeletableLines, "deletable line"), cascade)
	for _, root := range listing.Roots {
		fmt.Fprintf(held, "  component root: %s\n", root)
	}
	for _, member := range listing.Members {
		fmt.Fprintf(held, "  component member: %s\n", member)
	}
}

// componentIdentity is the identifier the report mints for one dead component, read
// from the finding that names one of its roots, and the cascade mode's own spelling
// where the report names no root of it.
//
// The identifier is read from the report rather than minted here, so this command
// holds no second copy of the rule that mints it.
func componentIdentity(set *findingSet, roots []graph.SymbolID) string {
	for i := range set.result.Findings {
		found := &set.result.Findings[i]
		if slices.Contains(roots, graph.SymbolID(positionKey(&found.Position))) {
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
func findingAbout(findings []kinds.Finding, subject *graph.Symbol) *kinds.Finding {
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
func retentionsOf(retained []graph.Exemption, id graph.SymbolID) []graph.Exemption {
	var held []graph.Exemption
	for i := range retained {
		if retained[i].ID == id {
			held = append(held, retained[i])
		}
	}
	return held
}

// candidateFor is the candidate record of one symbol, and nil where the sweep judged
// the symbol live.
func candidateFor(candidates []graph.Candidate, id graph.SymbolID) *graph.Candidate {
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
func rootReasons(roots []graph.Root, id graph.SymbolID) []graph.Root {
	var held []graph.Root
	for i := range roots {
		if roots[i].ID != id {
			continue
		}
		if slices.ContainsFunc(held, func(r graph.Root) bool {
			return r.Kind == roots[i].Kind && r.Source == roots[i].Source
		}) {
			continue
		}
		held = append(held, roots[i])
	}
	return held
}

// referencesTo is every reference to one symbol, in the order the merge returned
// them, with one entry per site whatever the number of configurations that compiled
// it.
func referencesTo(references []graph.Reference, id graph.SymbolID) []graph.Reference {
	var held []graph.Reference
	for i := range references {
		if references[i].To != id {
			continue
		}
		if slices.ContainsFunc(held, func(r graph.Reference) bool { return r.Pos == references[i].Pos }) {
			continue
		}
		held = append(held, references[i])
	}
	return held
}

// componentOf is the dead component one symbol is a member of, and nil where no
// component holds it.
func componentOf(components []graph.Component, id graph.SymbolID) *graph.Component {
	for i := range components {
		if slices.Contains(components[i].Members, id) {
			return &components[i]
		}
	}
	return nil
}

// pathFrom is the path one explanation prints: the shortest path of production
// references from a root no test file declares, and the shortest path over every
// reference where no such path exists.
//
// The production path is preferred because it is the path the sweep the report was
// built from counts, so a symbol that sweep holds live is named live for a production
// reason. Where the only path runs through a test file the answer prints it and says
// so on the hop.
func pathFrom(merged *graph.Merged, to graph.SymbolID) (graph.Root, []graph.Reference, bool) {
	if root, path, reached := shortestPath(merged, to, productionReferences); reached {
		return root, path, true
	}
	return shortestPath(merged, to, everyReference)
}

// referenceSet is which references one path walk follows. It is the walk's own
// question rather than the run's mode: an explanation prefers the path the report's
// sweep counts and answers over every reference where there is no such path, so one
// explanation walks both sets whatever mode the run analysed under.
type referenceSet uint8

const (
	// productionReferences is the set a production sweep counts: no reference a
	// test file made, and no root a test file declares.
	productionReferences referenceSet = iota

	// everyReference is every reference and every root, which is the set that
	// answers where no production path reaches the symbol.
	everyReference
)

// shortestPath is the shortest path of references from a root to one symbol, and the
// root it starts from.
//
// The search is breadth-first from every root at once, in the order the merge holds
// the roots and then the references, so the path is a shortest one and is the same
// path on every run over one tree. A symbol that is itself a root is reached by the
// empty path.
//
// The production set drops the test roots from the seed and every reference a test
// file made, which is the set the production sweep counts.
func shortestPath(merged *graph.Merged, to graph.SymbolID, follows referenceSet) (graph.Root, []graph.Reference, bool) {
	out := adjacency(merged, follows)
	steps, seen := rootSeed(merged, follows)

	for head := 0; head < len(steps); head++ {
		if steps[head].at == to {
			return merged.Roots[steps[head].root], pathOf(merged, steps, head), true
		}
		for _, at := range out[steps[head].at] {
			next := merged.References[at].To
			if seen[next] {
				continue
			}
			seen[next] = true
			steps = append(steps, walked{at: next, reference: at, previous: head, root: steps[head].root})
		}
	}
	return graph.Root{}, nil, false
}

// adjacency is the references each symbol makes, by the index the merge holds them
// at, so the walk follows them in the order the merge returned them.
func adjacency(merged *graph.Merged, follows referenceSet) map[graph.SymbolID][]int {
	out := make(map[graph.SymbolID][]int, len(merged.Symbols))
	for i := range merged.References {
		if follows == productionReferences && merged.References[i].Test {
			continue
		}
		out[merged.References[i].From] = append(out[merged.References[i].From], i)
	}
	return out
}

// rootSeed is the walk's first steps, one per root the seed holds, and the symbols
// already reached.
func rootSeed(merged *graph.Merged, follows referenceSet) (steps []walked, seen map[graph.SymbolID]bool) {
	steps = make([]walked, 0, len(merged.Symbols))
	seen = make(map[graph.SymbolID]bool, len(merged.Symbols))
	for i := range merged.Roots {
		if seen[merged.Roots[i].ID] || (follows == productionReferences && merged.Roots[i].Kind == graph.RootTest) {
			continue
		}
		seen[merged.Roots[i].ID] = true
		steps = append(steps, walked{at: merged.Roots[i].ID, reference: -1, previous: -1, root: i})
	}
	return steps, seen
}

// walked is one step of the breadth-first walk: the symbol reached, the reference
// that reached it, the step that made that reference, and the root the walk started
// from. The reference and the previous step are absent for a root.
type walked struct {
	at        graph.SymbolID
	reference int
	previous  int
	root      int
}

// pathOf is the path one step was reached by, from the root to the symbol.
func pathOf(merged *graph.Merged, steps []walked, at int) []graph.Reference {
	var path []graph.Reference
	for held := at; steps[held].reference >= 0; held = steps[held].previous {
		path = append(path, merged.References[steps[held].reference])
	}
	slices.Reverse(path)
	return path
}

// relationsOf is the relations one symbol is live under, and the statement that none
// is where the set is empty.
func relationsOf(held graph.RelationSet) string {
	var named []string
	for _, relation := range []graph.Relation{graph.ReferenceCounting, graph.Reachability} {
		if held.Has(relation) {
			named = append(named, relation.String())
		}
	}
	return listed(named)
}

// configurationNames is the identifiers of the configurations one declaration exists
// in, in the matrix's own order.
func configurationNames(identifiers []string, configs graph.ConfigSet) string {
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

// cascadeOf is the graph's own spelling of the configured cascade depth, so the
// listing a component answers is the depth the configuration asked for.
func cascadeOf(configured config.Cascade) graph.Cascade {
	if configured == config.CascadeFull {
		return graph.CascadeFull
	}
	return graph.CascadeRoots
}
