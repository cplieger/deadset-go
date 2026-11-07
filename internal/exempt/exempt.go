// Package exempt computes the exemptions of one loaded build configuration: the
// classes of reason for which a symbol the reference graph alone would report is
// live all the same.
//
// A class is policy over type information and produces a set of retained symbols,
// each an [Exemption], which the liveness sweep consumes. This package reads
// neither the sweep nor the configuration: the composition root converts every
// setting into Options, the way it converts every other setting for every other
// stage.
//
// One file holds one class. [Detectors] maps every class this analyzer computes
// to its detection, and Compute runs the table it is given, so a class absent from
// it produces nothing rather than failing to compile.
package exempt

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Detectors is every class this analyzer computes, mapped to its detection. A test
// holds it equal to [goClasses].
var Detectors = map[vocab.Exemption]Detector{
	vocab.InterfaceSatisfaction: InterfaceSatisfactionDetector,
	vocab.EncodingReflection:    EncodingReflectionDetector,
	vocab.FormatVerbContract:    FormatVerbContractDetector,
	vocab.ErrorsDuckTyping:      ErrorsDuckTypingDetector,
	vocab.EnumGroup:             EnumGroupDetector,
	vocab.GeneratedFile:         GeneratedFileDetector,
	vocab.LinknameCgoAsmPlugin:  LinknameCgoAsmPluginDetector,
	vocab.TemplateField:         TemplateFieldDetector,
	vocab.ReflectiveLookup:      ReflectiveLookupDetector,
}

// goClasses lists the classes a Go analysis computes, in vocabulary order, which is
// the order Compute runs them in.
func goClasses() []vocab.Exemption {
	var computed []vocab.Exemption
	for _, class := range vocab.ExemptionClasses() {
		if class.AppliesTo(vocab.Go) {
			computed = append(computed, class.Name)
		}
	}
	return computed
}

// Options is the configured half of an exemption run: what the maintainer turned
// off, and the settings the two classes that read files need. Nothing else about
// the configuration reaches a class.
type Options struct {
	// Disabled are the classes that do not run, so that an exemption suspected
	// of hiding a defect can be tested. A class named twice is disabled once,
	// and a name Compute does not run disables nothing, a class the vocabulary
	// declares for another language alone included.
	Disabled []vocab.Exemption

	// TemplateDelimiters are the action delimiters the template class parses
	// with. Both empty asks for the delimiters the template grammar itself
	// defaults to, which is what a project that sets none renders with.
	TemplateDelimiters Delimiters

	// TemplateDirs are the directories the template class scans, relative to the
	// target root. With none configured that class retains nothing.
	TemplateDirs []string

	// IncludeGenerated asks for findings in generated files, so the generated
	// class retains nothing and each finding in such a file is marked as one no
	// mechanical edit may act on.
	IncludeGenerated bool

	// PublishedAPI states that the target is a library, whose published API a
	// program outside the analysis can call ([publishedConversions]).
	PublishedAPI bool
}

// Delimiters are the pair that opens and closes an action of a template, as the
// project that renders the templates sets it.
type Delimiters struct {
	Left  string
	Right string
}

// Exemption is one symbol an exemption class holds back, and why. Class is the
// vocabulary's spelling of the class. Site is the evidence's position, rendered as
// every inventory position is: target-relative, solidus-separated, UTF-16 columns. A
// class whose evidence is a type relation rather than a source site leaves Site
// and Detail empty.
type Exemption struct {
	ID     inventory.SymbolID
	Class  string
	Detail string // one short clause naming the evidence

	// Holder is the declaration whose code holds the evidence, where the class
	// retains as a use by that declaration: the symbol is then live only while
	// the holder is, and falls into the holder's dead component with it. A root
	// the exemption names is held by the holder instead. Empty, the exemption
	// holds the symbol live whatever else is.
	Holder inventory.SymbolID

	// CoHolders are the further declarations holding the evidence where the class
	// derives it from sites several declarations hold. The exemption is then a
	// use by each holder, and it holds the symbol reachable only while the holder
	// and every co-holder are.
	CoHolders []inventory.SymbolID

	// Via is the interface method a retained method answers, where the class
	// retains through an interface the inventory declares.
	Via inventory.SymbolID

	// Consumer is the module path of the loaded consumer whose file holds the
	// evidence, empty for a file of the target. Site is relative to that module's
	// root.
	Consumer string

	// Confidence is empty where the exemption holds the symbol. Possible, it holds
	// nothing, and a finding about the symbol is reported at the class possible: a
	// callee outside the program fills the field from a document and may read it, or
	// an encoder emits the field though nothing writes it.
	Confidence vocab.Class

	// File is the path of the file that writes the evidence, relative to the root
	// Site renders against, which a line directive does not move: what classifies
	// the evidence as a test's or a generated file's.
	File string

	Site token.Position // after every pointer field, so the counted fields of a position end the pointer data

	// Build is the build that compiles the file holding the evidence
	// ([resolve.Resolver.Build]).
	Build load.Build
}

