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
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/deps"
	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
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
	errInput = errors.New("kinds: incomplete input")

	// errEmitter reports a finding an emitter returned that the Contract cannot
	// carry: a message that is not one sentence, a code that is not the
	// emitter's own, a second finding about one declaration, or a finding about
	// a symbol an exemption retained. Each is a defect in an emitter rather than
	// a row of a report, so the pass fails instead of publishing it.
	errEmitter = errors.New("kinds: emitter defect")
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

// findingComponent is the dead component a subject belongs to, so a report is worked from
// its roots and one deletion removes a cluster. The identifier is minted by the
// framework, prefixed with this analyzer's own name so a merge unions components
// without renaming one.
//
// Members is every symbol of the component in the canonical order, where the run
// lists a component in full, and empty otherwise.
type findingComponent struct {
	ID             string
	Members        []positioned
	SymbolCount    int
	DeletableLines int
	Root           bool

	// Held reports a component no mechanical edit deletes, whose findings carry
	// the manual fixability where their kind lists it.
	Held bool
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
	Code       vocab.Code
	Kind       string
	Language   vocab.Language
	Position   Position
	Symbol     Subject
	Class      vocab.Class
	Confidence vocab.Class
	Relation   graph.Relation

	// Live reports that no liveness relation decided this finding, so a report
	// carries none: a declaration the sweep judged live, a finding under a code of
	// a kind that holds its subject live, and a subject that is no declaration of the inventory at all.
	// Relation then holds the relation a reader of a document that still requires
	// the member reads, and a reporter writing a document that does not omits the
	// member.
	Live bool

	TestOnly        bool
	Generated       bool
	Component       findingComponent
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
	id graph.SymbolID

	// ceiling caps the reachability class [Input.complete] gives the finding, for an
	// emitter whose subject's class is not its declaration's own; no class caps nothing.
	ceiling vocab.Class

	// testUsed marks a finding [Input.testUse] lowers to possible, and untested is the
	// confidence it would carry otherwise, which is what its component's cap and the
	// dials read: the lowering is the finding's own and no other member's.
	testUsed bool
	untested vocab.Class

	// testRead marks a write-only finding whose declaration only test files read
	// ([Input.capTestUsed]).
	testRead bool

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

// DeclarationRef is the reference of the declaration a finding's subject is or is a
// part of. A parameter has a reference of its own, under its holder's.
func (f *Finding) DeclarationRef() string {
	if f.Symbol.Kind != parameterSubject {
		return f.Symbol.Ref
	}
	holder, _ := graph.ParameterHolder(f.Symbol.Ref)
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
// from it, the resolver over them and its test classes, for a kind that reads
// syntax or types.
type Configured struct {
	Result  *load.Result
	Resolve *graph.Resolver
	Tests   graph.TestClasses
	Symbols []graph.Symbol
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

	// TestEvidence is the exemption evidence a test file carries, which a
	// production mode leaves out of Exempt. A conversion or a satisfaction a test
	// makes still fixes the signature it reaches, so the intra-function kinds read
	// it beside Exempt.
	TestEvidence []graph.Exemption

	// Filled is every field a callee outside the analysed program fills from a
	// document and may read, which nothing retains ([Input.capFilled]).
	Filled map[graph.SymbolID]bool

	// Generated reports whether one target-relative path is a generated file.
	Generated func(path string) bool

	// TypeErrorSkips is every declaration a type error skipped, about which
	// nothing is reported.
	TypeErrorSkips []graph.TypeErrorSkip

	// Marks is every suppression record the run read, bound or not, and Refusals
	// is what the grammar refused with a finding of its own rather than with an
	// exit. The self-check kinds read both: a record the sweep did not hold back
	// is stale, a record whose code the configuration disables is dormant and
	// reported by nothing, and a refusal is reported as the suppression was
	// written.
	Marks    []suppress.Record
	Refusals []suppress.Refusal

	// Root is the target root, which every position a kind reports is relative
	// to.
	Root string

	// Modules is every module file of the target as the toolchain reports it,
	// in unit order, which is what the dependency kinds read.
	Modules []ModuleFile

	// Unmatched is every configured root the roots pass matched nothing with,
	// which is a configuration naming something that no longer exists.
	Unmatched []graph.Unmatched

	// RootsDocument is the configuration document that declared the configured
	// roots, as a target-relative path, and is empty where no document of the
	// target did.
	//
	// A finding about a configured root is a finding about the document that
	// declares it, so the document's own path is the path it carries. Which
	// document that is belongs to the configuration resolution, which reads the
	// documents in the order they outrank each other and records where every
	// setting came from, so the composition root hands the answer here rather than
	// letting this pass name a conventional path the run may not have read.
	RootsDocument string

	// Edges is the declared cross-language edges of the target, nil where the
	// target carries no edges document.
	Edges *edges.Document

	// indexed is what a lookup over the inventory needs, built on first use.
	indexed *index

	// fixed is [Input.fixedByOutsideInterface]'s answer, built on first use.
	fixed map[graph.SymbolID]bool

	// withheld names every record of Marks, by its place in that slice, whose code
	// and declaration matched a finding this pass produced, with each finding the
	// pass withheld through it. A pass records it and the kind that reports a record
	// which withheld nothing reads it, which is what makes staleness one question
	// asked once over every kind rather than one answer per kind.
	//
	// It is the pass's own answer and is rebuilt by each pass, so a second pass over
	// one input inherits nothing from the first.
	withheld map[int][]heldFinding

	// dialed is what the configuration dials withhold beyond the findings they
	// name, which the pass computes between its two phases.
	dialed dialed

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
	vocab.StaleSuppression:            staleSuppressions,
	vocab.UnmatchedRoot:               unmatchedRoots,
	vocab.UnusedParameter:             unusedParameter,
	vocab.UnusedReceiver:              unusedReceiver,
	vocab.UnusedResult:                unusedResult,
	vocab.UnreachableStatement:        unreachableStatement,
	vocab.DeadStore:                   deadStore,
	vocab.UnreachableCase:             unreachableCase,
}

// Result is what one findings pass produced: the findings in the canonical order,
// and the findings the configured minimum confidence alone withheld.
type Result struct {
	Findings []Finding
	Withheld []Finding
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

// vocabularyKinds is the vocabulary the framework runs the emitters in the order of.
// It is a variable so that a test can put a kind with a ceiling below certain in
// front of the framework: every shipped kind declares certain, so the arm that caps
// a finding's confidence is otherwise unreachable.
var vocabularyKinds = vocab.Kinds

// readsThePass names the kinds whose rule is about what this pass produced rather
// than about what the analysis answered, so their emitters run after every other
// one and read what the earlier emitters recorded on the input.
//
// One kind is in it. A suppression record withholds the finding its code would have
// produced whatever the kind, so which records withheld nothing is known only once
// every other emitter has answered, which no position in the vocabulary promises.
// The order is therefore a property of the kind, declared here beside the emitter.
var readsThePass = map[vocab.Code]bool{
	vocab.StaleSuppression: true,
}

// Compute runs every kind the resolved severity does not disable and returns the
// findings of the whole run.
//
// The emitters run in the vocabulary's order, so a run's own work order is the
// Contract's rather than a map's, and a code this analyzer has no emitter for
// reports nothing. Each finding an emitter returns is checked against what the
// Contract can carry and then completed; a refusal fails the pass, because each
// one is a defect in an emitter rather than something a report could say. A finding
// a suppression record names is then withheld, which is the whole of what a record
// does beyond the mark the sweep already took.
//
// The kinds run in two phases, both in the vocabulary's order: every kind that reads
// the analysis, then the kinds of readsThePass, which read what the first phase
// withheld. Both phases share one identity check over the findings, so a second
// finding at one key fails the pass whichever phase produced it.
func Compute(in *Input, emitters map[vocab.Code]Emitter) (Result, error) {
	if in == nil || in.Config == nil {
		return Result{}, fmt.Errorf("%w: no resolved configuration", errInput)
	}

	if err := checkTable(emitters); err != nil {
		return Result{}, err
	}
	in.withheld = make(map[int][]heldFinding, len(in.Marks))
	reported := make(map[string]string)
	published := vocabularyKinds()
	findings, err := in.phase(published, emitters, reported, false)
	if err != nil {
		return Result{}, err
	}
	capComponents(findings)
	if withholding := in.withholdComponents(published, emitters, findings); withholding != nil {
		return Result{}, withholding
	}
	second, err := in.phase(published, emitters, reported, true)
	if err != nil {
		return Result{}, err
	}
	findings = append(findings, second...)

	kept, withheld := in.reportable(findings)
	slices.SortStableFunc(kept, compare)
	return Result{Findings: in.withoutSkipped(kept), Withheld: in.withoutSkipped(withheld)}, nil
}

// withoutSkipped drops every finding positioned inside a declaration a type error
// skipped: nothing is reported about the declaration or any part of it.
func (in *Input) withoutSkipped(findings []Finding) []Finding {
	if len(in.TypeErrorSkips) == 0 {
		return findings
	}
	return slices.DeleteFunc(findings, func(found Finding) bool {
		return slices.ContainsFunc(in.TypeErrorSkips, func(skip graph.TypeErrorSkip) bool {
			return skip.FromLine > 0 && skip.Path == found.Position.Path &&
				skip.FromLine <= found.Position.Line && found.Position.Line <= skip.ToLine
		})
	})
}

// capComponents lowers every finding of a dead component to the lowest confidence
// among the component's root findings. [Input.componentCap] reads only the roots'
// classes, so a cap a root's own finding carries, the name-literal one, would
// otherwise split the component and the dials would withhold a certain member.
func capComponents(findings []Finding) {
	least := make(map[string]vocab.Class)
	for i := range findings {
		found := &findings[i]
		if !found.Component.Root {
			continue
		}
		if floor, seen := least[found.Component.ID]; !seen || rank(found.componentConfidence()) < rank(floor) {
			least[found.Component.ID] = found.componentConfidence()
		}
	}
	for i := range findings {
		if floor, seen := least[findings[i].Component.ID]; seen {
			findings[i].Confidence = lower(findings[i].Confidence, floor)
			findings[i].untested = lower(findings[i].untested, floor)
		}
	}
}

// componentConfidence is the confidence a finding gives its component's cap and the
// dials: its own, but for the lowering [Input.capTestUsed] made.
func (f *Finding) componentConfidence() vocab.Class {
	if f.testUsed {
		return f.untested
	}
	return f.Confidence
}

// capTestUsed lowers a finding about production code only tests use to possible: a
// declaration of [testUseFacts.dead], and a declaration of a package only tests reach
// that a write-only finding finds read only by test files, which the sweep holds
// live. Its class is lowered too unless it is a test of dead code, whose class is
// its own.
func (in *Input) capTestUsed(found *Finding, row *vocab.Kind) {
	if found.id == "" || found.local {
		return
	}
	symbol := in.symbol(found.id)
	dead := !row.HoldsLive && in.testUse().dead[found.id]
	if !dead && (!found.testRead || symbol == nil || !in.testReached(symbol)) {
		return
	}
	found.testUsed, found.untested = true, found.Confidence
	if row.Code != vocab.TestOfDeadCode {
		found.Class = lower(found.Class, vocab.Possible)
	}
	found.Confidence = lower(found.Confidence, vocab.Possible)
}

// phase runs the emitters of one phase, in the vocabulary's order, and returns what
// they produced that no suppression record withheld.
func (in *Input) phase(published []vocab.Kind, emitters map[vocab.Code]Emitter,
	reported map[string]string, second bool,
) ([]Finding, error) {
	var findings []Finding
	for i := range published {
		row := &published[i]
		if readsThePass[row.Code] != second {
			continue
		}
		emit, runs := in.emitterOf(row, emitters)
		if !runs {
			continue
		}
		produced, err := in.runKind(emit, row, reported)
		if err != nil {
			return nil, err
		}
		findings = append(findings, produced...)
	}
	return findings, nil
}

// emitterOf is the emitter one kind of the vocabulary runs in this pass, and false
// where the kind contributes nothing: a kind of another language, a kind the
// resolved severity allows, and a code this analyzer has no emitter for.
func (in *Input) emitterOf(row *vocab.Kind, emitters map[vocab.Code]Emitter) (Emitter, bool) {
	if !row.AppliesTo(vocab.Go) {
		return nil, false
	}
	if in.Config.EffectiveSeverity(row.Code) == vocab.Allow {
		return nil, false
	}
	emit, carried := emitters[row.Code]
	return emit, carried
}

// runKind runs one kind's emitter, completes every finding it returned in the order
// the emitter returned them, and withholds each finding a suppression record names.
//
// The withholding runs after the completion because the shape of a finding decides
// what a record can bind to and the completion is what fills the subject's kind in.
// A withheld finding is identified first and keeps its identity, so a record does not
// license a second kind to claim the position the withheld finding named.
func (in *Input) runKind(emit Emitter, row *vocab.Kind, reported map[string]string) ([]Finding, error) {
	produced, err := emit(in)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errEmitter, row.Code, err)
	}
	completed := make([]Finding, 0, len(produced))
	for i := range produced {
		found := produced[i]
		if err := in.resolve(&found, row.Code, reported); err != nil {
			return nil, err
		}
		in.complete(&found, row)
		if found.id != "" && in.annotationCascade().falls[found.id] {
			continue
		}
		if row.Code == vocab.TestOfDeadCode && in.testsOutsideUse()[found.id] {
			continue
		}
		if in.withhold(&found) {
			continue
		}
		completed = append(completed, found)
	}
	return completed, nil
}

// testsOutsideUse is every test of dead code that references a production
// declaration whose reachability class is possible: outside programs may call that
// declaration, so it counts as a live target symbol and the test is no test of dead
// code. It is computed on first use.
func (in *Input) testsOutsideUse() map[graph.SymbolID]bool {
	held := in.index()
	if held.outsideUse != nil {
		return held.outsideUse
	}
	held.outsideUse = make(map[graph.SymbolID]bool)
	if in.Merged == nil {
		return held.outsideUse
	}
	for i := range in.Merged.References {
		ref := &in.Merged.References[i]
		test, target := in.candidateOf(ref.From), in.symbol(ref.To)
		if test == nil || !test.TestOfDeadCode || target == nil || testFile(target) || target.TestSupport {
			continue
		}
		if in.classOf(ref.To) == vocab.Possible {
			held.outsideUse[ref.From] = true
		}
	}
	return held.outsideUse
}

// withhold reports whether a suppression record withholds one finding, recording
// each finding against the first record in Marks' reading order that pairs with it:
// a record's mark changes nothing for a finding about a declaration the analysis
// holds live or about a part, so the finding is withheld where it is produced. A
// dependency row is withheld by a record naming its reference and path; any other
// row of a document binds no record. A withheld finding is counted nowhere: the
// suppression totals of the envelope report it.
func (in *Input) withhold(found *Finding) bool {
	if shapeOf(found.Symbol.Kind) == shapeRow {
		return false
	}
	for i := range in.Marks {
		if !pairs(&in.Marks[i], found) {
			continue
		}
		in.withheld[i] = append(in.withheld[i], heldFinding{confidence: found.Confidence, component: found.Component.ID})
		return true
	}
	return false
}

// heldFinding is what the dials read of one finding a record withheld: its
// confidence and its dead component.
type heldFinding struct {
	confidence vocab.Class
	component  string
}

// pairs reports whether one suppression record is a record of one finding: it marked
// the finding's own declaration, as a directive does, or it names the finding's code,
// reference and path exactly, as an entry and a baseline row do. A satisfaction
// assertion's subject and a part are reachable only the second way. A record naming a
// declaration reaches every part of it; one naming a parameter's own reference reaches
// that parameter alone. The path is the finding's position, not the reference's file,
// so an assertion and its interface may live in two files and an entry for one file
// never masks a same-named symbol in another.
func pairs(mark *suppress.Record, found *Finding) bool {
	if mark.Code != found.Code {
		return false
	}
	if mark.Bound != "" && mark.Bound == found.id {
		return true
	}
	return (mark.Symbol == found.Symbol.Ref || mark.Symbol == found.DeclarationRef()) && mark.Path == found.Position.Path
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
	if !aboutRow(found) {
		return at
	}
	record := at + " " + string(found.Code) + " " + found.Symbol.Ref
	if found.Details.Entry != nil {
		record += " " + string(found.Details.Entry.Code)
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
func (in *Input) resolve(found *Finding, code vocab.Code, reported map[string]string) error {
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
	switch found.shape() {
	case shapeRow, shapeDependency:
		if found.Symbol.Ref == "" {
			return fmt.Errorf("%w: %s reports a %s and names no reference",
				errEmitter, found.Code, found.Symbol.Kind)
		}
	case shapePart:
		if in.index().byRef[found.DeclarationRef()] == "" {
			return fmt.Errorf("%w: %s reports a %s of %q, which names no declaration of the inventory",
				errEmitter, found.Code, found.Symbol.Kind, found.Symbol.Ref)
		}
	default:
		if found.id == "" {
			return fmt.Errorf("%w: %s names no declaration of the inventory: %q",
				errEmitter, found.Code, found.Symbol.Ref)
		}
		if in.index().retained[found.id] {
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

// complete fills everything the Contract requires of a finding whatever its kind,
// so no emitter decides any of it. A declaration carries the relation that decided
// it (none where the sweep judged it live or the kind holds its subject live), its
// reachability class and its dead component; a part or a document row carries no
// relation, the certain class and a component of its own. The confidence is capped
// by the kind's ceiling and the component's cap, and a finding in a generated file
// carries no fixability. A finding about a declaration of a held component carries
// the manual one where its kind lists it.
func (in *Input) complete(found *Finding, row *vocab.Kind) {
	held := in.index()
	found.Kind = row.Name
	found.Language = language
	if found.Symbol.Kind == "" {
		found.Symbol.Kind = symbolKinds[held.kindOf(found.id)]
	}
	switch {
	case found.local:
		found.Live, found.Relation = false, graph.ReferenceCounting
		found.Class = vocab.Certain
		found.Component = held.componentOf("")
	case shapeOf(found.Symbol.Kind) == shapeDeclaration:
		if row.HoldsLive {
			found.relation(nil)
		} else {
			found.relation(held.candidates[found.id])
		}
		found.Class = lower(in.classOf(found.id), found.ceiling)
		found.Component = held.componentOf(found.id)
	default:
		found.relation(nil)
		found.Class = vocab.Certain
		found.Component = held.componentOf("")
	}
	if in.Config != nil && in.Config.Reporters.CascadeFull {
		found.Component.Members = held.membersOf(found)
	}
	in.capNameLiteral(found)
	in.capFilled(found)
	found.Confidence = lower(lower(found.Class, row.MaxClass), in.componentCap(found.id))
	in.capTestUsed(found, row)
	found.Severity = in.Config.EffectiveSeverity(row.Code)
	found.Fixability = row.Fixability
	if found.Component.Held && slices.Contains(row.FindingFixabilities, vocab.Manual) {
		found.Fixability = vocab.Manual
	}
	found.RetainedBy = []string{}
	found.ConsumersLoaded = slices.Clone(in.Consumers.Loaded)
	if found.ConsumersLoaded == nil {
		found.ConsumersLoaded = []string{}
	}
	found.Configurations = in.configurationsOf(found)
	found.Details.Overlap = row.Overlap[vocab.Go]
	found.Generated = in.generated(found.Position.Path)
	if found.Generated {
		found.Fixability = vocab.None
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

// compare orders two findings as the report orders them, so explain lists them
// in the report's order.
//
//nolint:gocritic // hugeParam: the standard library's sort takes the element type by value
func compare(a, b Finding) int {
	keyA, keyB := a.orderKey(), b.orderKey()
	return report.CompareFindings(&keyA, &keyB)
}

// generated reports whether one path is a generated file, and false where the run
// carries no rule for it.
func (in *Input) generated(file string) bool {
	if in.Generated == nil {
		return false
	}
	return in.Generated(file)
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
	switch found.shape() {
	case shapeRow, shapeDependency:
		return append(make([]string, 0, len(in.Matrix)), in.Matrix...)
	case shapePart:
		id = held.byRef[found.DeclarationRef()]
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
	mains      map[string]bool     // by [index.packageKey]
	dirs       map[string][]string // each import path's target-relative package directories
	children   map[graph.SymbolID][]graph.SymbolID

	// caps is each dead component's confidence cap by its index, computed on first use.
	caps map[int]vocab.Class

	// exposed is every type code outside its package can reach, computed on first
	// use and nil until then.
	exposed map[graph.SymbolID]bool

	// deprecated is every declaration carrying a deprecation marker, and
	// enumerated every constant of an enumerated type with the type's name. Both
	// are read from the syntax on first use, because the kinds that need them are
	// few and the walk is the whole tree's.
	deprecated map[graph.SymbolID]bool
	enumerated map[graph.SymbolID]string

	// literals is every production string literal of the target by its value, and
	// constTypes each constant's defined type, both read on first use.
	literals   map[string][]literalSite
	constTypes map[graph.SymbolID]graph.SymbolID

	// falling is the write-only kind's annotation cascade, computed on first use.
	falling *annotationFalls

	// unlinked is every package no build links in any configuration that loads it,
	// by [index.packageKey], and unanalysed every unit by its target-relative
	// directory, flagged where it has an unanalysed consumer. Both are computed on
	// first use and nil until then.
	unlinked   map[string]bool
	unanalysed map[string]bool
	// interfaces is what the interface kinds read, computed on first use by a kind
	// that judges an interface method outside them.
	interfaces *interfaceFacts
	testUse    *testUseFacts
	outsideUse map[graph.SymbolID]bool
	// listed is each dead component's member listing by its index, on first use.
	listed map[int][]positioned

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
		dirs:       make(map[string][]string),
		children:   make(map[graph.SymbolID][]graph.SymbolID),
		caps:       make(map[int]vocab.Class),
	}
	held.holdInventory(in.Merged)
	held.holdSweep(in.Sweep)
	held.holdPackages(in.Per, in.Root)
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
		if symbol.Parent != "" {
			x.children[symbol.Parent] = append(x.children[symbol.Parent], symbol.ID)
		}
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
	for ix := range swept.Retained {
		exemption := &swept.Retained[ix]
		x.retained[exemption.ID] = true
	}
}

// holdPackages records the directories of each import path, and which packages
// are main packages, which nothing outside the module can import.
func (x *index) holdPackages(per []Configured, root string) {
	eachPackage(per, func(p *packages.Package) {
		if dir := packageDir(p, root); dir != "" && !slices.Contains(x.dirs[p.PkgPath], dir) {
			x.dirs[p.PkgPath] = append(x.dirs[p.PkgPath], dir)
		}
	})
	eachPackage(per, func(p *packages.Package) {
		if p.Name == mainPackageName {
			x.mains[x.packageKey(p.PkgPath, packageDir(p, root))] = true
		}
	})
}

// eachPackage calls visit for every target package of every configuration.
func eachPackage(per []Configured, visit func(*packages.Package)) {
	for _, one := range per {
		if one.Result != nil {
			for _, p := range one.Result.Packages {
				visit(p)
			}
		}
	}
}

// packageDir is the directory of p's first file relative to root, with forward
// slashes, as a symbol's position names its file.
func packageDir(p *packages.Package, root string) string {
	if len(p.GoFiles) == 0 {
		return ""
	}
	rel, err := filepath.Rel(root, filepath.Dir(p.GoFiles[0]))
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// packageKey names one package of the target: its import path, and where two
// modules of the target declare that path, its directory too.
func (x *index) packageKey(pkgPath, dir string) string {
	if len(x.dirs[pkgPath]) <= 1 {
		return pkgPath
	}
	return pkgPath + "\x00" + dir
}

// packageOf is the [index.packageKey] of the package declaring symbol.
func (in *Input) packageOf(symbol *graph.Symbol) string {
	return in.index().packageKey(symbol.PkgPath, path.Dir(symbol.Pos.Filename))
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
func (in *Input) finding(id graph.SymbolID, code vocab.Code, message string) (Finding, bool) {
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
func findingAt(code vocab.Code, subject Subject, at Position, message string) Finding {
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
// A subject that belongs to none gets a component minted for it alone, the subject
// its one root and [index.fallen] its members, which deletes no line. Four subjects
// answer that way: a declaration the sweep judged live, which the narrowing kinds
// report; a declaration of test-support code that test code references; a part of a
// declaration, which is deleted without the declaration; and a row of a document,
// which names the empty identifier.
func (x *index) componentOf(id graph.SymbolID) findingComponent {
	component := x.components[id]
	if component == nil {
		x.minted++
		return findingComponent{
			ID:          componentID(x.minted),
			Root:        true,
			SymbolCount: len(x.fallen(id)),
		}
	}
	return findingComponent{
		ID:             componentID(component.Index + 1),
		SymbolCount:    len(component.Members),
		DeletableLines: component.DeletableLines,
		Root:           slices.Contains(component.Roots, id),
		Held:           component.Held,
	}
}

// membersOf is every symbol of one finding's component, ordered by position and
// then by reference, which is the order a full listing names them in. A component
// minted for a subject that belongs to none holds the subject alone. Every finding
// of one dead component shares one listing, which no caller may change.
func (x *index) membersOf(found *Finding) []positioned {
	component := x.components[found.id]
	if found.id == "" || (component == nil && x.candidates[found.id] == nil) {
		return []positioned{{Ref: found.Symbol.Ref, Name: found.Symbol.Name, Position: found.Position}}
	}
	if component == nil {
		return x.listing(x.fallen(found.id))
	}
	if held, listed := x.listed[component.Index]; listed {
		return held
	}
	if x.listed == nil {
		x.listed = make(map[int][]positioned)
	}
	x.listed[component.Index] = x.listing(component.Members)
	return x.listed[component.Index]
}

// listing is the members of one set of symbols in the order [index.membersOf] names.
func (x *index) listing(ids []graph.SymbolID) []positioned {
	members := make([]positioned, 0, len(ids))
	for _, id := range ids {
		symbol := x.symbols[id]
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
func (x *index) fallen(id graph.SymbolID) []graph.SymbolID {
	members := []graph.SymbolID{id}
	if id == "" || x.candidates[id] == nil {
		return members
	}
	for at := 0; at < len(members); at++ {
		for _, child := range x.children[members[at]] {
			if x.candidates[child] != nil {
				members = append(members, child)
			}
		}
	}
	return members
}

// componentID is the identifier this analyzer mints for one component: its own
// name, a solidus, and the component's number.
func componentID(number int) string {
	return analyzerName + "/c-" + strconv.Itoa(number)
}

// holdsLive reports whether the kind of one code reports a subject the analysis
// holds live, so no relation decided its finding.
func holdsLive(code vocab.Code) bool {
	kind, _ := vocab.KindOf(code)
	return kind.HoldsLive
}
