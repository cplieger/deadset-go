// Package kinds turns one run's liveness answers into the findings a report
// carries.
//
// One emitter per issue kind decides which subjects of its own kind to report and
// says why in one sentence; the framework does everything the Contract requires of
// every finding whatever its kind, so no kind can get it wrong or spell it
// differently. It runs the emitters the resolved severity does not disable,
// completes each finding with the reachability class, the confidence, the
// severity, the fixability, the dead component, the configurations, the loaded
// consumers and the generated-file flag, refuses a finding the Contract cannot
// carry, drops what the configured minimum confidence excludes and counts it, and
// returns the survivors in the canonical order.
//
// A kind decides which code a subject is reported under and never re-decides
// liveness: the candidate set is the sweep's. A kind reads no severity and sets no
// class, because those are the two dials the report keeps separate.
package kinds

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

// The constants of every finding this analyzer produces: the language it analyzes,
// and the name it mints every identifier of its own under.
const (
	language     = "go"
	analyzerName = "deadset-go"
)

// generatedFixability is what a mechanical edit may do with a finding in a
// generated file, which is nothing: the next run of the generator rewrites it.
const generatedFixability = "none"

var (
	// ErrInput reports a findings pass with nothing to read: no input, or no
	// resolved configuration to read the severity map from.
	ErrInput = errors.New("kinds: incomplete input")

	// ErrEmitter reports a finding an emitter returned that the Contract cannot
	// carry: a message that is not one sentence, a code that is not the
	// emitter's own, a second finding about one declaration, or a finding about
	// a symbol an exemption retained. Each is a defect in an emitter rather than
	// a row of a report, so the pass fails instead of publishing it.
	ErrEmitter = errors.New("kinds: emitter defect")
)

// Class is the reachability class of the Contract: what the analysis knows about
// a subject's callers.
type Class string

// The three classes, from what the analysis knows to what it does not.
const (
	Certain  Class = "certain"
	Probable Class = "probable"
	Possible Class = "possible"
)

// Position is where a subject is, rendered the way a report carries it: a
// target-relative path with the solidus as separator, a column counting UTF-16
// code units, and the line the subject ends on.
type Position struct {
	Path    string
	Line    int
	Column  int
	EndLine int
}

// Subject is what a finding is about: the reference that survives an edit above
// it, the vocabulary's word for the kind of subject, the name a text line renders,
// and the number of source lines it spans.
type Subject struct {
	Ref       string
	Kind      string
	Name      string
	SizeLines int
}

// Component is the dead component a subject belongs to, so a report is worked from
// its roots and one deletion removes a cluster. The identifier is minted by the
// framework, prefixed with this analyzer's own name so a merge unions components
// without renaming one.
type Component struct {
	ID             string
	Root           bool
	SymbolCount    int
	DeletableLines int
}

// Positioned is one symbol a finding names beside its subject, with its position.
type Positioned struct {
	Ref      string
	Name     string
	Position Position
}

// Details carries the per-kind members of the Contract's details object. A kind
// sets only its own, and a reporter omits the members the kind left unset.
//
//nolint:govet // fieldalignment: the field order is the Contract's field order, which a reporter writes
type Details struct {
	NarrowerVisibility string       // the visibility the references support
	Implementations    []Positioned // the concrete implementations of an interface
	WritePositions     []Position   // every position a subject is written at
	ExcludedBy         string       // the build constraint that excluded a file
	DependencyClass    string       // the section that declares a dependency
	Replacement        string       // the right-hand side of a module directive
	RemovesLastUseOf   string       // the dependency whose last use a deletion removes
}