// Fact is what one exemption states, with the site left out: this class holds this
// symbol for this reason while these holders are live. A class that finds the same
// fact at several sites found it once, and the sweep records it once.
type Fact struct {
	id        inventory.SymbolID
	class     string
	detail    string
	holder    inventory.SymbolID
	coHolders string
}

// Fact is the fact the exemption states.
func (e *Exemption) Fact() Fact {
	return Fact{id: e.ID, class: e.Class, detail: e.Detail, holder: e.Holder, coHolders: joinedIDs(e.CoHolders)}
}

// joinedIDs spells a list of identifiers as one comparable value.
func joinedIDs(ids []inventory.SymbolID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = string(id)
	}
	return strings.Join(parts, "\x00")
}

// Loaded is one loaded configuration as the composition root hands it over: the
// declarations enumerated from it, the resolver that maps a type-checked object back
// to one of them, and the configured half.
type Loaded struct {
	// Result is the loaded configuration every class type-checks against.
	Result *load.Result

	// Resolve maps a types.Object or a position to the declaration written
	// there, which is how a class names the symbol it retains.
	Resolve *resolve.Resolver

	// Facts is the configuration's syntax in the walk order, its generated files
	// and what its published API reaches.
	Facts *facts.Index

	// Symbols is the inventory, for a class that reads a declaration's own
	// properties rather than resolving an object.
	Symbols []inventory.Symbol

	// Root is the target root: what a scanned path is joined onto and what a
	// recorded site is rendered relative to.
	Root string

	// Read is how a class that reads a file reads it. A class reaches the
	// filesystem through this and nowhere else.
	Read inventory.ReadFile

	Options Options

	// Mode is the run's reference mode, the one value the composition root decided
	// for every stage. Under a production mode an exemption whose evidence is
	// written in a test file holds nothing: a test that marshals a value or
	// compares one makes no member of it live for production, exactly as a test's
	// reference is no reference there.
	Mode facts.Mode
}

// Program is what every class reads: one loaded configuration and the facts about
// it several classes share, computed by [NewProgram] before any class runs, so no
// class depends on another having run. The one exception is assert, which
// memoises a pure answer per outside package on first ask, so the classes must
// run on one goroutine.
type Program struct {
	// conversions is every site where a value of a concrete type reaches a
	// position typed as an interface ([facts.Conversions]); a site converting an
	// interface value is the interface kinds' subject rather than an exemption's.
	conversions []facts.Conversion

	// allConversions is every site, a site converting an interface value
	// included, which is what a chain of sites between two interfaces is read from.
	allConversions []facts.Conversion

	// programConversions is conversions followed by every concrete site each
	// loaded consumer writes, in the order the scope declares the consumers.
	programConversions []facts.Conversion

	// published is [publishedConversions]' sites.
	published []facts.Conversion

	// outside is [facts.Index.Outside].
	outside []*types.Package

	// assertions are the configuration's satisfaction assertions ([satisfactionAssertions]).
	assertions []span

	// functions is every function the program declares ([programFunctions]).
	functions []programFunction

	// assert is what the packages outside the program can assert, read from their
	// source per package on a class's first ask, because most are never asked.
	assert *assertable

	// sources reads the packages outside the program from their source. The
	// program owns it, so the trees it read go when the program does.
	sources *load.Sources

	Loaded
}

// NewProgram computes the shared facts of one loaded configuration.
func NewProgram(l *Loaded) (*Program, error) {
	if l.Result == nil || l.Result.Fset == nil {
		return nil, fmt.Errorf("%w: no file set", inventory.ErrIncompleteLoad)
	}
	p := &Program{Loaded: *l, sources: l.Result.Sources()}
	p.allConversions = facts.Conversions(p.Resolve.Positions(), p.Result.Packages)
	p.conversions = concrete(p.allConversions)
	p.programConversions = slices.Clone(p.conversions)
	for _, consumer := range p.Result.Referencing() {
		p.programConversions = append(p.programConversions, concrete(facts.Conversions(p.Resolve.Positions(), consumer.Packages))...)
	}
	p.published = publishedConversions(p)
	p.functions = programFunctions(p)
	p.outside = p.Facts.Outside()
	p.assert = newAssertable(p)
	p.assertions = satisfactionAssertions(p)
	return p, nil
}

// Detector is one class's detection over one loaded configuration. It returns
// every symbol the class retains, each carrying the class's own spelling, and
// fails only where the evidence it needs is unreadable.
type Detector func(in *Program) ([]Exemption, error)

// Computed is what the classes found over one program.
type Computed struct {
	Held []Exemption

	// TestEvidence is the exemptions found in a test file under a production run,
	// which retain nothing and count as test references.
	TestEvidence []Exemption

	// Filled is the records of the class possible whose symbol no held exemption
	// holds, each found in a file the mode counts or naming a symbol a test file
	// declares, whose finding a test file's evidence may lower under any mode.
	Filled []Exemption
}

