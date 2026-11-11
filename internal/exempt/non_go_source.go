package exempt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"golang.org/x/tools/go/packages"
)

// nonGoExtensions are the file name endings of the sources in another language
// than Go that a package directory can hold for some build configuration: the
// go/build source lists (assembly, C, C++, Objective-C, Fortran, SWIG, headers)
// and system objects.
var nonGoExtensions = [...]string{
	".s", ".S", ".sx", ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx",
	".m", ".f", ".F", ".for", ".f90", ".swig", ".swigcxx", ".syso",
}

// The directories the tree search of non-Go sources does not enter.
var nonGoSkippedDirs = [...]string{".git", "node_modules", "vendor"}

// nonGoSources is the linkname-cgo-asm-plugin class's rule N over one
// configuration: which package directories of the target hold a source in another
// language than Go, and every identifier any such source under the target root
// writes. It reads the directories themselves rather than go list's file lists,
// because go list lists neither a file whose name starts with an underscore or a
// full stop nor, with cgo off, a cgo file's C sources.
type nonGoSources struct {
	in *Program

	// dirs is each package directory read so far to the first file of it that makes
	// it hold a non-Go source, or to "" when it holds none.
	dirs map[string]string

	// tokens is every identifier the target's non-Go sources write, to the first
	// such file that writes it.
	tokens map[string]string

	// unread is every directory and non-Go source under the target root the tree
	// search could not read, in the order it met them. Each may name any function
	// or variable, so the first counts as naming every one.
	unread  []string
	scanned bool
}

// nonGo is the program's rule N, computed on a class's first ask.
func (in *Program) nonGo() *nonGoSources {
	if in.nonGoSources == nil {
		in.nonGoSources = &nonGoSources{in: in, dirs: make(map[string]string)}
	}
	return in.nonGoSources
}

// holds reports whether the directory of one package of the target holds a source
// in another language than Go, or a Go file of it imports "C".
func (n *nonGoSources) holds(p *packages.Package) bool {
	return n.evidence(p) != ""
}

// evidence is the first file, in directory order, that makes the directory of one
// package hold a non-Go source, or "" when it holds none.
func (n *nonGoSources) evidence(p *packages.Package) string {
	dir := packageDir(p)
	if dir == "" {
		return ""
	}
	if file, decided := n.dirs[dir]; decided {
		return file
	}
	file := n.listed(dir)
	n.dirs[dir] = file
	return file
}

// listed reads one directory's own listing for the first file that makes it hold a
// non-Go source. A directory the run cannot list holds one, the directory itself
// standing as the evidence, and so does an entry of a source's name it cannot
// inspect: the rule retains where it cannot decide.
func (n *nonGoSources) listed(dir string) string {
	if n.in.ReadDir == nil {
		return ""
	}
	entries, err := n.in.ReadDir(dir)
	if err != nil {
		return dir
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		switch load.KindOf(path, entry.Type(), n.in.Stat) {
		case load.File:
			if nonGoName(name) || strings.HasSuffix(name, goSuffix) && n.importsC(path) {
				return path
			}
		case load.Unread:
			if nonGoName(name) || strings.HasSuffix(name, goSuffix) {
				return path
			}
		}
	}
	return ""
}

// failed records an entry the tree search could not read.
func (n *nonGoSources) failed(path string) {
	n.unread = append(n.unread, path)
}

// goSuffix is a Go source file's extension.
const goSuffix = ".go"

// importsC reports whether the Go file at path imports "C". A file the run cannot
// read or parse does: the rule retains where it cannot decide.
func (n *nonGoSources) importsC(path string) bool {
	src, err := n.in.Read(path)
	if err != nil {
		return true
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return true
	}
	return fileImportsC(f)
}

// fileImportsC reports whether one parsed Go file imports "C".
func fileImportsC(f *ast.File) bool {
	for _, imp := range f.Imports {
		if imp.Path != nil && imp.Path.Value == `"C"` {
			return true
		}
	}
	return false
}

// nonGoName reports whether a file of that name is a source in another language
// than Go, whatever its name starts with.
func nonGoName(name string) bool {
	for _, ext := range nonGoExtensions {
		if strings.HasSuffix(name, ext) && len(name) > len(ext) {
			return true
		}
	}
	return false
}

// packageDir is the directory one package's files are read from.
func packageDir(p *packages.Package) string {
	if p.Dir != "" {
		return filepath.Clean(p.Dir)
	}
	for _, files := range [][]string{p.GoFiles, p.CompiledGoFiles, p.OtherFiles, p.IgnoredFiles} {
		if len(files) > 0 {
			return filepath.Dir(files[0])
		}
	}
	return ""
}

