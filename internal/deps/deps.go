// Package deps answers what a target's dependency declarations and its module
// graph say: which direct requirement no import needs, which replace directive
// redirects nothing, and which modules a deletion candidate holds the last use
// of.
//
// The module file is read through the same toolchain that loaded the packages, by
// running `go mod edit -json` in the target root, so this package and the `go`
// command cannot disagree about what the file means: the grammar, the defaults
// and every normalisation are the toolchain's, and a directive reasoned about
// here is one the toolchain reported. Reading the file in process would be a
// second reading of the same grammar. That output carries no position, so the
// file's own text is read once more for the line each directive is written on.
//
// Nothing here reports anything. The findings pass reads [UnusedRequirements] for
// the unused-dependency kind, [NoopReplacements] for the module-directive kind,
// and [LastUses] for the dependency a deletion finding names as losing its last
// use.
//
// Every answer is one build configuration's, because a load is one build
// configuration: a requirement a second configuration imports is not unused, and
// a module a second configuration loads is in the build list. The caller
// intersects the configurations it ran, as it does for a symbol.
package deps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
)

// modFileName is the module file's name, which is also the file name a position
// in it carries.
const modFileName = "go.mod"

// goCommand is the toolchain, resolved on the path the load resolves it on.
const goCommand = "go"

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
	Path    string
	Version string

	Site     token.Position // the directive's line in the module file
	Indirect bool           // the directive carries the indirect comment
}

// Replacement is one replace directive: the module it replaces, and what it
// replaces that module with.
type Replacement struct {
	Old  Module
	New  Module
	Site token.Position // the directive's line in the module file
}

// File is the module file's directives, each carrying the line it is written on.
//
// The three members are the ones a rule here decides. The toolchain prints the
// whole file, so a directive absent from this shape is one nothing reads: an
// exclude states which version not to select, which is a counterfactual about a
// resolution that did not happen, and a retract, a godebug and an ignore say
// nothing about whether a dependency is used.
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

// ModuleFile reads the module file of the module rooted at root. Cancelling ctx
// stops the toolchain it runs.
func ModuleFile(ctx context.Context, root string) (File, error) {
	printed, err := printModuleFile(ctx, root)
	if err != nil {
		return File{}, err
	}

	// Unknown members are ignored rather than refused: the toolchain prints every
	// directive of the file and may gain one, and a document this shape cannot
	// hold whole is still a document every rule here can read.
	var document modDocument
	if decoded := json.Unmarshal(printed, &document); decoded != nil {
		return File{}, fmt.Errorf("deps: decode the module file of %s: %w", root, decoded)
	}

	text, err := os.ReadFile(filepath.Join(root, modFileName))
	if err != nil {
		return File{}, fmt.Errorf("deps: read the module file of %s: %w", root, err)
	}
	return document.file(scan(text)), nil
}

// printModuleFile runs the toolchain that prints the module file as it reads it.
// The two settings it pins are the two the load pins for the same reason: an
// ambient workspace or an ambient flag list reaching the child would make one
// target's answer depend on where the run was started.
func printModuleFile(ctx context.Context, root string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, goCommand, "mod", "edit", "-json")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")

	var printed, diagnostic bytes.Buffer
	cmd.Stdout = &printed
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("deps: read the module file of %s: %s: %w", root, trimmed(&diagnostic), err)
	}
	return printed.Bytes(), nil
}

// trimmed renders what the toolchain wrote to its diagnostic stream as one line
// of a message, and says so when it wrote nothing.
func trimmed(diagnostic *bytes.Buffer) string {
	text := string(bytes.TrimSpace(diagnostic.Bytes()))
	if text == "" {
		return "no diagnostic"
	}
	return text
}

// modDocument mirrors the members of the printed module file this package reads.
type modDocument struct {
	Require []modRequire
	Replace []modReplace
	Tool    []modTool
}

// modRequire is one printed require directive.
type modRequire struct {
	Path     string
	Version  string
	Indirect bool
}

// modReplace is one printed replace directive.
type modReplace struct {
	Old modModule
	New modModule
}

// modModule is one module a printed directive names.
type modModule struct {
	Path    string
	Version string
}

// modTool is one printed tool directive.
type modTool struct {
	Path string
}

// file renders the printed document as the file the rules read, giving every
// directive the line the scan found for it.
func (d *modDocument) file(s sites) File {
	f := File{
		Requires: make([]Requirement, 0, len(d.Require)),
		Replaces: make([]Replacement, 0, len(d.Replace)),
		Tools:    make([]string, 0, len(d.Tool)),
	}
	for _, require := range d.Require {
		module := Module{Path: require.Path, Version: require.Version}
		f.Requires = append(f.Requires, Requirement{
			Path:     require.Path,
			Version:  require.Version,
			Indirect: require.Indirect,
			Site:     s.require(module),
		})
	}
	for _, replace := range d.Replace {
		key := replaceKey{
			old:         Module{Path: replace.Old.Path, Version: replace.Old.Version},
			replacement: Module{Path: replace.New.Path, Version: replace.New.Version},
		}
		f.Replaces = append(f.Replaces, Replacement{
			Old:  key.old,
			New:  key.replacement,
			Site: s.replace(key),
		})
	}
	for _, tool := range d.Tool {
		f.Tools = append(f.Tools, tool.Path)
	}
	return f
}