// Finding is one row of a report: the Contract's finding object, field for field,
// so a reporter marshals it and nothing else.
//
// An emitter sets Code, Position, Symbol, Relation, TestOnly, Message and Details,
// and may set Symbol.Kind where the subject is a part of a declaration rather than
// the declaration itself. The framework sets everything else.
//
//nolint:govet // fieldalignment: the field order is the Contract's field order, which a reporter writes
type Finding struct {
	Code            string
	Kind            string
	Language        string
	Position        Position
	Symbol          Subject
	Class           Class
	Confidence      Class
	Relation        graph.Relation
	TestOnly        bool
	Generated       bool
	Component       Component
	RetainedBy      []string
	Configurations  []string
	ConsumersLoaded []string
	Fixability      string
	Severity        config.Severity
	Message         string
	Details         Details

	// id is the declaration of the inventory the finding is about. It is the key
	// the framework completes a finding by, because a report's own symbol
	// reference is not one: a blank declaration shares the reference of its
	// container. An emitter that leaves it unset is resolved by reference.
	id graph.SymbolID
}

// Consumers is what the run knows about the target's consumers: the ones the scope
// declared, the ones that loaded, and whether the configuration declares the set
// complete.
type Consumers struct {
	Declared []string
	Loaded   []string
	Complete bool
}

// Configured is one build configuration's load, with the declarations enumerated
// from it and the resolver over them, for a kind that reads syntax or types.
type Configured struct {
	Result  *load.Result
	Resolve *graph.Resolver
	Symbols []graph.Symbol
}

// Input is what every kind reads. A findings pass runs on one goroutine.
type Input struct {
	// Config is the resolved configuration, which decides which kinds report and
	// how loudly.
	Config *config.Config

	// Merged is the matrix inventory and Sweep is what the matrix sweep answered
	// over it: the candidates with the configurations each is dead in, the dead
	// components, the relations that hold each symbol live, and the exemptions
	// that held a symbol back.
	//
	// The sweep is the production one, because that is the only sweep under which
	// a symbol only a test references is dead and a test of dead code is
	// admitted. A declaration a test file writes is therefore a candidate by
	// construction and is the subject of the test-of-dead-code kind alone.
	Merged *graph.Merged
	Sweep  *graph.Result

	// Refs is the reference of every symbol of the inventory.
	Refs map[graph.SymbolID]string

	// Exempt is every exemption the sweep was given, which is not what the sweep
	// held back: an exemption on a declaration no relation found dead is here and
	// is absent from Sweep.Retained, because nothing held that declaration back.
	//
	// A kind whose subject is a LIVE declaration reads it, because an exemption
	// is a use no reference names: a class fires where a mechanism the analysis
	// cannot see reaches the declaration, and for a variable or a field that use
	// is a read of its value.
	Exempt []graph.Exemption

	// Generated reports whether one target-relative path is a generated file.
	Generated func(path string) bool

	// indexed is what a lookup over the inventory needs, built on first use.
	indexed *index

	// Matrix is the configuration identifiers in matrix order, and Per is one
	// entry per configuration in the same order.
	Matrix []string
	Per    []Configured

	// Consumers is what the run knows about the target's consumers.
	Consumers Consumers

	// Production is the reference mode the sweep ran under, which a kind that
	// counts reads needs and a swept graph does not carry. Under a production mode
	// a reference a test file made counts for nothing, so a read from a test file
	// is no read and a declaration written in production and read only from a test
	// is written and never read.
	//
	// Sweep is the production sweep, so this is true for the run the analyzer
	// makes; it is a field rather than a constant because the mode is the run's
	// and a kind must not assume it.
	Production bool
}

// Emitter is one kind's rule: it returns the findings of its own code.
type Emitter func(in *Input) ([]Finding, error)

// Result is what one findings pass produced: the findings in the canonical order,
// and how many the configured minimum confidence excluded.
type Result struct {
	Findings                  []Finding
	OmittedBelowMinConfidence int
}

