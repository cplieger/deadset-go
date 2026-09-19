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
	"github.com/cplieger/deadset-go/internal/deps"
	"github.com/cplieger/deadset-go/internal/edges"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/matrix"
	"github.com/cplieger/deadset-go/internal/suppress"
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

// Entry is the suppression record a self-check finding reports, in the shape an
// entry of the ignore file and a row of the baseline share. A member is empty
// exactly where the record lacks it, which is what the reason-free and the
// unscoped kinds report.
type Entry struct {
	Code   string // the code the record names, which is not the finding's own
	Symbol string // the reference the record names, empty where it names none
	Path   string // the file the record names, empty where it names none
	Reason string // the reason the record carries, empty where it carries none
}

// Details carries the per-kind members of the Contract's details object. A kind
// sets only its own, and a reporter omits the members the kind left unset.
//
// The field order is the Contract's field order, which is the order a reporter
// writes them in. A kind whose subject is a suppression record sets Entry, which is
// a pointer so that a record carrying nothing but a code is told from a kind that
// reports no record at all.
type Details struct {
	NarrowerVisibility string       // the visibility the references support
	Implementations    []Positioned // the concrete implementations of an interface
	WritePositions     []Position   // every position a subject is written at
	ExcludedBy         string       // the build constraint that excluded a file
	DependencyClass    string       // the section that declares a dependency
	Replacement        string       // the right-hand side of a module directive
	Mechanism          string       // where the suppression a finding reports lives
	Entry              *Entry       // the suppression record a finding reports
	Overlap            []string     // the external rules that report the same kind
	RemovesLastUseOf   []string     // the dependencies whose last use a deletion removes
}

// Finding is one row of a report: the Contract's finding object, field for field,
// so a reporter marshals it and nothing else.
//
// An emitter sets Code, Position, Symbol, TestOnly, Message and Details, and may
// set Symbol.Kind where the subject is a part of a declaration rather than the
// declaration itself. The framework sets everything else, the liveness relation
// included.
//
//nolint:govet // fieldalignment: the field order is the Contract's field order, which a reporter writes
type Finding struct {
	Code       string
	Kind       string
	Language   string
	Position   Position
	Symbol     Subject
	Class      Class
	Confidence Class
	Relation   graph.Relation

	// Live reports that no liveness relation decided this finding, so a report
	// carries none. Two subjects answer that way: a declaration the sweep judged
	// live, which the narrowing kinds, the unused-satisfaction-assertion kind and
	// the write-only kind all claim something about, and a subject that is no
	// declaration of the inventory at all, which a relation over declarations has
	// nothing to say about. Relation then holds the relation a reader of a document
	// that still requires the member reads, and a reporter writing a document that
	// does not omits the member.
	Live bool

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

	// id is the declaration of the inventory the finding is about, and is empty on
	// a finding about anything else: a part of a declaration and a row of a
	// document are no declaration of the inventory, so neither carries one. It is
	// what the framework reads the sweep's answers about a declaration by, because
	// a report's own symbol reference is not one: a blank declaration shares the
	// reference of its container.
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

	// Marks is every suppression record the run read, bound or not, and Refusals
	// is what the grammar refused with a finding of its own rather than with an
	// exit. The self-check kinds read both: a record the sweep did not hold back
	// is stale, a record whose code the configuration disables is dormant and
	// reported by nothing, and a refusal is reported as the suppression was
	// written.
	Marks    []suppress.Record
	Refusals []suppress.Refusal

	// Deps is the target's module file as the toolchain reports it, which is what
	// the dependency kinds read, and nil where the run did not read it.
	Deps *deps.File

	// Derived is the matrix the derivation answered, and nil where the
	// configuration named the build configurations itself.
	//
	// No kind reads it: the kind that claims something about every configuration
	// of a target gates on the configuration listing them and declaring the set
	// complete, and reads the files each configuration ignored. What the run
	// derived is what the report's declared gaps name, so the field carries the
	// derivation to the envelope and no further.
	Derived *matrix.Derived

	// Unmatched is every configured root the roots pass matched nothing with,
	// which is a configuration naming something that no longer exists.
	Unmatched []graph.Unmatched

	// Edges is the declared cross-language edges of the target, nil where the
	// target carries no edges document.
	Edges *edges.Document

	// indexed is what a lookup over the inventory needs, built on first use.
	indexed *index

	// Matrix is the configuration identifiers in matrix order, and Per is one
	// entry per configuration in the same order.
	Matrix []string
	Per    []Configured

	// Consumers is what the run knows about the target's consumers.
	Consumers Consumers

	// Mode is the run's reference mode, the one value the composition root decided
	// for every stage. A kind that counts reads needs it and a swept graph does not
	// carry it: under a production mode a reference a test file made counts for
	// nothing, so a read from a test file is no read and a declaration written in
	// production and read only from a test is written and never read.
	//
	// Sweep is the production sweep, so the mode of the run this analyzer makes is
	// the production one; it is a field rather than a constant because the mode is
	// the run's and a kind must not assume it.
	Mode graph.Mode
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
	graph.KindFile:            fileSubject,
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
	reported := make(map[string]string)
	var findings []Finding
	published := kindsOfCatalog()
	for i := range published {
		row := &published[i]
		emit, runs := in.emitterOf(row, emitters)
		if !runs {
			continue
		}
		produced, err := in.runKind(emit, row, reported)
		if err != nil {
			return Result{}, err
		}
		findings = append(findings, produced...)
	}

	kept, omitted := in.abovePar(findings)
	slices.SortStableFunc(kept, Compare)
	return Result{Findings: kept, OmittedBelowMinConfidence: omitted}, nil
}

