// Package exempt computes the exemptions of one loaded build configuration: the
// classes of reason for which a symbol the reference graph alone would report is
// live all the same.
//
// A class is policy over type information and the class produces a set of
// retained symbols; the graph is the mechanism that consumes them, and the value
// that crosses the boundary is a graph.Exemption. So this package reads the graph
// and never the configuration: the composition root converts every setting into
// Options, the way it converts every other setting for every other stage.
//
// One file holds one class, and the framework names none of them: a caller
// assembles the classes it has into the detector table Compute runs, so a class
// that is not implemented is a class that produces nothing rather than a compile
// failure.
package exempt

import (
	"fmt"
	"go/ast"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

// Class names one exemption class of the vocabulary. The spelling is the one a
// finding's retained record carries and the one a maintainer disables by.
type Class string

// The classes a Go analysis computes, in vocabulary order.
const (
	InterfaceSatisfaction Class = "interface-satisfaction"
	EncodingReflection    Class = "encoding-reflection"
	FormatVerbContract    Class = "format-verb-contract"
	ErrorsDuckTyping      Class = "errors-duck-typing"
	EnumGroup             Class = "enum-group"
	GeneratedFile         Class = "generated-file"
	LinknameCgoAsmPlugin  Class = "linkname-cgo-asm-plugin"
	TemplateField         Class = "template-field"
	ReflectiveLookup      Class = "reflective-lookup"
)

// goLanguage is the spelling the vocabulary gives this analyzer's language.
const goLanguage = "go"

// classRow is one class of the vocabulary and the languages it runs on.
type classRow struct {
	class     Class
	languages []string
}

// vocabulary is every class of the vocabulary, in its order, the classes another
// language's analyzer computes included. The rows are written here rather than
// decoded from the vocabulary document at run time, and a test pins them equal to
// the document.
var vocabulary = []classRow{
	{class: InterfaceSatisfaction, languages: []string{"go", "ts"}},
	{class: EncodingReflection, languages: []string{"go"}},
	{class: FormatVerbContract, languages: []string{"go"}},
	{class: ErrorsDuckTyping, languages: []string{"go"}},
	{class: EnumGroup, languages: []string{"go", "ts"}},
	{class: GeneratedFile, languages: []string{"go", "ts"}},
	{class: LinknameCgoAsmPlugin, languages: []string{"go"}},
	{class: TemplateField, languages: []string{"go", "ts"}},
	{class: ReflectiveLookup, languages: []string{"go", "ts"}},
	{class: "decorator", languages: []string{"ts"}},
	{class: "injection-container", languages: []string{"ts"}},
	{class: "framework-lifecycle", languages: []string{"ts"}},
	{class: "serialization-contract", languages: []string{"ts"}},
}

// Vocabulary lists every class of the vocabulary, in its order, which is every
// name a configuration may disable.
func Vocabulary() []Class {
	names := make([]Class, len(vocabulary))
	for i, row := range vocabulary {
		names[i] = row.class
	}
	return names
}

// Classes lists the classes a Go analysis computes, in vocabulary order, which is
// the order Compute runs them in.
func Classes() []Class {
	computed := make([]Class, 0, len(vocabulary))
	for _, row := range vocabulary {
		if slices.Contains(row.languages, goLanguage) {
			computed = append(computed, row.class)
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
	Disabled []Class

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
}

// Delimiters are the pair that opens and closes an action of a template, as the
// project that renders the templates sets it.
type Delimiters struct {
	Left  string
	Right string
}

// Input is what every class reads: one loaded configuration, the declarations
// enumerated from it, the resolver that maps a type-checked object back to one of
// them, and the configured half.
type Input struct {
	// Result is the loaded configuration every class type-checks against.
	Result *load.Result

	// Resolve maps a types.Object or a position to the declaration written
	// there, which is how a class names the symbol it retains.
	Resolve *graph.Resolver

	// conversions is the conversion set [Conversions] computed first, which every
	// later class reads rather than walking the program again.
	conversions *[]graph.Conversion

	// assert is what the packages outside the program can assert, read from their
	// source on first use and shared by every class that asks ([assertableOf]).
	assert *assertable

	// Symbols is the inventory, for a class that reads a declaration's own
	// properties rather than resolving an object.
	Symbols []graph.Symbol

	// Root is the target root: what a scanned path is joined onto and what a
	// recorded site is rendered relative to.
	Root string

	// Read is how a class that reads a file reads it. A class reaches the
	// filesystem through this and nowhere else.
	Read graph.ReadFile

	Options Options

	// Mode is the run's reference mode, the one value the composition root decided
	// for every stage. Under a production mode an exemption whose evidence is
	// written in a test file holds nothing: a test that marshals a value or
	// compares one makes no member of it live for production, exactly as a test's
	// reference is no reference there.
	Mode graph.Mode
}

// Detector is one class's detection over one loaded configuration. It returns
// every symbol the class retains, each carrying the class's own spelling, and
// fails only where the evidence it needs is unreadable.
type Detector func(in *Input) ([]graph.Exemption, error)

// Compute runs every class the detector table holds and the options do not disable,
// in vocabulary order; a class the table does not hold contributes nothing. Each
// result is ordered by site, class and symbol and holds one entry per distinct
// symbol, class, detail and holder at its first site, so a method one declaration
// converts to one interface at sixty-nine sites is one entry. Under a production run an exemption
// found in a test file is returned as test evidence rather than held: it retains
// nothing and counts as a test reference, and a fact a source file also carries
// stays the source file's held entry.
func Compute(in *Input, detectors map[Class]Detector) (held, testEvidence []graph.Exemption, err error) {
	disabled := make(map[Class]bool, len(in.Options.Disabled))
	for _, class := range in.Options.Disabled {
		disabled[class] = true
	}

	var found, evidence []graph.Exemption
	for _, class := range Classes() {
		detect, carried := detectors[class]
		if disabled[class] || !carried {
			continue
		}
		retained, detectErr := detect(in)
		if detectErr != nil {
			return nil, nil, fmt.Errorf("exempt: %s: %w", class, detectErr)
		}
		for i := range retained {
			e := &retained[i]
			if holdsInMode(e, in.Mode) {
				found = append(found, *e)
			} else {
				evidence = append(evidence, *e)
			}
		}
	}

	slices.SortStableFunc(found, byEvidence)
	slices.SortStableFunc(evidence, byEvidence)
	return firstPerFact(found), firstPerFact(evidence), nil
}

// fact is what one exemption states, with the site left out: this class holds
// this symbol for this reason while this holder is live. A class that finds the
// same fact at several sites found it once.
type fact struct {
	id     graph.SymbolID
	class  string
	detail string
	holder graph.SymbolID
}

// firstPerFact keeps one exemption per distinct fact, the first of each in the
// order given, which after the sort by evidence is the one at the earliest
// rendered site.
func firstPerFact(found []graph.Exemption) []graph.Exemption {
	seen := make(map[fact]bool, len(found))
	kept := found[:0]
	for ix := range found {
		e := &found[ix]
		stated := fact{id: e.ID, class: e.Class, detail: e.Detail, holder: e.Holder}
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
func byEvidence(a, b graph.Exemption) int {
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
func bySite(a, b *graph.Exemption) int {
	if c := strings.Compare(a.Consumer, b.Consumer); c != 0 {
		return c
	}
	return graph.ByPosition(a.Site, b.Site)
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
