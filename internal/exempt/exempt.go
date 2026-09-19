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
	"go/token"
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

// classes is the vocabulary in its own order, which is the order Compute runs the
// classes in.
var classes = [...]Class{
	InterfaceSatisfaction,
	EncodingReflection,
	FormatVerbContract,
	ErrorsDuckTyping,
	EnumGroup,
	GeneratedFile,
	LinknameCgoAsmPlugin,
	TemplateField,
	ReflectiveLookup,
}

// Classes lists every class of the vocabulary, in vocabulary order.
func Classes() []Class { return slices.Clone(classes[:]) }

// Options is the configured half of an exemption run: what the maintainer turned
// off, and the settings the two classes that read files need. Nothing else about
// the configuration reaches a class.
type Options struct {
	// Disabled are the classes that do not run, so that an exemption suspected
	// of hiding a defect can be tested. A class named twice is disabled once,
	// and a name outside the vocabulary disables nothing.
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

// Compute runs every class of the vocabulary the detector table holds and the
// options do not disable, in vocabulary order, and returns the union.
//
// The result is ordered by site, then by class, then by symbol, and holds one
// entry per distinct symbol, class and detail, carrying the first site by that
// order: a method a program converts to one interface at sixty-nine sites is one
// entry naming the earliest of them, and the same method converted to a second
// interface is a second entry, because the detail is what differs. A class the
// table does not hold contributes nothing, so a caller assembling the table from
// the classes it has needs no placeholder for the ones it does not.
//
// Under a production run an exemption found in a test file is dropped before the
// union, so the same fact found in a source file and in a test file is the source
// file's entry and the same fact found only in a test file is no entry at all.
func Compute(in *Input, detectors map[Class]Detector) ([]graph.Exemption, error) {
	disabled := make(map[Class]bool, len(in.Options.Disabled))
	for _, class := range in.Options.Disabled {
		disabled[class] = true
	}

	var found []graph.Exemption
	for _, class := range classes {
		detect, held := detectors[class]
		if disabled[class] || !held {
			continue
		}
		retained, err := detect(in)
		if err != nil {
			return nil, fmt.Errorf("exempt: %s: %w", class, err)
		}
		found = append(found, in.holding(retained)...)
	}

	slices.SortStableFunc(found, byEvidence)
	return firstPerFact(found), nil
}

// holding keeps the exemptions of one class whose evidence holds under the run's
// reference mode, which the boundary's own predicate decides: the crossing of
// evidence out of a production run is one of the five the boundary states, so the
// rule is written there and read here.
func (in *Input) holding(found []graph.Exemption) []graph.Exemption {
	kept := found[:0]
	for _, e := range found {
		if !holdsInMode(e.Site, in.Mode) {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// fact is what one exemption states, with the site left out: this class holds
// this symbol for this reason. A class that finds the same fact at several sites
// found it once.
type fact struct {
	id     graph.SymbolID
	class  string
	detail string
}

// firstPerFact keeps one exemption per distinct fact, the first of each in the
// order given, which after the sort by evidence is the one at the earliest
// rendered site.
func firstPerFact(found []graph.Exemption) []graph.Exemption {
	seen := make(map[fact]bool, len(found))
	kept := found[:0]
	for _, e := range found {
		stated := fact{id: e.ID, class: e.Class, detail: e.Detail}
		if seen[stated] {
			continue
		}
		seen[stated] = true
		kept = append(kept, e)
	}
	return kept
}

// byEvidence orders two exemptions by the site the evidence was found at, then by
// the class, then by the symbol retained.
//
//nolint:gocritic // slices.SortStableFunc fixes a comparator's parameters to values.
func byEvidence(a, b graph.Exemption) int {
	if c := graph.ByPosition(a.Site, b.Site); c != 0 {
		return c
	}
	if c := strings.Compare(a.Class, b.Class); c != 0 {
		return c
	}
	return strings.Compare(string(a.ID), string(b.ID))
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

// Conversion is one site at which a value of a concrete type reaches a position
// typed as an interface: an assertion, an assignment, an argument, a return value
// or an element stored in an interface-typed container.
//
// The set of these sites is what narrows interface satisfaction from every type
// that could implement an interface to the types a program actually converts, and
// three classes read it: the methods a conversion's interface requires, the error
// helpers' duck typing over a conversion to error, and the string and error
// methods a formatting verb calls.
type Conversion struct {
	// From is the concrete type converted, with the pointer stripped where the
	// site strips it.
	From types.Type

	// To is the interface type reached.
	To *types.Interface

	// Site is where the conversion is written, unrendered: a class records the
	// site it publishes through the resolver.
	Site token.Pos
}