// words is the reader's word for each kind of declaration, which a message spells
// out where the vocabulary hyphenates it.
var words = map[graph.SymbolKind]string{
	graph.KindFunc:            "function",
	graph.KindMethod:          "method",
	graph.KindType:            "type",
	graph.KindInterface:       "interface",
	graph.KindInterfaceMethod: "interface method",
	graph.KindField:           "field",
	graph.KindConst:           "constant",
	graph.KindVar:             "variable",
	graph.KindTypeParam:       "type parameter",
}

// symbolKinds maps a declaration's kind onto the Contract's subject vocabulary. A
// package is absent because the vocabulary has no word for one and nothing reports
// a package as a subject.
var symbolKinds = map[graph.SymbolKind]string{
	graph.KindFunc:            "function",
	graph.KindMethod:          "method",
	graph.KindType:            "type",
	graph.KindInterface:       "interface",
	graph.KindInterfaceMethod: "interface-method",
	graph.KindField:           "field",
	graph.KindConst:           "constant",
	graph.KindVar:             "variable",
	graph.KindTypeParam:       "type-parameter",
	graph.KindFile:            "file",
}

// kindsOfCatalog is the vocabulary the framework runs the emitters in the order of.
// It is a variable so that a test can put a kind with a ceiling below certain in
// front of the framework: every shipped kind declares certain, so the arm that caps
// a finding's confidence is otherwise unreachable.
var kindsOfCatalog = catalog.Kinds

// Compute runs every kind the resolved severity does not disable and returns the
// findings of the whole run.
//
// The emitters run in the vocabulary's order, so a run's own work order is the
// Contract's rather than a map's, and a code this analyzer has no emitter for
// reports nothing. Each finding an emitter returns is checked against what the
// Contract can carry and then completed; a refusal fails the pass, because each
// one is a defect in an emitter rather than something a report could say.
func Compute(in *Input, emitters map[string]Emitter) (Result, error) {
	if in == nil || in.Config == nil {
		return Result{}, fmt.Errorf("%w: no resolved configuration", ErrInput)
	}

	if err := checkTable(emitters); err != nil {
		return Result{}, err
	}
	reported := make(map[graph.SymbolID]string)
	var findings []Finding
	for _, row := range kindsOfCatalog() {
		emit, runs := in.emitterOf(&row, emitters)
		if !runs {
			continue
		}
		produced, err := in.runKind(emit, &row, reported)
		if err != nil {
			return Result{}, err
		}
		findings = append(findings, produced...)
	}

	kept, omitted := in.abovePar(findings)
	slices.SortStableFunc(kept, compare)
	return Result{Findings: kept, OmittedBelowMinConfidence: omitted}, nil
}

// emitterOf is the emitter one kind of the vocabulary runs in this pass, and false
// where the kind contributes nothing: a kind of another language, a kind the
// resolved severity allows, and a code this analyzer has no emitter for.
func (in *Input) emitterOf(row *catalog.Row, emitters map[string]Emitter) (Emitter, bool) {
	if !slices.Contains(row.Languages, language) {
		return nil, false
	}
	if in.Config.EffectiveSeverity(row.Code, in.consumersLoaded()) == config.Allow {
		return nil, false
	}
	emit, carried := emitters[row.Code]
	return emit, carried
}

// runKind runs one kind's emitter and completes every finding it returned, in the
// order the emitter returned them.
func (in *Input) runKind(emit Emitter, row *catalog.Row, reported map[graph.SymbolID]string) ([]Finding, error) {
	produced, err := emit(in)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrEmitter, row.Code, err)
	}
	completed := make([]Finding, 0, len(produced))
	for i := range produced {
		found := produced[i]
		if err := in.resolve(&found, row.Code, reported); err != nil {
			return nil, err
		}
		in.complete(&found, row)
		reported[found.id] = found.Code
		completed = append(completed, found)
	}
	return completed, nil
}