// named reports whether a function or a variable of the target is named by a
// source in another language than Go: its package's directory holds one, or the
// tree search finds a source that names it ([nonGoSources.namer]).
func (n *nonGoSources) named(p *packages.Package, obj types.Object) bool {
	switch obj.(type) {
	case *types.Func, *types.Var:
	default:
		return false
	}
	return n.holds(p) || n.namer(obj.Name()) != ""
}

// namer is the first source in another language than Go under the target root in
// which a name occurs as a maximal run of identifier characters, or else the
// first entry the search could not read, and "" where neither is.
func (n *nonGoSources) namer(name string) string {
	n.scan()
	if file := n.tokens[name]; file != "" {
		return file
	}
	if len(n.unread) > 0 {
		return n.unread[0]
	}
	return ""
}

// unreadEntries is [nonGoSources.unread], or none where no class asked for the
// tree search.
func (in *Program) unreadEntries() []string {
	if in.nonGoSources == nil {
		return nil
	}
	return in.nonGoSources.unread
}

// scan reads every source in another language than Go under the target root once,
// skipping version control, vendored and node module trees and every directory
// holding a module file of its own that no package of the target lives under.
func (n *nonGoSources) scan() {
	if n.scanned {
		return
	}
	n.scanned = true
	n.tokens = make(map[string]string)
	if n.in.ReadDir == nil || n.in.Root == "" {
		return
	}
	dirs := n.packageDirs()
	root := filepath.Clean(n.in.Root)
	queue := []string{root}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		queue = append(queue, n.scanDir(dir, dir == root, dirs)...)
	}
}

// scanDir reads the non-Go sources of one directory and returns the directories
// below it the scan goes on into: none where the directory holds a module file of
// its own and is not the root, unless a package of the target lives under it. An
// entry it cannot read is recorded, and the scan goes on past it.
func (n *nonGoSources) scanDir(dir string, root bool, packageDirs []string) []string {
	entries, err := n.in.ReadDir(dir)
	if err != nil {
		n.failed(dir)
		return nil
	}
	if !root && holdsModule(entries) && !slices.ContainsFunc(packageDirs, func(d string) bool { return underDir(d, dir) }) {
		return nil
	}
	var below []string
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		switch {
		case entry.IsDir():
			if !slices.Contains(nonGoSkippedDirs[:], entry.Name()) {
				below = append(below, path)
			}
		case nonGoName(entry.Name()):
			n.scanFile(path, load.KindOf(path, entry.Type(), n.in.Stat))
		}
	}
	return below
}

// scanFile reads the identifiers of the non-Go source at path, of kind, recording it
// where it cannot be inspected or read.
func (n *nonGoSources) scanFile(path string, kind load.Kind) {
	if kind == load.Unread {
		n.failed(path)
		return
	}
	if kind != load.File {
		return
	}
	src, err := n.in.Read(path)
	if err != nil {
		n.failed(path)
		return
	}
	identifierRuns(src, path, n.tokens)
}

// packageDirs is the directory of every package of the target.
func (n *nonGoSources) packageDirs() []string {
	var dirs []string
	for _, p := range n.in.Facts.Packages() {
		if dir := packageDir(p); dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// holdsModule reports whether a directory listing holds a module file.
func holdsModule(entries []fs.DirEntry) bool {
	return slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() == "go.mod" && !e.IsDir() })
}

// underDir reports whether path is dir or lies under it.
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel) || path == dir
}

// identifierRuns adds every maximal run of letters, digits and underscores src
// holds, which is how a source in another language spells a Go name, the middle
// dot of an assembly symbol being no letter. A run already added keeps the file
// that first wrote it.
func identifierRuns(src []byte, file string, into map[string]string) {
	start := -1
	for i := 0; i < len(src); {
		r, size := utf8.DecodeRune(src[i:])
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			addRun(into, string(src[start:i]), file)
			start = -1
		}
		i += size
	}
	if start >= 0 {
		addRun(into, string(src[start:]), file)
	}
}

// addRun records one identifier run at the first file that writes it.
func addRun(into map[string]string, run, file string) {
	if into[run] == "" {
		into[run] = file
	}
}

// heldByNonGo reports whether rule N retains every declaration of one package of
// the target.
func (in *Program) heldByNonGo(p *packages.Package) bool {
	return in.Resolve.Inside(packageStart(p)) && in.nonGo().holds(p)
}

// packageStart is a position in one package's first parsed file, or no position.
func packageStart(p *packages.Package) token.Pos {
	if len(p.Syntax) == 0 {
		return token.NoPos
	}
	return p.Syntax[0].Package
}

// offsetOf is where pos is written, the key a declaration has in every reading of
// its file.
func (in *Program) offsetOf(pos token.Pos) inventory.Offset {
	return in.Resolve.Positions().Offset(pos)
}
