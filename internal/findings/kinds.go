// Package findings turns one run's liveness answers into the findings a report
// carries.
//
// One emitter per issue kind returns a draft about each subject of its own kind and
// says why in one sentence; the framework completes every draft with what the
// Contract requires of every finding, refuses one the Contract cannot carry, and
// applies the one confidence stage and the dials. A kind decides which code a
// subject is reported under and never re-decides liveness: the candidate set is the
// sweep's. A kind reads no severity and sets no class.
package findings

import (
	"cmp"
	"errors"
	"fmt"
	"go/token"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/deps"
	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/report"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The constants of every finding this analyzer produces: the language it analyzes,
// and the name it mints every identifier of its own under.
const (
	language     = vocab.Go
	analyzerName = "deadset-go"
)

var (
	// errInput reports a findings pass with nothing to read: no input, or no
	// resolved configuration to read the severity map from.
	errInput = errors.New("findings: incomplete input")

	// errEmitter reports a finding an emitter returned that the Contract cannot
	// carry: a message that is not one sentence, a code that is not the
	// emitter's own, a second finding about one declaration, or a finding about
	// a symbol an exemption retained. Each is a defect in an emitter rather than
	// a row of a report, so the pass fails instead of publishing it.
	errEmitter = errors.New("findings: emitter defect")
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

// positionAt renders the position of one identifier, which begins and ends on the
// one line.
func positionAt(pos token.Position) Position {
	return Position{Path: pos.Filename, Line: pos.Line, Column: pos.Column, EndLine: pos.Line}
}

// Subject is what a finding is about: the reference that survives an edit above
// it, the vocabulary's word for the kind of subject, the name a text line renders,
// and the number of source lines it spans.
//
// Exported is the subject's visibility where a document read back names one, and
// nil where it names none; the findings pass leaves it nil.
type Subject struct {
	Exported  *bool
	Ref       string
	Kind      string
	Name      string
	SizeLines int
}

// positioned is one symbol a finding names beside its subject, with its position.
type positioned struct {
	Ref      string
	Name     string
	Position Position
}

// suppressionEntry is the suppression record a self-check finding reports, in the shape an
// entry of the ignore file and a row of the baseline share. A member is empty
// exactly where the record lacks it, which is what the reason-free and the
// unscoped kinds report.
type suppressionEntry struct {
	Code   vocab.Code // the code the record names, which is not the finding's own
	Symbol string     // the reference the record names, empty where it names none
	Path   string     // the file the record names, empty where it names none
	Reason string     // the reason the record carries, empty where it carries none
}

// Details carries the per-kind members of the Contract's details object. A kind
// sets only its own, and a reporter omits the members the kind left unset.
//
// The field order is the Contract's field order, which is the order a reporter
// writes them in. A kind whose subject is a suppression record sets Entry, which is
// a pointer so that a record carrying nothing but a code is told from a kind that
// reports no record at all.
type Details struct {
	NarrowerVisibility string            // the visibility the references support
	Implementations    []positioned      // the concrete implementations of an interface, empty and not nil where it has none
	WritePositions     []Position        // every position a subject is written at
	NameLiteral        *Position         // the literal spelling the subject's name, or a constant's type's, that lowered its class
	ExcludedBy         string            // the build constraint that excluded a file
	DependencyClass    string            // the section that declares a dependency
	Replacement        string            // the right-hand side of a module directive
	Mechanism          string            // where the suppression a finding reports lives
	Entry              *suppressionEntry // the suppression record a finding reports
	Overlap            []string          // the external rules that report the same kind
	RemovesLastUseOf   []string          // the dependencies whose last use a deletion removes
}

// draft is what an emitter returns about one subject: what its kind decides, and
// the facts of the subject [pass.complete] reads. An emitter sets Symbol.Kind only
// where the subject is a part of a declaration rather than the declaration itself.
type draft struct {
	Code     vocab.Code
	Message  string
	id       inventory.SymbolID // [Finding]'s
	decl     inventory.SymbolID // [Finding]'s
	record   string             // [Finding]'s
	Details  Details
	Symbol   Subject
	Position Position
	written  int // [Finding]'s
	TestOnly bool

	// unseen marks a subject a program the run does not load may name, whatever
	// consumers loaded, which caps its class at possible.
	unseen bool

	// testRead marks a write-only subject only test files read ([pass.capTestUsed]).
	testRead bool

	local bool // [Finding]'s
}

// Finding is one row of a report: the Contract's finding object, field for field,
// as [pass.complete] makes it from a draft.
//
//nolint:govet // fieldalignment: the field order is the Contract's field order
type Finding struct {
	Code       vocab.Code
	Kind       string
	Language   vocab.Language
	Position   Position
	Symbol     Subject
	Class      vocab.Class
	Confidence vocab.Class
	Relation   liveness.Relation

	// Live reports that no liveness relation decided this finding, so a report
	// carries none: a declaration the sweep judged live, a finding under a code of
	// a kind that holds its subject live, and a subject that is no declaration of the inventory at all.
	// Relation then holds the relation a reader of a document that still requires
	// the member reads, and a reporter writing a document that does not omits the
	// member.
	Live bool

	TestOnly        bool
	Generated       bool
	Component       report.Component
	RetainedBy      []string
	Configurations  []string
	ConsumersLoaded []string
	Fixability      vocab.Fixability
	Severity        vocab.Severity
	Message         string
	Details         Details

	// id is the declaration of the inventory the finding is about, and is empty on
	// a finding about anything else: a part of a declaration and a row of a
	// document are no declaration of the inventory, so neither carries one. It is
	// what the framework reads the sweep's answers about a declaration by, because
	// a report's own symbol reference is not one: a blank declaration shares the
	// reference of its container.
	id inventory.SymbolID

	// decl is the declaration of the inventory a part is of, the one the walk read the
	// part in, and is empty on any other finding. The part's reference does not name
	// it alone: every init function of a package shares one.
	decl inventory.SymbolID

	// written is the byte of decl's file a part's token is written at ([partKey]).
	written int

	// record is where a row's suppression record is written ([core.Site.Written]),
	// which tells apart two records a line directive renders at one site.
	record string

	// shared is the confidence the finding gives its component's cap and the dials:
	// its own, but for the possible [pass.capTestUsed] lowers it to alone.
	shared vocab.Class

	// local marks a finding about a declaration a function body makes, which the
	// inventory does not hold: the enclosing declaration's reference names it, as it
	// names a part, and reference counting inside the body decided it.
	local bool
}

// shape is the shape of one finding's subject, a local declaration being shaped
// as the part of the enclosing declaration its reference makes it.
func (f *Finding) shape() shape {
	if f.local {
		return shapePart
	}
	return shapeOf(f.Symbol.Kind)
}

// Declaration is the declaration of the inventory the finding is about, and empty
// on a finding about anything else.
func (f *Finding) Declaration() inventory.SymbolID { return f.id }

// DeclarationRef is the reference of the declaration a finding's subject is or is a
// part of. A parameter has a reference of its own, under its holder's.
func (f *Finding) DeclarationRef() string {
	if f.Symbol.Kind != parameterSubject {
		return f.Symbol.Ref
	}
	holder, _ := inventory.ParameterHolder(f.Symbol.Ref)
	return holder
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
	Resolve *resolve.Resolver
	Facts   *facts.Index
	Symbols []inventory.Symbol
}

// ModuleFile is the module file of one unit of the target.
type ModuleFile struct {
	Dir  string // the unit's root, absolute
	Path string // the module path the file declares
	File deps.File
}

// Input is what every kind reads. A findings pass runs on one goroutine.
type Input struct {
	// Config is the resolved configuration, which decides which kinds report and
	// how loudly.
	Config *config.Config

	// Merged is the matrix inventory and Sweep what the matrix sweep answered over
	// it. The sweep is the production one, the only one under which a symbol only a
	// test references is dead and a test of dead code is admitted, so a declaration
	// a test file writes is a candidate and the test-of-dead-code kind's alone.
	Merged *liveness.Merged
	Sweep  *liveness.Result

	// Unmarked is the sweep with every suppression record's mark withdrawn, which
	// a record on a declaration is decided against, and nil where no record marks.
	Unmarked *liveness.Result

	// Refs is the reference of every symbol of the inventory.
	Refs map[inventory.SymbolID]string

	// Exempt is every exemption the sweep was given, which is not what the sweep
	// held back: an exemption on a declaration no relation found dead is here and
	// is absent from Sweep.Retained, because nothing held that declaration back.
	//
	// A kind whose subject is a LIVE declaration reads it, because an exemption
	// is a use no reference names: a class fires where a mechanism the analysis
	// cannot see reaches the declaration, and for a variable or a field that use
	// is a read of its value.
	Exempt []exempt.Exemption

	// TestEvidence is the exemption evidence a test file carries, which a
	// production mode leaves out of Exempt. A conversion or a satisfaction a test
	// makes still fixes the signature it reaches, so the intra-function kinds read
	// it beside Exempt.
	TestEvidence []exempt.Exemption

	// Filled is every field the encoding-reflection class keeps only at the class
	// possible and nothing retains ([pass.capFilled]).
	Filled map[inventory.SymbolID]bool

	// Generated reports whether one target-relative path is a generated file.
	Generated func(path string) bool

	// TypeErrorSkips is every declaration a type error skipped, about which
	// nothing is reported.
	TypeErrorSkips []roots.TypeErrorSkip

	// Marks is every suppression record the run read, bound or not, in the
	// reading order the ledger decides them in, and Refusals is what the grammar
	// refused with a finding of its own rather than with an exit.
	Marks    []core.Record
	Refusals []core.Refusal

	// Root is the target root, which every position a kind reports is relative
	// to.
	Root string

	// Modules is every module file of the target as the toolchain reports it,
	// in unit order, which is what the dependency kinds read.
	Modules []ModuleFile

	// Unmatched is every configured root the roots pass matched nothing with,
	// which is a configuration naming something that no longer exists.
	Unmatched []roots.Unmatched

	// RootsDocument is the configuration document that declared the configured
	// roots, as a target-relative path, and is empty where no document of the
	// target did: the path a finding about a configured root carries. The
	// configuration resolution decides which document that is.
	RootsDocument string

	// Edges is the declared cross-language edges of the target, nil where the
	// target carries no edges document.
	Edges *edges.Document

	// Matrix is the configuration identifiers in matrix order, and Per is one
	// entry per configuration in the same order.
	Matrix []string
	Per    []Configured

	// Consumers is what the run knows about the target's consumers.
	Consumers Consumers

	// Mode is the run's reference mode, which a kind that counts reads needs and a
	// swept graph does not carry: under a production mode a read from a test file is
	// no read. It is the run's, not a constant, so no kind assumes it.
	Mode facts.Mode
}

// Emitter is one kind's rule: it returns the drafts of its own code.
type Emitter func(in *pass) ([]draft, error)

// Emitters is every kind this analyzer reports, mapped to its rule. A code absent
// from it reports nothing; a test holds it to the vocabulary's Go kinds.
var Emitters = map[vocab.Code]Emitter{
	vocab.UnusedExported:              UnusedExported,
	vocab.UnusedUnexported:            unusedUnexported,
	vocab.UnusedMember:                unusedMember,
	vocab.TestOnlyUse:                 testOnlyUse,
	vocab.TestOfDeadCode:              testOfDeadCode,
	vocab.DeprecatedUnused:            deprecatedAndUnused,
	vocab.UnnecessaryExport:           unnecessaryExport,
	vocab.UnnecessaryExposure:         unnecessaryExposure,
	vocab.UnreachableExport:           unreachableExport,
	vocab.FileNeverBuilt:              fileNeverBuilt,
	vocab.FileNeverImported:           fileNeverImported,
	vocab.UnusedInterface:             unusedInterface,
	vocab.UncalledInterfaceMethod:     uncalledInterfaceMethod,
	vocab.UnusedSatisfactionAssertion: unusedSatisfactionAssertion,
	vocab.WriteOnlySymbol:             writeOnlySymbol,
	vocab.UnusedEnumMember:            unusedEnumMember,
	vocab.UnusedTypeParameter:         unusedTypeParameter,
	vocab.UnusedDependency:            unusedDependency,
	vocab.UnusedModuleDirective:       unusedReplace,
	vocab.SuppressionWithoutReason:    suppressionsWithoutReason,
	vocab.UnscopedIgnoreEntry:         unscopedEntries,
	vocab.UnmatchedRoot:               unmatchedRoots,
	vocab.UnusedParameter:             unusedParameter,
	vocab.UnusedReceiver:              unusedReceiver,
	vocab.UnusedResult:                unusedResult,
	vocab.UnreachableStatement:        unreachableStatement,
	vocab.DeadStore:                   deadStore,
	vocab.UnreachableCase:             unreachableCase,
}

// Result is what one findings pass produced: the findings in the canonical order,
// the findings the configured minimum confidence alone withheld, the evaluation of
// every declared edge side this analyzer enumerated, the stale suppressions and the
// two suppression counts, which the ledger ([pass.decide]) answers.
type Result struct {
	// Members is every component's members by identifier, in the order a full
	// listing names them, where the run lists components in full, and nil otherwise.
	Members map[string][]report.Positioned

	Findings    []Finding
	Withheld    []Finding
	Evaluations []evaluation
	Stale       []report.StaleSuppression
	InEffect    int
	Reasons     int
}

// words is the reader's word for each kind of declaration, which a message spells
// out where the vocabulary hyphenates it.
var words = map[inventory.SymbolKind]string{
	inventory.KindFunc:            "function",
	inventory.KindMethod:          "method",
	inventory.KindType:            "type",
	inventory.KindInterface:       "interface",
	inventory.KindInterfaceMethod: "interface method",
	inventory.KindField:           "field",
	inventory.KindConst:           "constant",
	inventory.KindVar:             "variable",
	inventory.KindTypeParam:       "type parameter",
}

// symbolKinds maps a declaration's kind onto the Contract's subject vocabulary. A
// package is absent because the vocabulary has no word for one and nothing reports
// a package as a subject.
var symbolKinds = map[inventory.SymbolKind]string{
	inventory.KindFunc:            "function",
	inventory.KindMethod:          "method",
	inventory.KindType:            "type",
	inventory.KindInterface:       "interface",
	inventory.KindInterfaceMethod: "interface-method",
	inventory.KindField:           "field",
	inventory.KindConst:           "constant",
	inventory.KindVar:             "variable",
	inventory.KindTypeParam:       "type-parameter",
	inventory.KindFile:            fileSubject,
}

// vocabularyKinds is the vocabulary the framework runs the emitters in the order of.
// It is a variable so that a test can put a kind with a ceiling below certain in
// front of the framework: every shipped kind declares certain, so the arm that caps
// a finding's confidence is otherwise unreachable.
var vocabularyKinds = vocab.Kinds

// Compute runs every kind of the language, in the vocabulary's order, and returns
// the finished findings of the run, the edge evaluations and the suppression
// ledger's answer. A draft the Contract cannot carry fails the pass: it is an
// emitter's defect. The order is fixed: complete, skip, the component cap
// ([cohere]), the dials ([pass.dial]), the ledger ([pass.decide]), then the edge
// evaluations.
func Compute(in *Input, emitters map[vocab.Code]Emitter) (Result, error) {
	if in == nil || in.Config == nil {
		return Result{}, fmt.Errorf("%w: no resolved configuration", errInput)
	}

	if err := checkTable(emitters); err != nil {
		return Result{}, err
	}
	return newPass(in).compute(emitters)
}

// compute is [Compute] over the facts of p.
func (in *pass) compute(emitters map[vocab.Code]Emitter) (Result, error) {
	findings, skipped, err := in.phase(vocabularyKinds(), emitters)
	if err != nil {
		return Result{}, err
	}
	cohere(findings)
	in.dialed = in.dial(findings)
	ledger, would, err := in.decide(findings, skipped, emitters)
	if err != nil {
		return Result{}, err
	}

	kept, withheld := in.reportable(findings, ledger)
	slices.SortStableFunc(kept, compare)
	in.annotate(kept)
	result := Result{Withheld: withheld, Stale: in.stale(ledger, would)}
	result.Findings, result.Evaluations = in.evaluate(kept)
	result.Withheld, _ = in.evaluate(result.Withheld)
	result.InEffect, result.Reasons = core.Counts(in.Marks, ledger)
	in.mint(&result)
	return result, nil
}

// mint numbers every component of its own past the sweep's, in the order the report
// carries the findings: the reported ones, then each evaluation's, then the
// withheld ones, so a report's bytes do not depend on which kind ran first.
func (in *pass) mint(result *Result) {
	next := len(in.swept)
	number := func(found *Finding) {
		if found.Component.ID == "" {
			next++
			found.Component.ID = ComponentID(next)
		}
	}
	for i := range result.Findings {
		number(&result.Findings[i])
	}
	for i := range result.Evaluations {
		if pending := result.Evaluations[i].Finding; pending != nil {
			number(pending)
		}
	}
	for i := range result.Withheld {
		number(&result.Withheld[i])
	}
	if in.Config.Reporters.CascadeFull {
		result.Members = in.members(result)
	}
}

// members is the members of every component the findings and the evaluations'
// findings name.
func (in *pass) members(result *Result) map[string][]report.Positioned {
	members := map[string][]report.Positioned{}
	add := func(found *Finding) {
		if _, listed := members[found.Component.ID]; listed {
			return
		}
		held := in.membersOf(found)
		written := make([]report.Positioned, len(held))
		for i := range held {
			written[i] = report.Positioned{Ref: held[i].Ref, Name: held[i].Name, Position: held[i].Position.document()}
		}
		members[found.Component.ID] = written
	}
	for i := range result.Findings {
		add(&result.Findings[i])
	}
	for i := range result.Evaluations {
		if pending := result.Evaluations[i].Finding; pending != nil {
			add(pending)
		}
	}
	return members
}

// phase runs the emitters of the published kinds in the vocabulary's order: every
// kind of the language this analyzer has an emitter for, one the severity allows
// included, because its root findings withhold their components. It returns the
// findings and, apart, those a type error skips ([pass.runKind]).
func (in *pass) phase(published []vocab.Kind, emitters map[vocab.Code]Emitter) (findings, skipped []Finding, err error) {
	reported := make(map[string]string)
	for i := range published {
		row := &published[i]
		emit, runs := emitters[row.Code]
		if !runs || !row.AppliesTo(vocab.Go) {
			continue
		}
		var produced, held []Finding
		if produced, held, err = in.runKind(emit, row, reported); err != nil {
			return nil, nil, err
		}
		findings, skipped = append(findings, produced...), append(skipped, held...)
	}
	return findings, skipped, nil
}

// runKind runs one kind's emitter and completes every draft it returned, in the order
// the emitter returned them, dropping each that falls. A finding a type error skips
// is returned apart: only the record naming it reads it ([pass.decide]); no cap,
// severity or confidence dial, or report does.
func (in *pass) runKind(emit Emitter, row *vocab.Kind, reported map[string]string) (completed, skipped []Finding, err error) {
	produced, err := emit(in)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %w", errEmitter, row.Code, err)
	}
	completed = make([]Finding, 0, len(produced))
	for i := range produced {
		found := in.complete(&produced[i], row)
		if refused := in.resolve(&found, row.Code, reported); refused != nil {
			return nil, nil, refused
		}
		if found.id != "" && in.falling.falls[found.id] {
			continue
		}
		if in.skips(&found) {
			skipped = append(skipped, found)
			continue
		}
		completed = append(completed, found)
	}
	return completed, skipped, nil
}

// checkTable refuses a table registering an emitter under a code the vocabulary
// does not hold, which is a retired or misspelled code whose emitter would
// otherwise run as nothing and report silently.
func checkTable(emitters map[vocab.Code]Emitter) error {
	for _, code := range slices.Sorted(maps.Keys(emitters)) {
		if !code.Live() {
			return fmt.Errorf("%w: an emitter is registered under %s, which names no live kind",
				errEmitter, code)
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

	// shapeRow is a row of a document: a file no configuration compiled, a
	// suppression record, a configured root. The analysis enumerates no
	// declaration for it at all, and no suppression record binds to it.
	shapeRow

	// shapeDependency is a row of a document a suppression record binds to by its
	// reference and path: a requirement or a directive of the module file. It is a
	// row in every other respect.
	shapeDependency
)

// shapes is the shape a finding takes for each subject the vocabulary names: an
// import binding is a part of its file, a file and a record are rows.
var shapes = map[vocab.Subject]shape{
	vocab.SubjectDeclaration: shapeDeclaration,
	vocab.SubjectBinding:     shapePart,
	vocab.SubjectPart:        shapePart,
	vocab.SubjectDependency:  shapeDependency,
	vocab.SubjectFile:        shapeRow,
	vocab.SubjectRecord:      shapeRow,
}

// aboutRow reports whether one finding's subject is a row of any document, a
// dependency row included.
func aboutRow(found *Finding) bool {
	held := shapeOf(found.Symbol.Kind)
	return held == shapeRow || held == shapeDependency
}

// shapeOf is the shape of a finding about one kind of subject.
func shapeOf(subjectKind string) shape {
	subject, _ := vocab.SubjectOf(subjectKind)
	return shapes[subject]
}

// livenessAbsent reports whether one finding carries no liveness relation, which the
// Contract decides two ways: the subject is a part of a declaration or a row of a
// document, or the kind holds its subject live. A finding read back from a document
// that names no relation answers through Live.
func livenessAbsent(found *Finding) bool {
	return shapeOf(found.Symbol.Kind) != shapeDeclaration || holdsLive(found.Code) || found.Live
}

// key is one finding's identity, and two findings of one key is a defect: a part's
// is its [partKey], a declaration's its identifier, and anything else's the rendered
// position of the thing it names. A row of a document adds where its record is
// written, the rule it breaks and the record (reference and code): an entry lacking
// both its reason and its path is one finding per rule at one position, a
// reasonless directive one per code, two directives a line directive renders at one
// site are two, and every unmatched configured root renders at its document's first
// position.
func key(found *Finding) string {
	switch {
	case found.decl != "":
		return string(found.decl) + "@" + strconv.Itoa(found.written)
	case found.id != "":
		return string(found.id)
	}
	at := found.Position.Path + ":" + strconv.Itoa(found.Position.Line) +
		":" + strconv.Itoa(found.Position.Column)
	if !aboutRow(found) {
		return at
	}
	record := at + "@" + found.record + " " + string(found.Code) + " " + found.Symbol.Ref
	if found.Details.Entry != nil {
		record += " " + string(found.Details.Entry.Code)
	}
	return record
}

// resolve identifies one finding and refuses what the Contract cannot carry: a code
// that is not the emitter's own, a message that is not one sentence a consumer can
// extend, a second finding of one [key], which is two kinds disagreeing about which
// reports the subject when each kind's precedence should settle it, and a subject
// the inventory cannot answer for ([pass.admit]).
func (in *pass) resolve(found *Finding, code vocab.Code, reported map[string]string) error {
	if found.Code != code {
		return fmt.Errorf("%w: the %s emitter returned a %s finding", errEmitter, code, found.Code)
	}
	if err := checkMessage(found); err != nil {
		return err
	}
	at := key(found)
	if first, twice := reported[at]; twice {
		return fmt.Errorf("%w: %s reports %s at %s, which %s already reports",
			errEmitter, found.Code, found.Symbol.Ref, at, first)
	}
	if err := in.admit(found); err != nil {
		return err
	}
	reported[at] = string(found.Code)
	return nil
}

// admit refuses a finding whose subject the inventory cannot answer for, by shape: a
// declaration it does not hold or an exemption retained, a part of a declaration it
// does not hold, and a row naming no record, whose position names only the document.
// A part answers to no exemption: one over its declaration is no claim about it.
func (in *pass) admit(found *Finding) error {
	switch found.shape() {
	case shapeRow, shapeDependency:
		if found.Symbol.Ref == "" {
			return fmt.Errorf("%w: %s reports a %s and names no reference",
				errEmitter, found.Code, found.Symbol.Kind)
		}
	case shapePart:
		if in.byRef[found.DeclarationRef()] == "" {
			return fmt.Errorf("%w: %s reports a %s of %q, which names no declaration of the inventory",
				errEmitter, found.Code, found.Symbol.Kind, found.Symbol.Ref)
		}
	default:
		if found.id == "" {
			return fmt.Errorf("%w: %s names no declaration of the inventory: %q",
				errEmitter, found.Code, found.Symbol.Ref)
		}
		if in.retained[found.id] {
			return fmt.Errorf("%w: %s reports %s, which an exemption retained",
				errEmitter, found.Code, found.Symbol.Ref)
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
			errEmitter, found.Code, found.Symbol.Ref)
	case strings.ContainsAny(found.Message, "\r\n"):
		return fmt.Errorf("%w: %s holds a line break in its message: %q",
			errEmitter, found.Code, found.Message)
	case strings.HasSuffix(found.Message, "."):
		return fmt.Errorf("%w: %s ends its message in a full stop: %q",
			errEmitter, found.Code, found.Message)
	}
	return nil
}

// subjectOf copies the draft's subject and, where the emitter left the kind empty,
// fills the inventory's word for the declaration.
func (in *pass) subjectOf(d *draft) Subject {
	subject := d.Symbol
	if subject.Kind == "" {
		subject.Kind = symbolKinds[in.kindOf(d.id)]
	}
	return subject
}

// complete makes the finding of one draft, filling everything the Contract requires
// of a finding whatever its kind, so no emitter decides any of it. A declaration
// carries the relation that decided it (none where the sweep judged it live or the
// kind holds its subject live) and its dead component; a part or a document row
// carries no relation and a component of its own. A finding in a generated file
// carries no fixability. A finding about a declaration of a held component carries
// the manual one where its kind lists it.
func (in *pass) complete(d *draft, row *vocab.Kind) Finding {
	found := Finding{
		Code:     d.Code,
		Kind:     row.Name,
		Language: language,
		Position: d.Position,
		Symbol:   in.subjectOf(d),
		TestOnly: d.TestOnly,
		Message:  d.Message,
		Details:  d.Details,
		id:       d.id,
		decl:     d.decl,
		written:  d.written,
		record:   d.record,
		local:    d.local,
	}
	held := false
	switch {
	case found.local:
		found.Live, found.Relation = false, liveness.ReferenceCounting
		found.Component = in.componentOf("")
	case shapeOf(found.Symbol.Kind) == shapeDeclaration:
		if row.HoldsLive {
			found.relation(nil)
		} else {
			found.relation(in.candidates[found.id])
		}
		found.Component = in.componentOf(found.id)
		if c, _ := in.component(found.id); c != nil {
			held = c.Held
		}
	default:
		found.relation(nil)
		found.Component = in.componentOf("")
	}
	in.classify(&found, d, row)
	found.Severity = in.Config.EffectiveSeverity(row.Code)
	found.Fixability = row.Fixability
	if held && slices.Contains(row.FindingFixabilities, vocab.Manual) {
		found.Fixability = vocab.Manual
	}
	found.RetainedBy = []string{}
	found.ConsumersLoaded = slices.Clone(in.Consumers.Loaded)
	if found.ConsumersLoaded == nil {
		found.ConsumersLoaded = []string{}
	}
	found.Configurations = in.configurationsOf(&found)
	found.Details.Overlap = row.Overlap[vocab.Go]
	found.Generated = in.generated(in.fileOf(&found))
	if found.Generated {
		found.Fixability = vocab.None
	}
	return found
}

// relation records the liveness relation one finding carries: the candidate's
// where the sweep judged the subject dead, and none where it judged it live.
//
// The relation Live leaves in place is the reference-counting one, which is what a
// consumer of a document that requires the member reads about a subject no relation
// decided.
func (f *Finding) relation(candidate *liveness.Candidate) {
	if candidate == nil {
		f.Live = true
		f.Relation = liveness.ReferenceCounting
		return
	}
	f.Live = false
	f.Relation = candidate.Relation
}

// compare orders two findings as the report orders them, so explain lists them
// in the report's order.
//
//nolint:gocritic // hugeParam: the standard library's sort takes the element type by value
func compare(a, b Finding) int {
	keyA, keyB := a.orderKey(), b.orderKey()
	return report.CompareFindings(&keyA, &keyB)
}

// fileOf is the file that writes one finding's subject, or the declaration holding
// it, which a line directive does not move; and a row's document.
func (in *pass) fileOf(found *Finding) string {
	if symbol := in.symbol(cmp.Or(found.id, found.decl)); symbol != nil {
		return symbol.File
	}
	return found.Position.Path
}

// generated reports whether one path is a generated file, and false where the run
// carries no rule for it.
func (in *pass) generated(file string) bool {
	if in.Generated == nil {
		return false
	}
	return in.Generated(file)
}

// configurationsOf names the build configurations one finding holds under, by the
// shape of its subject: a declaration's candidate's, or where the sweep judged it
// live every one it exists in; a part's declaration's; and for a row every
// configuration of the matrix, since a document is read once and a row is reported
// only where every configuration agrees.
func (in *pass) configurationsOf(found *Finding) []string {
	var id inventory.SymbolID
	switch found.shape() {
	case shapeRow, shapeDependency:
		return append(make([]string, 0, len(in.Matrix)), in.Matrix...)
	case shapePart:
		id = found.decl
	default:
		id = found.id
	}
	set := inventory.ConfigSet(0)
	if candidate := in.candidates[id]; candidate != nil {
		set = candidate.Configs
	}
	if set == 0 {
		if symbol := in.symbols[id]; symbol != nil {
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

// finding starts the draft about one declaration of the inventory, named by its
// identifier, and answers false for an identifier the inventory does not hold, so a
// kind that reports one reports nothing. A part or a row is started by [findingAt],
// by position, since the inventory holds no declaration for either.
func (in *pass) finding(id inventory.SymbolID, code vocab.Code, message string) (draft, bool) {
	symbol := in.symbol(id)
	if symbol == nil {
		return draft{}, false
	}
	position := Position{
		Path:    symbol.Pos.Filename,
		Line:    symbol.Pos.Line,
		Column:  symbol.Pos.Column,
		EndLine: max(symbol.Pos.Line, symbol.EndLine),
	}
	return draft{
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
func findingAt(code vocab.Code, subject Subject, at Position, message string) draft {
	return draft{
		Code:     code,
		Position: at,
		Symbol:   subject,
		Message:  message,
	}
}

// word is the reader's word for the kind of one declaration, and the vocabulary's
// own spelling for a kind no message has a word for.
func (in *pass) word(id inventory.SymbolID) string {
	kind := in.kindOf(id)
	if word, held := words[kind]; held {
		return word
	}
	return kind.String()
}

// kindOf is the kind of one declaration of the inventory.
func (in *pass) kindOf(id inventory.SymbolID) inventory.SymbolKind {
	if symbol := in.symbols[id]; symbol != nil {
		return symbol.Kind
	}
	return inventory.KindPackage
}

// componentOf is the dead component one finding's subject belongs to, as the report
// carries it: the one projection of the sweep's structural component. A subject in
// none, a live declaration, referenced test-support code, a part or a row, gets one
// of its own, the subject its one root and [pass.fallen] its members, deleting no
// line, which [pass.mint] numbers once the report's order is known.
func (in *pass) componentOf(id inventory.SymbolID) report.Component {
	component, at := in.component(id)
	if component == nil {
		return report.Component{
			Root:        true,
			SymbolCount: len(in.fallen(id)),
		}
	}
	return report.Component{
		ID:             ComponentID(at + 1),
		SymbolCount:    len(component.Members),
		DeletableLines: DeletableLines(component),
		Root:           slices.Contains(component.Roots, id),
	}
}

// membersOf is every symbol of one finding's component, ordered by position and
// then by reference, which is the order a full listing names them in. A component
// minted for a subject that belongs to none holds the subject alone. Every finding
// of one dead component shares one listing, which no caller may change.
func (in *pass) membersOf(found *Finding) []positioned {
	component, at := in.component(found.id)
	if found.id == "" || (component == nil && in.candidates[found.id] == nil) {
		return []positioned{{Ref: found.Symbol.Ref, Name: found.Symbol.Name, Position: found.Position}}
	}
	if component == nil {
		return in.listing(in.fallen(found.id))
	}
	return in.listed[at]
}

// listing is the members of one set of symbols in the order [pass.membersOf] names.
func (in *pass) listing(ids []inventory.SymbolID) []positioned {
	members := make([]positioned, 0, len(ids))
	for _, id := range ids {
		symbol := in.symbols[id]
		if symbol == nil {
			continue
		}
		members = append(members, positioned{Ref: symbol.Ref, Name: symbol.Name, Position: Position{
			Path:    symbol.Pos.Filename,
			Line:    symbol.Pos.Line,
			Column:  symbol.Pos.Column,
			EndLine: max(symbol.Pos.Line, symbol.EndLine),
		}})
	}
	slices.SortFunc(members, byWrittenPosition)
	return members
}

// fallen is the members of a component minted for one subject: the subject, and
// where the sweep judged it dead, every dead member it contains at any depth, which
// falls with its container.
func (in *pass) fallen(id inventory.SymbolID) []inventory.SymbolID {
	members := []inventory.SymbolID{id}
	if id == "" || in.candidates[id] == nil {
		return members
	}
	for at := 0; at < len(members); at++ {
		for _, child := range in.children[members[at]] {
			if in.candidates[child] != nil {
				members = append(members, child)
			}
		}
	}
	return members
}

// DeletableLines is the number of distinct source lines deleting one dead component
// removes: the lines its spans cover, a line two of them share counted once, and 0
// for a held component.
func DeletableLines(c *liveness.Component) int {
	if c.Held {
		return 0
	}
	ordered := slices.Clone(c.Spans)
	slices.SortFunc(ordered, func(a, b liveness.Span) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.First, b.First))
	})
	total := 0
	var covered liveness.Span
	for i, s := range ordered {
		if i == 0 || s.Path != covered.Path || s.First > covered.Last {
			total += s.Last - s.First + 1
			covered = s
			continue
		}
		if s.Last > covered.Last {
			total += s.Last - covered.Last
			covered.Last = s.Last
		}
	}
	return total
}

// ComponentID is the identifier this analyzer mints for one component: its own
// name, a solidus, and the component's number.
func ComponentID(number int) string {
	return componentPrefix + strconv.Itoa(number)
}

const componentPrefix = analyzerName + "/c-"

// ComponentNumber is the number of an identifier [ComponentID] minted, and false
// for any other identifier.
func ComponentNumber(id string) (int, bool) {
	digits, minted := strings.CutPrefix(id, componentPrefix)
	n, err := strconv.Atoi(digits)
	return n, minted && err == nil && ComponentID(n) == id
}

// holdsLive reports whether the kind of one code reports a subject the analysis
// holds live, so no relation decided its finding.
func holdsLive(code vocab.Code) bool {
	kind, _ := vocab.KindOf(code)
	return kind.HoldsLive
}