// checkTable refuses a table registering an emitter under a code the vocabulary
// does not hold, which is a retired or misspelled code whose emitter would
// otherwise run as nothing and report silently.
func checkTable(emitters map[string]Emitter) error {
	for _, code := range slices.Sorted(maps.Keys(emitters)) {
		if _, live := catalog.Kind(code); !live {
			return fmt.Errorf("%w: an emitter is registered under %s, which names no live kind",
				ErrEmitter, code)
		}
	}
	return nil
}

// resolve identifies the declaration one finding is about and refuses what the
// Contract cannot carry: a code that is not the emitter's own, a message that is
// not one sentence a consumer can extend, a finding about no declaration of the
// inventory, a second finding about one declaration, and a finding about a symbol
// an exemption retained.
func (in *Input) resolve(found *Finding, code string, reported map[graph.SymbolID]string) error {
	if found.Code != code {
		return fmt.Errorf("%w: the %s emitter returned a %s finding", ErrEmitter, code, found.Code)
	}
	if err := checkMessage(found); err != nil {
		return err
	}
	if found.id == "" {
		found.id = in.index().byRef[found.Symbol.Ref]
	}
	if found.id == "" {
		return fmt.Errorf("%w: %s names no declaration of the inventory: %q",
			ErrEmitter, found.Code, found.Symbol.Ref)
	}
	if first, twice := reported[found.id]; twice {
		return fmt.Errorf("%w: %s reports %s, which %s already reports",
			ErrEmitter, found.Code, found.Symbol.Ref, first)
	}
	if in.index().retained[found.id] {
		return fmt.Errorf("%w: %s reports %s, which an exemption retained",
			ErrEmitter, found.Code, found.Symbol.Ref)
	}
	return nil
}

// checkMessage refuses a message a text line and a SARIF result cannot carry: an
// empty one, one holding a line break, and one ending in a full stop, which is
// what lets a consumer append a clause to the same sentence.
func checkMessage(found *Finding) error {
	switch {
	case found.Message == "":
		return fmt.Errorf("%w: %s reports %s with no message",
			ErrEmitter, found.Code, found.Symbol.Ref)
	case strings.ContainsAny(found.Message, "\r\n"):
		return fmt.Errorf("%w: %s holds a line break in its message: %q",
			ErrEmitter, found.Code, found.Message)
	case strings.HasSuffix(found.Message, "."):
		return fmt.Errorf("%w: %s ends its message in a full stop: %q",
			ErrEmitter, found.Code, found.Message)
	}
	return nil
}

// complete fills everything the Contract requires of a finding whatever its kind,
// so no emitter decides any of it.
//
// The class comes from the run's consumer knowledge and the subject's visibility,
// the confidence is that class capped by the kind's own ceiling, and the severity
// is the resolved configuration's; a finding in a generated file carries no
// fixability, because nothing mechanical acts on a file a generator rewrites.
func (in *Input) complete(found *Finding, row *catalog.Row) {
	held := in.index()
	found.Kind = row.Name
	found.Language = language
	found.Class = in.ClassOf(found.id)
	found.Confidence = found.Class.lower(Class(row.MaxClass))
	found.Severity = in.Config.EffectiveSeverity(row.Code, in.consumersLoaded())
	found.Fixability = row.Fixability
	found.RetainedBy = []string{}
	found.ConsumersLoaded = slices.Clone(in.Consumers.Loaded)
	if found.ConsumersLoaded == nil {
		found.ConsumersLoaded = []string{}
	}
	found.Configurations = in.configurationsOf(found.id)
	found.Generated = in.generated(found.Position.Path)
	if found.Generated {
		found.Fixability = generatedFixability
	}
	if found.Symbol.Kind == "" {
		found.Symbol.Kind = symbolKinds[held.kindOf(found.id)]
	}
	found.Component = held.componentOf(found.id)
}