// Compute runs every class the detector table holds and the options do not disable,
// in vocabulary order; a class the table does not hold contributes nothing. Each
// result is ordered by site, class and symbol and holds one entry per distinct
// symbol, class, detail and holder at its first site, so a method one declaration
// converts to one interface at sixty-nine sites is one entry. Under a production run an exemption
// found in a test file is returned as test evidence rather than held: it retains
// nothing and counts as a test reference, and a fact a source file also carries
// stays the source file's held entry.
func Compute(in *Program, detectors map[vocab.Exemption]Detector) (Computed, error) {
	disabled := make(map[vocab.Exemption]bool, len(in.Options.Disabled))
	for _, class := range in.Options.Disabled {
		disabled[class] = true
	}

	testDeclared := testDeclarations(in.Symbols)
	var found, evidence, filled []Exemption
	for _, class := range goClasses() {
		detect, carried := detectors[class]
		if disabled[class] || !carried {
			continue
		}
		retained, detectErr := detect(in)
		if detectErr != nil {
			return Computed{}, fmt.Errorf("exempt: %s: %w", class, detectErr)
		}
		for i := range retained {
			switch e := &retained[i]; sortRecord(e, in.Mode, testDeclared) {
			case recordHeld:
				found = append(found, *e)
			case recordTestEvidence:
				evidence = append(evidence, *e)
			case recordFilled:
				filled = append(filled, *e)
			}
		}
	}

	held := make(map[inventory.SymbolID]bool, len(found))
	for i := range found {
		held[found[i].ID] = true
	}
	filled = slices.DeleteFunc(filled, func(e Exemption) bool { return held[e.ID] })
	for _, set := range [][]Exemption{found, evidence, filled} {
		slices.SortStableFunc(set, byEvidence)
	}
	return Computed{Held: firstPerFact(found), TestEvidence: firstPerFact(evidence), Filled: firstPerFact(filled)}, nil
}

// recordSet is which part of [Computed] one record of a class goes to, if any.
type recordSet uint8

const (
	recordDropped recordSet = iota
	recordHeld
	recordTestEvidence
	recordFilled
)

// sortRecord is the part of [Computed] one record goes to under mode m: a record of
// the class possible goes to Filled where the mode counts its file or a test file
// declares its symbol, testDeclared being those symbols.
func sortRecord(e *Exemption, m facts.Mode, testDeclared map[inventory.SymbolID]bool) recordSet {
	switch counted := holdsInMode(e, m); {
	case e.Confidence != vocab.Possible && counted:
		return recordHeld
	case e.Confidence != vocab.Possible:
		return recordTestEvidence
	case counted || testDeclared[e.ID]:
		return recordFilled
	default:
		return recordDropped
	}
}

// testDeclarations is the symbols a test file declares.
func testDeclarations(symbols []inventory.Symbol) map[inventory.SymbolID]bool {
	declared := make(map[inventory.SymbolID]bool)
	for i := range symbols {
		if symbols[i].InTestFile {
			declared[symbols[i].ID] = true
		}
	}
	return declared
}

// firstPerFact keeps one exemption per distinct fact, the first of each in the
// order given, which after the sort by evidence is the one at the earliest
// rendered site.
func firstPerFact(found []Exemption) []Exemption {
	seen := make(map[Fact]bool, len(found))
	kept := found[:0]
	for ix := range found {
		e := &found[ix]
		stated := e.Fact()
		if seen[stated] {
			continue
		}
		seen[stated] = true
		kept = append(kept, *e)
	}
	return kept
}

// byEvidence orders two exemptions by the site the evidence was found at, then by
// the class, then by the symbol retained.
//
//nolint:gocritic // slices.SortStableFunc fixes a comparator's parameters to values.
func byEvidence(a, b Exemption) int {
	if c := bySite(&a, &b); c != 0 {
		return c
	}
	if c := strings.Compare(a.Class, b.Class); c != 0 {
		return c
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

// bySite orders two exemptions by the site their evidence was found at: the
// target's files first, then each consumer's by module path.
func bySite(a, b *Exemption) int {
	if c := strings.Compare(a.Consumer, b.Consumer); c != 0 {
		return c
	}
	return inventory.ByPosition(a.Site, b.Site)
}

// resolveObject returns the declaration an identifier or a selector expression
// names, read from the type information rather than from the spelling of the
// expression, so a local name for a package or a value whose type is an alias
// names the same declaration. An instantiation of a generic function or method
// names the declaration it instantiates.
//
// It is the whole of the package's ident-or-selector resolution: a class that
// needs a function, a constant or a key derives it from the object this returns.
func resolveObject(info *types.Info, expr ast.Expr) types.Object {
	switch e := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return info.Uses[e]
	case *ast.SelectorExpr:
		return info.Uses[e.Sel]
	case *ast.IndexExpr:
		return resolveObject(info, e.X)
	case *ast.IndexListExpr:
		return resolveObject(info, e.X)
	}
	return nil
}