// emitterOf is the emitter one kind of the vocabulary runs in this pass, and false
// where the kind contributes nothing: a kind of another language, a kind the
// resolved severity allows, and a code this analyzer has no emitter for.
func (in *Input) emitterOf(row *catalog.Row, emitters map[string]Emitter) (Emitter, bool) {
	if !slices.Contains(row.Languages, language) {
		return nil, false
	}
	if in.Config.EffectiveSeverity(row.Code, in.consumersAllLoaded()) == config.Allow {
		return nil, false
	}
	emit, carried := emitters[row.Code]
	return emit, carried
}

// runKind runs one kind's emitter and completes every finding it returned, in the
// order the emitter returned them.
func (in *Input) runKind(emit Emitter, row *catalog.Row, reported map[string]string) ([]Finding, error) {
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

// shape is what one finding carries, which the Contract decides by the kind of the
// subject: a declaration of the inventory, a part of a declaration, or a row of a
// document.
type shape uint8

const (
	// shapeDeclaration is a symbol of the inventory, which the sweep judged, so the
	// finding carries the liveness relation that decided it, the reachability class
	// its visibility gives it, the dead component it falls with and the
	// configurations it holds under.
	shapeDeclaration shape = iota

	// shapePart is a part of a declaration: a parameter, a receiver, a result, a
	// statement, a case or a store. The declaration stays and the part is the
	// subject, so no relation over declarations answers for it.
	shapePart

	// shapeRow is a row of a document: a requirement or a directive of the module
	// file, a file no configuration compiled, a suppression record, a configured
	// root. The analysis enumerates no declaration for it at all.
	shapeRow
)

// shapes is the shape of every subject kind that is not a declaration, which is the
// list the Contract publishes as the kinds a finding carries no liveness relation
// on. Every kind absent from it is a declaration, so the table holds the exception
// rather than the rule and a test pins its keys equal to the Contract's list.
var shapes = map[string]shape{
	caseSubject:        shapePart,
	parameterSubject:   shapePart,
	receiverSubject:    shapePart,
	resultSubject:      shapePart,
	statementSubject:   shapePart,
	storeSubject:       shapePart,
	dependencySubject:  shapeRow,
	directiveSubject:   shapeRow,
	fileSubject:        shapeRow,
	rootSubject:        shapeRow,
	suppressionSubject: shapeRow,
}

// shapeOf is the shape of a finding about one kind of subject.
func shapeOf(subjectKind string) shape {
	return shapes[subjectKind]
}

// LivenessAbsent reports whether one finding carries no liveness relation, which the
// Contract decides two ways and this answers both: the subject is a part of a
// declaration or a row of a document, which no relation over declarations answers
// for, and the subject is a declaration the sweep judged live, which every kind
// claiming something other than deadness reports.
//
// It is exported because the analyzer and the Contract answer this one question, so
// a test compares this predicate against the condition the finding schema states
// rather than against a copy of it.
func LivenessAbsent(found *Finding) bool {
	return shapeOf(found.Symbol.Kind) != shapeDeclaration || found.Live
}

// key is one finding's identity: the position of the thing it names, rendered the way
// the identifier of a declaration is, so the key of a finding about a declaration is
// that identifier and the key of a finding about a part of one is the part's own
// token. Two findings of one key is a defect, which is what makes the rendered
// position the whole identity of everything the analysis enumerates: one identifier,
// one parameter name, one statement's first token.
//
// A row of a document carries the record it reports as well, because a position does
// not identify a row the way it identifies a declaration, and two documents say so. A
// suppression record breaks as many of the grammar's rules as it breaks and a
// directive names as many codes as it names, so an entry lacking both its reason and
// its path is one finding per rule at one position and a reasonless directive naming
// several codes is one finding per code at one position. The repository configuration
// writes its roots as an array whose members carry no position at all, so every
// configured root that matches nothing renders at the document's first position and
// the string it names is what tells one from another. Each of the three components is
// load-bearing and the key is no longer than those two documents make it: the
// position, the rule the row breaks, and the record itself, which is the reference it
// names and the code it names.
func key(found *Finding) string {
	at := found.Position.Path + ":" + strconv.Itoa(found.Position.Line) +
		":" + strconv.Itoa(found.Position.Column)
	if shapeOf(found.Symbol.Kind) != shapeRow {
		return at
	}
	record := at + " " + found.Code + " " + found.Symbol.Ref
	if found.Details.Entry != nil {
		record += " " + found.Details.Entry.Code
	}
	return record
}

// resolve identifies one finding and refuses what the Contract cannot carry: a code
// that is not the emitter's own, a message that is not one sentence a consumer can
// extend, a second finding of one identity, and a subject the inventory cannot answer
// for.
//
// A finding's identity is its key, which is the position of the thing it names, so two
// findings of one identity is two kinds disagreeing about which of them reports the
// subject. The precedence each kind's own rule states is what settles that, so the
// pass fails rather than publishing both.
//
// The key is one rule for every shape; what the inventory must be able to answer for a
// finding is the shape's, which admit decides.
func (in *Input) resolve(found *Finding, code string, reported map[string]string) error {
	if found.Code != code {
		return fmt.Errorf("%w: the %s emitter returned a %s finding", ErrEmitter, code, found.Code)
	}
	if err := checkMessage(found); err != nil {
		return err
	}
	at := key(found)
	if first, twice := reported[at]; twice {
		return fmt.Errorf("%w: %s reports %s at %s, which %s already reports",
			ErrEmitter, found.Code, found.Symbol.Ref, at, first)
	}
	if err := in.admit(found); err != nil {
		return err
	}
	reported[at] = found.Code
	return nil
}

// admit refuses a finding whose subject the inventory cannot answer for, which the
// shape of the subject decides: a declaration the inventory does not hold, which is an
// emitter naming a subject the analysis never enumerated; a declaration an exemption
// retained, which is a symbol no kind reports at all; a part of a declaration the
// inventory does not hold, since a part names that declaration by reference; and a row
// naming no record, which is a row a maintainer cannot find the subject of, because a
// row's position names the document rather than the record.
//
// A part does not answer for the retained rule: the subject is the part, the
// declaration it belongs to stays whatever held it live, and an exemption over that
// declaration is no claim about the part.
func (in *Input) admit(found *Finding) error {
	switch shapeOf(found.Symbol.Kind) {
	case shapeRow:
		if found.Symbol.Ref == "" {
			return fmt.Errorf("%w: %s reports a %s and names no reference",
				ErrEmitter, found.Code, found.Symbol.Kind)
		}
	case shapePart:
		if in.index().byRef[found.Symbol.Ref] == "" {
			return fmt.Errorf("%w: %s reports a %s of %q, which names no declaration of the inventory",
				ErrEmitter, found.Code, found.Symbol.Kind, found.Symbol.Ref)
		}
	default:
		if found.id == "" {
			return fmt.Errorf("%w: %s names no declaration of the inventory: %q",
				ErrEmitter, found.Code, found.Symbol.Ref)
		}
		if in.index().retained[found.id] {
			return fmt.Errorf("%w: %s reports %s, which an exemption retained",
				ErrEmitter, found.Code, found.Symbol.Ref)
		}
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
// What a finding carries is the shape of its subject, and the subject's kind is what
// the Contract decides that by, so the shape is read from the table and nothing else
// asks what a kind reports. A declaration carries the liveness relation that decided
// it, which is the candidate's and is absent where the sweep judged the declaration
// live; the reachability class the run's consumer knowledge and the declaration's
// visibility give it; and the dead component it falls with. A part of a declaration
// and a row of a document carry none of those and answer the degenerate value at
// each step: no relation, because none decided them; certain, because a subject with
// no visibility of its own has no question about its callers; and a component of
// their own, because nothing falls with them.
//
// The confidence is the class capped by the kind's own ceiling, the severity is the
// resolved configuration's, and the overlap is the vocabulary's own list for the
// kind, so a message names no other tool. A finding in a generated file carries no
// fixability, because nothing mechanical acts on a file a generator rewrites.
func (in *Input) complete(found *Finding, row *catalog.Row) {
	held := in.index()
	found.Kind = row.Name
	found.Language = language
	if found.Symbol.Kind == "" {
		found.Symbol.Kind = symbolKinds[held.kindOf(found.id)]
	}
	if shapeOf(found.Symbol.Kind) == shapeDeclaration {
		found.relation(held.candidates[found.id])
		found.Class = in.ClassOf(found.id)
		found.Component = held.componentOf(found.id)
	} else {
		found.relation(nil)
		found.Class = Certain
		found.Component = held.componentOf("")
	}
	found.Confidence = found.Class.lower(Class(row.MaxClass))
	found.Severity = in.Config.EffectiveSeverity(row.Code, in.consumersAllLoaded())
	found.Fixability = row.Fixability
	found.RetainedBy = []string{}
	found.ConsumersLoaded = slices.Clone(in.Consumers.Loaded)
	if found.ConsumersLoaded == nil {
		found.ConsumersLoaded = []string{}
	}
	found.Configurations = in.configurationsOf(found)
	found.Details.Overlap = slices.Clone(row.Overlap)
	found.Generated = in.generated(found.Position.Path)
	if found.Generated {
		found.Fixability = generatedFixability
	}
}

// relation records the liveness relation one finding carries: the candidate's
// where the sweep judged the subject dead, and none where it judged it live.
//
// The relation Live leaves in place is the reference-counting one, which is what a
// consumer of a document that requires the member reads about a subject no relation
// decided.
func (f *Finding) relation(candidate *graph.Candidate) {
	if candidate == nil {
		f.Live = true
		f.Relation = graph.ReferenceCounting
		return
	}
	f.Live = false
	f.Relation = candidate.Relation
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

// Compare orders two findings by the canonical key: the path, the line, the
// column, the code and the symbol reference. The analyzer name is the key's last
// component and is one analyzer's own in its own report, so it decides nothing
// here.
//
// It is exported because the key is one fact with one owner: a reporter that orders
// findings calls this rather than comparing the five components again.
//
//nolint:gocritic // hugeParam: the standard library's sort takes the element type by value
func Compare(a, b Finding) int {
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

// configurationsOf names the build configurations one finding holds under, which the
// shape of its subject decides.
//
// A declaration holds under the configurations its candidate is dead in, and under
// the ones it exists in where the sweep judged it live. A part holds wherever the
// declaration it belongs to exists, because a configuration that compiles no part of
// a declaration compiles none of it, and the part names that declaration by
// reference. A row holds under every configuration of the matrix: the run reads a
// document once for the whole matrix, and a requirement, a directive or a file no
// configuration compiles is reported only where every configuration agrees.
func (in *Input) configurationsOf(found *Finding) []string {
	held := in.index()
	var id graph.SymbolID
	switch shapeOf(found.Symbol.Kind) {
	case shapeRow:
		return append(make([]string, 0, len(in.Matrix)), in.Matrix...)
	case shapePart:
		id = held.byRef[found.Symbol.Ref]
	default:
		id = found.id
	}
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

// finding starts a finding about one declaration of the inventory, named by the
// identifier the analysis enumerated it under: its code, its subject, its position
// and the message the kind gives it. The framework fills everything else, and it
// answers false for an identifier the inventory does not hold, so a kind that
// reports one reports nothing rather than a finding with no subject.
//
// A finding about a part of a declaration or about a row of a document is started by
// findingAt instead, which names its subject by position: the inventory holds no
// declaration for either, and the identifier is what the sweep's answers about a
// declaration are read by.
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

// findingAt starts a finding about something the inventory holds no declaration for:
// a part of a declaration, whose subject names that declaration by reference, and a
// row of a document, whose subject names the record it reports. The position is the
// subject's own, which is what identifies the finding, and the framework fills
// everything else.
func findingAt(code string, subject Subject, at Position, message string) Finding {
	return Finding{
		Code:     code,
		Position: at,
		Symbol:   subject,
		Message:  message,
	}
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
// A subject that belongs to none falls with nothing, which the Contract has no
// spelling for, so the framework mints a component of that subject alone: one symbol
// falls with it and no line is deleted. Three subjects answer that way: a declaration
// the sweep judged live, which the narrowing kinds report; a part of a declaration,
// which is deleted without the declaration; and a row of a document, which the
// analysis enumerates no declaration for and names the empty identifier.
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