// abovePar drops every finding the configured minimum confidence excludes and
// returns how many it dropped, which is what the report accounts for.
func (in *Input) abovePar(findings []Finding) (kept []Finding, omitted int) {
	least := Class(in.Config.Analysis.MinConfidence)
	if least.rank() == 0 {
		return findings, 0
	}
	kept = make([]Finding, 0, len(findings))
	for i := range findings {
		if findings[i].Confidence.rank() < least.rank() {
			omitted++
			continue
		}
		kept = append(kept, findings[i])
	}
	return kept, omitted
}

// compare orders two findings by the canonical key: the path, the line, the
// column, the code and the symbol reference. The analyzer name is the key's last
// component and is one analyzer's own in its own report, so it decides nothing
// here.
//
//nolint:gocritic // hugeParam: the standard library's sort takes the element type by value
func compare(a, b Finding) int {
	return cmp.Or(
		cmp.Compare(a.Position.Path, b.Position.Path),
		cmp.Compare(a.Position.Line, b.Position.Line),
		cmp.Compare(a.Position.Column, b.Position.Column),
		cmp.Compare(a.Code, b.Code),
		cmp.Compare(a.Symbol.Ref, b.Symbol.Ref),
	)
}

// generated reports whether one path is a generated file, and false where the run
// carries no rule for it.
func (in *Input) generated(path string) bool {
	if in.Generated == nil {
		return false
	}
	return in.Generated(path)
}

// configurationsOf names the build configurations a finding about one declaration
// holds under: the ones the candidate is dead in, and the ones the declaration
// exists in for a finding about a live symbol.
func (in *Input) configurationsOf(id graph.SymbolID) []string {
	held := in.index()
	set := graph.ConfigSet(0)
	if candidate := held.candidates[id]; candidate != nil {
		set = candidate.Configs
	}
	if set == 0 {
		if symbol := held.symbols[id]; symbol != nil {
			set = symbol.Configs
		}
	}
	named := make([]string, 0, len(in.Matrix))
	for _, at := range set.Indexes() {
		if at < len(in.Matrix) {
			named = append(named, in.Matrix[at])
		}
	}
	return named
}

// index is what a lookup over one run's inventory needs, keyed by declaration.
type index struct {
	symbols    map[graph.SymbolID]*graph.Symbol
	candidates map[graph.SymbolID]*graph.Candidate
	components map[graph.SymbolID]*graph.Component
	retained   map[graph.SymbolID]bool
	byRef      map[string]graph.SymbolID
	mains      map[string]bool

	// deprecated is every declaration carrying a deprecation marker, and
	// enumerated every constant of an enumerated type with the type's name. Both
	// are read from the syntax on first use, because the kinds that need them are
	// few and the walk is the whole tree's.
	deprecated map[graph.SymbolID]bool
	enumerated map[graph.SymbolID]string

	minted int
}

// index builds the lookup on first use and returns it.
func (in *Input) index() *index {
	if in.indexed != nil {
		return in.indexed
	}
	held := &index{
		symbols:    make(map[graph.SymbolID]*graph.Symbol),
		candidates: make(map[graph.SymbolID]*graph.Candidate),
		components: make(map[graph.SymbolID]*graph.Component),
		retained:   make(map[graph.SymbolID]bool),
		byRef:      make(map[string]graph.SymbolID),
		mains:      make(map[string]bool),
	}
	held.holdInventory(in.Merged)
	held.holdSweep(in.Sweep)
	held.holdPackages(in.Per)
	in.indexed = held
	return held
}

// holdInventory keys every declaration of the matrix inventory by its identifier,
// and by its reference for a finding that names no identifier. Two declarations
// share one reference where one is blank, and the first of them answers for it.
func (x *index) holdInventory(merged *graph.Merged) {
	if merged == nil {
		return
	}
	for i := range merged.Symbols {
		symbol := &merged.Symbols[i]
		x.symbols[symbol.ID] = symbol
		if _, collides := x.byRef[symbol.Ref]; !collides {
			x.byRef[symbol.Ref] = symbol.ID
		}
	}
}

