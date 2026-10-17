// Package deps answers what a target's dependency declarations and its module
// graph say: which direct requirement no import needs, which replace directive
// redirects nothing, and which modules a deletion candidate holds the last use
// of.
//
// The module file is the one the load's topology read with x/mod's modfile, the
// parser the toolchain vendors. Nothing here reports anything: the findings pass reads
// [UnusedRequirements], [NoopReplacements] and [LastUses]. Every answer is one
// build configuration's, so the caller intersects the configurations it ran, as
// it does for a symbol.
package deps

import (
	"bytes"
	"go/token"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"golang.org/x/mod/modfile"
)

// modFileName is the module file's name, which is also the file name a position
// in it carries.
const modFileName = "go.mod"

// Module names one module: the path a directive spells and the version it gives
// it. The version is empty where the directive omits one, and where a
// replacement is a directory rather than a module, Path is that directory as the
// file spells it.
type Module struct {
	Path    string
	Version string
}

// Requirement is one require directive.
type Requirement struct {
	Path string

	Site     token.Position // the directive's first module path in the module file
	Indirect bool           // the directive carries the indirect comment
}

// Replacement is one replace directive: the module it replaces, and what it
// replaces that module with.
type Replacement struct {
	Old  Module
	New  Module
	Site token.Position // the directive's first module path in the module file
}

// File is the module file's directives, each carrying the position of the first
// module path it writes.
//
// The three members are the ones a rule here decides: an exclude states which
// version not to select, which is a counterfactual about a resolution that did not
// happen, and a retract, a godebug and an ignore say nothing about whether a
// dependency is used.
type File struct {
	Requires []Requirement
	Replaces []Replacement

	// Tools are the package paths the tool directives name. A tool directive is
	// never reported: its invocation is a command outside the module graph and
	// the source. It is read all the same, because the toolchain builds that
	// package from the module the directives select, so a replace of that module
	// is not a no-op.
	Tools []string
}

// Directives is the directives of one module file, in the order it writes them.
func Directives(m *load.ModuleFile) File {
	f := File{
		Requires: make([]Requirement, 0, len(m.Syntax.Require)),
		Replaces: make([]Replacement, 0, len(m.Syntax.Replace)),
		Tools:    make([]string, 0, len(m.Syntax.Tool)),
	}
	for _, r := range m.Syntax.Require {
		f.Requires = append(f.Requires, Requirement{Path: r.Mod.Path, Indirect: r.Indirect, Site: site(m.Text, r.Syntax)})
	}
	for _, r := range m.Syntax.Replace {
		f.Replaces = append(f.Replaces, Replacement{
			Old:  Module{Path: r.Old.Path, Version: r.Old.Version},
			New:  Module{Path: r.New.Path, Version: r.New.Version},
			Site: site(m.Text, r.Syntax),
		})
	}
	for _, t := range m.Syntax.Tool {
		f.Tools = append(f.Tools, t.Path)
	}
	return f
}

// site is the position of the first module path one directive line writes, its
// column counted in UTF-16 code units by [graph.Column], which is the unit every
// position of a report carries. A line inside a block opens with that path; a line
// of its own opens with the directive's keyword, which the path follows.
func site(text []byte, line *modfile.Line) token.Position {
	at := line.Start.Byte
	if !line.InBlock && len(line.Token) > 1 {
		at += len(line.Token[0])
		for at < len(text) && (text[at] == ' ' || text[at] == '\t') {
			at++
		}
	}
	start := bytes.LastIndexByte(text[:at], '\n') + 1
	return token.Position{Filename: modFileName, Line: line.Start.Line, Column: graph.Column(text[start:], at-start)}
}
