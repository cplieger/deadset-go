// Package conventions is the closed set of rule shapes a Go tool's convention
// takes, and the interpreter of each. A shape answers what its rule names in one
// file's syntax; which tool a rule belongs to, and the root kind it reports, is the
// row that holds it.
package conventions

import (
	"go/ast"
	"go/build/constraint"
	"go/types"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
)

// Rule is a pointer to one of the five shapes: [DocDirective], [GenerateInputs],
// [PackageGenerator], [SuiteRunner] and [TaggedEntry].
type Rule interface{ rule() }

// docTarget is what a doc-comment directive roots.
type docTarget uint8

const (
	// Functions roots a function or method whose doc comment holds the directive.
	Functions docTarget = iota
	// Declarations roots a declaration whose doc comment, or whose enclosing
	// declaration's, holds the directive, and every field of a struct type it
	// declares, an anonymous struct's included.
	Declarations
)

// DocDirective is a doc-comment line that opens with Prefix, followed by a space or
// a tab where Separated is set.
type DocDirective struct {
	Prefix    string
	Of        docTarget
	Separated bool
}

// inputSelect is how a go:generate directive names its generator's inputs.
type inputSelect uint8

const (
	// TypeFlag names, by its -type flag, types of the directive's own package.
	TypeFlag inputSelect = iota
	// SourceOrNames names the interfaces of the file its -source flag gives,
	// or else, by its last two positional arguments, a package and the names it
	// declares.
	SourceOrNames
)

// GenerateInputs is a go:generate directive running one of Programs, whose words
// name declarations the generator reads. BooleanFlags take no separate value.
type GenerateInputs struct {
	Programs     []string
	BooleanFlags []string
	Select       inputSelect
}

// PackageGenerator is a go:generate directive running one of Programs with
// Subcommand, whose positional arguments pass, by a relative path, packages whose
// exported declarations the generator reads by name.
type PackageGenerator struct {
	Subcommand string
	Programs   []string
}

// SuiteRunner is a call of Func of package Pkg that passes, as its argument at Arg,
// a value whose methods it calls by reflection: each whose name opens with one of
// Prefixes, and each that Names lists.
type SuiteRunner struct {
	Pkg, Func string
	Prefixes  []string
	Names     []string
	Arg       int
}

// TaggedEntry is an entry point a build that sets Tag calls: a function of type
// func([]byte) int whose name opens with Prefix as a test family's does, in a file
// whose build constraint holds with Tag set for some setting of its other tags.
type TaggedEntry struct {
	Tag, Prefix string
}

func (*DocDirective) rule()     {}
func (*GenerateInputs) rule()   {}
func (*PackageGenerator) rule() {}
func (*SuiteRunner) rule()      {}
func (*TaggedEntry) rule()      {}

// Holds reports whether doc holds a line the directive opens.
func (r *DocDirective) Holds(doc *ast.CommentGroup) bool {
	for line := range docLines(doc) {
		rest, ok := strings.CutPrefix(line, r.Prefix)
		if ok && (!r.Separated || rest != "" && (rest[0] == ' ' || rest[0] == '\t')) {
			return true
		}
	}
	return false
}

// Named is what one go:generate directive names as its generator's input: the
// interfaces of File, or Names in Package, where "." is the directive's own.
type Named struct {
	File    string
	Package string
	Names   []string
}

// Inputs is what words, one directive's, name, and false where the directive runs
// none of the rule's programs or names nothing.
func (r *GenerateInputs) Inputs(words []string) (Named, bool) {
	name, args := generatorOf(words)
	if !slices.Contains(r.Programs, name) {
		return Named{}, false
	}
	switch r.Select {
	case TypeFlag:
		return Named{Package: currentPackage, Names: listed(flagValue(args, typeFlag, nil))}, true
	default:
		if source := flagValue(args, sourceFlag, r.BooleanFlags); source != "" {
			return Named{File: source}, true
		}
		positional := positionalArgs(args, r.BooleanFlags)
		if len(positional) < 2 {
			return Named{}, false
		}
		return Named{Package: positional[len(positional)-2], Names: listed(positional[len(positional)-1])}, true
	}
}

// Packages is every relative path words, one directive's, pass to the rule's
// subcommand.
func (r *PackageGenerator) Packages(words []string) []string {
	name, args := generatorOf(words)
	if !slices.Contains(r.Programs, name) || len(args) == 0 || args[0] != r.Subcommand {
		return nil
	}
	var held []string
	for _, arg := range positionalArgs(args[1:], nil) {
		if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
			held = append(held, arg)
		}
	}
	return held
}

// Runs reports whether fn is the runner's function.
func (r *SuiteRunner) Runs(fn *types.Func) bool {
	return fn.Pkg() != nil && fn.Pkg().Path() == r.Pkg && fn.Name() == r.Func
}

// Calls reports whether the runner calls a method of that name.
func (r *SuiteRunner) Calls(method string) bool {
	for _, prefix := range r.Prefixes {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	return slices.Contains(r.Names, method)
}

// Compiles reports whether a build that sets the rule's tag may compile f: its
// build constraint holds with the tag set, for some setting of its other tags. A
// file whose constraint names no such tag compiles either way.
func (r *TaggedEntry) Compiles(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			if expr, err := constraint.Parse(c.Text); err == nil && !buildconstraint.HoldsWith(expr, r.Tag, true) {
				return false
			}
		}
	}
	return true
}

// Entry reports whether fn, which info defines, is an entry point of the rule.
func (r *TaggedEntry) Entry(info *types.Info, fn *ast.FuncDecl) bool {
	obj, _ := info.Defs[fn.Name].(*types.Func)
	if obj == nil || fn.Recv != nil || !NamePrefixed(fn.Name.Name, r.Prefix) || fn.Type.TypeParams.NumFields() > 0 {
		return false
	}
	sig, _ := obj.Type().(*types.Signature)
	if sig == nil || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	slice, _ := types.Unalias(sig.Params().At(0).Type()).(*types.Slice)
	return slice != nil && types.Identical(slice.Elem(), types.Typ[types.Byte]) &&
		types.Identical(sig.Results().At(0).Type(), types.Typ[types.Int])
}

// NamePrefixed reports whether name is prefix or prefix followed by something that
// is not a lower-case letter, the toolchain's rule for a test family's name, so
// Testing is not a test.
func NamePrefixed(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	first, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(first)
}

// listed is each comma-separated name of names, spaces trimmed.
func listed(names string) []string {
	var held []string
	for name := range strings.SplitSeq(names, ",") {
		held = append(held, strings.TrimSpace(name))
	}
	return held
}