// holdSweep keys the sweep's answers by declaration: the candidates, the dead
// component each member belongs to, the number past the components it minted, and
// the symbols an exemption held back.
func (x *index) holdSweep(swept *graph.Result) {
	if swept == nil {
		return
	}
	for i := range swept.Candidates {
		x.candidates[swept.Candidates[i].ID] = &swept.Candidates[i]
	}
	for i := range swept.Components {
		component := &swept.Components[i]
		x.minted = max(x.minted, component.Index+1)
		for _, member := range component.Members {
			x.components[member] = component
		}
	}
	for _, exemption := range swept.Retained {
		x.retained[exemption.ID] = true
	}
}

// holdPackages records which import paths name a main package, which nothing
// outside the module can import.
func (x *index) holdPackages(per []Configured) {
	for _, one := range per {
		if one.Result == nil {
			continue
		}
		for _, p := range one.Result.Packages {
			if p.Name == mainPackageName {
				x.mains[p.PkgPath] = true
			}
		}
	}
}

// symbol is the declaration of the inventory one identifier names, or nil where the
// inventory holds none.
func (in *Input) symbol(id graph.SymbolID) *graph.Symbol {
	return in.index().symbols[id]
}

// candidateOf is the sweep's candidate for one declaration, or nil where the sweep
// judged the declaration live.
func (in *Input) candidateOf(id graph.SymbolID) *graph.Candidate {
	return in.index().candidates[id]
}

// finding starts a finding about one declaration of the inventory: its code, its
// subject, its position and the message the kind gives it. The framework fills
// everything else, and it answers false for an identifier the inventory does not
// hold, so a kind that reports one reports nothing rather than a finding with no
// subject.
func (in *Input) finding(id graph.SymbolID, code, message string) (Finding, bool) {
	symbol := in.symbol(id)
	if symbol == nil {
		return Finding{}, false
	}
	position := Position{
		Path:    symbol.Pos.Filename,
		Line:    symbol.Pos.Line,
		Column:  symbol.Pos.Column,
		EndLine: max(symbol.Pos.Line, symbol.EndLine),
	}
	return Finding{
		Code:     code,
		Position: position,
		Symbol: Subject{
			Ref:       symbol.Ref,
			Name:      symbol.Name,
			SizeLines: position.EndLine - position.Line + 1,
		},
		Message: message,
		id:      id,
	}, true
}

// word is the reader's word for the kind of one declaration, and the vocabulary's
// own spelling for a kind no message has a word for.
func (in *Input) word(id graph.SymbolID) string {
	kind := in.index().kindOf(id)
	if word, held := words[kind]; held {
		return word
	}
	return kind.String()
}

// kindOf is the kind of one declaration of the inventory.
func (x *index) kindOf(id graph.SymbolID) graph.SymbolKind {
	if symbol := x.symbols[id]; symbol != nil {
		return symbol.Kind
	}
	return graph.KindPackage
}

// componentOf is the dead component one finding's subject belongs to.
//
// A finding about a live symbol belongs to no dead component, which the narrowing
// kinds report and the Contract has no spelling for, so the framework mints a
// component of that subject alone: one symbol falls with it and no line is
// deleted, because narrowing a declaration deletes nothing.
func (x *index) componentOf(id graph.SymbolID) Component {
	component := x.components[id]
	if component == nil {
		x.minted++
		return Component{
			ID:          componentID(x.minted),
			Root:        true,
			SymbolCount: 1,
		}
	}
	return Component{
		ID:             componentID(component.Index + 1),
		Root:           slices.Contains(component.Roots, id),
		SymbolCount:    len(component.Falls),
		DeletableLines: component.DeletableLines,
	}
}

// componentID is the identifier this analyzer mints for one component: its own
// name, a solidus, and the component's number.
func componentID(number int) string {
	return analyzerName + "/c-" + strconv.Itoa(number)
}
