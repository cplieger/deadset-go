package scope

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// namesChecked is whether [Root.Parse] fails the run on a file parsed by a name
// [Root.Name] did not give it, or named two ways: in every test binary, and in a
// command built with the deadset_names tag.
var namesChecked = testing.Testing()

// Name is the one name the run gives the file at path, whatever spelling reached
// it: its directory resolved ([resolvePath]) and its own name kept, which inside
// the root is the root's own spelling joined with that path relative to the
// resolved root, and elsewhere the resolved path itself. Two spellings of one file
// through directory links, inside the root or outside it, are one name, so every
// reading of the file writes each declaration at one position. A file that is
// itself a link keeps its own name, as the toolchain compiles it as a file of its
// directory and dev.50 renders it there. Name gives a name its own name back. The
// zero Root names every file by its resolved directory.
func (r Root) Name(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if r.at == nil {
		parent := filepath.Dir(abs)
		return filepath.Join(resolvePath(parent), filepath.Base(abs))
	}
	entry := r.entryPath(abs)
	if relative, inside := lexicallyWithin(r.at.resolved, entry); inside {
		return filepath.Join(r.at.path, filepath.FromSlash(relative))
	}
	return entry
}

// Parse parses the Go file at path into fset, reading it where src is nil. It is
// the run's one parse of a file whose positions a result keeps, and path is the
// file's name ([Root.Name]): where names are checked ([namesChecked]), Parse panics
// on a path that is not its own name, which a discovery left unnamed, and on one
// file, where its directory resolves, parsed under two names.
func (r Root) Parse(fset *token.FileSet, path string, src []byte, mode parser.Mode) (*ast.File, error) {
	if namesChecked {
		r.saw(path)
	}
	if src == nil {
		return parser.ParseFile(fset, path, nil, mode)
	}
	return parser.ParseFile(fset, path, src, mode)
}

// saw records that a parse named a file path, and panics where path is not the
// name [Root.Name] gives it or the file was parsed under another name before.
func (r Root) saw(path string) {
	name := r.Name(path)
	if path != name {
		panic(fmt.Sprintf("scope: %s is parsed by a name the run did not give it, %s being its name", path, name))
	}
	if r.at == nil {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	file := r.entryPath(abs)
	r.at.mu.Lock()
	defer r.at.mu.Unlock()
	if held, seen := r.at.names[file]; seen && held != name {
		panic(fmt.Sprintf("scope: %s is parsed as %s and as %s", file, held, name))
	}
	r.at.names[file] = name
}

// ParseText parses src, a Go file read for what it declares or imports, under no
// name and into a file set of its own: no position of the tree is kept, so no name
// can disagree with another.
func ParseText(src []byte, mode parser.Mode) (*ast.File, error) {
	return parser.ParseFile(token.NewFileSet(), "", src, mode)
}
